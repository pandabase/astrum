package db_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pandabase/astrum/internal/kernel/db"
	"github.com/pandabase/astrum/internal/kernel/testdb"
)

const edgeMigrationLock = 7_341_902_118

func TestEdgeConnectBadURL(t *testing.T) {
	for _, url := range []string{"://nope", "postgres://host:notaport/db", "postgres://%zz", "host=x port=abc"} {
		t.Run(url, func(t *testing.T) {
			pool, err := db.Connect(context.Background(), url, db.Options{})
			if err == nil || pool != nil || !strings.HasPrefix(err.Error(), "db: parse url") {
				t.Fatalf("Connect(%q) = %v, %v", url, pool, err)
			}
		})
	}
}

func TestEdgeConnectUnreachable(t *testing.T) {
	start := time.Now()
	pool, err := db.Connect(context.Background(), "postgres://u:p@127.0.0.1:1/db?sslmode=disable&connect_timeout=2", db.Options{})
	if err == nil || pool != nil || !strings.HasPrefix(err.Error(), "db: ping") {
		t.Fatalf("Connect() = %v, %v", pool, err)
	}
	if strings.Contains(err.Error(), ":p@") {
		t.Fatalf("error leaks the password: %v", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatalf("unreachable host took %s", time.Since(start))
	}
}

func TestEdgeConnectCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	pool, err := db.Connect(ctx, "postgres://u@127.0.0.1:1/db?sslmode=disable", db.Options{})
	if err == nil || pool != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("Connect() = %v, %v, want context.Canceled", pool, err)
	}
}

func TestEdgeHarden(t *testing.T) {
	tests := []struct {
		name     string
		url      string
		maxConns int32
		want     int32
	}{
		{"explicit", "postgres://u@h/db", 7, 7},
		{"zero keeps url value", "postgres://u@h/db?pool_max_conns=3", 0, 3},
		{"negative keeps url value", "postgres://u@h/db?pool_max_conns=3", -1, 3},
		{"option beats url", "postgres://u@h/db?pool_max_conns=3", 10, 10},
		{"one", "postgres://u@h/db", 1, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := pgxpool.ParseConfig(tt.url)
			if err != nil {
				t.Fatal(err)
			}
			db.Harden(cfg, tt.maxConns)
			if cfg.MaxConns != tt.want {
				t.Fatalf("MaxConns = %d, want %d", cfg.MaxConns, tt.want)
			}
			if cfg.MaxConnLifetime != time.Hour || cfg.MaxConnIdleTime != 5*time.Minute || cfg.HealthCheckPeriod != 15*time.Second {
				t.Fatalf("lifetimes = %s %s %s", cfg.MaxConnLifetime, cfg.MaxConnIdleTime, cfg.HealthCheckPeriod)
			}
		})
	}
	t.Run("zero keeps pgx default", func(t *testing.T) {
		plain, _ := pgxpool.ParseConfig("postgres://u@h/db")
		cfg, _ := pgxpool.ParseConfig("postgres://u@h/db")
		db.Harden(cfg, 0)
		if cfg.MaxConns != plain.MaxConns {
			t.Fatalf("MaxConns = %d, want default %d", cfg.MaxConns, plain.MaxConns)
		}
	})
	t.Run("session params override url", func(t *testing.T) {
		cfg, err := pgxpool.ParseConfig("postgres://u@h/db?statement_timeout=0&lock_timeout=0&synchronous_commit=off&application_name=evil&idle_in_transaction_session_timeout=0&search_path=keep&work_mem=64MB")
		if err != nil {
			t.Fatal(err)
		}
		db.Harden(cfg, 0)
		want := map[string]string{
			"synchronous_commit":                  "on",
			"lock_timeout":                        "10s",
			"statement_timeout":                   "30s",
			"idle_in_transaction_session_timeout": "30s",
			"application_name":                    "astrum",
			"search_path":                         "keep",
			"work_mem":                            "64MB",
		}
		for k, v := range want {
			if got := cfg.ConnConfig.RuntimeParams[k]; got != v {
				t.Errorf("%s = %q, want %q", k, got, v)
			}
		}
	})
	t.Run("idempotent", func(t *testing.T) {
		cfg, _ := pgxpool.ParseConfig("postgres://u@h/db")
		db.Harden(cfg, 5)
		db.Harden(cfg, 0)
		if cfg.MaxConns != 5 || cfg.ConnConfig.RuntimeParams["lock_timeout"] != "10s" {
			t.Fatalf("second Harden changed config: %d %v", cfg.MaxConns, cfg.ConnConfig.RuntimeParams)
		}
	})
}

