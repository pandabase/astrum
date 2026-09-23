package ledger

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pandabase/astrum/internal/kernel/db"
	"github.com/pandabase/astrum/internal/kernel/logger"
)

const (
	maxBulkItems = 10_000
	bulkLease    = time.Minute
)

func (s *service) createBulk(ctx context.Context, in CreateBulkInput) (BulkRequest, error) {
	l := logger.For(ctx, s.log).With("op", "create_bulk_request", "idempotency_key", in.IdempotencyKey, "items", len(in.Transactions))
	start := time.Now()

	for i := range in.Transactions {
		in.Transactions[i].IdempotencyKey = fmt.Sprintf("%s/%d", in.IdempotencyKey, i)
	}
	if err := validateBulk(in); err != nil {
		return BulkRequest{}, s.fail(l, "create bulk request", err, start)
	}
	requests := make([]string, len(in.Transactions))
	indexes := make([]int, len(in.Transactions))
	h := sha256.New()
	for i, t := range in.Transactions {
		raw, err := json.Marshal(t)
		if err != nil {
			return BulkRequest{}, s.fail(l, "create bulk request", err, start)
		}
		requests[i], indexes[i] = string(raw), i
		h.Write(raw)
		h.Write([]byte{'\n'})
	}
	hash := h.Sum(nil)

	var (
		bulk     BulkRequest
		replayed bool
	)
	err := db.RunTx(ctx, s.pool, func(tx pgx.Tx) error {
		id, err := uuid.NewV7()
		if err != nil {
			return err
		}
		bulk, err = scanBulk(tx.QueryRow(ctx, `
			INSERT INTO ledger_bulk_requests (id, idempotency_key, request_hash, total)
			VALUES ($1, $2, $3, $4)
			ON CONFLICT (idempotency_key) DO NOTHING
			RETURNING `+bulkColumns, id, in.IdempotencyKey, hash, len(requests)))
		if errors.Is(err, pgx.ErrNoRows) {
			var existing []byte
			if err := tx.QueryRow(ctx, `SELECT request_hash FROM ledger_bulk_requests WHERE idempotency_key = $1`,
				in.IdempotencyKey).Scan(&existing); err != nil {
				return err
			}
			if !bytes.Equal(existing, hash) {
				return ErrIdempotencyConflict
			}
			replayed = true
			bulk, err = scanBulk(tx.QueryRow(ctx, `SELECT `+bulkColumns+` FROM ledger_bulk_requests WHERE idempotency_key = $1`, in.IdempotencyKey))
			return err
		}
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `
			INSERT INTO ledger_bulk_items (bulk_id, index, request)
			SELECT $1, i, r::jsonb FROM unnest($2::int[], $3::text[]) AS x(i, r)`, bulk.ID, indexes, requests)
		return err
	})
	if err != nil {
		return BulkRequest{}, s.fail(l, "create bulk request", err, start)
	}
	if replayed {
		l.Info("bulk request replayed", "bulk_id", bulk.ID, "duration", time.Since(start))
		return bulk, nil
	}
	l.Info("bulk request accepted", "bulk_id", bulk.ID, "duration", time.Since(start))
	return bulk, nil
}

