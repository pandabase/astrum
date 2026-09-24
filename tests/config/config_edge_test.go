package config_test

import (
	"strings"
	"testing"
	"time"

	"github.com/pandabase/astrum/internal/kernel/config"
)

const edgeSealKey = "0123456789abcdef0123456789abcdef"

var edgeEnvVars = []string{
	"DATABASE_URL", "HTTP_ADDR", "LOG_LEVEL", "LOG_FORMAT", "LEDGER_SEAL_KEY", "WEB_DIR",
	"DB_MAX_CONNS", "LEDGER_WORKERS", "LEDGER_MAX_BATCH", "LEDGER_BATCH_CONCURRENCY",
	"DB_ALLOW_UNSAFE_DURABILITY", "WEBHOOK_ALLOW_INSECURE", "EVENT_RETENTION",
}

func edgeEnv(t *testing.T) {
	t.Helper()
	for _, k := range edgeEnvVars {
		t.Setenv(k, "")
	}
	t.Setenv("DATABASE_URL", "postgres://localhost/astrum")
	t.Setenv("LEDGER_SEAL_KEY", edgeSealKey)
}

func TestEdgeDefaults(t *testing.T) {
	edgeEnv(t)
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	want := config.Config{
		DatabaseURL:            "postgres://localhost/astrum",
		HTTPAddr:               ":8080",
		LogLevel:               "info",
		LogFormat:              "text",
		DBMaxConns:             32,
		LedgerWorkers:          8,
		LedgerMaxBatch:         256,
		LedgerBatchConcurrency: 4,
		LedgerSealKey:          edgeSealKey,
		EventRetention:         720 * time.Hour,
	}
	if cfg != want {
		t.Fatalf("Load() = %+v\nwant %+v", cfg, want)
	}
}

func TestEdgeStringPassthrough(t *testing.T) {
	tests := []struct {
		env   string
		value string
		get   func(config.Config) string
	}{
		{"HTTP_ADDR", "127.0.0.1:0", func(c config.Config) string { return c.HTTPAddr }},
		{"HTTP_ADDR", " :9 ", func(c config.Config) string { return c.HTTPAddr }},
		{"LOG_LEVEL", "LOUD", func(c config.Config) string { return c.LogLevel }},
		{"LOG_FORMAT", "xml", func(c config.Config) string { return c.LogFormat }},
		{"WEB_DIR", "/srv/astrum/web", func(c config.Config) string { return c.WebDir }},
		{"WEB_DIR", "relative/dir", func(c config.Config) string { return c.WebDir }},
		{"DATABASE_URL", "not a url at all", func(c config.Config) string { return c.DatabaseURL }},
	}
	for _, tt := range tests {
		t.Run(tt.env+"="+tt.value, func(t *testing.T) {
			edgeEnv(t)
			t.Setenv(tt.env, tt.value)
			cfg, err := config.Load()
			if err != nil {
				t.Fatal(err)
			}
			if got := tt.get(cfg); got != tt.value {
				t.Fatalf("%s = %q, want %q verbatim", tt.env, got, tt.value)
			}
		})
	}
}

func TestEdgeWebDirDefaultsEmpty(t *testing.T) {
	edgeEnv(t)
	cfg, err := config.Load()
	if err != nil || cfg.WebDir != "" {
		t.Fatalf("WebDir = %q, %v", cfg.WebDir, err)
	}
}

