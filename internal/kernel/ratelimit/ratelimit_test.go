package ratelimit

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestAllow(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	l := New(2, 3)
	l.now = func() time.Time { return now }
	a, b := uuid.New(), uuid.New()

	for i := range 3 {
		if ok, _ := l.Allow(a); !ok {
			t.Fatalf("request %d within the burst was limited", i)
		}
	}
	ok, wait := l.Allow(a)
	if ok || wait != 500*time.Millisecond {
		t.Fatalf("request past the burst = %v, %v; want limited for 500ms", ok, wait)
	}
	if ok, _ := l.Allow(b); !ok {
		t.Fatal("another key shared the bucket")
	}

	now = now.Add(500 * time.Millisecond)
	if ok, _ := l.Allow(a); !ok {
		t.Fatal("token was not refilled after the wait")
	}
	if ok, _ := l.Allow(a); ok {
		t.Fatal("refill granted more than one token")
	}

	now = now.Add(time.Hour)
	for i := range 3 {
		if ok, _ := l.Allow(a); !ok {
			t.Fatalf("request %d after idling was limited", i)
		}
	}
	if ok, _ := l.Allow(a); ok {
		t.Fatal("idle refill exceeded the burst")
	}

	now = now.Add(-time.Minute)
	if ok, _ := l.Allow(a); ok {
		t.Fatal("a clock step backwards granted tokens")
	}
}

func TestMiddlewarePassesThrough(t *testing.T) {
	t.Parallel()
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	for name, l := range map[string]*Limiter{"disabled": New(0, 1), "nil": nil, "unauthenticated": New(1, 1)} {
		h := l.Middleware(next)
		for range 5 {
			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/ledgers", nil))
			if w.Code != http.StatusNoContent {
				t.Fatalf("%s: status = %d", name, w.Code)
			}
		}
	}
}
