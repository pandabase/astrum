package idempotency

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pandabase/astrum/internal/kernel/testdb"
)

type harness struct {
	svc     *Service
	calls   atomic.Int32
	status  atomic.Int32
	release chan struct{}
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	svc := New(nil, testdb.Logger())
	svc.pool = testdb.New(t, map[string]fs.FS{svc.Name(): svc.Migrations()})
	h := &harness{svc: svc}
	h.status.Store(http.StatusCreated)
	return h
}

func (h *harness) handler() http.Handler {
	return h.svc.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := h.calls.Add(1)
		if h.release != nil {
			<-h.release
		}
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(int(h.status.Load()))
		fmt.Fprintf(w, `{"call":%d,"echo":%s}`, n, body)
	}))
}

func send(h http.Handler, method, key, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "/v1/things", strings.NewReader(body))
	if key != "" {
		r.Header.Set(Header, key)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestReplay(t *testing.T) {
	h := newHarness(t)
	handler := h.handler()

	first := send(handler, http.MethodPost, "k1", `{"n":1}`)
	if first.Code != http.StatusCreated || first.Header().Get(ReplayedHeader) != "" {
		t.Fatalf("first = %d replayed=%q", first.Code, first.Header().Get(ReplayedHeader))
	}

	second := send(handler, http.MethodPost, "k1", `{"n":1}`)
	if second.Code != http.StatusCreated {
		t.Fatalf("replay status = %d, want 201", second.Code)
	}
	if second.Header().Get(ReplayedHeader) != "true" {
		t.Fatal("replay missing Idempotent-Replayed header")
	}
	if second.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("replay Content-Type = %q", second.Header().Get("Content-Type"))
	}
	if second.Body.String() != first.Body.String() {
		t.Fatalf("replay body = %q, want %q", second.Body.String(), first.Body.String())
	}
	if n := h.calls.Load(); n != 1 {
		t.Fatalf("handler calls = %d, want 1", n)
	}
}

func TestClientErrorsAreReplayed(t *testing.T) {
	h := newHarness(t)
	h.status.Store(http.StatusUnprocessableEntity)
	handler := h.handler()

	send(handler, http.MethodPost, "k4", `{}`)
	again := send(handler, http.MethodPost, "k4", `{}`)
	if again.Code != http.StatusUnprocessableEntity || again.Header().Get(ReplayedHeader) != "true" {
		t.Fatalf("4xx replay = %d replayed=%q", again.Code, again.Header().Get(ReplayedHeader))
	}
	if n := h.calls.Load(); n != 1 {
		t.Fatalf("handler calls = %d, want 1", n)
	}
}

