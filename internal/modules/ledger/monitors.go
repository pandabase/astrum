package ledger

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pandabase/astrum/internal/kernel/db"
	"github.com/pandabase/astrum/internal/kernel/logger"
)

func (s *service) createMonitor(ctx context.Context, in CreateBalanceMonitorInput) (BalanceMonitor, error) {
	l := logger.For(ctx, s.log).With("op", "create_balance_monitor", "account_id", in.AccountID)
	start := time.Now()

	if err := validateMonitor(in); err != nil {
		return BalanceMonitor{}, s.fail(l, "create balance monitor", err, start)
	}
	id, err := uuid.NewV7()
	if err != nil {
		return BalanceMonitor{}, s.fail(l, "create balance monitor", err, start)
	}
	var m BalanceMonitor
	err = db.RunTx(ctx, s.pool, func(tx pgx.Tx) error {
		acc, err := selectAccount(ctx, tx, in.AccountID)
		if err != nil {
			return err
		}
		if m, err = scanMonitor(tx.QueryRow(ctx, `
			INSERT INTO ledger_balance_monitors (id, account_id, field, operator, value, description, metadata)
			VALUES ($1, $2, $3, $4, $5, $6, $7::text::jsonb)
			RETURNING `+monitorColumns,
			id, in.AccountID, in.Condition.Field, in.Condition.Operator, in.Condition.Value, in.Description,
			string(normalizeMetadata(in.Metadata)))); err != nil {
			return err
		}
		m.Triggered = m.Condition.holds(accountBalances(acc))
		return nil
	})
	if err != nil {
		return BalanceMonitor{}, s.fail(l, "create balance monitor", err, start)
	}
	l.Info("balance monitor created", "monitor_id", m.ID, "field", m.Condition.Field, "operator", m.Condition.Operator,
		"value", m.Condition.Value, "duration", time.Since(start))
	return m, nil
}

func (s *service) monitor(ctx context.Context, id uuid.UUID) (BalanceMonitor, error) {
	l := logger.For(ctx, s.log).With("op", "get_balance_monitor", "monitor_id", id)
	start := time.Now()

	monitors, err := s.readMonitors(ctx, `m.id = $1`, id)
	if err == nil && len(monitors) == 0 {
		err = ErrNotFound
	}
	if err != nil {
		return BalanceMonitor{}, s.fail(l, "get balance monitor", err, start)
	}
	return monitors[0], nil
}

func (s *service) listMonitors(ctx context.Context, in ListBalanceMonitorsInput) ([]BalanceMonitor, error) {
	l := logger.For(ctx, s.log).With("op", "list_balance_monitors", "account_id", in.AccountID)
	start := time.Now()

	if in.Limit < 1 || in.Limit > maxListLimit {
		return nil, s.fail(l, "list balance monitors", fmt.Errorf("%w: limit must be 1-%d", ErrInvalid, maxListLimit), start)
	}
	monitors, err := s.readMonitors(ctx, `($1::uuid IS NULL OR m.account_id = $1) AND ($2::uuid IS NULL OR m.id < $2)
		ORDER BY m.id DESC LIMIT $3`, nullUUID(in.AccountID), nullUUID(in.Before), in.Limit)
	if err != nil {
		return nil, s.fail(l, "list balance monitors", err, start)
	}
	return monitors, nil
}

func (s *service) readMonitors(ctx context.Context, where string, args ...any) ([]BalanceMonitor, error) {
	var monitors []BalanceMonitor
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT `+monitorColumns+` FROM ledger_balance_monitors AS m WHERE `+where, args...)
		if err != nil {
			return err
		}
		if monitors, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (BalanceMonitor, error) { return scanMonitor(row) }); err != nil {
			return err
		}
		for i := range monitors {
			acc, err := selectAccount(ctx, tx, monitors[i].AccountID)
			if err != nil {
				return err
			}
			monitors[i].Triggered = monitors[i].Condition.holds(accountBalances(acc))
		}
		return nil
	})
	return monitors, err
}

func (s *service) updateMonitor(ctx context.Context, id uuid.UUID, in UpdateInput) (BalanceMonitor, error) {
	l := logger.For(ctx, s.log).With("op", "update_balance_monitor", "monitor_id", id)
	start := time.Now()

	if in.Name != nil {
		return BalanceMonitor{}, s.fail(l, "update balance monitor", fmt.Errorf("%w: balance monitors have no name", ErrInvalid), start)
	}
	err := db.RunTx(ctx, s.pool, func(tx pgx.Tx) error {
		current, err := scanMonitor(tx.QueryRow(ctx, `SELECT `+monitorColumns+` FROM ledger_balance_monitors WHERE id = $1 FOR UPDATE`, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		_, description, metadata, err := applyUpdate("", current.Description, current.Metadata, in, false)
		if err != nil || sameDetails("", current.Description, current.Metadata, "", description, metadata) {
			return err
		}
		_, err = tx.Exec(ctx, `
			UPDATE ledger_balance_monitors SET description = $2, metadata = $3::text::jsonb, version = version + 1
			WHERE id = $1`, id, description, string(metadata))
		return err
	})
	if err != nil {
		return BalanceMonitor{}, s.fail(l, "update balance monitor", err, start)
	}
	l.Info("balance monitor updated", "duration", time.Since(start))
	return s.monitor(ctx, id)
}

func (s *service) deleteMonitor(ctx context.Context, id uuid.UUID) error {
	l := logger.For(ctx, s.log).With("op", "delete_balance_monitor", "monitor_id", id)
	start := time.Now()

	tag, err := s.pool.Exec(ctx, `DELETE FROM ledger_balance_monitors WHERE id = $1`, id)
	if err == nil && tag.RowsAffected() == 0 {
		err = ErrNotFound
	}
	if err != nil {
		return s.fail(l, "delete balance monitor", err, start)
	}
	l.Info("balance monitor deleted", "duration", time.Since(start))
	return nil
}

const monitorColumns = `id, account_id, field, operator, value, description, metadata, version, created_at`

func scanMonitor(row pgx.Row) (BalanceMonitor, error) {
	var (
		m        BalanceMonitor
		metadata []byte
	)
	err := row.Scan(&m.ID, &m.AccountID, &m.Condition.Field, &m.Condition.Operator, &m.Condition.Value,
		&m.Description, &metadata, &m.Version, &m.CreatedAt)
	m.Metadata = bytes.Clone(metadata)
	return m, err
}

func accountBalances(a Account) Balances {
	return Balances{Pending: a.Pending, Posted: a.Posted, Available: a.Available}
}

func validateMonitor(in CreateBalanceMonitorInput) error {
	switch {
	case in.AccountID == uuid.Nil:
		return fmt.Errorf("%w: account_id is required", ErrInvalid)
	case in.Condition.Field != "pending" && in.Condition.Field != "posted" && in.Condition.Field != "available":
		return fmt.Errorf("%w: alert_condition.field must be pending_balance_amount, posted_balance_amount or available_balance_amount", ErrInvalid)
	}
	switch in.Condition.Operator {
	case "gt", "gte", "eq", "lt", "lte", "not_eq":
	default:
		return fmt.Errorf("%w: alert_condition.operator must be gt, gte, eq, lt, lte or not_eq", ErrInvalid)
	}
	return validateText(in.Description, in.Metadata)
}
