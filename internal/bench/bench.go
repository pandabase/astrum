package bench

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type Config struct {
	URL         string
	Key         string
	Scenario    string
	Duration    time.Duration
	Operations  int64
	Concurrency int
	Accounts    int
	BatchSize   int
	Rate        float64
	Currency    string
	Verify      bool
	Seed        uint64
	Progress    func(Progress)
	HTTPClient  *http.Client
}

type Progress struct {
	Elapsed    time.Duration
	Operations int64
	Failed     int64
	PerSecond  float64
}

type Integrity struct {
	OK     bool     `json:"ok"`
	Issues []string `json:"issues"`
}

type Report struct {
	Scenario              string           `json:"scenario"`
	LedgerID              string           `json:"ledger_id"`
	Concurrency           int              `json:"concurrency"`
	Accounts              int              `json:"accounts"`
	BatchSize             int              `json:"batch_size,omitzero"`
	Rate                  float64          `json:"rate,omitzero"`
	Seed                  uint64           `json:"seed"`
	Elapsed               time.Duration    `json:"elapsed_ns"`
	Operations            int64            `json:"operations"`
	Succeeded             int64            `json:"succeeded"`
	Failed                int64            `json:"failed"`
	Transactions          int64            `json:"transactions"`
	OperationsPerSecond   float64          `json:"operations_per_second"`
	TransactionsPerSecond float64          `json:"transactions_per_second"`
	Errors                map[string]int64 `json:"errors"`
	Latency               Latency          `json:"latency_ns"`
	Integrity             *Integrity       `json:"integrity,omitzero"`
}

func (c *Config) normalize() error {
	c.URL = strings.TrimRight(c.URL, "/")
	if c.Currency == "" {
		c.Currency = "USD"
	}
	if c.BatchSize == 0 {
		c.BatchSize = 100
	}
	if c.Seed == 0 {
		c.Seed = uint64(time.Now().UnixNano())
	}
	switch {
	case c.URL == "":
		return errors.New("bench: url is required")
	case c.Key == "":
		return errors.New("bench: an API key with the write role is required")
	case c.Duration <= 0 && c.Operations <= 0:
		return errors.New("bench: set a duration or a number of operations")
	case c.Concurrency < 1:
		return errors.New("bench: concurrency must be at least 1")
	case c.Accounts < 2:
		return errors.New("bench: at least 2 accounts are needed to move money between")
	case c.BatchSize < 1 || c.BatchSize > 1000:
		return errors.New("bench: batch size must be 1-1000")
	case c.Rate < 0:
		return errors.New("bench: rate cannot be negative")
	}
	if _, ok := scenarios[c.Scenario]; !ok {
		return fmt.Errorf("bench: unknown scenario %q; choose one of %s", c.Scenario, strings.Join(Scenarios(), ", "))
	}
	return nil
}

type run struct {
	cfg      Config
	client   *client
	ledgerID string
	accounts []string
}

func Run(ctx context.Context, cfg Config) (Report, error) {
	if err := cfg.normalize(); err != nil {
		return Report{}, err
	}
	httpClient := cfg.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{
			Timeout:   time.Minute,
			Transport: &http.Transport{MaxIdleConns: cfg.Concurrency * 2, MaxIdleConnsPerHost: cfg.Concurrency * 2, IdleConnTimeout: time.Minute},
		}
	}
	r := &run{cfg: cfg, client: &client{http: httpClient, base: cfg.URL, key: cfg.Key}}
	if err := r.checkKey(ctx); err != nil {
		return Report{}, err
	}
	if err := r.setup(ctx); err != nil {
		return Report{}, err
	}
	step, err := scenarioStep(r)
	if err != nil {
		return Report{}, err
	}

	rec, elapsed := r.drive(ctx, step)

	report := Report{
		Scenario:     cfg.Scenario,
		LedgerID:     r.ledgerID,
		Concurrency:  cfg.Concurrency,
		Accounts:     cfg.Accounts,
		Rate:         cfg.Rate,
		Seed:         cfg.Seed,
		Elapsed:      elapsed,
		Operations:   rec.operations.Load(),
		Succeeded:    rec.succeeded.Load(),
		Failed:       rec.failed.Load(),
		Transactions: rec.transactions.Load(),
		Errors:       rec.errors,
		Latency:      summarize(rec.latencies),
	}
	if cfg.Scenario == "batch" {
		report.BatchSize = cfg.BatchSize
	}
	if seconds := elapsed.Seconds(); seconds > 0 {
		report.OperationsPerSecond = float64(report.Succeeded) / seconds
		report.TransactionsPerSecond = float64(report.Transactions) / seconds
	}
	if cfg.Verify {
		var integrity Integrity
		if err := r.client.do(ctx, "GET", "/v1/integrity", nil, false, &integrity); err != nil {
			return report, fmt.Errorf("bench: integrity check: %w", err)
		}
		report.Integrity = &integrity
	}
	return report, ctx.Err()
}

