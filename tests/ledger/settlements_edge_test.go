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

func feSettle(t *testing.T, e *env, in ledger.CreateSettlementInput) ledger.Settlement {
	t.Helper()
	st, err := e.m.CreateSettlement(context.Background(), in)
	if err != nil {
		t.Fatalf("CreateSettlement(%s) error = %v", in.IdempotencyKey, err)
	}
	return st
}

func feUnsettled(t *testing.T, e *env, account uuid.UUID) int {
	t.Helper()
	entries, err := e.m.ListEntries(context.Background(), ledger.ListEntriesInput{AccountID: account, Settled: new(false), Limit: 1000})
	if err != nil {
		t.Fatal(err)
	}
	return len(entries)
}

func TestSettlementsEdgeValidation(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	acme := e.account(t, "USD", ledger.Credit, unrestricted)
	payouts := e.account(t, "USD", ledger.Debit, unrestricted)
	eur := e.account(t, "EUR", ledger.Debit, unrestricted)
	other, err := e.m.CreateLedger(ctx, ledger.CreateLedgerInput{Name: "other"})
	if err != nil {
		t.Fatal(err)
	}
	elsewhere := e.accountIn(t, other.ID)
	e.post(t, transfer("v-sale", acme.ID, payouts.ID, 10))

	tests := []struct {
		name   string
		mutate func(*ledger.CreateSettlementInput)
		want   error
	}{
		{"no key", func(in *ledger.CreateSettlementInput) { in.IdempotencyKey = "" }, ledger.ErrInvalid},
		{"key too long", func(in *ledger.CreateSettlementInput) { in.IdempotencyKey = strings.Repeat("k", 256) }, ledger.ErrInvalid},
		{"no settled account", func(in *ledger.CreateSettlementInput) { in.SettledAccountID = uuid.Nil }, ledger.ErrInvalid},
		{"no contra account", func(in *ledger.CreateSettlementInput) { in.ContraAccountID = uuid.Nil }, ledger.ErrInvalid},
		{"settle into itself", func(in *ledger.CreateSettlementInput) { in.ContraAccountID = acme.ID }, ledger.ErrInvalid},
		{"description too long", func(in *ledger.CreateSettlementInput) { in.Description = strings.Repeat("d", 1025) }, ledger.ErrInvalid},
		{"metadata not an object", func(in *ledger.CreateSettlementInput) { in.Metadata = jsontext.Value(`[1]`) }, ledger.ErrInvalid},
		{"metadata null", func(in *ledger.CreateSettlementInput) { in.Metadata = jsontext.Value(`null`) }, ledger.ErrInvalid},
		{"unknown settled account", func(in *ledger.CreateSettlementInput) { in.SettledAccountID = uuid.New() }, ledger.ErrNotFound},
		{"unknown contra account", func(in *ledger.CreateSettlementInput) { in.ContraAccountID = uuid.New() }, ledger.ErrNotFound},
		{"mixed currencies", func(in *ledger.CreateSettlementInput) { in.ContraAccountID = eur.ID }, ledger.ErrInvalid},
		{"mixed ledgers", func(in *ledger.CreateSettlementInput) { in.ContraAccountID = elsewhere.ID }, ledger.ErrInvalid},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := settle("v:"+uuid.NewString(), acme.ID, payouts.ID)
			tt.mutate(&in)
			_, err := e.m.CreateSettlement(ctx, in)
			wantErr(t, err, tt.want)
		})
	}
	if feUnsettled(t, e, acme.ID) != 1 {
		t.Fatal("a rejected settlement marked entries")
	}
	list, err := e.m.ListSettlements(ctx, ledger.ListSettlementsInput{Limit: 10})
	if err != nil || len(list) != 0 {
		t.Fatalf("settlements after rejections = %+v, %v", list, err)
	}
	e.verify(t)
}

