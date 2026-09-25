package ledger

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pandabase/astrum/internal/kernel/db"
	"github.com/pandabase/astrum/internal/money"
)

func (s *service) createHold(ctx context.Context, in CreateHoldInput) (Hold, error) {
	op := s.begin(ctx, "create hold", "idempotency_key", in.IdempotencyKey, "account_id", in.AccountID)

	if err := validateHold(in); err != nil {
		return Hold{}, op.fail(err)
	}
	in.ExpiresAt = in.ExpiresAt.Truncate(time.Microsecond)

	var (
		hold     Hold
		replayed bool
	)
	err := db.RunTx(ctx, s.pool, func(tx pgx.Tx) error {
		replayed = false
		state, err := lockAccounts(ctx, tx, []uuid.UUID{in.AccountID})
		if err != nil {
			return err
		}
		existing, err := selectHold(ctx, tx, `idempotency_key = $1`, in.IdempotencyKey)
		switch {
		case err == nil:
			if !existing.matches(in) {
				return ErrIdempotencyConflict
			}
			hold, replayed = existing, true
			return nil
		case !errors.Is(err, ErrNotFound):
			return err
		}
		if err := state.reserve(in.AccountID, in.Currency, in.Amount); err != nil {
			return err
		}

		id, err := uuid.NewV7()
		if err != nil {
			return err
		}
		hold, err = insertHold(ctx, tx, Hold{
			ID:             id,
			IdempotencyKey: in.IdempotencyKey,
			AccountID:      in.AccountID,
			Currency:       state.accounts[in.AccountID].currency,
			Amount:         in.Amount,
			Description:    in.Description,
			ExpiresAt:      in.ExpiresAt,
		})
		if db.Constraint(err) == constraintHoldKey {
			return fmt.Errorf("%w: %w", db.ErrRetry, err)
		}
		if err != nil {
			return err
		}
		if err := writeAccounts(ctx, tx, state); err != nil {
			return err
		}
		return emit(ctx, tx, eventHoldCreated, toHold, hold)
	})
	if err != nil {
		return Hold{}, op.fail(err)
	}

	if replayed {
		op.info("hold replayed", "hold_id", hold.ID)
		return hold, nil
	}
	op.info("hold created", "hold_id", hold.ID, "amount", hold.Amount, "expires_at", hold.ExpiresAt)
	return hold, nil
}

func (s *service) hold(ctx context.Context, id uuid.UUID) (Hold, error) {
	op := s.begin(ctx, "get hold", "hold_id", id)

	h, err := selectHold(ctx, s.pool, `id = $1`, id)
	if err != nil {
		return Hold{}, op.fail(err)
	}
	return h, nil
}

func (s *service) captureHold(ctx context.Context, id uuid.UUID, in CaptureInput) (Hold, error) {
	op := s.begin(ctx, "capture hold", "hold_id", id, "idempotency_key", in.IdempotencyKey)

	if err := validateCapture(in); err != nil {
		return Hold{}, op.fail(err)
	}

	var (
		hold     Hold
		replayed bool
	)
	err := db.RunTx(ctx, s.pool, func(tx pgx.Tx) error {
		replayed = false
		h, err := selectHold(ctx, tx, `id = $1 FOR UPDATE`, id)
		if err != nil {
			return err
		}

		normalSide, err := selectNormalSide(ctx, tx, h.AccountID)
		if err != nil {
			return err
		}
		side := normalSide.opposite()
		post := PostInput{
			IdempotencyKey: in.IdempotencyKey,
			Description:    in.Description,
			Metadata:       in.Metadata,
			Postings: []Posting{
				{AccountID: h.AccountID, Side: side, Amount: in.Amount, Currency: h.Currency},
				{AccountID: in.Destination, Side: side.opposite(), Amount: in.Amount, Currency: h.Currency},
			},
		}

		if h.Status == HoldCaptured {
			txn, err := selectTransaction(ctx, tx, *h.CaptureTransactionID)
			if err != nil {
				return err
			}
			if txn.IdempotencyKey != in.IdempotencyKey {
				return fmt.Errorf("%w: hold is %s", ErrHoldNotPending, h.Status)
			}
			if *h.CapturedAmount != in.Amount || !txn.matches(post) {
				return fmt.Errorf("%w: hold was captured with different terms", ErrIdempotencyConflict)
			}
			hold, replayed = h, true
			return nil
		}
		if h.Status != HoldPending {
			return fmt.Errorf("%w: hold is %s", ErrHoldNotPending, h.Status)
		}
		if in.Amount.Cmp(h.Amount) > 0 {
			return fmt.Errorf("%w: capture of %s exceeds hold of %s", ErrInvalid, in.Amount, h.Amount)
		}

		now, err := selectClock(ctx, tx)
		if err != nil {
			return err
		}
		if !now.Before(h.ExpiresAt) {
			return fmt.Errorf("%w: hold expired at %s", ErrHoldNotPending, h.ExpiresAt)
		}

		results, err := applyRequests(ctx, tx, []*postingRequest{{
			in:       post,
			releases: map[uuid.UUID]money.Amount{h.AccountID: h.Amount},
		}}, true)
		if err != nil && !errors.Is(err, errAborted) {
			return err
		}
		o := results[0]
		if o.err != nil {
			return o.err
		}
		if o.replayed {
			return ErrIdempotencyConflict
		}

		if hold, err = resolveHold(ctx, tx, h.ID, HoldCaptured, &in.Amount, &o.txn.ID); err != nil {
			return err
		}
		return emit(ctx, tx, eventHoldCaptured, toHold, hold)
	})
	if err != nil {
		return Hold{}, op.fail(err)
	}

	if replayed {
		op.info("capture replayed", "hold_id", hold.ID)
		return hold, nil
	}
	released, _ := hold.Amount.Sub(in.Amount)
	op.info("hold captured",
		"hold_id", hold.ID,
		"captured", in.Amount,
		"released", released,
		"transaction_id", hold.CaptureTransactionID)
	return hold, nil
}

