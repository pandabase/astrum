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

type postingRequest struct {
	in       PostInput
	reverses *uuid.UUID
	releases map[uuid.UUID]money.Amount
}

type postingResult struct {
	txn      Transaction
	replayed bool
	err      error
}

func applyRequests(ctx context.Context, tx pgx.Tx, reqs []*postingRequest, atomic bool) ([]postingResult, error) {
	var (
		accountIDs  []uuid.UUID
		keys        = make([]string, 0, len(reqs))
		externalIDs []string
	)
	for _, req := range reqs {
		keys = append(keys, req.in.IdempotencyKey)
		if req.in.ExternalID != "" {
			externalIDs = append(externalIDs, req.in.ExternalID)
		}
		for _, p := range req.in.Postings {
			accountIDs = append(accountIDs, p.AccountID)
		}
		for id := range req.releases {
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
	results := make([]postingResult, len(reqs))
	firstByKey := make(map[string]int, len(reqs))
	var accepted []Transaction

	for i, req := range reqs {
		results[i] = resolveRequest(req, i, state, existing, taken, firstByKey, results, now)
		if results[i].err != nil && atomic {
			return results, errAborted
		}
		if results[i].err == nil && !results[i].replayed {
			accepted = append(accepted, results[i].txn)
		}
	}

	if len(accepted) == 0 {
		return results, nil
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
	return results, nil
}

func resolveRequest(
	req *postingRequest,
	i int,
	state *ledgerState,
	existing map[string]Transaction,
	taken map[externalKey]string,
	firstByKey map[string]int,
	results []postingResult,
	now time.Time,
) postingResult {
	key := req.in.IdempotencyKey
	if req.in.EffectiveAt != nil {
		at := req.in.EffectiveAt.Truncate(time.Microsecond)
		req.in.EffectiveAt = &at
	}

	if txn, ok := existing[key]; ok {
		if !txn.matches(req.in) {
			return postingResult{err: ErrIdempotencyConflict}
		}
		return postingResult{txn: txn, replayed: true}
	}

	if j, ok := firstByKey[key]; ok {
		prior := results[j]
		switch {
		case prior.err != nil:
			return postingResult{err: prior.err}
		case !prior.txn.matches(req.in):
			return postingResult{err: ErrIdempotencyConflict}
		}
		return postingResult{txn: prior.txn, replayed: true}
	}
	firstByKey[key] = i

	var external *externalKey
	if first, ok := state.accounts[req.in.Postings[0].AccountID]; ok && req.in.ExternalID != "" {
		external = &externalKey{ledgerID: first.ledgerID, externalID: req.in.ExternalID}
		if owner, ok := taken[*external]; ok && owner != key {
			return postingResult{err: fmt.Errorf("%w: %s", ErrExternalIDExists, req.in.ExternalID)}
		}
	}

	status := req.in.status()
	effectiveAt := now
	if req.in.EffectiveAt != nil {
		effectiveAt = *req.in.EffectiveAt
	}
	if a, ok := state.accounts[req.in.Postings[0].AccountID]; ok && status != TransactionArchived && a.closedBefore != nil && effectiveAt.Before(*a.closedBefore) {
		return postingResult{err: fmt.Errorf("%w: effective_at is before %s", ErrPeriodClosed, a.closedBefore.UTC().Format(time.RFC3339Nano))}
	}
	ledgerID, postings, err := state.transition(balanceChange{add: req.in.Postings, status: status, releases: req.releases})
	if err != nil {
		if !req.in.ArchiveOnLockFailure || !(errors.Is(err, ErrBalanceLock) || errors.Is(err, ErrLockVersion)) {
			return postingResult{err: err}
		}
		status, ledgerID, postings = archivedPostings(state, req.in.Postings)
	}
	if external != nil {
		taken[*external] = key
	}

	id, err := uuid.NewV7()
	if err != nil {
		return postingResult{err: fmt.Errorf("ledger: generate id: %w", err)}
	}
	txn := Transaction{
		ID:             id,
		LedgerID:       ledgerID,
		IdempotencyKey: key,
		ExternalID:     req.in.ExternalID,
		Status:         status,
		Version:        1,
		entriesVersion: 1,
		Description:    req.in.Description,
		Metadata:       normalizeMetadata(req.in.Metadata),
		ReversesID:     req.reverses,
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
		request := req.in
		txn.request = &request
	}
	return postingResult{txn: txn}
}

func archivedPostings(state *ledgerState, in []Posting) (TransactionStatus, uuid.UUID, []Posting) {
	postings := make([]Posting, len(in))
	for i, p := range in {
		p.Currency = state.accounts[p.AccountID].currency
		postings[i] = p
	}
	return TransactionArchived, state.accounts[in[0].AccountID].ledgerID, postings
}

func runRequests(ctx context.Context, s *service, reqs []*postingRequest, atomic bool) ([]postingResult, error) {
	var results []postingResult
	err := db.RunTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		results, err = applyRequests(ctx, tx, reqs, atomic)
		return err
	})
	if errors.Is(err, errAborted) {
		for i := range results {
			if results[i].err == nil {
				results[i] = postingResult{err: ErrBatchAborted}
			}
		}
		return results, nil
	}
	if err != nil {
		return nil, err
	}
	return results, nil
}
