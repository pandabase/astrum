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

func feBulkItems(n int, from, to uuid.UUID, amount int64) []ledger.PostInput {
	items := make([]ledger.PostInput, n)
	for i := range items {
		items[i] = transfer("", from, to, amount)
	}
	return items
}

func feBulkResults(t *testing.T, e *env, id uuid.UUID) []ledger.BulkResult {
	t.Helper()
	results, err := e.m.BulkResults(context.Background(), id, -1, 1000)
	if err != nil {
		t.Fatalf("BulkResults() error = %v", err)
	}
	return results
}

func feProcessAll(t *testing.T, e *env) int {
	t.Helper()
	n := 0
	for {
		found, err := e.m.ProcessBulk(context.Background())
		if err != nil {
			t.Fatalf("ProcessBulk() error = %v", err)
		}
		if !found {
			return n
		}
		n++
	}
}

func TestBulkEdgeSizeAndKeys(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	a := e.account(t, "USD", ledger.Debit, unrestricted)
	b := e.account(t, "USD", ledger.Debit, unrestricted)

	tests := []struct {
		name  string
		key   string
		items int
		want  error
	}{
		{"one item", "one", 1, nil},
		{"max items", "max", 10_000, nil},
		{"max plus one", "max-plus-one", 10_001, ledger.ErrInvalid},
		{"empty", "empty", 0, ledger.ErrInvalid},
		{"blank key", "  ", 1, ledger.ErrInvalid},
		{"key with NUL", "a\x00", 1, ledger.ErrInvalid},
		{"derived keys fit", strings.Repeat("k", 253), 10, nil},
		{"derived key one over", strings.Repeat("q", 253), 11, ledger.ErrInvalid},
		{"key at the limit", strings.Repeat("z", 255), 1, ledger.ErrInvalid},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bulk, err := e.m.CreateBulk(ctx, ledger.CreateBulkInput{IdempotencyKey: tt.key, Transactions: feBulkItems(tt.items, a.ID, b.ID, 1)})
			if !errors.Is(err, tt.want) {
				t.Fatalf("CreateBulk() error = %v, want %v", err, tt.want)
			}
			if err == nil && (bulk.Total != tt.items || bulk.Status != ledger.BulkPending || bulk.Processed != 0 || bulk.StartedAt != nil) {
				t.Fatalf("bulk = %+v", bulk)
			}
		})
	}

	t.Run("invalid item names its index", func(t *testing.T) {
		items := feBulkItems(3, a.ID, b.ID, 1)
		items[2].Postings = items[2].Postings[:1]
		_, err := e.m.CreateBulk(ctx, ledger.CreateBulkInput{IdempotencyKey: "bad-item", Transactions: items})
		wantErr(t, err, ledger.ErrInvalid)
		if !strings.Contains(err.Error(), "transaction 2") {
			t.Fatalf("error = %v, want it to name transaction 2", err)
		}
		if _, err := e.m.CreateBulk(ctx, ledger.CreateBulkInput{IdempotencyKey: "bad-item", Transactions: feBulkItems(3, a.ID, b.ID, 1)}); err != nil {
			t.Fatalf("a rejected bulk reserved its key: %v", err)
		}
	})
}

