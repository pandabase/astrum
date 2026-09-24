package idempotency_test

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pandabase/astrum/internal/kernel/httpx"
	"github.com/pandabase/astrum/internal/kernel/idempotency"
	"github.com/pandabase/astrum/internal/kernel/testdb"
)

type edgeHarness struct {
	t       *testing.T
	svc     *idempotency.Service
	calls   atomic.Int32
	status  atomic.Int32
	release chan struct{}
	srv     *httptest.Server
	pool    *pgxpool.Pool
}

func newEdgeHarness(t *testing.T, scope func(*http.Request) string) *edgeHarness {
	t.Helper()
	probe := idempotency.New(nil, testdb.Logger())
	pool := testdb.New(t, map[string]fs.FS{probe.Name(): probe.Migrations()})
	h := &edgeHarness{t: t, svc: idempotency.New(pool, testdb.Logger()), pool: pool}
	h.svc.Scope = scope
	h.status.Store(http.StatusCreated)
	h.srv = httptest.NewServer(httpx.Logging(testdb.Logger(), h.svc.Middleware(http.HandlerFunc(h.serve))))
	t.Cleanup(h.srv.Close)
	return h
}

func (h *edgeHarness) serve(w http.ResponseWriter, r *http.Request) {
	n := h.calls.Add(1)
	if h.release != nil {
		<-h.release
	}
	body, _ := io.ReadAll(r.Body)
	switch r.URL.Path {
	case "/empty":
		return
	case "/nocontent":
		w.WriteHeader(http.StatusNoContent)
		return
	case "/implicit":
		io.WriteString(w, "implicit")
		return
	case "/secret":
		w.Header().Set("Cache-Control", "no-store")
		httpx.JSON(w, r, http.StatusCreated, map[string]any{"call": n, "secret": "sk_edge_secret_value"})
		return
	case "/headers":
		w.Header().Set("Location", "/v1/things/1")
		w.Header().Set("X-Custom", "yes")
	case "/decode":
		var v map[string]any
		if err := httpx.Decode(w, r, &v); err != nil {
			httpx.Error(w, r, http.StatusBadRequest, httpx.CodeInvalidRequest, err.Error())
			return
		}
	}
	httpx.JSON(w, r, int(h.status.Load()), map[string]any{"call": n, "len": len(body), "method": r.Method, "query": r.URL.RawQuery})
}

type edgeResp struct {
	status int
	header http.Header
	body   []byte
}

func (h *edgeHarness) send(method, path string, keys []string, body string, extra ...string) edgeResp {
	h.t.Helper()
	req, err := http.NewRequest(method, h.srv.URL+path, strings.NewReader(body))
	if err != nil {
		h.t.Fatal(err)
	}
	for _, k := range keys {
		req.Header.Add(idempotency.Header, k)
	}
	for i := 0; i+1 < len(extra); i += 2 {
		req.Header.Set(extra[i], extra[i+1])
	}
	resp, err := h.srv.Client().Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		h.t.Fatal(err)
	}
	return edgeResp{status: resp.StatusCode, header: resp.Header, body: raw}
}

func (h *edgeHarness) post(key, body string) edgeResp {
	h.t.Helper()
	return h.send(http.MethodPost, "/v1/things", []string{key}, body)
}

func problemCode(t *testing.T, r edgeResp) string {
	t.Helper()
	var p httpx.Problem
	if err := json.Unmarshal(r.body, &p); err != nil {
		t.Fatalf("problem %q: %v", r.body, err)
	}
	if r.header.Get("Content-Type") != "application/problem+json" {
		t.Fatalf("problem Content-Type = %q", r.header.Get("Content-Type"))
	}
	return p.Code
}

