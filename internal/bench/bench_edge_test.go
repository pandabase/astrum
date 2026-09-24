package bench

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func durations(ns ...int64) []time.Duration {
	out := make([]time.Duration, len(ns))
	for i, n := range ns {
		out[i] = time.Duration(n)
	}
	return out
}

func series(n int) []time.Duration {
	out := make([]time.Duration, n)
	for i := range out {
		out[i] = time.Duration(i + 1)
	}
	return out
}

func TestPercentileEdge(t *testing.T) {
	tests := []struct {
		name   string
		sorted []time.Duration
		q      float64
		want   time.Duration
	}{
		{"single p0", durations(7), 0, 7},
		{"single p50", durations(7), 0.5, 7},
		{"single p100", durations(7), 1, 7},
		{"two p50 rounds down to first", durations(1, 2), 0.5, 1},
		{"two p74 first", durations(1, 2), 0.74, 1},
		{"two p75 second", durations(1, 2), 0.75, 2},
		{"two p90", durations(1, 2), 0.9, 2},
		{"two p0 clamps low", durations(1, 2), 0, 1},
		{"two above one clamps high", durations(1, 2), 2, 2},
		{"ten p50", series(10), 0.5, 5},
		{"ten p90", series(10), 0.9, 9},
		{"ten p99", series(10), 0.99, 10},
		{"hundred p50", series(100), 0.5, 50},
		{"hundred p90", series(100), 0.9, 90},
		{"hundred p99", series(100), 0.99, 99},
		{"hundred p999", series(100), 0.999, 100},
		{"thousand p999", series(1000), 0.999, 999},
		{"thousand p99", series(1000), 0.99, 990},
		{"negative q clamps", series(10), -1, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := percentile(tt.sorted, tt.q); got != tt.want {
				t.Fatalf("percentile(%v, %v) = %d, want %d", tt.sorted, tt.q, got, tt.want)
			}
		})
	}
}

func TestSummarizeEdge(t *testing.T) {
	tests := []struct {
		name string
		in   []time.Duration
		want Latency
	}{
		{"nil", nil, Latency{}},
		{"empty", []time.Duration{}, Latency{}},
		{"one", durations(4), Latency{Mean: 4, P50: 4, P90: 4, P99: 4, P999: 4, Max: 4}},
		{"two truncating mean", durations(2, 1), Latency{Mean: 1, P50: 1, P90: 2, P99: 2, P999: 2, Max: 2}},
		{"identical", durations(3, 3, 3), Latency{Mean: 3, P50: 3, P90: 3, P99: 3, P999: 3, Max: 3}},
		{"hundred reversed", reversed(series(100)), Latency{Mean: 50, P50: 50, P90: 90, P99: 99, P999: 100, Max: 100}},
		{"zero latency", durations(0, 0), Latency{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before := slices.Clone(tt.in)
			if got := summarize(tt.in); got != tt.want {
				t.Fatalf("summarize() = %+v, want %+v", got, tt.want)
			}
			if !slices.Equal(before, tt.in) {
				t.Fatalf("summarize mutated its input: %v, was %v", tt.in, before)
			}
		})
	}
}

func reversed(in []time.Duration) []time.Duration {
	slices.Reverse(in)
	return in
}

func TestRecorderBucketsErrors(t *testing.T) {
	rec := newRecorder()
	s := &sample{}
	rec.record(s, 1, 3, nil)
	rec.record(s, 2, 5, &apiError{Status: 422, Code: "insufficient_funds"})
	rec.record(s, 3, 5, fmt.Errorf("wrapped: %w", &apiError{Status: 409, Code: "conflict"}))
	rec.record(s, 4, 5, errors.New("dial tcp: refused"))
	rec.record(s, 5, 5, context.DeadlineExceeded)
	rec.record(s, 6, 5, &apiError{Status: 599})
	rec.merge(s)

	if rec.operations.Load() != 6 || rec.succeeded.Load() != 1 || rec.failed.Load() != 5 {
		t.Fatalf("counts = %d/%d/%d", rec.operations.Load(), rec.succeeded.Load(), rec.failed.Load())
	}
	if rec.transactions.Load() != 3 {
		t.Fatalf("transactions = %d, want only successful ones counted", rec.transactions.Load())
	}
	want := map[string]int64{"insufficient_funds": 1, "conflict": 1, "transport": 2, "": 1}
	if len(rec.errors) != len(want) {
		t.Fatalf("errors = %v, want %v", rec.errors, want)
	}
	for k, v := range want {
		if rec.errors[k] != v {
			t.Fatalf("errors = %v, want %v", rec.errors, want)
		}
	}
	if !slices.Equal(rec.latencies, durations(1, 2, 3, 4, 5, 6)) {
		t.Fatalf("latencies = %v", rec.latencies)
	}
}