func TestBulkEdgeOutcomes(t *testing.T) {
	e := setupWith(t, ledger.Config{SweepInterval: time.Hour, MaxBatch: 3})
	ctx := context.Background()
	rich := e.funded(t, 100)
	poor := e.funded(t, 1)
	b := e.account(t, "USD", ledger.Debit)
	frozen := e.account(t, "USD", ledger.Debit, unrestricted)
	if _, err := e.m.FreezeAccount(ctx, frozen.ID); err != nil {
		t.Fatal(err)
	}
	other, err := e.m.CreateLedger(ctx, ledger.CreateLedgerInput{Name: "other"})
	if err != nil {
		t.Fatal(err)
	}
	elsewhere := e.accountIn(t, other.ID)
	replayed := e.post(t, transfer("imp/7", rich.ID, b.ID, 1))
	e.post(t, transfer("imp/8", rich.ID, b.ID, 2))

	unbalanced := transfer("", rich.ID, b.ID, 1)
	unbalanced.Postings[0].Amount = amt(2)
	items := []ledger.PostInput{
		transfer("custom-key-ignored", rich.ID, b.ID, 1),
		transfer("", rich.ID, uuid.New(), 1),
		unbalanced,
		transfer("", rich.ID, frozen.ID, 1),
		transfer("", poor.ID, b.ID, 2),
		transfer("", rich.ID, b.ID, 1),
		transfer("", rich.ID, b.ID, 1),
		transfer("", rich.ID, b.ID, 1),
		transfer("", rich.ID, b.ID, 1),
		transfer("", rich.ID, elsewhere.ID, 1),
	}
	codes := []string{"", "not_found", "unbalanced_transaction", "account_not_open", "insufficient_funds", "", "", "", "idempotency_key_reused", "cross_ledger_transaction"}

	bulk, err := e.m.CreateBulk(ctx, ledger.CreateBulkInput{IdempotencyKey: "imp", Transactions: items})
	if err != nil {
		t.Fatal(err)
	}
	pendingResults := feBulkResults(t, e, bulk.ID)
	for _, r := range pendingResults {
		if r.TransactionID != nil || r.ErrorCode != nil || r.ErrorDetail != nil {
			t.Fatalf("unprocessed result = %+v", r)
		}
	}
	if len(pendingResults) != len(items) {
		t.Fatalf("results = %d, want %d", len(pendingResults), len(items))
	}

	if n := feProcessAll(t, e); n != 1 {
		t.Fatalf("processed %d bulk requests, want 1", n)
	}
	done, err := e.m.Bulk(ctx, bulk.ID)
	if err != nil || done.Status != ledger.BulkCompleted || done.Processed != 10 || done.Succeeded != 4 || done.Failed != 6 ||
		done.StartedAt == nil || done.CompletedAt == nil || done.CompletedAt.Before(*done.StartedAt) {
		t.Fatalf("bulk = %+v, %v", done, err)
	}
	results := feBulkResults(t, e, bulk.ID)
	txns := map[uuid.UUID]bool{}
	for i, r := range results {
		if r.Index != i {
			t.Fatalf("result %d has index %d", i, r.Index)
		}
		if codes[i] == "" {
			if r.TransactionID == nil || r.ErrorCode != nil || txns[*r.TransactionID] {
				t.Fatalf("item %d = %+v, want a distinct transaction", i, r)
			}
			txns[*r.TransactionID] = true
			txn, err := e.m.Transaction(ctx, *r.TransactionID)
			if err != nil || txn.IdempotencyKey != fmt.Sprint("imp/", i) {
				t.Fatalf("item %d transaction = %+v, %v", i, txn, err)
			}
			if i == 7 && txn.ID != replayed.ID {
				t.Fatalf("item 7 = %s, want the already posted %s", txn.ID, replayed.ID)
			}
			continue
		}
		if r.TransactionID != nil || r.ErrorCode == nil || *r.ErrorCode != codes[i] || r.ErrorDetail == nil || *r.ErrorDetail == "" {
			t.Fatalf("item %d = %+v, want error %s", i, r, codes[i])
		}
	}
	if got := e.balance(t, b.ID); got != 1+2+3 {
		t.Fatalf("b = %d, want 6", got)
	}
	e.verify(t)
}

