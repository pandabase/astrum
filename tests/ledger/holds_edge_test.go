package tests

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pandabase/astrum/internal/modules/ledger"
)

func feHoldAt(key string, account uuid.UUID, amount int64, expires time.Time) ledger.CreateHoldInput {
	return ledger.CreateHoldInput{IdempotencyKey: key, AccountID: account, Amount: amt(amount), Description: "auth", ExpiresAt: expires}
}

func feHold(t *testing.T, e *env, in ledger.CreateHoldInput) ledger.Hold {
	t.Helper()
	h, err := e.m.CreateHold(context.Background(), in)
	if err != nil {
		t.Fatalf("CreateHold(%s) error = %v", in.IdempotencyKey, err)
	}
	return h
}

func feHoldStatus(t *testing.T, e *env, id uuid.UUID) ledger.HoldStatus {
	t.Helper()
	h, err := e.m.Hold(context.Background(), id)
	if err != nil {
		t.Fatalf("Hold() error = %v", err)
	}
	return h.Status
}

func feHeld(t *testing.T, e *env, id uuid.UUID) int64 {
	t.Helper()
	return small(t, e.get(t, id).Held)
}

func feCreditFunded(t *testing.T, e *env, amount int64) ledger.Account {
	t.Helper()
	acc := e.account(t, "USD", ledger.Credit)
	sink := e.account(t, "USD", ledger.Debit, unrestricted)
	e.post(t, transfer("credit-fund:"+uuid.NewString(), acc.ID, sink.ID, amount))
	return acc
}

func TestHoldsEdgeReserveBoundaries(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	later := time.Now().Add(time.Hour)

	tests := []struct {
		name    string
		account func(t *testing.T) ledger.Account
		amount  int64
		want    error
	}{
		{"exactly available", func(t *testing.T) ledger.Account { return e.funded(t, 100) }, 100, nil},
		{"one over available", func(t *testing.T) ledger.Account { return e.funded(t, 100) }, 101, ledger.ErrInsufficientFunds},
		{"zero balance", func(t *testing.T) ledger.Account { return e.account(t, "USD", ledger.Debit) }, 1, ledger.ErrInsufficientFunds},
		{"exactly available plus overdraft", func(t *testing.T) ledger.Account {
			acc := e.account(t, "USD", ledger.Debit, func(in *ledger.CreateAccountInput) { in.OverdraftLimit = amt(50) })
			e.post(t, transfer("od:"+uuid.NewString(), e.open.ID, acc.ID, 100))
			return acc
		}, 150, nil},
		{"one over overdraft", func(t *testing.T) ledger.Account {
			acc := e.account(t, "USD", ledger.Debit, func(in *ledger.CreateAccountInput) { in.OverdraftLimit = amt(50) })
			e.post(t, transfer("od:"+uuid.NewString(), e.open.ID, acc.ID, 100))
			return acc
		}, 151, ledger.ErrInsufficientFunds},
		{"allow negative has no ceiling", func(t *testing.T) ledger.Account { return e.account(t, "USD", ledger.Debit, unrestricted) }, 1_000_000, nil},
		{"credit normal exactly available", func(t *testing.T) ledger.Account { return feCreditFunded(t, e, 80) }, 80, nil},
		{"credit normal one over", func(t *testing.T) ledger.Account { return feCreditFunded(t, e, 80) }, 81, ledger.ErrInsufficientFunds},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			acc := tt.account(t)
			before := e.get(t, acc.ID)
			_, err := e.m.CreateHold(ctx, feHoldAt("h:"+uuid.NewString(), acc.ID, tt.amount, later))
			if !errors.Is(err, tt.want) {
				t.Fatalf("CreateHold() error = %v, want %v", err, tt.want)
			}
			after := e.get(t, acc.ID)
			if tt.want != nil {
				if after.Held != before.Held || after.Version != before.Version {
					t.Fatalf("rejected hold changed the account: held %s version %d", after.Held, after.Version)
				}
				return
			}
			if after.Held != amt(tt.amount) || after.Version != before.Version+1 {
				t.Fatalf("held %s version %d, want %d and %d", after.Held, after.Version, tt.amount, before.Version+1)
			}
			wantAvail, _ := before.Available.Amount.Sub(amt(tt.amount))
			if after.Available.Amount != wantAvail || after.Posted != before.Posted {
				t.Fatalf("available %s posted %s, want %s and unchanged", after.Available.Amount, after.Posted.Amount, wantAvail)
			}
		})
	}
	e.verify(t)
}

