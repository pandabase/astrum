package db

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/charmbracelet/log"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const migrationLockID = 7_341_902_118

func Migrate(ctx context.Context, pool *pgxpool.Pool, logger *log.Logger, module string, migrations fs.FS) error {
	logger = logger.WithPrefix("migrate").With("module", module)
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("migrate %s: acquire: %w", module, err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock(hashtextextended(current_schema(), $1))`, migrationLockID); err != nil {
		return fmt.Errorf("migrate %s: lock: %w", module, err)
	}
	defer conn.Exec(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock(hashtextextended(current_schema(), $1))`, migrationLockID)

	if _, err := conn.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			module     text        NOT NULL,
			version    text        NOT NULL,
			applied_at timestamptz NOT NULL DEFAULT now(),
			PRIMARY KEY (module, version)
		);
		ALTER TABLE schema_migrations ADD COLUMN IF NOT EXISTS checksum text`); err != nil {
		return fmt.Errorf("migrate %s: create table: %w", module, err)
	}

	files, err := fs.Glob(migrations, "*.sql")
	if err != nil {
		return fmt.Errorf("migrate %s: list: %w", module, err)
	}
	slices.Sort(files)
	logger.Debug("found migrations", "count", len(files))

	applied := 0
	for _, file := range files {
		version := strings.TrimSuffix(path.Base(file), ".sql")
		sql, err := fs.ReadFile(migrations, file)
		if err != nil {
			return fmt.Errorf("migrate %s/%s: read: %w", module, version, err)
		}
		start := time.Now()
		ran := false
		sum := sha256.Sum256(sql)
		checksum := hex.EncodeToString(sum[:])
		err = pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
			tag, err := tx.Exec(ctx,
				`INSERT INTO schema_migrations (module, version, checksum) VALUES ($1, $2, $3) ON CONFLICT DO NOTHING`,
				module, version, checksum)
			if err != nil {
				return err
			}
			if tag.RowsAffected() == 0 {
				return verifyChecksum(ctx, tx, module, version, checksum)
			}
			ran = true
			_, err = tx.Exec(ctx, string(sql))
			return err
		})
		if err != nil {
			logger.Error("migration failed", "version", version, "err", err)
			return fmt.Errorf("migrate %s/%s: %w", module, version, err)
		}
		if !ran {
			logger.Debug("migration already applied", "version", version)
			continue
		}
		applied++
		logger.Info("migration applied", "version", version, "duration", time.Since(start))
	}
	logger.Info("migrations up to date", "applied", applied, "total", len(files))
	return nil
}

func verifyChecksum(ctx context.Context, tx pgx.Tx, module, version, checksum string) error {
	var stored *string
	if err := tx.QueryRow(ctx,
		`SELECT checksum FROM schema_migrations WHERE module = $1 AND version = $2`, module, version).Scan(&stored); err != nil {
		return err
	}
	if stored == nil {
		_, err := tx.Exec(ctx,
			`UPDATE schema_migrations SET checksum = $3 WHERE module = $1 AND version = $2`, module, version, checksum)
		return err
	}
	if *stored != checksum {
		return fmt.Errorf("applied migration was modified (checksum %s, file %s); add a new migration instead", *stored, checksum)
	}
	return nil
}
