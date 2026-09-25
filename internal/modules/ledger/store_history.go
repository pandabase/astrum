package ledger

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pandabase/astrum/internal/money"
)

func selectAccountEntries(ctx context.Context, q querier, account Account, after int64, limit int) ([]StatementLine, error) {
	rows, err := q.Query(ctx, `
		SELECT p.id, p.transaction_id, p.side, p.amount, p.currency, p.balance_after, t.created_at
		FROM ledger_postings AS p
		JOIN ledger_transactions AS t ON t.id = p.transaction_id
		WHERE p.account_id = $1 AND p.id > $2
		ORDER BY p.id
		LIMIT $3`, account.ID, after, limit)
	if err != nil {
		return nil, fmt.Errorf("select account entries: %w", err)
	}

	lines := []StatementLine{}
	var (
		line     StatementLine
		side     string
		currency string
	)
	_, err = pgx.ForEachRow(rows,
		[]any{&line.PostingID, &line.TransactionID, &side, &line.Amount, &currency, &line.BalanceAfter, &line.CreatedAt},
		func() error {
			line.Side = Side(side)
			line.Currency = money.Currency(currency)
			if account.NormalSide == Credit {
				line.BalanceAfter = line.BalanceAfter.Neg()
			}
			lines = append(lines, line)
			return nil
		})
	if err != nil {
		return nil, fmt.Errorf("select account entries: %w", err)
	}
	return lines, nil
}

const postedEntriesSQL = `
	SELECT p.id, p.transaction_id, t.ledger_id, p.account_id, t.status, p.side, p.amount, p.currency,
	       CASE a.normal_side WHEN 'credit' THEN -p.balance_after ELSE p.balance_after END,
	       t.effective_at, t.posted_at, se.settlement_id
	FROM ledger_postings AS p
	JOIN ledger_transactions AS t ON t.id = p.transaction_id
	JOIN ledger_accounts AS a ON a.id = p.account_id
	LEFT JOIN ledger_settlement_entries AS se ON se.posting_id = p.id
	WHERE p.id > $1
	  AND ($2::uuid IS NULL OR p.account_id = $2)
	  AND ($3::uuid IS NULL OR t.ledger_id = $3)
	  AND ($4::uuid IS NULL OR p.transaction_id = $4)
	  AND ($5::text IS NULL OR p.side = $5)
	  AND ($6::jsonb IS NULL OR t.metadata @> $6)
	  AND ($7::timestamptz IS NULL OR t.effective_at >= $7)
	  AND ($8::timestamptz IS NULL OR t.effective_at < $8)
	  AND ($9::xid8 IS NULL OR t.posted_xid < $9)
	  AND ($11::uuid IS NULL OR se.settlement_id = $11)
	  AND ($12::bool IS NULL OR (se.posting_id IS NOT NULL) = $12)
	ORDER BY p.id
	LIMIT $10`

const openEntriesSQL = `
	SELECT e.id, e.transaction_id, t.ledger_id, e.account_id, t.status, e.side, e.amount, e.currency,
	       NULL::numeric, t.effective_at, t.created_at, NULL::uuid
	FROM ledger_pending_entries AS e
	JOIN ledger_transactions AS t ON t.id = e.transaction_id AND e.version = t.entries_version
	WHERE e.id > $1
	  AND ($2::uuid IS NULL OR e.account_id = $2)
	  AND ($3::uuid IS NULL OR t.ledger_id = $3)
	  AND ($4::uuid IS NULL OR e.transaction_id = $4)
	  AND ($5::text IS NULL OR e.side = $5)
	  AND ($6::jsonb IS NULL OR t.metadata @> $6)
	  AND ($7::timestamptz IS NULL OR t.effective_at >= $7)
	  AND ($8::timestamptz IS NULL OR t.effective_at < $8)
	  AND t.status = $9
	  AND $11::uuid IS NULL AND ($12::bool IS NULL OR NOT $12)
	ORDER BY e.id
	LIMIT $10`

func selectEntries(ctx context.Context, q querier, in ListEntriesInput, postedBefore uint64) ([]Entry, error) {
	query, last := postedEntriesSQL, any(xidBound(postedBefore))
	if in.Status != TransactionPosted {
		query, last = openEntriesSQL, string(in.Status)
	}
	rows, err := q.Query(ctx, query, in.After, nullUUID(in.AccountID), nullUUID(in.LedgerID), nullUUID(in.TransactionID),
		nullString(string(in.Side)), metadataFilter(in.Metadata), in.Effective.From, in.Effective.Until, last, in.Limit,
		nullUUID(in.SettlementID), in.Settled)
	if err != nil {
		return nil, fmt.Errorf("select entries: %w", err)
	}
	entries := []Entry{}
	var (
		e                      Entry
		status, side, currency string
	)
	_, err = pgx.ForEachRow(rows,
		[]any{&e.Sequence, &e.TransactionID, &e.LedgerID, &e.AccountID, &status, &side, &e.Amount, &currency,
			&e.BalanceAfter, &e.EffectiveAt, &e.CreatedAt, &e.SettlementID},
		func() error {
			e.Status, e.Side, e.Currency = TransactionStatus(status), Side(side), money.Currency(currency)
			entries = append(entries, e)
			return nil
		})
	if err != nil {
		return nil, fmt.Errorf("select entries: %w", err)
	}
	return entries, nil
}

