package testdb

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"io/fs"
	neturl "net/url"
	"os"
	"testing"

	"github.com/charmbracelet/log"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pandabase/astrum/internal/kernel/db"
)

const EnvURL = "ASTRUM_TEST_DATABASE_URL"

var slots = make(chan struct{}, 4)

func New(t testing.TB, migrations map[string]fs.FS) *pgxpool.Pool {
	t.Helper()

	url := os.Getenv(EnvURL)
	if url == "" || testing.Short() {
		t.Skipf("integration test: set %s to run", EnvURL)
	}

	slots <- struct{}{}
	t.Cleanup(func() { <-slots })

	ctx := context.Background()
	schema := "test_" + randomSuffix(t)

	admin, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer admin.Close(ctx)
	if _, err := admin.Exec(ctx, `CREATE SCHEMA `+schema); err != nil {
		t.Fatalf("create schema: %v", err)
	}

	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatalf("parse config: %v", err)
	}
	db.Harden(cfg, 32)
	cfg.ConnConfig.RuntimeParams["search_path"] = schema

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}

	t.Cleanup(func() {
		pool.Close()
		conn, err := pgx.Connect(ctx, url)
		if err != nil {
			t.Errorf("cleanup connect: %v", err)
			return
		}
		defer conn.Close(ctx)
		if _, err := conn.Exec(ctx, `DROP SCHEMA `+schema+` CASCADE`); err != nil {
			t.Errorf("drop schema: %v", err)
		}
	})

	for name, m := range migrations {
		if err := db.Migrate(ctx, pool, Logger(), name, m); err != nil {
			t.Fatalf("migrate %s: %v", name, err)
		}
	}
	return pool
}

func URL(t testing.TB) string {
	t.Helper()
	pool := New(t, nil)
	schema := pool.Config().ConnConfig.RuntimeParams["search_path"]
	pool.Close()

	u, err := neturl.Parse(os.Getenv(EnvURL))
	if err != nil {
		t.Fatalf("parse %s: %v", EnvURL, err)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	return u.String()
}

func Logger() *log.Logger {
	if os.Getenv("ASTRUM_TEST_LOG") != "" {
		return log.NewWithOptions(os.Stderr, log.Options{Level: log.DebugLevel, ReportTimestamp: true})
	}
	return log.New(io.Discard)
}

func randomSuffix(t testing.TB) string {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatalf("random: %v", err)
	}
	return hex.EncodeToString(b[:])
}
