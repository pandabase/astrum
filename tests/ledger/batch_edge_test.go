package tests

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pandabase/astrum/internal/modules/ledger"
)

func peBatch(t testing.TB, e *env, ins []ledger.PostInput, atomic bool) []ledger.BatchResult {
	t.Helper()
	results, err := e.m.PostBatch(context.Background(), ins, atomic)
	if err != nil {
		t.Fatalf("PostBatch() error = %v", err)
	}
	if len(results) != len(ins) {
		t.Fatalf("results = %d, want %d", len(results), len(ins))
	}
	return results
}

func peLockAccountsTable(t testing.TB, e *env) func() {
	t.Helper()
	ctx := context.Background()
	tx, err := e.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `LOCK TABLE ledger_accounts IN EXCLUSIVE MODE`); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatal(err)
	}
	var once sync.Once
	release := func() { once.Do(func() { _ = tx.Rollback(ctx) }) }
	t.Cleanup(release)
	return release
}

func peAccountWaiters(t testing.TB, e *env) int {
	t.Helper()
	var n int
	if err := e.pool.QueryRow(context.Background(),
		`SELECT count(DISTINCT pid) FROM pg_locks WHERE locktype = 'relation' AND relation = 'ledger_accounts'::regclass AND NOT granted`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func peAwaitWaiters(t testing.TB, e *env, want int) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		got := peAccountWaiters(t, e)
		if got == want {
			return
		}
		if got > want || time.Now().After(deadline) {
			t.Fatalf("transactions waiting on accounts = %d, want %d", got, want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestBatchEdgeSizes(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	a := e.funded(t, 100_000)
	b := e.account(t, "USD", ledger.Debit)
	many := func(prefix string, n int) []ledger.PostInput {
		ins := make([]ledger.PostInput, n)
		for i := range ins {
			ins[i] = transfer(fmt.Sprintf("%s-%d", prefix, i), a.ID, b.ID, 1)
		}
		return ins
	}

	t.Run("empty non-nil batch", func(t *testing.T) {
		_, err := e.m.PostBatch(ctx, []ledger.PostInput{}, false)
		wantErr(t, err, ledger.ErrInvalid)
	})

	for _, atomic := range []bool{false, true} {
		t.Run(fmt.Sprintf("single entry atomic=%v", atomic), func(t *testing.T) {
			r := peBatch(t, e, many(fmt.Sprintf("one-%v", atomic), 1), atomic)
			if r[0].Err != nil || r[0].Transaction == nil || r[0].Replayed {
				t.Fatalf("result = %+v", r[0])
			}
		})
	}

	t.Run("1001 valid entries rejected without writing", func(t *testing.T) {
		_, err := e.m.PostBatch(ctx, many("over", 1001), false)
		wantErr(t, err, ledger.ErrInvalid)
		if peKeyExists(t, e, "over-0") {
			t.Fatal("oversized batch wrote entries")
		}
	})

	t.Run("exactly 1000 entries atomically", func(t *testing.T) {
		results := peBatch(t, e, many("max", 1000), true)
		for i, r := range results {
			if r.Err != nil || r.Transaction.IdempotencyKey != fmt.Sprintf("max-%d", i) {
				t.Fatalf("result %d = %+v", i, r)
			}
		}
	})

	if got := e.balance(t, b.ID); got != 1002 {
		t.Fatalf("b = %d, want 1002", got)
	}
	e.verify(t)
}

func TestBatchEdgeValidationAndAlignment(t *testing.T) {
	e := setup(t)
	a := e.funded(t, 100)
	b := e.account(t, "USD", ledger.Debit)
	invalid := func(key string) ledger.PostInput {
		in := transfer(key, a.ID, b.ID, 1)
		in.Postings = in.Postings[:1]
		return in
	}

	t.Run("atomic batch rejects on any invalid entry", func(t *testing.T) {
		_, err := e.m.PostBatch(context.Background(), []ledger.PostInput{transfer("av-ok", a.ID, b.ID, 1), invalid("av-bad")}, true)
		wantErr(t, err, ledger.ErrInvalid)
		if peKeyExists(t, e, "av-ok") {
			t.Fatal("atomic batch with an invalid entry wrote the valid one")
		}
	})

	t.Run("non-atomic results stay aligned with inputs", func(t *testing.T) {
		ins := []ledger.PostInput{
			invalid("al-0"),
			transfer("al-1", a.ID, b.ID, 1),
			invalid("al-2"),
			transfer("al-3", a.ID, b.ID, 1_000),
			transfer("al-4", a.ID, b.ID, 2),
			invalid("al-5"),
		}
		results := peBatch(t, e, ins, false)
		wants := []error{ledger.ErrInvalid, nil, ledger.ErrInvalid, ledger.ErrInsufficientFunds, nil, ledger.ErrInvalid}
		for i, want := range wants {
			wantErr(t, results[i].Err, want)
			if want == nil && results[i].Transaction.IdempotencyKey != ins[i].IdempotencyKey {
				t.Fatalf("result %d is %s", i, results[i].Transaction.IdempotencyKey)
			}
			if want != nil && (results[i].Transaction != nil || results[i].Error == "") {
				t.Fatalf("result %d = %+v", i, results[i])
			}
		}
	})

	t.Run("all invalid non-atomic", func(t *testing.T) {
		results := peBatch(t, e, []ledger.PostInput{invalid("x-0"), invalid("x-1")}, false)
		for i, r := range results {
			wantErr(t, r.Err, ledger.ErrInvalid)
			if r.Transaction != nil {
				t.Fatalf("result %d has a transaction", i)
			}
		}
	})

	if got := e.balance(t, b.ID); got != 3 {
		t.Fatalf("b = %d, want 3", got)
	}
	e.verify(t)
}

func TestBatchEdgeAtomicAbort(t *testing.T) {
	e := setup(t)
	a := e.funded(t, 100)
	b := e.account(t, "USD", ledger.Debit)

	for _, failAt := range []int{0, 1, 2} {
		t.Run(fmt.Sprintf("failure at %d", failAt), func(t *testing.T) {
			ins := make([]ledger.PostInput, 3)
			for i := range ins {
				amount := int64(10)
				if i == failAt {
					amount = 1_000
				}
				ins[i] = transfer(fmt.Sprintf("abort-%d-%d", failAt, i), a.ID, b.ID, amount)
			}
			results := peBatch(t, e, ins, true)
			for i, r := range results {
				want := ledger.ErrBatchAborted
				if i == failAt {
					want = ledger.ErrInsufficientFunds
				}
				wantErr(t, r.Err, want)
				if peKeyExists(t, e, ins[i].IdempotencyKey) {
					t.Fatalf("aborted entry %d was written", i)
				}
			}
		})
	}

	t.Run("all replays", func(t *testing.T) {
		first := peBatch(t, e, []ledger.PostInput{transfer("rep-0", a.ID, b.ID, 1), transfer("rep-1", a.ID, b.ID, 1)}, true)
		again := peBatch(t, e, []ledger.PostInput{transfer("rep-0", a.ID, b.ID, 1), transfer("rep-1", a.ID, b.ID, 1)}, true)
		for i := range again {
			if again[i].Err != nil || !again[i].Replayed || again[i].Transaction.ID != first[i].Transaction.ID {
				t.Fatalf("replay %d = %+v", i, again[i])
			}
		}
	})

	t.Run("replay and new entry together", func(t *testing.T) {
		results := peBatch(t, e, []ledger.PostInput{transfer("rep-0", a.ID, b.ID, 1), transfer("rep-2", a.ID, b.ID, 1)}, true)
		if !results[0].Replayed || results[1].Replayed || results[1].Err != nil {
			t.Fatalf("results = %+v", results)
		}
	})

	if got := e.balance(t, b.ID); got != 3 {
		t.Fatalf("b = %d, want 3", got)
	}
	e.verify(t)
}

func TestBatchEdgeCancellation(t *testing.T) {
	e := setup(t)
	a := e.funded(t, 100)
	b := e.account(t, "USD", ledger.Debit)

	for _, atomic := range []bool{false, true} {
		t.Run(fmt.Sprintf("already cancelled atomic=%v", atomic), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			key := fmt.Sprintf("cancelled-%v", atomic)
			_, err := e.m.PostBatch(ctx, []ledger.PostInput{transfer(key, a.ID, b.ID, 1)}, atomic)
			wantErr(t, err, context.Canceled)
			if peKeyExists(t, e, key) {
				t.Fatal("cancelled batch was written")
			}
		})
	}
	if got := e.balance(t, b.ID); got != 0 {
		t.Fatalf("b = %d, want 0", got)
	}
}

func TestBatchEdgeConcurrencyCap(t *testing.T) {
	tests := []struct {
		name   string
		config int
		want   int
	}{
		{"one", 1, 1},
		{"two", 2, 2},
		{"default", 0, 4},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := setupWith(t, ledger.Config{BatchConcurrency: tt.config, SweepInterval: time.Hour})
			a := e.funded(t, 1_000)
			b := e.account(t, "USD", ledger.Debit)

			release := peLockAccountsTable(t, e)
			queued := tt.want + 2
			errs := make([]error, queued)
			var wg sync.WaitGroup
			for i := range queued {
				wg.Go(func() {
					_, errs[i] = e.m.PostBatch(context.Background(), []ledger.PostInput{transfer(fmt.Sprintf("cap-%d", i), a.ID, b.ID, 1)}, i%2 == 0)
				})
			}
			peAwaitWaiters(t, e, tt.want)

			ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
			_, err := e.m.PostBatch(ctx, []ledger.PostInput{transfer("cap-late", a.ID, b.ID, 1)}, true)
			cancel()
			wantErr(t, err, context.DeadlineExceeded)
			if got := peAccountWaiters(t, e); got != tt.want {
				t.Fatalf("transactions waiting on accounts = %d, want %d", got, tt.want)
			}

			release()
			wg.Wait()
			for i, err := range errs {
				if err != nil {
					t.Fatalf("queued batch %d: %v", i, err)
				}
			}
			if peKeyExists(t, e, "cap-late") {
				t.Fatal("batch that timed out waiting for a slot was written")
			}
			if got := e.balance(t, b.ID); got != int64(queued) {
				t.Fatalf("b = %d, want %d", got, queued)
			}
			e.verify(t)
		})
	}
}

func TestBatchEdgeCancelWhileWaitingForSlot(t *testing.T) {
	e := setupWith(t, ledger.Config{BatchConcurrency: 1, SweepInterval: time.Hour})
	a := e.funded(t, 1_000)
	b := e.account(t, "USD", ledger.Debit)

	release := peLockAccountsTable(t, e)
	var (
		wg        sync.WaitGroup
		holderErr error
	)
	wg.Go(func() {
		_, holderErr = e.m.PostBatch(context.Background(), []ledger.PostInput{transfer("slot-holder", a.ID, b.ID, 5)}, true)
	})
	peAwaitWaiters(t, e, 1)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := e.m.PostBatch(ctx, []ledger.PostInput{
			transfer("slot-waiter-1", a.ID, b.ID, 1),
			transfer("slot-waiter-2", a.ID, b.ID, 1),
		}, false)
		done <- err
	}()
	cancel()
	select {
	case err := <-done:
		wantErr(t, err, context.Canceled)
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled batch kept waiting for a slot")
	}
	if got := peAccountWaiters(t, e); got != 1 {
		t.Fatalf("waiters = %d, want only the slot holder", got)
	}

	release()
	wg.Wait()
	if holderErr != nil {
		t.Fatal(holderErr)
	}
	for _, key := range []string{"slot-waiter-1", "slot-waiter-2"} {
		if peKeyExists(t, e, key) {
			t.Fatalf("%s was written by a cancelled batch", key)
		}
	}
	peBatch(t, e, []ledger.PostInput{transfer("slot-waiter-1", a.ID, b.ID, 1)}, true)
	if got := e.balance(t, b.ID); got != 6 {
		t.Fatalf("b = %d, want 6", got)
	}
	e.verify(t)
}

