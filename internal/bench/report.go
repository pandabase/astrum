package bench

import (
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"slices"
	"text/tabwriter"
	"time"
)

func (l Latency) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]int64{
		"mean": int64(l.Mean), "p50": int64(l.P50), "p90": int64(l.P90),
		"p99": int64(l.P99), "p999": int64(l.P999), "max": int64(l.Max),
	})
}

func WriteText(w io.Writer, r Report) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	line := func(label, format string, args ...any) {
		fmt.Fprintf(tw, "%s\t"+format+"\n", append([]any{label}, args...)...)
	}
	line("scenario", "%s", r.Scenario)
	line("ledger", "%s", r.LedgerID)
	setup := fmt.Sprintf("%d workers, %d accounts", r.Concurrency, r.Accounts)
	if r.BatchSize > 0 {
		setup += fmt.Sprintf(", %d per batch", r.BatchSize)
	}
	if r.Rate > 0 {
		setup += fmt.Sprintf(", %.0f/s target", r.Rate)
	}
	line("setup", "%s, seed %d", setup, r.Seed)
	line("elapsed", "%s", r.Elapsed.Round(time.Millisecond))
	line("operations", "%d ok, %d failed", r.Succeeded, r.Failed)
	line("throughput", "%.1f ops/s, %.1f tx/s", r.OperationsPerSecond, r.TransactionsPerSecond)
	line("latency", "mean %s  p50 %s  p90 %s  p99 %s  p99.9 %s  max %s",
		ms(r.Latency.Mean), ms(r.Latency.P50), ms(r.Latency.P90), ms(r.Latency.P99), ms(r.Latency.P999), ms(r.Latency.Max))
	for _, code := range slices.Sorted(maps.Keys(r.Errors)) {
		line("error", "%s × %d", code, r.Errors[code])
	}
	if r.Integrity != nil {
		if r.Integrity.OK {
			line("integrity", "ok")
		} else {
			line("integrity", "FAILED: %v", r.Integrity.Issues)
		}
	}
	return tw.Flush()
}

func ms(d time.Duration) string {
	return fmt.Sprintf("%.2fms", float64(d)/float64(time.Millisecond))
}
