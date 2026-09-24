package db_test

import (
	"context"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/pandabase/astrum/internal/kernel/db"
	"github.com/pandabase/astrum/internal/kernel/testdb"
)

func TestMigrate(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := testdb.New(t, nil)
	logger := testdb.Logger()

	migrations := fstest.MapFS{
		"0002_seed.sql": {Data: []byte(`INSERT INTO widgets (name) VALUES ('first')`)},
		"0001_init.sql": {Data: []byte(`CREATE TABLE widgets (name text NOT NULL)`)},
		"README.md":     {Data: []byte(`not a migration`)},
	}

	for range 2 {
		if err := db.Migrate(ctx, pool, logger, "widgets", migrations); err != nil {
			t.Fatalf("Migrate() error = %v", err)
		}
	}

	var widgets int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM widgets`).Scan(&widgets); err != nil {
		t.Fatal(err)
	}
	if widgets != 1 {
		t.Fatalf("seed applied %d times, want exactly once", widgets)
	}

	var versions []string
	rows, err := pool.Query(ctx, `SELECT version FROM schema_migrations WHERE module = 'widgets' ORDER BY version`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			t.Fatal(err)
		}
		versions = append(versions, v)
	}
	if len(versions) != 2 || versions[0] != "0001_init" || versions[1] != "0002_seed" {
		t.Fatalf("recorded versions = %v", versions)
	}
}

func TestMigrateFailureRollsBack(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := testdb.New(t, nil)

	migrations := fstest.MapFS{
		"0001_broken.sql": {Data: []byte(`CREATE TABLE half (id int); SELECT * FROM missing_table;`)},
	}
	if err := db.Migrate(ctx, pool, testdb.Logger(), "broken", migrations); err == nil {
		t.Fatal("Migrate() error = nil, want failure")
	}

	var exists bool
	if err := pool.QueryRow(ctx, `SELECT to_regclass('half') IS NOT NULL`).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	if exists {
		t.Fatal("partial migration was not rolled back")
	}

	var recorded int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM schema_migrations WHERE module = 'broken'`).Scan(&recorded); err != nil {
		t.Fatal(err)
	}
	if recorded != 0 {
		t.Fatal("failed migration was recorded as applied")
	}
}

func TestMigrateModulesAreIndependent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := testdb.New(t, nil)
	logger := testdb.Logger()

	for _, module := range []string{"alpha", "beta"} {
		migrations := fstest.MapFS{
			"0001_init.sql": {Data: []byte(`CREATE TABLE ` + module + `_things (id int)`)},
		}
		if err := db.Migrate(ctx, pool, logger, module, migrations); err != nil {
			t.Fatalf("Migrate(%s) error = %v", module, err)
		}
	}

	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM schema_migrations`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("schema_migrations rows = %d, want 2", count)
	}
}

func TestMigrateRejectsModifiedMigration(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := testdb.New(t, nil)

	original := fstest.MapFS{"0001_init.sql": {Data: []byte(`CREATE TABLE gadgets (name text)`)}}
	if err := db.Migrate(ctx, pool, testdb.Logger(), "gadgets", original); err != nil {
		t.Fatal(err)
	}
	edited := fstest.MapFS{"0001_init.sql": {Data: []byte(`CREATE TABLE gadgets (name text, price bigint)`)}}
	err := db.Migrate(ctx, pool, testdb.Logger(), "gadgets", edited)
	if err == nil || !strings.Contains(err.Error(), "was modified") {
		t.Fatalf("Migrate() error = %v, want modified migration rejected", err)
	}
}