func TestRecorderConcurrentMerge(t *testing.T) {
	rec := newRecorder()
	var wg sync.WaitGroup
	for w := range 8 {
		wg.Go(func() {
			s := &sample{}
			defer rec.merge(s)
			for i := range 100 {
				var err error
				if i%4 == 0 {
					err = &apiError{Code: fmt.Sprintf("code_%d", w%2)}
				}
				rec.record(s, time.Duration(i), 1, err)
			}
		})
	}
	wg.Wait()
	if len(rec.latencies) != 800 || rec.operations.Load() != 800 || rec.failed.Load() != 200 {
		t.Fatalf("latencies=%d operations=%d failed=%d", len(rec.latencies), rec.operations.Load(), rec.failed.Load())
	}
	if rec.errors["code_0"] != 100 || rec.errors["code_1"] != 100 {
		t.Fatalf("errors = %v", rec.errors)
	}
}

func TestNormalizeEdge(t *testing.T) {
	valid := Config{URL: "http://x", Key: "k", Scenario: "transfer", Operations: 1, Concurrency: 1, Accounts: 2}
	tests := []struct {
		name    string
		change  func(*Config)
		wantErr string
		check   func(*testing.T, Config)
	}{
		{"valid", func(*Config) {}, "", nil},
		{"trailing slashes trimmed", func(c *Config) { c.URL = "http://x/api///" }, "", func(t *testing.T, c Config) {
			if c.URL != "http://x/api" {
				t.Fatalf("URL = %q", c.URL)
			}
		}},
		{"only slashes becomes empty", func(c *Config) { c.URL = "///" }, "url is required", nil},
		{"empty url", func(c *Config) { c.URL = "" }, "url is required", nil},
		{"empty key", func(c *Config) { c.Key = "" }, "API key", nil},
		{"defaults filled", func(c *Config) { c.Currency, c.BatchSize, c.Seed = "", 0, 0 }, "", func(t *testing.T, c Config) {
			if c.Currency != "USD" || c.BatchSize != 100 || c.Seed == 0 {
				t.Fatalf("defaults = %q %d %d", c.Currency, c.BatchSize, c.Seed)
			}
		}},
		{"explicit seed kept", func(c *Config) { c.Seed = 42 }, "", func(t *testing.T, c Config) {
			if c.Seed != 42 {
				t.Fatalf("Seed = %d", c.Seed)
			}
		}},
		{"no duration or operations", func(c *Config) { c.Operations = 0 }, "set a duration", nil},
		{"negative duration", func(c *Config) { c.Operations, c.Duration = 0, -time.Second }, "set a duration", nil},
		{"negative operations", func(c *Config) { c.Operations = -1 }, "set a duration", nil},
		{"negative operations with duration", func(c *Config) { c.Operations, c.Duration = -1, time.Second }, "", nil},
		{"zero concurrency", func(c *Config) { c.Concurrency = 0 }, "concurrency", nil},
		{"negative concurrency", func(c *Config) { c.Concurrency = -3 }, "concurrency", nil},
		{"one account", func(c *Config) { c.Accounts = 1 }, "at least 2 accounts", nil},
		{"zero accounts", func(c *Config) { c.Accounts = 0 }, "at least 2 accounts", nil},
		{"negative accounts", func(c *Config) { c.Accounts = -2 }, "at least 2 accounts", nil},
		{"batch one", func(c *Config) { c.BatchSize = 1 }, "", nil},
		{"batch thousand", func(c *Config) { c.BatchSize = 1000 }, "", nil},
		{"batch thousand and one", func(c *Config) { c.BatchSize = 1001 }, "batch size", nil},
		{"negative batch", func(c *Config) { c.BatchSize = -1 }, "batch size", nil},
		{"negative rate", func(c *Config) { c.Rate = -0.5 }, "rate cannot be negative", nil},
		{"fractional rate", func(c *Config) { c.Rate = 0.5 }, "", nil},
		{"unknown scenario", func(c *Config) { c.Scenario = "mint" }, `unknown scenario "mint"`, nil},
		{"empty scenario", func(c *Config) { c.Scenario = "" }, "unknown scenario", nil},
		{"scenario is case sensitive", func(c *Config) { c.Scenario = "Transfer" }, "unknown scenario", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := valid
			tt.change(&cfg)
			err := cfg.normalize()
			switch {
			case tt.wantErr == "" && err != nil:
				t.Fatalf("normalize() = %v", err)
			case tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)):
				t.Fatalf("normalize() = %v, want %q", err, tt.wantErr)
			}
			if tt.check != nil {
				tt.check(t, cfg)
			}
		})
	}
}

