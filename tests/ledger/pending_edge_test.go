package tests

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/pandabase/astrum/internal/modules/ledger"
	"github.com/pandabase/astrum/internal/money"
)

func TestPendingEdgeReservationBoundary(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	a := e.funded(t, 100)
	b := e.account(t, "USD", ledger.Debit)

	whole := e.post(t, pending(transfer("reserve-all", a.ID, b.ID, 100)))
	if got := peViews(t, e, a.ID); got != [3]money.Amount{amt(100), amt(0), amt(0)} {
		t.Fatalf("views = %v", got)
	}

	for name, in := range map[string]ledger.PostInput{
		"posted spend of one":  transfer("over-posted", a.ID, b.ID, 1),
		"pending spend of one": pending(transfer("over-pending", a.ID, b.ID, 1)),
	} {
		t.Run(name+" beyond the reservation", func(t *testing.T) {
			_, err := e.m.Post(ctx, in)
			wantErr(t, err, ledger.ErrInsufficientFunds)
		})
	}

	t.Run("posting a fully reserved pending never fails for funds", func(t *testing.T) {
		posted, err := e.m.PostTransaction(ctx, whole.ID, ledger.PostPendingInput{})
		if err != nil || posted.Status != ledger.TransactionPosted {
			t.Fatalf("post = %+v, %v", posted, err)
		}
		if got := peViews(t, e, a.ID); got != [3]money.Amount{amt(0), amt(0), amt(0)} {
			t.Fatalf("a views = %v", got)
		}
		if got := peViews(t, e, b.ID); got != [3]money.Amount{amt(100), amt(100), amt(100)} {
			t.Fatalf("b views = %v", got)
		}
	})

	t.Run("pending inflow is not spendable until posted", func(t *testing.T) {
		c := e.account(t, "USD", ledger.Debit)
		in := e.post(t, pending(transfer("inflow", b.ID, c.ID, 40)))
		_, err := e.m.Post(ctx, transfer("spend-inflow", c.ID, b.ID, 1))
		wantErr(t, err, ledger.ErrInsufficientFunds)
		if _, err := e.m.PostTransaction(ctx, in.ID, ledger.PostPendingInput{}); err != nil {
			t.Fatal(err)
		}
		e.post(t, transfer("spend-inflow", c.ID, b.ID, 40))
	})

	t.Run("archive releases exactly the reservation", func(t *testing.T) {
		held := e.post(t, pending(transfer("release", b.ID, a.ID, 60)))
		if got := e.get(t, b.ID).Available.Amount; got != amt(40) {
			t.Fatalf("available = %s", got)
		}
		if _, err := e.m.ArchiveTransaction(ctx, held.ID); err != nil {
			t.Fatal(err)
		}
		if got := peViews(t, e, b.ID); got != [3]money.Amount{amt(100), amt(100), amt(100)} {
			t.Fatalf("b views after archive = %v", got)
		}
		if got := peViews(t, e, a.ID); got != [3]money.Amount{amt(0), amt(0), amt(0)} {
			t.Fatalf("a views after archive = %v", got)
		}
	})
	e.verify(t)
}

func TestPendingEdgeCreditNormal(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	liability := e.account(t, "USD", ledger.Credit)
	e.post(t, peLegs("fund", peLeg(liability.ID, ledger.Credit, 100), peLeg(e.open.ID, ledger.Debit, 100)))

	txn := e.post(t, pending(peLegs("draw", peLeg(liability.ID, ledger.Debit, 70), peLeg(e.open.ID, ledger.Credit, 70))))
	acc := e.get(t, liability.ID)
	wantBalance(t, "posted", acc.Posted, 0, 100, 100)
	wantBalance(t, "pending", acc.Pending, 70, 100, 30)
	wantBalance(t, "available", acc.Available, 70, 100, 30)

	_, err := e.m.Post(ctx, peLegs("draw-more", peLeg(liability.ID, ledger.Debit, 31), peLeg(e.open.ID, ledger.Credit, 31)))
	wantErr(t, err, ledger.ErrInsufficientFunds)

	partial := []ledger.Posting{peLeg(liability.ID, ledger.Debit, 25), peLeg(e.open.ID, ledger.Credit, 25)}
	if _, err := e.m.PostTransaction(ctx, txn.ID, ledger.PostPendingInput{Postings: partial}); err != nil {
		t.Fatal(err)
	}
	acc = e.get(t, liability.ID)
	wantBalance(t, "posted after partial", acc.Posted, 25, 100, 75)
	wantBalance(t, "available after partial", acc.Available, 25, 100, 75)
	e.verify(t)
}

