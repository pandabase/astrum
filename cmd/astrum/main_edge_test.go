package main

import (
	"context"
	"encoding/json/v2"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pandabase/astrum/internal/kernel/auth"
	"github.com/pandabase/astrum/internal/kernel/config"
	"github.com/pandabase/astrum/internal/kernel/db"
	"github.com/pandabase/astrum/internal/kernel/testdb"
	"github.com/pandabase/astrum/internal/kernel/typeid"
)

const (
	edgeSealKey  = "edge-seal-key-0123456789abcdef-0123456789"
	edgeOtherKey = "edge-other-key-0123456789abcdef-012345678"
)

func edgeConfig(t *testing.T, url string) config.Config {
	t.Helper()
	return config.Config{
		DatabaseURL:    url,
		HTTPAddr:       freeAddr(t),
		LogLevel:       "error",
		LogFormat:      "text",
		DBMaxConns:     16,
		LedgerWorkers:  4,
		LedgerMaxBatch: 64,
		LedgerSealKey:  edgeSealKey,
	}
}

func edgePool(t *testing.T, url string) *pgxpool.Pool {
	t.Helper()
	pool, err := db.Connect(context.Background(), url, db.Options{MaxConns: 4})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func runWithin(t *testing.T, ctx context.Context, cfg config.Config, limit time.Duration) error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- run(ctx, cfg) }()
	select {
	case err := <-done:
		return err
	case <-time.After(limit):
		t.Fatalf("run() did not return within %s", limit)
		return nil
	}
}

type edgeServer struct {
	base   string
	cancel context.CancelFunc
	done   chan error
}

func startEdge(t *testing.T, cfg config.Config) *edgeServer {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	s := &edgeServer{base: "http://" + cfg.HTTPAddr, cancel: cancel, done: make(chan error, 1)}
	go func() { s.done <- run(ctx, cfg) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-s.done:
		case <-time.After(40 * time.Second):
		}
	})
	waitHealthy(t, s.base, s.done)
	return s
}

func (s *edgeServer) stop(t *testing.T) error {
	t.Helper()
	s.cancel()
	select {
	case err := <-s.done:
		s.done <- err
		return err
	case <-time.After(40 * time.Second):
		t.Fatal("shutdown did not complete")
		return nil
	}
}

