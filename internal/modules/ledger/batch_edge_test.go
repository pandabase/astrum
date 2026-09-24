package ledger

import (
	"context"
	"errors"
	"io/fs"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pandabase/astrum/internal/kernel/events"
	"github.com/pandabase/astrum/internal/kernel/testdb"
)

type peEnv struct {
	m      *Module
	ledger Ledger
	open   Account
}

func peSetup(t *testing.T, cfg Config) *peEnv {
	t.Helper()
	cfg.SealKey = []byte("test-seal-key-0123456789abcdef-0123456789")
	cfg.SweepInterval = time.Hour
	probe, err := New(nil, testdb.Logger(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	pool := testdb.New(t, map[string]fs.FS{probe.Name(): probe.Migrations(), "events": events.Migrations()})
	m, err := New(pool, testdb.Logger(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	l, err := m.CreateLedger(context.Background(), CreateLedgerInput{Name: "edge"})
	if err != nil {
		t.Fatal(err)
	}
	e := &peEnv{m: m, ledger: l}
	e.open = e.account(t, Credit, true)
	return e
}

func (e *peEnv) account(t *testing.T, side Side, allowNegative bool) Account {
	t.Helper()
	acc, err := e.m.CreateAccount(context.Background(), CreateAccountInput{
		LedgerID: e.ledger.ID, Code: "acct:" + uuid.NewString(), Currency: "USD", NormalSide: side, AllowNegative: allowNegative,
	})
	if err != nil {
		t.Fatal(err)
	}
	return acc
}

func (e *peEnv) balance(t *testing.T, id uuid.UUID) int64 {
	t.Helper()
	acc, err := e.m.Account(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	n, ok := acc.Posted.Amount.Int64()
	if !ok {
		t.Fatalf("balance %s does not fit", acc.Posted.Amount)
	}
	return n
}

func (e *peEnv) keyExists(t *testing.T, key string) bool {
	t.Helper()
	var exists bool
	if err := e.m.svc.pool.QueryRow(context.Background(),
		`SELECT EXISTS (SELECT 1 FROM ledger_transactions WHERE idempotency_key = $1)`, key).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	return exists
}

func peTransfer(key string, from, to uuid.UUID, n int64) PostInput {
	return PostInput{IdempotencyKey: key, Postings: []Posting{
		{AccountID: to, Side: Debit, Amount: amt(n)},
		{AccountID: from, Side: Credit, Amount: amt(n)},
	}}
}

func peRequest(ctx context.Context, in PostInput) *request {
	return &request{ctx: ctx, e: &entry{in: in}, done: make(chan outcome, 1)}
}

func peOutcomes(t *testing.T, reqs []*request) []outcome {
	t.Helper()
	out := make([]outcome, len(reqs))
	for i, r := range reqs {
		select {
		case out[i] = <-r.done:
		default:
			t.Fatalf("request %d got no outcome", i)
		}
	}
	return out
}

func TestBatchEdgeGroupCommitSkipsCancelledRequests(t *testing.T) {
	t.Parallel()
	e := peSetup(t, Config{})
	x := e.account(t, Debit, false)
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()

	reqs := []*request{
		peRequest(context.Background(), peTransfer("live-1", e.open.ID, x.ID, 3)),
		peRequest(cancelled, peTransfer("dead", e.open.ID, x.ID, 100)),
		peRequest(context.Background(), peTransfer("live-2", e.open.ID, x.ID, 4)),
	}
	e.m.svc.batcher.commit(slices.Clone(reqs))
	out := peOutcomes(t, reqs)

	if out[0].err != nil || out[2].err != nil || out[0].replayed || out[2].replayed {
		t.Fatalf("live outcomes = %+v, %+v", out[0], out[2])
	}
	if !errors.Is(out[1].err, context.Canceled) {
		t.Fatalf("cancelled outcome = %v, want context.Canceled", out[1].err)
	}
	if e.keyExists(t, "dead") {
		t.Fatal("cancelled request was committed")
	}
	if got := e.balance(t, x.ID); got != 7 {
		t.Fatalf("x = %d, want 7", got)
	}

	all := []*request{peRequest(cancelled, peTransfer("dead-1", e.open.ID, x.ID, 1)), peRequest(cancelled, peTransfer("dead-2", e.open.ID, x.ID, 1))}
	e.m.svc.batcher.commit(slices.Clone(all))
	for i, o := range peOutcomes(t, all) {
		if !errors.Is(o.err, context.Canceled) {
			t.Fatalf("all-cancelled outcome %d = %v", i, o.err)
		}
	}
	if e.keyExists(t, "dead-1") || e.keyExists(t, "dead-2") {
		t.Fatal("all-cancelled group wrote entries")
	}
}

func TestBatchEdgeGroupFailureRetriesIndividually(t *testing.T) {
	t.Parallel()
	e := peSetup(t, Config{})
	x, y, z := e.account(t, Debit, false), e.account(t, Debit, false), e.account(t, Debit, false)
	far := time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
	poison := peTransfer("poison", e.open.ID, z.ID, 1_000)
	poison.EffectiveAt = &far

	reqs := []*request{
		peRequest(context.Background(), peTransfer("chain-1", e.open.ID, x.ID, 10)),
		peRequest(context.Background(), peTransfer("chain-2", x.ID, y.ID, 10)),
		peRequest(context.Background(), poison),
		peRequest(context.Background(), peTransfer("chain-3", y.ID, z.ID, 5)),
	}
	e.m.svc.batcher.commit(slices.Clone(reqs))
	out := peOutcomes(t, reqs)

	for _, i := range []int{0, 1, 3} {
		if out[i].err != nil {
			t.Fatalf("entry %d error = %v", i, out[i].err)
		}
	}
	if out[2].err == nil {
		t.Fatal("poison entry succeeded")
	}
	if e.keyExists(t, "poison") {
		t.Fatal("poison entry was written")
	}
	if e.balance(t, x.ID) != 0 || e.balance(t, y.ID) != 5 || e.balance(t, z.ID) != 5 {
		t.Fatalf("x %d y %d z %d, want 0 5 5", e.balance(t, x.ID), e.balance(t, y.ID), e.balance(t, z.ID))
	}
}

func TestBatchEdgeGroupDomainFailuresStayLocal(t *testing.T) {
	t.Parallel()
	e := peSetup(t, Config{})
	x, y := e.account(t, Debit, false), e.account(t, Debit, false)

	reqs := []*request{
		peRequest(context.Background(), peTransfer("g-fund", e.open.ID, x.ID, 10)),
		peRequest(context.Background(), peTransfer("g-broke", x.ID, y.ID, 11)),
		peRequest(context.Background(), peTransfer("g-dup", x.ID, y.ID, 2)),
		peRequest(context.Background(), peTransfer("g-dup", x.ID, y.ID, 3)),
		peRequest(context.Background(), peTransfer("g-dup", x.ID, y.ID, 2)),
		peRequest(context.Background(), peTransfer("g-unknown", x.ID, uuid.New(), 1)),
		peRequest(context.Background(), peTransfer("g-spend", x.ID, y.ID, 8)),
	}
	e.m.svc.batcher.commit(slices.Clone(reqs))
	out := peOutcomes(t, reqs)

	wants := []error{nil, ErrInsufficientFunds, nil, ErrIdempotencyConflict, nil, ErrNotFound, nil}
	for i, want := range wants {
		if !errors.Is(out[i].err, want) {
			t.Fatalf("entry %d error = %v, want %v", i, out[i].err, want)
		}
	}
	if !out[4].replayed || out[4].txn.ID != out[2].txn.ID {
		t.Fatalf("duplicate = %+v", out[4])
	}
	if e.balance(t, x.ID) != 0 || e.balance(t, y.ID) != 10 {
		t.Fatalf("x %d y %d, want 0 10", e.balance(t, x.ID), e.balance(t, y.ID))
	}
}

func TestBatchEdgeGroupConflictThenMatchingReplay(t *testing.T) {
	t.Parallel()
	e := peSetup(t, Config{})
	x := e.account(t, Debit, false)
	first := []*request{peRequest(context.Background(), peTransfer("shared", e.open.ID, x.ID, 1))}
	e.m.svc.batcher.commit(slices.Clone(first))
	committed := peOutcomes(t, first)[0]
	if committed.err != nil {
		t.Fatal(committed.err)
	}

	reqs := []*request{
		peRequest(context.Background(), peTransfer("shared", e.open.ID, x.ID, 2)),
		peRequest(context.Background(), peTransfer("shared", e.open.ID, x.ID, 1)),
	}
	e.m.svc.batcher.commit(slices.Clone(reqs))
	out := peOutcomes(t, reqs)
	if !errors.Is(out[0].err, ErrIdempotencyConflict) {
		t.Fatalf("different body = %v, want conflict", out[0].err)
	}
	if out[1].err != nil || !out[1].replayed || out[1].txn.ID != committed.txn.ID {
		t.Fatalf("matching body = %+v, %v; want replay of %s", out[1].txn.ID, out[1].err, committed.txn.ID)
	}
}

func TestBatchEdgeQueuedRequestCancelledBeforeWorkersRun(t *testing.T) {
	t.Parallel()
	e := peSetup(t, Config{Workers: 1, QueueSize: 1})
	x := e.account(t, Debit, false)
	b := e.m.svc.batcher

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := b.submit(ctx, &entry{in: peTransfer("queued-dead", e.open.ID, x.ID, 1)})
		done <- err
	}()
	deadline := time.Now().Add(5 * time.Second)
	for len(b.queue) != 1 {
		if time.Now().After(deadline) {
			t.Fatal("request never reached the queue")
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("submit error = %v, want context.Canceled", err)
	}

	runCtx, stop := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Go(func() { _ = e.m.Run(runCtx) })
	t.Cleanup(func() {
		stop()
		wg.Wait()
	})

	if _, err := e.m.Post(context.Background(), peTransfer("queued-live", e.open.ID, x.ID, 2)); err != nil {
		t.Fatal(err)
	}
	if e.keyExists(t, "queued-dead") {
		t.Fatal("request cancelled while queued was committed")
	}
	if got := e.balance(t, x.ID); got != 2 {
		t.Fatalf("x = %d, want 2", got)
	}
}

func TestBatchEdgeSubmitHonoursFullQueue(t *testing.T) {
	t.Parallel()
	e := peSetup(t, Config{Workers: 1, QueueSize: 1})
	x := e.account(t, Debit, false)
	b := e.m.svc.batcher
	b.queue <- peRequest(context.Background(), peTransfer("filler", e.open.ID, x.ID, 1))

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := b.submit(ctx, &entry{in: peTransfer("blocked", e.open.ID, x.ID, 1)})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("submit error = %v, want context.DeadlineExceeded", err)
	}
	if len(b.queue) != 1 {
		t.Fatalf("queue length = %d, want only the filler", len(b.queue))
	}
	filler := <-b.queue
	if filler.e.in.IdempotencyKey != "filler" {
		t.Fatalf("unexpected queued request %s", filler.e.in.IdempotencyKey)
	}
}
