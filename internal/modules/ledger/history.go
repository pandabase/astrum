package ledger

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pandabase/astrum/internal/kernel/db"
)

const maxMetadataFilters = 20

func (s *service) listEntries(ctx context.Context, in ListEntriesInput) ([]Entry, error) {
	op := s.begin(ctx, "list entries", "limit", in.Limit)

	if in.Status == "" {
		in.Status = TransactionPosted
	}
	if err := validateListEntries(in); err != nil {
		return nil, op.fail(err)
	}

	var postedBefore uint64
	if in.StatementID != uuid.Nil {
		st, err := queryStatement(ctx, s.pool, in.StatementID)
		if err != nil {
			return nil, op.fail(err)
		}
		if in.Status != TransactionPosted || (in.AccountID != uuid.Nil && in.AccountID != st.AccountID) {
			return []Entry{}, nil
		}
		in.AccountID = st.AccountID
		in.Effective = narrow(in.Effective, EffectiveRange{From: &st.From, Until: &st.Until})
		postedBefore = st.postedBefore
	}

	entries, err := selectEntries(ctx, s.pool, in, postedBefore)
	if err != nil {
		return nil, op.fail(err)
	}
	op.debug("entries listed", "count", len(entries))
	return entries, nil
}

func narrow(a, b EffectiveRange) EffectiveRange {
	if b.From != nil && (a.From == nil || b.From.After(*a.From)) {
		a.From = b.From
	}
	if b.Until != nil && (a.Until == nil || b.Until.Before(*a.Until)) {
		a.Until = b.Until
	}
	return a
}

func (r EffectiveRange) truncated() EffectiveRange {
	if r.From != nil {
		from := r.From.Truncate(time.Microsecond)
		r.From = &from
	}
	if r.Until != nil {
		until := r.Until.Truncate(time.Microsecond)
		r.Until = &until
	}
	return r
}

func (s *service) balancesAt(ctx context.Context, id uuid.UUID, r EffectiveRange) (Balances, error) {
	op := s.begin(ctx, "account balances", "account_id", id)

	if err := validateRange(r); err != nil {
		return Balances{}, op.fail(err)
	}
	r = r.truncated()
	var b Balances
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		acc, err := selectAccount(ctx, tx, id)
		if err != nil {
			return err
		}
		t, err := selectTotals(ctx, tx, id, r, 0)
		if err != nil {
			return err
		}
		a := accountState{
			normalSide:     acc.NormalSide,
			postedDebits:   t.postedDebits,
			postedCredits:  t.postedCredits,
			pendingDebits:  t.pendingDebits,
			pendingCredits: t.pendingCredits,
		}
		b.Posted, b.Pending, b.Available, err = a.balances()
		return err
	})
	if err != nil {
		return Balances{}, op.fail(err)
	}
	return b, nil
}

func (s *service) createStatement(ctx context.Context, in CreateStatementInput) (Statement, error) {
	op := s.begin(ctx, "create statement", "account_id", in.AccountID)

	in.From, in.Until = in.From.Truncate(time.Microsecond), in.Until.Truncate(time.Microsecond)
	if err := validateStatement(in); err != nil {
		return Statement{}, op.fail(err)
	}

	var st Statement
	err := db.RunTx(ctx, s.pool, func(tx pgx.Tx) error {
		acc, err := selectAccount(ctx, tx, in.AccountID)
		if err != nil {
			return err
		}
		bound, err := selectSnapshotXmin(ctx, tx)
		if err != nil {
			return err
		}
		opening, err := selectTotals(ctx, tx, acc.ID, EffectiveRange{Until: &in.From}, bound)
		if err != nil {
			return err
		}
		period, err := selectTotals(ctx, tx, acc.ID, EffectiveRange{From: &in.From, Until: &in.Until}, bound)
		if err != nil {
			return err
		}
		endDebits, err := opening.postedDebits.Add(period.postedDebits)
		if err != nil {
			return err
		}
		endCredits, err := opening.postedCredits.Add(period.postedCredits)
		if err != nil {
			return err
		}
		id, err := uuid.NewV7()
		if err != nil {
			return err
		}
		st, err = insertStatement(ctx, tx, Statement{
			ID:           id,
			LedgerID:     acc.LedgerID,
			AccountID:    acc.ID,
			Description:  in.Description,
			From:         in.From,
			Until:        in.Until,
			Starting:     Balance{Debits: opening.postedDebits, Credits: opening.postedCredits},
			Ending:       Balance{Debits: endDebits, Credits: endCredits},
			EntryCount:   period.postedCount,
			postedBefore: bound,
		})
		return err
	})
	if err != nil {
		return Statement{}, op.fail(err)
	}
	op.info("statement created",
		"statement_id", st.ID,
		"entries", st.EntryCount,
		"ending", st.Ending.Amount)
	return st, nil
}

func (s *service) statement(ctx context.Context, id uuid.UUID) (Statement, error) {
	op := s.begin(ctx, "get statement", "statement_id", id)

	st, err := queryStatement(ctx, s.pool, id)
	if err != nil {
		return Statement{}, op.fail(err)
	}
	return st, nil
}

func (s *service) listStatements(ctx context.Context, in ListStatementsInput) ([]Statement, error) {
	op := s.begin(ctx, "list statements", "account_id", in.AccountID)

	if in.Limit < 1 || in.Limit > maxListLimit {
		return nil, op.fail(fmt.Errorf("%w: limit must be 1-%d", ErrInvalid, maxListLimit))
	}
	statements, err := selectStatements(ctx, s.pool, in)
	if err != nil {
		return nil, op.fail(err)
	}
	return statements, nil
}

func validateListEntries(in ListEntriesInput) error {
	switch {
	case in.Status != TransactionPending && in.Status != TransactionPosted && in.Status != TransactionArchived:
		return fmt.Errorf("%w: status must be pending, posted or archived", ErrInvalid)
	case in.Side != "" && !in.Side.valid():
		return fmt.Errorf("%w: side must be debit or credit", ErrInvalid)
	case in.Limit < 1 || in.Limit > maxListLimit:
		return fmt.Errorf("%w: limit must be 1-%d", ErrInvalid, maxListLimit)
	}
	if err := validateMetadataFilter(in.Metadata); err != nil {
		return err
	}
	return validateRange(in.Effective)
}

func validateRange(r EffectiveRange) error {
	if r.From != nil && r.Until != nil && !r.Until.After(*r.From) {
		return fmt.Errorf("%w: effective_at_upper_bound must be after effective_at_lower_bound", ErrInvalid)
	}
	return nil
}

func validateMetadataFilter(m map[string]string) error {
	if len(m) > maxMetadataFilters {
		return fmt.Errorf("%w: at most %d metadata filters", ErrInvalid, maxMetadataFilters)
	}
	for k, v := range m {
		if k == "" || !storableText(k) || !storableText(v) {
			return fmt.Errorf("%w: metadata filter keys must be non-empty and text must be valid UTF-8 without NUL", ErrInvalid)
		}
	}
	return nil
}

func validateStatement(in CreateStatementInput) error {
	switch {
	case in.AccountID == uuid.Nil:
		return fmt.Errorf("%w: account_id is required", ErrInvalid)
	case in.From.IsZero() || in.Until.IsZero():
		return fmt.Errorf("%w: effective_at_lower_bound and effective_at_upper_bound are required", ErrInvalid)
	}
	if err := validateRange(EffectiveRange{From: &in.From, Until: &in.Until}); err != nil {
		return err
	}
	return validateText(in.Description, nil)
}