func TestIdempotencyEdgeReplayIsByteIdentical(t *testing.T) {
	h := newEdgeHarness(t, nil)
	for _, status := range []int{200, 201, 202, 400, 401, 403, 404, 409, 410, 422, 429} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			h.status.Store(int32(status))
			before := h.calls.Load()
			key := fmt.Sprint("replay-", status)
			first := h.post(key, `{"n":1}`)
			second := h.post(key, `{"n":1}`)
			third := h.post(key, `{"n":1}`)
			if first.status != status || second.status != status || third.status != status {
				t.Fatalf("statuses = %d %d %d, want %d", first.status, second.status, third.status, status)
			}
			if !bytes.Equal(first.body, second.body) || !bytes.Equal(first.body, third.body) || !bytes.HasSuffix(second.body, []byte("}\n")) {
				t.Fatalf("bodies differ: %q %q %q", first.body, second.body, third.body)
			}
			if first.header.Get(idempotency.ReplayedHeader) != "" || second.header.Get(idempotency.ReplayedHeader) != "true" {
				t.Fatalf("replayed headers = %q %q", first.header.Get(idempotency.ReplayedHeader), second.header.Get(idempotency.ReplayedHeader))
			}
			if second.header.Get("Content-Type") != first.header.Get("Content-Type") {
				t.Fatalf("Content-Type = %q, want %q", second.header.Get("Content-Type"), first.header.Get("Content-Type"))
			}
			if second.header.Get("X-Request-ID") == first.header.Get("X-Request-ID") {
				t.Fatal("replay reused the original request id header")
			}
			if got := h.calls.Load() - before; got != 1 {
				t.Fatalf("handler ran %d times", got)
			}
		})
	}
}

func TestIdempotencyEdgeServerErrorsAreNotStored(t *testing.T) {
	h := newEdgeHarness(t, nil)
	for _, status := range []int{500, 502, 503, 504} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			key := fmt.Sprint("fail-", status)
			h.status.Store(int32(status))
			before := h.calls.Load()
			if r := h.post(key, `{}`); r.status != status {
				t.Fatalf("first = %d", r.status)
			}
			if r := h.post(key, `{"different":true}`); r.status != status || r.header.Get(idempotency.ReplayedHeader) != "" {
				t.Fatalf("retry with a new body = %d replayed=%q, want a fresh execution", r.status, r.header.Get(idempotency.ReplayedHeader))
			}
			h.status.Store(http.StatusCreated)
			if r := h.post(key, `{}`); r.status != http.StatusCreated {
				t.Fatalf("recovery = %d", r.status)
			}
			if got := h.calls.Load() - before; got != 3 {
				t.Fatalf("handler ran %d times, want 3", got)
			}
		})
	}
}

func TestIdempotencyEdgeResponseShapes(t *testing.T) {
	h := newEdgeHarness(t, nil)

	t.Run("no body and no status", func(t *testing.T) {
		first := h.send(http.MethodPost, "/empty", []string{"empty"}, "")
		second := h.send(http.MethodPost, "/empty", []string{"empty"}, "")
		if first.status != 200 || second.status != 200 || len(second.body) != 0 || second.header.Get(idempotency.ReplayedHeader) != "true" {
			t.Fatalf("empty replay = %d %q %v", second.status, second.body, second.header)
		}
	})

	t.Run("no content", func(t *testing.T) {
		h.send(http.MethodDelete, "/nocontent", []string{"nc"}, "")
		second := h.send(http.MethodDelete, "/nocontent", []string{"nc"}, "")
		if second.status != http.StatusNoContent || len(second.body) != 0 || second.header.Get(idempotency.ReplayedHeader) != "true" {
			t.Fatalf("204 replay = %d %q", second.status, second.body)
		}
	})

	t.Run("implicit status and sniffed content type", func(t *testing.T) {
		first := h.send(http.MethodPost, "/implicit", []string{"implicit"}, "")
		second := h.send(http.MethodPost, "/implicit", []string{"implicit"}, "")
		if first.header.Get("Content-Type") != "text/plain; charset=utf-8" || second.status != 200 || string(second.body) != "implicit" ||
			second.header.Get("Content-Type") != "" || second.header.Get(idempotency.ReplayedHeader) != "true" {
			t.Fatalf("implicit replay = %d %q %q (first %q)", second.status, second.body, second.header.Get("Content-Type"), first.header.Get("Content-Type"))
		}
	})

	t.Run("only content type is replayed", func(t *testing.T) {
		first := h.send(http.MethodPost, "/headers", []string{"hdr"}, "")
		second := h.send(http.MethodPost, "/headers", []string{"hdr"}, "")
		if first.header.Get("Location") == "" || first.header.Get("X-Custom") != "yes" {
			t.Fatalf("first headers = %v", first.header)
		}
		if second.header.Get("Location") != "" || second.header.Get("X-Custom") != "" || second.header.Get("Content-Type") != "application/json" {
			t.Fatalf("replayed headers = %v", second.header)
		}
	})

	t.Run("decode errors inside the handler are stored", func(t *testing.T) {
		big := `{"a":"` + strings.Repeat("x", 1<<20) + `"}`
		first := h.send(http.MethodPost, "/decode", []string{"toolarge"}, big)
		if first.status != http.StatusBadRequest || problemCode(t, first) != httpx.CodeInvalidRequest {
			t.Fatalf("oversized for handler = %d %s", first.status, first.body)
		}
		second := h.send(http.MethodPost, "/decode", []string{"toolarge"}, big)
		if second.status != http.StatusBadRequest || second.header.Get(idempotency.ReplayedHeader) != "true" || !bytes.Equal(first.body, second.body) {
			t.Fatalf("stored 400 replay = %d %q", second.status, second.body)
		}
		if r := h.send(http.MethodPost, "/decode", []string{"toolarge"}, `{"a":"x"}`); r.status != http.StatusUnprocessableEntity || problemCode(t, r) != httpx.CodeIdempotencyReuse {
			t.Fatalf("fixed body with the same key = %d %s", r.status, r.body)
		}
	})
}

