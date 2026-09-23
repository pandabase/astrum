package ledger

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pandabase/astrum/internal/kernel/db"
	"github.com/pandabase/astrum/internal/kernel/events"
	"github.com/pandabase/astrum/internal/kernel/logger"
	"github.com/pandabase/astrum/internal/money"
)

func (s *service) updateTransaction(ctx context.Context, id uuid.UUID, in UpdateTransactionInput) (Transaction, error) {
	l := logger.For(ctx, s.log).With("op", "update_transaction", "transaction_id", id)
	start := time.Now()

	if err := validateUpdateTransaction(in); err != nil {
		return Transaction{}, s.fail(l, "update transaction", err, start)
	}

	var (
		txn     Transaction
		changed bool
	)
	err := s.changePending(ctx, id, func(current Transaction) (Transaction, *change, error) {
		next := current
		var err error
		if next.Description, next.Metadata, err = applyTransactionUpdate(current, in); err != nil {
			return Transaction{}, nil, err
		}
		if in.EffectiveAt != nil {
			next.EffectiveAt = in.EffectiveAt.Truncate(time.Microsecond)
		}
		entriesChanged := in.Postings != nil && !sameContent(PostInput{Postings: current.Postings}, PostInput{Postings: in.Postings})
		changed = entriesChanged || next.Description != current.Description ||
			!jsonEqual(next.Metadata, current.Metadata) || !next.EffectiveAt.Equal(current.EffectiveAt)
		if !changed {
			return current, nil, nil
		}
		if !entriesChanged {
			return next, &change{}, nil
		}
		next.Postings = in.Postings
		next.entriesVersion = current.Version + 1
		return next, &change{ledgerID: current.LedgerID, unpend: current.Postings, add: in.Postings, status: TransactionPending}, nil
	}, &txn, nil)
	if err != nil {
		return Transaction{}, s.fail(l, "update transaction", err, start)
	}
	l.Info("transaction updated", "changed", changed, "version", txn.Version, "duration", time.Since(start))
	return txn, nil
}

func (s *service) postPending(ctx context.Context, id uuid.UUID, in PostPendingInput) (Transaction, error) {
	l := logger.For(ctx, s.log).With("op", "post_transaction", "transaction_id", id, "partial", len(in.Postings) > 0)
	start := time.Now()

	if err := validatePartialEntries(in.Postings); err != nil {
		return Transaction{}, s.fail(l, "post transaction", err, start)
	}

	var txn Transaction
	err := s.changePending(ctx, id, func(current Transaction) (Transaction, *change, error) {
		posted := current.Postings
		if len(in.Postings) > 0 {
			if err := checkPartial(current.Postings, in.Postings); err != nil {
				return Transaction{}, nil, err
			}
			posted = in.Postings
		}
		next := current
		next.Status = TransactionPosted
		next.Postings = posted
		return next, &change{ledgerID: current.LedgerID, unpend: current.Postings, add: posted, status: TransactionPosted}, nil
	}, &txn, func(current Transaction) bool {
		return current.Status == TransactionPosted &&
			(len(in.Postings) == 0 || sameContent(PostInput{Postings: current.Postings}, PostInput{Postings: in.Postings}))
	})
	if err != nil {
		return Transaction{}, s.fail(l, "post transaction", err, start)
	}
	l.Info("transaction posted", "version", txn.Version, "entries", len(txn.Postings), "duration", time.Since(start))
	return txn, nil
}

func (s *service) archiveTransaction(ctx context.Context, id uuid.UUID) (Transaction, error) {
	l := logger.For(ctx, s.log).With("op", "archive_transaction", "transaction_id", id)
	start := time.Now()

	var txn Transaction
	err := s.changePending(ctx, id, func(current Transaction) (Transaction, *change, error) {
		next := current
		next.Status = TransactionArchived
		return next, &change{ledgerID: current.LedgerID, unpend: current.Postings}, nil
	}, &txn, func(current Transaction) bool { return current.Status == TransactionArchived })
	if err != nil {
		return Transaction{}, s.fail(l, "archive transaction", err, start)
	}
	l.Info("transaction archived", "version", txn.Version, "duration", time.Since(start))
	return txn, nil
}