type totals struct {
	postedDebits, postedCredits, pendingDebits, pendingCredits money.Amount
	postedCount                                                int64
}

func selectTotals(ctx context.Context, q querier, accountID uuid.UUID, r EffectiveRange, postedBefore uint64) (totals, error) {
	var t totals
	err := q.QueryRow(ctx, `
		WITH posted AS (
			SELECT coalesce(sum(p.amount) FILTER (WHERE p.side = 'debit'), 0) AS debits,
			       coalesce(sum(p.amount) FILTER (WHERE p.side = 'credit'), 0) AS credits,
			       count(*) AS entries
			FROM ledger_postings AS p
			JOIN ledger_transactions AS t ON t.id = p.transaction_id
			WHERE p.account_id = $1
			  AND ($2::timestamptz IS NULL OR t.effective_at >= $2)
			  AND ($3::timestamptz IS NULL OR t.effective_at < $3)
			  AND ($4::xid8 IS NULL OR t.posted_xid < $4)
		), pending AS (
			SELECT coalesce(sum(e.amount) FILTER (WHERE e.side = 'debit'), 0) AS debits,
			       coalesce(sum(e.amount) FILTER (WHERE e.side = 'credit'), 0) AS credits
			FROM ledger_pending_entries AS e
			JOIN ledger_transactions AS t ON t.id = e.transaction_id AND e.version = t.entries_version
			WHERE e.account_id = $1 AND t.status = 'pending' AND $4::xid8 IS NULL
			  AND ($2::timestamptz IS NULL OR t.effective_at >= $2)
			  AND ($3::timestamptz IS NULL OR t.effective_at < $3)
		)
		SELECT posted.debits, posted.credits, posted.entries, pending.debits, pending.credits
		FROM posted, pending`,
		accountID, r.From, r.Until, xidBound(postedBefore),
	).Scan(&t.postedDebits, &t.postedCredits, &t.postedCount, &t.pendingDebits, &t.pendingCredits)
	return t, err
}

const statementColumns = `s.id, s.ledger_id, s.account_id, a.currency, a.normal_side, s.description, s.effective_from,
	s.effective_until, s.posted_before::text, s.starting_debits, s.starting_credits, s.ending_debits, s.ending_credits,
	s.entry_count, s.created_at`

func scanStatement(row pgx.Row) (Statement, error) {
	var (
		st                                 Statement
		currency, normalSide, postedBefore string
		startDebits, startCredits          money.Amount
		endDebits, endCredits              money.Amount
	)
	err := row.Scan(&st.ID, &st.LedgerID, &st.AccountID, &currency, &normalSide, &st.Description, &st.From, &st.Until,
		&postedBefore, &startDebits, &startCredits, &endDebits, &endCredits, &st.EntryCount, &st.CreatedAt)
	if err != nil {
		return Statement{}, err
	}
	st.Currency = money.Currency(currency)
	if st.postedBefore, err = strconv.ParseUint(postedBefore, 10, 64); err != nil {
		return Statement{}, err
	}
	side := &accountState{normalSide: Side(normalSide)}
	if st.Starting, err = side.balance(startDebits, startCredits); err != nil {
		return Statement{}, err
	}
	st.Ending, err = side.balance(endDebits, endCredits)
	return st, err
}

func insertStatement(ctx context.Context, q querier, st Statement) (Statement, error) {
	return scanStatement(q.QueryRow(ctx, `
		WITH s AS (
			INSERT INTO ledger_statements (id, ledger_id, account_id, description, effective_from, effective_until,
				posted_before, starting_debits, starting_credits, ending_debits, ending_credits, entry_count)
			VALUES ($1, $2, $3, $4, $5, $6, $7::text::xid8, $8, $9, $10, $11, $12)
			RETURNING *)
		SELECT `+statementColumns+` FROM s JOIN ledger_accounts AS a ON a.id = s.account_id`,
		st.ID, st.LedgerID, st.AccountID, st.Description, st.From, st.Until, strconv.FormatUint(st.postedBefore, 10),
		st.Starting.Debits, st.Starting.Credits, st.Ending.Debits, st.Ending.Credits, st.EntryCount))
}

func queryStatement(ctx context.Context, q querier, id uuid.UUID) (Statement, error) {
	st, err := scanStatement(q.QueryRow(ctx, `
		SELECT `+statementColumns+`
		FROM ledger_statements AS s JOIN ledger_accounts AS a ON a.id = s.account_id
		WHERE s.id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Statement{}, ErrNotFound
	}
	return st, err
}

func selectStatements(ctx context.Context, q querier, in ListStatementsInput) ([]Statement, error) {
	rows, err := q.Query(ctx, `
		SELECT `+statementColumns+`
		FROM ledger_statements AS s JOIN ledger_accounts AS a ON a.id = s.account_id
		WHERE ($1::uuid IS NULL OR s.account_id = $1) AND ($2::uuid IS NULL OR s.id < $2)
		ORDER BY s.id DESC
		LIMIT $3`, nullUUID(in.AccountID), nullUUID(in.Before), in.Limit)
	if err != nil {
		return nil, fmt.Errorf("select statements: %w", err)
	}
	statements, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Statement, error) { return scanStatement(row) })
	if err != nil {
		return nil, fmt.Errorf("select statements: %w", err)
	}
	return statements, nil
}