func TestIdempotencyEdgeRequestFingerprint(t *testing.T) {
	h := newEdgeHarness(t, nil)
	base := h.send(http.MethodPost, "/v1/things?x=1", []string{"fp"}, `{"a":1}`)
	if base.status != http.StatusCreated {
		t.Fatalf("base = %d", base.status)
	}
	tests := []struct {
		name, method, path, body string
		extra                    []string
		replay                   bool
	}{
		{"identical", http.MethodPost, "/v1/things?x=1", `{"a":1}`, nil, true},
		{"different content type header", http.MethodPost, "/v1/things?x=1", `{"a":1}`, []string{"Content-Type", "text/plain"}, true},
		{"different authorization header", http.MethodPost, "/v1/things?x=1", `{"a":1}`, []string{"Authorization", "Bearer other"}, true},
		{"different body", http.MethodPost, "/v1/things?x=1", `{"a":2}`, nil, false},
		{"reformatted body", http.MethodPost, "/v1/things?x=1", `{ "a": 1 }`, nil, false},
		{"trailing newline", http.MethodPost, "/v1/things?x=1", "{\"a\":1}\n", nil, false},
		{"empty body", http.MethodPost, "/v1/things?x=1", ``, nil, false},
		{"different query", http.MethodPost, "/v1/things?x=2", `{"a":1}`, nil, false},
		{"reordered query", http.MethodPost, "/v1/things?x=1&", `{"a":1}`, nil, false},
		{"no query", http.MethodPost, "/v1/things", `{"a":1}`, nil, false},
		{"different path", http.MethodPost, "/v1/other?x=1", `{"a":1}`, nil, false},
		{"escaped path", http.MethodPost, "/v1/th%69ngs?x=1", `{"a":1}`, nil, false},
		{"different method", http.MethodPut, "/v1/things?x=1", `{"a":1}`, nil, false},
		{"delete", http.MethodDelete, "/v1/things?x=1", `{"a":1}`, nil, false},
		{"patch", http.MethodPatch, "/v1/things?x=1", `{"a":1}`, nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before := h.calls.Load()
			r := h.send(tt.method, tt.path, []string{"fp"}, tt.body, tt.extra...)
			if tt.replay {
				if r.status != base.status || !bytes.Equal(r.body, base.body) || r.header.Get(idempotency.ReplayedHeader) != "true" {
					t.Fatalf("replay = %d %q", r.status, r.body)
				}
			} else if r.status != http.StatusUnprocessableEntity || problemCode(t, r) != httpx.CodeIdempotencyReuse {
				t.Fatalf("mismatch = %d %s, want 422 %s", r.status, r.body, httpx.CodeIdempotencyReuse)
			}
			if h.calls.Load() != before {
				t.Fatal("handler ran for a reused key")
			}
		})
	}
}