func TestEdgeIntegers(t *testing.T) {
	vars := []struct {
		env string
		get func(config.Config) int
	}{
		{"DB_MAX_CONNS", func(c config.Config) int { return c.DBMaxConns }},
		{"LEDGER_WORKERS", func(c config.Config) int { return c.LedgerWorkers }},
		{"LEDGER_MAX_BATCH", func(c config.Config) int { return c.LedgerMaxBatch }},
		{"LEDGER_BATCH_CONCURRENCY", func(c config.Config) int { return c.LedgerBatchConcurrency }},
	}
	invalid := []string{"abc", "0", "-0", "-1", "-100", "1.5", "1e3", "0x10", " 8", "8 ", "８", "9223372036854775808", "99999999999999999999999"}
	accepted := []struct {
		raw  string
		want int
	}{{"1", 1}, {"+5", 5}, {"007", 7}, {"12", 12}}
	for _, v := range vars {
		for _, raw := range invalid {
			t.Run(v.env+" rejects "+raw, func(t *testing.T) {
				edgeEnv(t)
				t.Setenv(v.env, raw)
				_, err := config.Load()
				if err == nil {
					t.Fatalf("%s=%q accepted", v.env, raw)
				}
				if want := "config: " + v.env + " must be a positive integer"; err.Error() != want {
					t.Fatalf("error = %q, want %q", err, want)
				}
			})
		}
		for _, a := range accepted {
			t.Run(v.env+" accepts "+a.raw, func(t *testing.T) {
				edgeEnv(t)
				t.Setenv("DB_MAX_CONNS", "100")
				t.Setenv("LEDGER_WORKERS", "1")
				t.Setenv(v.env, a.raw)
				if v.env == "DB_MAX_CONNS" {
					t.Setenv("LEDGER_WORKERS", "")
					t.Setenv("DB_MAX_CONNS", a.raw)
				}
				cfg, err := config.Load()
				if v.env == "DB_MAX_CONNS" && a.want <= 8 {
					if err == nil || !strings.Contains(err.Error(), "LEDGER_WORKERS (8) must be below DB_MAX_CONNS") {
						t.Fatalf("DB_MAX_CONNS=%s with default workers: %v", a.raw, err)
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				if got := v.get(cfg); got != a.want {
					t.Fatalf("%s=%q = %d, want %d", v.env, a.raw, got, a.want)
				}
			})
		}
	}
}

func TestEdgeLargeIntegers(t *testing.T) {
	edgeEnv(t)
	t.Setenv("DB_MAX_CONNS", "9223372036854775807")
	t.Setenv("LEDGER_WORKERS", "9223372036854775806")
	t.Setenv("LEDGER_MAX_BATCH", "9223372036854775807")
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DBMaxConns != 1<<63-1 || cfg.LedgerWorkers != 1<<63-2 || cfg.LedgerMaxBatch != 1<<63-1 {
		t.Fatalf("cfg = %+v", cfg)
	}
}

func TestEdgeWorkersBelowConnections(t *testing.T) {
	tests := []struct {
		conns, workers string
		ok             bool
	}{
		{"8", "8", false},
		{"8", "9", false},
		{"8", "7", true},
		{"2", "1", true},
		{"1", "1", false},
		{"", "32", false},
		{"", "31", true},
		{"8", "", false},
		{"9", "", true},
	}
	for _, tt := range tests {
		t.Run("conns="+tt.conns+" workers="+tt.workers, func(t *testing.T) {
			edgeEnv(t)
			t.Setenv("DB_MAX_CONNS", tt.conns)
			t.Setenv("LEDGER_WORKERS", tt.workers)
			_, err := config.Load()
			if tt.ok != (err == nil) {
				t.Fatalf("Load() = %v, want ok=%v", err, tt.ok)
			}
			if err != nil && !strings.Contains(err.Error(), "must be below DB_MAX_CONNS") {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestEdgeBatchConcurrencyIndependentOfConnections(t *testing.T) {
	edgeEnv(t)
	t.Setenv("DB_MAX_CONNS", "9")
	t.Setenv("LEDGER_BATCH_CONCURRENCY", "1000")
	cfg, err := config.Load()
	if err != nil || cfg.LedgerBatchConcurrency != 1000 {
		t.Fatalf("Load() = %+v, %v", cfg, err)
	}
}

func TestEdgeBooleans(t *testing.T) {
	vars := []struct {
		env string
		get func(config.Config) bool
	}{
		{"DB_ALLOW_UNSAFE_DURABILITY", func(c config.Config) bool { return c.AllowUnsafeDurability }},
		{"WEBHOOK_ALLOW_INSECURE", func(c config.Config) bool { return c.WebhookAllowInsecure }},
	}
	truthy := []string{"1", "t", "T", "true", "TRUE", "True"}
	falsy := []string{"", "0", "f", "F", "false", "FALSE", "False"}
	invalid := []string{"yes", "no", "on", "off", "y", "n", "tRUE", "2", "-1", " true", "true ", "enabled"}
	for _, v := range vars {
		for _, raw := range truthy {
			t.Run(v.env+"="+raw, func(t *testing.T) {
				edgeEnv(t)
				t.Setenv(v.env, raw)
				cfg, err := config.Load()
				if err != nil || !v.get(cfg) {
					t.Fatalf("%s=%q = %v, %v; want true", v.env, raw, v.get(cfg), err)
				}
			})
		}
		for _, raw := range falsy {
			t.Run(v.env+"="+raw, func(t *testing.T) {
				edgeEnv(t)
				t.Setenv(v.env, raw)
				cfg, err := config.Load()
				if err != nil || v.get(cfg) {
					t.Fatalf("%s=%q = %v, %v; want false", v.env, raw, v.get(cfg), err)
				}
			})
		}
		for _, raw := range invalid {
			t.Run(v.env+" rejects "+raw, func(t *testing.T) {
				edgeEnv(t)
				t.Setenv(v.env, raw)
				_, err := config.Load()
				if err == nil || err.Error() != "config: "+v.env+" must be a boolean" {
					t.Fatalf("%s=%q: %v", v.env, raw, err)
				}
			})
		}
	}
}

func TestEdgeEventRetention(t *testing.T) {
	tests := []struct {
		raw  string
		want time.Duration
		ok   bool
	}{
		{"1ns", time.Nanosecond, true},
		{"720h", 720 * time.Hour, true},
		{"1h30m", 90 * time.Minute, true},
		{"1.5h", 90 * time.Minute, true},
		{"+2h", 2 * time.Hour, true},
		{"2562047h", 2562047 * time.Hour, true},
		{"0", 0, false},
		{"0s", 0, false},
		{"-1ns", 0, false},
		{"10", 0, false},
		{"30d", 0, false},
		{"1 h", 0, false},
		{" 1h", 0, false},
		{"2562048h", 0, false},
		{"99999999999999999999h", 0, false},
		{"h", 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.raw, func(t *testing.T) {
			edgeEnv(t)
			t.Setenv("EVENT_RETENTION", tt.raw)
			cfg, err := config.Load()
			if !tt.ok {
				if err == nil || err.Error() != "config: EVENT_RETENTION must be a positive duration such as 720h" {
					t.Fatalf("EVENT_RETENTION=%q: %v", tt.raw, err)
				}
				return
			}
			if err != nil || cfg.EventRetention != tt.want {
				t.Fatalf("EVENT_RETENTION=%q = %s, %v; want %s", tt.raw, cfg.EventRetention, err, tt.want)
			}
		})
	}
}

func TestEdgeSealKeyLength(t *testing.T) {
	tests := []struct {
		name string
		key  string
		ok   bool
	}{
		{"empty", "", false},
		{"31 bytes", strings.Repeat("k", 31), false},
		{"32 bytes", strings.Repeat("k", 32), true},
		{"33 bytes", strings.Repeat("k", 33), true},
		{"4096 bytes", strings.Repeat("k", 4096), true},
		{"11 runes 33 bytes", strings.Repeat("€", 11), true},
		{"10 runes 30 bytes", strings.Repeat("€", 10), false},
		{"32 spaces", strings.Repeat(" ", 32), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			edgeEnv(t)
			t.Setenv("LEDGER_SEAL_KEY", tt.key)
			cfg, err := config.Load()
			if tt.ok {
				if err != nil || cfg.LedgerSealKey != tt.key {
					t.Fatalf("Load() = %v, key kept = %v", err, cfg.LedgerSealKey == tt.key)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), "LEDGER_SEAL_KEY is required and must be at least 32 bytes") {
				t.Fatalf("Load() = %v", err)
			}
			if strings.Contains(err.Error(), tt.key) && tt.key != "" {
				t.Fatalf("error leaks the key: %v", err)
			}
		})
	}
}

func TestEdgeErrorPrecedence(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want string
	}{
		{"database url before seal key", map[string]string{"DATABASE_URL": "", "LEDGER_SEAL_KEY": ""}, "DATABASE_URL is required"},
		{"seal key before numbers", map[string]string{"LEDGER_SEAL_KEY": "x", "DB_MAX_CONNS": "x"}, "LEDGER_SEAL_KEY"},
		{"numbers in declared order", map[string]string{"LEDGER_WORKERS": "x", "DB_MAX_CONNS": "x"}, "DB_MAX_CONNS must"},
		{"integers before booleans", map[string]string{"LEDGER_BATCH_CONCURRENCY": "0", "DB_ALLOW_UNSAFE_DURABILITY": "x"}, "LEDGER_BATCH_CONCURRENCY"},
		{"booleans before retention", map[string]string{"WEBHOOK_ALLOW_INSECURE": "x", "EVENT_RETENTION": "x"}, "WEBHOOK_ALLOW_INSECURE"},
		{"retention before worker check", map[string]string{"EVENT_RETENTION": "x", "LEDGER_WORKERS": "99"}, "EVENT_RETENTION"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			edgeEnv(t)
			for k, v := range tt.env {
				t.Setenv(k, v)
			}
			cfg, err := config.Load()
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Load() = %v, want %q", err, tt.want)
			}
			if cfg != (config.Config{}) {
				t.Fatalf("Load() returned a partial config on error: %+v", cfg)
			}
		})
	}
}