func (s *service) changePending(
	ctx context.Context,
	id uuid.UUID,
	edit func(Transaction) (Transaction, *change, error),
	out *Transaction,
	done func(Transaction) bool,
) error {
	return db.RunTx(ctx, s.pool, func(tx pgx.Tx) error {
		current, err := lockTransaction(ctx, tx, id)
		if err != nil {
			return err
		}
		if current.Status != TransactionPending {
			if done != nil && done(current) {
				*out = current
				return nil
			}
			return fmt.Errorf("%w: transaction %s is %s", ErrNotPending, id, current.Status)
		}

		next, c, err := edit(current)
		if err != nil {
			return err
		}
		if c == nil {
			*out = current
			return nil
		}

		b := &pgx.Batch{}
		next.Version = current.Version + 1
		if len(c.unpend)+len(c.add) > 0 {
			ids := make([]uuid.UUID, 0, len(c.unpend)+len(c.add))
			for _, p := range c.unpend {
				ids = append(ids, p.AccountID)
			}
			for _, p := range c.add {
				ids = append(ids, p.AccountID)
			}
			state, err := lockAccounts(ctx, tx, ids)
			if err != nil {
				return err
			}
			if _, next.Postings, err = state.transition(*c); err != nil {
				return err
			}
			if err := queueWriteAccounts(b, state); err != nil {
				return err
			}
		}

		queueUpdateTransaction(b, next)
		var entries entryColumns
		entries.add(next.ID, next.Postings)
		switch {
		case next.Status == TransactionPosted:
			queueInsertPostings(b, entries)
		case next.entriesVersion == next.Version:
			queueInsertPendingEntries(b, entries, next.Version)
		}
		if err := tx.SendBatch(ctx, b).Close(); err != nil {
			return err
		}
		if *out, err = selectTransaction(ctx, tx, id); err != nil {
			return err
		}

		if len(out.Postings) == len(next.Postings) && len(c.add) > 0 {
			for i := range out.Postings {
				out.Postings[i].Resulting = next.Postings[i].Resulting
			}
		}
		eventType := map[TransactionStatus]string{
			TransactionPending:  eventTransactionUpdated,
			TransactionPosted:   eventTransactionPosted,
			TransactionArchived: eventTransactionArchived,
		}[out.Status]
		evs, err := transactionEvents(eventType, *out)
		if err != nil {
			return err
		}
		return events.Insert(ctx, tx, evs...)
	})
}

func checkPartial(pending, posted []Posting) error {
	type slot struct {
		account uuid.UUID
		side    Side
	}
	limits := make(map[slot]money.Amount)
	for _, p := range pending {
		k := slot{p.AccountID, p.Side}
		total, err := limits[k].Add(p.Amount)
		if err != nil {
			return err
		}
		limits[k] = total
	}
	for i, p := range posted {
		k := slot{p.AccountID, p.Side}
		limit, ok := limits[k]
		if !ok {
			return fmt.Errorf("%w: entry %d %s on account %s was not pending", ErrInvalid, i, p.Side, p.AccountID)
		}
		remaining, err := limit.Sub(p.Amount)
		if err != nil {
			return err
		}
		if remaining.Sign() < 0 {
			return fmt.Errorf("%w: entry %d posts more than the %s pending on account %s", ErrInvalid, i, limit, p.AccountID)
		}
		limits[k] = remaining
	}
	return nil
}

func applyTransactionUpdate(current Transaction, in UpdateTransactionInput) (string, json.RawMessage, error) {
	_, description, metadata, err := applyUpdate("", current.Description, current.Metadata,
		UpdateInput{Description: in.Description, Metadata: in.Metadata}, false)
	return description, metadata, err
}
