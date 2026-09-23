package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/charmbracelet/log"
	"github.com/pandabase/astrum/internal/bench"
	"github.com/pandabase/astrum/internal/kernel/logger"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	err := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	switch {
	case errors.Is(err, flag.ErrHelp):
	case err != nil:
		fmt.Fprintln(os.Stderr, "astrum-bench:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("astrum-bench", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprintf(stderr, "Load-tests a running Astrum server through its HTTP API.\n\nusage: astrum-bench [flags]\n\nscenarios: %s\n\n", strings.Join(bench.Scenarios(), ", "))
		fs.PrintDefaults()
	}
	cfg := bench.Config{}
	fs.StringVar(&cfg.URL, "url", envOr("ASTRUM_URL", "http://localhost:8080"), "server URL, or ASTRUM_URL")
	fs.StringVar(&cfg.Key, "key", os.Getenv("ASTRUM_KEY"), "write or admin API key, or ASTRUM_KEY")
	fs.StringVar(&cfg.Scenario, "scenario", "transfer", "what each operation does: "+strings.Join(bench.Scenarios(), ", "))
	fs.DurationVar(&cfg.Duration, "duration", 30*time.Second, "how long to run; ignored when -operations is set")
	fs.Int64Var(&cfg.Operations, "operations", 0, "stop after this many operations instead of after -duration")
	fs.IntVar(&cfg.Concurrency, "concurrency", 32, "operations in flight at once")
	fs.IntVar(&cfg.Accounts, "accounts", 1000, "accounts to spread load over; 2 makes every operation contend")
	fs.IntVar(&cfg.BatchSize, "batch", 100, "transactions per batch in the batch scenario")
	fs.Float64Var(&cfg.Rate, "rate", 0, "target operations per second; 0 runs as fast as possible")
	fs.StringVar(&cfg.Currency, "currency", "USD", "currency of the benchmark accounts")
	fs.BoolVar(&cfg.Verify, "verify", false, "run the integrity check afterwards")
	fs.Uint64Var(&cfg.Seed, "seed", 0, "seed for choosing accounts and amounts; 0 picks one, shown in the report")
	asJSON := fs.Bool("json", false, "print the report as JSON")
	quiet := fs.Bool("quiet", false, "hide per-second progress")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected arguments %v", fs.Args())
	}
	if cfg.Operations > 0 {
		cfg.Duration = 0
	}

	l, err := logger.New(stderr, "info", "text")
	if err != nil {
		return err
	}
	l = l.WithPrefix("bench")
	if !*quiet {
		cfg.Progress = func(p bench.Progress) {
			l.Info("progress", "elapsed", p.Elapsed.Round(time.Second), "operations", p.Operations, "failed", p.Failed, "per_second", fmt.Sprintf("%.0f", p.PerSecond))
		}
	}

	l.Info("starting", "url", cfg.URL, "scenario", cfg.Scenario, "concurrency", cfg.Concurrency, "accounts", cfg.Accounts)
	report, err := bench.Run(ctx, cfg)
	if err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	if errors.Is(err, context.Canceled) {
		l.Warn("interrupted; reporting what completed")
	}

	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(report); err != nil {
			return err
		}
	} else if err := bench.WriteText(stdout, report); err != nil {
		return err
	}
	return outcome(l, report)
}

func outcome(l *log.Logger, r bench.Report) error {
	switch {
	case r.Integrity != nil && !r.Integrity.OK:
		return errors.New("integrity check failed")
	case r.Failed > 0:
		l.Warn("some operations failed", "failed", r.Failed)
		return fmt.Errorf("%d of %d operations failed", r.Failed, r.Operations)
	}
	return nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
