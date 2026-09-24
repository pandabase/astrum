package bench_test

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/pandabase/astrum/internal/bench"
)

type fakeAPI struct {
	role      string
	meStatus  int
	meBody    string
	ledger    func(w http.ResponseWriter) bool
	account   func(n int64, w http.ResponseWriter) bool
	txn       func(n int64, w http.ResponseWriter, r *http.Request) bool
	integrity string

	accounts atomic.Int64
	txns     atomic.Int64
	mu       sync.Mutex
	requests []string
	bodies   []string
}

func (f *fakeAPI) serve(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.requests = append(f.requests, r.Method+" "+r.URL.Path)
		if r.Method == http.MethodPost {
			f.bodies = append(f.bodies, r.URL.Path+" "+string(raw))
		}
		f.mu.Unlock()
		switch {
		case r.URL.Path == "/v1/me":
			if f.meStatus != 0 {
				w.WriteHeader(f.meStatus)
			}
			if f.meBody != "" {
				io.WriteString(w, f.meBody)
				return
			}
			role := f.role
			if role == "" {
				role = "write"
			}
			fmt.Fprintf(w, `{"role":%q}`, role)
		case r.URL.Path == "/v1/ledgers":
			if f.ledger != nil && f.ledger(w) {
				return
			}
			w.WriteHeader(http.StatusCreated)
			io.WriteString(w, `{"id":"ldg_fake"}`)
		case r.URL.Path == "/v1/accounts":
			n := f.accounts.Add(1)
			if f.account != nil && f.account(n, w) {
				return
			}
			var body struct {
				Code string `json:"code"`
			}
			_ = json.Unmarshal(raw, &body)
			w.WriteHeader(http.StatusCreated)
			fmt.Fprintf(w, `{"id":"acct_%s"}`, body.Code)
		case r.URL.Path == "/v1/integrity":
			if f.integrity == "" {
				io.WriteString(w, `{"ok":true,"issues":[]}`)
				return
			}
			if strings.HasPrefix(f.integrity, "5") {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			io.WriteString(w, f.integrity)
		default:
			n := f.txns.Add(1)
			if f.txn != nil && f.txn(n, w, r) {
				return
			}
			if r.Method == http.MethodGet {
				io.WriteString(w, `{"id":"acct_x"}`)
				return
			}
			w.WriteHeader(http.StatusCreated)
			fmt.Fprintf(w, `{"id":"obj_%d"}`, n)
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func (f *fakeAPI) seen() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.requests...)
}

func fakeConfig(url string) bench.Config {
	return bench.Config{URL: url, Key: "sk_fake", Scenario: "transfer", Operations: 6, Concurrency: 1, Accounts: 3, Seed: 11}
}

func TestEdgeRunSetupFailures(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		api  *fakeAPI
		want string
	}{
		{"me server error", &fakeAPI{meStatus: 500, meBody: `{"code":"internal","detail":"boom"}`}, "bench: reach"},
		{"me forbidden is not a rejected key", &fakeAPI{meStatus: 403}, "403 forbidden"},
		{"me unauthorized", &fakeAPI{meStatus: 401}, "API key was rejected"},
		{"me garbage", &fakeAPI{meBody: `not json`}, "bench: reach"},
		{"read role", &fakeAPI{role: "read"}, "read key"},
		{"ledger fails", &fakeAPI{ledger: func(w http.ResponseWriter) bool {
			w.WriteHeader(http.StatusUnprocessableEntity)
			io.WriteString(w, `{"code":"invalid_ledger","detail":"nope"}`)
			return true
		}}, "create ledger: 422 invalid_ledger: nope"},
		{"third account fails", &fakeAPI{account: func(n int64, w http.ResponseWriter) bool {
			if n == 3 {
				w.WriteHeader(http.StatusConflict)
				io.WriteString(w, `{"code":"duplicate_code"}`)
				return true
			}
			return false
		}}, "duplicate_code"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			report, err := bench.Run(context.Background(), fakeConfig(tt.api.serve(t)))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Run() = %v, want %q", err, tt.want)
			}
			if report.Operations != 0 || report.LedgerID != "" {
				t.Fatalf("setup failure returned a report: %+v", report)
			}
			for _, r := range tt.api.seen() {
				if strings.HasPrefix(r, "POST /v1/transactions") {
					t.Fatalf("operations ran after setup failed: %v", tt.api.seen())
				}
			}
		})
	}
}

