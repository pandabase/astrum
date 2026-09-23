package tests

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pandabase/astrum/internal/modules/ledger"
)

func TestConcurrentTransfersConserveMoney(t *testing.T) {
	e := setup(t)
	ctx := context.Background()

	const (
		accounts  = 8
		workers   = 24
		perWorker = 40
		seed      = 5_000
	)
	ids := make([]uuid.UUID, accounts)
	for i := range ids {
		ids[i] = e.funded(t, seed).ID
	}

	var ok, rejected atomic.Int64
	var wg sync.WaitGroup
	for w := range workers {
		wg.Go(func() {
			rng := rand.New(rand.NewPCG(uint64(w), 7))
			for i := range perWorker {
				from, to := ids[rng.IntN(accounts)], ids[rng.IntN(accounts)]
				if from == to {
					continue
				}
				_, err := e.m.Post(ctx, transfer(fmt.Sprintf("w%d-%d", w, i), from, to, rng.Int64N(3_000)+1))
				switch {
				case err == nil:
					ok.Add(1)
				case errors.Is(err, ledger.ErrInsufficientFunds):
					rejected.Add(1)
				default:
					t.Errorf("unexpected: %v", err)
				}
			}
		})
	}
	wg.Wait()

	var total int64
	for _, id := range ids {
		acc := e.get(t, id)
		if acc.Posted.Amount.Sign() < 0 {
			t.Errorf("account %s went negative: %s", id, acc.Posted.Amount)
		}
		total += small(t, acc.Posted.Amount)
	}
	if total != accounts*seed {
		t.Fatalf("total = %d, want %d", total, accounts*seed)
	}
	if ok.Load() == 0 || rejected.Load() == 0 {
		t.Fatalf("expected both successes and funds rejections, got %d / %d", ok.Load(), rejected.Load())
	}
	t.Logf("%d posted, %d rejected for funds", ok.Load(), rejected.Load())
	e.verify(t)
}

func TestConcurrentSameIdempotencyKey(t *testing.T) {
	e := setup(t)
	a := e.funded(t, 1_000)
	b := e.account(t, "USD", ledger.Debit)
	in := transfer("storm", a.ID, b.ID, 777)

	const attempts = 30
	ids := make([]uuid.UUID, attempts)
	var wg sync.WaitGroup
	for i := range attempts {
		wg.Go(func() {
			txn, err := e.m.Post(context.Background(), in)
			if err != nil {
				t.Errorf("attempt %d: %v", i, err)
				return
			}
			ids[i] = txn.ID
		})
	}
	wg.Wait()

	for i, id := range ids {
		if id != ids[0] {
			t.Fatalf("attempt %d got %s, attempt 0 got %s", i, id, ids[0])
		}
	}
	if bal := e.balance(t, b.ID); bal != 777 {
		t.Fatalf("b = %d, want 777", bal)
	}
	e.verify(t)
}

func TestThroughput(t *testing.T) {
	if testing.Short() {
		t.Skip("throughput test")
	}
	for _, tc := range []struct {
		name string
		hot  bool
	}{{"spread accounts", false}, {"single hot account", true}} {
		t.Run(tc.name, func(t *testing.T) {
			e := setup(t)
			ctx := context.Background()
			hot := e.funded(t, 1<<40)

			const (
				clients = 256
				total   = 8_000
			)
			pairs := make([][2]uuid.UUID, clients)
			for i := range pairs {
				src := hot.ID
				if !tc.hot {
					src = e.account(t, "USD", ledger.Debit, unrestricted).ID
				}
				pairs[i] = [2]uuid.UUID{src, e.account(t, "USD", ledger.Debit).ID}
			}

			var next atomic.Int64
			start := time.Now()
			var wg sync.WaitGroup
			for c := range clients {
				wg.Go(func() {
					for {
						n := next.Add(1)
						if n > total {
							return
						}
						if _, err := e.m.Post(ctx, transfer(fmt.Sprintf("tp-%d", n), pairs[c][0], pairs[c][1], 1)); err != nil {
							t.Errorf("post %d: %v", n, err)
							return
						}
					}
				})
			}
			wg.Wait()
			elapsed := time.Since(start)
			t.Logf("%d transactions in %s = %.0f tx/s", total, elapsed.Round(time.Millisecond), float64(total)/elapsed.Seconds())
			e.verify(t)
		})
	}
}

func BenchmarkPostSerial(b *testing.B) {
	e := setup(b)
	from := e.account(b, "USD", ledger.Debit, unrestricted)
	to := e.account(b, "USD", ledger.Debit)
	ctx := context.Background()

	for i := 0; b.Loop(); i++ {
		if _, err := e.m.Post(ctx, transfer(fmt.Sprintf("b-%d", i), from.ID, to.ID, 1)); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkBatch1000(b *testing.B) {
	e := setup(b)
	from := e.account(b, "USD", ledger.Debit, unrestricted)
	to := e.account(b, "USD", ledger.Debit)
	ctx := context.Background()

	for i := 0; b.Loop(); i++ {
		batch := make([]ledger.PostInput, 1000)
		for j := range batch {
			batch[j] = transfer(fmt.Sprintf("b-%d-%d", i, j), from.ID, to.ID, 1)
		}
		if _, err := e.m.PostBatch(ctx, batch, true); err != nil {
			b.Fatal(err)
		}
	}
	b.ReportMetric(float64(b.N*1000)/b.Elapsed().Seconds(), "tx/s")
}

func BenchmarkBatchContended(b *testing.B) {
	const (
		accounts  = 1000
		workers   = 32
		batchSize = 100
	)
	e := setup(b)
	ids := make([]uuid.UUID, accounts)
	for i := range ids {
		ids[i] = e.account(b, "USD", ledger.Debit, unrestricted).ID
	}
	ctx := context.Background()

	var (
		next      atomic.Int64
		failed    atomic.Int64
		mu        sync.Mutex
		latencies []time.Duration
		wg        sync.WaitGroup
	)
	b.ResetTimer()
	for w := range workers {
		wg.Go(func() {
			rng := rand.New(rand.NewPCG(uint64(w), 11))
			for {
				n := next.Add(1)
				if n > int64(b.N) {
					return
				}
				batch := make([]ledger.PostInput, batchSize)
				for j := range batch {
					from := ids[rng.IntN(accounts)]
					to := ids[rng.IntN(accounts)]
					for to == from {
						to = ids[rng.IntN(accounts)]
					}
					batch[j] = transfer(fmt.Sprintf("c-%d-%d", n, j), from, to, 1)
				}
				start := time.Now()
				_, err := e.m.PostBatch(ctx, batch, false)
				elapsed := time.Since(start)
				if err != nil {
					failed.Add(1)
					continue
				}
				mu.Lock()
				latencies = append(latencies, elapsed)
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	b.StopTimer()

	if n := failed.Load(); n > 0 {
		b.Fatalf("%d batches failed", n)
	}
	slices.Sort(latencies)
	b.ReportMetric(float64(b.N*batchSize)/b.Elapsed().Seconds(), "tx/s")
	b.ReportMetric(float64(latencies[len(latencies)/2].Milliseconds()), "p50-ms")
	b.ReportMetric(float64(latencies[len(latencies)*99/100].Milliseconds()), "p99-ms")
}
