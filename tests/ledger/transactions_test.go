package tests

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/pandabase/astrum/internal/modules/ledger"
	"github.com/pandabase/astrum/internal/money"
)

func TestCreateAccount(t *testing.T) {
	e := setup(t)
	ctx := context.Background()

	in := ledger.CreateAccountInput{LedgerID: e.ledger.ID, Code: "liabilities:customer:1", Currency: "USD", NormalSide: ledger.Credit, OverdraftLimit: amt(500)}
	acc, err := e.m.CreateAccount(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if acc.ID.Version() != 7 || acc.Code != in.Code || acc.OverdraftLimit != amt(500) || acc.Posted.Amount != amt(0) || acc.Version != 0 {
		t.Fatalf("CreateAccount() = %+v", acc)
	}

	t.Run("identical request replays", func(t *testing.T) {
		again, err := e.m.CreateAccount(ctx, in)
		if err != nil || again.ID != acc.ID {
			t.Fatalf("replay = %s, %v; want %s", again.ID, err, acc.ID)
		}
	})

	t.Run("same code different terms conflicts", func(t *testing.T) {
		changed := in
		changed.OverdraftLimit = amt(0)
		_, err := e.m.CreateAccount(ctx, changed)
		wantErr(t, err, ledger.ErrAccountExists)
	})

	t.Run("invalid", func(t *testing.T) {
		_, err := e.m.CreateAccount(ctx, ledger.CreateAccountInput{LedgerID: e.ledger.ID, Code: "x", Currency: "usd", NormalSide: ledger.Debit})
		wantErr(t, err, ledger.ErrInvalid)
	})

	t.Run("not found", func(t *testing.T) {
		_, err := e.m.Account(ctx, uuid.New())
		wantErr(t, err, ledger.ErrNotFound)
	})
}

func TestPostBalancesOnNormalSide(t *testing.T) {
	e := setup(t)

	cash := e.account(t, "USD", ledger.Debit, unrestricted)
	deposits := e.account(t, "USD", ledger.Credit)

	txn := e.post(t, ledger.PostInput{
		IdempotencyKey: "deposit-1",
		Description:    "customer deposit",
		Metadata:       jsontext.Value(`{"psp_ref": "ch_123", "amount_minor": 10000}`),
		Postings: []ledger.Posting{
			{AccountID: cash.ID, Side: ledger.Debit, Amount: amt(10_000)},
			{AccountID: deposits.ID, Side: ledger.Credit, Amount: amt(10_000)},
		},
	})
	if txn.ID.Version() != 7 || txn.CreatedAt.IsZero() || len(txn.Postings) != 2 {
		t.Fatalf("Post() = %+v", txn)
	}

	got, err := e.m.Transaction(context.Background(), txn.ID)
	if err != nil {
		t.Fatal(err)
	}
	var meta map[string]any
	if err := json.Unmarshal(got.Metadata, &meta); err != nil || meta["psp_ref"] != "ch_123" {
		t.Fatalf("metadata round trip = %s, %v", got.Metadata, err)
	}

	if b := e.balance(t, cash.ID); b != 10_000 {
		t.Errorf("cash = %d, want 10000", b)
	}
	if b := e.balance(t, deposits.ID); b != 10_000 {
		t.Errorf("deposits = %d, want 10000 (credit-normal reads positive)", b)
	}
	e.verify(t)
}

func TestPostIdempotency(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	a := e.funded(t, 5_000)
	b := e.account(t, "USD", ledger.Debit)

	in := transfer("pay-42", a.ID, b.ID, 1_000)
	in.Metadata = jsontext.Value(`{"order":"A1"}`)
	first := e.post(t, in)

	t.Run("replay returns original and applies once", func(t *testing.T) {
		for range 3 {
			again, err := e.m.Post(ctx, in)
			if err != nil || again.ID != first.ID {
				t.Fatalf("replay = %s, %v; want %s", again.ID, err, first.ID)
			}
		}
		if bal := e.balance(t, b.ID); bal != 1_000 {
			t.Fatalf("balance = %d, want 1000", bal)
		}
	})

	t.Run("metadata formatting does not break replay", func(t *testing.T) {
		reformatted := in
		reformatted.Metadata = jsontext.Value(`{ "order" : "A1" }`)
		if again, err := e.m.Post(ctx, reformatted); err != nil || again.ID != first.ID {
			t.Fatalf("replay = %s, %v", again.ID, err)
		}
	})

	t.Run("different payload conflicts", func(t *testing.T) {
		changed := transfer("pay-42", a.ID, b.ID, 1_001)
		changed.Metadata = in.Metadata
		_, err := e.m.Post(ctx, changed)
		wantErr(t, err, ledger.ErrIdempotencyConflict)
	})

	t.Run("rejected post does not consume key", func(t *testing.T) {
		_, err := e.m.Post(ctx, transfer("pay-43", a.ID, b.ID, 1_000_000))
		wantErr(t, err, ledger.ErrInsufficientFunds)
		e.post(t, transfer("pay-43", a.ID, b.ID, 5))
	})
}

func TestPostRejections(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	usdA := e.funded(t, 100)
	usdB := e.account(t, "USD", ledger.Debit)
	eur := e.account(t, "EUR", ledger.Debit, unrestricted)
	overdraft := e.account(t, "USD", ledger.Debit, func(in *ledger.CreateAccountInput) { in.OverdraftLimit = amt(50) })

	tests := []struct {
		name    string
		in      ledger.PostInput
		wantErr error
	}{
		{"insufficient funds", transfer("r1", usdA.ID, usdB.ID, 101), ledger.ErrInsufficientFunds},
		{"beyond overdraft", transfer("r2", overdraft.ID, usdB.ID, 51), ledger.ErrInsufficientFunds},
		{"unknown account", transfer("r3", usdA.ID, uuid.New(), 1), ledger.ErrNotFound},
		{"invalid", transfer("", usdA.ID, usdB.ID, 1), ledger.ErrInvalid},
		{"unbalanced", ledger.PostInput{IdempotencyKey: "r4", Postings: []ledger.Posting{
			{AccountID: usdB.ID, Side: ledger.Debit, Amount: amt(10)},
			{AccountID: usdA.ID, Side: ledger.Credit, Amount: amt(9)},
		}}, ledger.ErrUnbalanced},
		{"cross currency", ledger.PostInput{IdempotencyKey: "r5", Postings: []ledger.Posting{
			{AccountID: eur.ID, Side: ledger.Debit, Amount: amt(10)},
			{AccountID: usdA.ID, Side: ledger.Credit, Amount: amt(10)},
		}}, ledger.ErrUnbalanced},
		{"declared currency mismatch", ledger.PostInput{IdempotencyKey: "r6", Postings: []ledger.Posting{
			{AccountID: usdB.ID, Side: ledger.Debit, Amount: amt(10), Currency: "EUR"},
			{AccountID: usdA.ID, Side: ledger.Credit, Amount: amt(10)},
		}}, ledger.ErrInvalid},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := e.m.Post(ctx, tt.in)
			wantErr(t, err, tt.wantErr)
		})
	}

	e.post(t, transfer("ok-overdraft", overdraft.ID, usdB.ID, 50))
	if acc := e.get(t, overdraft.ID); acc.Posted.Amount != amt(-50) || acc.Available.Amount != amt(-50) {
		t.Fatalf("overdraft account = %+v, want balance -50", acc)
	}
	if b := e.balance(t, usdA.ID); b != 100 {
		t.Fatalf("usdA = %d after rejections, want 100", b)
	}
	e.verify(t)
}

