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
	"github.com/pandabase/astrum/internal/money"
)

const transactionSelect = `
	SELECT t.id, t.ledger_id, t.idempotency_key, t.external_id, t.status, t.version, t.entries_version, t.description, t.metadata,
	       t.reverses_id, t.effective_at, t.created_at, t.posted_at, t.archived_at, t.pending_request,
	       e.account_id, e.currency, e.side, e.amount, e.balance_after
	FROM ledger_transactions AS t
	CROSS JOIN LATERAL (
		SELECT p.id, p.account_id, p.currency, p.side, p.amount, p.balance_after
		FROM ledger_postings AS p
		WHERE p.transaction_id = t.id AND t.status = 'posted'
		UNION ALL
		SELECT p.id, p.account_id, p.currency, p.side, p.amount, NULL
		FROM ledger_pending_entries AS p
		WHERE p.transaction_id = t.id AND p.version = t.entries_version AND t.status <> 'posted'
	) AS e`

func scanTransactions(rows pgx.Rows) ([]Transaction, error) {
	var (
		out          []Transaction
		index        = map[uuid.UUID]int{}
		txn          Transaction
		externalID   *string
		status       string
		metadata     []byte
		request      []byte
		posting      Posting
		currency     string
		side         string
		balanceAfter *money.Amount
	)
	_, err := pgx.ForEachRow(rows,
		[]any{&txn.ID, &txn.LedgerID, &txn.IdempotencyKey, &externalID, &status, &txn.Version, &txn.entriesVersion, &txn.Description, &metadata,
			&txn.ReversesID, &txn.EffectiveAt, &txn.CreatedAt, &txn.PostedAt, &txn.ArchivedAt, &request,
			&posting.AccountID, &currency, &side, &posting.Amount, &balanceAfter},
		func() error {
			posting.Currency = money.Currency(currency)
			posting.Side = Side(side)
			posting.balanceAfter = money.Amount{}
			if balanceAfter != nil {
				posting.balanceAfter = *balanceAfter
			}
			i, ok := index[txn.ID]
			if !ok {
				i = len(out)
				index[txn.ID] = i
				t := txn
				t.Status = TransactionStatus(status)
				t.ExternalID = ""
				if externalID != nil {
					t.ExternalID = *externalID
				}
				t.Metadata = bytes.Clone(metadata)
				t.Postings = nil
				t.request = nil
				if request != nil {
					t.request = &PostInput{}
					if err := json.Unmarshal(request, t.request); err != nil {
						return fmt.Errorf("decode pending request of %s: %w", t.ID, err)
					}
				}
				out = append(out, t)
			}
			out[i].Postings = append(out[i].Postings, posting)
			return nil
		})
	return out, err
}

func selectTransactions(ctx context.Context, q querier, in ListTransactionsInput) ([]Transaction, error) {
	rows, err := q.Query(ctx, transactionSelect+`
		WHERE t.id IN (
			SELECT l.id
			FROM ledger_transactions AS l
			WHERE ($1::uuid IS NULL OR l.id < $1)
			  AND ($2::uuid IS NULL OR EXISTS (
				SELECT 1 FROM ledger_postings AS a WHERE a.transaction_id = l.id AND a.account_id = $2
				UNION ALL
				SELECT 1 FROM ledger_pending_entries AS a
				WHERE a.transaction_id = l.id AND a.account_id = $2 AND a.version = l.entries_version))
			  AND ($3::uuid IS NULL OR l.ledger_id = $3)
			  AND ($4::text IS NULL OR l.status = $4)
			  AND ($5::text IS NULL OR l.external_id = $5)
			  AND ($6::jsonb IS NULL OR l.metadata @> $6)
			  AND ($7::timestamptz IS NULL OR l.effective_at >= $7)
			  AND ($8::timestamptz IS NULL OR l.effective_at < $8)
			ORDER BY l.id DESC
			LIMIT $9)
		ORDER BY t.id DESC, e.id`,
		nullUUID(in.Before), nullUUID(in.AccountID), nullUUID(in.LedgerID), nullString(string(in.Status)),
		nullString(in.ExternalID), metadataFilter(in.Metadata), in.Effective.From, in.Effective.Until, in.Limit)
	if err != nil {
		return nil, fmt.Errorf("select transactions: %w", err)
	}
	txns, err := scanTransactions(rows)
	if err != nil {
		return nil, fmt.Errorf("select transactions: %w", err)
	}
	if txns == nil {
		txns = []Transaction{}
	}
	return txns, nil
}

