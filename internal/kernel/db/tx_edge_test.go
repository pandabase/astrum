package db

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestBackoffEdgeBounds(t *testing.T) {
	for attempt := range maxAttempts {
		ceiling := min(baseBackoff<<attempt, maxBackoff)
		lo, hi := ceiling, time.Duration(0)
		for range 2000 {
			d := backoff(attempt)
			lo, hi = min(lo, d), max(hi, d)
		}
		if lo < ceiling/2 || hi > ceiling {
			t.Fatalf("attempt %d: backoff range [%s, %s], want within [%s, %s]", attempt, lo, hi, ceiling/2, ceiling)
		}
		if lo == hi {
			t.Fatalf("attempt %d: backoff has no jitter (%s)", attempt, lo)
		}
	}
}

func TestBackoffEdgeGrowsAndCaps(t *testing.T) {
	want := []time.Duration{5 * time.Millisecond, 10 * time.Millisecond, 20 * time.Millisecond, 40 * time.Millisecond, 80 * time.Millisecond, 160 * time.Millisecond, maxBackoff, maxBackoff}
	for attempt, w := range want {
		if got := min(baseBackoff<<attempt, maxBackoff); got != w {
			t.Fatalf("ceiling(%d) = %s, want %s", attempt, got, w)
		}
		if d := backoff(attempt); d > w || d < w/2 {
			t.Fatalf("backoff(%d) = %s, want within [%s, %s]", attempt, d, w/2, w)
		}
	}
	var total time.Duration
	for attempt := range maxAttempts - 1 {
		total += min(baseBackoff<<attempt, maxBackoff)
	}
	if total > time.Second {
		t.Fatalf("worst-case retry sleep = %s", total)
	}
}

func TestSleepEdge(t *testing.T) {
	t.Run("zero duration", func(t *testing.T) {
		if err := sleep(context.Background(), 0); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("negative duration", func(t *testing.T) {
		if err := sleep(context.Background(), -time.Second); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("canceled before", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		start := time.Now()
		if err := sleep(ctx, time.Hour); !errors.Is(err, context.Canceled) {
			t.Fatalf("sleep = %v", err)
		}
		if time.Since(start) > time.Second {
			t.Fatal("sleep ignored cancellation")
		}
	})
	t.Run("deadline during", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
		defer cancel()
		if err := sleep(ctx, time.Hour); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("sleep = %v", err)
		}
	})
	t.Run("completes", func(t *testing.T) {
		start := time.Now()
		if err := sleep(context.Background(), 5*time.Millisecond); err != nil || time.Since(start) < 5*time.Millisecond {
			t.Fatalf("sleep = %v after %s", err, time.Since(start))
		}
	})
}
