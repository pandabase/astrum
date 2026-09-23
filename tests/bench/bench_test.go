package bench_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pandabase/astrum/internal/bench"
	"github.com/pandabase/astrum/internal/kernel/auth"
	"github.com/pandabase/astrum/internal/kernel/events"
	"github.com/pandabase/astrum/internal/kernel/httpx"
	"github.com/pandabase/astrum/internal/kernel/idempotency"
	"github.com/pandabase/astrum/internal/kernel/testdb"
	"github.com/pandabase/astrum/internal/modules/ledger"
)

type server struct {
	url    string
	writer string
	reader string
}

func start(t *testing.T) server {
	t.Helper()
	log := testdb.Logger()
	cfg := ledger.Config{SealKey: []byte("bench-seal-key-0123456789abcdef-0123456789"), SweepInterval: 100 * time.Millisecond}
	probeAuth, probeIdem := auth.New(nil, log), idempotency.New(nil, log)
	probeLedger, err := ledger.New(nil, log, cfg)
	if err != nil {
		t.Fatal(err)
	}
	pool := testdb.New(t, map[string]fs.FS{
		probeAuth.Name():   probeAuth.Migrations(),
		probeIdem.Name():   probeIdem.Migrations(),
		"events":           events.Migrations(),
		probeLedger.Name(): probeLedger.Migrations(),
	})

	authn := auth.New(pool, log)
	idem := idempotency.New(pool, log)
	idem.Scope = auth.Scope
	m, err := ledger.New(pool, log, cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Go(func() { _ = m.Run(ctx) })
	t.Cleanup(func() {
		cancel()
		wg.Wait()
	})

	mux := http.NewServeMux()
	authn.Routes(mux)
	m.Routes(mux)
	srv := httptest.NewServer(httpx.Logging(log, authn.Middleware([]string{"/healthz"}, idem.Middleware(mux))))
	t.Cleanup(srv.Close)

	key := func(role auth.Role) string {
		_, token, err := authn.Create(context.Background(), auth.CreateInput{Name: string(role), Role: role})
		if err != nil {
			t.Fatal(err)
		}
		return token
	}
	return server{url: srv.URL, writer: key(auth.RoleWrite), reader: key(auth.RoleRead)}
}

func TestScenarios(t *testing.T) {
	s := start(t)
	tests := []struct {
		scenario     string
		transactions int64
	}{
		{"transfer", 30},
		{"batch", 150},
		{"pending", 30},
		{"hold", 30},
		{"read", 0},
	}
	for _, tt := range tests {
		t.Run(tt.scenario, func(t *testing.T) {
			report, err := bench.Run(context.Background(), bench.Config{
				URL:         s.url,
				Key:         s.writer,
				Scenario:    tt.scenario,
				Operations:  30,
				Concurrency: 4,
				Accounts:    5,
				BatchSize:   5,
				Verify:      true,
			})
			if err != nil {
				t.Fatal(err)
			}
			if report.Operations != 30 || report.Succeeded != 30 || report.Failed != 0 || len(report.Errors) != 0 {
				t.Fatalf("report = %+v", report)
			}
			if report.Transactions != tt.transactions {
				t.Fatalf("transactions = %d, want %d", report.Transactions, tt.transactions)
			}
			if !strings.HasPrefix(report.LedgerID, "ldg_") || report.OperationsPerSecond <= 0 {
				t.Fatalf("report = %+v", report)
			}
			l := report.Latency
			if l.P50 <= 0 || l.P50 > l.P90 || l.P90 > l.P99 || l.P99 > l.P999 || l.P999 > l.Max {
				t.Fatalf("latency = %+v", l)
			}
			if report.Integrity == nil || !report.Integrity.OK {
				t.Fatalf("integrity = %+v", report.Integrity)
			}
		})
	}
}

func TestHotAccounts(t *testing.T) {
	s := start(t)
	report, err := bench.Run(context.Background(), bench.Config{
		URL: s.url, Key: s.writer, Scenario: "transfer", Operations: 40, Concurrency: 8, Accounts: 2, Verify: true,
	})
	if err != nil || report.Succeeded != 40 || !report.Integrity.OK {
		t.Fatalf("report = %+v, %v", report, err)
	}
}

func TestDurationAndRate(t *testing.T) {
	s := start(t)
	var ticks atomic.Int64
	report, err := bench.Run(context.Background(), bench.Config{
		URL: s.url, Key: s.writer, Scenario: "read", Duration: 2 * time.Second, Concurrency: 4, Accounts: 2, Rate: 20,
		Progress: func(bench.Progress) { ticks.Add(1) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.Elapsed < 2*time.Second || report.Elapsed > 4*time.Second {
		t.Fatalf("elapsed = %s", report.Elapsed)
	}
	if report.Operations < 25 || report.Operations > 45 {
		t.Fatalf("operations at 20/s for 2s = %d", report.Operations)
	}
	if ticks.Load() < 1 {
		t.Fatal("progress was never reported")
	}
}

func TestRejectsBadSetup(t *testing.T) {
	s := start(t)
	base := bench.Config{URL: s.url, Key: s.writer, Scenario: "transfer", Operations: 1, Concurrency: 1, Accounts: 2}
	tests := []struct {
		name   string
		change func(*bench.Config)
		want   string
	}{
		{"no key", func(c *bench.Config) { c.Key = "" }, "API key"},
		{"wrong key", func(c *bench.Config) { c.Key = "sk_01h455vb4pex5vsknk084sn02q_" + strings.Repeat("A", 43) }, "rejected"},
		{"read key", func(c *bench.Config) { c.Key = s.reader }, "read key"},
		{"unknown scenario", func(c *bench.Config) { c.Scenario = "mint" }, "unknown scenario"},
		{"one account", func(c *bench.Config) { c.Accounts = 1 }, "at least 2 accounts"},
		{"no stop", func(c *bench.Config) { c.Operations = 0 }, "duration"},
		{"huge batch", func(c *bench.Config) { c.BatchSize = 5000 }, "batch size"},
		{"unknown currency", func(c *bench.Config) { c.Currency = "ZZZ" }, "create account"},
		{"unreachable", func(c *bench.Config) { c.URL = "http://127.0.0.1:1" }, "reach"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := base
			tt.change(&cfg)
			_, err := bench.Run(context.Background(), cfg)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want it to mention %q", err, tt.want)
			}
		})
	}
}

func TestFailuresAreCountedByCode(t *testing.T) {
	var posts atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/me":
			w.Write([]byte(`{"role":"write"}`))
		case r.URL.Path == "/v1/transactions":
			if posts.Add(1)%2 == 0 {
				w.WriteHeader(http.StatusUnprocessableEntity)
				w.Write([]byte(`{"code":"insufficient_funds","detail":"not enough"}`))
				return
			}
			w.WriteHeader(http.StatusCreated)
			w.Write([]byte(`{"id":"txn_1"}`))
		default:
			w.WriteHeader(http.StatusCreated)
			w.Write([]byte(`{"id":"x_1"}`))
		}
	}))
	defer srv.Close()

	report, err := bench.Run(context.Background(), bench.Config{
		URL: srv.URL, Key: "sk_test", Scenario: "transfer", Operations: 10, Concurrency: 1, Accounts: 2, Seed: 7,
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.Succeeded != 5 || report.Failed != 5 || report.Errors["insufficient_funds"] != 5 || report.Transactions != 5 {
		t.Fatalf("report = %+v", report)
	}

	var text bytes.Buffer
	if err := bench.WriteText(&text, report); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"scenario    transfer", "5 ok, 5 failed", "insufficient_funds × 5", "seed 7", "p99.9"} {
		if !strings.Contains(text.String(), want) {
			t.Errorf("text report lacks %q:\n%s", want, text.String())
		}
	}

	raw, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	latency, ok := decoded["latency_ns"].(map[string]any)
	if !ok || latency["p99"] == nil || decoded["errors"].(map[string]any)["insufficient_funds"] != float64(5) {
		t.Fatalf("json report = %s", raw)
	}
}