func TestScenarioList(t *testing.T) {
	names := Scenarios()
	if len(names) != len(scenarios) {
		t.Fatalf("Scenarios() = %v, registry has %d", names, len(scenarios))
	}
	for _, name := range names {
		if _, ok := scenarios[name]; !ok {
			t.Fatalf("Scenarios() lists unregistered %q", name)
		}
	}
	names[0] = "mutated"
	if Scenarios()[0] != "transfer" {
		t.Fatal("Scenarios() shares its backing array")
	}
	if _, err := scenarioStep(&run{cfg: Config{Scenario: "nope"}}); err == nil || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("scenarioStep(nope) = %v", err)
	}
}

func TestPairNeverSelf(t *testing.T) {
	for _, n := range []int{2, 3, 10} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			r := &run{accounts: make([]string, n)}
			for i := range r.accounts {
				r.accounts[i] = fmt.Sprint(i)
			}
			rng := rand.New(rand.NewPCG(1, 2))
			seen := map[[2]string]bool{}
			for range 5000 {
				from, to := r.pair(rng)
				if from == to {
					t.Fatalf("pair returned %s twice", from)
				}
				seen[[2]string{from, to}] = true
			}
			if len(seen) != n*(n-1) {
				t.Fatalf("pairs covered = %d, want %d", len(seen), n*(n-1))
			}
		})
	}
}

func TestAmountRange(t *testing.T) {
	r := &run{}
	rng := rand.New(rand.NewPCG(3, 4))
	lo, hi := 1<<30, 0
	for range 200000 {
		var n int
		if _, err := fmt.Sscan(r.amount(rng), &n); err != nil {
			t.Fatal(err)
		}
		lo, hi = min(lo, n), max(hi, n)
	}
	if lo != 1 || hi != 10000 {
		t.Fatalf("amount range = [%d, %d], want [1, 10000]", lo, hi)
	}
}

func TestSeedIsDeterministic(t *testing.T) {
	r := &run{accounts: []string{"a", "b", "c", "d"}}
	draw := func() []string {
		rng := rand.New(rand.NewPCG(99, 0))
		var out []string
		for range 20 {
			body := r.transferBody(rng, "")
			out = append(out, body.Entries[0].AccountID+body.Entries[1].AccountID+body.Entries[0].Amount)
		}
		return out
	}
	if !slices.Equal(draw(), draw()) {
		t.Fatal("same seed produced different operations")
	}
}

func TestTransferBodyBalances(t *testing.T) {
	r := &run{accounts: []string{"a", "b"}}
	rng := rand.New(rand.NewPCG(5, 6))
	body := r.transferBody(rng, "pending")
	if body.Status != "pending" || len(body.Entries) != 2 {
		t.Fatalf("body = %+v", body)
	}
	d, c := body.Entries[0], body.Entries[1]
	if d.Side != "debit" || c.Side != "credit" || d.Amount != c.Amount || d.AccountID == c.AccountID {
		t.Fatalf("entries = %+v", body.Entries)
	}
	raw, err := json.Marshal(r.transferBody(rng, ""))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "status") {
		t.Fatalf("empty status was encoded: %s", raw)
	}
}