func TestPostBalanceOverflow(t *testing.T) {
	e := setup(t)
	a := e.account(t, "USD", ledger.Debit, unrestricted)
	b := e.account(t, "USD", ledger.Debit, unrestricted)

	e.post(t, transferAmount("max", a.ID, b.ID, money.MaxAmount()))
	_, err := e.m.Post(context.Background(), transfer("over", a.ID, b.ID, 1))
	wantErr(t, err, money.ErrOverflow)
	if bal := e.get(t, b.ID).Posted.Amount; bal != money.MaxAmount() {
		t.Fatalf("balance = %s, want %s", bal, money.MaxAmount())
	}
	e.verify(t)
}

func TestBatchAtomic(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	a := e.funded(t, 1_000)
	b := e.account(t, "USD", ledger.Debit)
	c := e.account(t, "USD", ledger.Debit)

	t.Run("all or nothing", func(t *testing.T) {
		results, err := e.m.PostBatch(ctx, []ledger.PostInput{
			transfer("atomic-1", a.ID, b.ID, 400),
			transfer("atomic-2", a.ID, c.ID, 700),
		}, true)
		if err != nil {
			t.Fatal(err)
		}
		wantErr(t, results[0].Err, ledger.ErrBatchAborted)
		wantErr(t, results[1].Err, ledger.ErrInsufficientFunds)
		if bal := e.balance(t, a.ID); bal != 1_000 {
			t.Fatalf("a = %d after aborted batch, want 1000", bal)
		}

		e.post(t, transfer("atomic-1", a.ID, b.ID, 1))
	})

	t.Run("later entries see earlier ones", func(t *testing.T) {
		results, err := e.m.PostBatch(ctx, []ledger.PostInput{
			transfer("chain-1", a.ID, b.ID, 500),
			transfer("chain-2", b.ID, c.ID, 500),
		}, true)
		if err != nil {
			t.Fatal(err)
		}
		for i, r := range results {
			if r.Err != nil {
				t.Fatalf("result %d: %v", i, r.Err)
			}
		}
		if bal := e.balance(t, c.ID); bal != 500 {
			t.Fatalf("c = %d, want 500", bal)
		}
	})
	e.verify(t)
}

