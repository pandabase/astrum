package ledger

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pandabase/astrum/internal/money"
)

const holdColumns = `id, idempotency_key, account_id, currency, amount, status, description,
	expires_at, created_at, resolved_at, captured_amount, capture_transaction_id`

func scanHold(row pgx.Row) (Hold, error) {
	var (
		h        Hold
		currency string
		status   string
	)
	err := row.Scan(&h.ID, &h.IdempotencyKey, &h.AccountID, &currency, &h.Amount, &status, &h.Description,
		&h.ExpiresAt, &h.CreatedAt, &h.ResolvedAt, &h.CapturedAmount, &h.CaptureTransactionID)
	if err != nil {
		return Hold{}, err
	}
	h.Currency = money.Currency(currency)
	h.Status = HoldStatus(status)
	return h, nil
}

func selectHold(ctx context.Context, q querier, where string, arg any) (Hold, error) {
	h, err := scanHold(q.QueryRow(ctx, `SELECT `+holdColumns+` FROM ledger_holds WHERE `+where, arg))
	if errors.Is(err, pgx.ErrNoRows) {
		return Hold{}, ErrNotFound
	}
	return h, err
}

func insertHold(ctx context.Context, tx pgx.Tx, h Hold) (Hold, error) {
	return scanHold(tx.QueryRow(ctx, `
		INSERT INTO ledger_holds (id, idempotency_key, account_id, currency, amount, description, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING `+holdColumns,
		h.ID, h.IdempotencyKey, h.AccountID, string(h.Currency), h.Amount, h.Description, h.ExpiresAt))
}

func resolveHold(ctx context.Context, tx pgx.Tx, id uuid.UUID, status HoldStatus, captured *money.Amount, txnID *uuid.UUID) (Hold, error) {
	return scanHold(tx.QueryRow(ctx, `
		UPDATE ledger_holds
		SET status = $2, resolved_at = now(), captured_amount = $3, capture_transaction_id = $4
		WHERE id = $1
		RETURNING `+holdColumns,
		id, string(status), captured, txnID))
}

const scheduleColumns = `id, idempotency_key, execute_at, request, status, transaction_id, failure,
	created_at, resolved_at`

func scanSchedule(row pgx.Row) (ScheduledTransaction, error) {
	var (
		s       ScheduledTransaction
		request []byte
		status  string
	)
	err := row.Scan(&s.ID, &s.IdempotencyKey, &s.ExecuteAt, &request, &status, &s.TransactionID, &s.Failure,
		&s.CreatedAt, &s.ResolvedAt)
	if err != nil {
		return ScheduledTransaction{}, err
	}
	if err := json.Unmarshal(request, &s.Request); err != nil {
		return ScheduledTransaction{}, fmt.Errorf("decode scheduled request %s: %w", s.ID, err)
	}
	s.Status = ScheduleStatus(status)
	return s, nil
}

func selectSchedule(ctx context.Context, q querier, where string, arg any) (ScheduledTransaction, error) {
	s, err := scanSchedule(q.QueryRow(ctx, `SELECT `+scheduleColumns+` FROM ledger_scheduled_transactions WHERE `+where, arg))
	if errors.Is(err, pgx.ErrNoRows) {
		return ScheduledTransaction{}, ErrNotFound
	}
	return s, err
}
