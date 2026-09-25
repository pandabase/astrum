package ledger

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pandabase/astrum/internal/kernel/db"
)

func (s *service) schedule(ctx context.Context, in ScheduleInput) (ScheduledTransaction, error) {
	op := s.begin(ctx, "schedule transaction", "idempotency_key", in.IdempotencyKey, "execute_at", in.ExecuteAt)

	if err := validateSchedule(in); err != nil {
		return ScheduledTransaction{}, op.fail(err)
	}
	in.ExecuteAt = in.ExecuteAt.Truncate(time.Microsecond)
	in.Metadata = normalizeMetadata(in.Metadata)

	request, err := json.Marshal(in.PostInput)
	if err != nil {
		return ScheduledTransaction{}, op.fail(err)
	}
	id, err := uuid.NewV7()
	if err != nil {
		return ScheduledTransaction{}, op.fail(err)
	}

	var (
		st       ScheduledTransaction
		replayed bool
	)
	err = db.RunTx(ctx, s.pool, func(tx pgx.Tx) error {
		replayed = false
		if err := checkAccountsExist(ctx, tx, in.Postings); err != nil {
			return err
		}

		inserted, insertErr := scanSchedule(tx.QueryRow(ctx, `
			INSERT INTO ledger_scheduled_transactions (id, idempotency_key, execute_at, request)
			VALUES ($1, $2, $3, $4)
			ON CONFLICT (idempotency_key) DO NOTHING
			RETURNING `+scheduleColumns,
			id, in.IdempotencyKey, in.ExecuteAt, request))
		if !errors.Is(insertErr, pgx.ErrNoRows) {
			st = inserted
			return insertErr
		}

		existing, selectErr := selectSchedule(ctx, tx, `idempotency_key = $1`, in.IdempotencyKey)
		if selectErr != nil {
			return selectErr
		}
		if !existing.matches(in) {
			return ErrIdempotencyConflict
		}
		st, replayed = existing, true
		return nil
	})
	if err != nil {
		return ScheduledTransaction{}, op.fail(err)
	}

	if replayed {
		op.info("schedule replayed", "schedule_id", st.ID)
		return st, nil
	}
	op.info("transaction scheduled", "schedule_id", st.ID)
	return st, nil
}

func (s *service) scheduled(ctx context.Context, id uuid.UUID) (ScheduledTransaction, error) {
	op := s.begin(ctx, "get schedule", "schedule_id", id)

	st, err := selectSchedule(ctx, s.pool, `id = $1`, id)
	if err != nil {
		return ScheduledTransaction{}, op.fail(err)
	}
	return st, nil
}

func (s *service) cancelSchedule(ctx context.Context, id uuid.UUID) (ScheduledTransaction, error) {
	op := s.begin(ctx, "cancel schedule", "schedule_id", id)

	var st ScheduledTransaction
	err := db.RunTx(ctx, s.pool, func(tx pgx.Tx) error {
		current, err := selectSchedule(ctx, tx, `id = $1 FOR UPDATE`, id)
		if err != nil {
			return err
		}
		switch current.Status {
		case ScheduleCanceled:
			st = current
			return nil
		case ScheduleScheduled:
		default:
			return fmt.Errorf("%w: schedule is %s", ErrScheduleNotPending, current.Status)
		}
		st, err = scanSchedule(tx.QueryRow(ctx, `
			UPDATE ledger_scheduled_transactions
			SET status = 'canceled', resolved_at = now()
			WHERE id = $1
			RETURNING `+scheduleColumns, id))
		return err
	})
	if err != nil {
		return ScheduledTransaction{}, op.fail(err)
	}
	op.info("schedule canceled", "schedule_id", st.ID)
	return st, nil
}