func TestEdgeHealthHandler(t *testing.T) {
	pool := testdb.New(t, nil)
	h := health(pool)

	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "application/json" || rec.Body.String() != "{\"status\":\"ok\"}\n" {
		t.Fatalf("healthy = %d %q %q", rec.Code, rec.Header().Get("Content-Type"), rec.Body.String())
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	rec = httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil).WithContext(ctx))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("canceled request = %d, want 503", rec.Code)
	}

	pool.Close()
	rec = httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Content-Type") != "application/problem+json" {
		t.Fatalf("closed pool = %d %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	var problem struct {
		Status int    `json:"status"`
		Code   string `json:"code"`
		Detail string `json:"detail"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &problem); err != nil {
		t.Fatal(err)
	}
	if problem.Status != 503 || problem.Code != "service_unavailable" || problem.Detail != "database unavailable" {
		t.Fatalf("problem = %+v", problem)
	}
	if strings.Contains(rec.Body.String(), "closed pool") {
		t.Fatalf("health leaks the database error: %s", rec.Body.String())
	}
}

func TestEdgeStartupFailuresWithoutDatabase(t *testing.T) {
	base := config.Config{
		DatabaseURL:   "postgres://u@127.0.0.1:1/db?sslmode=disable&connect_timeout=2",
		HTTPAddr:      "127.0.0.1:0",
		LogLevel:      "error",
		LogFormat:     "text",
		DBMaxConns:    4,
		LedgerWorkers: 1,
		LedgerSealKey: edgeSealKey,
	}
	tests := []struct {
		name   string
		change func(*config.Config)
		want   string
	}{
		{"bad log level", func(c *config.Config) { c.LogLevel = "loud" }, "logger:"},
		{"bad log format", func(c *config.Config) { c.LogFormat = "xml" }, `logger: unknown format "xml"`},
		{"missing database url", func(c *config.Config) { c.DatabaseURL = "" }, "db: ping"},
		{"unparseable database url", func(c *config.Config) { c.DatabaseURL = "://nope" }, "db: parse url"},
		{"unreachable database", func(*config.Config) {}, "db: ping"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.name == "missing database url" {
				t.Setenv("PGHOST", "127.0.0.1")
				t.Setenv("PGPORT", "1")
				t.Setenv("PGCONNECT_TIMEOUT", "2")
				t.Setenv("PGSSLMODE", "disable")
			}
			cfg := base
			tt.change(&cfg)
			err := runWithin(t, context.Background(), cfg, 15*time.Second)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("run() = %v, want %q", err, tt.want)
			}
		})
	}
	t.Run("canceled before start", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := runWithin(t, ctx, base, 15*time.Second); !errors.Is(err, context.Canceled) {
			t.Fatalf("run() = %v, want context.Canceled", err)
		}
	})
}

func TestEdgeStartupFailuresWithDatabase(t *testing.T) {
	t.Run("short seal key", func(t *testing.T) {
		cfg := edgeConfig(t, testdb.URL(t))
		cfg.LedgerSealKey = strings.Repeat("k", 31)
		if err := runWithin(t, context.Background(), cfg, 30*time.Second); err == nil || !strings.Contains(err.Error(), "seal key must be at least 32 bytes") {
			t.Fatalf("run() = %v", err)
		}
	})
	t.Run("web dir without index", func(t *testing.T) {
		cfg := edgeConfig(t, testdb.URL(t))
		cfg.WebDir = t.TempDir()
		if err := runWithin(t, context.Background(), cfg, 30*time.Second); err == nil || !strings.Contains(err.Error(), "has no index.html") {
			t.Fatalf("run() = %v", err)
		}
	})
	t.Run("missing web dir", func(t *testing.T) {
		cfg := edgeConfig(t, testdb.URL(t))
		cfg.WebDir = t.TempDir() + "/missing"
		if err := runWithin(t, context.Background(), cfg, 30*time.Second); err == nil || !strings.Contains(err.Error(), "has no index.html") {
			t.Fatalf("run() = %v", err)
		}
	})
	t.Run("modified kernel migration", func(t *testing.T) {
		url := testdb.URL(t)
		pool := edgePool(t, url)
		tampered := fstest.MapFS{"0001_init.sql": {Data: []byte(`CREATE TABLE api_keys (id uuid PRIMARY KEY)`)}}
		if err := db.Migrate(context.Background(), pool, testdb.Logger(), "auth", tampered); err != nil {
			t.Fatal(err)
		}
		err := runWithin(t, context.Background(), edgeConfig(t, url), 30*time.Second)
		if err == nil || !strings.Contains(err.Error(), "migrate auth/0001_init: applied migration was modified") {
			t.Fatalf("run() = %v", err)
		}
	})
	t.Run("address in use", func(t *testing.T) {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer l.Close()
		cfg := edgeConfig(t, testdb.URL(t))
		cfg.HTTPAddr = l.Addr().String()
		if err := runWithin(t, context.Background(), cfg, 40*time.Second); err == nil || !strings.Contains(err.Error(), "address already in use") {
			t.Fatalf("run() = %v", err)
		}
	})
	t.Run("invalid address", func(t *testing.T) {
		cfg := edgeConfig(t, testdb.URL(t))
		cfg.HTTPAddr = "127.0.0.1:notaport"
		if err := runWithin(t, context.Background(), cfg, 40*time.Second); err == nil {
			t.Fatal("run() accepted an invalid listen address")
		}
	})
}

func TestEdgeSealKeyMismatch(t *testing.T) {
	url := testdb.URL(t)
	cfg := edgeConfig(t, url)
	s := startEdge(t, cfg)

	var out strings.Builder
	if err := runKeys(context.Background(), cfg, []string{"create", "-name", "seal"}, &out); err != nil {
		t.Fatal(err)
	}
	token := tokenFrom(t, out.String())
	ledger := postAs(t, token, s.base+"/v1/ledgers", "", `{"name":"seal"}`)["id"]
	a := postAs(t, token, s.base+"/v1/accounts", "", fmt.Sprintf(`{"ledger_id":%q,"code":"a","currency":"USD","normal_side":"debit","allow_negative":true}`, ledger))["id"]
	b := postAs(t, token, s.base+"/v1/accounts", "", fmt.Sprintf(`{"ledger_id":%q,"code":"b","currency":"USD","normal_side":"credit","allow_negative":true}`, ledger))["id"]
	postAs(t, token, s.base+"/v1/transactions", "seal-1", fmt.Sprintf(`{"entries":[{"account_id":%q,"side":"debit","amount":"5"},{"account_id":%q,"side":"credit","amount":"5"}]}`, a, b))

	pool := edgePool(t, url)
	deadline := time.Now().Add(20 * time.Second)
	for {
		var n int
		if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM ledger_seals`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("transaction was never sealed")
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err := s.stop(t); err != nil {
		t.Fatalf("first run() = %v", err)
	}

	wrong := edgeConfig(t, url)
	wrong.LedgerSealKey = edgeOtherKey
	err := runWithin(t, context.Background(), wrong, 30*time.Second)
	if err == nil || !strings.Contains(err.Error(), "seal key does not match the existing chain head") {
		t.Fatalf("run() with another seal key = %v", err)
	}

	again := startEdge(t, edgeConfig(t, url))
	if err := again.stop(t); err != nil {
		t.Fatalf("restart with the original key = %v", err)
	}
}