func (s *service) voidHold(ctx context.Context, id uuid.UUID) (Hold, error) {
	op := s.begin(ctx, "void hold", "hold_id", id)

	var (
		hold     Hold
		replayed bool
	)
	err := db.RunTx(ctx, s.pool, func(tx pgx.Tx) error {
		replayed = false
		h, err := selectHold(ctx, tx, `id = $1 FOR UPDATE`, id)
		if err != nil {
			return err
		}
		switch h.Status {
		case HoldVoided:
			hold, replayed = h, true
			return nil
		case HoldPending:
		default:
			return fmt.Errorf("%w: hold is %s", ErrHoldNotPending, h.Status)
		}

		state, err := lockAccounts(ctx, tx, []uuid.UUID{h.AccountID})
		if err != nil {
			return err
		}
		if err := state.release(h.AccountID, h.Amount); err != nil {
			return err
		}
		if err := writeAccounts(ctx, tx, state); err != nil {
			return err
		}
		if hold, err = resolveHold(ctx, tx, h.ID, HoldVoided, nil, nil); err != nil {
			return err
		}
		return emit(ctx, tx, eventHoldVoided, toHold, hold)
	})
	if err != nil {
		return Hold{}, op.fail(err)
	}

	if replayed {
		op.info("void replayed", "hold_id", hold.ID)
		return hold, nil
	}
	op.info("hold voided", "hold_id", hold.ID, "released", hold.Amount)
	return hold, nil
}

func (s *service) expireHolds(ctx context.Context, limit int) (int, error) {
	var expired int
	err := db.RunTx(ctx, s.pool, func(tx pgx.Tx) error {
		expired = 0
		rows, err := tx.Query(ctx, `
			SELECT id, account_id, amount
			FROM ledger_holds
			WHERE status = 'pending' AND expires_at <= now()
			ORDER BY expires_at
			LIMIT $1
			FOR UPDATE SKIP LOCKED`, limit)
		if err != nil {
			return err
		}
		var (
			ids      []uuid.UUID
			accounts []uuid.UUID
			amounts  []money.Amount
			id       uuid.UUID
			account  uuid.UUID
			amount   money.Amount
		)
		_, err = pgx.ForEachRow(rows, []any{&id, &account, &amount}, func() error {
			ids = append(ids, id)
			accounts = append(accounts, account)
			amounts = append(amounts, amount)
			return nil
		})
		if err != nil || len(ids) == 0 {
			return err
		}

		state, err := lockAccounts(ctx, tx, accounts)
		if err != nil {
			return err
		}
		for i := range ids {
			if err := state.release(accounts[i], amounts[i]); err != nil {
				return err
			}
		}
		if err := writeAccounts(ctx, tx, state); err != nil {
			return err
		}
		rows, err = tx.Query(ctx, `
			UPDATE ledger_holds SET status = 'expired', resolved_at = now()
			WHERE id = ANY($1)
			RETURNING `+holdColumns, ids)
		if err != nil {
			return err
		}
		holds, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Hold, error) { return scanHold(row) })
		if err != nil {
			return err
		}
		expired = len(holds)
		return emit(ctx, tx, eventHoldExpired, toHold, holds...)
	})
	return expired, err
}

func (h Hold) matches(in CreateHoldInput) bool {
	return h.AccountID == in.AccountID &&
		h.Amount == in.Amount &&
		(in.Currency == "" || in.Currency == h.Currency) &&
		h.Description == in.Description &&
		h.ExpiresAt.Equal(in.ExpiresAt)
}

func (s *service) listHolds(ctx context.Context, in ListHoldsInput) ([]Hold, error) {
	op := s.begin(ctx, "list holds", "account_id", in.AccountID)

	switch {
	case in.Limit < 1 || in.Limit > maxListLimit:
		return nil, op.fail(fmt.Errorf("%w: limit must be 1-%d", ErrInvalid, maxListLimit))
	case !slices.Contains([]HoldStatus{"", HoldPending, HoldCaptured, HoldVoided, HoldExpired}, in.Status):
		return nil, op.fail(fmt.Errorf("%w: status must be pending, captured, voided or expired", ErrInvalid))
	}
	rows, err := s.pool.Query(ctx, `
		SELECT `+holdColumns+` FROM ledger_holds
		WHERE ($1::uuid IS NULL OR account_id = $1) AND ($2::text IS NULL OR status = $2) AND ($3::uuid IS NULL OR id < $3)
		ORDER BY id DESC
		LIMIT $4`, nullUUID(in.AccountID), nullString(string(in.Status)), nullUUID(in.Before), in.Limit)
	if err != nil {
		return nil, op.fail(err)
	}
	holds, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Hold, error) { return scanHold(row) })
	if err != nil {
		return nil, op.fail(err)
	}
	return holds, nil
}