func TestEdgeRunAdminKeyAccepted(t *testing.T) {
	t.Parallel()
	api := &fakeAPI{role: "admin"}
	report, err := bench.Run(context.Background(), fakeConfig(api.serve(t)))
	if err != nil || report.Succeeded != 6 {
		t.Fatalf("Run() = %+v, %v", report, err)
	}
}

func TestEdgeRunSetupRequests(t *testing.T) {
	t.Parallel()
	api := &fakeAPI{}
	cfg := fakeConfig(api.serve(t) + "//")
	cfg.Accounts = 20
	cfg.Concurrency = 32
	cfg.Currency = "EUR"
	report, err := bench.Run(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if api.accounts.Load() != 20 || report.LedgerID != "ldg_fake" || report.Accounts != 20 {
		t.Fatalf("accounts created = %d, report = %+v", api.accounts.Load(), report)
	}
	for _, r := range api.seen() {
		if strings.Contains(r, "//") {
			t.Fatalf("request path %q kept the trailing slash", r)
		}
	}
	codes := map[string]bool{}
	api.mu.Lock()
	defer api.mu.Unlock()
	for _, b := range api.bodies {
		path, body, _ := strings.Cut(b, " ")
		switch path {
		case "/v1/ledgers":
			if !strings.Contains(body, `"name":"bench-transfer-`) || !strings.Contains(body, `"bench":"true"`) {
				t.Fatalf("ledger body = %s", body)
			}
		case "/v1/accounts":
			var a struct {
				LedgerID      string `json:"ledger_id"`
				Code          string `json:"code"`
				Currency      string `json:"currency"`
				NormalSide    string `json:"normal_side"`
				AllowNegative bool   `json:"allow_negative"`
			}
			if err := json.Unmarshal([]byte(body), &a); err != nil {
				t.Fatal(err)
			}
			if a.LedgerID != "ldg_fake" || a.Currency != "EUR" || a.NormalSide != "debit" || !a.AllowNegative {
				t.Fatalf("account body = %s", body)
			}
			codes[a.Code] = true
		}
	}
	for i := range 20 {
		if !codes[fmt.Sprintf("bench-%d", i)] {
			t.Fatalf("account codes = %v, missing bench-%d", codes, i)
		}
	}
}

func TestEdgeRunScenarioRequests(t *testing.T) {
	t.Parallel()
	tests := []struct {
		scenario string
		paths    []string
		txns     int64
	}{
		{"transfer", []string{"POST /v1/transactions"}, 1},
		{"batch", []string{"POST /v1/transactions/batch"}, 7},
		{"pending", []string{"POST /v1/transactions", "POST /v1/transactions/obj_1/post"}, 1},
		{"hold", []string{"POST /v1/holds", "POST /v1/holds/obj_1/capture"}, 1},
		{"read", []string{"GET /v1/accounts/acct_bench-"}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.scenario, func(t *testing.T) {
			api := &fakeAPI{}
			cfg := fakeConfig(api.serve(t))
			cfg.Scenario = tt.scenario
			cfg.Operations = 1
			cfg.BatchSize = 7
			report, err := bench.Run(context.Background(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			if report.Transactions != tt.txns || report.Succeeded != 1 {
				t.Fatalf("report = %+v", report)
			}
			wantBatch := 0
			if tt.scenario == "batch" {
				wantBatch = 7
			}
			if report.BatchSize != wantBatch {
				t.Fatalf("BatchSize = %d, want %d", report.BatchSize, wantBatch)
			}
			seen := api.seen()
			ops := seen[len(seen)-len(tt.paths):]
			for i, p := range tt.paths {
				if !strings.HasPrefix(ops[i], p) {
					t.Fatalf("requests = %v, want %v", ops, tt.paths)
				}
			}
			if tt.scenario == "batch" {
				api.mu.Lock()
				last := api.bodies[len(api.bodies)-1]
				api.mu.Unlock()
				var b struct {
					Atomic       bool `json:"atomic"`
					Transactions []struct {
						Entries []struct {
							Amount string `json:"amount"`
						} `json:"entries"`
					} `json:"transactions"`
				}
				if err := json.Unmarshal([]byte(strings.TrimPrefix(last, "/v1/transactions/batch ")), &b); err != nil {
					t.Fatal(err)
				}
				if !b.Atomic || len(b.Transactions) != 7 {
					t.Fatalf("batch body = %s", last)
				}
			}
		})
	}
}

func TestEdgeRunTwoStepFailures(t *testing.T) {
	t.Parallel()
	tests := []struct {
		scenario string
		failOn   int64
		code     string
	}{
		{"pending", 1, "create_failed"},
		{"pending", 2, "post_failed"},
		{"hold", 1, "create_failed"},
		{"hold", 2, "post_failed"},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("%s step %d", tt.scenario, tt.failOn), func(t *testing.T) {
			api := &fakeAPI{txn: func(n int64, w http.ResponseWriter, _ *http.Request) bool {
				if n == tt.failOn {
					w.WriteHeader(http.StatusUnprocessableEntity)
					fmt.Fprintf(w, `{"code":%q}`, tt.code)
					return true
				}
				return false
			}}
			cfg := fakeConfig(api.serve(t))
			cfg.Scenario = tt.scenario
			cfg.Operations = 1
			report, err := bench.Run(context.Background(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			if report.Operations != 1 || report.Failed != 1 || report.Transactions != 0 || report.Errors[tt.code] != 1 {
				t.Fatalf("report = %+v", report)
			}
			if tt.failOn == 1 && api.txns.Load() != 1 {
				t.Fatalf("second step ran after the first failed: %v", api.seen())
			}
		})
	}
}

func TestEdgeRunTransportErrors(t *testing.T) {
	t.Parallel()
	api := &fakeAPI{txn: func(_ int64, w http.ResponseWriter, _ *http.Request) bool {
		conn, _, err := http.NewResponseController(w).Hijack()
		if err == nil {
			conn.Close()
		}
		return true
	}}
	report, err := bench.Run(context.Background(), fakeConfig(api.serve(t)))
	if err != nil {
		t.Fatal(err)
	}
	if report.Operations != 6 || report.Failed != 6 || report.Succeeded != 0 || report.Errors["transport"] != 6 || len(report.Errors) != 1 {
		t.Fatalf("report = %+v", report)
	}
}

func TestEdgeRunCancelReportsPartialResults(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	api := &fakeAPI{txn: func(n int64, _ http.ResponseWriter, _ *http.Request) bool {
		if n == 4 {
			cancel()
		}
		return false
	}}
	cfg := fakeConfig(api.serve(t))
	cfg.Operations = 1000
	report, err := bench.Run(ctx, cfg)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v, want context.Canceled", err)
	}
	if report.Operations < 3 || report.Operations > 4 || report.Failed != 0 || report.LedgerID != "ldg_fake" || report.Elapsed <= 0 {
		t.Fatalf("partial report = %+v", report)
	}
	if report.Latency.Max <= 0 || report.OperationsPerSecond <= 0 {
		t.Fatalf("partial report latency = %+v", report)
	}
}

func TestEdgeRunCancelWithVerify(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	api := &fakeAPI{txn: func(n int64, _ http.ResponseWriter, _ *http.Request) bool {
		if n == 2 {
			cancel()
		}
		return false
	}}
	cfg := fakeConfig(api.serve(t))
	cfg.Operations = 1000
	cfg.Verify = true
	report, err := bench.Run(ctx, cfg)
	if !errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), "integrity check") {
		t.Fatalf("Run() error = %v", err)
	}
	if report.Integrity != nil || report.Operations == 0 {
		t.Fatalf("report = %+v", report)
	}
}

func TestEdgeRunIntegrity(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		integrity string
		wantErr   bool
		wantOK    bool
		issues    int
	}{
		{"ok", `{"ok":true,"issues":[]}`, false, true, 0},
		{"broken", `{"ok":false,"issues":["seq 4 hash mismatch","drift"]}`, false, false, 2},
		{"server error", "500", true, false, 0},
		{"garbage", `nope`, true, false, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			api := &fakeAPI{integrity: tt.integrity}
			cfg := fakeConfig(api.serve(t))
			cfg.Verify = true
			report, err := bench.Run(context.Background(), cfg)
			if (err != nil) != tt.wantErr {
				t.Fatalf("Run() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr {
				if !strings.Contains(err.Error(), "integrity check") || report.Operations != 6 {
					t.Fatalf("error = %v report = %+v; the report must survive a failed check", err, report)
				}
				return
			}
			if report.Integrity == nil || report.Integrity.OK != tt.wantOK || len(report.Integrity.Issues) != tt.issues {
				t.Fatalf("integrity = %+v", report.Integrity)
			}
		})
	}
}