func TestEdgeServerBehaviour(t *testing.T) {
	cfg := edgeConfig(t, testdb.URL(t))
	s := startEdge(t, cfg)

	tests := []struct {
		method string
		path   string
		want   int
	}{
		{http.MethodGet, "/healthz", http.StatusOK},
		{http.MethodHead, "/healthz", http.StatusOK},
		{http.MethodPost, "/healthz", http.StatusMethodNotAllowed},
		{http.MethodGet, "/healthz/", http.StatusUnauthorized},
		{http.MethodGet, "/v1/ledgers", http.StatusUnauthorized},
		{http.MethodGet, "/ledgers/x", http.StatusUnauthorized},
	}
	for _, tt := range tests {
		t.Run(tt.method+" "+tt.path, func(t *testing.T) {
			req, err := http.NewRequest(tt.method, s.base+tt.path, nil)
			if err != nil {
				t.Fatal(err)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if resp.StatusCode != tt.want {
				t.Fatalf("%s %s = %d, want %d", tt.method, tt.path, resp.StatusCode, tt.want)
			}
		})
	}

	t.Run("graceful shutdown releases the port", func(t *testing.T) {
		if err := s.stop(t); err != nil {
			t.Fatalf("run() = %v", err)
		}
		if _, err := http.Get(s.base + "/healthz"); err == nil {
			t.Fatal("server still answering after shutdown")
		}
		l, err := net.Listen("tcp", cfg.HTTPAddr)
		if err != nil {
			t.Fatalf("port not released: %v", err)
		}
		l.Close()
	})
}

func TestEdgeKeysWithoutDatabase(t *testing.T) {
	cfg := config.Config{DatabaseURL: "://nope", LogFormat: "text"}
	t.Run("no args prints usage without connecting", func(t *testing.T) {
		err := runKeys(context.Background(), cfg, nil, &strings.Builder{})
		if err == nil || err.Error() != keysUsage {
			t.Fatalf("runKeys() = %v", err)
		}
		if err := runKeys(context.Background(), cfg, []string{}, &strings.Builder{}); err == nil || err.Error() != keysUsage {
			t.Fatalf("runKeys([]) = %v", err)
		}
	})
	t.Run("bad log format", func(t *testing.T) {
		bad := cfg
		bad.LogFormat = "xml"
		if err := runKeys(context.Background(), bad, []string{"create"}, &strings.Builder{}); err == nil || !strings.Contains(err.Error(), "logger") {
			t.Fatalf("runKeys() = %v", err)
		}
	})
	t.Run("bad database url", func(t *testing.T) {
		if err := runKeys(context.Background(), cfg, []string{"create", "-name", "x"}, &strings.Builder{}); err == nil || !strings.Contains(err.Error(), "db: parse url") {
			t.Fatalf("runKeys() = %v", err)
		}
	})
	t.Run("usage text", func(t *testing.T) {
		for _, want := range []string{"astrum keys create -name <name> [-role admin|write|read] [-expires <duration>]", "astrum keys revoke -id <key_...>"} {
			if !strings.Contains(keysUsage, want) {
				t.Fatalf("usage lacks %q", want)
			}
		}
	})
}

var createdLine = regexp.MustCompile(`^created (admin|write|read) key (key_[0-9a-hjkmnp-tv-z]{26}) \((.*)\)\n(sk_[0-9a-hjkmnp-tv-z]{26}_[A-Za-z0-9_-]{43})\nStore it now: it cannot be shown again\.\n$`)

func TestEdgeKeysCLI(t *testing.T) {
	url := testdb.URL(t)
	cfg := config.Config{DatabaseURL: url, LogFormat: "text"}
	pool := edgePool(t, url)
	authn := auth.New(pool, testdb.Logger())
	ctx := context.Background()

	keys := func(args ...string) (string, error) {
		var out strings.Builder
		err := runKeys(ctx, cfg, args, &out)
		return out.String(), err
	}

	t.Run("create roles", func(t *testing.T) {
		tests := []struct {
			args []string
			role string
			name string
		}{
			{[]string{"create", "-name", "default"}, "admin", "default"},
			{[]string{"create", "-name", "ops", "-role", "admin"}, "admin", "ops"},
			{[]string{"create", "-name", "svc", "-role", "write"}, "write", "svc"},
			{[]string{"create", "-role", "read", "-name", "dash board"}, "read", "dash board"},
			{[]string{"create", "-name=équipe ünïcode"}, "admin", "équipe ünïcode"},
			{[]string{"create", "-name", strings.Repeat("n", 255)}, "admin", strings.Repeat("n", 255)},
		}
		for i, tt := range tests {
			t.Run(fmt.Sprintf("%d %s", i, tt.role), func(t *testing.T) {
				out, err := keys(tt.args...)
				if err != nil {
					t.Fatal(err)
				}
				m := createdLine.FindStringSubmatch(out)
				if m == nil {
					t.Fatalf("output = %q", out)
				}
				if m[1] != tt.role || m[3] != tt.name {
					t.Fatalf("role/name = %s/%s, want %s/%s", m[1], m[3], tt.role, tt.name)
				}
				id, err := typeid.Parse("key", m[2])
				if err != nil {
					t.Fatal(err)
				}
				k, err := authn.Authenticate(ctx, m[4])
				if err != nil || k.ID != id || string(k.Role) != tt.role || k.ExpiresAt != nil {
					t.Fatalf("Authenticate() = %+v, %v", k, err)
				}
			})
		}
	})

	t.Run("create with expiry", func(t *testing.T) {
		out, err := keys("create", "-name", "temp", "-expires", "1h")
		if err != nil {
			t.Fatal(err)
		}
		m := createdLine.FindStringSubmatch(out)
		if m == nil {
			t.Fatalf("output = %q", out)
		}
		k, err := authn.Authenticate(ctx, m[4])
		if err != nil || k.ExpiresAt == nil {
			t.Fatalf("Authenticate() = %+v, %v", k, err)
		}
		if left := time.Until(*k.ExpiresAt); left < 59*time.Minute || left > 61*time.Minute {
			t.Fatalf("expires in %s, want about 1h", left)
		}
	})

	t.Run("create rejects", func(t *testing.T) {
		tests := []struct {
			name string
			args []string
			want string
		}{
			{"missing name", []string{"create"}, "name must be 1-255 characters"},
			{"blank name", []string{"create", "-name", "   "}, "name must be 1-255 characters"},
			{"long name", []string{"create", "-name", strings.Repeat("n", 256)}, "name must be 1-255 characters"},
			{"unknown role", []string{"create", "-name", "x", "-role", "superuser"}, "role must be admin, write or read"},
			{"uppercase role", []string{"create", "-name", "x", "-role", "ADMIN"}, "role must be admin, write or read"},
			{"empty role", []string{"create", "-name", "x", "-role", ""}, "role must be admin, write or read"},
			{"bad expiry", []string{"create", "-name", "x", "-expires", "tomorrow"}, "invalid value"},
			{"expiry without unit", []string{"create", "-name", "x", "-expires", "24"}, "invalid value"},
			{"unknown flag", []string{"create", "-name", "x", "-admin"}, "flag provided but not defined: -admin"},
			{"missing flag value", []string{"create", "-name"}, "flag needs an argument"},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				out, err := keys(tt.args...)
				if err == nil || !strings.Contains(err.Error(), tt.want) {
					t.Fatalf("runKeys(%v) = %v, want %q", tt.args, err, tt.want)
				}
				if out != "" {
					t.Fatalf("output on error = %q", out)
				}
			})
		}
		if _, err := keys("create", "-name", "x", "-role", "god"); !errors.Is(err, auth.ErrInvalid) {
			t.Fatalf("invalid role error = %v, want auth.ErrInvalid", err)
		}
	})

	t.Run("create help", func(t *testing.T) {
		if _, err := keys("create", "-h"); !errors.Is(err, flag.ErrHelp) {
			t.Fatalf("runKeys(create -h) = %v, want flag.ErrHelp", err)
		}
	})

	t.Run("negative expiry is rejected", func(t *testing.T) {
		out, err := keys("create", "-name", "negative", "-expires", "-1h")
		if err == nil {
			m := createdLine.FindStringSubmatch(out)
			var expires *time.Time
			if m != nil {
				if k, err := authn.Authenticate(ctx, m[4]); err == nil {
					expires = k.ExpiresAt
				}
			}
			t.Fatalf("runKeys(create -expires -1h) created a key (expires_at = %v): %q", expires, out)
		}
	})

	t.Run("trailing arguments are rejected", func(t *testing.T) {
		for _, args := range [][]string{
			{"create", "-name", "trail", "extra", "-role", "read"},
			{"revoke", "-id", "key_01h455vb4pex5vsknk084sn02q", "extra"},
		} {
			out, err := keys(args...)
			if err == nil || !strings.Contains(err.Error(), `unexpected argument "extra"`) || out != "" {
				t.Fatalf("runKeys(%q) = %q, %v", args, out, err)
			}
		}
	})

	t.Run("unknown subcommands", func(t *testing.T) {
		for _, sub := range []string{"list", "delete", "CREATE", "", "-h"} {
			if _, err := keys(sub); err == nil || err.Error() != keysUsage {
				t.Fatalf("runKeys(%q) = %v, want usage", sub, err)
			}
		}
	})

	t.Run("revoke", func(t *testing.T) {
		out, err := keys("create", "-name", "doomed", "-role", "write")
		if err != nil {
			t.Fatal(err)
		}
		m := createdLine.FindStringSubmatch(out)
		if m == nil {
			t.Fatalf("output = %q", out)
		}
		out, err = keys("revoke", "-id", m[2])
		if err != nil || out != fmt.Sprintf("revoked %s (doomed)\n", m[2]) {
			t.Fatalf("revoke = %q, %v", out, err)
		}
		if _, err := authn.Authenticate(ctx, m[4]); !errors.Is(err, auth.ErrUnauthenticated) {
			t.Fatalf("revoked key still authenticates: %v", err)
		}
		out, err = keys("revoke", "-id", m[2])
		if err != nil || out != fmt.Sprintf("revoked %s (doomed)\n", m[2]) {
			t.Fatalf("second revoke = %q, %v", out, err)
		}
		id, _ := typeid.Parse("key", m[2])
		k, err := authn.Key(ctx, id)
		if err != nil || k.RevokedAt == nil {
			t.Fatalf("Key() = %+v, %v", k, err)
		}
	})

	t.Run("revoke rejects", func(t *testing.T) {
		unknown := typeid.Encode("key", [16]byte{1})
		tests := []struct {
			name string
			args []string
			want error
			text string
		}{
			{"missing id", []string{"revoke"}, typeid.ErrInvalid, "expected a key_ id"},
			{"wrong prefix", []string{"revoke", "-id", "acct_01h455vb4pex5vsknk084sn02q"}, typeid.ErrInvalid, "expected a key_ id"},
			{"malformed", []string{"revoke", "-id", "key_nope"}, typeid.ErrInvalid, "malformed key_ id"},
			{"raw uuid", []string{"revoke", "-id", "01890a5d-ac96-774b-bcce-b302099a8057"}, typeid.ErrInvalid, "expected a key_ id"},
			{"unknown key", []string{"revoke", "-id", unknown}, auth.ErrNotFound, "api key not found"},
			{"unknown flag", []string{"revoke", "-name", "x"}, nil, "flag provided but not defined: -name"},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				out, err := keys(tt.args...)
				if err == nil || !strings.Contains(err.Error(), tt.text) || (tt.want != nil && !errors.Is(err, tt.want)) {
					t.Fatalf("runKeys(%v) = %v, want %q", tt.args, err, tt.text)
				}
				if out != "" {
					t.Fatalf("output on error = %q", out)
				}
			})
		}
	})

	t.Run("canceled context", func(t *testing.T) {
		cctx, cancel := context.WithCancel(ctx)
		cancel()
		if err := runKeys(cctx, cfg, []string{"create", "-name", "x"}, &strings.Builder{}); !errors.Is(err, context.Canceled) {
			t.Fatalf("runKeys() = %v, want context.Canceled", err)
		}
	})
}