func TestIdempotencyEdgeMethodsAndKeys(t *testing.T) {
	h := newEdgeHarness(t, nil)
	for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodOptions, "PROPFIND"} {
		t.Run("ignored for "+method, func(t *testing.T) {
			before := h.calls.Load()
			for range 3 {
				r := h.send(method, "/v1/things", []string{"safe"}, "")
				if r.header.Get(idempotency.ReplayedHeader) != "" {
					t.Fatalf("%s replayed", method)
				}
			}
			if got := h.calls.Load() - before; got != 3 {
				t.Fatalf("%s handler ran %d times, want 3", method, got)
			}
		})
	}
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		t.Run("honored for "+method, func(t *testing.T) {
			before := h.calls.Load()
			h.send(method, "/v1/things", []string{"m-" + method}, "")
			if r := h.send(method, "/v1/things", []string{"m-" + method}, ""); r.header.Get(idempotency.ReplayedHeader) != "true" {
				t.Fatalf("%s not replayed", method)
			}
			if got := h.calls.Load() - before; got != 1 {
				t.Fatalf("%s handler ran %d times, want 1", method, got)
			}
		})
	}

	tests := []struct {
		name   string
		keys   []string
		status int
		stored bool
	}{
		{"one byte", []string{"a"}, 201, true},
		{"at byte limit", []string{strings.Repeat("k", 255)}, 201, true},
		{"over byte limit", []string{strings.Repeat("k", 256)}, 400, false},
		{"multibyte under character limit but over byte limit", []string{strings.Repeat("é", 128)}, 400, false},
		{"multibyte at byte limit", []string{strings.Repeat("é", 127) + "k"}, 201, true},
		{"punctuation and spaces", []string{`a b/c:d?e#f"g'h`}, 201, true},
		{"unicode", []string{"ключ-🔑"}, 201, true},
		{"uuid", []string{"3f6b0c1e-8d2a-4c5b-9e7f-0a1b2c3d4e5f"}, 201, true},
		{"only whitespace is no key", []string{"   "}, 201, false},
		{"empty is no key", []string{""}, 201, false},
		{"surrounding whitespace is trimmed", []string{"  trimmed  "}, 201, true},
		{"first header wins", []string{"first", "second"}, 201, true},
		{"invalid utf8", []string{"bad\xffkey"}, 400, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before := h.calls.Load()
			r := h.send(http.MethodPost, "/v1/things", tt.keys, `{}`)
			if r.status != tt.status {
				t.Fatalf("status = %d %s, want %d", r.status, r.body, tt.status)
			}
			if tt.status == 400 && problemCode(t, r) != httpx.CodeInvalidRequest {
				t.Fatalf("problem = %s", r.body)
			}
			again := h.send(http.MethodPost, "/v1/things", tt.keys, `{}`)
			if replayed := again.header.Get(idempotency.ReplayedHeader) == "true"; replayed != tt.stored {
				t.Fatalf("second request replayed = %v, want %v (%d %s)", replayed, tt.stored, again.status, again.body)
			}
			want := int32(2)
			switch {
			case tt.stored:
				want = 1
			case tt.status == 400:
				want = 0
			}
			if got := h.calls.Load() - before; got != want {
				t.Fatalf("handler ran %d times, want %d", got, want)
			}
		})
	}

	t.Run("trimmed key equals untrimmed", func(t *testing.T) {
		if r := h.send(http.MethodPost, "/v1/things", []string{"trimmed"}, `{}`); r.header.Get(idempotency.ReplayedHeader) != "true" {
			t.Fatalf("trimmed = %d, want replay of the padded key", r.status)
		}
	})
	t.Run("second header is not a key", func(t *testing.T) {
		if r := h.send(http.MethodPost, "/v1/things", []string{"second"}, `{}`); r.header.Get(idempotency.ReplayedHeader) != "" {
			t.Fatal("second header value was used as a key")
		}
	})
}

