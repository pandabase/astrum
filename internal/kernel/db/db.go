package db

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Options struct {
	MaxConns int32

	AllowUnsafeDurability bool
}

var sessionParams = map[string]string{
	"synchronous_commit":                  "on",
	"lock_timeout":                        "10s",
	"statement_timeout":                   "30s",
	"idle_in_transaction_session_timeout": "30s",
	"application_name":                    "astrum",
}

func Connect(ctx context.Context, url string, opts Options) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("db: parse url: %w", err)
	}
	Harden(cfg, opts.MaxConns)

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("db: connect: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("db: ping: %w", err)
	}
	if err := CheckDurability(ctx, pool); err != nil && !opts.AllowUnsafeDurability {
		pool.Close()
		return nil, err
	}
	return pool, nil
}

func Harden(cfg *pgxpool.Config, maxConns int32) {
	for k, v := range sessionParams {
		cfg.ConnConfig.RuntimeParams[k] = v
	}
	if maxConns > 0 {
		cfg.MaxConns = maxConns
	}
	cfg.MaxConnLifetime = time.Hour
	cfg.MaxConnIdleTime = 5 * time.Minute
	cfg.HealthCheckPeriod = 15 * time.Second
}

type rowQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func CheckDurability(ctx context.Context, q rowQuerier) error {
	required := map[string]string{
		"fsync":              "on",
		"full_page_writes":   "on",
		"synchronous_commit": "on",
	}
	for name, want := range required {
		var got string
		if err := q.QueryRow(ctx, "SELECT current_setting($1)", name).Scan(&got); err != nil {
			return fmt.Errorf("db: read %s: %w", name, err)
		}
		if got != want {
			return fmt.Errorf("db: unsafe durability: %s = %s, want %s", name, got, want)
		}
	}
	return nil
}
