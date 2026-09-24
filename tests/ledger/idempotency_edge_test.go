package tests

import (
	"context"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pandabase/astrum/internal/modules/ledger"
)

func TestIdempotencyEdgeReplayedFlag(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	a := e.funded(t, 100)
	b := e.account(t, "USD", ledger.Debit)
	in := transfer("flagged", a.ID, b.ID, 10)

	first, err := e.m.PostBatch(ctx, []ledger.PostInput{in}, false)
	if err != nil || first[0].Err != nil || first[0].Replayed {
		t.Fatalf("first = %+v, %v", first, err)
	}
	for _, atomic := range []bool{false, true} {
		again, err := e.m.PostBatch(ctx, []ledger.PostInput{in}, atomic)
		if err != nil || again[0].Err != nil || !again[0].Replayed || again[0].Transaction.ID != first[0].Transaction.ID {
			t.Fatalf("atomic=%v replay = %+v, %v", atomic, again, err)
		}
	}
	if got := e.balance(t, b.ID); got != 10 {
		t.Fatalf("b = %d, want 10", got)
	}
}

func TestIdempotencyEdgeMetadataNumbers(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	a := e.funded(t, 100)
	b := e.account(t, "USD", ledger.Debit)
	fields := map[string]string{
		"n": `1`, "big": `12345678901234567890`, "f": `0.5`, "neg": `-0`, "arr": `[1,2]`, "x": `1e2`, "s": `"1"`,
	}
	render := func(override map[string]string) jsontext.Value {
		v := func(k string) string {
			if o, ok := override[k]; ok {
				return o
			}
			return fields[k]
		}
		return jsontext.Value(fmt.Sprintf(`{"n":%s,"big":%s,"f":%s,"neg":%s,"arr":%s,"nested":{"x":%s},"s":%s}`,
			v("n"), v("big"), v("f"), v("neg"), v("arr"), v("x"), v("s")))
	}
	base := transfer("meta-numbers", a.ID, b.ID, 7)
	base.Metadata = render(nil)
	original := e.post(t, base)

	tests := []struct {
		name     string
		metadata jsontext.Value
		replay   bool
	}{
		{"1.0", render(map[string]string{"n": `1.0`}), true},
		{"1e0", render(map[string]string{"n": `1e0`}), true},
		{"10e-1", render(map[string]string{"n": `10e-1`}), true},
		{"0.1E+1", render(map[string]string{"n": `0.1E+1`}), true},
		{"1.000", render(map[string]string{"n": `1.000`}), true},
		{"big in exponent form", render(map[string]string{"big": `1.2345678901234567890e19`}), true},
		{"big with trailing fraction zero", render(map[string]string{"big": `12345678901234567890.0`}), true},
		{"half as 5e-1", render(map[string]string{"f": `5e-1`}), true},
		{"negative zero as zero", render(map[string]string{"neg": `0`}), true},
		{"negative zero as -0.0", render(map[string]string{"neg": `-0.0`}), true},
		{"nested 100.0", render(map[string]string{"x": `100.0`}), true},
		{"reordered and spaced", jsontext.Value(`{ "s":"1", "nested":{ "x":100 }, "arr":[ 1, 2 ], "neg":0, "f":0.5, "big":12345678901234567890, "n":1 }`), true},
		{"2", render(map[string]string{"n": `2`}), false},
		{"1 plus 1e-22", render(map[string]string{"n": `1.0000000000000000000001`}), false},
		{"big off by one", render(map[string]string{"big": `12345678901234567891`}), false},
		{"big as float64 rounding", render(map[string]string{"big": `12345678901234567000`}), false},
		{"half plus epsilon", render(map[string]string{"f": `0.5000000000000001`}), false},
		{"number as string", render(map[string]string{"n": `"1"`}), false},
		{"string as number", render(map[string]string{"s": `1`}), false},
		{"array reordered", render(map[string]string{"arr": `[2,1]`}), false},
		{"array extended", render(map[string]string{"arr": `[1,2,0]`}), false},
		{"negative one", render(map[string]string{"n": `-1`}), false},
		{"extra null field", jsontext.Value(strings.TrimSuffix(string(render(nil)), "}") + `,"extra":null}`), false},
		{"missing field", jsontext.Value(`{"n":1}`), false},
		{"empty object", jsontext.Value(`{}`), false},
		{"omitted", nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := base
			in.Metadata = tt.metadata
			got, err := e.m.Post(ctx, in)
			if !tt.replay {
				wantErr(t, err, ledger.ErrIdempotencyConflict)
				return
			}
			if err != nil || got.ID != original.ID {
				t.Fatalf("replay = %s, %v; want %s", got.ID, err, original.ID)
			}
		})
	}
	if got := e.balance(t, b.ID); got != 7 {
		t.Fatalf("b = %d, want 7", got)
	}
}