func TestIdempotencyEdgeScope(t *testing.T) {
	h := newEdgeHarness(t, func(r *http.Request) string { return r.Header.Get("X-Tenant") })
	as := func(tenant, body string) edgeResp {
		t.Helper()
		return h.send(http.MethodPost, "/v1/things", []string{"shared"}, body, "X-Tenant", tenant)
	}
	a := as("a", `{"who":"a"}`)
	b := as("b", `{"who":"b"}`)
	anon := as("", `{"who":"anon"}`)
	if a.status != 201 || b.status != 201 || anon.status != 201 || h.calls.Load() != 3 {
		t.Fatalf("a %d, b %d, anon %d, calls %d; want three executions", a.status, b.status, anon.status, h.calls.Load())
	}
	if r := as("a", `{"who":"a"}`); r.header.Get(idempotency.ReplayedHeader) != "true" || !bytes.Equal(r.body, a.body) {
		t.Fatalf("a replay = %d %q", r.status, r.body)
	}
	if r := as("a", `{"who":"b"}`); r.status != http.StatusUnprocessableEntity {
		t.Fatalf("a with b's body = %d, want 422", r.status)
	}
	if r := as("b", `{"who":"b"}`); !bytes.Equal(r.body, b.body) {
		t.Fatalf("b replay = %q, want %q", r.body, b.body)
	}
	if r := as("", `{"who":"anon"}`); !bytes.Equal(r.body, anon.body) {
		t.Fatalf("unscoped replay = %q", r.body)
	}
	if r := as("A", `{"who":"a"}`); r.header.Get(idempotency.ReplayedHeader) != "" {
		t.Fatal("scopes differing in case share keys")
	}
	if h.calls.Load() != 4 {
		t.Fatalf("calls = %d, want 4", h.calls.Load())
	}
}

func TestIdempotencyEdgeConcurrentSameKey(t *testing.T) {
	h := newEdgeHarness(t, nil)
	h.release = make(chan struct{})
	const clients = 12
	results := make(chan edgeResp, clients)
	for range clients {
		go func() { results <- h.post("race", `{"n":1}`) }()
	}
	var rejected []edgeResp
	for range clients - 1 {
		rejected = append(rejected, <-results)
	}
	if n := h.calls.Load(); n != 1 {
		t.Fatalf("handler entered %d times while one request is in flight", n)
	}
	for _, r := range rejected {
		if r.status != http.StatusConflict || problemCode(t, r) != httpx.CodeIdempotencyBusy {
			t.Fatalf("concurrent duplicate = %d %s, want 409 %s", r.status, r.body, httpx.CodeIdempotencyBusy)
		}
	}
	close(h.release)
	winner := <-results
	if winner.status != http.StatusCreated {
		t.Fatalf("winner = %d", winner.status)
	}
	if r := h.post("race", `{"n":1}`); !bytes.Equal(r.body, winner.body) || r.header.Get(idempotency.ReplayedHeader) != "true" {
		t.Fatalf("after the race = %d %q", r.status, r.body)
	}
	if r := h.post("race", `{"n":2}`); r.status != http.StatusUnprocessableEntity {
		t.Fatalf("different body after the race = %d", r.status)
	}
	if n := h.calls.Load(); n != 1 {
		t.Fatalf("handler ran %d times, want 1", n)
	}
}

func TestIdempotencyEdgeConcurrentDifferentBodyWhileInFlight(t *testing.T) {
	h := newEdgeHarness(t, nil)
	h.release = make(chan struct{})
	done := make(chan edgeResp, 1)
	go func() { done <- h.post("inflight", `{"n":1}`) }()
	for h.calls.Load() == 0 {
		select {
		case r := <-done:
			t.Fatalf("original finished early: %d", r.status)
		case <-time.After(time.Millisecond):
		}
	}
	if r := h.post("inflight", `{"n":2}`); r.status != http.StatusUnprocessableEntity || problemCode(t, r) != httpx.CodeIdempotencyReuse {
		t.Fatalf("different body in flight = %d %s, want 422", r.status, r.body)
	}
	close(h.release)
	if r := <-done; r.status != http.StatusCreated {
		t.Fatalf("original = %d", r.status)
	}
}

