package ledger

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pandabase/astrum/internal/kernel/db"
	"github.com/pandabase/astrum/internal/kernel/events"
	"github.com/pandabase/astrum/internal/money"
)

var errAborted = errors.New("ledger: atomic batch aborted")

type entry struct {
	in       PostInput
	reverses *uuid.UUID
	releases map[uuid.UUID]money.Amount
}

type outcome struct {
	txn      Transaction
	replayed bool
	err      error
}

func applyEntries(ctx context.Context, tx pgx.Tx, entries []*entry, atomic bool) ([]outcome, error) {
	var (
		accountIDs  []uuid.UUID
		keys        = make([]string, 0, len(entries))
		externalIDs []string
	)
	for _, e := range entries {
		keys = append(keys, e.in.IdempotencyKey)
		if e.in.ExternalID != "" {
			externalIDs = append(externalIDs, e.in.ExternalID)
		}
		for _, p := range e.in.Postings {
			accountIDs = append(accountIDs, p.AccountID)
		}
		for id := range e.releases {
			accountIDs = append(accountIDs, id)
		}
	}

	var (
		locked   []*accountState
		monitors []BalanceMonitor
		existing = make(map[string]Transaction)
		taken    = make(map[externalKey]string)
		now      time.Time
	)
	read := &pgx.Batch{}
	queueLockAccounts(read, accountIDs, &locked)
	queueSelectMonitors(read, accountIDs, &monitors)
	queueSelectTransactionsByKey(read, keys, existing)
	queueSelectExternalIDs(read, externalIDs, taken)
	queueNow(read, &now)
	if err := tx.SendBatch(ctx, read).Close(); err != nil {
		return nil, fmt.Errorf("read batch: %w", err)
	}

	state := newLedgerState(locked, monitors)
	outcomes := make([]outcome, len(entries))
	firstByKey := make(map[string]int, len(entries))
	var accepted []Transaction

	for i, e := range entries {
		outcomes[i] = resolveEntry(e, i, state, existing, taken, firstByKey, outcomes, now)
		if outcomes[i].err != nil && atomic {
			return outcomes, errAborted
		}
		if outcomes[i].err == nil && !outcomes[i].replayed {
			accepted = append(accepted, outcomes[i].txn)
		}
	}

	if len(accepted) == 0 {
		return outcomes, nil
	}

	created, err := transactionEvents(eventTransactionCreated, accepted...)
	if err != nil {
		return nil, err
	}
	write := &pgx.Batch{}
	queueInsertEntries(write, accepted)
	events.Queue(write, created...)
	if err := queueWriteAccounts(write, state); err != nil {
		return nil, err
	}
	if err := tx.SendBatch(ctx, write).Close(); err != nil {
		if c := db.Constraint(err); c == constraintTransactionKey || c == constraintExternalID {

			return nil, fmt.Errorf("%w: %w", db.ErrRetry, err)
		}
		return nil, fmt.Errorf("write batch: %w", err)
	}
	return outcomes, nil
}

func resolveEntry(
	e *entry,
	i int,
	state *ledgerState,
	existing map[string]Transaction,
	taken map[externalKey]string,
	firstByKey map[string]int,
	outcomes []outcome,
	now time.Time,
) outcome {
	key := e.in.IdempotencyKey
	if e.in.EffectiveAt != nil {
		at := e.in.EffectiveAt.Truncate(time.Microsecond)
		e.in.EffectiveAt = &at
	}

	if txn, ok := existing[key]; ok {
		if !txn.matches(e.in) {
			return outcome{err: ErrIdempotencyConflict}
		}
		return outcome{txn: txn, replayed: true}
	}

	if j, ok := firstByKey[key]; ok {
		prior := outcomes[j]
		switch {
		case prior.err != nil:
			return outcome{err: prior.err}
		case !prior.txn.matches(e.in):
			return outcome{err: ErrIdempotencyConflict}
		}
		return outcome{txn: prior.txn, replayed: true}
	}
	firstByKey[key] = i

	var external *externalKey
	if first, ok := state.accounts[e.in.Postings[0].AccountID]; ok && e.in.ExternalID != "" {
		external = &externalKey{ledgerID: first.ledgerID, externalID: e.in.ExternalID}
		if owner, ok := taken[*external]; ok && owner != key {
			return outcome{err: fmt.Errorf("%w: %s", ErrExternalIDExists, e.in.ExternalID)}
		}
	}

	status := e.in.status()
	effectiveAt := now
	if e.in.EffectiveAt != nil {
		effectiveAt = *e.in.EffectiveAt
	}
	if a, ok := state.accounts[e.in.Postings[0].AccountID]; ok && status != TransactionArchived && a.closedBefore != nil && effectiveAt.Before(*a.closedBefore) {
		return outcome{err: fmt.Errorf("%w: effective_at is before %s", ErrPeriodClosed, a.closedBefore.UTC().Format(time.RFC3339Nano))}
	}
	ledgerID, postings, err := state.transition(change{add: e.in.Postings, status: status, releases: e.releases})
	if err != nil {
		if !e.in.ArchiveOnLockFailure || !(errors.Is(err, ErrBalanceLock) || errors.Is(err, ErrLockVersion)) {
			return outcome{err: err}
		}
		status, ledgerID, postings = archivedEntries(state, e.in.Postings)
	}
	if external != nil {
		taken[*external] = key
	}

	id, err := uuid.NewV7()
	if err != nil {
		return outcome{err: fmt.Errorf("ledger: generate id: %w", err)}
	}
	txn := Transaction{
		ID:             id,
		LedgerID:       ledgerID,
		IdempotencyKey: key,
		ExternalID:     e.in.ExternalID,
		Status:         status,
		Version:        1,
		entriesVersion: 1,
		Description:    e.in.Description,
		Metadata:       normalizeMetadata(e.in.Metadata),
		ReversesID:     e.reverses,
		Postings:       postings,
		EffectiveAt:    effectiveAt,
		CreatedAt:      now,
	}
	switch status {
	case TransactionPosted:
		txn.PostedAt = &now
	case TransactionArchived:
		txn.ArchivedAt = &now
	}
	if status != TransactionPosted {
		request := e.in
		txn.request = &request
	}
	return outcome{txn: txn}
}

func archivedEntries(state *ledgerState, in []Posting) (TransactionStatus, uuid.UUID, []Posting) {
	postings := make([]Posting, len(in))
	for i, p := range in {
		p.Currency = state.accounts[p.AccountID].currency
		postings[i] = p
	}
	return TransactionArchived, state.accounts[in[0].AccountID].ledgerID, postings
}

func runEntries(ctx context.Context, s *service, entries []*entry, atomic bool) ([]outcome, error) {
	var outcomes []outcome
	err := db.RunTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		outcomes, err = applyEntries(ctx, tx, entries, atomic)
		return err
	})
	if errors.Is(err, errAborted) {
		for i := range outcomes {
			if outcomes[i].err == nil {
				outcomes[i] = outcome{err: ErrBatchAborted}
			}
		}
		return outcomes, nil
	}
	if err != nil {
		return nil, err
	}
	return outcomes, nil
}