func TestIdempotencyEdgePayloadFields(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	a := e.funded(t, 1_000)
	b := e.account(t, "USD", ledger.Debit)
	c := e.account(t, "USD", ledger.Debit)
	at := time.Date(2026, 3, 4, 5, 6, 7, 123_456_789, time.UTC)

	base := func() ledger.PostInput {
		in := transfer("fields", a.ID, b.ID, 10)
		in.Description = "d"
		in.ExternalID = "ext-fields"
		in.Metadata = jsontext.Value(`{"k":"v"}`)
		when := at
		in.EffectiveAt = &when
		return in
	}
	original := e.post(t, base())

	tests := []struct {
		name   string
		mutate func(*ledger.PostInput)
		replay bool
	}{
		{"identical", func(*ledger.PostInput) {}, true},
		{"same microsecond different nanoseconds", func(in *ledger.PostInput) { v := at.Add(200); in.EffectiveAt = &v }, true},
		{"same instant other zone", func(in *ledger.PostInput) { v := at.In(time.FixedZone("X", -7*3600)); in.EffectiveAt = &v }, true},
		{"declared currencies", func(in *ledger.PostInput) { in.Postings[0].Currency, in.Postings[1].Currency = "USD", "USD" }, true},
		{"archive flag toggled", func(in *ledger.PostInput) { in.ArchiveOnLockFailure = true }, true},
		{"explicit posted status", func(in *ledger.PostInput) { in.Status = ledger.TransactionPosted }, true},
		{"effective_at omitted", func(in *ledger.PostInput) { in.EffectiveAt = nil }, true},
		{"passing lock added", func(in *ledger.PostInput) { in.Postings[1].AvailableBalance = &ledger.BalanceCondition{GTE: bound(0)} }, true},
		{"next microsecond", func(in *ledger.PostInput) { v := at.Add(time.Microsecond); in.EffectiveAt = &v }, false},
		{"description changed", func(in *ledger.PostInput) { in.Description = "D" }, false},
		{"description cleared", func(in *ledger.PostInput) { in.Description = "" }, false},
		{"amount changed", func(in *ledger.PostInput) { in.Postings[0].Amount, in.Postings[1].Amount = amt(11), amt(11) }, false},
		{"sides swapped", func(in *ledger.PostInput) { in.Postings[0].Side, in.Postings[1].Side = ledger.Credit, ledger.Debit }, false},
		{"account changed", func(in *ledger.PostInput) { in.Postings[0].AccountID = c.ID }, false},
		{"legs reordered", func(in *ledger.PostInput) { in.Postings[0], in.Postings[1] = in.Postings[1], in.Postings[0] }, false},
		{"extra leg", func(in *ledger.PostInput) {
			in.Postings = []ledger.Posting{peLeg(b.ID, ledger.Debit, 5), peLeg(b.ID, ledger.Debit, 5), peLeg(a.ID, ledger.Credit, 10)}
		}, false},
		{"external_id changed", func(in *ledger.PostInput) { in.ExternalID = "ext-other" }, false},
		{"external_id removed", func(in *ledger.PostInput) { in.ExternalID = "" }, false},
		{"pending status", func(in *ledger.PostInput) { in.Status = ledger.TransactionPending }, false},
		{"metadata changed", func(in *ledger.PostInput) { in.Metadata = jsontext.Value(`{"k":"w"}`) }, false},
		{"metadata removed", func(in *ledger.PostInput) { in.Metadata = nil }, false},
		{"declared wrong currency", func(in *ledger.PostInput) { in.Postings[0].Currency = "EUR" }, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := base()
			tt.mutate(&in)
			got, err := e.m.Post(ctx, in)
			if !tt.replay {
				wantErr(t, err, ledger.ErrIdempotencyConflict)
				return
			}
			if err != nil || got.ID != original.ID {
				t.Fatalf("replay = %s, %v; want %s", got.ID, err, original.ID)
			}
		})
	}

	t.Run("posted without effective_at replays with its assigned time", func(t *testing.T) {
		txn := e.post(t, transfer("no-at", a.ID, b.ID, 1))
		in := transfer("no-at", a.ID, b.ID, 1)
		in.EffectiveAt = &txn.EffectiveAt
		if got, err := e.m.Post(ctx, in); err != nil || got.ID != txn.ID {
			t.Fatalf("replay = %+v, %v", got, err)
		}
	})

	t.Run("pending without effective_at conflicts with any explicit time", func(t *testing.T) {
		txn := e.post(t, pending(transfer("no-at-pending", a.ID, b.ID, 1)))
		in := pending(transfer("no-at-pending", a.ID, b.ID, 1))
		in.EffectiveAt = &txn.EffectiveAt
		_, err := e.m.Post(ctx, in)
		wantErr(t, err, ledger.ErrIdempotencyConflict)
	})

	if got := e.balance(t, b.ID); got != 11 {
		t.Fatalf("b = %d, want 11", got)
	}
	e.verify(t)
}