func TestScheduleEdge(t *testing.T) {
	t.Run("zero rate has no schedule", func(t *testing.T) {
		r := &run{cfg: Config{Rate: 0, Concurrency: 1}}
		if r.schedule(context.Background(), time.Now()) != nil {
			t.Fatal("schedule for rate 0 is not nil")
		}
	})
	t.Run("ticks are exact multiples of the interval", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		r := &run{cfg: Config{Rate: 2000, Concurrency: 4}}
		start := time.Now()
		ticks := r.schedule(ctx, start)
		for k := range 20 {
			got := <-ticks
			if want := start.Add(time.Duration(k) * 500 * time.Microsecond); !got.Equal(want) {
				t.Fatalf("tick %d = %v, want %v", k, got.Sub(start), want.Sub(start))
			}
		}
	})
	t.Run("rate above one per nanosecond has zero interval", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		r := &run{cfg: Config{Rate: 1e10, Concurrency: 1}}
		start := time.Now()
		ticks := r.schedule(ctx, start)
		for range 5 {
			if got := <-ticks; !got.Equal(start) {
				t.Fatalf("tick = %v, want all ticks at start", got.Sub(start))
			}
		}
	})
	t.Run("canceled context stops ticks", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		r := &run{cfg: Config{Rate: 0.001, Concurrency: 1}}
		ticks := r.schedule(ctx, time.Now())
		<-ticks
		cancel()
		select {
		case tick := <-ticks:
			t.Fatalf("tick %v after cancel", tick)
		case <-time.After(50 * time.Millisecond):
		}
	})
}

func TestDriveOperationsLimit(t *testing.T) {
	for _, tt := range []struct {
		ops         int64
		concurrency int
	}{{1, 1}, {7, 3}, {3, 16}, {100, 8}} {
		t.Run(fmt.Sprintf("%d ops %d workers", tt.ops, tt.concurrency), func(t *testing.T) {
			r := &run{cfg: Config{Operations: tt.ops, Concurrency: tt.concurrency, Seed: 1}}
			var calls atomic.Int64
			rec, _ := r.drive(context.Background(), func(context.Context, *rand.Rand) (int, error) {
				calls.Add(1)
				return 2, nil
			})
			if calls.Load() != tt.ops || rec.operations.Load() != tt.ops || rec.transactions.Load() != 2*tt.ops {
				t.Fatalf("calls=%d operations=%d transactions=%d, want %d", calls.Load(), rec.operations.Load(), rec.transactions.Load(), tt.ops)
			}
			if int64(len(rec.latencies)) != tt.ops {
				t.Fatalf("latencies = %d", len(rec.latencies))
			}
		})
	}
}

func TestDriveDuration(t *testing.T) {
	r := &run{cfg: Config{Duration: 60 * time.Millisecond, Concurrency: 2, Seed: 1}}
	rec, elapsed := r.drive(context.Background(), func(ctx context.Context, _ *rand.Rand) (int, error) {
		select {
		case <-time.After(time.Millisecond):
			return 1, nil
		case <-ctx.Done():
			return 0, ctx.Err()
		}
	})
	if elapsed < 60*time.Millisecond {
		t.Fatalf("elapsed = %s, want at least the duration", elapsed)
	}
	if rec.operations.Load() == 0 || rec.failed.Load() != 0 {
		t.Fatalf("operations=%d failed=%d; operations cut off by the deadline must not count as failures", rec.operations.Load(), rec.failed.Load())
	}
}

func TestDriveCancellationKeepsCompleted(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := &run{cfg: Config{Operations: 1000, Concurrency: 1, Seed: 1}}
	var calls atomic.Int64
	rec, _ := r.drive(ctx, func(ctx context.Context, _ *rand.Rand) (int, error) {
		if calls.Add(1) == 5 {
			cancel()
			return 0, ctx.Err()
		}
		return 1, nil
	})
	if rec.operations.Load() != 4 || rec.succeeded.Load() != 4 || rec.failed.Load() != 0 {
		t.Fatalf("operations=%d succeeded=%d failed=%d, want the 4 completed before cancel", rec.operations.Load(), rec.succeeded.Load(), rec.failed.Load())
	}
	if calls.Load() != 5 {
		t.Fatalf("calls after cancel = %d, want 5", calls.Load())
	}
}