func TestSettlementsEdgeZeroNet(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	payouts := e.account(t, "USD", ledger.Debit, unrestricted)

	t.Run("no entries at all", func(t *testing.T) {
		fresh := e.account(t, "USD", ledger.Credit)
		st := feSettle(t, e, settle("fresh", fresh.ID, payouts.ID))
		if !st.Amount.IsZero() || st.EntryCount != 0 || st.TransactionID != nil || st.Currency != "USD" || st.LedgerID != e.ledger.ID {
			t.Fatalf("settlement = %+v", st)
		}
		if !jsonSame(t, st.Metadata, `{}`) || st.UpperBound != nil {
			t.Fatalf("defaults = metadata %s bound %v", st.Metadata, st.UpperBound)
		}
		again := feSettle(t, e, settle("fresh-2", fresh.ID, payouts.ID))
		if again.ID == st.ID || again.EntryCount != 0 {
			t.Fatalf("second empty settlement = %+v", again)
		}
	})

	t.Run("entries that net to zero are settled without a transaction", func(t *testing.T) {
		acme := e.account(t, "USD", ledger.Credit, unrestricted)
		e.post(t, transfer("zn-in", acme.ID, payouts.ID, 50))
		e.post(t, transfer("zn-out", payouts.ID, acme.ID, 50))
		st := feSettle(t, e, settle("zero-net", acme.ID, payouts.ID))
		if !st.Amount.IsZero() || st.EntryCount != 2 || st.TransactionID != nil {
			t.Fatalf("settlement = %+v", st)
		}
		if n := feUnsettled(t, e, acme.ID); n != 0 {
			t.Fatalf("unsettled = %d, want 0", n)
		}
		entries, err := e.m.ListEntries(ctx, ledger.ListEntriesInput{SettlementID: st.ID, Limit: 10})
		if err != nil || len(entries) != 2 {
			t.Fatalf("settled entries = %+v, %v", entries, err)
		}
	})

	t.Run("pending transactions are not settled", func(t *testing.T) {
		acme := e.account(t, "USD", ledger.Credit, unrestricted)
		e.post(t, pending(transfer("zn-pending", acme.ID, payouts.ID, 30)))
		st := feSettle(t, e, settle("pending-only", acme.ID, payouts.ID))
		if !st.Amount.IsZero() || st.EntryCount != 0 {
			t.Fatalf("settlement = %+v", st)
		}
	})
	e.verify(t)
}

func TestSettlementsEdgeUpperBound(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	acme := e.account(t, "USD", ledger.Credit, unrestricted)
	payouts := e.account(t, "USD", ledger.Debit, unrestricted)
	bound := day(10)

	post := func(key string, amount int64, when time.Time) {
		in := transfer(key, acme.ID, payouts.ID, amount)
		in.EffectiveAt = at(when)
		e.post(t, in)
	}
	post("just-before", 1, bound.Add(-time.Microsecond))
	post("exactly-at", 10, bound)
	post("after", 100, bound.Add(time.Hour))

	t.Run("before every entry", func(t *testing.T) {
		in := settle("ub-early", acme.ID, payouts.ID)
		in.UpperBound = at(day(1))
		if st := feSettle(t, e, in); st.EntryCount != 0 || !st.Amount.IsZero() {
			t.Fatalf("settlement = %+v", st)
		}
	})

	in := settle("ub-exact", acme.ID, payouts.ID)
	in.UpperBound = at(bound.Add(999 * time.Nanosecond))
	st := feSettle(t, e, in)
	if st.EntryCount != 1 || st.Amount != amt(1) || !st.UpperBound.Equal(bound) {
		t.Fatalf("bounded settlement = %+v, want only the entry strictly before the bound", st)
	}

	t.Run("replay with the same truncated bound", func(t *testing.T) {
		replay := in
		replay.UpperBound = at(bound.Add(1 * time.Nanosecond))
		got, err := e.m.CreateSettlement(ctx, replay)
		if err != nil || got.ID != st.ID {
			t.Fatalf("replay = %+v, %v", got, err)
		}
		for name, mutate := range map[string]func(*ledger.CreateSettlementInput){
			"no bound":    func(in *ledger.CreateSettlementInput) { in.UpperBound = nil },
			"later bound": func(in *ledger.CreateSettlementInput) { in.UpperBound = at(bound.Add(time.Microsecond)) },
		} {
			changed := in
			mutate(&changed)
			_, err := e.m.CreateSettlement(ctx, changed)
			if !errors.Is(err, ledger.ErrIdempotencyConflict) {
				t.Errorf("%s: error = %v, want ErrIdempotencyConflict", name, err)
			}
		}
	})

	t.Run("bound in the future takes the rest", func(t *testing.T) {
		in := settle("ub-future", acme.ID, payouts.ID)
		in.UpperBound = at(time.Now().Add(24 * time.Hour))
		st := feSettle(t, e, in)
		if st.EntryCount != 2 || st.Amount != amt(110) {
			t.Fatalf("settlement = %+v", st)
		}
	})
	if feUnsettled(t, e, acme.ID) != 0 {
		t.Fatal("entries left unsettled")
	}
	e.verify(t)
}