func TestIdempotencyEdgeKeyScope(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	a := e.funded(t, 100)
	b := e.account(t, "USD", ledger.Debit)

	t.Run("keys are global across ledgers", func(t *testing.T) {
		e.post(t, transfer("global", a.ID, b.ID, 1))
		other, err := e.m.CreateLedger(ctx, ledger.CreateLedgerInput{Name: "other"})
		if err != nil {
			t.Fatal(err)
		}
		x, y := e.accountIn(t, other.ID), e.accountIn(t, other.ID)
		_, err = e.m.Post(ctx, transfer("global", x.ID, y.ID, 1))
		wantErr(t, err, ledger.ErrIdempotencyConflict)
		if got := e.balance(t, y.ID); got != 0 {
			t.Fatalf("y = %d", got)
		}
	})

	t.Run("keys match byte for byte", func(t *testing.T) {
		ids := map[uuid.UUID]bool{}
		for _, key := range []string{"exact", "Exact", "exact ", " exact", "exact\t", "éxact", "éxact"} {
			ids[e.post(t, transfer(key, a.ID, b.ID, 1)).ID] = true
		}
		if len(ids) != 7 {
			t.Fatalf("distinct transactions = %d, want 7", len(ids))
		}
	})

	t.Run("pending key stays consumed after archive", func(t *testing.T) {
		in := pending(transfer("archived-key", a.ID, b.ID, 5))
		txn := e.post(t, in)
		if _, err := e.m.ArchiveTransaction(ctx, txn.ID); err != nil {
			t.Fatal(err)
		}
		before := peViews(t, e, a.ID)
		got, err := e.m.Post(ctx, in)
		if err != nil || got.ID != txn.ID || got.Status != ledger.TransactionArchived {
			t.Fatalf("replay = %+v, %v", got, err)
		}
		if after := peViews(t, e, a.ID); after != before {
			t.Fatalf("replay reserved funds again: %v -> %v", before, after)
		}
	})
	e.verify(t)
}