func TestDriveCancellationRecordsSuccessRacingCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := &run{cfg: Config{Operations: 1000, Concurrency: 1, Seed: 1}}
	rec, _ := r.drive(ctx, func(context.Context, *rand.Rand) (int, error) {
		cancel()
		return 1, nil
	})
	if rec.operations.Load() != 1 || rec.succeeded.Load() != 1 {
		t.Fatalf("operations=%d succeeded=%d, want the success that finished during cancel", rec.operations.Load(), rec.succeeded.Load())
	}
}

func TestDrivePreCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, rate := range []float64{0, 100} {
		r := &run{cfg: Config{Operations: 10, Concurrency: 4, Rate: rate, Seed: 1}}
		var calls atomic.Int64
		rec, _ := r.drive(ctx, func(context.Context, *rand.Rand) (int, error) {
			calls.Add(1)
			return 1, nil
		})
		if calls.Load() != 0 || rec.operations.Load() != 0 {
			t.Fatalf("rate %v: calls=%d operations=%d on a canceled context", rate, calls.Load(), rec.operations.Load())
		}
	}
}

func TestDriveFixedRateAccountsForCoordinatedOmission(t *testing.T) {
	const work = 20 * time.Millisecond
	r := &run{cfg: Config{Operations: 6, Concurrency: 1, Rate: 1000, Seed: 1}}
	rec, _ := r.drive(context.Background(), func(context.Context, *rand.Rand) (int, error) {
		time.Sleep(work)
		return 1, nil
	})
	l := summarize(rec.latencies)
	if l.Max < 5*work {
		t.Fatalf("max latency = %s; queueing behind slow operations must count from the scheduled time (want >= %s)", l.Max, 5*work)
	}
	if l.Mean < 3*work {
		t.Fatalf("mean latency = %s, want >= %s", l.Mean, 3*work)
	}
}

func TestDriveFixedRatePaces(t *testing.T) {
	r := &run{cfg: Config{Operations: 11, Concurrency: 4, Rate: 200, Seed: 1}}
	_, elapsed := r.drive(context.Background(), func(context.Context, *rand.Rand) (int, error) { return 1, nil })
	if elapsed < 50*time.Millisecond {
		t.Fatalf("11 operations at 200/s took %s, want at least 50ms", elapsed)
	}
}

func TestDriveWorkersGetDistinctStreams(t *testing.T) {
	r := &run{cfg: Config{Operations: 40, Concurrency: 4, Seed: 3}}
	var mu sync.Mutex
	draws := map[uint64]int{}
	r.drive(context.Background(), func(_ context.Context, rng *rand.Rand) (int, error) {
		v := rng.Uint64()
		mu.Lock()
		draws[v]++
		mu.Unlock()
		return 1, nil
	})
	for v, n := range draws {
		if n > 1 {
			t.Fatalf("value %d drawn %d times; workers share a random stream", v, n)
		}
	}
}

func TestProgressNilIsNoop(t *testing.T) {
	r := &run{}
	stop := r.progress(newRecorder(), time.Now())
	stop()
}

