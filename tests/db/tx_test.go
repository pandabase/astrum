package db_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/pandabase/astrum/internal/kernel/db"
	"github.com/pandabase/astrum/internal/kernel/testdb"
)

func TestRetryable(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"marked retry", fmt.Errorf("wrap: %w", db.ErrRetry), true},
		{"serialization failure", &pgconn.PgError{Code: "40001"}, true},
		{"deadlock", fmt.Errorf("x: %w", &pgconn.PgError{Code: "40P01"}), true},
		{"unique violation", &pgconn.PgError{Code: "23505"}, false},
		{"check violation", &pgconn.PgError{Code: "23514"}, false},
		{"plain error", errors.New("boom"), false},
		{"canceled", context.Canceled, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := db.Retryable(tt.err); got != tt.want {
				t.Fatalf("Retryable() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestErrorAccessors(t *testing.T) {
	t.Parallel()
	err := fmt.Errorf("wrapped: %w", &pgconn.PgError{Code: "23505", ConstraintName: "things_key"})
	if db.Code(err) != "23505" || db.Constraint(err) != "things_key" {
		t.Fatalf("Code/Constraint = %q/%q", db.Code(err), db.Constraint(err))
	}
	if db.Code(errors.New("x")) != "" || db.Constraint(nil) != "" {
		t.Fatal("non-pg errors must yield empty strings")
	}
}

func TestRunTxRetriesTransientFailures(t *testing.T) {
	t.Parallel()
	pool := testdb.New(t, nil)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `CREATE TABLE counter (n int NOT NULL)`); err != nil {
		t.Fatal(err)
	}

	attempts := 0
	err := db.RunTx(ctx, pool, func(tx pgx.Tx) error {
		attempts++
		if _, err := tx.Exec(ctx, `INSERT INTO counter VALUES (1)`); err != nil {
			return err
		}
		if attempts < 3 {
			return &pgconn.PgError{Code: "40001"}
		}
		return nil
	})
	if err != nil || attempts != 3 {
		t.Fatalf("RunTx() = %v after %d attempts, want success after 3", err, attempts)
	}

	var rows int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM counter`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Fatalf("rows = %d, want 1 (failed attempts rolled back)", rows)
	}
}

func TestRunTxStopsOnPermanentFailure(t *testing.T) {
	t.Parallel()
	pool := testdb.New(t, nil)
	attempts := 0
	boom := errors.New("permanent")
	err := db.RunTx(context.Background(), pool, func(pgx.Tx) error {
		attempts++
		return boom
	})
	if !errors.Is(err, boom) || attempts != 1 {
		t.Fatalf("RunTx() = %v after %d attempts", err, attempts)
	}
}

func TestRunTxGivesUp(t *testing.T) {
	t.Parallel()
	pool := testdb.New(t, nil)
	attempts := 0
	err := db.RunTx(context.Background(), pool, func(pgx.Tx) error {
		attempts++
		return db.ErrRetry
	})
	if !errors.Is(err, db.ErrRetry) || attempts != 6 {
		t.Fatalf("RunTx() = %v after %d attempts, want ErrRetry after 6", err, attempts)
	}
}

func TestRunTxHonoursCancellation(t *testing.T) {
	t.Parallel()
	pool := testdb.New(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	err := db.RunTx(ctx, pool, func(pgx.Tx) error {
		cancel()
		return db.ErrRetry
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("RunTx() = %v, want context.Canceled", err)
	}
}

func TestSessionIsHardened(t *testing.T) {
	t.Parallel()
	pool := testdb.New(t, nil)
	ctx := context.Background()

	if err := db.CheckDurability(ctx, pool); err != nil {
		t.Fatalf("CheckDurability() = %v", err)
	}
	want := map[string]string{
		"synchronous_commit":                  "on",
		"lock_timeout":                        "10s",
		"statement_timeout":                   "30s",
		"idle_in_transaction_session_timeout": "30s",
	}
	for name, v := range want {
		var got string
		if err := pool.QueryRow(ctx, `SELECT current_setting($1)`, name).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != v {
			t.Errorf("%s = %s, want %s", name, got, v)
		}
	}
}

func TestCheckDurabilityRejectsAsyncCommit(t *testing.T) {
	t.Parallel()
	pool := testdb.New(t, nil)
	ctx := context.Background()
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, `SET synchronous_commit = off`); err != nil {
		t.Fatal(err)
	}
	defer conn.Exec(ctx, `SET synchronous_commit = on`)

	if err := db.CheckDurability(ctx, conn); err == nil {
		t.Fatal("CheckDurability() accepted synchronous_commit = off")
	}
}
