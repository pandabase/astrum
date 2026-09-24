package tests

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/pandabase/astrum/internal/kernel/db"
	"github.com/pandabase/astrum/internal/modules/ledger"
)

func TestPeriodClose(t *testing.T) {
	t.Parallel()
	e := setup(t)
	ctx := context.Background()
	a := e.account(t, "USD", ledger.Credit, unrestricted)
	b := e.account(t, "USD", ledger.Debit, unrestricted)
	early := e.post(t, dated("early", a.ID, b.ID, 10, 5, ""))

	closed, err := e.m.ClosePeriod(ctx, e.ledger.ID, new(day(10)))
	if err != nil {
		t.Fatal(err)
	}
	if closed.ClosedBefore == nil || !closed.ClosedBefore.Equal(day(10)) || closed.Version != e.ledger.Version+1 {
		t.Fatalf("closed ledger = %+v", closed)
	}

	t.Run("backdated posts are rejected", func(t *testing.T) {
		_, err := e.m.Post(ctx, dated("backdated", a.ID, b.ID, 1, 9, ""))
		wantErr(t, err, ledger.ErrPeriodClosed)
		in := transfer("just-before", a.ID, b.ID, 1)
		in.EffectiveAt = new(day(10).Add(-time.Microsecond))
		_, err = e.m.Post(ctx, in)
		wantErr(t, err, ledger.ErrPeriodClosed)
	})

	t.Run("the boundary and later dates are open", func(t *testing.T) {
		in := transfer("at-boundary", a.ID, b.ID, 1)
		in.EffectiveAt = new(day(10))
		e.post(t, in)
		e.post(t, dated("later", a.ID, b.ID, 1, 11, ""))
		e.post(t, transfer("now", a.ID, b.ID, 1))
	})

	t.Run("replays of earlier requests still succeed", func(t *testing.T) {
		again, err := e.m.Post(ctx, dated("early", a.ID, b.ID, 10, 5, ""))
		if err != nil || again.ID != early.ID {
			t.Fatalf("replay = %+v, %v", again, err)
		}
	})

	t.Run("batches fail only the closed entries", func(t *testing.T) {
		results, err := e.m.PostBatch(ctx, []ledger.PostInput{
			dated("batch-closed", a.ID, b.ID, 1, 3, ""),
			dated("batch-open", a.ID, b.ID, 1, 12, ""),
		}, false)
		if err != nil {
			t.Fatal(err)
		}
		if !errors.Is(results[0].Err, ledger.ErrPeriodClosed) || results[1].Err != nil {
			t.Fatalf("results = %+v", results)
		}
		results, err = e.m.PostBatch(ctx, []ledger.PostInput{
			dated("atomic-open", a.ID, b.ID, 1, 12, ""),
			dated("atomic-closed", a.ID, b.ID, 1, 3, ""),
		}, true)
		if err != nil {
			t.Fatal(err)
		}
		if !errors.Is(results[0].Err, ledger.ErrBatchAborted) || !errors.Is(results[1].Err, ledger.ErrPeriodClosed) {
			t.Fatalf("atomic results = %+v", results)
		}
	})

	t.Run("pending transactions cannot post into a closed period but can be archived", func(t *testing.T) {
		in := dated("pending-closed", a.ID, b.ID, 1, 4, "")
		in.Status = ledger.TransactionPending
		_, err := e.m.Post(ctx, in)
		wantErr(t, err, ledger.ErrPeriodClosed)

		open := dated("pending-open", a.ID, b.ID, 1, 15, "")
		open.Status = ledger.TransactionPending
		pending := e.post(t, open)
		_, err = e.m.UpdateTransaction(ctx, pending.ID, ledger.UpdateTransactionInput{EffectiveAt: new(day(2))})
		wantErr(t, err, ledger.ErrPeriodClosed)
		if _, err := e.m.ArchiveTransaction(ctx, pending.ID); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("a pending transaction created before the close cannot post after it", func(t *testing.T) {
		if _, err := e.m.ClosePeriod(ctx, e.ledger.ID, nil); err != nil {
			t.Fatal(err)
		}
		in := dated("pending-then-close", a.ID, b.ID, 1, 6, "")
		in.Status = ledger.TransactionPending
		pending := e.post(t, in)
		if _, err := e.m.ClosePeriod(ctx, e.ledger.ID, new(day(10))); err != nil {
			t.Fatal(err)
		}
		_, err := e.m.PostTransaction(ctx, pending.ID, ledger.PostPendingInput{})
		wantErr(t, err, ledger.ErrPeriodClosed)
		if _, err := e.m.ArchiveTransaction(ctx, pending.ID); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("reversals are dated now", func(t *testing.T) {
		rev, err := e.m.Reverse(ctx, early.ID, ledger.ReverseInput{IdempotencyKey: "reverse-early"})
		if err != nil || rev.EffectiveAt.Before(day(10)) {
			t.Fatalf("reversal = %+v, %v", rev, err)
		}
	})

	t.Run("reopening allows backdated posts again", func(t *testing.T) {
		reopened, err := e.m.ClosePeriod(ctx, e.ledger.ID, new(day(3)))
		if err != nil || !reopened.ClosedBefore.Equal(day(3)) {
			t.Fatalf("reopen = %+v, %v", reopened, err)
		}
		e.post(t, dated("after-reopen", a.ID, b.ID, 1, 4, ""))
		cleared, err := e.m.ClosePeriod(ctx, e.ledger.ID, nil)
		if err != nil || cleared.ClosedBefore != nil {
			t.Fatalf("clear = %+v, %v", cleared, err)
		}
		e.post(t, dated("after-clear", a.ID, b.ID, 1, 1, ""))
	})

	t.Run("repeating a close changes nothing", func(t *testing.T) {
		first, err := e.m.ClosePeriod(ctx, e.ledger.ID, new(day(2)))
		if err != nil {
			t.Fatal(err)
		}
		second, err := e.m.ClosePeriod(ctx, e.ledger.ID, new(day(2).Add(100*time.Nanosecond)))
		if err != nil || second.Version != first.Version {
			t.Fatalf("repeat = %+v, %v; first version %d", second, err, first.Version)
		}
	})

	t.Run("validation", func(t *testing.T) {
		_, err := e.m.ClosePeriod(ctx, e.ledger.ID, new(time.Now().Add(time.Hour)))
		wantErr(t, err, ledger.ErrInvalid)
		_, err = e.m.ClosePeriod(ctx, e.ledger.ID, new(time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)))
		wantErr(t, err, ledger.ErrInvalid)
		_, err = e.m.ClosePeriod(ctx, e.open.ID, new(day(1)))
		wantErr(t, err, ledger.ErrNotFound)
	})

	t.Run("the database rejects writes into a closed period", func(t *testing.T) {
		if _, err := e.m.ClosePeriod(ctx, e.ledger.ID, new(day(10))); err != nil {
			t.Fatal(err)
		}
		_, err := e.pool.Exec(ctx, `UPDATE ledger_transactions SET effective_at = $2, version = version + 1 WHERE id = $1`,
			early.ID, day(1))
		if db.Code(err) == "" {
			t.Fatal("rewriting a posted transaction into a closed period succeeded")
		}
		var ledgerID string
		if err := e.pool.QueryRow(ctx, `SELECT ledger_id::text FROM ledger_transactions WHERE id = $1`, early.ID).Scan(&ledgerID); err != nil {
			t.Fatal(err)
		}
		_, err = e.pool.Exec(ctx, `
			INSERT INTO ledger_transactions (id, idempotency_key, ledger_id, status, effective_at, created_xid)
			VALUES (gen_random_uuid(), 'raw-closed', $1, 'pending', $2, pg_current_xact_id())`, ledgerID, day(1))
		if db.Constraint(err) != "ledger_transactions_period_open" {
			t.Fatalf("raw insert error = %v, want period_open violation", err)
		}
	})
	e.verify(t)
}

func TestPeriodCloseWaitsForInFlightTransactions(t *testing.T) {
	t.Parallel()
	e := setup(t)
	ctx := context.Background()
	tx, err := e.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SELECT pg_current_xact_id()`); err != nil {
		t.Fatal(err)
	}

	short, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
	defer cancel()
	_, err = e.m.ClosePeriod(short, e.ledger.ID, new(day(10)))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("close with an older transaction in flight = %v, want deadline exceeded", err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.ClosePeriod(ctx, e.ledger.ID, new(day(10))); err != nil {
		t.Fatal(err)
	}
}

func TestHTTPPeriodClose(t *testing.T) {
	t.Parallel()
	a := newAPI(t)
	cash := a.account("cash", "debit", `,"allow_negative":true`)
	sales := a.account("sales", "credit", `,"allow_negative":true`)
	path := "/v1/ledgers/" + a.ledger + "/close_period"

	for _, body := range []string{``, `{}`, `{"closed_before":"yesterday"}`, `{"closed_before":"2026-01-10T00:00:00Z","x":1}`} {
		if got := a.do(http.MethodPost, path, "", body); got.status != http.StatusBadRequest {
			t.Errorf("body %q = %d %v, want 400", body, got.status, got.body)
		}
	}
	closedAt := func(body map[string]any) time.Time {
		t.Helper()
		at, err := time.Parse(time.RFC3339Nano, fmt.Sprint(body["closed_before"]))
		if err != nil {
			t.Fatalf("closed_before = %v: %v", body["closed_before"], err)
		}
		return at
	}
	closed := a.must(http.StatusOK, http.MethodPost, path, "", `{"closed_before":"2026-01-10T00:00:00Z"}`)
	if !closedAt(closed).Equal(day(10)) {
		t.Fatalf("closed = %v", closed)
	}
	if got := a.must(http.StatusOK, http.MethodGet, "/v1/ledgers/"+a.ledger, "", ""); !closedAt(got).Equal(day(10)) {
		t.Fatalf("ledger = %v", got)
	}

	post := func(key, date string) response {
		return a.do(http.MethodPost, "/v1/transactions", key, fmt.Sprintf(
			`{"effective_at":%q,"entries":[{"account_id":%q,"side":"debit","amount":"1"},{"account_id":%q,"side":"credit","amount":"1"}]}`,
			date, cash, sales))
	}
	if got := post("closed", "2026-01-09T12:00:00Z"); got.status != http.StatusConflict || got.body["code"] != "period_closed" {
		t.Fatalf("backdated post = %d %v", got.status, got.body)
	}
	if got := post("open", "2026-01-10T00:00:00Z"); got.status != http.StatusCreated {
		t.Fatalf("post at the boundary = %d %v", got.status, got.body)
	}

	reopened := a.must(http.StatusOK, http.MethodPost, path, "", `{"closed_before":null}`)
	if reopened["closed_before"] != nil {
		t.Fatalf("reopened = %v", reopened)
	}
	if got := post("closed", "2026-01-09T12:00:00Z"); got.status != http.StatusCreated {
		t.Fatalf("post after reopening = %d %v", got.status, got.body)
	}
}