func TestClientDo(t *testing.T) {
	var (
		mu      sync.Mutex
		headers []http.Header
		bodies  []string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		mu.Lock()
		headers = append(headers, r.Header.Clone())
		bodies = append(bodies, string(raw))
		mu.Unlock()
		switch r.URL.Path {
		case "/problem":
			w.Header().Set("Content-Type", "application/problem+json")
			w.WriteHeader(http.StatusConflict)
			w.Write([]byte(`{"code":"version_conflict","detail":"stale"}`))
		case "/plain":
			w.WriteHeader(http.StatusServiceUnavailable)
			w.Write([]byte("upstream down"))
		case "/teapot":
			w.WriteHeader(http.StatusTeapot)
		case "/unknown":
			w.WriteHeader(599)
		case "/multiple":
			w.WriteHeader(http.StatusMultipleChoices)
		case "/edge":
			w.WriteHeader(299)
			w.Write([]byte(`{"id":"x"}`))
		case "/garbage":
			w.Write([]byte(`{not json`))
		case "/empty":
			w.WriteHeader(http.StatusNoContent)
		default:
			w.Write([]byte(`{"id":"ok_1"}`))
		}
	}))
	defer srv.Close()
	c := &client{http: srv.Client(), base: srv.URL, key: "sk_secret"}
	ctx := context.Background()

	t.Run("problem code", func(t *testing.T) {
		err := c.do(ctx, "POST", "/problem", map[string]string{"a": "b"}, true, nil)
		apiErr, ok := errors.AsType[*apiError](err)
		if !ok || apiErr.Status != 409 || apiErr.Code != "version_conflict" || apiErr.Detail != "stale" {
			t.Fatalf("err = %#v", err)
		}
		if err.Error() != "409 version_conflict: stale" {
			t.Fatalf("Error() = %q", err.Error())
		}
	})
	statusCodes := []struct {
		path string
		code string
		st   int
	}{
		{"/plain", "service_unavailable", 503},
		{"/teapot", "i'm_a_teapot", 418},
		{"/unknown", "", 599},
		{"/multiple", "multiple_choices", 300},
	}
	for _, tt := range statusCodes {
		t.Run("status text "+tt.path, func(t *testing.T) {
			err := c.do(ctx, "GET", tt.path, nil, false, nil)
			apiErr, ok := errors.AsType[*apiError](err)
			if !ok || apiErr.Status != tt.st || apiErr.Code != tt.code {
				t.Fatalf("err = %#v, want status %d code %q", err, tt.st, tt.code)
			}
		})
	}
	t.Run("2xx edge is success", func(t *testing.T) {
		var out resource
		if err := c.do(ctx, "GET", "/edge", nil, false, &out); err != nil || out.ID != "x" {
			t.Fatalf("do = %v, %+v", err, out)
		}
	})
	t.Run("undecodable success body", func(t *testing.T) {
		var out resource
		if err := c.do(ctx, "GET", "/garbage", nil, false, &out); err == nil {
			t.Fatal("garbage body decoded without error")
		}
		if err := c.do(ctx, "GET", "/garbage", nil, false, nil); err != nil {
			t.Fatalf("body is read even when discarded: %v", err)
		}
	})
	t.Run("empty body into out", func(t *testing.T) {
		var out resource
		if err := c.do(ctx, "GET", "/empty", nil, false, &out); err == nil {
			t.Fatal("empty 204 body decoded into a struct without error")
		}
	})
	t.Run("unencodable body", func(t *testing.T) {
		if err := c.do(ctx, "POST", "/ok", map[string]any{"c": make(chan int)}, false, nil); err == nil {
			t.Fatal("unencodable body accepted")
		}
	})
	t.Run("bad method", func(t *testing.T) {
		if err := c.do(ctx, "BAD METHOD", "/ok", nil, false, nil); err == nil {
			t.Fatal("invalid method accepted")
		}
	})
	t.Run("headers", func(t *testing.T) {
		mu.Lock()
		headers, bodies = nil, nil
		mu.Unlock()
		for range 2 {
			if err := c.do(ctx, "POST", "/ok", map[string]int{"n": 1}, true, nil); err != nil {
				t.Fatal(err)
			}
		}
		if err := c.do(ctx, "GET", "/ok", nil, false, nil); err != nil {
			t.Fatal(err)
		}
		mu.Lock()
		defer mu.Unlock()
		for i, h := range headers {
			if h.Get("Authorization") != "Bearer sk_secret" || h.Get("Accept") != "application/json" {
				t.Fatalf("request %d headers = %v", i, h)
			}
		}
		a, b := headers[0].Get("Idempotency-Key"), headers[1].Get("Idempotency-Key")
		if !strings.HasPrefix(a, "bench-") || !strings.HasPrefix(b, "bench-") || a == b {
			t.Fatalf("idempotency keys = %q, %q; want distinct bench- keys", a, b)
		}
		if headers[0].Get("Content-Type") != "application/json" || bodies[0] != `{"n":1}` {
			t.Fatalf("body request = %v %q", headers[0], bodies[0])
		}
		if headers[2].Get("Idempotency-Key") != "" || headers[2].Get("Content-Type") != "" || bodies[2] != "" {
			t.Fatalf("GET request = %v %q", headers[2], bodies[2])
		}
	})
	t.Run("transport error is not an apiError", func(t *testing.T) {
		dead := httptest.NewServer(http.NotFoundHandler())
		dead.Close()
		dc := &client{http: http.DefaultClient, base: dead.URL, key: "k"}
		err := dc.do(ctx, "GET", "/ok", nil, false, nil)
		if err == nil {
			t.Fatal("request to closed server succeeded")
		}
		if _, ok := errors.AsType[*apiError](err); ok {
			t.Fatalf("transport failure reported as apiError: %v", err)
		}
	})
	t.Run("canceled context", func(t *testing.T) {
		cctx, cancel := context.WithCancel(ctx)
		cancel()
		if err := c.do(cctx, "GET", "/ok", nil, false, nil); !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
	})
}