func TestBulkEdgeReplay(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	a := e.account(t, "USD", ledger.Debit, unrestricted)
	b := e.account(t, "USD", ledger.Debit, unrestricted)
	items := []ledger.PostInput{transfer("", a.ID, b.ID, 1), transfer("", a.ID, b.ID, 2), transfer("", a.ID, b.ID, 2)}

	first, err := e.m.CreateBulk(ctx, ledger.CreateBulkInput{IdempotencyKey: "rp", Transactions: items})
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name   string
		mutate func([]ledger.PostInput) []ledger.PostInput
		want   error
	}{
		{"identical", func(in []ledger.PostInput) []ledger.PostInput { return in }, nil},
		{"different item keys", func(in []ledger.PostInput) []ledger.PostInput {
			in[0].IdempotencyKey = "mine"
			return in
		}, nil},
		{"reordered", func(in []ledger.PostInput) []ledger.PostInput {
			in[0], in[1] = in[1], in[0]
			return in
		}, ledger.ErrIdempotencyConflict},
		{"extra item", func(in []ledger.PostInput) []ledger.PostInput { return append(in, transfer("", a.ID, b.ID, 1)) }, ledger.ErrIdempotencyConflict},
		{"dropped duplicate", func(in []ledger.PostInput) []ledger.PostInput { return in[:2] }, ledger.ErrIdempotencyConflict},
		{"changed amount", func(in []ledger.PostInput) []ledger.PostInput {
			in[2] = transfer("", a.ID, b.ID, 3)
			return in
		}, ledger.ErrIdempotencyConflict},
		{"changed description", func(in []ledger.PostInput) []ledger.PostInput {
			in[1].Description = "other"
			return in
		}, ledger.ErrIdempotencyConflict},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fresh := make([]ledger.PostInput, len(items))
			for i := range items {
				fresh[i] = transfer("", a.ID, b.ID, small(t, items[i].Postings[0].Amount))
			}
			got, err := e.m.CreateBulk(ctx, ledger.CreateBulkInput{IdempotencyKey: "rp", Transactions: tt.mutate(fresh)})
			if !errors.Is(err, tt.want) {
				t.Fatalf("CreateBulk() error = %v, want %v", err, tt.want)
			}
			if err == nil && got.ID != first.ID {
				t.Fatalf("replay created %s, want %s", got.ID, first.ID)
			}
		})
	}

	if n := feProcessAll(t, e); n != 1 {
		t.Fatalf("processed %d, want 1", n)
	}
	done, err := e.m.CreateBulk(ctx, ledger.CreateBulkInput{IdempotencyKey: "rp", Transactions: feBulkItems(0, a.ID, b.ID, 1)})
	wantErr(t, err, ledger.ErrInvalid)
	done, err = e.m.CreateBulk(ctx, ledger.CreateBulkInput{IdempotencyKey: "rp", Transactions: []ledger.PostInput{
		transfer("", a.ID, b.ID, 1), transfer("", a.ID, b.ID, 2), transfer("", a.ID, b.ID, 2),
	}})
	if err != nil || done.ID != first.ID || done.Status != ledger.BulkCompleted || done.Succeeded != 3 {
		t.Fatalf("replay after completion = %+v, %v", done, err)
	}
	if n := feProcessAll(t, e); n != 0 {
		t.Fatalf("a replayed completed bulk was processed again (%d)", n)
	}
	if got := e.balance(t, b.ID); got != 5 {
		t.Fatalf("b = %d, want 5 (duplicate rows both post, once)", got)
	}
	e.verify(t)
}

func TestBulkEdgeResultsPaging(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	a := e.account(t, "USD", ledger.Debit, unrestricted)
	b := e.account(t, "USD", ledger.Debit, unrestricted)
	bulk, err := e.m.CreateBulk(ctx, ledger.CreateBulkInput{IdempotencyKey: "pg", Transactions: feBulkItems(5, a.ID, b.ID, 1)})
	if err != nil {
		t.Fatal(err)
	}
	feProcessAll(t, e)

	tests := []struct {
		name         string
		id           uuid.UUID
		after, limit int
		first, count int
		err          error
	}{
		{"limit zero", bulk.ID, -1, 0, 0, 0, ledger.ErrInvalid},
		{"negative limit", bulk.ID, -1, -1, 0, 0, ledger.ErrInvalid},
		{"limit over max", bulk.ID, -1, 1001, 0, 0, ledger.ErrInvalid},
		{"unknown bulk", uuid.New(), -1, 10, 0, 0, ledger.ErrNotFound},
		{"unknown bulk with bad limit", uuid.New(), -1, 0, 0, 0, ledger.ErrNotFound},
		{"limit at max", bulk.ID, -1, 1000, 0, 5, nil},
		{"limit one", bulk.ID, -1, 1, 0, 1, nil},
		{"exact page", bulk.ID, -1, 5, 0, 5, nil},
		{"after the last", bulk.ID, 4, 5, 0, 0, nil},
		{"after zero skips index zero", bulk.ID, 0, 10, 1, 4, nil},
		{"very negative cursor", bulk.ID, -1_000, 2, 0, 2, nil},
		{"middle", bulk.ID, 1, 2, 2, 2, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := e.m.BulkResults(ctx, tt.id, tt.after, tt.limit)
			if !errors.Is(err, tt.err) {
				t.Fatalf("BulkResults() error = %v, want %v", err, tt.err)
			}
			if err != nil {
				return
			}
			if len(got) != tt.count || (tt.count > 0 && got[0].Index != tt.first) {
				t.Fatalf("results = %+v, want %d starting at %d", got, tt.count, tt.first)
			}
		})
	}
	_, err = e.m.Bulk(ctx, uuid.New())
	wantErr(t, err, ledger.ErrNotFound)
}

