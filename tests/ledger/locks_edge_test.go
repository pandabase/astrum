package tests

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/pandabase/astrum/internal/modules/ledger"
	"github.com/pandabase/astrum/internal/money"
)

func peCondition(op string, v money.Amount) *ledger.BalanceCondition {
	c := &ledger.BalanceCondition{}
	switch op {
	case "gt":
		c.GT = &v
	case "gte":
		c.GTE = &v
	case "eq":
		c.EQ = &v
	case "not_eq":
		c.NotEQ = &v
	case "lt":
		c.LT = &v
	case "lte":
		c.LTE = &v
	}
	return c
}

func TestLocksEdgeOperatorMatrix(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	a := e.account(t, "USD", ledger.Debit)
	b := e.account(t, "USD", ledger.Debit)
	e.post(t, transfer("fund", e.open.ID, a.ID, 100))
	e.post(t, pending(transfer("pending-out", a.ID, b.ID, 30)))
	e.post(t, pending(transfer("pending-in", e.open.ID, a.ID, 20)))
	want := [3]money.Amount{amt(100), amt(90), amt(70)}
	if got := peViews(t, e, a.ID); got != want {
		t.Fatalf("views = %v, want %v", got, want)
	}

	views := []struct {
		name  string
		value money.Amount
		set   func(*ledger.Posting, *ledger.BalanceCondition)
	}{
		{"posted", want[0], func(p *ledger.Posting, c *ledger.BalanceCondition) { p.PostedBalance = c }},
		{"pending", want[1], func(p *ledger.Posting, c *ledger.BalanceCondition) { p.PendingBalance = c }},
		{"available", want[2], func(p *ledger.Posting, c *ledger.BalanceCondition) { p.AvailableBalance = c }},
	}
	holds := map[string][3]bool{
		"gt":     {true, false, false},
		"gte":    {true, true, false},
		"eq":     {false, true, false},
		"not_eq": {true, false, true},
		"lt":     {false, false, true},
		"lte":    {false, true, true},
	}
	relations := []struct {
		name   string
		offset int64
	}{{"above", -1}, {"at", 0}, {"below", 1}}

	versionBefore := e.get(t, a.ID).Version
	accepted := int64(0)
	for _, view := range views {
		for _, op := range []string{"gt", "gte", "eq", "not_eq", "lt", "lte"} {
			for r, rel := range relations {
				t.Run(fmt.Sprintf("%s %s value %s bound", view.name, op, rel.name), func(t *testing.T) {
					bound, err := view.value.Add(amt(rel.offset))
					if err != nil {
						t.Fatal(err)
					}
					lockLeg := peLeg(a.ID, ledger.Debit, 1)
					view.set(&lockLeg, peCondition(op, bound))
					in := peLegs(fmt.Sprintf("lock-%s-%s-%s", view.name, op, rel.name), lockLeg, peLeg(a.ID, ledger.Credit, 1))
					txn, err := e.m.Post(ctx, in)
					if !holds[op][r] {
						wantErr(t, err, ledger.ErrBalanceLock)
						return
					}
					if err != nil {
						t.Fatalf("Post() error = %v", err)
					}
					accepted++
					got := txn.Postings[0].Resulting
					if got == nil || got.Posted.Amount != want[0] || got.Pending.Amount != want[1] || got.Available.Amount != want[2] {
						t.Fatalf("resulting = %+v", got)
					}
				})
			}
		}
	}
	if got := peViews(t, e, a.ID); got != want {
		t.Fatalf("views = %v after lock matrix, want %v", got, want)
	}
	if got := e.get(t, a.ID).Version; got != versionBefore+accepted {
		t.Fatalf("version = %d, want %d (only accepted transactions bump it)", got, versionBefore+accepted)
	}
	e.verify(t)
}