func TestReportJSONExact(t *testing.T) {
	r := Report{
		Scenario:              "batch",
		LedgerID:              "ldg_1",
		Concurrency:           4,
		Accounts:              10,
		BatchSize:             50,
		Rate:                  12.5,
		Seed:                  18446744073709551615,
		Elapsed:               1500 * time.Millisecond,
		Operations:            3,
		Succeeded:             2,
		Failed:                1,
		Transactions:          100,
		OperationsPerSecond:   1.25,
		TransactionsPerSecond: 66.5,
		Errors:                map[string]int64{"zeta": 1, "alpha": 2, "Mid": 3, "": 4},
		Latency:               Latency{Mean: 1, P50: 2, P90: 3, P99: 4, P999: 5, Max: time.Duration(1<<63 - 1)},
		Integrity:             &Integrity{OK: false, Issues: []string{"b", "a"}},
	}
	want := `{"scenario":"batch","ledger_id":"ldg_1","concurrency":4,"accounts":10,"batch_size":50,"rate":12.5,"seed":18446744073709551615,"elapsed_ns":1500000000,"operations":3,"succeeded":2,"failed":1,"transactions":100,"operations_per_second":1.25,"transactions_per_second":66.5,"errors":{"":4,"Mid":3,"alpha":2,"zeta":1},"latency_ns":{"mean":1,"p50":2,"p90":3,"p99":4,"p999":5,"max":9223372036854775807},"integrity":{"ok":false,"issues":["b","a"]}}`
	for i := range 20 {
		raw, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		if string(raw) != want {
			t.Fatalf("marshal %d =\n%s\nwant\n%s", i, raw, want)
		}
	}
}

func TestReportJSONZeroValue(t *testing.T) {
	raw, err := json.Marshal(Report{})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"scenario":"","ledger_id":"","concurrency":0,"accounts":0,"seed":0,"elapsed_ns":0,"operations":0,"succeeded":0,"failed":0,"transactions":0,"operations_per_second":0,"transactions_per_second":0,"errors":{},"latency_ns":{"mean":0,"p50":0,"p90":0,"p99":0,"p999":0,"max":0}}`
	if string(raw) != want {
		t.Fatalf("zero report =\n%s\nwant\n%s", raw, want)
	}
	raw, err = json.Marshal(Report{Integrity: &Integrity{OK: true}, Elapsed: -time.Nanosecond})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"elapsed_ns":-1`) || !strings.HasSuffix(string(raw), `"integrity":{"ok":true,"issues":[]}}`) {
		t.Fatalf("report = %s", raw)
	}
}