func TestIdempotencyEdgeRejectionsDoNotConsumeKeys(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	a := e.funded(t, 100)
	b := e.account(t, "USD", ledger.Debit)
	frozen := e.account(t, "USD", ledger.Debit, unrestricted)
	if _, err := e.m.FreezeAccount(ctx, frozen.ID); err != nil {
		t.Fatal(err)
	}
	other, err := e.m.CreateLedger(ctx, ledger.CreateLedgerInput{Name: "other"})
	if err != nil {
		t.Fatal(err)
	}
	foreign := e.accountIn(t, other.ID)
	taken := transfer("owner", a.ID, b.ID, 1)
	taken.ExternalID = "ext-taken"
	e.post(t, taken)

	tests := []struct {
		name string
		in   ledger.PostInput
		want error
	}{
		{"insufficient funds", transfer("", a.ID, b.ID, 1_000), ledger.ErrInsufficientFunds},
		{"balance lock", locked(transfer("", a.ID, b.ID, 1), a.ID, availableAtLeast(1_000)), ledger.ErrBalanceLock},
		{"lock version", locked(transfer("", a.ID, b.ID, 1), a.ID, func(p *ledger.Posting) { p.LockVersion = peVersion(999) }), ledger.ErrLockVersion},
		{"frozen", transfer("", a.ID, frozen.ID, 1), ledger.ErrAccountNotOpen},
		{"unbalanced", peLegs("", peLeg(b.ID, ledger.Debit, 2), peLeg(a.ID, ledger.Credit, 1)), ledger.ErrUnbalanced},
		{"unknown account", transfer("", a.ID, uuid.New(), 1), ledger.ErrNotFound},
		{"cross ledger", transfer("", a.ID, foreign.ID, 1), ledger.ErrCrossLedger},
		{"external id taken", func() ledger.PostInput {
			in := transfer("", a.ID, b.ID, 1)
			in.ExternalID = "ext-taken"
			return in
		}(), ledger.ErrExternalIDExists},
		{"invalid", peLegs("", peLeg(b.ID, ledger.Debit, 0), peLeg(a.ID, ledger.Credit, 0)), ledger.ErrInvalid},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			key := "reject-" + tt.name
			in := tt.in
			in.IdempotencyKey = key
			_, err := e.m.Post(ctx, in)
			wantErr(t, err, tt.want)
			if peKeyExists(t, e, key) {
				t.Fatal("rejected post consumed its key")
			}
			e.post(t, transfer(key, a.ID, b.ID, 1))
		})
	}
	if got := e.balance(t, b.ID); got != int64(len(tests))+1 {
		t.Fatalf("b = %d, want %d", got, len(tests)+1)
	}
	e.verify(t)
}