func (r *run) checkKey(ctx context.Context) error {
	var me struct {
		Role string `json:"role"`
	}
	if err := r.client.do(ctx, "GET", "/v1/me", nil, false, &me); err != nil {
		if apiErr, ok := errors.AsType[*apiError](err); ok && apiErr.Status == http.StatusUnauthorized {
			return errors.New("bench: the API key was rejected; check -key or ASTRUM_KEY")
		}
		return fmt.Errorf("bench: reach %s: %w", r.cfg.URL, err)
	}
	if me.Role == "read" {
		return errors.New("bench: a read key cannot create the benchmark ledger; use a write or admin key")
	}
	return nil
}

func (r *run) setup(ctx context.Context) error {
	var ledger resource
	name := fmt.Sprintf("bench-%s-%s", r.cfg.Scenario, time.Now().UTC().Format("20060102T150405Z"))
	body := map[string]any{"name": name, "description": "Created by astrum-bench", "metadata": map[string]string{"bench": "true"}}
	if err := r.client.do(ctx, "POST", "/v1/ledgers", body, true, &ledger); err != nil {
		return fmt.Errorf("bench: create ledger: %w", err)
	}
	r.ledgerID = ledger.ID
	r.accounts = make([]string, r.cfg.Accounts)

	var (
		next     atomic.Int64
		firstErr error
		once     sync.Once
		wg       sync.WaitGroup
	)
	for range min(r.cfg.Concurrency, 16) {
		wg.Go(func() {
			for {
				i := int(next.Add(1) - 1)
				if i >= len(r.accounts) {
					return
				}
				var account resource
				body := map[string]any{
					"ledger_id":      r.ledgerID,
					"code":           fmt.Sprintf("bench-%d", i),
					"currency":       r.cfg.Currency,
					"normal_side":    "debit",
					"allow_negative": true,
				}
				if err := r.client.do(ctx, "POST", "/v1/accounts", body, true, &account); err != nil {
					once.Do(func() { firstErr = fmt.Errorf("bench: create account %d: %w", i, err) })
					return
				}
				r.accounts[i] = account.ID
			}
		})
	}
	wg.Wait()
	return firstErr
}

func (r *run) drive(ctx context.Context, step step) (*recorder, time.Duration) {
	runCtx := ctx
	if r.cfg.Duration > 0 {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, r.cfg.Duration)
		defer cancel()
	}
	rec := newRecorder()
	start := time.Now()
	schedule := r.schedule(runCtx, start)
	stopProgress := r.progress(rec, start)
	defer stopProgress()

	var claimed atomic.Int64
	var wg sync.WaitGroup
	for w := range r.cfg.Concurrency {
		wg.Go(func() {
			rng := rand.New(rand.NewPCG(r.cfg.Seed, uint64(w)))
			s := &sample{}
			defer rec.merge(s)
			for {
				if r.cfg.Operations > 0 && claimed.Add(1) > r.cfg.Operations {
					return
				}
				scheduled := time.Now()
				if schedule != nil {
					select {
					case scheduled = <-schedule:
					case <-runCtx.Done():
						return
					}
				}
				if runCtx.Err() != nil {
					return
				}
				transactions, err := step(runCtx, rng)
				if err != nil && runCtx.Err() != nil {
					return
				}
				rec.record(s, time.Since(scheduled), transactions, err)
			}
		})
	}
	wg.Wait()
	return rec, time.Since(start)
}

func (r *run) schedule(ctx context.Context, start time.Time) <-chan time.Time {
	if r.cfg.Rate <= 0 {
		return nil
	}
	interval := time.Duration(float64(time.Second) / r.cfg.Rate)
	out := make(chan time.Time, r.cfg.Concurrency)
	go func() {
		for next := start; ; next = next.Add(interval) {
			if wait := time.Until(next); wait > 0 {
				select {
				case <-time.After(wait):
				case <-ctx.Done():
					return
				}
			}
			select {
			case out <- next:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out
}

func (r *run) progress(rec *recorder, start time.Time) func() {
	if r.cfg.Progress == nil {
		return func() {}
	}
	done := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		last, lastAt := int64(0), start
		for {
			select {
			case <-done:
				return
			case now := <-ticker.C:
				ops := rec.operations.Load()
				r.cfg.Progress(Progress{
					Elapsed:    now.Sub(start),
					Operations: ops,
					Failed:     rec.failed.Load(),
					PerSecond:  float64(ops-last) / now.Sub(lastAt).Seconds(),
				})
				last, lastAt = ops, now
			}
		}
	}()
	return func() {
		close(done)
		<-finished
	}
}