func TestPendingEdgeStateTransitions(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	a := e.funded(t, 1_000)
	b := e.account(t, "USD", ledger.Debit)

	t.Run("posting a directly posted transaction is a no-op", func(t *testing.T) {
		direct := e.post(t, transfer("direct", a.ID, b.ID, 10))
		got, err := e.m.PostTransaction(ctx, direct.ID, ledger.PostPendingInput{})
		if err != nil || got.ID != direct.ID || got.Version != direct.Version {
			t.Fatalf("PostTransaction(direct) = %+v, %v", got, err)
		}
		_, err = e.m.PostTransaction(ctx, direct.ID, ledger.PostPendingInput{Postings: transfer("", a.ID, b.ID, 5).Postings})
		wantErr(t, err, ledger.ErrNotPending)
		_, err = e.m.ArchiveTransaction(ctx, direct.ID)
		wantErr(t, err, ledger.ErrNotPending)
		if got := e.balance(t, b.ID); got != 10 {
			t.Fatalf("b = %d, want 10", got)
		}
	})

	t.Run("unknown transaction", func(t *testing.T) {
		id := uuid.New()
		_, err := e.m.PostTransaction(ctx, id, ledger.PostPendingInput{})
		wantErr(t, err, ledger.ErrNotFound)
		_, err = e.m.ArchiveTransaction(ctx, id)
		wantErr(t, err, ledger.ErrNotFound)
		_, err = e.m.UpdateTransaction(ctx, id, ledger.UpdateTransactionInput{Description: str("x")})
		wantErr(t, err, ledger.ErrNotFound)
	})

	t.Run("partial post with invalid entries", func(t *testing.T) {
		txn := e.post(t, pending(transfer("partial-invalid", a.ID, b.ID, 50)))
		for name, postings := range map[string][]ledger.Posting{
			"single leg":   {peLeg(b.ID, ledger.Debit, 1)},
			"zero amounts": {peLeg(b.ID, ledger.Debit, 0), peLeg(a.ID, ledger.Credit, 0)},
		} {
			_, err := e.m.PostTransaction(ctx, txn.ID, ledger.PostPendingInput{Postings: postings})
			if err == nil {
				t.Fatalf("%s: partial post accepted", name)
			}
			wantErr(t, err, ledger.ErrInvalid)
		}
		if acc := e.get(t, a.ID); acc.Available.Amount != amt(940) {
			t.Fatalf("a available = %s, want 940", acc.Available.Amount)
		}
		if _, err := e.m.ArchiveTransaction(ctx, txn.ID); err != nil {
			t.Fatal(err)
		}
	})

	frozen := e.account(t, "USD", ledger.Debit, unrestricted)
	other, err := e.m.CreateLedger(ctx, ledger.CreateLedgerInput{Name: "other"})
	if err != nil {
		t.Fatal(err)
	}
	foreign := e.accountIn(t, other.ID)
	if _, err := e.m.FreezeAccount(ctx, frozen.ID); err != nil {
		t.Fatal(err)
	}

	t.Run("failed updates keep the reservation intact", func(t *testing.T) {
		txn := e.post(t, pending(transfer("update-guard", a.ID, b.ID, 100)))
		before := peViews(t, e, a.ID)
		for name, tt := range map[string]struct {
			postings []ledger.Posting
			want     error
		}{
			"unbalanced":      {[]ledger.Posting{peLeg(b.ID, ledger.Debit, 100), peLeg(a.ID, ledger.Credit, 99)}, ledger.ErrUnbalanced},
			"cross ledger":    {[]ledger.Posting{peLeg(foreign.ID, ledger.Debit, 100), peLeg(a.ID, ledger.Credit, 100)}, ledger.ErrCrossLedger},
			"frozen account":  {[]ledger.Posting{peLeg(frozen.ID, ledger.Debit, 100), peLeg(a.ID, ledger.Credit, 100)}, ledger.ErrAccountNotOpen},
			"unknown account": {[]ledger.Posting{peLeg(uuid.New(), ledger.Debit, 100), peLeg(a.ID, ledger.Credit, 100)}, ledger.ErrNotFound},
			"single leg":      {[]ledger.Posting{peLeg(b.ID, ledger.Debit, 100)}, ledger.ErrInvalid},
			"over funds":      {transfer("", a.ID, b.ID, 1_000).Postings, ledger.ErrInsufficientFunds},
		} {
			_, err := e.m.UpdateTransaction(ctx, txn.ID, ledger.UpdateTransactionInput{Postings: tt.postings})
			if err == nil {
				t.Fatalf("%s: update accepted", name)
			}
			wantErr(t, err, tt.want)
		}
		if after := peViews(t, e, a.ID); after != before {
			t.Fatalf("views moved: %v -> %v", before, after)
		}
		current, err := e.m.Transaction(ctx, txn.ID)
		if err != nil || current.Version != txn.Version || current.Postings[0].Amount != amt(100) {
			t.Fatalf("transaction after failed updates = %+v, %v", current, err)
		}
		if _, err := e.m.ArchiveTransaction(ctx, txn.ID); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("receiver frozen after creation blocks posting but not archiving", func(t *testing.T) {
		c := e.account(t, "USD", ledger.Debit)
		first := e.post(t, pending(transfer("frozen-recv-1", a.ID, c.ID, 5)))
		second := e.post(t, pending(transfer("frozen-recv-2", a.ID, c.ID, 5)))
		if _, err := e.m.FreezeAccount(ctx, c.ID); err != nil {
			t.Fatal(err)
		}
		_, err := e.m.PostTransaction(ctx, first.ID, ledger.PostPendingInput{})
		wantErr(t, err, ledger.ErrAccountNotOpen)
		if _, err := e.m.ArchiveTransaction(ctx, second.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := e.m.UnfreezeAccount(ctx, c.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := e.m.PostTransaction(ctx, first.ID, ledger.PostPendingInput{}); err != nil {
			t.Fatal(err)
		}
		if got := peViews(t, e, c.ID); got != [3]money.Amount{amt(5), amt(5), amt(5)} {
			t.Fatalf("c views = %v", got)
		}
	})

	if acc := e.get(t, a.ID); acc.Pending != acc.Posted {
		t.Fatalf("pending money left behind: %+v", acc)
	}
	e.verify(t)
}

func TestPendingEdgeConcurrentResolution(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	a := e.funded(t, 1_000)
	b := e.account(t, "USD", ledger.Debit)
	const racers = 10

	for _, op := range []string{"post", "archive"} {
		t.Run(op, func(t *testing.T) {
			txn := e.post(t, pending(transfer("race-"+op, a.ID, b.ID, 100)))
			results := make([]ledger.Transaction, racers)
			errs := make([]error, racers)
			var wg sync.WaitGroup
			for i := range racers {
				wg.Go(func() {
					if op == "post" {
						results[i], errs[i] = e.m.PostTransaction(ctx, txn.ID, ledger.PostPendingInput{})
						return
					}
					results[i], errs[i] = e.m.ArchiveTransaction(ctx, txn.ID)
				})
			}
			wg.Wait()
			for i := range racers {
				if errs[i] != nil {
					t.Fatalf("racer %d: %v", i, errs[i])
				}
				if results[i].Version != txn.Version+1 {
					t.Fatalf("racer %d version = %d, want %d", i, results[i].Version, txn.Version+1)
				}
			}
		})
	}

	if got := peViews(t, e, a.ID); got != [3]money.Amount{amt(900), amt(900), amt(900)} {
		t.Fatalf("a views = %v, want 900 everywhere", got)
	}
	if got := e.balance(t, b.ID); got != 100 {
		t.Fatalf("b = %d, want 100 (posted exactly once)", got)
	}
	e.verify(t)
}

func TestPendingEdgeManyPendingsExhaustFunds(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	a := e.funded(t, 100)
	b := e.account(t, "USD", ledger.Debit)

	const racers = 25
	errs := make([]error, racers)
	txns := make([]ledger.Transaction, racers)
	var wg sync.WaitGroup
	for i := range racers {
		wg.Go(func() { txns[i], errs[i] = e.m.Post(ctx, pending(transfer(fmt.Sprintf("many-%d", i), a.ID, b.ID, 7))) })
	}
	wg.Wait()
	accepted := 0
	for i, err := range errs {
		switch {
		case err == nil:
			accepted++
		case !errors.Is(err, ledger.ErrInsufficientFunds):
			t.Fatalf("racer %d: %v", i, err)
		}
	}
	if accepted != 14 {
		t.Fatalf("accepted = %d, want 14", accepted)
	}
	if got := e.get(t, a.ID).Available.Amount; got != amt(2) {
		t.Fatalf("available = %s, want 2", got)
	}
	for i, err := range errs {
		if err != nil {
			continue
		}
		if i%2 == 0 {
			_, err = e.m.PostTransaction(ctx, txns[i].ID, ledger.PostPendingInput{})
		} else {
			_, err = e.m.ArchiveTransaction(ctx, txns[i].ID)
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	acc := e.get(t, a.ID)
	if acc.Pending != acc.Posted || acc.Available.Amount != acc.Posted.Amount || acc.Posted.Amount.Sign() < 0 {
		t.Fatalf("a = %+v", acc)
	}
	if total := small(t, acc.Posted.Amount) + e.balance(t, b.ID); total != 100 {
		t.Fatalf("a + b = %d, want 100", total)
	}
	e.verify(t)
}
