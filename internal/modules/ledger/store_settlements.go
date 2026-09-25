package ledger

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pandabase/astrum/internal/money"
)

func sumUnsettled(ctx context.Context, q querier, accountID uuid.UUID, upperBound *time.Time) (debits, credits money.Amount, count int, err error) {
	err = q.QueryRow(ctx, `
		SELECT coalesce(sum(p.amount) FILTER (WHERE p.side = 'debit'), 0),
		       coalesce(sum(p.amount) FILTER (WHERE p.side = 'credit'), 0),
		       count(*)
		FROM ledger_postings AS p
		JOIN ledger_transactions AS t ON t.id = p.transaction_id
		WHERE p.account_id = $1
		  AND ($2::timestamptz IS NULL OR t.effective_at < $2)
		  AND NOT EXISTS (SELECT 1 FROM ledger_settlement_entries AS s WHERE s.posting_id = p.id)`,
		accountID, upperBound).Scan(&debits, &credits, &count)
	return debits, credits, count, err
}

func insertSettlement(ctx context.Context, q querier, st Settlement) (Settlement, error) {
	return scanSettlement(q.QueryRow(ctx, `
		INSERT INTO ledger_settlements (id, idempotency_key, ledger_id, settled_account_id, contra_account_id,
			currency, effective_at_upper_bound, amount, entry_count, transaction_id, description, metadata)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12::text::jsonb)
		RETURNING `+settlementColumns,
		st.ID, st.IdempotencyKey, st.LedgerID, st.SettledAccountID, st.ContraAccountID, string(st.Currency),
		st.UpperBound, st.Amount, st.EntryCount, st.TransactionID, st.Description, string(st.Metadata)))
}

func markSettled(ctx context.Context, tx pgx.Tx, st Settlement) (int64, error) {
	tag, err := tx.Exec(ctx, `
		INSERT INTO ledger_settlement_entries (posting_id, settlement_id)
		SELECT p.id, $3
		FROM ledger_postings AS p
		JOIN ledger_transactions AS t ON t.id = p.transaction_id
		WHERE p.account_id = $1
		  AND (p.transaction_id = $4 OR $2::timestamptz IS NULL OR t.effective_at < $2)
		  AND NOT EXISTS (SELECT 1 FROM ledger_settlement_entries AS s WHERE s.posting_id = p.id)`,
		st.SettledAccountID, st.UpperBound, st.ID, st.TransactionID)
	return tag.RowsAffected(), err
}
