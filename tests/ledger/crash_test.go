package tests

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pandabase/astrum/internal/kernel/testdb"
	"github.com/pandabase/astrum/internal/modules/ledger"
)

func TestCrashBeforeCommitLeavesNoTrace(t *testing.T) {
	t.Parallel()
	e := setup(t)
	ctx := context.Background()
	a := e.funded(t, 1_000)
	b := e.account(t, "USD", ledger.Debit)

	conn, err := e.pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `
		WITH t AS (`+e.insertPosted(`'crash'`)+` RETURNING id)
		INSERT INTO ledger_postings (transaction_id, account_id, currency, side, amount, balance_after)
		SELECT t.id, v.account, 'USD', v.side, 500, 0
		FROM t, (VALUES ($1::uuid, 'debit'), ($2::uuid, 'credit')) AS v(account, side)`, b.ID, a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `UPDATE ledger_accounts SET posted_credits = posted_credits + 500, version = version + 1 WHERE id = $1`, a.ID); err != nil {
		t.Fatal(err)
	}

	_ = conn.Conn().PgConn().Conn().Close()
	conn.Release()

	var exists bool
	if err := e.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM ledger_transactions WHERE idempotency_key = 'crash')`).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	if exists {
		t.Fatal("uncommitted transaction survived the crash")
	}
	if bal := e.balance(t, a.ID); bal != 1_000 {
		t.Fatalf("a = %d after crash, want 1000", bal)
	}

	e.post(t, transfer("crash", a.ID, b.ID, 500))
	e.verify(t)
}

func TestClientDisconnectsAtRandomMoments(t *testing.T) {
	t.Parallel()
	e := setup(t)
	a := e.funded(t, 1_000_000)
	b := e.account(t, "USD", ledger.Debit)

	const attempts = 60
	var wg sync.WaitGroup
	for i := range attempts {
		wg.Go(func() {
			ctx, cancel := context.WithTimeout(context.Background(), time.Duration(i%12)*500*time.Microsecond)
			defer cancel()
			_, _ = e.m.Post(ctx, transfer(fmt.Sprintf("flaky-%d", i), a.ID, b.ID, 7))
		})
	}
	wg.Wait()

	for i := range attempts {
		e.post(t, transfer(fmt.Sprintf("flaky-%d", i), a.ID, b.ID, 7))
	}
	if bal := e.balance(t, b.ID); bal != attempts*7 {
		t.Fatalf("b = %d, want %d (each key applied exactly once)", bal, attempts*7)
	}
	e.verify(t)
}

func TestShutdownDrainsQueuedEntries(t *testing.T) {
	t.Parallel()
	e := setup(t)
	a := e.funded(t, 1_000_000)
	b := e.account(t, "USD", ledger.Debit)

	runCtx, stop := context.WithCancel(context.Background())
	m, err := ledger.New(e.pool, testdb.Logger(), ledger.Config{
		Workers: 1, MaxBatch: 4, SweepInterval: time.Hour,
		SealKey: []byte("test-seal-key-0123456789abcdef-0123456789"),
	})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		_ = m.Run(runCtx)
		close(done)
	}()

	const callers = 80
	results := make([]error, callers)
	var wg sync.WaitGroup
	for i := range callers {
		wg.Go(func() {
			_, results[i] = m.Post(context.Background(), transfer(fmt.Sprintf("drain-%d", i), a.ID, b.ID, 1))
		})
	}
	time.Sleep(20 * time.Millisecond)
	stop()
	<-done
	wg.Wait()

	var committed int64
	for i, err := range results {
		var exists bool
		if qerr := e.pool.QueryRow(context.Background(),
			`SELECT EXISTS (SELECT 1 FROM ledger_transactions WHERE idempotency_key = $1)`,
			fmt.Sprintf("drain-%d", i)).Scan(&exists); qerr != nil {
			t.Fatal(qerr)
		}
		switch {
		case err == nil && !exists:
			t.Errorf("caller %d told success but nothing was written", i)
		case errors.Is(err, ledger.ErrStopped) && exists:
			t.Errorf("caller %d told ErrStopped but the entry was written", i)
		case err != nil && !errors.Is(err, ledger.ErrStopped):
			t.Errorf("caller %d unexpected error: %v", i, err)
		}
		if exists {
			committed++
		}
	}
	if bal := e.balance(t, b.ID); bal != committed {
		t.Fatalf("b = %d, committed entries = %d", bal, committed)
	}

	_, err = m.Post(context.Background(), transfer("after-stop", a.ID, b.ID, 1))
	wantErr(t, err, ledger.ErrStopped)
	e.verify(t)
}

func TestHotAccountContention(t *testing.T) {
	t.Parallel()
	e := setupWith(t, ledger.Config{Workers: 8, MaxBatch: 1, SweepInterval: time.Hour})
	hot := e.funded(t, 1_000_000)

	const writers = 40
	sinks := make([]uuid.UUID, writers)
	for i := range sinks {
		sinks[i] = e.account(t, "USD", ledger.Debit).ID
	}

	var wg sync.WaitGroup
	for i := range writers {
		wg.Go(func() {
			if _, err := e.m.Post(context.Background(), transfer(fmt.Sprintf("hot-%d", i), hot.ID, sinks[i], 10)); err != nil {
				t.Errorf("writer %d: %v", i, err)
			}
		})
	}
	wg.Wait()

	if bal := e.balance(t, hot.ID); bal != 1_000_000-writers*10 {
		t.Fatalf("hot = %d", bal)
	}
	e.verify(t)
}