func TestLocksEdgeSignsAndCombinations(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	a := e.funded(t, 100)
	b := e.account(t, "USD", ledger.Debit)
	credit := e.account(t, "USD", ledger.Credit)
	negative := e.account(t, "USD", ledger.Debit, unrestricted)
	e.post(t, peLegs("fund-credit", peLeg(credit.ID, ledger.Credit, 50), peLeg(e.open.ID, ledger.Debit, 50)))
	e.post(t, transfer("dig", negative.ID, b.ID, 70))

	withLock := func(key string, legs []ledger.Posting, i int, set func(*ledger.Posting)) ledger.PostInput {
		set(&legs[i])
		return ledger.PostInput{IdempotencyKey: key, Postings: legs}
	}
	available := func(c *ledger.BalanceCondition) func(*ledger.Posting) {
		return func(p *ledger.Posting) { p.AvailableBalance = c }
	}
	posted := func(c *ledger.BalanceCondition) func(*ledger.Posting) {
		return func(p *ledger.Posting) { p.PostedBalance = c }
	}

	tests := []struct {
		name string
		in   ledger.PostInput
		want error
	}{
		{"credit normal drained to exactly zero", withLock("credit-zero",
			[]ledger.Posting{peLeg(credit.ID, ledger.Debit, 50), peLeg(e.open.ID, ledger.Credit, 50)}, 0,
			available(&ledger.BalanceCondition{EQ: bound(0)})), nil},
		{"negative balance at negative bound", withLock("neg-at",
			[]ledger.Posting{peLeg(negative.ID, ledger.Debit, 1), peLeg(negative.ID, ledger.Credit, 1)}, 0,
			posted(&ledger.BalanceCondition{GTE: bound(-70)})), nil},
		{"negative balance below negative bound", withLock("neg-below",
			[]ledger.Posting{peLeg(negative.ID, ledger.Debit, 1), peLeg(negative.ID, ledger.Credit, 1)}, 0,
			posted(&ledger.BalanceCondition{GTE: bound(-69)})), ledger.ErrBalanceLock},
		{"inclusive range at upper edge", withLock("range-in",
			transfer("", a.ID, b.ID, 10).Postings, 0,
			posted(&ledger.BalanceCondition{GTE: bound(0), LTE: bound(80)})), nil},
		{"exclusive range at upper edge", withLock("range-out",
			transfer("", a.ID, b.ID, 10).Postings, 0,
			posted(&ledger.BalanceCondition{GT: bound(0), LT: bound(80)})), ledger.ErrBalanceLock},
		{"contradictory bounds never hold", withLock("contradiction",
			transfer("", a.ID, b.ID, 1).Postings, 1,
			posted(&ledger.BalanceCondition{GT: bound(10), LT: bound(10)})), ledger.ErrBalanceLock},
		{"one failing view fails the posting", withLock("multi-view",
			transfer("", a.ID, b.ID, 1).Postings, 1, func(p *ledger.Posting) {
				p.PostedBalance = &ledger.BalanceCondition{GTE: bound(0)}
				p.PendingBalance = &ledger.BalanceCondition{GTE: bound(0)}
				p.AvailableBalance = &ledger.BalanceCondition{GTE: bound(1_000)}
			}), ledger.ErrBalanceLock},
		{"counterparty lock fails", withLock("counterparty",
			transfer("", a.ID, b.ID, 1).Postings, 0,
			posted(&ledger.BalanceCondition{LTE: bound(70)})), ledger.ErrBalanceLock},
		{"empty condition always holds", withLock("empty-cond",
			transfer("", a.ID, b.ID, 1).Postings, 1,
			available(&ledger.BalanceCondition{})), nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before := [2][3]money.Amount{peViews(t, e, tt.in.Postings[0].AccountID), peViews(t, e, tt.in.Postings[1].AccountID)}
			_, err := e.m.Post(ctx, tt.in)
			wantErr(t, err, tt.want)
			if err != nil {
				after := [2][3]money.Amount{peViews(t, e, tt.in.Postings[0].AccountID), peViews(t, e, tt.in.Postings[1].AccountID)}
				if after != before {
					t.Fatalf("rejected lock moved money: %v -> %v", before, after)
				}
			}
		})
	}

	t.Run("repeated legs are locked on the final balance", func(t *testing.T) {
		acc := e.funded(t, 10)
		in := peLegs("repeat-lock",
			ledger.Posting{AccountID: acc.ID, Side: ledger.Credit, Amount: amt(10), AvailableBalance: &ledger.BalanceCondition{EQ: bound(5)}},
			peLeg(b.ID, ledger.Debit, 10),
			peLeg(acc.ID, ledger.Debit, 5),
			peLeg(e.open.ID, ledger.Credit, 5))
		txn := e.post(t, in)
		if txn.Postings[0].Resulting.Available.Amount != amt(5) || txn.Postings[2].Resulting.Available.Amount != amt(5) {
			t.Fatalf("resulting = %+v / %+v", txn.Postings[0].Resulting, txn.Postings[2].Resulting)
		}
	})

	t.Run("locks inside a batch see earlier entries", func(t *testing.T) {
		acc := e.funded(t, 50)
		results, err := e.m.PostBatch(ctx, []ledger.PostInput{
			transfer("batch-spend", acc.ID, b.ID, 20),
			locked(transfer("batch-locked", acc.ID, b.ID, 1), acc.ID, func(p *ledger.Posting) {
				p.PostedBalance = &ledger.BalanceCondition{EQ: bound(29)}
			}),
		}, true)
		if err != nil || results[0].Err != nil || results[1].Err != nil {
			t.Fatalf("batch = %+v, %v", results, err)
		}
	})

	t.Run("pending transaction locks see its own reservation", func(t *testing.T) {
		acc := e.funded(t, 100)
		in := pending(locked(transfer("pending-lock", acc.ID, b.ID, 10), acc.ID, func(p *ledger.Posting) {
			p.PostedBalance = &ledger.BalanceCondition{EQ: bound(100)}
			p.PendingBalance = &ledger.BalanceCondition{EQ: bound(90)}
			p.AvailableBalance = &ledger.BalanceCondition{EQ: bound(90)}
		}))
		e.post(t, in)
	})
	e.verify(t)
}