func TestIdempotencyEdgeBatchKeys(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	a := e.funded(t, 100)
	b := e.account(t, "USD", ledger.Debit)

	t.Run("atomic duplicate with same body replays within the batch", func(t *testing.T) {
		in := transfer("atomic-dup", a.ID, b.ID, 3)
		results, err := e.m.PostBatch(ctx, []ledger.PostInput{in, in}, true)
		if err != nil || results[0].Err != nil || results[1].Err != nil {
			t.Fatalf("batch = %+v, %v", results, err)
		}
		if results[0].Replayed || !results[1].Replayed || results[0].Transaction.ID != results[1].Transaction.ID {
			t.Fatalf("replay flags = %v %v", results[0].Replayed, results[1].Replayed)
		}
		if got := e.balance(t, b.ID); got != 3 {
			t.Fatalf("b = %d, want 3", got)
		}
	})

	t.Run("atomic duplicate with different body aborts everything", func(t *testing.T) {
		results, err := e.m.PostBatch(ctx, []ledger.PostInput{
			transfer("atomic-clash", a.ID, b.ID, 1),
			transfer("atomic-clash", a.ID, b.ID, 2),
		}, true)
		if err != nil {
			t.Fatal(err)
		}
		wantErr(t, results[0].Err, ledger.ErrBatchAborted)
		wantErr(t, results[1].Err, ledger.ErrIdempotencyConflict)
		if peKeyExists(t, e, "atomic-clash") {
			t.Fatal("aborted batch wrote its key")
		}
	})

	t.Run("failure of the first occurrence is shared by later ones", func(t *testing.T) {
		results, err := e.m.PostBatch(ctx, []ledger.PostInput{
			transfer("first-fails", a.ID, b.ID, 1_000),
			transfer("first-fails", a.ID, b.ID, 1),
		}, false)
		if err != nil {
			t.Fatal(err)
		}
		wantErr(t, results[0].Err, ledger.ErrInsufficientFunds)
		wantErr(t, results[1].Err, ledger.ErrInsufficientFunds)
		if peKeyExists(t, e, "first-fails") {
			t.Fatal("failed key was written")
		}
	})

	t.Run("invalid first occurrence does not shadow a valid one", func(t *testing.T) {
		bad := transfer("shadow", a.ID, b.ID, 1)
		bad.Description = "\x00"
		results, err := e.m.PostBatch(ctx, []ledger.PostInput{bad, transfer("shadow", a.ID, b.ID, 1)}, false)
		if err != nil {
			t.Fatal(err)
		}
		wantErr(t, results[0].Err, ledger.ErrInvalid)
		if results[1].Err != nil || results[1].Transaction.IdempotencyKey != "shadow" {
			t.Fatalf("second = %+v", results[1])
		}
	})

	t.Run("existing key inside a batch", func(t *testing.T) {
		existing := e.post(t, transfer("pre-existing", a.ID, b.ID, 1))
		results, err := e.m.PostBatch(ctx, []ledger.PostInput{
			transfer("fresh-in-batch", a.ID, b.ID, 1),
			transfer("pre-existing", a.ID, b.ID, 1),
			transfer("pre-existing", a.ID, b.ID, 2),
		}, false)
		if err != nil {
			t.Fatal(err)
		}
		if results[0].Err != nil || results[0].Replayed {
			t.Fatalf("fresh = %+v", results[0])
		}
		if results[1].Err != nil || !results[1].Replayed || results[1].Transaction.ID != existing.ID {
			t.Fatalf("existing = %+v", results[1])
		}
		wantErr(t, results[2].Err, ledger.ErrIdempotencyConflict)
	})

	t.Run("matching duplicate after a conflicting one still replays", func(t *testing.T) {
		existing := e.post(t, transfer("committed", a.ID, b.ID, 1))
		results, err := e.m.PostBatch(ctx, []ledger.PostInput{
			transfer("committed", a.ID, b.ID, 2),
			transfer("committed", a.ID, b.ID, 1),
		}, false)
		if err != nil {
			t.Fatal(err)
		}
		wantErr(t, results[0].Err, ledger.ErrIdempotencyConflict)
		if results[1].Err != nil || !results[1].Replayed || results[1].Transaction.ID != existing.ID {
			t.Fatalf("matching duplicate = %+v, want replay of %s", results[1], existing.ID)
		}
	})

	if got := e.balance(t, b.ID); got != 7 {
		t.Fatalf("b = %d, want 7", got)
	}
	e.verify(t)
}