func TestHoldsEdgeValidation(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	acc := e.funded(t, 100)
	later := time.Now().Add(time.Hour)
	frozen := e.funded(t, 100)
	if _, err := e.m.FreezeAccount(ctx, frozen.ID); err != nil {
		t.Fatal(err)
	}
	closed := e.account(t, "USD", ledger.Debit)
	if _, err := e.m.CloseAccount(ctx, closed.ID); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name   string
		mutate func(*ledger.CreateHoldInput)
		want   error
	}{
		{"empty key", func(in *ledger.CreateHoldInput) { in.IdempotencyKey = "" }, ledger.ErrInvalid},
		{"blank key", func(in *ledger.CreateHoldInput) { in.IdempotencyKey = "   " }, ledger.ErrInvalid},
		{"key at 256", func(in *ledger.CreateHoldInput) { in.IdempotencyKey = strings.Repeat("k", 256) }, ledger.ErrInvalid},
		{"key with NUL", func(in *ledger.CreateHoldInput) { in.IdempotencyKey = "a\x00b" }, ledger.ErrInvalid},
		{"no account", func(in *ledger.CreateHoldInput) { in.AccountID = uuid.Nil }, ledger.ErrInvalid},
		{"zero amount", func(in *ledger.CreateHoldInput) { in.Amount = amt(0) }, ledger.ErrInvalid},
		{"negative amount", func(in *ledger.CreateHoldInput) { in.Amount = amt(-1) }, ledger.ErrInvalid},
		{"lowercase currency", func(in *ledger.CreateHoldInput) { in.Currency = "usd" }, ledger.ErrInvalid},
		{"mismatched currency", func(in *ledger.CreateHoldInput) { in.Currency = "EUR" }, ledger.ErrInvalid},
		{"no expiry", func(in *ledger.CreateHoldInput) { in.ExpiresAt = time.Time{} }, ledger.ErrInvalid},
		{"description too long", func(in *ledger.CreateHoldInput) { in.Description = strings.Repeat("d", 1025) }, ledger.ErrInvalid},
		{"unknown account", func(in *ledger.CreateHoldInput) { in.AccountID = uuid.New() }, ledger.ErrNotFound},
		{"frozen account", func(in *ledger.CreateHoldInput) { in.AccountID = frozen.ID }, ledger.ErrAccountNotOpen},
		{"closed account", func(in *ledger.CreateHoldInput) { in.AccountID = closed.ID }, ledger.ErrAccountNotOpen},
		{"key at 255", func(in *ledger.CreateHoldInput) { in.IdempotencyKey = strings.Repeat("k", 255) }, nil},
		{"matching currency", func(in *ledger.CreateHoldInput) { in.Currency = "USD" }, nil},
		{"description at 1024", func(in *ledger.CreateHoldInput) { in.Description = strings.Repeat("d", 1024) }, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := feHoldAt("v:"+uuid.NewString(), acc.ID, 1, later)
			tt.mutate(&in)
			_, err := e.m.CreateHold(ctx, in)
			if !errors.Is(err, tt.want) {
				t.Fatalf("CreateHold() error = %v, want %v", err, tt.want)
			}
		})
	}
	if held := feHeld(t, e, acc.ID); held != 3 {
		t.Fatalf("held = %d, want the 3 valid holds", held)
	}
	e.verify(t)
}