func TestEdgeKeysMigratesFreshDatabase(t *testing.T) {
	url := testdb.URL(t)
	var out strings.Builder
	if err := runKeys(context.Background(), config.Config{DatabaseURL: url}, []string{"create", "-name", "first"}, &out); err != nil {
		t.Fatal(err)
	}
	pool := edgePool(t, url)
	var modules []string
	rows, err := pool.Query(context.Background(), `SELECT DISTINCT module FROM schema_migrations ORDER BY module`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var m string
		if err := rows.Scan(&m); err != nil {
			t.Fatal(err)
		}
		modules = append(modules, m)
	}
	if strings.Join(modules, ",") != "auth" {
		t.Fatalf("keys CLI migrated %v, want only auth", modules)
	}
	admins, err := auth.New(pool, testdb.Logger()).ActiveAdmins(context.Background())
	if err != nil || admins != 1 {
		t.Fatalf("ActiveAdmins() = %d, %v", admins, err)
	}
}

func TestEdgeMainEnvWithoutConfig(t *testing.T) {
	if os.Getenv("ASTRUM_EDGE_MAIN") == "1" {
		os.Args = []string{"astrum", "keys"}
		main()
		return
	}
	if testing.Short() {
		t.Skip("spawns a process")
	}
	tests := []struct {
		name string
		env  []string
		want string
	}{
		{"missing database url", []string{"DATABASE_URL=", "LEDGER_SEAL_KEY=" + edgeSealKey}, "DATABASE_URL is required"},
		{"short seal key", []string{"DATABASE_URL=postgres://x/y", "LEDGER_SEAL_KEY=short"}, "LEDGER_SEAL_KEY is required"},
		{"keys without subcommand", []string{"DATABASE_URL=postgres://x/y", "LEDGER_SEAL_KEY=" + edgeSealKey}, "usage:"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := edgeCommand(t, "^TestEdgeMainEnvWithoutConfig$")
			cmd.Env = append(cmd.Env, "ASTRUM_EDGE_MAIN=1")
			cmd.Env = append(cmd.Env, tt.env...)
			out, err := cmd.CombinedOutput()
			if code := exitCode(t, err); code != 1 || !strings.Contains(string(out), tt.want) || !strings.Contains(string(out), "astrum exited") {
				t.Fatalf("exit %d, output:\n%s", code, out)
			}
		})
	}
}

func edgeCommand(t *testing.T, pattern string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run="+pattern)
	cmd.Env = os.Environ()
	for _, k := range []string{"HTTP_ADDR", "LOG_LEVEL", "LOG_FORMAT", "WEB_DIR", "DB_MAX_CONNS", "LEDGER_WORKERS", "LEDGER_MAX_BATCH", "LEDGER_BATCH_CONCURRENCY", "DB_ALLOW_UNSAFE_DURABILITY", "WEBHOOK_ALLOW_INSECURE", "EVENT_RETENTION"} {
		cmd.Env = append(cmd.Env, k+"=")
	}
	return cmd
}

func exitCode(t *testing.T, err error) int {
	t.Helper()
	if err == nil {
		return 0
	}
	if exitErr, ok := errors.AsType[*exec.ExitError](err); ok {
		return exitErr.ExitCode()
	}
	t.Fatal(err)
	return -1
}