func TestEdgeRunSeedReproducesOperations(t *testing.T) {
	t.Parallel()
	bodies := func() []string {
		api := &fakeAPI{}
		cfg := fakeConfig(api.serve(t))
		cfg.Operations = 15
		cfg.Seed = 1234
		report, err := bench.Run(context.Background(), cfg)
		if err != nil || report.Seed != 1234 {
			t.Fatalf("Run() = %+v, %v", report, err)
		}
		api.mu.Lock()
		defer api.mu.Unlock()
		var out []string
		for _, b := range api.bodies {
			if strings.HasPrefix(b, "/v1/transactions ") {
				out = append(out, b)
			}
		}
		return out
	}
	a, b := bodies(), bodies()
	if len(a) != 15 || strings.Join(a, "\n") != strings.Join(b, "\n") {
		t.Fatalf("same seed produced different operations:\n%v\n%v", a, b)
	}
}

func TestEdgeRunRandomSeedIsReported(t *testing.T) {
	t.Parallel()
	api := &fakeAPI{}
	cfg := fakeConfig(api.serve(t))
	cfg.Seed = 0
	report, err := bench.Run(context.Background(), cfg)
	if err != nil || report.Seed == 0 {
		t.Fatalf("Run() = seed %d, %v", report.Seed, err)
	}
}

func TestEdgeRunCustomHTTPClient(t *testing.T) {
	t.Parallel()
	var used atomic.Int64
	api := &fakeAPI{}
	url := api.serve(t)
	cfg := fakeConfig(url)
	cfg.HTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		used.Add(1)
		return http.DefaultTransport.RoundTrip(r)
	})}
	if _, err := bench.Run(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	if used.Load() != int64(len(api.seen())) || used.Load() == 0 {
		t.Fatalf("custom client used %d times for %d requests", used.Load(), len(api.seen()))
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestEdgeRunDroppedConnectionRetriedByIdempotencyKey(t *testing.T) {
	t.Parallel()
	api := &fakeAPI{txn: func(n int64, w http.ResponseWriter, _ *http.Request) bool {
		if n != 1 {
			return false
		}
		conn, _, err := http.NewResponseController(w).Hijack()
		if err == nil {
			conn.Close()
		}
		return true
	}}
	report, err := bench.Run(context.Background(), fakeConfig(api.serve(t)))
	if err != nil {
		t.Fatal(err)
	}
	if report.Succeeded != 6 || report.Failed != 0 || api.txns.Load() != 7 {
		t.Fatalf("report = %+v, server saw %d transaction requests", report, api.txns.Load())
	}
}
