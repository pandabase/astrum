package ratelimit

import (
	"fmt"
	"math"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/pandabase/astrum/internal/kernel/auth"
	"github.com/pandabase/astrum/internal/kernel/httpx"
)

type Limiter struct {
	rate    float64
	burst   float64
	now     func() time.Time
	mu      sync.Mutex
	buckets map[uuid.UUID]*bucket
}

type bucket struct {
	tokens float64
	last   time.Time
}

func New(rate, burst int) *Limiter {
	return &Limiter{rate: float64(rate), burst: float64(burst), now: time.Now, buckets: map[uuid.UUID]*bucket{}}
}

func (l *Limiter) Allow(key uuid.UUID) (bool, time.Duration) {
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	b, ok := l.buckets[key]
	if !ok {
		b = &bucket{tokens: l.burst, last: now}
		l.buckets[key] = b
	}
	if elapsed := now.Sub(b.last).Seconds(); elapsed > 0 {
		b.tokens = math.Min(l.burst, b.tokens+elapsed*l.rate)
		b.last = now
	}
	if b.tokens >= 1 {
		b.tokens--
		return true, 0
	}
	return false, time.Duration((1 - b.tokens) / l.rate * float64(time.Second))
}

func (l *Limiter) Middleware(next http.Handler) http.Handler {
	if l == nil || l.rate <= 0 {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key, ok := auth.FromContext(r.Context())
		if !ok {
			next.ServeHTTP(w, r)
			return
		}
		if allowed, wait := l.Allow(key.ID); !allowed {
			seconds := int(math.Ceil(wait.Seconds()))
			w.Header().Set("Retry-After", strconv.Itoa(seconds))
			httpx.Error(w, r, http.StatusTooManyRequests, httpx.CodeRateLimited,
				fmt.Sprintf("rate limit of %g requests per second exceeded; retry after %d seconds", l.rate, seconds))
			return
		}
		next.ServeHTTP(w, r)
	})
}
