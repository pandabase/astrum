package tests

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/pandabase/astrum/internal/modules/ledger"
	"github.com/pandabase/astrum/internal/money"
)

func TestConcurrencyEdgeMixedWorkloadConservesMoney(t *testing.T) {
	t.Parallel()
	e := setup(t)
	ctx := context.Background()

	const (
		workers   = 12
		perWorker = 25
		seed      = 2_000
		overdraft = 500
	)
	var ids []uuid.UUID
	for range 4 {
		ids = append(ids, e.funded(t, seed).ID)
	}
	credit := e.account(t, "USD", ledger.Credit)
	e.post(t, peLegs("fund-credit", peLeg(credit.ID, ledger.Credit, seed), peLeg(e.open.ID, ledger.Debit, seed)))
	od := e.account(t, "USD", ledger.Debit, func(in *ledger.CreateAccountInput) { in.OverdraftLimit = amt(overdraft) })
	ids = append(ids, od.ID)

	move := func(key string, from, to uuid.UUID, n int64) ledger.PostInput {
		return transfer(key, from, to, n)
	}
	tolerated := func(err error) bool {
		return errors.Is(err, ledger.ErrInsufficientFunds) || errors.Is(err, ledger.ErrBatchAborted) || errors.Is(err, ledger.ErrAlreadyReversed)
	}

	var wg sync.WaitGroup
	failures := make(chan error, workers*perWorker*4)
	for w := range workers {
		wg.Go(func() {
			rng := rand.New(rand.NewPCG(uint64(w), 99))
			pick := func() (uuid.UUID, uuid.UUID) {
				from := ids[rng.IntN(len(ids))]
				to := ids[rng.IntN(len(ids))]
				for to == from {
					to = ids[rng.IntN(len(ids))]
				}
				return from, to
			}
			var mine []uuid.UUID
			for i := range perWorker {
				key := fmt.Sprintf("mix-%d-%d", w, i)
				from, to := pick()
				n := rng.Int64N(400) + 1
				var err error
				switch rng.IntN(6) {
				case 0:
					var txn ledger.Transaction
					if txn, err = e.m.Post(ctx, move(key, from, to, n)); err == nil {
						mine = append(mine, txn.ID)
					}
				case 1:
					var results []ledger.BatchResult
					f2, t2 := pick()
					results, err = e.m.PostBatch(ctx, []ledger.PostInput{move(key+"-a", from, to, n), move(key+"-b", f2, t2, n/2+1)}, rng.IntN(2) == 0)
					for _, r := range results {
						if r.Err != nil && !tolerated(r.Err) {
							failures <- fmt.Errorf("%s batch entry: %w", key, r.Err)
						}
					}
				case 2:
					var txn ledger.Transaction
					if txn, err = e.m.Post(ctx, pending(move(key, from, to, n))); err == nil {
						if rng.IntN(2) == 0 {
							_, err = e.m.PostTransaction(ctx, txn.ID, ledger.PostPendingInput{})
						} else {
							_, err = e.m.ArchiveTransaction(ctx, txn.ID)
						}
					}
				case 3:
					if len(mine) > 0 {
						_, err = e.m.Reverse(ctx, mine[rng.IntN(len(mine))], ledger.ReverseInput{IdempotencyKey: key})
					}
				case 4:
					_, err = e.m.Post(ctx, peLegs(key, peLeg(credit.ID, ledger.Debit, n), peLeg(from, ledger.Debit, n), peLeg(e.open.ID, ledger.Credit, 2*n)))
				default:
					_, err = e.m.Post(ctx, locked(move(key, from, to, n), from, availableAtLeast(0)))
					if errors.Is(err, ledger.ErrBalanceLock) {
						err = nil
					}
				}
				if err != nil && !tolerated(err) {
					failures <- fmt.Errorf("%s: %w", key, err)
				}
			}
		})
	}
	wg.Wait()
	close(failures)
	for err := range failures {
		t.Error(err)
	}

	accounts, err := e.m.ListAccounts(ctx, ledger.ListAccountsInput{LedgerID: e.ledger.ID, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	var postedNet, pendingNet money.Amount
	for _, acc := range accounts {
		signed := func(b ledger.Balance) money.Amount {
			net, err := b.Debits.Sub(b.Credits)
			if err != nil {
				t.Fatal(err)
			}
			return net
		}
		if postedNet, err = postedNet.Add(signed(acc.Posted)); err != nil {
			t.Fatal(err)
		}
		if pendingNet, err = pendingNet.Add(signed(acc.Pending)); err != nil {
			t.Fatal(err)
		}
		if acc.Pending != acc.Posted {
			t.Errorf("account %s has unresolved pending money: %+v", acc.Code, acc)
		}
		floor := amt(0)
		switch {
		case acc.AllowNegative:
			continue
		case acc.ID == od.ID:
			floor = amt(-overdraft)
		}
		if acc.Posted.Amount.Cmp(floor) < 0 || acc.Available.Amount.Cmp(floor) < 0 {
			t.Errorf("account %s below its floor %s: posted %s available %s", acc.Code, floor, acc.Posted.Amount, acc.Available.Amount)
		}
		lines, err := e.m.AccountEntries(ctx, acc.ID, 0, 1000)
		if err != nil {
			t.Fatal(err)
		}
		if len(lines) > 0 && lines[len(lines)-1].BalanceAfter != acc.Posted.Amount {
			t.Errorf("account %s statement ends at %s, balance is %s", acc.Code, lines[len(lines)-1].BalanceAfter, acc.Posted.Amount)
		}
	}
	if !postedNet.IsZero() || !pendingNet.IsZero() {
		t.Fatalf("money created or destroyed: posted net %s, pending net %s", postedNet, pendingNet)
	}
	e.verify(t)
}

func TestConcurrencyEdgeExactExhaustion(t *testing.T) {
	t.Parallel()
	e := setup(t)
	ctx := context.Background()
	a := e.funded(t, 1_000)
	sinks := make([]uuid.UUID, 4)
	for i := range sinks {
		sinks[i] = e.account(t, "USD", ledger.Debit).ID
	}

	const attempts = 60
	errs := make([]error, attempts)
	var wg sync.WaitGroup
	for i := range attempts {
		wg.Go(func() {
			in := transfer(fmt.Sprintf("drain-%d", i), a.ID, sinks[i%len(sinks)], 30)
			switch i % 3 {
			case 0:
				_, errs[i] = e.m.Post(ctx, in)
			case 1:
				_, errs[i] = e.m.Post(ctx, pending(in))
			default:
				var results []ledger.BatchResult
				if results, errs[i] = e.m.PostBatch(ctx, []ledger.PostInput{in}, true); errs[i] == nil {
					errs[i] = results[0].Err
				}
			}
		})
	}
	wg.Wait()

	accepted := 0
	for i, err := range errs {
		switch {
		case err == nil:
			accepted++
		case !errors.Is(err, ledger.ErrInsufficientFunds):
			t.Fatalf("attempt %d: %v", i, err)
		}
	}
	if accepted != 33 {
		t.Fatalf("accepted = %d, want exactly 33", accepted)
	}
	acc := e.get(t, a.ID)
	if acc.Available.Amount != amt(10) {
		t.Fatalf("available = %s, want 10", acc.Available.Amount)
	}
	var inbound int64
	for _, id := range sinks {
		inbound += small(t, e.get(t, id).Pending.Amount)
	}
	if inbound != 990 {
		t.Fatalf("sinks pending total = %d, want 990", inbound)
	}
	e.verify(t)
}

func TestConcurrencyEdgeOpposingTransfers(t *testing.T) {
	t.Parallel()
	e := setup(t)
	ctx := context.Background()
	a := e.funded(t, 500)
	b := e.funded(t, 500)

	const rounds = 40
	errs := make([]error, 2*rounds)
	var wg sync.WaitGroup
	for i := range rounds {
		wg.Go(func() { _, errs[2*i] = e.m.Post(ctx, transfer(fmt.Sprintf("ab-%d", i), a.ID, b.ID, 50)) })
		wg.Go(func() { _, errs[2*i+1] = e.m.Post(ctx, transfer(fmt.Sprintf("ba-%d", i), b.ID, a.ID, 50)) })
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil && !errors.Is(err, ledger.ErrInsufficientFunds) {
			t.Fatalf("transfer %d: %v", i, err)
		}
	}
	balA, balB := e.balance(t, a.ID), e.balance(t, b.ID)
	if balA+balB != 1_000 || balA < 0 || balB < 0 || balA%50 != 0 {
		t.Fatalf("a = %d, b = %d", balA, balB)
	}
	e.verify(t)
}