func TestHoldsEdgeReplay(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	acc := e.funded(t, 100)
	other := e.funded(t, 100)
	expires := time.Now().Add(time.Hour).Truncate(time.Microsecond).Add(789 * time.Nanosecond)
	base := feHoldAt("replay", acc.ID, 40, expires)
	first := feHold(t, e, base)

	tests := []struct {
		name   string
		mutate func(*ledger.CreateHoldInput)
		want   error
	}{
		{"identical", func(in *ledger.CreateHoldInput) {}, nil},
		{"sub-microsecond expiry difference", func(in *ledger.CreateHoldInput) { in.ExpiresAt = expires.Add(100 * time.Nanosecond) }, nil},
		{"explicit matching currency", func(in *ledger.CreateHoldInput) { in.Currency = "USD" }, nil},
		{"different amount", func(in *ledger.CreateHoldInput) { in.Amount = amt(41) }, ledger.ErrIdempotencyConflict},
		{"different account", func(in *ledger.CreateHoldInput) { in.AccountID = other.ID }, ledger.ErrIdempotencyConflict},
		{"different description", func(in *ledger.CreateHoldInput) { in.Description = "other" }, ledger.ErrIdempotencyConflict},
		{"different expiry", func(in *ledger.CreateHoldInput) { in.ExpiresAt = expires.Add(time.Microsecond) }, ledger.ErrIdempotencyConflict},
		{"different currency", func(in *ledger.CreateHoldInput) { in.Currency = "EUR" }, ledger.ErrIdempotencyConflict},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := base
			tt.mutate(&in)
			got, err := e.m.CreateHold(ctx, in)
			if !errors.Is(err, tt.want) {
				t.Fatalf("CreateHold() error = %v, want %v", err, tt.want)
			}
			if err == nil && got.ID != first.ID {
				t.Fatalf("replay created %s, want %s", got.ID, first.ID)
			}
		})
	}
	if feHeld(t, e, acc.ID) != 40 || feHeld(t, e, other.ID) != 0 {
		t.Fatal("replays reserved funds again")
	}

	t.Run("replay after resolution returns the resolved hold", func(t *testing.T) {
		if _, err := e.m.VoidHold(ctx, first.ID); err != nil {
			t.Fatal(err)
		}
		got, err := e.m.CreateHold(ctx, base)
		if err != nil || got.ID != first.ID || got.Status != ledger.HoldVoided {
			t.Fatalf("replay = %+v, %v", got, err)
		}
		if held := feHeld(t, e, acc.ID); held != 0 {
			t.Fatalf("held = %d after replaying a voided hold, want 0", held)
		}
	})

	t.Run("concurrent creates with one key replay instead of failing funds", func(t *testing.T) {
		for round := range 5 {
			racer := e.funded(t, 100)
			in := feHoldAt(fmt.Sprint("race-create-", round), racer.ID, 60, expires)
			var (
				wg   sync.WaitGroup
				mu   sync.Mutex
				ids  = map[uuid.UUID]int{}
				errs []error
			)
			for range 16 {
				wg.Go(func() {
					h, err := e.m.CreateHold(ctx, in)
					mu.Lock()
					defer mu.Unlock()
					if err != nil {
						errs = append(errs, err)
						return
					}
					ids[h.ID]++
				})
			}
			wg.Wait()
			if len(errs) > 0 {
				t.Fatalf("round %d: %d of 16 identical creates failed, first: %v", round, len(errs), errs[0])
			}
			if len(ids) != 1 {
				t.Fatalf("round %d: concurrent creates produced %d holds, want 1", round, len(ids))
			}
			if held := feHeld(t, e, racer.ID); held != 60 {
				t.Fatalf("round %d: held = %d, want 60", round, held)
			}
		}
	})
	e.verify(t)
}

func TestHoldsEdgeExpiry(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	acc := e.funded(t, 1_000)
	merchant := e.account(t, "USD", ledger.Debit)
	past := time.Now().Add(-time.Minute)

	stale := feHold(t, e, feHoldAt("stale", acc.ID, 100, past))
	if stale.Status != ledger.HoldPending || feHeld(t, e, acc.ID) != 100 {
		t.Fatalf("a hold created already expired = %+v, held %d", stale, feHeld(t, e, acc.ID))
	}
	live := feHold(t, e, feHoldAt("live", acc.ID, 50, time.Now().Add(time.Hour)))

	_, err := e.m.CaptureHold(ctx, stale.ID, ledger.CaptureInput{IdempotencyKey: "cap-stale", Destination: merchant.ID, Amount: amt(1)})
	wantErr(t, err, ledger.ErrHoldNotPending)
	if got := feHoldStatus(t, e, stale.ID); got != ledger.HoldPending {
		t.Fatalf("rejected capture moved the unswept hold to %s", got)
	}

	n, err := e.m.ExpireHolds(ctx)
	if err != nil || n != 1 {
		t.Fatalf("ExpireHolds() = %d, %v; want 1", n, err)
	}
	expired, err := e.m.Hold(ctx, stale.ID)
	if err != nil || expired.Status != ledger.HoldExpired || expired.ResolvedAt == nil || expired.CapturedAmount != nil {
		t.Fatalf("expired hold = %+v, %v", expired, err)
	}
	if feHeld(t, e, acc.ID) != 50 || feHoldStatus(t, e, live.ID) != ledger.HoldPending {
		t.Fatal("sweep touched the live hold")
	}

	for name, act := range map[string]func() error{
		"capture": func() error {
			_, err := e.m.CaptureHold(ctx, stale.ID, ledger.CaptureInput{IdempotencyKey: "cap-late", Destination: merchant.ID, Amount: amt(1)})
			return err
		},
		"void": func() error { _, err := e.m.VoidHold(ctx, stale.ID); return err },
	} {
		t.Run(name+" after sweep", func(t *testing.T) { wantErr(t, act(), ledger.ErrHoldNotPending) })
	}
	if n, err := e.m.ExpireHolds(ctx); err != nil || n != 0 {
		t.Fatalf("second sweep = %d, %v; want 0", n, err)
	}

	t.Run("void wins over an unswept expiry", func(t *testing.T) {
		h := feHold(t, e, feHoldAt("void-stale", acc.ID, 30, past))
		voided, err := e.m.VoidHold(ctx, h.ID)
		if err != nil || voided.Status != ledger.HoldVoided {
			t.Fatalf("void = %+v, %v", voided, err)
		}
		if n, err := e.m.ExpireHolds(ctx); err != nil || n != 0 {
			t.Fatalf("sweep after void = %d, %v; want 0", n, err)
		}
	})
	if merchant := e.get(t, merchant.ID); !merchant.Posted.Amount.IsZero() {
		t.Fatalf("merchant received %s from expired holds", merchant.Posted.Amount)
	}
	e.verify(t)
}