func selectTransaction(ctx context.Context, q querier, id uuid.UUID) (Transaction, error) {
	return selectOneTransaction(ctx, q, `t.id = $1`, id)
}

func lockTransaction(ctx context.Context, q querier, id uuid.UUID) (Transaction, error) {
	var locked uuid.UUID
	err := q.QueryRow(ctx, `SELECT id FROM ledger_transactions WHERE id = $1 FOR UPDATE`, id).Scan(&locked)
	if errors.Is(err, pgx.ErrNoRows) {
		return Transaction{}, ErrNotFound
	}
	if err != nil {
		return Transaction{}, err
	}
	return selectTransaction(ctx, q, id)
}

func selectOneTransaction(ctx context.Context, q querier, where string, arg any) (Transaction, error) {
	rows, err := q.Query(ctx, transactionSelect+` WHERE `+where+` ORDER BY e.id`, arg)
	if err != nil {
		return Transaction{}, fmt.Errorf("select transaction: %w", err)
	}
	txns, err := scanTransactions(rows)
	if err != nil {
		return Transaction{}, fmt.Errorf("select transaction: %w", err)
	}
	if len(txns) == 0 {
		return Transaction{}, ErrNotFound
	}
	return txns[0], nil
}

func queueSelectTransactionsByKey(b *pgx.Batch, keys []string, out map[string]Transaction) {
	b.Queue(transactionSelect+` WHERE t.idempotency_key = ANY($1) ORDER BY e.id`, keys).
		Query(func(rows pgx.Rows) error {
			txns, err := scanTransactions(rows)
			for _, t := range txns {
				out[t.IdempotencyKey] = t
			}
			return err
		})
}

func queueSelectExternalIDs(b *pgx.Batch, ids []string, out map[externalKey]string) {
	if len(ids) == 0 {
		return
	}
	b.Queue(`SELECT ledger_id, external_id, idempotency_key FROM ledger_transactions WHERE external_id = ANY($1)`, ids).
		Query(func(rows pgx.Rows) error {
			var (
				k   externalKey
				key string
			)
			_, err := pgx.ForEachRow(rows, []any{&k.ledgerID, &k.externalID, &key}, func() error {
				out[k] = key
				return nil
			})
			return err
		})
}

type externalKey struct {
	ledgerID   uuid.UUID
	externalID string
}

func selectReversal(ctx context.Context, q querier, id uuid.UUID) (Transaction, error) {
	txn, err := selectOneTransaction(ctx, q, `t.reverses_id = $1`, id)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return Transaction{}, fmt.Errorf("select reversal: %w", err)
	}
	return txn, err
}

