package tests

import (
	"context"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pandabase/astrum/internal/modules/ledger"
)

func holdInput(key string, account uuid.UUID, amount int64, ttl time.Duration) ledger.CreateHoldInput {
	return ledger.CreateHoldInput{
		IdempotencyKey: key,
		AccountID:      account,
		Amount:         amt(amount),
		Description:    "card authorization",
		ExpiresAt:      time.Now().Add(ttl),
	}
}

func TestHoldLifecycle(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	customer := e.funded(t, 1_000)
	merchant := e.account(t, "USD", ledger.Debit)

	hold, err := e.m.CreateHold(ctx, holdInput("auth-1", customer.ID, 700, time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if hold.Status != ledger.HoldPending || hold.Currency != "USD" {
		t.Fatalf("hold = %+v", hold)
	}
	acc := e.get(t, customer.ID)
	if acc.Posted.Amount != amt(1_000) || acc.Held != amt(700) || acc.Available.Amount != amt(300) {
		t.Fatalf("account after hold = balance %s held %s available %s", acc.Posted.Amount, acc.Held, acc.Available.Amount)
	}

	t.Run("held funds cannot be spent", func(t *testing.T) {
		_, err := e.m.Post(ctx, transfer("spend-held", customer.ID, merchant.ID, 301))
		wantErr(t, err, ledger.ErrInsufficientFunds)
		e.post(t, transfer("spend-free", customer.ID, merchant.ID, 300))
	})

	t.Run("cannot hold more than available", func(t *testing.T) {
		_, err := e.m.CreateHold(ctx, holdInput("auth-2", customer.ID, 1, time.Hour))
		wantErr(t, err, ledger.ErrInsufficientFunds)
	})

	t.Run("partial capture releases remainder", func(t *testing.T) {
		captured, err := e.m.CaptureHold(ctx, hold.ID, ledger.CaptureInput{
			IdempotencyKey: "capture-1",
			Destination:    merchant.ID,
			Amount:         amt(450),
			Metadata:       jsontext.Value(`{"order":1e2}`),
		})
		if err != nil {
			t.Fatal(err)
		}
		if captured.Status != ledger.HoldCaptured || *captured.CapturedAmount != amt(450) || captured.CaptureTransactionID == nil {
			t.Fatalf("captured = %+v", captured)
		}
		acc := e.get(t, customer.ID)
		if acc.Posted.Amount != amt(250) || acc.Held != amt(0) || acc.Available.Amount != amt(250) {
			t.Fatalf("customer after capture = balance %s held %s", acc.Posted.Amount, acc.Held)
		}
		if bal := e.balance(t, merchant.ID); bal != 750 {
			t.Fatalf("merchant = %d, want 750", bal)
		}
	})

	t.Run("capture replay", func(t *testing.T) {

		again, err := e.m.CaptureHold(ctx, hold.ID, ledger.CaptureInput{
			IdempotencyKey: "capture-1", Destination: merchant.ID, Amount: amt(450), Metadata: jsontext.Value(`{"order":1e2}`),
		})
		if err != nil || again.Status != ledger.HoldCaptured {
			t.Fatalf("replay = %+v, %v", again, err)
		}
		if bal := e.balance(t, merchant.ID); bal != 750 {
			t.Fatalf("merchant = %d after replay, want 750", bal)
		}
	})

	t.Run("capture replay with different terms is rejected", func(t *testing.T) {
		other := e.account(t, "USD", ledger.Debit)
		base := ledger.CaptureInput{IdempotencyKey: "capture-1", Destination: merchant.ID, Amount: amt(450), Metadata: jsontext.Value(`{"order":100}`)}
		for name, mutate := range map[string]func(*ledger.CaptureInput){
			"destination": func(in *ledger.CaptureInput) { in.Destination = other.ID },
			"amount":      func(in *ledger.CaptureInput) { in.Amount = amt(449) },
			"description": func(in *ledger.CaptureInput) { in.Description = "different" },
			"metadata":    func(in *ledger.CaptureInput) { in.Metadata = jsontext.Value(`{"order":101}`) },
		} {
			in := base
			mutate(&in)
			_, err := e.m.CaptureHold(ctx, hold.ID, in)
			if !errors.Is(err, ledger.ErrIdempotencyConflict) {
				t.Errorf("%s: err = %v, want ErrIdempotencyConflict", name, err)
			}
		}
		if bal := e.balance(t, other.ID); bal != 0 {
			t.Fatalf("other = %d, want 0", bal)
		}
	})

	t.Run("second capture rejected", func(t *testing.T) {
		_, err := e.m.CaptureHold(ctx, hold.ID, ledger.CaptureInput{IdempotencyKey: "capture-2", Destination: merchant.ID, Amount: amt(1)})
		wantErr(t, err, ledger.ErrHoldNotPending)
	})

	t.Run("void after capture rejected", func(t *testing.T) {
		_, err := e.m.VoidHold(ctx, hold.ID)
		wantErr(t, err, ledger.ErrHoldNotPending)
	})
	e.verify(t)
}

func TestHoldVoidAndReplay(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	customer := e.funded(t, 500)
	merchant := e.account(t, "USD", ledger.Debit)

	in := holdInput("auth", customer.ID, 200, time.Hour)
	hold, err := e.m.CreateHold(ctx, in)
	if err != nil {
		t.Fatal(err)
	}

	again, err := e.m.CreateHold(ctx, in)
	if err != nil || again.ID != hold.ID {
		t.Fatalf("hold replay = %s, %v", again.ID, err)
	}
	changed := in
	changed.Amount = amt(201)
	_, err = e.m.CreateHold(ctx, changed)
	wantErr(t, err, ledger.ErrIdempotencyConflict)

	for range 2 {
		voided, err := e.m.VoidHold(ctx, hold.ID)
		if err != nil || voided.Status != ledger.HoldVoided {
			t.Fatalf("void = %+v, %v", voided, err)
		}
	}
	if acc := e.get(t, customer.ID); acc.Held != amt(0) || acc.Available.Amount != amt(500) {
		t.Fatalf("after void held %s available %s", acc.Held, acc.Available.Amount)
	}

	_, err = e.m.CaptureHold(ctx, hold.ID, ledger.CaptureInput{IdempotencyKey: "c", Destination: merchant.ID, Amount: amt(1)})
	wantErr(t, err, ledger.ErrHoldNotPending)
	_, err = e.m.VoidHold(ctx, uuid.New())
	wantErr(t, err, ledger.ErrNotFound)
	e.verify(t)
}

func TestHoldCaptureRules(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	customer := e.funded(t, 500)
	eur := e.account(t, "EUR", ledger.Debit)
	merchant := e.account(t, "USD", ledger.Debit)
	hold, err := e.m.CreateHold(ctx, holdInput("auth", customer.ID, 100, time.Hour))
	if err != nil {
		t.Fatal(err)
	}

	_, err = e.m.CaptureHold(ctx, hold.ID, ledger.CaptureInput{IdempotencyKey: "over", Destination: merchant.ID, Amount: amt(101)})
	wantErr(t, err, ledger.ErrInvalid)
	_, err = e.m.CaptureHold(ctx, hold.ID, ledger.CaptureInput{IdempotencyKey: "fx", Destination: eur.ID, Amount: amt(50)})
	wantErr(t, err, ledger.ErrInvalid)
	_, err = e.m.CaptureHold(ctx, hold.ID, ledger.CaptureInput{IdempotencyKey: "ghost", Destination: uuid.New(), Amount: amt(50)})
	wantErr(t, err, ledger.ErrNotFound)

	if h, _ := e.m.Hold(ctx, hold.ID); h.Status != ledger.HoldPending {
		t.Fatalf("failed captures changed hold status to %s", h.Status)
	}
	e.verify(t)
}

func TestHoldExpiry(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	customer := e.funded(t, 500)
	merchant := e.account(t, "USD", ledger.Debit)

	short, err := e.m.CreateHold(ctx, holdInput("short", customer.ID, 100, 50*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	long, err := e.m.CreateHold(ctx, holdInput("long", customer.ID, 100, time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)

	_, err = e.m.CaptureHold(ctx, short.ID, ledger.CaptureInput{IdempotencyKey: "late", Destination: merchant.ID, Amount: amt(10)})
	wantErr(t, err, ledger.ErrHoldNotPending)

	n, err := e.m.ExpireHolds(ctx)
	if err != nil || n != 1 {
		t.Fatalf("ExpireHolds() = %d, %v; want 1", n, err)
	}
	if h, _ := e.m.Hold(ctx, short.ID); h.Status != ledger.HoldExpired {
		t.Fatalf("short hold status = %s", h.Status)
	}
	if h, _ := e.m.Hold(ctx, long.ID); h.Status != ledger.HoldPending {
		t.Fatalf("long hold status = %s", h.Status)
	}
	if acc := e.get(t, customer.ID); acc.Held != amt(100) {
		t.Fatalf("held = %s, want 100", acc.Held)
	}
	if n, _ := e.m.ExpireHolds(ctx); n != 0 {
		t.Fatalf("second sweep expired %d, want 0", n)
	}
	e.verify(t)
}

func TestConcurrentCapturesOnlyOneWins(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	customer := e.funded(t, 1_000)
	merchant := e.account(t, "USD", ledger.Debit)
	hold, err := e.m.CreateHold(ctx, holdInput("auth", customer.ID, 1_000, time.Hour))
	if err != nil {
		t.Fatal(err)
	}

	const racers = 12
	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		wins int
	)
	for i := range racers {
		wg.Go(func() {
			_, err := e.m.CaptureHold(ctx, hold.ID, ledger.CaptureInput{
				IdempotencyKey: fmt.Sprintf("race-%d", i),
				Destination:    merchant.ID,
				Amount:         amt(1_000),
			})
			if err == nil {
				mu.Lock()
				wins++
				mu.Unlock()
			}
		})
	}
	wg.Wait()

	if wins != 1 {
		t.Fatalf("successful captures = %d, want 1", wins)
	}
	if bal := e.balance(t, merchant.ID); bal != 1_000 {
		t.Fatalf("merchant = %d, want 1000", bal)
	}
	e.verify(t)
}

func TestListHolds(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	customer := e.funded(t, 1_000)
	other := e.funded(t, 1_000)
	merchant := e.account(t, "USD", ledger.Debit)

	first, err := e.m.CreateHold(ctx, holdInput("list-1", customer.ID, 100, time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	second, err := e.m.CreateHold(ctx, holdInput("list-2", customer.ID, 200, time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.CreateHold(ctx, holdInput("list-3", other.ID, 300, time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.CaptureHold(ctx, first.ID, ledger.CaptureInput{IdempotencyKey: "list-capture", Destination: merchant.ID, Amount: amt(100)}); err != nil {
		t.Fatal(err)
	}

	ids := func(holds []ledger.Hold) []uuid.UUID {
		out := make([]uuid.UUID, len(holds))
		for i, h := range holds {
			out[i] = h.ID
		}
		return out
	}
	tests := []struct {
		name string
		in   ledger.ListHoldsInput
		want []uuid.UUID
	}{
		{"account newest first", ledger.ListHoldsInput{AccountID: customer.ID, Limit: 10}, []uuid.UUID{second.ID, first.ID}},
		{"pending only", ledger.ListHoldsInput{AccountID: customer.ID, Status: ledger.HoldPending, Limit: 10}, []uuid.UUID{second.ID}},
		{"captured only", ledger.ListHoldsInput{AccountID: customer.ID, Status: ledger.HoldCaptured, Limit: 10}, []uuid.UUID{first.ID}},
		{"page after the newest", ledger.ListHoldsInput{AccountID: customer.ID, Before: second.ID, Limit: 10}, []uuid.UUID{first.ID}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := e.m.ListHolds(ctx, tt.in)
			if err != nil {
				t.Fatal(err)
			}
			if fmt.Sprint(ids(got)) != fmt.Sprint(tt.want) {
				t.Fatalf("holds = %v, want %v", ids(got), tt.want)
			}
		})
	}
	if all, _ := e.m.ListHolds(ctx, ledger.ListHoldsInput{Limit: 10}); len(all) != 3 {
		t.Fatalf("all holds = %d, want 3", len(all))
	}
	_, err = e.m.ListHolds(ctx, ledger.ListHoldsInput{Status: "held", Limit: 10})
	wantErr(t, err, ledger.ErrInvalid)
}