type fakeRow struct {
	value string
	err   error
}

func (r fakeRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	*dest[0].(*string) = r.value
	return nil
}

type fakeSettings map[string]fakeRow

func (f fakeSettings) QueryRow(_ context.Context, _ string, args ...any) pgx.Row {
	if row, ok := f[args[0].(string)]; ok {
		return row
	}
	return fakeRow{value: "on"}
}

func TestEdgeCheckDurabilityFake(t *testing.T) {
	tests := []struct {
		name     string
		settings fakeSettings
		want     string
	}{
		{"all on", fakeSettings{}, ""},
		{"fsync off", fakeSettings{"fsync": {value: "off"}}, "db: unsafe durability: fsync = off, want on"},
		{"full page writes off", fakeSettings{"full_page_writes": {value: "off"}}, "db: unsafe durability: full_page_writes = off, want on"},
		{"async commit", fakeSettings{"synchronous_commit": {value: "off"}}, "db: unsafe durability: synchronous_commit = off, want on"},
		{"local commit", fakeSettings{"synchronous_commit": {value: "local"}}, "synchronous_commit = local"},
		{"uppercase", fakeSettings{"fsync": {value: "ON"}}, "fsync = ON"},
		{"read failure", fakeSettings{"fsync": {err: errors.New("permission denied")}}, "db: read fsync: permission denied"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := db.CheckDurability(context.Background(), tt.settings)
			if tt.want == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("CheckDurability() = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestEdgeConnectAppliesSessionParams(t *testing.T) {
	url := testdb.URL(t) + "&statement_timeout=0&lock_timeout=0&application_name=evil&synchronous_commit=off&options=-c%20synchronous_commit%3Doff"
	ctx := context.Background()
	pool, err := db.Connect(ctx, url, db.Options{MaxConns: 3})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if pool.Config().MaxConns != 3 {
		t.Fatalf("MaxConns = %d", pool.Config().MaxConns)
	}
	want := map[string]string{
		"synchronous_commit":                  "on",
		"lock_timeout":                        "10s",
		"statement_timeout":                   "30s",
		"idle_in_transaction_session_timeout": "30s",
		"application_name":                    "astrum",
	}
	if err := db.CheckDurability(ctx, pool); err != nil {
		t.Fatalf("CheckDurability() on hardened pool = %v", err)
	}
	conns := make([]*pgxpool.Conn, 3)
	for i := range conns {
		if conns[i], err = pool.Acquire(ctx); err != nil {
			t.Fatal(err)
		}
	}
	defer func() {
		for _, c := range conns {
			c.Release()
		}
	}()
	pids := map[uint32]bool{}
	for i, c := range conns {
		pids[c.Conn().PgConn().PID()] = true
		for name, v := range want {
			var got string
			if err := c.QueryRow(ctx, "SELECT current_setting($1)", name).Scan(&got); err != nil {
				t.Fatal(err)
			}
			if got != v {
				t.Errorf("conn %d %s = %s, want %s", i, name, got, v)
			}
		}
	}
	if len(pids) != 3 {
		t.Fatalf("acquired %d distinct backends, want 3", len(pids))
	}
}

func TestEdgeStatementTimeoutEnforced(t *testing.T) {
	pool, err := db.Connect(context.Background(), testdb.URL(t), db.Options{MaxConns: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	ctx := context.Background()
	err = pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SET LOCAL statement_timeout = '50ms'"); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, "SELECT pg_sleep(2)")
		return err
	})
	if db.Code(err) != "57014" {
		t.Fatalf("err = %v, want query_canceled 57014", err)
	}
}

func edgeVersions(t *testing.T, pool *pgxpool.Pool, module string) []string {
	t.Helper()
	rows, err := pool.Query(context.Background(), `SELECT version FROM schema_migrations WHERE module = $1 ORDER BY version`, module)
	if err != nil {
		t.Fatal(err)
	}
	versions, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	return versions
}

func edgeOrder(t *testing.T, pool *pgxpool.Pool) []string {
	t.Helper()
	rows, err := pool.Query(context.Background(), `SELECT v FROM applied ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	out, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestEdgeMigrateEmpty(t *testing.T) {
	pool := testdb.New(t, nil)
	ctx := context.Background()
	for range 2 {
		if err := db.Migrate(ctx, pool, testdb.Logger(), "empty", fstest.MapFS{}); err != nil {
			t.Fatal(err)
		}
	}
	var hasChecksum bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = 'schema_migrations' AND column_name = 'checksum')`).Scan(&hasChecksum); err != nil {
		t.Fatal(err)
	}
	if !hasChecksum || len(edgeVersions(t, pool, "empty")) != 0 {
		t.Fatalf("checksum column = %v, versions = %v", hasChecksum, edgeVersions(t, pool, "empty"))
	}
}

func TestEdgeMigrateFileSelection(t *testing.T) {
	pool := testdb.New(t, nil)
	migrations := fstest.MapFS{
		"0000_init.sql":        {Data: []byte(`CREATE TABLE applied (id serial PRIMARY KEY, v text NOT NULL)`)},
		"9_late.sql":           {Data: []byte(`INSERT INTO applied (v) VALUES ('9_late')`)},
		"10_early.sql":         {Data: []byte(`INSERT INTO applied (v) VALUES ('10_early')`)},
		"0002_b.sql":           {Data: []byte(`INSERT INTO applied (v) VALUES ('0002_b')`)},
		"0002_a.sql":           {Data: []byte(`INSERT INTO applied (v) VALUES ('0002_a')`)},
		"0003_upper.SQL":       {Data: []byte(`SELECT broken syntax here`)},
		"0004_notes.sql.bak":   {Data: []byte(`SELECT broken syntax here`)},
		"nested/0001_deep.sql": {Data: []byte(`SELECT broken syntax here`)},
		"README.md":            {Data: []byte(`# migrations`)},
	}
	if err := db.Migrate(context.Background(), pool, testdb.Logger(), "order", migrations); err != nil {
		t.Fatal(err)
	}
	got := strings.Join(edgeOrder(t, pool), ",")
	if want := "0002_a,0002_b,10_early,9_late"; got != want {
		t.Fatalf("applied order = %s, want lexical %s", got, want)
	}
	versions := strings.Join(edgeVersions(t, pool, "order"), ",")
	if want := "0000_init,0002_a,0002_b,10_early,9_late"; versions != want {
		t.Fatalf("recorded versions = %s, want %s", versions, want)
	}
}

func TestEdgeMigrateEvolves(t *testing.T) {
	pool := testdb.New(t, nil)
	ctx := context.Background()
	m := fstest.MapFS{
		"0001_init.sql": {Data: []byte(`CREATE TABLE applied (id serial PRIMARY KEY, v text NOT NULL)`)},
		"0005_five.sql": {Data: []byte(`INSERT INTO applied (v) VALUES ('0005')`)},
	}
	if err := db.Migrate(ctx, pool, testdb.Logger(), "evolve", m); err != nil {
		t.Fatal(err)
	}
	m["0006_six.sql"] = &fstest.MapFile{Data: []byte(`INSERT INTO applied (v) VALUES ('0006')`)}
	m["0003_backfilled.sql"] = &fstest.MapFile{Data: []byte(`INSERT INTO applied (v) VALUES ('0003')`)}
	if err := db.Migrate(ctx, pool, testdb.Logger(), "evolve", m); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(edgeOrder(t, pool), ","); got != "0005,0003,0006" {
		t.Fatalf("applied order = %s; a lower version added later is applied on the next run", got)
	}
	delete(m, "0005_five.sql")
	if err := db.Migrate(ctx, pool, testdb.Logger(), "evolve", m); err != nil {
		t.Fatalf("Migrate() with an applied file removed = %v", err)
	}
	if got := strings.Join(edgeVersions(t, pool, "evolve"), ","); got != "0001_init,0003_backfilled,0005_five,0006_six" {
		t.Fatalf("versions = %s", got)
	}
	if got := len(edgeOrder(t, pool)); got != 3 {
		t.Fatalf("rows = %d, rerun applied something twice", got)
	}
}

func TestEdgeMigrateChecksums(t *testing.T) {
	pool := testdb.New(t, nil)
	ctx := context.Background()
	body := []byte(`CREATE TABLE sums (id int)`)
	sum := sha256.Sum256(body)
	want := hex.EncodeToString(sum[:])
	m := fstest.MapFS{"0001_init.sql": {Data: body}}
	if err := db.Migrate(ctx, pool, testdb.Logger(), "sums", m); err != nil {
		t.Fatal(err)
	}
	stored := func() *string {
		var s *string
		if err := pool.QueryRow(ctx, `SELECT checksum FROM schema_migrations WHERE module = 'sums'`).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	if s := stored(); s == nil || *s != want {
		t.Fatalf("stored checksum = %v, want %s", s, want)
	}

	t.Run("legacy null checksum is backfilled", func(t *testing.T) {
		if _, err := pool.Exec(ctx, `UPDATE schema_migrations SET checksum = NULL WHERE module = 'sums'`); err != nil {
			t.Fatal(err)
		}
		if err := db.Migrate(ctx, pool, testdb.Logger(), "sums", m); err != nil {
			t.Fatal(err)
		}
		if s := stored(); s == nil || *s != want {
			t.Fatalf("backfilled checksum = %v, want %s", s, want)
		}
	})

	edits := []struct {
		name string
		data string
	}{
		{"trailing newline", "CREATE TABLE sums (id int)\n"},
		{"case change", "create table sums (id int)"},
		{"comment added", "-- note\nCREATE TABLE sums (id int)"},
		{"emptied", ""},
	}
	for _, e := range edits {
		t.Run("rejects "+e.name, func(t *testing.T) {
			edited := fstest.MapFS{"0001_init.sql": {Data: []byte(e.data)}}
			err := db.Migrate(ctx, pool, testdb.Logger(), "sums", edited)
			newSum := sha256.Sum256([]byte(e.data))
			if err == nil || !strings.HasPrefix(err.Error(), "migrate sums/0001_init: applied migration was modified") ||
				!strings.Contains(err.Error(), want) || !strings.Contains(err.Error(), hex.EncodeToString(newSum[:])) {
				t.Fatalf("Migrate() = %v", err)
			}
		})
	}

	t.Run("same file in another module is independent", func(t *testing.T) {
		other := fstest.MapFS{"0001_init.sql": {Data: []byte(`CREATE TABLE other_sums (id int)`)}}
		if err := db.Migrate(ctx, pool, testdb.Logger(), "other", other); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("modified migration stops later ones", func(t *testing.T) {
		edited := fstest.MapFS{
			"0001_init.sql": {Data: []byte("CREATE TABLE sums (id bigint)")},
			"0002_next.sql": {Data: []byte("CREATE TABLE after_edit (id int)")},
		}
		if err := db.Migrate(ctx, pool, testdb.Logger(), "sums", edited); err == nil {
			t.Fatal("Migrate() accepted a modified migration")
		}
		var exists bool
		if err := pool.QueryRow(ctx, `SELECT to_regclass('after_edit') IS NOT NULL`).Scan(&exists); err != nil {
			t.Fatal(err)
		}
		if exists {
			t.Fatal("later migration ran after a checksum mismatch")
		}
	})
}

func TestEdgeMigrateResumesAfterFailure(t *testing.T) {
	pool := testdb.New(t, nil)
	ctx := context.Background()
	m := fstest.MapFS{
		"0001_init.sql":  {Data: []byte(`CREATE TABLE applied (id serial PRIMARY KEY, v text NOT NULL)`)},
		"0002_bad.sql":   {Data: []byte(`INSERT INTO applied (v) VALUES ('0002'); INSERT INTO nowhere VALUES (1)`)},
		"0003_later.sql": {Data: []byte(`INSERT INTO applied (v) VALUES ('0003')`)},
	}
	err := db.Migrate(ctx, pool, testdb.Logger(), "resume", m)
	if err == nil || !strings.HasPrefix(err.Error(), "migrate resume/0002_bad: ") || db.Code(err) != "42P01" {
		t.Fatalf("Migrate() = %v, want wrapped undefined_table", err)
	}
	if got := strings.Join(edgeVersions(t, pool, "resume"), ","); got != "0001_init" {
		t.Fatalf("versions after failure = %s", got)
	}
	m["0002_bad.sql"] = &fstest.MapFile{Data: []byte(`INSERT INTO applied (v) VALUES ('0002')`)}
	if err := db.Migrate(ctx, pool, testdb.Logger(), "resume", m); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(edgeOrder(t, pool), ","); got != "0002,0003" {
		t.Fatalf("applied = %s", got)
	}
}

func TestEdgeMigrateEmptyFile(t *testing.T) {
	pool := testdb.New(t, nil)
	m := fstest.MapFS{"0001_empty.sql": {Data: nil}, "0002_blank.sql": {Data: []byte("  \n-- nothing\n")}}
	for range 2 {
		if err := db.Migrate(context.Background(), pool, testdb.Logger(), "blank", m); err != nil {
			t.Fatalf("Migrate() = %v", err)
		}
	}
	if got := strings.Join(edgeVersions(t, pool, "blank"), ","); got != "0001_empty,0002_blank" {
		t.Fatalf("versions = %s", got)
	}
}

func TestEdgeMigrateConcurrent(t *testing.T) {
	pool := testdb.New(t, nil)
	ctx := context.Background()
	m := fstest.MapFS{
		"0001_init.sql": {Data: []byte(`CREATE TABLE once (id int NOT NULL)`)},
		"0002_seed.sql": {Data: []byte(`INSERT INTO once VALUES (1); SELECT pg_sleep(0.05)`)},
	}
	var wg sync.WaitGroup
	errs := make([]error, 8)
	for i := range errs {
		wg.Go(func() { errs[i] = db.Migrate(ctx, pool, testdb.Logger(), "concurrent", m) })
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("Migrate() #%d = %v", i, err)
		}
	}
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM once`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("seed rows = %d, want exactly 1 across concurrent migrators", n)
	}
}

func TestEdgeMigrateWaitsForLock(t *testing.T) {
	pool := testdb.New(t, nil)
	ctx := context.Background()
	holder, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Release()
	if _, err := holder.Exec(ctx, `SELECT pg_advisory_lock($1)`, edgeMigrationLock); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		done <- db.Migrate(ctx, pool, testdb.Logger(), "locked", fstest.MapFS{"0001.sql": {Data: []byte(`CREATE TABLE locked (id int)`)}})
	}()
	select {
	case err := <-done:
		t.Fatalf("Migrate() finished while the lock was held: %v", err)
	case <-time.After(300 * time.Millisecond):
	}
	if _, err := holder.Exec(ctx, `SELECT pg_advisory_unlock($1)`, edgeMigrationLock); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("Migrate() never acquired the released lock")
	}
}

func TestEdgeMigrateLockTimeoutWhileWaiting(t *testing.T) {
	pool := testdb.New(t, nil)
	ctx := context.Background()
	holder, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Release()
	if _, err := holder.Exec(ctx, `SELECT pg_advisory_lock($1)`, edgeMigrationLock); err != nil {
		t.Fatal(err)
	}
	defer holder.Exec(ctx, `SELECT pg_advisory_unlock($1)`, edgeMigrationLock)
	waitCtx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer cancel()
	err = db.Migrate(waitCtx, pool, testdb.Logger(), "gave_up", fstest.MapFS{})
	if err == nil || !strings.HasPrefix(err.Error(), "migrate gave_up: lock: ") {
		t.Fatalf("Migrate() = %v, want lock error", err)
	}
}

func TestEdgeMigrateReleasesLockAfterFailure(t *testing.T) {
	pool := testdb.New(t, nil)
	ctx := context.Background()
	bad := fstest.MapFS{"0001_bad.sql": {Data: []byte(`SELECT * FROM missing`)}}
	if err := db.Migrate(ctx, pool, testdb.Logger(), "release", bad); err == nil {
		t.Fatal("broken migration succeeded")
	}
	conn, err := pgx.Connect(ctx, testdb.URL(t))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	deadline := time.Now().Add(5 * time.Second)
	for {
		var got bool
		if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, edgeMigrationLock).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got {
			conn.Exec(ctx, `SELECT pg_advisory_unlock($1)`, edgeMigrationLock)
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("migration lock still held after a failed Migrate")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestEdgeMigrateCanceled(t *testing.T) {
	pool := testdb.New(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := db.Migrate(ctx, pool, testdb.Logger(), "canceled", fstest.MapFS{"0001.sql": {Data: []byte(`SELECT 1`)}})
	if err == nil || !errors.Is(err, context.Canceled) || !strings.HasPrefix(err.Error(), "migrate canceled: acquire: ") {
		t.Fatalf("Migrate() = %v", err)
	}
}

func TestEdgeRetryableNil(t *testing.T) {
	if db.Retryable(nil) || db.Code(nil) != "" {
		t.Fatal("nil error must not be retryable")
	}
	for _, code := range []string{"40001", "40P01"} {
		if !db.Retryable(fmt.Errorf("a: %w", fmt.Errorf("b: %w", &pgconn.PgError{Code: code}))) {
			t.Fatalf("doubly wrapped %s not retryable", code)
		}
	}
	for _, code := range []string{"40002", "40003", "55P03", "57014", "53300", "08006"} {
		if db.Retryable(&pgconn.PgError{Code: code}) {
			t.Fatalf("%s retried", code)
		}
	}
	if db.Retryable(errors.Join(errors.New("x"), db.ErrRetry)) != true {
		t.Fatal("joined ErrRetry not retryable")
	}
}

func TestEdgeRunTxRetryCodes(t *testing.T) {
	pool := testdb.New(t, nil)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `CREATE TABLE tx_edge (n int NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	for _, code := range []string{"40001", "40P01"} {
		t.Run(code, func(t *testing.T) {
			if _, err := pool.Exec(ctx, `TRUNCATE tx_edge`); err != nil {
				t.Fatal(err)
			}
			attempts := 0
			err := db.RunTx(ctx, pool, func(tx pgx.Tx) error {
				attempts++
				if _, err := tx.Exec(ctx, `INSERT INTO tx_edge VALUES ($1)`, attempts); err != nil {
					return err
				}
				if attempts < 5 {
					return &pgconn.PgError{Code: code}
				}
				return nil
			})
			if err != nil || attempts != 5 {
				t.Fatalf("RunTx() = %v after %d attempts", err, attempts)
			}
			var rows []int
			r, err := pool.Query(ctx, `SELECT n FROM tx_edge`)
			if err != nil {
				t.Fatal(err)
			}
			if rows, err = pgx.CollectRows(r, pgx.RowTo[int]); err != nil {
				t.Fatal(err)
			}
			if len(rows) != 1 || rows[0] != 5 {
				t.Fatalf("rows = %v, want only the successful attempt committed", rows)
			}
		})
	}
}

func TestEdgeRunTxReturnsLastError(t *testing.T) {
	pool := testdb.New(t, nil)
	attempts := 0
	start := time.Now()
	err := db.RunTx(context.Background(), pool, func(pgx.Tx) error {
		attempts++
		return &pgconn.PgError{Code: "40001", Message: fmt.Sprintf("attempt %d", attempts)}
	})
	if attempts != 6 || db.Code(err) != "40001" || !strings.Contains(err.Error(), "attempt 6") {
		t.Fatalf("RunTx() = %v after %d attempts, want the sixth error", err, attempts)
	}
	if elapsed := time.Since(start); elapsed < 75*time.Millisecond {
		t.Fatalf("six attempts took %s; backoff between attempts is missing", elapsed)
	}
}

func TestEdgeRunTxCanceledDuringBackoff(t *testing.T) {
	pool := testdb.New(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	attempts := 0
	var canceledAt time.Time
	err := db.RunTx(ctx, pool, func(pgx.Tx) error {
		attempts++
		if attempts == 3 {
			canceledAt = time.Now()
			cancel()
		}
		return db.ErrRetry
	})
	if !errors.Is(err, context.Canceled) || attempts != 3 {
		t.Fatalf("RunTx() = %v after %d attempts, want context.Canceled after 3", err, attempts)
	}
	if time.Since(canceledAt) > time.Second {
		t.Fatal("backoff did not stop promptly on cancellation")
	}
}

func TestEdgeRunTxDeadlineExceeded(t *testing.T) {
	pool := testdb.New(t, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	attempts := 0
	err := db.RunTx(ctx, pool, func(pgx.Tx) error {
		attempts++
		return db.ErrRetry
	})
	if !errors.Is(err, context.DeadlineExceeded) || attempts >= 6 {
		t.Fatalf("RunTx() = %v after %d attempts, want DeadlineExceeded before exhausting attempts", err, attempts)
	}
}

func TestEdgeRunTxPreCanceled(t *testing.T) {
	pool := testdb.New(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	called := false
	err := db.RunTx(ctx, pool, func(pgx.Tx) error {
		called = true
		return nil
	})
	if !errors.Is(err, context.Canceled) || called {
		t.Fatalf("RunTx() = %v, fn called = %v", err, called)
	}
}

func TestEdgeRunTxCommitFailureNotRetried(t *testing.T) {
	pool := testdb.New(t, nil)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `CREATE TABLE deferred_edge (id int, CONSTRAINT deferred_edge_id UNIQUE (id) DEFERRABLE INITIALLY DEFERRED)`); err != nil {
		t.Fatal(err)
	}
	attempts := 0
	err := db.RunTx(ctx, pool, func(tx pgx.Tx) error {
		attempts++
		_, err := tx.Exec(ctx, `INSERT INTO deferred_edge VALUES (1), (1)`)
		return err
	})
	if db.Code(err) != "23505" || db.Constraint(err) != "deferred_edge_id" || attempts != 1 {
		t.Fatalf("RunTx() = %v after %d attempts, want commit-time unique violation once", err, attempts)
	}
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM deferred_edge`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("rows = %d, %v", n, err)
	}
}

func TestEdgeRunTxRealSerializationConflict(t *testing.T) {
	pool := testdb.New(t, nil)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `CREATE TABLE ser_edge (id int PRIMARY KEY, n int NOT NULL); INSERT INTO ser_edge VALUES (1, 0)`); err != nil {
		t.Fatal(err)
	}
	const workers = 6
	var wg sync.WaitGroup
	errs := make([]error, workers)
	for i := range workers {
		wg.Go(func() {
			errs[i] = db.RunTx(ctx, pool, func(tx pgx.Tx) error {
				if _, err := tx.Exec(ctx, `SET TRANSACTION ISOLATION LEVEL REPEATABLE READ`); err != nil {
					return err
				}
				var n int
				if err := tx.QueryRow(ctx, `SELECT n FROM ser_edge WHERE id = 1`).Scan(&n); err != nil {
					return err
				}
				_, err := tx.Exec(ctx, `UPDATE ser_edge SET n = $1 WHERE id = 1`, n+1)
				return err
			})
		})
	}
	wg.Wait()
	succeeded := 0
	for _, err := range errs {
		switch {
		case err == nil:
			succeeded++
		case db.Code(err) != "40001":
			t.Fatalf("unexpected error: %v", err)
		}
	}
	var n int
	if err := pool.QueryRow(ctx, `SELECT n FROM ser_edge WHERE id = 1`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != succeeded || succeeded == 0 {
		t.Fatalf("counter = %d, successful transactions = %d; lost update", n, succeeded)
	}
}