func TestHoldsEdgeSweepBatches(t *testing.T) {
	e := setupWith(t, ledger.Config{SweepInterval: time.Hour, SweepBatch: 2})
	ctx := context.Background()
	acc := e.funded(t, 1_000)
	base := time.Now().Add(-time.Hour)

	holds := make([]ledger.Hold, 5)
	for i := range holds {
		holds[i] = feHold(t, e, feHoldAt(fmt.Sprint("batch-", i), acc.ID, 10, base.Add(time.Duration(4-i)*time.Minute)))
	}
	for i, want := range []int{2, 2, 1, 0} {
		n, err := e.m.ExpireHolds(ctx)
		if err != nil || n != want {
			t.Fatalf("sweep %d = %d, %v; want %d", i, n, err, want)
		}
		if i == 0 {
			for j, h := range holds {
				want := ledger.HoldPending
				if j >= 3 {
					want = ledger.HoldExpired
				}
				if got := feHoldStatus(t, e, h.ID); got != want {
					t.Fatalf("after first sweep hold %d = %s, want %s (earliest expiry first)", j, got, want)
				}
			}
		}
	}
	if held := feHeld(t, e, acc.ID); held != 0 {
		t.Fatalf("held = %d, want 0", held)
	}
	e.verify(t)
}

func TestHoldsEdgeConcurrentSweeps(t *testing.T) {
	e := setupWith(t, ledger.Config{SweepInterval: time.Hour, SweepBatch: 3})
	ctx := context.Background()
	const total = 30
	accounts := []ledger.Account{e.funded(t, 1_000), e.funded(t, 1_000), e.funded(t, 1_000)}
	for i := range total {
		feHold(t, e, feHoldAt(fmt.Sprint("sweep-", i), accounts[i%3].ID, int64(i+1), time.Now().Add(-time.Minute)))
	}

	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		expired int
	)
	for range 6 {
		wg.Go(func() {
			for {
				n, err := e.m.ExpireHolds(ctx)
				if err != nil {
					t.Error(err)
					return
				}
				if n == 0 {
					return
				}
				mu.Lock()
				expired += n
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	if n, err := e.m.ExpireHolds(ctx); err != nil || n != 0 {
		t.Fatalf("final sweep = %d, %v", n, err)
	}
	if expired != total {
		t.Fatalf("expired %d holds across sweepers, want exactly %d", expired, total)
	}
	for _, acc := range accounts {
		if got := e.get(t, acc.ID); !got.Held.IsZero() || got.Available.Amount != amt(1_000) {
			t.Fatalf("account %s held %s available %s", acc.ID, got.Held, got.Available.Amount)
		}
	}
	list, err := e.m.ListHolds(ctx, ledger.ListHoldsInput{Status: ledger.HoldExpired, Limit: 1000})
	if err != nil || len(list) != total {
		t.Fatalf("expired holds listed = %d, %v", len(list), err)
	}
	e.verify(t)
}

func TestHoldsEdgeCapture(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	later := time.Now().Add(time.Hour)
	merchant := e.account(t, "USD", ledger.Debit)

	tests := []struct {
		name         string
		capture      int64
		want         error
		wantPosted   int64
		wantReleased int64
	}{
		{"full", 400, nil, 600, 0},
		{"one unit", 1, nil, 999, 399},
		{"one under full", 399, nil, 601, 1},
		{"one over", 401, ledger.ErrInvalid, 1_000, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			acc := e.funded(t, 1_000)
			h := feHold(t, e, feHoldAt("cap:"+uuid.NewString(), acc.ID, 400, later))
			before := e.balance(t, merchant.ID)
			got, err := e.m.CaptureHold(ctx, h.ID, ledger.CaptureInput{IdempotencyKey: "cap:" + uuid.NewString(), Destination: merchant.ID, Amount: amt(tt.capture)})
			if !errors.Is(err, tt.want) {
				t.Fatalf("CaptureHold() error = %v, want %v", err, tt.want)
			}
			after := e.get(t, acc.ID)
			if tt.want != nil {
				if feHoldStatus(t, e, h.ID) != ledger.HoldPending || after.Held != amt(400) {
					t.Fatalf("rejected capture changed the hold: held %s", after.Held)
				}
				return
			}
			if got.Status != ledger.HoldCaptured || *got.CapturedAmount != amt(tt.capture) || got.Amount != amt(400) || got.ResolvedAt == nil {
				t.Fatalf("captured hold = %+v", got)
			}
			if after.Posted.Amount != amt(tt.wantPosted) || !after.Held.IsZero() {
				t.Fatalf("customer posted %s held %s, want %d and 0", after.Posted.Amount, after.Held, tt.wantPosted)
			}
			if after.Available.Amount != amt(tt.wantPosted) {
				t.Fatalf("available %s, want %d (remainder %d released)", after.Available.Amount, tt.wantPosted, tt.wantReleased)
			}
			if gained := e.balance(t, merchant.ID) - before; gained != tt.capture {
				t.Fatalf("merchant gained %d, want %d", gained, tt.capture)
			}
			txn, err := e.m.Transaction(ctx, *got.CaptureTransactionID)
			if err != nil || len(txn.Postings) != 2 || txn.Postings[0].AccountID != acc.ID || txn.Postings[0].Side != ledger.Credit {
				t.Fatalf("capture transaction = %+v, %v", txn, err)
			}
		})
	}

	t.Run("credit normal hold account", func(t *testing.T) {
		acc := feCreditFunded(t, e, 100)
		dest := e.account(t, "USD", ledger.Credit)
		h := feHold(t, e, feHoldAt("credit-hold", acc.ID, 70, later))
		got, err := e.m.CaptureHold(ctx, h.ID, ledger.CaptureInput{IdempotencyKey: "credit-cap", Destination: dest.ID, Amount: amt(70)})
		if err != nil {
			t.Fatal(err)
		}
		txn, err := e.m.Transaction(ctx, *got.CaptureTransactionID)
		if err != nil || txn.Postings[0].Side != ledger.Debit || txn.Postings[1].Side != ledger.Credit {
			t.Fatalf("capture postings = %+v, %v", txn.Postings, err)
		}
		if e.balance(t, acc.ID) != 30 || e.balance(t, dest.ID) != 70 {
			t.Fatalf("balances = %d and %d, want 30 and 70", e.balance(t, acc.ID), e.balance(t, dest.ID))
		}
	})

	t.Run("rejections leave the hold pending", func(t *testing.T) {
		acc := e.funded(t, 1_000)
		h := feHold(t, e, feHoldAt("reject-hold", acc.ID, 100, later))
		taken := e.post(t, transfer("taken-key", e.open.ID, merchant.ID, 5))
		frozen := e.account(t, "USD", ledger.Debit)
		if _, err := e.m.FreezeAccount(ctx, frozen.ID); err != nil {
			t.Fatal(err)
		}
		cases := []struct {
			name string
			in   ledger.CaptureInput
			want error
		}{
			{"no key", ledger.CaptureInput{Destination: merchant.ID, Amount: amt(1)}, ledger.ErrInvalid},
			{"no destination", ledger.CaptureInput{IdempotencyKey: "c1", Amount: amt(1)}, ledger.ErrInvalid},
			{"zero amount", ledger.CaptureInput{IdempotencyKey: "c2", Destination: merchant.ID, Amount: amt(0)}, ledger.ErrInvalid},
			{"negative amount", ledger.CaptureInput{IdempotencyKey: "c3", Destination: merchant.ID, Amount: amt(-5)}, ledger.ErrInvalid},
			{"metadata not an object", ledger.CaptureInput{IdempotencyKey: "c4", Destination: merchant.ID, Amount: amt(1), Metadata: []byte(`[1]`)}, ledger.ErrInvalid},
			{"key of another transaction", ledger.CaptureInput{IdempotencyKey: taken.IdempotencyKey, Destination: merchant.ID, Amount: amt(1)}, ledger.ErrIdempotencyConflict},
			{"frozen destination", ledger.CaptureInput{IdempotencyKey: "c5", Destination: frozen.ID, Amount: amt(1)}, ledger.ErrAccountNotOpen},
			{"unknown hold", ledger.CaptureInput{IdempotencyKey: "c6", Destination: merchant.ID, Amount: amt(1)}, ledger.ErrNotFound},
		}
		for _, c := range cases {
			t.Run(c.name, func(t *testing.T) {
				id := h.ID
				if c.name == "unknown hold" {
					id = uuid.New()
				}
				_, err := e.m.CaptureHold(ctx, id, c.in)
				wantErr(t, err, c.want)
			})
		}
		if feHoldStatus(t, e, h.ID) != ledger.HoldPending || feHeld(t, e, acc.ID) != 100 || e.balance(t, acc.ID) != 1_000 {
			t.Fatal("a rejected capture moved money or resolved the hold")
		}
	})
	e.verify(t)
}

func TestHoldsEdgeTerminalStates(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	merchant := e.account(t, "USD", ledger.Debit)

	resolve := map[ledger.HoldStatus]func(t *testing.T, acc ledger.Account) ledger.Hold{
		ledger.HoldCaptured: func(t *testing.T, acc ledger.Account) ledger.Hold {
			h := feHold(t, e, feHoldAt("t:"+uuid.NewString(), acc.ID, 10, time.Now().Add(time.Hour)))
			if _, err := e.m.CaptureHold(ctx, h.ID, ledger.CaptureInput{IdempotencyKey: "tc:" + uuid.NewString(), Destination: merchant.ID, Amount: amt(10)}); err != nil {
				t.Fatal(err)
			}
			return h
		},
		ledger.HoldVoided: func(t *testing.T, acc ledger.Account) ledger.Hold {
			h := feHold(t, e, feHoldAt("t:"+uuid.NewString(), acc.ID, 10, time.Now().Add(time.Hour)))
			if _, err := e.m.VoidHold(ctx, h.ID); err != nil {
				t.Fatal(err)
			}
			return h
		},
		ledger.HoldExpired: func(t *testing.T, acc ledger.Account) ledger.Hold {
			h := feHold(t, e, feHoldAt("t:"+uuid.NewString(), acc.ID, 10, time.Now().Add(-time.Minute)))
			if n, err := e.m.ExpireHolds(ctx); err != nil || n != 1 {
				t.Fatalf("ExpireHolds() = %d, %v", n, err)
			}
			return h
		},
	}
	tests := []struct {
		state       ledger.HoldStatus
		voidErr     error
		captureErr  error
		wantBalance int64
	}{
		{ledger.HoldCaptured, ledger.ErrHoldNotPending, ledger.ErrHoldNotPending, 90},
		{ledger.HoldVoided, nil, ledger.ErrHoldNotPending, 100},
		{ledger.HoldExpired, ledger.ErrHoldNotPending, ledger.ErrHoldNotPending, 100},
	}
	for _, tt := range tests {
		t.Run(string(tt.state), func(t *testing.T) {
			acc := e.funded(t, 100)
			h := resolve[tt.state](t, acc)
			for range 2 {
				got, err := e.m.VoidHold(ctx, h.ID)
				if !errors.Is(err, tt.voidErr) {
					t.Fatalf("VoidHold() error = %v, want %v", err, tt.voidErr)
				}
				if err == nil && got.Status != tt.state {
					t.Fatalf("void replay status = %s", got.Status)
				}
				_, err = e.m.CaptureHold(ctx, h.ID, ledger.CaptureInput{IdempotencyKey: "late:" + uuid.NewString(), Destination: merchant.ID, Amount: amt(1)})
				wantErr(t, err, tt.captureErr)
			}
			got := e.get(t, acc.ID)
			if got.Posted.Amount != amt(tt.wantBalance) || !got.Held.IsZero() || got.Available.Amount != amt(tt.wantBalance) {
				t.Fatalf("account posted %s held %s available %s", got.Posted.Amount, got.Held, got.Available.Amount)
			}
			if st := feHoldStatus(t, e, h.ID); st != tt.state {
				t.Fatalf("hold status = %s, want %s", st, tt.state)
			}
		})
	}
	e.verify(t)
}

func TestHoldsEdgeConcurrentVoidAndCapture(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	acc := e.funded(t, 10_000)
	merchant := e.account(t, "USD", ledger.Debit)

	var captured int64
	for i := range 10 {
		h := feHold(t, e, feHoldAt(fmt.Sprint("vc-", i), acc.ID, 100, time.Now().Add(time.Hour)))
		var (
			wg                  sync.WaitGroup
			voidErr, captureErr error
		)
		wg.Go(func() { _, voidErr = e.m.VoidHold(ctx, h.ID) })
		wg.Go(func() {
			_, captureErr = e.m.CaptureHold(ctx, h.ID, ledger.CaptureInput{IdempotencyKey: fmt.Sprint("vc-cap-", i), Destination: merchant.ID, Amount: amt(100)})
		})
		wg.Wait()
		if (voidErr == nil) == (captureErr == nil) {
			t.Fatalf("round %d: void %v, capture %v; want exactly one winner", i, voidErr, captureErr)
		}
		if lost := errors.Join(voidErr, captureErr); !errors.Is(lost, ledger.ErrHoldNotPending) {
			t.Fatalf("round %d: loser error = %v, want ErrHoldNotPending", i, lost)
		}
		if captureErr == nil {
			captured += 100
		}
	}
	if got := e.get(t, acc.ID); !got.Held.IsZero() || got.Posted.Amount != amt(10_000-captured) {
		t.Fatalf("customer posted %s held %s, want %d and 0", got.Posted.Amount, got.Held, 10_000-captured)
	}
	if e.balance(t, merchant.ID) != captured {
		t.Fatalf("merchant = %d, want %d", e.balance(t, merchant.ID), captured)
	}
	e.verify(t)
}

func TestHoldsEdgeCloseAccountWithHolds(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	acc := e.account(t, "USD", ledger.Debit, unrestricted)
	h := feHold(t, e, feHoldAt("close-hold", acc.ID, 25, time.Now().Add(time.Hour)))

	_, err := e.m.CloseAccount(ctx, acc.ID)
	wantErr(t, err, ledger.ErrAccountNotEmpty)
	frozen, err := e.m.FreezeAccount(ctx, acc.ID)
	if err != nil || frozen.Held != amt(25) {
		t.Fatalf("freeze with a pending hold = %+v, %v", frozen, err)
	}
	_, err = e.m.CloseAccount(ctx, acc.ID)
	wantErr(t, err, ledger.ErrAccountNotEmpty)

	if _, err := e.m.VoidHold(ctx, h.ID); err != nil {
		t.Fatal(err)
	}
	closed, err := e.m.CloseAccount(ctx, acc.ID)
	if err != nil || closed.Status != ledger.AccountClosed {
		t.Fatalf("close after void = %+v, %v", closed, err)
	}
	_, err = e.m.CreateHold(ctx, feHoldAt("on-closed", acc.ID, 1, time.Now().Add(time.Hour)))
	wantErr(t, err, ledger.ErrAccountNotOpen)
	if again, err := e.m.VoidHold(ctx, h.ID); err != nil || again.Status != ledger.HoldVoided {
		t.Fatalf("void replay on a closed account = %+v, %v", again, err)
	}

	t.Run("expired hold on a zero balance account", func(t *testing.T) {
		acc := e.account(t, "USD", ledger.Debit, unrestricted)
		feHold(t, e, feHoldAt("close-expire", acc.ID, 5, time.Now().Add(-time.Minute)))
		_, err := e.m.CloseAccount(ctx, acc.ID)
		wantErr(t, err, ledger.ErrAccountNotEmpty)
		if n, err := e.m.ExpireHolds(ctx); err != nil || n != 1 {
			t.Fatalf("ExpireHolds() = %d, %v", n, err)
		}
		if _, err := e.m.CloseAccount(ctx, acc.ID); err != nil {
			t.Fatal(err)
		}
	})
	e.verify(t)
}

func TestHoldsEdgeList(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	acc := e.funded(t, 1_000)
	other := e.funded(t, 1_000)
	merchant := e.account(t, "USD", ledger.Debit)
	later := time.Now().Add(time.Hour)

	var holds []ledger.Hold
	for i := range 5 {
		holds = append(holds, feHold(t, e, feHoldAt(fmt.Sprint("list-", i), acc.ID, 10, later)))
	}
	expiring := feHold(t, e, feHoldAt("list-expire", other.ID, 10, time.Now().Add(-time.Minute)))
	if _, err := e.m.ExpireHolds(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.VoidHold(ctx, holds[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.CaptureHold(ctx, holds[1].ID, ledger.CaptureInput{IdempotencyKey: "list-cap", Destination: merchant.ID, Amount: amt(10)}); err != nil {
		t.Fatal(err)
	}

	ids := func(hs []ledger.Hold) string {
		out := make([]string, len(hs))
		for i, h := range hs {
			out[i] = h.ID.String()
		}
		return strings.Join(out, ",")
	}
	want := func(hs ...ledger.Hold) string { return ids(hs) }

	tests := []struct {
		name string
		in   ledger.ListHoldsInput
		want string
		err  error
	}{
		{"limit zero", ledger.ListHoldsInput{Limit: 0}, "", ledger.ErrInvalid},
		{"negative limit", ledger.ListHoldsInput{Limit: -1}, "", ledger.ErrInvalid},
		{"limit over max", ledger.ListHoldsInput{Limit: 1001}, "", ledger.ErrInvalid},
		{"unknown status", ledger.ListHoldsInput{Status: "PENDING", Limit: 10}, "", ledger.ErrInvalid},
		{"limit at max", ledger.ListHoldsInput{Limit: 1000}, want(expiring, holds[4], holds[3], holds[2], holds[1], holds[0]), nil},
		{"limit one", ledger.ListHoldsInput{Limit: 1}, want(expiring), nil},
		{"account only", ledger.ListHoldsInput{AccountID: acc.ID, Limit: 10}, want(holds[4], holds[3], holds[2], holds[1], holds[0]), nil},
		{"exact page", ledger.ListHoldsInput{AccountID: acc.ID, Limit: 5}, want(holds[4], holds[3], holds[2], holds[1], holds[0]), nil},
		{"after exact page", ledger.ListHoldsInput{AccountID: acc.ID, Before: holds[0].ID, Limit: 5}, "", nil},
		{"middle page", ledger.ListHoldsInput{AccountID: acc.ID, Before: holds[3].ID, Limit: 2}, want(holds[2], holds[1]), nil},
		{"pending", ledger.ListHoldsInput{AccountID: acc.ID, Status: ledger.HoldPending, Limit: 10}, want(holds[4], holds[3], holds[2]), nil},
		{"voided", ledger.ListHoldsInput{Status: ledger.HoldVoided, Limit: 10}, want(holds[0]), nil},
		{"captured", ledger.ListHoldsInput{Status: ledger.HoldCaptured, Limit: 10}, want(holds[1]), nil},
		{"expired", ledger.ListHoldsInput{Status: ledger.HoldExpired, Limit: 10}, want(expiring), nil},
		{"expired on another account", ledger.ListHoldsInput{AccountID: acc.ID, Status: ledger.HoldExpired, Limit: 10}, "", nil},
		{"unknown account", ledger.ListHoldsInput{AccountID: uuid.New(), Limit: 10}, "", nil},
		{"max cursor", ledger.ListHoldsInput{AccountID: acc.ID, Before: uuid.Max, Limit: 10}, want(holds[4], holds[3], holds[2], holds[1], holds[0]), nil},
		{"cursor from nowhere", ledger.ListHoldsInput{Before: uuid.MustParse("00000000-0000-7000-8000-000000000000"), Limit: 10}, "", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := e.m.ListHolds(ctx, tt.in)
			if !errors.Is(err, tt.err) {
				t.Fatalf("ListHolds() error = %v, want %v", err, tt.err)
			}
			if err != nil {
				return
			}
			if got == nil {
				got = []ledger.Hold{}
			}
			if ids(got) != tt.want {
				t.Fatalf("holds = %s, want %s", ids(got), tt.want)
			}
		})
	}

	t.Run("walk every page", func(t *testing.T) {
		var seen []ledger.Hold
		var before uuid.UUID
		for range 10 {
			page, err := e.m.ListHolds(ctx, ledger.ListHoldsInput{Before: before, Limit: 2})
			if err != nil {
				t.Fatal(err)
			}
			seen = append(seen, page...)
			if len(page) < 2 {
				break
			}
			before = page[len(page)-1].ID
		}
		if ids(seen) != want(expiring, holds[4], holds[3], holds[2], holds[1], holds[0]) {
			t.Fatalf("walked %s", ids(seen))
		}
	})
}
