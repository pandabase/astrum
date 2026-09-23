package tests

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pandabase/astrum/internal/kernel/db"
	"github.com/pandabase/astrum/internal/modules/ledger"
)

func TestFrozenAccount(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	a := e.funded(t, 1_000)
	b := e.account(t, "USD", ledger.Debit)
	toVoid, err := e.m.CreateHold(ctx, holdInput("void-me", a.ID, 100, time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	toCapture, err := e.m.CreateHold(ctx, holdInput("capture-me", a.ID, 100, time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	toExpire, err := e.m.CreateHold(ctx, holdInput("expire-me", a.ID, 100, 50*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	sched, err := e.m.Schedule(ctx, scheduleInput("sched", a.ID, b.ID, 10, time.Now().Add(-time.Second)))
	if err != nil {
		t.Fatal(err)
	}

	frozen, err := e.m.FreezeAccount(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if frozen.Status != ledger.AccountFrozen || frozen.StatusChangedAt == nil {
		t.Fatalf("frozen = %+v", frozen)
	}
	if again, err := e.m.FreezeAccount(ctx, a.ID); err != nil || again.Version != frozen.Version {
		t.Fatalf("second freeze = %+v, %v; want no-op", again, err)
	}

	t.Run("rejects money in and out", func(t *testing.T) {
		_, err := e.m.Post(ctx, transfer("out", a.ID, b.ID, 1))
		wantErr(t, err, ledger.ErrAccountNotOpen)
		_, err = e.m.Post(ctx, transfer("in", b.ID, a.ID, 1))
		wantErr(t, err, ledger.ErrAccountNotOpen)
		results, err := e.m.PostBatch(ctx, []ledger.PostInput{transfer("batch", a.ID, b.ID, 1)}, false)
		if err != nil || results[0].Err == nil {
			t.Fatalf("batch = %+v, %v", results, err)
		}
		wantErr(t, results[0].Err, ledger.ErrAccountNotOpen)
	})

	t.Run("rejects new holds and captures", func(t *testing.T) {
		_, err := e.m.CreateHold(ctx, holdInput("new-hold", a.ID, 1, time.Hour))
		wantErr(t, err, ledger.ErrAccountNotOpen)
		_, err = e.m.CaptureHold(ctx, toCapture.ID, ledger.CaptureInput{IdempotencyKey: "cap", Destination: b.ID, Amount: amt(50)})
		wantErr(t, err, ledger.ErrAccountNotOpen)
	})

	t.Run("existing holds can still be released", func(t *testing.T) {
		if _, err := e.m.VoidHold(ctx, toVoid.ID); err != nil {
			t.Fatal(err)
		}
		time.Sleep(100 * time.Millisecond)
		if n, err := e.m.ExpireHolds(ctx); err != nil || n != 1 {
			t.Fatalf("ExpireHolds() = %d, %v; want 1", n, err)
		}
		if h, _ := e.m.Hold(ctx, toExpire.ID); h.Status != ledger.HoldExpired {
			t.Fatalf("expire-me = %s", h.Status)
		}
	})

	t.Run("due schedule fails with a reason", func(t *testing.T) {
		if _, _, err := e.m.ExecuteDue(ctx); err != nil {
			t.Fatal(err)
		}
		st, _ := e.m.Scheduled(ctx, sched.ID)
		if st.Status != ledger.ScheduleFailed || st.Failure == nil || !strings.Contains(*st.Failure, "frozen") {
			t.Fatalf("schedule = %+v", st)
		}
	})

	t.Run("unfreeze restores posting", func(t *testing.T) {
		acc, err := e.m.UnfreezeAccount(ctx, a.ID)
		if err != nil || acc.Status != ledger.AccountOpen {
			t.Fatalf("unfreeze = %+v, %v", acc, err)
		}
		e.post(t, transfer("after-unfreeze", a.ID, b.ID, 1))
		if _, err := e.m.CaptureHold(ctx, toCapture.ID, ledger.CaptureInput{IdempotencyKey: "cap", Destination: b.ID, Amount: amt(50)}); err != nil {
			t.Fatal(err)
		}
	})

	if acc := e.get(t, a.ID); acc.Posted.Amount != amt(949) || acc.Held != amt(0) {
		t.Fatalf("a = balance %s held %s, want 949 and 0", acc.Posted.Amount, acc.Held)
	}
	e.verify(t)
}

func TestCloseAccount(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	a := e.funded(t, 100)
	b := e.account(t, "USD", ledger.Debit)

	hold, err := e.m.CreateHold(ctx, holdInput("h", a.ID, 10, time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	e.post(t, transfer("drain", a.ID, b.ID, 90))
	_, err = e.m.CloseAccount(ctx, a.ID)
	wantErr(t, err, ledger.ErrAccountNotEmpty)

	if _, err := e.m.CaptureHold(ctx, hold.ID, ledger.CaptureInput{IdempotencyKey: "c", Destination: b.ID, Amount: amt(10)}); err != nil {
		t.Fatal(err)
	}

	if _, err := e.m.FreezeAccount(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	closed, err := e.m.CloseAccount(ctx, a.ID)
	if err != nil || closed.Status != ledger.AccountClosed {
		t.Fatalf("close = %+v, %v", closed, err)
	}
	if again, err := e.m.CloseAccount(ctx, a.ID); err != nil || again.Version != closed.Version {
		t.Fatalf("second close = %+v, %v; want no-op", again, err)
	}

	_, err = e.m.UnfreezeAccount(ctx, a.ID)
	wantErr(t, err, ledger.ErrAccountNotOpen)
	_, err = e.m.FreezeAccount(ctx, a.ID)
	wantErr(t, err, ledger.ErrAccountNotOpen)
	_, err = e.m.Post(ctx, transfer("revive", b.ID, a.ID, 1))
	wantErr(t, err, ledger.ErrAccountNotOpen)

	if _, err := e.m.CloseAccount(ctx, uuid.New()); err == nil {
		t.Fatal("closing an unknown account succeeded")
	}
	e.verify(t)
}

func TestAccountStatusInvariants(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	funded := e.funded(t, 100)
	frozen := e.funded(t, 100)
	empty := e.account(t, "USD", ledger.Debit)
	if _, err := e.m.FreezeAccount(ctx, frozen.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.CloseAccount(ctx, empty.ID); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name     string
		wantCode string
		sql      string
		id       uuid.UUID
	}{
		{"move money on frozen account", "23001", `UPDATE ledger_accounts SET posted_credits = posted_credits + 1, version = version + 1 WHERE id = $1`, frozen.ID},
		{"hold funds on frozen account", "23001", `UPDATE ledger_accounts SET held = held + 1, version = version + 1 WHERE id = $1`, frozen.ID},
		{"reopen closed account", "23001", `UPDATE ledger_accounts SET status = 'open', version = version + 1 WHERE id = $1`, empty.ID},
		{"close funded account", "23514", `UPDATE ledger_accounts SET status = 'closed', version = version + 1 WHERE id = $1`, funded.ID},
		{"unknown status", "23514", `UPDATE ledger_accounts SET status = 'deleted', version = version + 1 WHERE id = $1`, funded.ID},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) error {
				_, err := tx.Exec(ctx, tt.sql, tt.id)
				return err
			})
			if got := db.Code(err); got != tt.wantCode {
				t.Fatalf("error = %v (code %q), want %s", err, got, tt.wantCode)
			}
		})
	}
	e.verify(t)
}
