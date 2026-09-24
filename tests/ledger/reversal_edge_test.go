package tests

import (
	"context"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/pandabase/astrum/internal/modules/ledger"
	"github.com/pandabase/astrum/internal/money"
)

func peReverse(t testing.TB, e *env, id uuid.UUID, key string) ledger.Transaction {
	t.Helper()
	txn, err := e.m.Reverse(context.Background(), id, ledger.ReverseInput{IdempotencyKey: key})
	if err != nil {
		t.Fatalf("Reverse(%s) error = %v", key, err)
	}
	return txn
}

func TestReversalEdgeChains(t *testing.T) {
	t.Parallel()
	e := setup(t)
	ctx := context.Background()
	a := e.funded(t, 1_000)
	b := e.account(t, "USD", ledger.Debit)
	original := e.post(t, transfer("pay", a.ID, b.ID, 300))

	first := peReverse(t, e, original.ID, "undo")
	for i, p := range first.Postings {
		o := original.Postings[i]
		if p.AccountID != o.AccountID || p.Side == o.Side || p.Amount != o.Amount || p.Currency != o.Currency {
			t.Fatalf("reversal leg %d = %+v, original %+v", i, p, o)
		}
	}
	if e.balance(t, b.ID) != 0 {
		t.Fatal("reversal did not restore b")
	}

	second := peReverse(t, e, first.ID, "redo")
	if second.ReversesID == nil || *second.ReversesID != first.ID {
		t.Fatalf("reversal of reversal reverses %v, want %s", second.ReversesID, first.ID)
	}
	if e.balance(t, a.ID) != 700 || e.balance(t, b.ID) != 300 {
		t.Fatal("reversal of a reversal did not reapply the original")
	}

	third := peReverse(t, e, second.ID, "undo-again")
	if e.balance(t, a.ID) != 1_000 || e.balance(t, b.ID) != 0 || third.ReversesID == nil || *third.ReversesID != second.ID {
		t.Fatal("third link in the chain wrong")
	}

	for id, key := range map[uuid.UUID]string{original.ID: "undo-2", first.ID: "redo-2", second.ID: "undo-again-2"} {
		_, err := e.m.Reverse(ctx, id, ledger.ReverseInput{IdempotencyKey: key})
		wantErr(t, err, ledger.ErrAlreadyReversed)
	}
	e.verify(t)
}

func TestReversalEdgeIdempotency(t *testing.T) {
	t.Parallel()
	e := setup(t)
	ctx := context.Background()
	a := e.funded(t, 1_000)
	b := e.account(t, "USD", ledger.Debit)
	original := e.post(t, transfer("pay", a.ID, b.ID, 100))
	other := e.post(t, transfer("pay-other", a.ID, b.ID, 50))
	rev, err := e.m.Reverse(ctx, original.ID, ledger.ReverseInput{IdempotencyKey: "refund", Description: "r", Metadata: jsontext.Value(`{"n":1}`)})
	if err != nil {
		t.Fatal(err)
	}

	t.Run("replay with equivalent metadata", func(t *testing.T) {
		again, err := e.m.Reverse(ctx, original.ID, ledger.ReverseInput{IdempotencyKey: "refund", Description: "r", Metadata: jsontext.Value(`{"n":1.0}`)})
		if err != nil || again.ID != rev.ID {
			t.Fatalf("replay = %+v, %v", again, err)
		}
	})

	t.Run("same key with a different description is refused", func(t *testing.T) {
		_, err := e.m.Reverse(ctx, original.ID, ledger.ReverseInput{IdempotencyKey: "refund", Description: "changed", Metadata: jsontext.Value(`{"n":1}`)})
		wantErr(t, err, ledger.ErrAlreadyReversed)
	})

	t.Run("reversal key reused for another transaction", func(t *testing.T) {
		_, err := e.m.Reverse(ctx, other.ID, ledger.ReverseInput{IdempotencyKey: "refund", Description: "r", Metadata: jsontext.Value(`{"n":1}`)})
		wantErr(t, err, ledger.ErrIdempotencyConflict)
	})

	t.Run("reversal key replayed through Post", func(t *testing.T) {
		in := ledger.PostInput{IdempotencyKey: "refund", Description: "r", Metadata: jsontext.Value(`{"n":1}`), Postings: rev.Postings}
		got, err := e.m.Post(ctx, in)
		if err != nil || got.ID != rev.ID {
			t.Fatalf("Post(reversal body) = %+v, %v", got, err)
		}
	})

	t.Run("invalid reverse input", func(t *testing.T) {
		for name, in := range map[string]ledger.ReverseInput{
			"empty key":        {},
			"blank key":        {IdempotencyKey: "  "},
			"array metadata":   {IdempotencyKey: "bad-meta", Metadata: jsontext.Value(`[]`)},
			"NUL description":  {IdempotencyKey: "bad-desc", Description: "\x00"},
			"key of 256 bytes": {IdempotencyKey: strings.Repeat("k", 256)},
		} {
			_, err := e.m.Reverse(ctx, other.ID, in)
			if !errors.Is(err, ledger.ErrInvalid) {
				t.Errorf("%s: error = %v, want ErrInvalid", name, err)
			}
		}
	})

	if e.balance(t, b.ID) != 50 {
		t.Fatalf("b = %d, want 50", e.balance(t, b.ID))
	}
	e.verify(t)
}

