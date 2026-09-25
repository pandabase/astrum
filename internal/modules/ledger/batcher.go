package ledger

import (
	"context"
	"sync"
	"time"

	"github.com/charmbracelet/log"
)

type request struct {
	ctx  context.Context
	req  *postingRequest
	done chan postingResult
}

type batcher struct {
	svc      *service
	log      *log.Logger
	queue    chan *request
	workers  int
	maxBatch int
	timeout  time.Duration
	stopped  chan struct{}
}

func newBatcher(svc *service, cfg Config) *batcher {
	return &batcher{
		svc:      svc,
		log:      svc.log.WithPrefix("ledger/batcher"),
		queue:    make(chan *request, cfg.QueueSize),
		workers:  cfg.Workers,
		maxBatch: cfg.MaxBatch,
		timeout:  cfg.CommitTimeout,
		stopped:  make(chan struct{}),
	}
}

func (b *batcher) submit(ctx context.Context, req *postingRequest) (postingResult, error) {
	r := &request{ctx: ctx, req: req, done: make(chan postingResult, 1)}

	select {
	case b.queue <- r:
	case <-ctx.Done():
		return postingResult{}, ctx.Err()
	case <-b.stopped:
		return postingResult{}, ErrStopped
	}

	select {
	case o := <-r.done:
		return o, nil
	case <-ctx.Done():

		return postingResult{}, ctx.Err()
	case <-b.stopped:
		select {
		case o := <-r.done:
			return o, nil
		default:
			return postingResult{}, ErrStopped
		}
	}
}

func (b *batcher) run(ctx context.Context) {
	b.log.Info("started", "workers", b.workers, "max_batch", b.maxBatch)

	var wg sync.WaitGroup
	for range b.workers {
		wg.Go(func() { b.work(ctx) })
	}
	wg.Wait()
	close(b.stopped)

	b.log.Info("stopped")
}

func (b *batcher) work(ctx context.Context) {
	for {
		var first *request
		select {
		case first = <-b.queue:
		case <-ctx.Done():
			b.drain()
			return
		}
		b.commit(b.collect(first))
	}
}

func (b *batcher) drain() {
	for {
		select {
		case r := <-b.queue:
			b.commit(b.collect(r))
		default:
			return
		}
	}
}

func (b *batcher) collect(first *request) []*request {
	batch := []*request{first}
	for len(batch) < b.maxBatch {
		select {
		case r := <-b.queue:
			batch = append(batch, r)
		default:
			return batch
		}
	}
	return batch
}

func (b *batcher) commit(batch []*request) {
	live := batch[:0]
	for _, r := range batch {
		if err := r.ctx.Err(); err != nil {
			r.done <- postingResult{err: err}
			continue
		}
		live = append(live, r)
	}
	if len(live) == 0 {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), b.timeout)
	defer cancel()

	start := time.Now()
	reqs := make([]*postingRequest, len(live))
	for i, r := range live {
		reqs[i] = r.req
	}

	results, err := runRequests(ctx, b.svc, reqs, false)
	if err != nil && len(live) > 1 {

		b.log.Warn("group commit failed, retrying entries individually", "size", len(live), "err", err)
		for _, r := range live {
			b.commit([]*request{r})
		}
		return
	}

	for i, r := range live {
		if err != nil {
			r.done <- postingResult{err: err}
			continue
		}
		r.done <- results[i]
	}
	b.log.Debug("group committed", "size", len(live), "duration", time.Since(start))
}