func TestBatchEdgeSlotReleasedAfterPanic(t *testing.T) {
	e := setupWith(t, ledger.Config{BatchConcurrency: 1, SweepInterval: time.Hour})
	a := e.funded(t, 100)
	b := e.account(t, "USD", ledger.Debit)

	poison := ledger.PostInput{IdempotencyKey: "poison", ArchiveOnLockFailure: true, Postings: []ledger.Posting{
		{AccountID: a.ID, Side: ledger.Credit, Amount: amt(1), LockVersion: peVersion(1 << 40)},
		peLeg(uuid.New(), ledger.Debit, 1),
	}}
	if _, err := peBatchRecover(context.Background(), e, []ledger.PostInput{poison}, false); err == nil {
		t.Log("poison entry no longer panics")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	results, err := e.m.PostBatch(ctx, []ledger.PostInput{transfer("after-panic", a.ID, b.ID, 1)}, true)
	if errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("batch slot leaked by a panicking batch; later batches block forever")
	}
	if err != nil || results[0].Err != nil {
		t.Fatalf("after panic = %+v, %v", results, err)
	}
}

func TestBatchEdgeNonAtomicIsolatesWriteTimeFailures(t *testing.T) {
	e := setup(t)
	a := e.funded(t, 100)
	b := e.account(t, "USD", ledger.Debit)
	far := time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
	bad := transfer("far-future", a.ID, b.ID, 1)
	bad.EffectiveAt = &far

	results, err := e.m.PostBatch(context.Background(), []ledger.PostInput{transfer("near", a.ID, b.ID, 1), bad}, false)
	if err != nil {
		t.Fatalf("PostBatch() error = %v, want per-entry results", err)
	}
	if results[0].Err != nil {
		t.Fatalf("good entry = %+v", results[0])
	}
	wantErr(t, results[1].Err, ledger.ErrInvalid)
}
