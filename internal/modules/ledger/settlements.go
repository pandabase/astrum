package ledger

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pandabase/astrum/internal/kernel/db"
	"github.com/pandabase/astrum/internal/kernel/typeid"
	"github.com/pandabase/astrum/internal/money"
)

const constraintSettlementKey = "ledger_settlements_idempotency_key_key"

func (s *service) createSettlement(ctx context.Context, in CreateSettlementInput) (Settlement, error) {
	op := s.begin(ctx, "create settlement", "idempotency_key", in.IdempotencyKey,
		"settled_account_id", in.SettledAccountID, "contra_account_id", in.ContraAccountID)

	if err := validateSettlement(in); err != nil {
		return Settlement{}, op.fail(err)
	}
	if in.UpperBound != nil {
		bound := in.UpperBound.Truncate(time.Microsecond)
		in.UpperBound = &bound
	}

	var (
		st       Settlement
		replayed bool
	)
	err := db.RunTx(ctx, s.pool, func(tx pgx.Tx) error {
		replayed = false
		existing, err := scanSettlement(tx.QueryRow(ctx, `SELECT `+settlementColumns+` FROM ledger_settlements WHERE idempotency_key = $1`, in.IdempotencyKey))
		switch {
		case err == nil:
			if !existing.matches(in) {
				return ErrIdempotencyConflict
			}
			st, replayed = existing, true
			return nil
		case !errors.Is(err, pgx.ErrNoRows):
			return err
		}

		state, err := lockAccounts(ctx, tx, []uuid.UUID{in.SettledAccountID, in.ContraAccountID})
		if err != nil {
			return err
		}
		settled, ok := state.accounts[in.SettledAccountID]
		if !ok {
			return fmt.Errorf("%w: settled account %s", ErrNotFound, in.SettledAccountID)
		}
		contra, ok := state.accounts[in.ContraAccountID]
		if !ok {
			return fmt.Errorf("%w: contra account %s", ErrNotFound, in.ContraAccountID)
		}
		if settled.ledgerID != contra.ledgerID || settled.currency != contra.currency {
			return fmt.Errorf("%w: settled and contra accounts must share a ledger and currency", ErrInvalid)
		}

		debits, credits, count, err := sumUnsettled(ctx, tx, in.SettledAccountID, in.UpperBound)
		if err != nil {
			return err
		}
		net, err := (&accountState{normalSide: settled.normalSide}).balance(debits, credits)
		if err != nil {
			return err
		}

		id, err := uuid.NewV7()
		if err != nil {
			return err
		}
		st = Settlement{
			ID:               id,
			IdempotencyKey:   in.IdempotencyKey,
			LedgerID:         settled.ledgerID,
			SettledAccountID: in.SettledAccountID,
			ContraAccountID:  in.ContraAccountID,
			Currency:         settled.currency,
			UpperBound:       in.UpperBound,
			Amount:           net.Amount,
			EntryCount:       count,
			Description:      in.Description,
			Metadata:         normalizeMetadata(in.Metadata),
		}

		if !net.Amount.IsZero() {
			txn, err := s.postSettlement(ctx, tx, st, settled.normalSide)
			if err != nil {
				return err
			}
			st.TransactionID = &txn.ID
		}

		st, err = insertSettlement(ctx, tx, st)
		if err != nil {
			if db.Constraint(err) == constraintSettlementKey {
				return fmt.Errorf("%w: %w", db.ErrRetry, err)
			}
			return err
		}

		marked, err := markSettled(ctx, tx, st)
		if err != nil {
			return err
		}
		own := 0
		if st.TransactionID != nil {
			own = 1
		}
		if marked != int64(count+own) {
			return fmt.Errorf("ledger: settlement marked %d entries, summed %d", marked, count+own)
		}
		return emit(ctx, tx, eventSettlementCreated, toSettlement, st)
	})
	if err != nil {
		return Settlement{}, op.fail(err)
	}
	if replayed {
		op.info("settlement replayed", "settlement_id", st.ID)
		return st, nil
	}
	op.info("settlement created", "settlement_id", st.ID, "amount", st.Amount, "entries", st.EntryCount,
		"transaction_id", st.TransactionID)
	return st, nil
}

