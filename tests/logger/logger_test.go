package logger_test

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"strings"
	"testing"

	"github.com/pandabase/astrum/internal/kernel/logger"
)

func TestNew(t *testing.T) {
	tests := []struct {
		name    string
		level   string
		format  string
		wantErr bool
	}{
		{"text", "info", "text", false},
		{"default format", "debug", "", false},
		{"json", "warn", "json", false},
		{"logfmt", "error", "logfmt", false},
		{"bad level", "loud", "text", true},
		{"bad format", "info", "xml", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := logger.New(&bytes.Buffer{}, tt.level, tt.format)
			if (err != nil) != tt.wantErr {
				t.Fatalf("New() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestLevelFiltering(t *testing.T) {
	var buf bytes.Buffer
	l, err := logger.New(&buf, "warn", "text")
	if err != nil {
		t.Fatal(err)
	}

	l.Info("hidden")
	l.Warn("shown")

	out := buf.String()
	if strings.Contains(out, "hidden") {
		t.Errorf("info message logged at warn level: %q", out)
	}
	if !strings.Contains(out, "shown") {
		t.Errorf("warn message missing: %q", out)
	}
}

func TestForAddsRequestID(t *testing.T) {
	var buf bytes.Buffer
	base, err := logger.New(&buf, "info", "json")
	if err != nil {
		t.Fatal(err)
	}

	ctx := logger.WithRequestID(context.Background(), "req-123")
	logger.For(ctx, base).Info("hello")

	var entry map[string]any
	if err := json.Unmarshal(buf.Bytes(), &entry); err != nil {
		t.Fatalf("decode log line %q: %v", buf.String(), err)
	}
	if entry["request_id"] != "req-123" {
		t.Fatalf("request_id = %v, want req-123", entry["request_id"])
	}
}

func TestForWithoutRequestID(t *testing.T) {
	var buf bytes.Buffer
	base, err := logger.New(&buf, "info", "json")
	if err != nil {
		t.Fatal(err)
	}

	logger.For(context.Background(), base).Info("hello")

	if strings.Contains(buf.String(), "request_id") {
		t.Fatalf("unexpected request_id in %q", buf.String())
	}
	if logger.RequestID(context.Background()) != "" {
		t.Fatal("RequestID() on empty context should be empty")
	}
}
