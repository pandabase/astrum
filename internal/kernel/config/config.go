package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"
)

type Config struct {
	DatabaseURL string
	HTTPAddr    string
	LogLevel    string
	LogFormat   string

	DBMaxConns int

	AllowUnsafeDurability bool

	LedgerWorkers  int
	LedgerMaxBatch int

	LedgerSealKey string

	WebhookAllowInsecure bool

	EventRetention time.Duration

	WebDir string
}

func Load() (Config, error) {
	cfg := Config{
		DatabaseURL: os.Getenv("DATABASE_URL"),
		HTTPAddr:    getenv("HTTP_ADDR", ":8080"),
		LogLevel:    getenv("LOG_LEVEL", "info"),
		LogFormat:   getenv("LOG_FORMAT", "text"),

		LedgerSealKey: os.Getenv("LEDGER_SEAL_KEY"),
		WebDir:        os.Getenv("WEB_DIR"),
	}
	if cfg.DatabaseURL == "" {
		return Config{}, errors.New("config: DATABASE_URL is required")
	}
	if len(cfg.LedgerSealKey) < 32 {
		return Config{}, errors.New("config: LEDGER_SEAL_KEY is required and must be at least 32 bytes (e.g. openssl rand -hex 32)")
	}

	var err error
	if cfg.DBMaxConns, err = intEnv("DB_MAX_CONNS", 32); err != nil {
		return Config{}, err
	}
	if cfg.LedgerWorkers, err = intEnv("LEDGER_WORKERS", 8); err != nil {
		return Config{}, err
	}
	if cfg.LedgerMaxBatch, err = intEnv("LEDGER_MAX_BATCH", 256); err != nil {
		return Config{}, err
	}
	if cfg.AllowUnsafeDurability, err = boolEnv("DB_ALLOW_UNSAFE_DURABILITY"); err != nil {
		return Config{}, err
	}
	if cfg.WebhookAllowInsecure, err = boolEnv("WEBHOOK_ALLOW_INSECURE"); err != nil {
		return Config{}, err
	}
	if cfg.EventRetention, err = durationEnv("EVENT_RETENTION", 30*24*time.Hour); err != nil {
		return Config{}, err
	}
	if cfg.LedgerWorkers >= cfg.DBMaxConns {
		return Config{}, fmt.Errorf("config: LEDGER_WORKERS (%d) must be below DB_MAX_CONNS (%d)", cfg.LedgerWorkers, cfg.DBMaxConns)
	}
	return cfg, nil
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func intEnv(key string, fallback int) (int, error) {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback, nil
	}
	v, err := strconv.Atoi(raw)
	if err != nil || v <= 0 {
		return 0, fmt.Errorf("config: %s must be a positive integer", key)
	}
	return v, nil
}

func durationEnv(key string, fallback time.Duration) (time.Duration, error) {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback, nil
	}
	v, err := time.ParseDuration(raw)
	if err != nil || v <= 0 {
		return 0, fmt.Errorf("config: %s must be a positive duration such as 720h", key)
	}
	return v, nil
}

func boolEnv(key string) (bool, error) {
	raw := os.Getenv(key)
	if raw == "" {
		return false, nil
	}
	v, err := strconv.ParseBool(raw)
	if err != nil {
		return false, fmt.Errorf("config: %s must be a boolean", key)
	}
	return v, nil
}