func (s *service) executeDue(ctx context.Context, limit int) (executed, failed int, err error) {
	err = db.RunTx(ctx, s.pool, func(tx pgx.Tx) error {
		executed, failed = 0, 0
		rows, err := tx.Query(ctx, `
			SELECT `+scheduleColumns+`
			FROM ledger_scheduled_transactions
			WHERE status = 'scheduled' AND execute_at <= now()
			ORDER BY execute_at, id
			LIMIT $1
			FOR UPDATE SKIP LOCKED`, limit)
		if err != nil {
			return err
		}
		due, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (ScheduledTransaction, error) {
			return scanSchedule(row)
		})
		if err != nil || len(due) == 0 {
			return err
		}

		reqs := make([]*postingRequest, len(due))
		for i, st := range due {
			in := st.Request
			in.IdempotencyKey = st.IdempotencyKey
			reqs[i] = &postingRequest{in: in}
		}
		results, err := applyRequests(ctx, tx, reqs, false)
		if err != nil {
			return err
		}

		var (
			ids      = make([]uuid.UUID, len(due))
			statuses = make([]string, len(due))
			txnIDs   = make([]uuid.NullUUID, len(due))
			failures = make([]*string, len(due))
		)
		for i, o := range results {
			ids[i] = due[i].ID
			if o.err != nil {
				msg := o.err.Error()
				statuses[i], failures[i] = string(ScheduleFailed), &msg
				failed++
				s.log.Warn("scheduled transaction failed", "schedule_id", due[i].ID, "err", o.err)
				continue
			}
			statuses[i] = string(ScheduleExecuted)
			txnIDs[i] = uuid.NullUUID{UUID: o.txn.ID, Valid: true}
			executed++
			s.log.Info("scheduled transaction executed",
				"schedule_id", due[i].ID, "transaction_id", o.txn.ID, "replayed", o.replayed)
		}

		_, err = tx.Exec(ctx, `
			UPDATE ledger_scheduled_transactions AS s
			SET status = u.status, transaction_id = u.transaction_id, failure = u.failure, resolved_at = now()
			FROM unnest($1::uuid[], $2::text[], $3::uuid[], $4::text[]) AS u(id, status, transaction_id, failure)
			WHERE s.id = u.id`,
			ids, statuses, txnIDs, failures)
		return err
	})
	return executed, failed, err
}

func checkAccountsExist(ctx context.Context, q querier, postings []Posting) error {
	ids := make([]uuid.UUID, len(postings))
	for i, p := range postings {
		ids[i] = p.AccountID
	}
	ids = sortedUnique(ids)

	var found int
	if err := q.QueryRow(ctx, `SELECT count(*) FROM ledger_accounts WHERE id = ANY($1)`, ids).Scan(&found); err != nil {
		return err
	}
	if found != len(ids) {
		return fmt.Errorf("%w: one or more accounts do not exist", ErrNotFound)
	}
	return nil
}

func (st ScheduledTransaction) matches(in ScheduleInput) bool {
	request := st.Request
	return st.ExecuteAt.Equal(in.ExecuteAt) && Transaction{request: &request}.matches(in.PostInput)
}

func (s *service) listSchedules(ctx context.Context, in ListSchedulesInput) ([]ScheduledTransaction, error) {
	op := s.begin(ctx, "list schedules", "status", in.Status)

	switch {
	case in.Limit < 1 || in.Limit > maxListLimit:
		return nil, op.fail(fmt.Errorf("%w: limit must be 1-%d", ErrInvalid, maxListLimit))
	case !slices.Contains([]ScheduleStatus{"", ScheduleScheduled, ScheduleExecuted, ScheduleFailed, ScheduleCanceled}, in.Status):
		return nil, op.fail(fmt.Errorf("%w: status must be scheduled, executed, failed or canceled", ErrInvalid))
	}
	rows, err := s.pool.Query(ctx, `
		SELECT `+scheduleColumns+` FROM ledger_scheduled_transactions
		WHERE ($1::text IS NULL OR status = $1) AND ($2::uuid IS NULL OR id < $2)
		ORDER BY id DESC
		LIMIT $3`, nullString(string(in.Status)), nullUUID(in.Before), in.Limit)
	if err != nil {
		return nil, op.fail(err)
	}
	schedules, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (ScheduledTransaction, error) { return scanSchedule(row) })
	if err != nil {
		return nil, op.fail(err)
	}
	return schedules, nil
}
