package tests

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pandabase/astrum/internal/kernel/db"
	"github.com/pandabase/astrum/internal/modules/ledger"
)

func TestDatabaseInvariants(t *testing.T) {
	e := setup(t)
	ctx := context.Background()

	a := e.funded(t, 100)
	b := e.account(t, "USD", ledger.Debit)
	eur := e.account(t, "EUR", ledger.Debit, unrestricted)
	posted := e.post(t, transfer("seed", a.ID, b.ID, 10))
	hold, err := e.m.CreateHold(ctx, holdInput("h", a.ID, 5, time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	voided, err := e.m.CreateHold(ctx, holdInput("v", a.ID, 5, time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.VoidHold(ctx, voided.ID); err != nil {
		t.Fatal(err)
	}
	sched, err := e.m.Schedule(ctx, scheduleInput("s", a.ID, b.ID, 1, time.Now().Add(time.Hour)))
	if err != nil {
		t.Fatal(err)
	}
	e.verify(t)

	insertEntry := func(currency string, debit, credit int64) string {
		return `
			WITH t AS (` + e.insertPosted(`'raw-' || gen_random_uuid()`) + ` RETURNING id)
			INSERT INTO ledger_postings (transaction_id, account_id, currency, side, amount, balance_after)
			SELECT t.id, v.account, '` + currency + `', v.side, v.amount, 0
			FROM t, (VALUES ($1::uuid, 'debit', ` + itoa(debit) + `::bigint), ($2::uuid, 'credit', ` + itoa(credit) + `::bigint)) AS v(account, side, amount)`
	}

	tests := []struct {
		name     string
		wantCode string
		sql      string
		args     []any
	}{
		{"unbalanced entry", "23514", insertEntry("USD", 10, 9), []any{b.ID, a.ID}},
		{"posting currency differs from account", "23503", insertEntry("EUR", 10, 10), []any{b.ID, eur.ID}},
		{"non-positive amount", "23514", insertEntry("USD", 0, 0), []any{b.ID, a.ID}},
		{"posting for unknown account", "23503", insertEntry("USD", 10, 10), []any{b.ID, uuid.New()}},
		{"posting for unknown transaction", "23001",
			`INSERT INTO ledger_postings (transaction_id, account_id, currency, side, amount, balance_after)
			 VALUES ($3, $1, 'USD', 'debit', 1, 0), ($3, $2, 'USD', 'credit', 1, 0)`, []any{b.ID, a.ID, uuid.New()}},
		{"append postings to committed transaction", "23001",
			`INSERT INTO ledger_postings (transaction_id, account_id, currency, side, amount, balance_after)
			 VALUES ($1, $2, 'USD', 'debit', 1, 0), ($1, $3, 'USD', 'credit', 1, 0)`, []any{posted.ID, b.ID, a.ID}},
		{"edit posting", "23001", `UPDATE ledger_postings SET amount = amount + 1 WHERE transaction_id = $1`, []any{posted.ID}},
		{"delete posting", "23001", `DELETE FROM ledger_postings WHERE transaction_id = $1`, []any{posted.ID}},
		{"edit transaction", "23001", `UPDATE ledger_transactions SET description = 'x' WHERE id = $1`, []any{posted.ID}},
		{"truncate postings", "23001", `TRUNCATE ledger_postings CASCADE`, nil},
		{"duplicate idempotency key", "23505", e.insertPosted(`'seed'`), nil},
		{"overdraw via direct update", "23514",
			`UPDATE ledger_accounts SET posted_credits = posted_debits + 1, version = version + 1 WHERE id = $1`, []any{a.ID}},
		{"spend held funds via direct update", "23514",
			`UPDATE ledger_accounts SET posted_credits = posted_debits - 1, version = version + 1 WHERE id = $1`, []any{a.ID}},
		{"change account currency", "23001",
			`UPDATE ledger_accounts SET currency = 'EUR', version = version + 1 WHERE id = $1`, []any{b.ID}},
		{"update without version bump", "23001",
			`UPDATE ledger_accounts SET held = held WHERE id = $1`, []any{b.ID}},
		{"delete account", "23001", `DELETE FROM ledger_accounts WHERE id = $1`, []any{b.ID}},
		{"negative held", "23514", `UPDATE ledger_accounts SET held = -1, version = version + 1 WHERE id = $1`, []any{b.ID}},
		{"change hold amount", "23001", `UPDATE ledger_holds SET amount = 1 WHERE id = $1`, []any{hold.ID}},
		{"reopen resolved hold", "23001", `UPDATE ledger_holds SET status = 'pending', resolved_at = NULL WHERE id = $1`, []any{voided.ID}},
		{"delete hold", "23001", `DELETE FROM ledger_holds WHERE id = $1`, []any{hold.ID}},
		{"reschedule", "23001", `UPDATE ledger_scheduled_transactions SET execute_at = now() WHERE id = $1`, []any{sched.ID}},
		{"edit seal", "23001", `UPDATE ledger_seals SET sealed_at = now()`, nil},
		{"delete seal", "23001", `DELETE FROM ledger_seals`, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) error {
				_, err := tx.Exec(ctx, tt.sql, tt.args...)
				return err
			})
			if got := db.Code(err); got != tt.wantCode {
				t.Fatalf("error = %v (code %q), want %s", err, got, tt.wantCode)
			}
		})
	}
	e.verify(t)

	t.Run("empty transaction is caught by verify", func(t *testing.T) {
		if _, err := e.pool.Exec(ctx, e.insertPosted(`'empty'`)); err != nil {
			t.Fatal(err)
		}
		report, err := e.m.Verify(ctx)
		if err != nil {
			t.Fatal(err)
		}
		requireIssue(t, report.Issues, "has no entries")
	})
}

func itoa(v int64) string {
	return fmt.Sprint(v)
}