func TestLocksEdgeLockVersion(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	a := e.funded(t, 100)
	b := e.account(t, "USD", ledger.Debit)
	at := func(v int64) func(*ledger.Posting) { return func(p *ledger.Posting) { p.LockVersion = &v } }

	t.Run("fresh account is at version zero", func(t *testing.T) {
		if v := e.get(t, b.ID).Version; v != 0 {
			t.Fatalf("version = %d", v)
		}
		e.post(t, locked(transfer("v-fresh", a.ID, b.ID, 1), b.ID, at(0)))
		if v := e.get(t, b.ID).Version; v != 1 {
			t.Fatalf("version after one transaction = %d, want 1", v)
		}
	})

	t.Run("future and extreme versions fail", func(t *testing.T) {
		current := e.get(t, a.ID).Version
		for _, v := range []int64{current + 1, math.MaxInt64} {
			_, err := e.m.Post(ctx, locked(transfer(fmt.Sprintf("v-future-%d", v), a.ID, b.ID, 1), a.ID, at(v)))
			wantErr(t, err, ledger.ErrLockVersion)
		}
	})

	t.Run("rejected transactions do not bump the version", func(t *testing.T) {
		current := e.get(t, a.ID).Version
		_, err := e.m.Post(ctx, transfer("v-broke", a.ID, b.ID, 1_000))
		wantErr(t, err, ledger.ErrInsufficientFunds)
		_, err = e.m.Post(ctx, locked(transfer("v-lockfail", a.ID, b.ID, 1), a.ID, availableAtLeast(1_000)))
		wantErr(t, err, ledger.ErrBalanceLock)
		e.post(t, locked(transfer("v-after-reject", a.ID, b.ID, 1), a.ID, at(current)))
	})

	t.Run("pending create and archive each bump the version", func(t *testing.T) {
		start := e.get(t, a.ID).Version
		txn := e.post(t, pending(transfer("v-pending", a.ID, b.ID, 1)))
		if v := e.get(t, a.ID).Version; v != start+1 {
			t.Fatalf("version after pending = %d, want %d", v, start+1)
		}
		if _, err := e.m.ArchiveTransaction(ctx, txn.ID); err != nil {
			t.Fatal(err)
		}
		_, err := e.m.Post(ctx, locked(transfer("v-stale-archive", a.ID, b.ID, 1), a.ID, at(start+1)))
		wantErr(t, err, ledger.ErrLockVersion)
		e.post(t, locked(transfer("v-current-archive", a.ID, b.ID, 1), a.ID, at(start+2)))
	})

	t.Run("repeated legs compare against the pre-transaction version", func(t *testing.T) {
		current := e.get(t, a.ID).Version
		same := peLegs("v-repeat",
			ledger.Posting{AccountID: a.ID, Side: ledger.Debit, Amount: amt(1), LockVersion: new(current)},
			ledger.Posting{AccountID: a.ID, Side: ledger.Credit, Amount: amt(1), LockVersion: new(current)})
		e.post(t, same)
		split := peLegs("v-repeat-split",
			ledger.Posting{AccountID: a.ID, Side: ledger.Debit, Amount: amt(1), LockVersion: new(current + 1)},
			ledger.Posting{AccountID: a.ID, Side: ledger.Credit, Amount: amt(1), LockVersion: new(current + 2)})
		_, err := e.m.Post(ctx, split)
		wantErr(t, err, ledger.ErrLockVersion)
	})

	t.Run("only one writer wins a version", func(t *testing.T) {
		current := e.get(t, a.ID).Version
		const contenders = 20
		beforeTotal := e.balance(t, b.ID)
		errs := make([]error, contenders)
		var wg sync.WaitGroup
		for i := range contenders {
			wg.Go(func() {
				_, errs[i] = e.m.Post(ctx, locked(transfer(fmt.Sprintf("v-race-%d", i), a.ID, b.ID, 1), a.ID, at(current)))
			})
		}
		wg.Wait()
		won := 0
		for _, err := range errs {
			switch {
			case err == nil:
				won++
			case !errors.Is(err, ledger.ErrLockVersion):
				t.Fatalf("unexpected error: %v", err)
			}
		}
		if won != 1 {
			t.Fatalf("won %d, want exactly 1", won)
		}
		if got := e.balance(t, b.ID); got != beforeTotal+1 {
			t.Fatalf("b = %d, want %d", got, beforeTotal+1)
		}
	})
	e.verify(t)
}