func TestBatchPartial(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	a := e.funded(t, 100)
	b := e.account(t, "USD", ledger.Debit)
	existing := e.post(t, transfer("seen", a.ID, b.ID, 10))

	results, err := e.m.PostBatch(ctx, []ledger.PostInput{
		transfer("p-ok", a.ID, b.ID, 50),
		transfer("p-broke", a.ID, b.ID, 1_000),
		transfer("", a.ID, b.ID, 1),
		transfer("seen", a.ID, b.ID, 10),
		transfer("p-dup", a.ID, b.ID, 5),
		transfer("p-dup", a.ID, b.ID, 5),
		transfer("p-dup", a.ID, b.ID, 6),
	}, false)
	if err != nil {
		t.Fatal(err)
	}

	checks := []struct {
		replayed bool
		err      error
	}{
		{false, nil},
		{false, ledger.ErrInsufficientFunds},
		{false, ledger.ErrInvalid},
		{true, nil},
		{false, nil},
		{true, nil},
		{false, ledger.ErrIdempotencyConflict},
	}
	for i, want := range checks {
		r := results[i]
		wantErr(t, r.Err, want.err)
		if r.Err == nil && r.Replayed != want.replayed {
			t.Errorf("result %d replayed = %v, want %v", i, r.Replayed, want.replayed)
		}
	}
	if results[3].Transaction.ID != existing.ID || results[5].Transaction.ID != results[4].Transaction.ID {
		t.Fatal("replays must return the original transactions")
	}
	if bal := e.balance(t, b.ID); bal != 65 {
		t.Fatalf("b = %d, want 65", bal)
	}
	e.verify(t)
}

func TestBatchLimits(t *testing.T) {
	e := setup(t)
	_, err := e.m.PostBatch(context.Background(), nil, true)
	wantErr(t, err, ledger.ErrInvalid)

	too := make([]ledger.PostInput, 1001)
	_, err = e.m.PostBatch(context.Background(), too, false)
	wantErr(t, err, ledger.ErrInvalid)
}