func TestReversalEdgeNonPosted(t *testing.T) {
	t.Parallel()
	e := setup(t)
	ctx := context.Background()
	a := e.funded(t, 1_000)
	b := e.account(t, "USD", ledger.Debit)

	open := e.post(t, pending(transfer("pending", a.ID, b.ID, 10)))
	archived := e.post(t, pending(transfer("to-archive", a.ID, b.ID, 10)))
	if _, err := e.m.ArchiveTransaction(ctx, archived.ID); err != nil {
		t.Fatal(err)
	}
	lockArchived := locked(transfer("lock-archived", a.ID, b.ID, 10), a.ID, availableAtLeast(10_000))
	lockArchived.ArchiveOnLockFailure = true
	auto := e.post(t, lockArchived)

	for name, id := range map[string]uuid.UUID{"pending": open.ID, "archived": archived.ID, "archived on lock failure": auto.ID} {
		t.Run(name, func(t *testing.T) {
			_, err := e.m.Reverse(ctx, id, ledger.ReverseInput{IdempotencyKey: "rev-" + name})
			wantErr(t, err, ledger.ErrNotPosted)
			if peKeyExists(t, e, "rev-"+name) {
				t.Fatal("refused reversal consumed its key")
			}
		})
	}

	t.Run("partially posted pending reverses only what was posted", func(t *testing.T) {
		if _, err := e.m.PostTransaction(ctx, open.ID, ledger.PostPendingInput{Postings: transfer("", a.ID, b.ID, 4).Postings}); err != nil {
			t.Fatal(err)
		}
		rev := peReverse(t, e, open.ID, "rev-partial")
		if rev.Postings[0].Amount != amt(4) {
			t.Fatalf("reversal amount = %s, want 4", rev.Postings[0].Amount)
		}
		if got := peViews(t, e, a.ID); got != [3]money.Amount{amt(1_000), amt(1_000), amt(1_000)} {
			t.Fatalf("a views = %v", got)
		}
	})
	e.verify(t)
}

func TestReversalEdgeAccountsAndCurrencies(t *testing.T) {
	t.Parallel()
	e := setup(t)
	ctx := context.Background()
	usdA := e.funded(t, 100)
	usdB := e.account(t, "USD", ledger.Debit)
	eurIssuer := e.account(t, "EUR", ledger.Credit, unrestricted)
	eurB := e.account(t, "EUR", ledger.Debit)

	multi := e.post(t, peLegs("multi",
		peLeg(usdB.ID, ledger.Debit, 30), peLeg(usdA.ID, ledger.Credit, 20), peLeg(usdA.ID, ledger.Credit, 10),
		peLeg(eurB.ID, ledger.Debit, 9), peLeg(eurIssuer.ID, ledger.Credit, 9)))

	t.Run("frozen account blocks reversal until unfrozen", func(t *testing.T) {
		if _, err := e.m.FreezeAccount(ctx, eurB.ID); err != nil {
			t.Fatal(err)
		}
		_, err := e.m.Reverse(ctx, multi.ID, ledger.ReverseInput{IdempotencyKey: "rev-multi"})
		wantErr(t, err, ledger.ErrAccountNotOpen)
		if _, err := e.m.UnfreezeAccount(ctx, eurB.ID); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("multi-leg multi-currency reversal restores every account", func(t *testing.T) {
		rev := peReverse(t, e, multi.ID, "rev-multi")
		if len(rev.Postings) != 5 {
			t.Fatalf("legs = %d", len(rev.Postings))
		}
		for id, want := range map[uuid.UUID]int64{usdA.ID: 100, usdB.ID: 0, eurB.ID: 0, eurIssuer.ID: 0} {
			if got := e.balance(t, id); got != want {
				t.Errorf("account %s = %d, want %d", id, got, want)
			}
		}
	})
	e.verify(t)
}

func TestReversalEdgeConcurrent(t *testing.T) {
	t.Parallel()
	e := setup(t)
	ctx := context.Background()
	a := e.funded(t, 1_000)
	b := e.account(t, "USD", ledger.Debit)
	const racers = 12

	for _, sameKey := range []bool{false, true} {
		t.Run(fmt.Sprintf("same key %v", sameKey), func(t *testing.T) {
			original := e.post(t, transfer(fmt.Sprintf("race-pay-%v", sameKey), a.ID, b.ID, 100))
			txns := make([]ledger.Transaction, racers)
			errs := make([]error, racers)
			var wg sync.WaitGroup
			for i := range racers {
				wg.Go(func() {
					key := fmt.Sprintf("race-rev-%v-%d", sameKey, i)
					if sameKey {
						key = fmt.Sprintf("race-rev-%v", sameKey)
					}
					txns[i], errs[i] = e.m.Reverse(ctx, original.ID, ledger.ReverseInput{IdempotencyKey: key})
				})
			}
			wg.Wait()
			ids := map[uuid.UUID]bool{}
			for i, err := range errs {
				switch {
				case err == nil:
					ids[txns[i].ID] = true
				case sameKey || !errors.Is(err, ledger.ErrAlreadyReversed):
					t.Fatalf("racer %d: %v", i, err)
				}
			}
			if len(ids) != 1 {
				t.Fatalf("distinct reversals = %d, want 1", len(ids))
			}
			if got := e.balance(t, b.ID); got != 0 {
				t.Fatalf("b = %d, want 0", got)
			}
		})
	}
	e.verify(t)
}