func TestReportJSONNestedAndIndented(t *testing.T) {
	r := Report{Elapsed: time.Second, Errors: map[string]int64{"b": 1, "a": 1}}
	raw, err := json.Marshal(struct {
		Ptr   *Report  `json:"ptr"`
		Items []Report `json:"items"`
	}{&r, []Report{r}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(raw), `"elapsed_ns":1000000000`) != 2 || strings.Count(string(raw), `"errors":{"a":1,"b":1}`) != 2 {
		t.Fatalf("nested = %s", raw)
	}
	var buf bytes.Buffer
	if err := json.MarshalWrite(&buf, r, jsontext.WithIndent("  ")); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(buf.String(), "{\n  \"scenario\": \"\",\n") || !strings.Contains(buf.String(), "\n    \"mean\": 0,\n") {
		t.Fatalf("indented =\n%s", buf.String())
	}
}

func TestReportJSONRoundTrip(t *testing.T) {
	r := Report{
		Scenario: "transfer", LedgerID: "ldg_x", Concurrency: 2, Accounts: 3, Seed: 9,
		Elapsed: 1234567891, Operations: 5, Succeeded: 4, Failed: 1, Transactions: 4,
		OperationsPerSecond: 3.25, TransactionsPerSecond: 3.25,
		Errors:    map[string]int64{"transport": 1},
		Latency:   Latency{Mean: 10, P50: 9, P90: 11, P99: 12, P999: 13, Max: 14},
		Integrity: &Integrity{OK: true, Issues: []string{}},
	}
	raw, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	var plain Report
	if err := json.Unmarshal(raw, &plain); err == nil {
		t.Fatal("Report now decodes durations by default; update this test to a plain round trip")
	}
	fromNanos := json.WithUnmarshalers(json.UnmarshalFromFunc(func(dec *jsontext.Decoder, d *time.Duration) error {
		tok, err := dec.ReadToken()
		if err != nil {
			return err
		}
		n, err := tok.Int()
		*d = time.Duration(n)
		return err
	}))
	var back Report
	if err := json.Unmarshal(raw, &back, fromNanos); err != nil {
		t.Fatal(err)
	}
	again, err := json.Marshal(back)
	if err != nil {
		t.Fatal(err)
	}
	if string(again) != string(raw) || back.Elapsed != r.Elapsed || back.Latency != r.Latency {
		t.Fatalf("round trip =\n%s\nwant\n%s", again, raw)
	}
}

func TestWriteTextEdge(t *testing.T) {
	tests := []struct {
		name    string
		report  Report
		want    []string
		notWant []string
	}{
		{
			name:    "zero report",
			report:  Report{},
			want:    []string{"setup       0 workers, 0 accounts, seed 0", "elapsed     0s", "0 ok, 0 failed", "0.0 ops/s, 0.0 tx/s", "mean 0.00ms"},
			notWant: []string{"error", "integrity", "per batch", "target"},
		},
		{
			name:   "batch and rate",
			report: Report{Concurrency: 2, Accounts: 3, BatchSize: 50, Rate: 99.6, Seed: 5},
			want:   []string{"2 workers, 3 accounts, 50 per batch, 100/s target, seed 5"},
		},
		{
			name:   "latency formatting and rounding",
			report: Report{Elapsed: 1234567891, Latency: Latency{Mean: 1_500_000, P50: 6_000, P999: 1_234_567_890, Max: 2 * time.Second}},
			want:   []string{"elapsed     1.235s", "mean 1.50ms", "p50 0.01ms", "p99.9 1234.57ms", "max 2000.00ms"},
		},
		{
			name:   "errors sorted",
			report: Report{Errors: map[string]int64{"zeta": 1, "alpha": 2}},
			want:   []string{"error       alpha × 2\nerror       zeta × 1\n"},
		},
		{
			name:   "integrity ok",
			report: Report{Integrity: &Integrity{OK: true}},
			want:   []string{"integrity   ok\n"},
		},
		{
			name:   "integrity failed",
			report: Report{Integrity: &Integrity{Issues: []string{"seq 3 broken", "drift"}}},
			want:   []string{"integrity   FAILED: [seq 3 broken drift]"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			if err := WriteText(&buf, tt.report); err != nil {
				t.Fatal(err)
			}
			for _, w := range tt.want {
				if !strings.Contains(buf.String(), w) {
					t.Errorf("missing %q in:\n%s", w, buf.String())
				}
			}
			for _, w := range tt.notWant {
				if strings.Contains(buf.String(), w) {
					t.Errorf("unexpected %q in:\n%s", w, buf.String())
				}
			}
		})
	}
}

type failWriter struct{}

func (failWriter) Write([]byte) (int, error) { return 0, errors.New("disk full") }

func TestWriteTextPropagatesWriteError(t *testing.T) {
	if err := WriteText(failWriter{}, Report{}); err == nil {
		t.Fatal("WriteText() ignored the write error")
	}
}
