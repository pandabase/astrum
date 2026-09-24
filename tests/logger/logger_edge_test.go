package logger_test

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/charmbracelet/log"
	"github.com/pandabase/astrum/internal/kernel/logger"
)

func TestEdgeLevels(t *testing.T) {
	tests := []struct {
		in   string
		want log.Level
		ok   bool
	}{
		{"debug", log.DebugLevel, true},
		{"DEBUG", log.DebugLevel, true},
		{"Info", log.InfoLevel, true},
		{"warn", log.WarnLevel, true},
		{"error", log.ErrorLevel, true},
		{"fatal", log.FatalLevel, true},
		{"", 0, false},
		{"warning", 0, false},
		{"trace", 0, false},
		{" info", 0, false},
		{"info ", 0, false},
		{"0", 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			l, err := logger.New(&bytes.Buffer{}, tt.in, "text")
			if !tt.ok {
				if err == nil || !strings.HasPrefix(err.Error(), "logger: ") || !errors.Is(err, log.ErrInvalidLevel) || l != nil {
					t.Fatalf("New(%q) = %v, %v", tt.in, l, err)
				}
				return
			}
			if err != nil || l.GetLevel() != tt.want {
				t.Fatalf("New(%q) level = %v, %v", tt.in, l.GetLevel(), err)
			}
		})
	}
}

func TestEdgeFormats(t *testing.T) {
	for _, bad := range []string{"JSON", "Text", "logfmt ", "console", "pretty"} {
		t.Run("rejects "+bad, func(t *testing.T) {
			if _, err := logger.New(&bytes.Buffer{}, "info", bad); err == nil || !strings.Contains(err.Error(), "logger: unknown format") {
				t.Fatalf("New(format %q) = %v", bad, err)
			}
		})
	}
	t.Run("level checked before format", func(t *testing.T) {
		if _, err := logger.New(&bytes.Buffer{}, "loud", "xml"); err == nil || !errors.Is(err, log.ErrInvalidLevel) {
			t.Fatalf("New() = %v", err)
		}
	})
}

func TestEdgeJSONLine(t *testing.T) {
	var buf bytes.Buffer
	l, err := logger.New(&buf, "debug", "json")
	if err != nil {
		t.Fatal(err)
	}
	before := time.Now().Add(-time.Second)
	l.Debug("hello", "n", 3, "s", "quoted \"text\"")
	var entry map[string]any
	if err := json.Unmarshal(buf.Bytes(), &entry); err != nil {
		t.Fatalf("line %q: %v", buf.String(), err)
	}
	if entry["msg"] != "hello" || entry["level"] != "debug" || entry["n"] != float64(3) || entry["s"] != `quoted "text"` {
		t.Fatalf("entry = %v", entry)
	}
	ts, ok := entry["time"].(string)
	if !ok {
		t.Fatalf("time = %v", entry["time"])
	}
	parsed, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil || parsed.Before(before) || parsed.After(time.Now().Add(time.Second)) {
		t.Fatalf("time %q = %v, %v", ts, parsed, err)
	}
	if strings.Count(buf.String(), "\n") != 1 {
		t.Fatalf("expected exactly one line: %q", buf.String())
	}
}

func TestEdgeLogfmtAndText(t *testing.T) {
	var lf bytes.Buffer
	l, err := logger.New(&lf, "info", "logfmt")
	if err != nil {
		t.Fatal(err)
	}
	logger.For(logger.WithRequestID(context.Background(), "req 1"), l).Info("hi", "k", "v")
	for _, want := range []string{"level=info", "msg=hi", `request_id="req 1"`, "k=v", "time="} {
		if !strings.Contains(lf.String(), want) {
			t.Fatalf("logfmt lacks %q: %q", want, lf.String())
		}
	}
	for _, format := range []string{"", "text"} {
		var tx bytes.Buffer
		l, err := logger.New(&tx, "info", format)
		if err != nil {
			t.Fatal(err)
		}
		logger.For(logger.WithRequestID(context.Background(), "req-2"), l).Info("hi")
		if !strings.Contains(tx.String(), "INFO") || !strings.Contains(tx.String(), "hi") || !strings.Contains(tx.String(), "request_id=req-2") {
			t.Fatalf("text %q = %q", format, tx.String())
		}
	}
}