func TestIdempotencyEdgeExternalIDs(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	a := e.funded(t, 1_000)
	b := e.account(t, "USD", ledger.Debit)
	with := func(key, external string, amount int64) ledger.PostInput {
		in := transfer(key, a.ID, b.ID, amount)
		in.ExternalID = external
		return in
	}

	t.Run("same key and external id replays", func(t *testing.T) {
		first := e.post(t, with("ext-k1", "ext-1", 1))
		again, err := e.m.Post(ctx, with("ext-k1", "ext-1", 1))
		if err != nil || again.ID != first.ID {
			t.Fatalf("replay = %+v, %v", again, err)
		}
	})

	t.Run("failed transaction does not claim its external id", func(t *testing.T) {
		_, err := e.m.Post(ctx, with("ext-broke", "ext-2", 10_000))
		wantErr(t, err, ledger.ErrInsufficientFunds)
		e.post(t, with("ext-k2", "ext-2", 1))
	})

	t.Run("external ids are case sensitive", func(t *testing.T) {
		e.post(t, with("ext-lower", "ext-case", 1))
		e.post(t, with("ext-upper", "EXT-CASE", 1))
	})

	t.Run("duplicate external ids abort an atomic batch", func(t *testing.T) {
		results, err := e.m.PostBatch(ctx, []ledger.PostInput{with("ext-b1", "ext-3", 1), with("ext-b2", "ext-3", 1)}, true)
		if err != nil {
			t.Fatal(err)
		}
		wantErr(t, results[0].Err, ledger.ErrBatchAborted)
		wantErr(t, results[1].Err, ledger.ErrExternalIDExists)
		e.post(t, with("ext-b3", "ext-3", 1))
	})

	t.Run("same external id in two ledgers within one batch", func(t *testing.T) {
		other, err := e.m.CreateLedger(ctx, ledger.CreateLedgerInput{Name: "other"})
		if err != nil {
			t.Fatal(err)
		}
		x, y := e.accountIn(t, other.ID), e.accountIn(t, other.ID)
		foreign := transfer("ext-foreign", x.ID, y.ID, 1)
		foreign.ExternalID = "ext-4"
		results, err := e.m.PostBatch(ctx, []ledger.PostInput{with("ext-local", "ext-4", 1), foreign}, true)
		if err != nil || results[0].Err != nil || results[1].Err != nil {
			t.Fatalf("batch = %+v, %v", results, err)
		}
	})

	t.Run("concurrent claims of one external id", func(t *testing.T) {
		const contenders = 12
		errs := make([]error, contenders)
		var wg sync.WaitGroup
		for i := range contenders {
			wg.Go(func() { _, errs[i] = e.m.Post(ctx, with(fmt.Sprintf("ext-race-%d", i), "ext-race", 1)) })
		}
		wg.Wait()
		won := 0
		for _, err := range errs {
			switch {
			case err == nil:
				won++
			case !errors.Is(err, ledger.ErrExternalIDExists):
				t.Fatalf("unexpected error: %v", err)
			}
		}
		if won != 1 {
			t.Fatalf("winners = %d, want 1", won)
		}
		found, err := e.m.ListTransactions(ctx, ledger.ListTransactionsInput{LedgerID: e.ledger.ID, ExternalID: "ext-race", Limit: 10})
		if err != nil || len(found) != 1 {
			t.Fatalf("transactions with ext-race = %d, %v", len(found), err)
		}
	})

	if got := e.balance(t, b.ID); got != 7 {
		t.Fatalf("b = %d, want 7", got)
	}
	e.verify(t)
}

func TestIdempotencyEdgeConcurrentDifferentBodies(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	a := e.funded(t, 1_000)
	b := e.account(t, "USD", ledger.Debit)

	const contenders = 24
	txns := make([]ledger.Transaction, contenders)
	errs := make([]error, contenders)
	var wg sync.WaitGroup
	for i := range contenders {
		wg.Go(func() { txns[i], errs[i] = e.m.Post(ctx, transfer("contested", a.ID, b.ID, int64(1+i%3))) })
	}
	wg.Wait()

	var winner ledger.Transaction
	for i, err := range errs {
		if err == nil {
			winner = txns[i]
			break
		}
	}
	if winner.ID == uuid.Nil {
		t.Fatal("no contender succeeded")
	}
	amount := small(t, winner.Postings[0].Amount)
	for i, err := range errs {
		mine := int64(1 + i%3)
		switch {
		case mine == amount && (err != nil || txns[i].ID != winner.ID):
			t.Errorf("contender %d with the winning body got %s, %v", i, txns[i].ID, err)
		case mine != amount && !errors.Is(err, ledger.ErrIdempotencyConflict):
			t.Errorf("contender %d with a losing body got %v", i, err)
		}
	}
	if got := e.balance(t, b.ID); got != amount {
		t.Fatalf("b = %d, want %d", got, amount)
	}
	e.verify(t)
}