func (s *service) postSettlement(ctx context.Context, tx pgx.Tx, st Settlement, settledSide Side) (Transaction, error) {

	side, amount := settledSide.opposite(), st.Amount
	if amount.Sign() < 0 {
		side, amount = settledSide, amount.Neg()
	}
	metadata, err := json.Marshal(map[string]string{"settlement_id": typeid.Encode("stl", st.ID)})
	if err != nil {
		return Transaction{}, err
	}
	results, err := applyRequests(ctx, tx, []*postingRequest{{in: PostInput{

		IdempotencyKey: typeid.Encode("stl", st.ID),
		Description:    st.Description,
		Metadata:       metadata,
		Postings: []Posting{
			{AccountID: st.SettledAccountID, Side: side, Amount: amount},
			{AccountID: st.ContraAccountID, Side: side.opposite(), Amount: amount},
		},
	}}}, true)
	if err != nil && !errors.Is(err, errAborted) {
		return Transaction{}, err
	}
	if results[0].err != nil {
		return Transaction{}, results[0].err
	}
	return results[0].txn, nil
}

func (s *service) settlement(ctx context.Context, id uuid.UUID) (Settlement, error) {
	op := s.begin(ctx, "get settlement", "settlement_id", id)

	st, err := scanSettlement(s.pool.QueryRow(ctx, `SELECT `+settlementColumns+` FROM ledger_settlements WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		err = ErrNotFound
	}
	if err != nil {
		return Settlement{}, op.fail(err)
	}
	return st, nil
}

func (s *service) listSettlements(ctx context.Context, in ListSettlementsInput) ([]Settlement, error) {
	op := s.begin(ctx, "list settlements", "account_id", in.AccountID)

	if in.Limit < 1 || in.Limit > maxListLimit {
		return nil, op.fail(fmt.Errorf("%w: limit must be 1-%d", ErrInvalid, maxListLimit))
	}
	rows, err := s.pool.Query(ctx, `
		SELECT `+settlementColumns+` FROM ledger_settlements
		WHERE ($1::uuid IS NULL OR settled_account_id = $1) AND ($2::uuid IS NULL OR id < $2)
		ORDER BY id DESC
		LIMIT $3`, nullUUID(in.AccountID), nullUUID(in.Before), in.Limit)
	if err != nil {
		return nil, op.fail(err)
	}
	settlements, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Settlement, error) { return scanSettlement(row) })
	if err != nil {
		return nil, op.fail(err)
	}
	return settlements, nil
}

const settlementColumns = `id, idempotency_key, ledger_id, settled_account_id, contra_account_id, currency,
	effective_at_upper_bound, amount, entry_count, transaction_id, description, metadata, created_at`

func scanSettlement(row pgx.Row) (Settlement, error) {
	var (
		st       Settlement
		currency string
		metadata []byte
	)
	err := row.Scan(&st.ID, &st.IdempotencyKey, &st.LedgerID, &st.SettledAccountID, &st.ContraAccountID, &currency,
		&st.UpperBound, &st.Amount, &st.EntryCount, &st.TransactionID, &st.Description, &metadata, &st.CreatedAt)
	st.Currency, st.Metadata = money.Currency(currency), bytes.Clone(metadata)
	return st, err
}

func (st Settlement) matches(in CreateSettlementInput) bool {
	sameBound := (st.UpperBound == nil && in.UpperBound == nil) ||
		(st.UpperBound != nil && in.UpperBound != nil && st.UpperBound.Equal(*in.UpperBound))
	return sameBound && st.SettledAccountID == in.SettledAccountID && st.ContraAccountID == in.ContraAccountID &&
		st.Description == in.Description && jsonEqual(st.Metadata, in.Metadata)
}

func validateSettlement(in CreateSettlementInput) error {
	if err := validateKeyAndText(in.IdempotencyKey, in.Description, in.Metadata); err != nil {
		return err
	}
	switch {
	case in.SettledAccountID == uuid.Nil || in.ContraAccountID == uuid.Nil:
		return fmt.Errorf("%w: settled_account_id and contra_account_id are required", ErrInvalid)
	case in.SettledAccountID == in.ContraAccountID:
		return fmt.Errorf("%w: an account cannot settle into itself", ErrInvalid)
	}
	return nil
}