func TestKeyReuseWithDifferentRequest(t *testing.T) {
	h := newHarness(t)
	handler := h.handler()

	send(handler, http.MethodPost, "k2", `{"amount":"100"}`)

	tests := []struct {
		name   string
		method string
		body   string
	}{
		{"different body", http.MethodPost, `{"amount":"999"}`},
		{"different method", http.MethodPut, `{"amount":"100"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := send(handler, tt.method, "k2", tt.body)
			if w.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d, want 422", w.Code)
			}
		})
	}
	if n := h.calls.Load(); n != 1 {
		t.Fatalf("handler calls = %d, want 1", n)
	}
}

func TestServerErrorReleasesKey(t *testing.T) {
	h := newHarness(t)
	h.status.Store(http.StatusInternalServerError)
	handler := h.handler()

	if w := send(handler, http.MethodPost, "k3", `{}`); w.Code != http.StatusInternalServerError {
		t.Fatalf("first = %d, want 500", w.Code)
	}

	h.status.Store(http.StatusCreated)
	w := send(handler, http.MethodPost, "k3", `{}`)
	if w.Code != http.StatusCreated || w.Header().Get(ReplayedHeader) != "" {
		t.Fatalf("retry = %d replayed=%q, want fresh 201", w.Code, w.Header().Get(ReplayedHeader))
	}
	if n := h.calls.Load(); n != 2 {
		t.Fatalf("handler calls = %d, want 2", n)
	}
}

func TestPanicReleasesKey(t *testing.T) {
	h := newHarness(t)
	panicking := h.svc.Middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	}))

	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("panic was swallowed, want re-panic")
			}
		}()
		send(panicking, http.MethodPost, "kp", `{}`)
	}()

	if w := send(h.handler(), http.MethodPost, "kp", `{}`); w.Code != http.StatusCreated {
		t.Fatalf("retry after panic = %d, want 201", w.Code)
	}
}

func TestConcurrentRequestIsRejectedWhileInFlight(t *testing.T) {
	h := newHarness(t)
	h.release = make(chan struct{})
	handler := h.handler()

	done := make(chan *httptest.ResponseRecorder)
	go func() { done <- send(handler, http.MethodPost, "k5", `{}`) }()

	waitFor(t, func() bool { return h.calls.Load() == 1 })

	if w := send(handler, http.MethodPost, "k5", `{}`); w.Code != http.StatusConflict {
		t.Fatalf("in-flight duplicate = %d, want 409", w.Code)
	}

	close(h.release)
	if w := <-done; w.Code != http.StatusCreated {
		t.Fatalf("original = %d, want 201", w.Code)
	}
	if w := send(handler, http.MethodPost, "k5", `{}`); w.Header().Get(ReplayedHeader) != "true" {
		t.Fatalf("after completion = %d, want replay", w.Code)
	}
}

func TestRetryStormExecutesOnce(t *testing.T) {
	h := newHarness(t)
	handler := h.handler()

	const clients = 20
	var (
		wg        sync.WaitGroup
		mu        sync.Mutex
		statusSet = map[int]int{}
	)
	for range clients {
		wg.Go(func() {
			w := send(handler, http.MethodPost, "storm", `{"n":1}`)
			mu.Lock()
			statusSet[w.Code]++
			mu.Unlock()
		})
	}
	wg.Wait()

	if n := h.calls.Load(); n != 1 {
		t.Fatalf("handler calls = %d, want 1", n)
	}
	if statusSet[http.StatusCreated]+statusSet[http.StatusConflict] != clients {
		t.Fatalf("unexpected statuses: %v", statusSet)
	}
}

func TestStaleLockCanBeTakenOver(t *testing.T) {
	h := newHarness(t)

	h.svc.lockTimeout = 500 * time.Millisecond

	ctx := context.Background()
	hash := requestHash(httptest.NewRequest(http.MethodPost, "/v1/things", nil), []byte(`{}`))
	claimed, err := h.svc.claim(ctx, "crashed", hash, uuid.New())
	if err != nil || !claimed {
		t.Fatalf("initial claim = %v, %v", claimed, err)
	}

	if w := send(h.handler(), http.MethodPost, "crashed", `{}`); w.Code != http.StatusConflict {
		t.Fatalf("before timeout = %d, want 409", w.Code)
	}

	time.Sleep(600 * time.Millisecond)
	if w := send(h.handler(), http.MethodPost, "crashed", `{}`); w.Code != http.StatusCreated {
		t.Fatalf("after timeout = %d, want 201", w.Code)
	}
}

func TestPassThrough(t *testing.T) {
	h := newHarness(t)
	handler := h.handler()

	tests := []struct {
		name   string
		method string
		key    string
	}{
		{"no key", http.MethodPost, ""},
		{"safe method", http.MethodGet, "k6"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before := h.calls.Load()
			send(handler, tt.method, tt.key, `{}`)
			send(handler, tt.method, tt.key, `{}`)
			if got := h.calls.Load() - before; got != 2 {
				t.Fatalf("handler calls = %d, want 2", got)
			}
		})
	}
}

func TestKeyTooLong(t *testing.T) {
	h := newHarness(t)
	w := send(h.handler(), http.MethodPost, strings.Repeat("k", maxKeyLen+1), `{}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

func TestBodyTooLarge(t *testing.T) {
	h := newHarness(t)
	w := send(h.handler(), http.MethodPost, "big", strings.Repeat("x", maxBodySize+1))
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", w.Code)
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met within 5s")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestTakenOverRequestCannotClobber(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	hash := requestHash(httptest.NewRequest(http.MethodPost, "/v1/things", nil), []byte(`{}`))

	stale := uuid.New()
	if claimed, err := h.svc.claim(ctx, "fenced", hash, stale); err != nil || !claimed {
		t.Fatalf("stale claim = %v, %v", claimed, err)
	}
	h.svc.lockTimeout = 0
	time.Sleep(10 * time.Millisecond)
	owner := uuid.New()
	if claimed, err := h.svc.claim(ctx, "fenced", hash, owner); err != nil || !claimed {
		t.Fatalf("takeover claim = %v, %v", claimed, err)
	}

	rec := &recorder{ResponseWriter: httptest.NewRecorder(), status: http.StatusInternalServerError}
	h.svc.finish(ctx, h.svc.log, "fenced", stale, rec)
	rec.status = http.StatusCreated
	h.svc.finish(ctx, h.svc.log, "fenced", stale, rec)

	var token uuid.UUID
	var status string
	if err := h.svc.pool.QueryRow(ctx, `SELECT lock_token, status FROM idempotency_keys WHERE key = 'fenced'`).Scan(&token, &status); err != nil {
		t.Fatalf("record lost: %v", err)
	}
	if token != owner || status != "processing" {
		t.Fatalf("record = %s/%s, want owner's processing record", token, status)
	}
}

func TestPrune(t *testing.T) {
	h := newHarness(t)
	handler := h.handler()
	send(handler, http.MethodPost, "old", `{}`)
	send(handler, http.MethodPost, "new", `{}`)
	ctx := context.Background()

	if n, err := h.svc.Prune(ctx); err != nil || n != 0 {
		t.Fatalf("Prune() = %d, %v; want nothing pruned", n, err)
	}
	if _, err := h.svc.pool.Exec(ctx, `UPDATE idempotency_keys SET created_at = now() - interval '1 hour' WHERE key = 'old'`); err != nil {
		t.Fatal(err)
	}
	h.svc.retention = 30 * time.Minute
	if n, err := h.svc.Prune(ctx); err != nil || n != 1 {
		t.Fatalf("Prune() = %d, %v; want 1", n, err)
	}
}

func TestScopedKeys(t *testing.T) {
	h := newHarness(t)
	h.svc.Scope = func(r *http.Request) string { return r.Header.Get("X-Caller") }
	handler := h.handler()
	as := func(caller, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/v1/things", strings.NewReader(body))
		r.Header.Set(Header, "shared")
		r.Header.Set("X-Caller", caller)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}

	alice := as("alice", `{"who":"alice"}`)
	bob := as("bob", `{"who":"bob"}`)
	if alice.Code != http.StatusCreated || bob.Code != http.StatusCreated || h.calls.Load() != 2 {
		t.Fatalf("alice %d, bob %d, calls %d; want both to run", alice.Code, bob.Code, h.calls.Load())
	}
	if again := as("alice", `{"who":"alice"}`); again.Header().Get(ReplayedHeader) != "true" || !strings.Contains(again.Body.String(), "alice") {
		t.Fatalf("alice replay = %d %s", again.Code, again.Body.String())
	}
	if h.calls.Load() != 2 {
		t.Fatalf("calls = %d after replay, want 2", h.calls.Load())
	}
}