func TestEdgeContextValues(t *testing.T) {
	var buf bytes.Buffer
	base, err := logger.New(&buf, "info", "json")
	if err != nil {
		t.Fatal(err)
	}
	decode := func() map[string]any {
		t.Helper()
		var entry map[string]any
		if err := json.Unmarshal(buf.Bytes(), &entry); err != nil {
			t.Fatalf("line %q: %v", buf.String(), err)
		}
		buf.Reset()
		return entry
	}
	type ctxKey string

	tests := []struct {
		name      string
		ctx       context.Context
		requestID any
		actor     any
	}{
		{"none", context.Background(), nil, nil},
		{"request id only", logger.WithRequestID(context.Background(), "r1"), "r1", nil},
		{"actor only", logger.WithActor(context.Background(), "key_abc"), nil, "key_abc"},
		{"both", logger.WithActor(logger.WithRequestID(context.Background(), "r2"), "key_x"), "r2", "key_x"},
		{"empty request id ignored", logger.WithRequestID(context.Background(), ""), nil, nil},
		{"empty actor ignored", logger.WithActor(context.Background(), ""), nil, nil},
		{"innermost wins", logger.WithRequestID(logger.WithRequestID(context.Background(), "outer"), "inner"), "inner", nil},
		{"empty inner hides outer", logger.WithRequestID(logger.WithRequestID(context.Background(), "outer"), ""), nil, nil},
		{"foreign string key ignored", context.WithValue(context.Background(), ctxKey("request_id"), "spoof"), nil, nil},
		{"survives derived contexts", func() context.Context {
			ctx, cancel := context.WithCancel(logger.WithRequestID(context.Background(), "r3"))
			cancel()
			return context.WithoutCancel(ctx)
		}(), "r3", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logger.For(tt.ctx, base).Info("x")
			entry := decode()
			if entry["request_id"] != tt.requestID || entry["actor"] != tt.actor {
				t.Fatalf("entry = %v, want request_id=%v actor=%v", entry, tt.requestID, tt.actor)
			}
			want := ""
			if s, ok := tt.requestID.(string); ok {
				want = s
			}
			if got := logger.RequestID(tt.ctx); got != want {
				t.Fatalf("RequestID() = %q, want %q", got, want)
			}
		})
	}
}

func TestEdgeForDoesNotMutateBase(t *testing.T) {
	var buf bytes.Buffer
	base, err := logger.New(&buf, "info", "json")
	if err != nil {
		t.Fatal(err)
	}
	derived := logger.For(logger.WithActor(logger.WithRequestID(context.Background(), "r"), "a"), base)
	if derived == base {
		t.Fatal("For() returned the base logger despite adding fields")
	}
	base.Info("plain")
	if strings.Contains(buf.String(), "request_id") || strings.Contains(buf.String(), "actor") {
		t.Fatalf("base logger gained fields: %q", buf.String())
	}
	if logger.For(context.Background(), base) != base {
		t.Fatal("For() without values should return the base logger")
	}
}

func TestEdgeForConcurrent(t *testing.T) {
	var buf safeBuffer
	base, err := logger.New(&buf, "info", "json")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	for i := range 16 {
		go func() {
			defer func() { done <- struct{}{} }()
			ctx := logger.WithRequestID(context.Background(), strings.Repeat("r", i+1))
			for range 20 {
				logger.For(ctx, base).Info("x")
			}
		}()
	}
	for range 16 {
		<-done
	}
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 320 {
		t.Fatalf("lines = %d, want 320", len(lines))
	}
	for _, line := range lines {
		var entry map[string]any
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatalf("interleaved line %q: %v", line, err)
		}
	}
}

type safeBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *safeBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *safeBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