func queueInsertEntries(b *pgx.Batch, txns []Transaction) {
	var (
		ids          = make([]uuid.UUID, len(txns))
		ledgers      = make([]uuid.UUID, len(txns))
		keys         = make([]string, len(txns))
		externalIDs  = make([]*string, len(txns))
		statuses     = make([]string, len(txns))
		descs        = make([]string, len(txns))
		metadata     = make([]string, len(txns))
		reverses     = make([]uuid.NullUUID, len(txns))
		effectiveAts = make([]time.Time, len(txns))
		requests     = make([]*string, len(txns))

		posted, pending entryColumns
	)
	for i, t := range txns {
		ids[i] = t.ID
		ledgers[i] = t.LedgerID
		keys[i] = t.IdempotencyKey
		externalIDs[i] = nullString(t.ExternalID)
		statuses[i] = string(t.Status)
		descs[i] = t.Description
		metadata[i] = string(t.Metadata)
		if t.ReversesID != nil {
			reverses[i] = uuid.NullUUID{UUID: *t.ReversesID, Valid: true}
		}
		effectiveAts[i] = t.EffectiveAt
		if t.request != nil {
			raw, _ := json.Marshal(t.request)
			requests[i] = new(string(raw))
		}
		target := &pending
		if t.Status == TransactionPosted {
			target = &posted
		}
		target.add(t.ID, t.Postings)
	}

	b.Queue(`
		INSERT INTO ledger_transactions (id, ledger_id, idempotency_key, external_id, status, description, metadata,
			reverses_id, effective_at, pending_request, posted_at, posted_xid, archived_at)
		SELECT id, ledger_id, key, external_id, status, description, metadata::jsonb, reverses_id, effective_at,
			request::jsonb,
			CASE WHEN status = 'posted' THEN now() END,
			CASE WHEN status = 'posted' THEN pg_current_xact_id() END,
			CASE WHEN status = 'archived' THEN now() END
		FROM unnest($1::uuid[], $2::uuid[], $3::text[], $4::text[], $5::text[], $6::text[], $7::text[], $8::uuid[],
			$9::timestamptz[], $10::text[])
			AS t(id, ledger_id, key, external_id, status, description, metadata, reverses_id, effective_at, request)`,
		ids, ledgers, keys, externalIDs, statuses, descs, metadata, reverses, effectiveAts, requests)
	if len(posted.txns) > 0 {
		queueInsertPostings(b, posted)
	}
	if len(pending.txns) > 0 {
		queueInsertPendingEntries(b, pending, 1)
	}
}

type entryColumns struct {
	txns, accounts    []uuid.UUID
	currencies, sides []string
	amounts, after    []money.Amount
}

func (c *entryColumns) add(txnID uuid.UUID, postings []Posting) {
	for _, p := range postings {
		c.txns = append(c.txns, txnID)
		c.accounts = append(c.accounts, p.AccountID)
		c.currencies = append(c.currencies, string(p.Currency))
		c.sides = append(c.sides, string(p.Side))
		c.amounts = append(c.amounts, p.Amount)
		c.after = append(c.after, p.balanceAfter)
	}
}

func queueInsertPostings(b *pgx.Batch, c entryColumns) {
	b.Queue(`
		INSERT INTO ledger_postings (transaction_id, account_id, currency, side, amount, balance_after)
		SELECT transaction_id, account_id, currency, side, amount, balance_after
		FROM unnest($1::uuid[], $2::uuid[], $3::text[], $4::text[], $5::numeric[], $6::numeric[])
			WITH ORDINALITY AS p(transaction_id, account_id, currency, side, amount, balance_after, ord)
		ORDER BY ord`,
		c.txns, c.accounts, c.currencies, c.sides, c.amounts, c.after)
}

func queueInsertPendingEntries(b *pgx.Batch, c entryColumns, version int) {
	b.Queue(`
		INSERT INTO ledger_pending_entries (transaction_id, version, account_id, currency, side, amount)
		SELECT transaction_id, $1, account_id, currency, side, amount
		FROM unnest($2::uuid[], $3::uuid[], $4::text[], $5::text[], $6::numeric[])
			WITH ORDINALITY AS p(transaction_id, account_id, currency, side, amount, ord)
		ORDER BY ord`,
		version, c.txns, c.accounts, c.currencies, c.sides, c.amounts)
}

func queueUpdateTransaction(b *pgx.Batch, t Transaction) {
	b.Queue(`
		UPDATE ledger_transactions
		SET status = $2, version = $3, entries_version = $4, description = $5, metadata = $6::text::jsonb,
			effective_at = $7,
			posted_at = CASE WHEN $2 = 'posted' THEN now() END,
			posted_xid = CASE WHEN $2 = 'posted' THEN pg_current_xact_id() END,
			archived_at = CASE WHEN $2 = 'archived' THEN now() END
		WHERE id = $1`,
		t.ID, string(t.Status), t.Version, t.entriesVersion, t.Description, string(t.Metadata), t.EffectiveAt)
}