func TestReverse(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	a := e.funded(t, 1_000)
	b := e.account(t, "USD", ledger.Debit)
	original := e.post(t, transfer("pay", a.ID, b.ID, 300))

	rev, err := e.m.Reverse(ctx, original.ID, ledger.ReverseInput{IdempotencyKey: "refund", Description: "refund"})
	if err != nil {
		t.Fatal(err)
	}
	if rev.ReversesID == nil || *rev.ReversesID != original.ID {
		t.Fatalf("reverses_id = %v, want %s", rev.ReversesID, original.ID)
	}
	if e.balance(t, a.ID) != 1_000 || e.balance(t, b.ID) != 0 {
		t.Fatal("reversal did not restore balances")
	}

	t.Run("replay", func(t *testing.T) {
		again, err := e.m.Reverse(ctx, original.ID, ledger.ReverseInput{IdempotencyKey: "refund", Description: "refund"})
		if err != nil || again.ID != rev.ID {
			t.Fatalf("replay = %s, %v", again.ID, err)
		}
	})

	t.Run("second reversal rejected", func(t *testing.T) {
		_, err := e.m.Reverse(ctx, original.ID, ledger.ReverseInput{IdempotencyKey: "refund-2"})
		wantErr(t, err, ledger.ErrAlreadyReversed)
	})

	t.Run("key owned by another transaction", func(t *testing.T) {
		other := e.post(t, transfer("pay-2", a.ID, b.ID, 1))
		_, err := e.m.Reverse(ctx, other.ID, ledger.ReverseInput{IdempotencyKey: "pay"})
		wantErr(t, err, ledger.ErrIdempotencyConflict)
	})

	t.Run("reversal respects funds", func(t *testing.T) {
		paid := e.post(t, transfer("pay-3", a.ID, b.ID, 200))
		e.post(t, transfer("spend", b.ID, a.ID, 201))
		_, err := e.m.Reverse(ctx, paid.ID, ledger.ReverseInput{IdempotencyKey: "refund-3"})
		wantErr(t, err, ledger.ErrInsufficientFunds)
	})

	t.Run("unknown transaction", func(t *testing.T) {
		_, err := e.m.Reverse(ctx, uuid.New(), ledger.ReverseInput{IdempotencyKey: "x"})
		wantErr(t, err, ledger.ErrNotFound)
	})
	e.verify(t)
}

func TestStatement(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	deposits := e.account(t, "USD", ledger.Credit)

	for i := range 5 {
		e.post(t, ledger.PostInput{
			IdempotencyKey: fmt.Sprintf("dep-%d", i),
			Postings: []ledger.Posting{
				{AccountID: e.open.ID, Side: ledger.Debit, Amount: amt(100)},
				{AccountID: deposits.ID, Side: ledger.Credit, Amount: amt(100)},
			},
		})
	}

	page1, err := e.m.AccountEntries(ctx, deposits.ID, 0, 3)
	if err != nil {
		t.Fatal(err)
	}
	page2, err := e.m.AccountEntries(ctx, deposits.ID, page1[len(page1)-1].PostingID, 3)
	if err != nil {
		t.Fatal(err)
	}
	lines := append(page1, page2...)
	if len(page1) != 3 || len(page2) != 2 {
		t.Fatalf("pages = %d + %d, want 3 + 2", len(page1), len(page2))
	}
	for i, line := range lines {
		if want := amt(int64(100 * (i + 1))); line.BalanceAfter != want {
			t.Errorf("line %d balance_after = %s, want %s (normal side, running)", i, line.BalanceAfter, want)
		}
	}

	_, err = e.m.AccountEntries(ctx, deposits.ID, 0, 0)
	wantErr(t, err, ledger.ErrInvalid)
	_, err = e.m.AccountEntries(ctx, uuid.New(), 0, 10)
	wantErr(t, err, ledger.ErrNotFound)
}