func TestBulkEdgeClaims(t *testing.T) {
	e := setupWith(t, ledger.Config{SweepInterval: time.Hour, MaxBatch: 5})
	ctx := context.Background()
	a := e.account(t, "USD", ledger.Debit, unrestricted)
	b := e.account(t, "USD", ledger.Debit, unrestricted)

	if found, err := e.m.ProcessBulk(ctx); err != nil || found {
		t.Fatalf("ProcessBulk() with nothing queued = %v, %v", found, err)
	}

	t.Run("one request, many workers", func(t *testing.T) {
		bulk, err := e.m.CreateBulk(ctx, ledger.CreateBulkInput{IdempotencyKey: "claim-one", Transactions: feBulkItems(20, a.ID, b.ID, 1)})
		if err != nil {
			t.Fatal(err)
		}
		var (
			wg    sync.WaitGroup
			mu    sync.Mutex
			found int
		)
		for range 8 {
			wg.Go(func() {
				ok, err := e.m.ProcessBulk(ctx)
				if err != nil {
					t.Error(err)
					return
				}
				if ok {
					mu.Lock()
					found++
					mu.Unlock()
				}
			})
		}
		wg.Wait()
		if found != 1 {
			t.Fatalf("%d workers claimed the request, want 1", found)
		}
		if done, _ := e.m.Bulk(ctx, bulk.ID); done.Status != ledger.BulkCompleted || done.Succeeded != 20 {
			t.Fatalf("bulk = %+v", done)
		}
	})

	t.Run("each worker takes a different request", func(t *testing.T) {
		const n = 4
		for i := range n {
			if _, err := e.m.CreateBulk(ctx, ledger.CreateBulkInput{IdempotencyKey: fmt.Sprint("claim-many-", i), Transactions: feBulkItems(5, a.ID, b.ID, 1)}); err != nil {
				t.Fatal(err)
			}
		}
		var (
			wg    sync.WaitGroup
			mu    sync.Mutex
			found int
		)
		for range n {
			wg.Go(func() {
				ok, err := e.m.ProcessBulk(ctx)
				if err != nil {
					t.Error(err)
					return
				}
				if ok {
					mu.Lock()
					found++
					mu.Unlock()
				}
			})
		}
		wg.Wait()
		if found != n {
			t.Fatalf("%d of %d workers found a request", found, n)
		}
		if extra := feProcessAll(t, e); extra != 0 {
			t.Fatalf("%d requests were left for a later worker", extra)
		}
	})

	t.Run("a live lease is not taken over", func(t *testing.T) {
		bulk, err := e.m.CreateBulk(ctx, ledger.CreateBulkInput{IdempotencyKey: "leased", Transactions: feBulkItems(3, a.ID, b.ID, 1)})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := e.pool.Exec(ctx, `
			UPDATE ledger_bulk_requests SET status = 'processing', lease_until = now() + interval '1 hour', started_at = now()
			WHERE id = $1`, bulk.ID); err != nil {
			t.Fatal(err)
		}
		if n := feProcessAll(t, e); n != 0 {
			t.Fatalf("claimed %d leased requests", n)
		}
		if _, err := e.pool.Exec(ctx, `UPDATE ledger_bulk_requests SET lease_until = now() - interval '1 second' WHERE id = $1`, bulk.ID); err != nil {
			t.Fatal(err)
		}
		if n := feProcessAll(t, e); n != 1 {
			t.Fatalf("claimed %d expired leases, want 1", n)
		}
	})

	if got := e.balance(t, b.ID); got != 20+20+3 {
		t.Fatalf("b = %d, want 43", got)
	}
	e.verify(t)
}