func (s *service) bulk(ctx context.Context, id uuid.UUID) (BulkRequest, error) {
	l := logger.For(ctx, s.log).With("op", "get_bulk_request", "bulk_id", id)
	start := time.Now()

	b, err := scanBulk(s.pool.QueryRow(ctx, `SELECT `+bulkColumns+` FROM ledger_bulk_requests WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		err = ErrNotFound
	}
	if err != nil {
		return BulkRequest{}, s.fail(l, "get bulk request", err, start)
	}
	return b, nil
}

func (s *service) bulkResults(ctx context.Context, id uuid.UUID, after, limit int) ([]BulkResult, error) {
	l := logger.For(ctx, s.log).With("op", "bulk_results", "bulk_id", id)
	start := time.Now()

	if _, err := s.bulk(ctx, id); err != nil {
		return nil, err
	}
	if limit < 1 || limit > maxListLimit {
		return nil, s.fail(l, "bulk results", fmt.Errorf("%w: limit must be 1-%d", ErrInvalid, maxListLimit), start)
	}
	rows, err := s.pool.Query(ctx, `
		SELECT index, transaction_id, error_code, error_detail
		FROM ledger_bulk_items
		WHERE bulk_id = $1 AND index > $2
		ORDER BY index
		LIMIT $3`, id, after, limit)
	if err != nil {
		return nil, s.fail(l, "bulk results", err, start)
	}
	results, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (BulkResult, error) {
		var r BulkResult
		err := row.Scan(&r.Index, &r.TransactionID, &r.ErrorCode, &r.ErrorDetail)
		return r, err
	})
	if err != nil {
		return nil, s.fail(l, "bulk results", err, start)
	}
	return results, nil
}

func (s *service) processBulk(ctx context.Context, chunk int) (bool, error) {
	var (
		id               uuid.UUID
		processed, total int
	)
	err := s.pool.QueryRow(ctx, `
		UPDATE ledger_bulk_requests
		SET status = 'processing', lease_until = now() + make_interval(secs => $1), started_at = coalesce(started_at, now())
		WHERE id = (
			SELECT id FROM ledger_bulk_requests
			WHERE status = 'pending' OR (status = 'processing' AND lease_until < now())
			ORDER BY created_at
			LIMIT 1
			FOR UPDATE SKIP LOCKED)
		RETURNING id, processed, total`, bulkLease.Seconds()).Scan(&id, &processed, &total)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	l := s.log.With("op", "process_bulk_request", "bulk_id", id)
	start := time.Now()
	l.Info("bulk request started", "processed", processed, "total", total)

	for processed < total {
		next, err := s.processChunk(ctx, id, processed, chunk)
		if err != nil {
			l.Warn("bulk request paused", "processed", processed, "err", err)
			return true, err
		}
		if next < 0 {
			l.Warn("bulk request taken over by another worker", "processed", processed)
			return true, nil
		}
		processed = next
	}
	l.Info("bulk request completed", "total", total, "duration", time.Since(start))
	return true, nil
}

func (s *service) processChunk(ctx context.Context, id uuid.UUID, processed, chunk int) (int, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT index, request FROM ledger_bulk_items
		WHERE bulk_id = $1 AND index >= $2
		ORDER BY index
		LIMIT $3`, id, processed, chunk)
	if err != nil {
		return 0, err
	}
	var (
		indexes []int
		inputs  []PostInput
		index   int
		raw     []byte
	)
	if _, err := pgx.ForEachRow(rows, []any{&index, &raw}, func() error {
		var in PostInput
		if err := json.Unmarshal(raw, &in); err != nil {
			return fmt.Errorf("decode bulk item %d: %w", index, err)
		}
		indexes, inputs = append(indexes, index), append(inputs, in)
		return nil
	}); err != nil {
		return 0, err
	}

	results, err := s.postBatch(ctx, inputs, false)
	if err != nil {
		return 0, err
	}
	var (
		txnIDs            = make([]*uuid.UUID, len(results))
		codes, details    = make([]*string, len(results)), make([]*string, len(results))
		succeeded, failed int
	)
	for i, r := range results {
		if r.Err != nil {
			_, code, detail := problemFor(r.Err)
			codes[i], details[i] = &code, &detail
			failed++
			continue
		}
		txnIDs[i] = &r.Transaction.ID
		succeeded++
	}

	next := processed + len(results)
	err = db.RunTx(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE ledger_bulk_requests
			SET processed = $3, succeeded = succeeded + $4, failed = failed + $5,
				lease_until = now() + make_interval(secs => $6),
				status = CASE WHEN $3 = total THEN 'completed' ELSE status END,
				completed_at = CASE WHEN $3 = total THEN now() END
			WHERE id = $1 AND processed = $2`, id, processed, next, succeeded, failed, bulkLease.Seconds())
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			next = -1
			return nil
		}
		if _, err := tx.Exec(ctx, `
			UPDATE ledger_bulk_items AS b
			SET transaction_id = r.txn, error_code = r.code, error_detail = r.detail
			FROM unnest($2::int[], $3::uuid[], $4::text[], $5::text[]) AS r(index, txn, code, detail)
			WHERE b.bulk_id = $1 AND b.index = r.index`, id, indexes, txnIDs, codes, details); err != nil {
			return err
		}
		b, err := scanBulk(tx.QueryRow(ctx, `SELECT `+bulkColumns+` FROM ledger_bulk_requests WHERE id = $1`, id))
		if err != nil || b.Status != BulkCompleted {
			return err
		}
		return emit(ctx, tx, eventBulkCompleted, toBulk, b)
	})
	return next, err
}

const bulkColumns = `id, idempotency_key, status, total, processed, succeeded, failed, created_at, started_at, completed_at`

func scanBulk(row pgx.Row) (BulkRequest, error) {
	var (
		b      BulkRequest
		status string
	)
	err := row.Scan(&b.ID, &b.IdempotencyKey, &status, &b.Total, &b.Processed, &b.Succeeded, &b.Failed,
		&b.CreatedAt, &b.StartedAt, &b.CompletedAt)
	b.Status = BulkStatus(status)
	return b, err
}

func validateBulk(in CreateBulkInput) error {
	if len(in.Transactions) == 0 || len(in.Transactions) > maxBulkItems {
		return fmt.Errorf("%w: a bulk request needs 1-%d transactions", ErrInvalid, maxBulkItems)
	}
	if err := validateKeyAndText(in.IdempotencyKey, "", nil); err != nil {
		return err
	}
	for i, t := range in.Transactions {
		if err := validatePost(t); err != nil {
			return fmt.Errorf("transaction %d: %w", i, err)
		}
	}
	return nil
}