func TestLocksEdgeArchiveOnFailure(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	a := e.funded(t, 100)
	b := e.account(t, "USD", ledger.Debit)
	failing := func(key string) ledger.PostInput {
		in := locked(transfer(key, a.ID, b.ID, 10), a.ID, availableAtLeast(1_000))
		in.ArchiveOnLockFailure = true
		return in
	}

	beforeA, beforeB := e.get(t, a.ID), e.get(t, b.ID)
	archived := e.post(t, failing("archive-me"))
	if archived.Status != ledger.TransactionArchived || archived.PostedAt != nil || archived.ArchivedAt == nil {
		t.Fatalf("archived = %+v", archived)
	}

	t.Run("no balance or version effect", func(t *testing.T) {
		afterA, afterB := e.get(t, a.ID), e.get(t, b.ID)
		if afterA.Posted != beforeA.Posted || afterA.Pending != beforeA.Pending || afterA.Available != beforeA.Available || afterA.Version != beforeA.Version {
			t.Fatalf("a changed: %+v -> %+v", beforeA, afterA)
		}
		if afterB.Posted != beforeB.Posted || afterB.Pending != beforeB.Pending || afterB.Version != beforeB.Version {
			t.Fatalf("b changed: %+v -> %+v", beforeB, afterB)
		}
		lines, err := e.m.AccountEntries(ctx, b.ID, 0, 10)
		if err != nil || len(lines) != 0 {
			t.Fatalf("b statement = %+v, %v", lines, err)
		}
	})

	t.Run("archived transaction is terminal", func(t *testing.T) {
		again, err := e.m.ArchiveTransaction(ctx, archived.ID)
		if err != nil || again.Status != ledger.TransactionArchived || again.Version != archived.Version {
			t.Fatalf("re-archive = %+v, %v", again, err)
		}
		_, err = e.m.PostTransaction(ctx, archived.ID, ledger.PostPendingInput{})
		wantErr(t, err, ledger.ErrNotPending)
		_, err = e.m.UpdateTransaction(ctx, archived.ID, ledger.UpdateTransactionInput{Description: new("x")})
		wantErr(t, err, ledger.ErrNotPending)
		_, err = e.m.Reverse(ctx, archived.ID, ledger.ReverseInput{IdempotencyKey: "reverse-archived"})
		wantErr(t, err, ledger.ErrNotPosted)
	})

	t.Run("key is consumed even without the flag", func(t *testing.T) {
		retry := failing("archive-me")
		retry.ArchiveOnLockFailure = false
		got, err := e.m.Post(ctx, retry)
		if err != nil || got.ID != archived.ID || got.Status != ledger.TransactionArchived {
			t.Fatalf("retry = %+v, %v", got, err)
		}
	})

	t.Run("pending request archived on lock failure reserves nothing", func(t *testing.T) {
		txn := e.post(t, pending(failing("archive-pending")))
		if txn.Status != ledger.TransactionArchived {
			t.Fatalf("status = %s", txn.Status)
		}
		if acc := e.get(t, a.ID); acc.Available.Amount != amt(100) || acc.Pending.Amount != amt(100) {
			t.Fatalf("a = %+v", acc)
		}
	})

	t.Run("archived entry does not abort an atomic batch", func(t *testing.T) {
		results, err := e.m.PostBatch(ctx, []ledger.PostInput{failing("archive-batch"), transfer("batch-ok", a.ID, b.ID, 5)}, true)
		if err != nil || results[0].Err != nil || results[1].Err != nil {
			t.Fatalf("batch = %+v, %v", results, err)
		}
		if results[0].Transaction.Status != ledger.TransactionArchived || results[1].Transaction.Status != ledger.TransactionPosted {
			t.Fatalf("statuses = %s, %s", results[0].Transaction.Status, results[1].Transaction.Status)
		}
	})

	t.Run("archived transaction owns its external_id", func(t *testing.T) {
		in := failing("archive-external")
		in.ExternalID = "ext-archived"
		e.post(t, in)
		taken := transfer("archive-external-2", a.ID, b.ID, 1)
		taken.ExternalID = "ext-archived"
		_, err := e.m.Post(ctx, taken)
		wantErr(t, err, ledger.ErrExternalIDExists)
	})

	frozen := e.account(t, "USD", ledger.Debit, unrestricted)
	if _, err := e.m.FreezeAccount(ctx, frozen.ID); err != nil {
		t.Fatal(err)
	}
	other, err := e.m.CreateLedger(ctx, ledger.CreateLedgerInput{Name: "other"})
	if err != nil {
		t.Fatal(err)
	}
	foreign := e.accountIn(t, other.ID)
	lockFails := &ledger.BalanceCondition{GTE: bound(1_000)}

	nonLock := []struct {
		name string
		legs []ledger.Posting
		want error
	}{
		{"frozen counterparty", []ledger.Posting{peLeg(frozen.ID, ledger.Debit, 1), peLeg(a.ID, ledger.Credit, 1)}, ledger.ErrAccountNotOpen},
		{"unbalanced", []ledger.Posting{peLeg(b.ID, ledger.Debit, 2), peLeg(a.ID, ledger.Credit, 1)}, ledger.ErrUnbalanced},
		{"unknown account", []ledger.Posting{peLeg(uuid.New(), ledger.Debit, 1), peLeg(a.ID, ledger.Credit, 1)}, ledger.ErrNotFound},
		{"cross ledger", []ledger.Posting{peLeg(foreign.ID, ledger.Debit, 1), peLeg(a.ID, ledger.Credit, 1)}, ledger.ErrCrossLedger},
		{"declared currency mismatch", []ledger.Posting{{AccountID: b.ID, Side: ledger.Debit, Amount: amt(1), Currency: "EUR"}, peLeg(a.ID, ledger.Credit, 1)}, ledger.ErrInvalid},
	}
	for _, tt := range nonLock {
		t.Run("balance lock failure does not mask "+tt.name, func(t *testing.T) {
			legs := append([]ledger.Posting(nil), tt.legs...)
			legs[1].AvailableBalance = lockFails
			in := ledger.PostInput{IdempotencyKey: "masked-balance-" + tt.name, ArchiveOnLockFailure: true, Postings: legs}
			_, err := e.m.Post(ctx, in)
			wantErr(t, err, tt.want)
		})
	}

	for _, tt := range nonLock {
		t.Run("lock_version failure does not mask "+tt.name, func(t *testing.T) {
			legs := append([]ledger.Posting(nil), tt.legs...)
			legs = append(legs[1:2], legs[0])
			legs[0].LockVersion = new(int64(math.MaxInt64))
			key := "masked-version-" + tt.name
			in := ledger.PostInput{IdempotencyKey: key, ArchiveOnLockFailure: true, Postings: legs}
			sibling := transfer("sibling-"+tt.name, a.ID, b.ID, 1)
			results, err := peBatchRecover(ctx, e, []ledger.PostInput{sibling, in}, false)
			if err != nil {
				t.Fatalf("PostBatch() error = %v, want per-entry results", err)
			}
			if results[0].Err != nil {
				t.Errorf("sibling error = %v", results[0].Err)
			}
			if !errors.Is(results[1].Err, tt.want) {
				t.Errorf("entry error = %v, want %v", results[1].Err, tt.want)
			}
			if results[1].Transaction != nil && results[1].Transaction.Status == ledger.TransactionArchived {
				t.Errorf("invalid transaction was archived instead of rejected")
			}
		})
	}
	e.verify(t)
}

func peBatchRecover(ctx context.Context, e *env, ins []ledger.PostInput, atomic bool) (results []ledger.BatchResult, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("PostBatch panicked: %v", r)
		}
	}()
	return e.m.PostBatch(ctx, ins, atomic)
}