func TestIdempotencyEdgeBodyLimit(t *testing.T) {
	h := newEdgeHarness(t, nil)
	atLimit := strings.Repeat("x", httpx.MaxBulkBodyBytes)
	r := h.post("limit", atLimit)
	var out map[string]any
	if err := json.Unmarshal(r.body, &out); err != nil || r.status != http.StatusCreated || out["len"] != float64(httpx.MaxBulkBodyBytes) {
		t.Fatalf("at limit = %d %v, want the handler to see the full body", r.status, out)
	}
	if again := h.post("limit", atLimit); again.header.Get(idempotency.ReplayedHeader) != "true" {
		t.Fatalf("at limit replay = %d", again.status)
	}

	over := h.post("over", atLimit+"x")
	if over.status != http.StatusRequestEntityTooLarge || problemCode(t, over) != httpx.CodeRequestTooLarge {
		t.Fatalf("over limit = %d %s", over.status, over.body)
	}
	before := h.calls.Load()
	if r := h.post("over", `{}`); r.status != http.StatusCreated || r.header.Get(idempotency.ReplayedHeader) != "" {
		t.Fatalf("key after 413 = %d, want the key to be unused", r.status)
	}
	if h.calls.Load()-before != 1 {
		t.Fatal("handler did not run after the 413")
	}

	before = h.calls.Load()
	if r := h.send(http.MethodPost, "/v1/things", nil, atLimit+"x"); r.status != http.StatusCreated {
		t.Fatalf("oversized without a key = %d, want the middleware to pass it through", r.status)
	}
	if h.calls.Load()-before != 1 {
		t.Fatal("handler did not run for a keyless oversized request")
	}
}

func TestIdempotencyEdgeRunAndPrune(t *testing.T) {
	h := newEdgeHarness(t, nil)
	h.post("fresh", `{}`)
	if n, err := h.svc.Prune(context.Background()); err != nil || n != 0 {
		t.Fatalf("Prune = %d, %v; want fresh keys kept", n, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- h.svc.Run(ctx) }()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run = %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not stop after cancel")
	}
	if r := h.post("fresh", `{}`); r.header.Get(idempotency.ReplayedHeader) != "true" {
		t.Fatal("key lost after Run")
	}
}

func TestIdempotencyEdgeSecretsAreNeverStored(t *testing.T) {
	h := newEdgeHarness(t, nil)
	first := h.send(http.MethodPost, "/secret", []string{"secret-key"}, `{}`)
	if first.status != http.StatusCreated || !strings.Contains(string(first.body), "sk_edge_secret_value") {
		t.Fatalf("first = %d %s", first.status, first.body)
	}
	for range 2 {
		replay := h.send(http.MethodPost, "/secret", []string{"secret-key"}, `{}`)
		if replay.status != http.StatusConflict || problemCode(t, replay) != httpx.CodeIdempotencyDone {
			t.Fatalf("replay = %d %s", replay.status, replay.body)
		}
		if strings.Contains(string(replay.body), "sk_edge_secret_value") {
			t.Fatalf("replay leaked the secret: %s", replay.body)
		}
	}
	if n := h.calls.Load(); n != 1 {
		t.Fatalf("handler ran %d times, want 1", n)
	}
	var stored int
	if err := h.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM idempotency_keys WHERE position('sk_edge_secret_value' in convert_from(response_body, 'UTF8')) > 0`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != 0 {
		t.Fatalf("%d stored responses contain the secret", stored)
	}
	if other := h.send(http.MethodPost, "/secret", []string{"secret-key"}, `{"changed":true}`); other.status != http.StatusUnprocessableEntity {
		t.Fatalf("different body = %d %s, want 422", other.status, other.body)
	}
}
