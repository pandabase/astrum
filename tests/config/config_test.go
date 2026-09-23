package config_test

import (
	"testing"
	"time"

	"github.com/pandabase/astrum/internal/kernel/config"
)

func TestLoad(t *testing.T) {
	const key = "0123456789abcdef0123456789abcdef"

	t.Run("requires seal key", func(t *testing.T) {
		t.Setenv("DATABASE_URL", "postgres://localhost/astrum")
		t.Setenv("LEDGER_SEAL_KEY", "short")
		if _, err := config.Load(); err == nil {
			t.Fatal("Load() error = nil, want error")
		}
	})

	t.Run("rejects bad numbers", func(t *testing.T) {
		t.Setenv("DATABASE_URL", "postgres://localhost/astrum")
		t.Setenv("LEDGER_SEAL_KEY", key)
		for _, env := range []string{"DB_MAX_CONNS", "LEDGER_WORKERS", "LEDGER_MAX_BATCH", "LEDGER_BATCH_CONCURRENCY"} {
			t.Setenv(env, "-1")
			if _, err := config.Load(); err == nil {
				t.Errorf("%s=-1 accepted", env)
			}
			t.Setenv(env, "")
		}
		t.Setenv("DB_ALLOW_UNSAFE_DURABILITY", "maybe")
		if _, err := config.Load(); err == nil {
			t.Error("non-boolean DB_ALLOW_UNSAFE_DURABILITY accepted")
		}
	})

	t.Run("workers must leave spare connections", func(t *testing.T) {
		t.Setenv("DATABASE_URL", "postgres://localhost/astrum")
		t.Setenv("LEDGER_SEAL_KEY", key)
		t.Setenv("DB_MAX_CONNS", "8")
		t.Setenv("LEDGER_WORKERS", "8")
		if _, err := config.Load(); err == nil {
			t.Fatal("Load() error = nil, want error")
		}
	})

	t.Run("requires database url", func(t *testing.T) {
		t.Setenv("DATABASE_URL", "")
		if _, err := config.Load(); err == nil {
			t.Fatal("Load() error = nil, want error")
		}
	})

	t.Run("defaults", func(t *testing.T) {
		t.Setenv("DATABASE_URL", "postgres://localhost/astrum")
		t.Setenv("LEDGER_SEAL_KEY", key)
		t.Setenv("HTTP_ADDR", "")
		t.Setenv("LOG_LEVEL", "")
		t.Setenv("LOG_FORMAT", "")

		cfg, err := config.Load()
		if err != nil {
			t.Fatalf("Load() error = %v", err)
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
			LedgerSealKey:          key,
			EventRetention:         30 * 24 * time.Hour,
		}
		if cfg != want {
			t.Fatalf("Load() = %+v, want %+v", cfg, want)
		}
	})

	t.Run("overrides", func(t *testing.T) {
		t.Setenv("DATABASE_URL", "postgres://db/astrum")
		t.Setenv("LEDGER_SEAL_KEY", key)
		t.Setenv("DB_MAX_CONNS", "64")
		t.Setenv("LEDGER_WORKERS", "16")
		t.Setenv("LEDGER_MAX_BATCH", "512")
		t.Setenv("LEDGER_BATCH_CONCURRENCY", "2")
		t.Setenv("DB_ALLOW_UNSAFE_DURABILITY", "true")
		t.Setenv("HTTP_ADDR", ":9090")
		t.Setenv("LOG_LEVEL", "debug")
		t.Setenv("LOG_FORMAT", "json")

		cfg, err := config.Load()
		if err != nil {
			t.Fatalf("Load() error = %v", err)
		}
		want := config.Config{
			DatabaseURL:            "postgres://db/astrum",
			HTTPAddr:               ":9090",
			LogLevel:               "debug",
			LogFormat:              "json",
			DBMaxConns:             64,
			AllowUnsafeDurability:  true,
			LedgerWorkers:          16,
			LedgerMaxBatch:         512,
			LedgerBatchConcurrency: 2,
			LedgerSealKey:          key,
			EventRetention:         30 * 24 * time.Hour,
		}
		if cfg != want {
			t.Fatalf("Load() = %+v, want %+v", cfg, want)
		}
	})
}

func TestEventRetention(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://localhost/astrum")
	t.Setenv("LEDGER_SEAL_KEY", "0123456789abcdef0123456789abcdef")

	cfg, err := config.Load()
	if err != nil || cfg.EventRetention != 30*24*time.Hour {
		t.Fatalf("default retention = %s, %v", cfg.EventRetention, err)
	}
	t.Setenv("EVENT_RETENTION", "48h")
	if cfg, err = config.Load(); err != nil || cfg.EventRetention != 48*time.Hour {
		t.Fatalf("retention = %s, %v", cfg.EventRetention, err)
	}
	for _, bad := range []string{"30 days", "-1h", "0s"} {
		t.Setenv("EVENT_RETENTION", bad)
		if _, err := config.Load(); err == nil {
			t.Errorf("EVENT_RETENTION=%q accepted", bad)
		}
	}
}