func TestSettlementsEdgeMovesMoney(t *testing.T) {
	e := setup(t)
	ctx := context.Background()

	t.Run("settlement transaction", func(t *testing.T) {
		acme := e.account(t, "USD", ledger.Credit, unrestricted)
		payouts := e.account(t, "USD", ledger.Debit, unrestricted)
		e.post(t, transfer("mm-sale", acme.ID, payouts.ID, 75))
		in := settle("mm", acme.ID, payouts.ID)
		in.Metadata = jsontext.Value(`{"batch": 7}`)
		st := feSettle(t, e, in)
		txn, err := e.m.Transaction(ctx, *st.TransactionID)
		if err != nil || !strings.HasPrefix(txn.IdempotencyKey, "stl_") || txn.Description != "payout" {
			t.Fatalf("settlement transaction = %+v, %v", txn, err)
		}
		if !jsonSame(t, txn.Metadata, fmt.Sprintf(`{"settlement_id":%q}`, txn.IdempotencyKey)) {
			t.Fatalf("transaction metadata = %s", txn.Metadata)
		}
		if txn.Postings[0].AccountID != acme.ID || txn.Postings[0].Side != ledger.Debit || txn.Postings[0].Amount != amt(75) {
			t.Fatalf("postings = %+v", txn.Postings)
		}
		if again, err := e.m.CreateSettlement(ctx, ledger.CreateSettlementInput{
			IdempotencyKey: "mm", SettledAccountID: acme.ID, ContraAccountID: payouts.ID, Description: "payout",
			Metadata: jsontext.Value(`{"batch":7.0}`),
		}); err != nil || again.ID != st.ID {
			t.Fatalf("replay with equivalent metadata = %+v, %v", again, err)
		}
		if n := feUnsettled(t, e, payouts.ID); n != 2 {
			t.Fatalf("contra unsettled = %d, want its sale and settlement entries", n)
		}
		third := e.account(t, "USD", ledger.Debit, unrestricted)
		back := feSettle(t, e, settle("mm-contra", payouts.ID, third.ID))
		if back.EntryCount != 2 || !back.Amount.IsZero() || back.TransactionID != nil {
			t.Fatalf("settling the contra = %+v", back)
		}
	})

	t.Run("contra without funds", func(t *testing.T) {
		acme := e.account(t, "USD", ledger.Credit, unrestricted)
		sink := e.account(t, "USD", ledger.Debit, unrestricted)
		strict := e.account(t, "USD", ledger.Debit)
		e.post(t, transfer("nf-sale", acme.ID, sink.ID, 40))
		_, err := e.m.CreateSettlement(ctx, settle("nf", acme.ID, strict.ID))
		wantErr(t, err, ledger.ErrInsufficientFunds)
		if feUnsettled(t, e, acme.ID) != 1 {
			t.Fatal("failed settlement marked entries")
		}
	})

	t.Run("frozen accounts", func(t *testing.T) {
		acme := e.account(t, "USD", ledger.Credit, unrestricted)
		payouts := e.account(t, "USD", ledger.Debit, unrestricted)
		e.post(t, transfer("fz-sale", acme.ID, payouts.ID, 20))
		if _, err := e.m.FreezeAccount(ctx, acme.ID); err != nil {
			t.Fatal(err)
		}
		_, err := e.m.CreateSettlement(ctx, settle("fz", acme.ID, payouts.ID))
		wantErr(t, err, ledger.ErrAccountNotOpen)
		if _, err := e.m.UnfreezeAccount(ctx, acme.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := e.m.FreezeAccount(ctx, payouts.ID); err != nil {
			t.Fatal(err)
		}
		_, err = e.m.CreateSettlement(ctx, settle("fz-contra", acme.ID, payouts.ID))
		wantErr(t, err, ledger.ErrAccountNotOpen)
		if feUnsettled(t, e, acme.ID) != 1 {
			t.Fatal("failed settlement marked entries")
		}
	})
	e.verify(t)
}

func TestSettlementsEdgeConcurrency(t *testing.T) {
	e := setup(t)
	ctx := context.Background()

	t.Run("one key", func(t *testing.T) {
		acme := e.account(t, "USD", ledger.Credit, unrestricted)
		payouts := e.account(t, "USD", ledger.Debit, unrestricted)
		e.post(t, transfer("ok-sale", acme.ID, payouts.ID, 90))
		var (
			wg  sync.WaitGroup
			mu  sync.Mutex
			ids = map[uuid.UUID]bool{}
		)
		for range 8 {
			wg.Go(func() {
				st, err := e.m.CreateSettlement(ctx, settle("same-key", acme.ID, payouts.ID))
				if err != nil {
					t.Error(err)
					return
				}
				mu.Lock()
				ids[st.ID] = true
				mu.Unlock()
			})
		}
		wg.Wait()
		if len(ids) != 1 {
			t.Fatalf("one key produced %d settlements", len(ids))
		}
		if e.balance(t, acme.ID) != 0 {
			t.Fatalf("acme = %d, want 0 after exactly one payout", e.balance(t, acme.ID))
		}
	})

	t.Run("opposite directions", func(t *testing.T) {
		a := e.account(t, "USD", ledger.Credit, unrestricted)
		b := e.account(t, "USD", ledger.Credit, unrestricted)
		for i := range 10 {
			e.post(t, transfer(fmt.Sprint("od-", i), a.ID, b.ID, 3))
		}
		var wg sync.WaitGroup
		for i := range 6 {
			wg.Go(func() {
				in := settle(fmt.Sprint("od-ab-", i), a.ID, b.ID)
				if i%2 == 1 {
					in = settle(fmt.Sprint("od-ba-", i), b.ID, a.ID)
				}
				if _, err := e.m.CreateSettlement(ctx, in); err != nil {
					t.Error(err)
				}
			})
		}
		wg.Wait()
		entries, err := e.m.ListEntries(ctx, ledger.ListEntriesInput{AccountID: a.ID, Limit: 1000})
		if err != nil {
			t.Fatal(err)
		}
		seen := map[int64]bool{}
		for _, en := range entries {
			if seen[en.Sequence] {
				t.Fatalf("entry %d listed twice", en.Sequence)
			}
			seen[en.Sequence] = true
		}
	})
	e.verify(t)
}

func TestSettlementsEdgeGetAndList(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	acme := e.account(t, "USD", ledger.Credit, unrestricted)
	globex := e.account(t, "USD", ledger.Credit, unrestricted)
	payouts := e.account(t, "USD", ledger.Debit, unrestricted)

	var acmes []ledger.Settlement
	for i := range 3 {
		e.post(t, transfer(fmt.Sprint("gl-a-", i), acme.ID, payouts.ID, int64(i+1)))
		acmes = append(acmes, feSettle(t, e, settle(fmt.Sprint("gl-acme-", i), acme.ID, payouts.ID)))
	}
	e.post(t, transfer("gl-g", globex.ID, payouts.ID, 9))
	g := feSettle(t, e, settle("gl-globex", globex.ID, payouts.ID))

	got, err := e.m.Settlement(ctx, acmes[1].ID)
	if err != nil || got.ID != acmes[1].ID || got.Amount != amt(2) || got.EntryCount != 1 || !got.CreatedAt.Equal(acmes[1].CreatedAt) {
		t.Fatalf("Settlement() = %+v, %v", got, err)
	}
	_, err = e.m.Settlement(ctx, uuid.New())
	wantErr(t, err, ledger.ErrNotFound)

	keys := func(sts []ledger.Settlement) string {
		out := make([]string, len(sts))
		for i, st := range sts {
			out[i] = st.IdempotencyKey
		}
		return strings.Join(out, ",")
	}
	tests := []struct {
		name string
		in   ledger.ListSettlementsInput
		want string
		err  error
	}{
		{"limit zero", ledger.ListSettlementsInput{}, "", ledger.ErrInvalid},
		{"negative limit", ledger.ListSettlementsInput{Limit: -1}, "", ledger.ErrInvalid},
		{"limit over max", ledger.ListSettlementsInput{Limit: 1001}, "", ledger.ErrInvalid},
		{"limit at max", ledger.ListSettlementsInput{Limit: 1000}, "gl-globex,gl-acme-2,gl-acme-1,gl-acme-0", nil},
		{"limit one", ledger.ListSettlementsInput{Limit: 1}, "gl-globex", nil},
		{"settled account", ledger.ListSettlementsInput{AccountID: acme.ID, Limit: 10}, "gl-acme-2,gl-acme-1,gl-acme-0", nil},
		{"contra account is not matched", ledger.ListSettlementsInput{AccountID: payouts.ID, Limit: 10}, "", nil},
		{"unknown account", ledger.ListSettlementsInput{AccountID: uuid.New(), Limit: 10}, "", nil},
		{"exact page", ledger.ListSettlementsInput{AccountID: acme.ID, Limit: 3}, "gl-acme-2,gl-acme-1,gl-acme-0", nil},
		{"after exact page", ledger.ListSettlementsInput{AccountID: acme.ID, Before: acmes[0].ID, Limit: 3}, "", nil},
		{"cursor", ledger.ListSettlementsInput{Before: g.ID, Limit: 2}, "gl-acme-2,gl-acme-1", nil},
		{"max cursor", ledger.ListSettlementsInput{Before: uuid.Max, Limit: 10}, "gl-globex,gl-acme-2,gl-acme-1,gl-acme-0", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := e.m.ListSettlements(ctx, tt.in)
			if !errors.Is(err, tt.err) {
				t.Fatalf("ListSettlements() error = %v, want %v", err, tt.err)
			}
			if err == nil && keys(got) != tt.want {
				t.Fatalf("settlements = %s, want %s", keys(got), tt.want)
			}
		})
	}
}
