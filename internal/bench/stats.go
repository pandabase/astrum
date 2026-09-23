package bench

import (
	"errors"
	"slices"
	"sync"
	"sync/atomic"
	"time"
)

type recorder struct {
	operations   atomic.Int64
	succeeded    atomic.Int64
	failed       atomic.Int64
	transactions atomic.Int64

	mu        sync.Mutex
	errors    map[string]int64
	latencies []time.Duration
}

func newRecorder() *recorder {
	return &recorder{errors: map[string]int64{}}
}

type sample struct {
	latencies []time.Duration
}

func (r *recorder) record(s *sample, latency time.Duration, transactions int, err error) {
	r.operations.Add(1)
	s.latencies = append(s.latencies, latency)
	if err == nil {
		r.succeeded.Add(1)
		r.transactions.Add(int64(transactions))
		return
	}
	r.failed.Add(1)
	code := "transport"
	if apiErr, ok := errors.AsType[*apiError](err); ok {
		code = apiErr.Code
	}
	r.mu.Lock()
	r.errors[code]++
	r.mu.Unlock()
}

func (r *recorder) merge(s *sample) {
	r.mu.Lock()
	r.latencies = append(r.latencies, s.latencies...)
	r.mu.Unlock()
}

type Latency struct {
	Mean time.Duration `json:"mean"`
	P50  time.Duration `json:"p50"`
	P90  time.Duration `json:"p90"`
	P99  time.Duration `json:"p99"`
	P999 time.Duration `json:"p999"`
	Max  time.Duration `json:"max"`
}

func summarize(latencies []time.Duration) Latency {
	if len(latencies) == 0 {
		return Latency{}
	}
	sorted := slices.Clone(latencies)
	slices.Sort(sorted)
	var total time.Duration
	for _, l := range sorted {
		total += l
	}
	return Latency{
		Mean: total / time.Duration(len(sorted)),
		P50:  percentile(sorted, 0.50),
		P90:  percentile(sorted, 0.90),
		P99:  percentile(sorted, 0.99),
		P999: percentile(sorted, 0.999),
		Max:  sorted[len(sorted)-1],
	}
}

func percentile(sorted []time.Duration, q float64) time.Duration {
	rank := int(q*float64(len(sorted))+0.5) - 1
	return sorted[min(max(rank, 0), len(sorted)-1)]
}
