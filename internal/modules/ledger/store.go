package ledger

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/pandabase/astrum/internal/kernel/db"
	"github.com/pandabase/astrum/internal/kernel/events"
	"github.com/pandabase/astrum/internal/money"
)

const (
	constraintAccountCurrency  = "ledger_accounts_currency_fkey"
	constraintAccountLedger    = "ledger_accounts_ledger_id_fkey"
	constraintSameLedger       = "ledger_postings_same_ledger"
	constraintCategoryLedger   = "ledger_categories_ledger_id_fkey"
	constraintCategoryCurrency = "ledger_categories_currency_fkey"
	constraintMemberAccount    = "ledger_category_accounts_account_fkey"
	constraintMemberCategory   = "ledger_category_accounts_category_fkey"
	constraintEdgeParent       = "ledger_category_edges_parent_fkey"
	constraintEdgeChild        = "ledger_category_edges_child_fkey"
	constraintFunds            = "ledger_accounts_funds_check"
	constraintAccountNotOpen   = "ledger_accounts_not_open"
	constraintClosedEmpty      = "ledger_accounts_closed_empty"
	constraintTransactionKey   = "ledger_transactions_idempotency_key_key"
	constraintExternalID       = "ledger_transactions_external_id_key"
	constraintNotPending       = "ledger_transactions_not_pending"
	constraintReverses         = "ledger_transactions_reverses_id_key"
	constraintHoldKey          = "ledger_holds_idempotency_key_key"
	constraintPostingsBalanced = "ledger_postings_balanced"
	constraintPeriodOpen       = "ledger_transactions_period_open"
)

type querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

const currencyColumns = `code, exponent, created_at`

func scanCurrency(row pgx.Row) (Currency, error) {
	var (
		c    Currency
		code string
	)
	if err := row.Scan(&code, &c.Exponent, &c.CreatedAt); err != nil {
		return Currency{}, err
	}
	c.Code = money.Currency(code)
	return c, nil
}

func insertCurrency(ctx context.Context, q querier, in CreateCurrencyInput) (Currency, bool, error) {
	c, err := scanCurrency(q.QueryRow(ctx, `
		INSERT INTO ledger_currencies (code, exponent)
		VALUES ($1, $2)
		ON CONFLICT (code) DO NOTHING
		RETURNING `+currencyColumns,
		string(in.Code), in.Exponent))
	if errors.Is(err, pgx.ErrNoRows) {
		return Currency{}, false, nil
	}
	return c, err == nil, err
}

func selectCurrency(ctx context.Context, q querier, code money.Currency) (Currency, error) {
	c, err := scanCurrency(q.QueryRow(ctx, `SELECT `+currencyColumns+` FROM ledger_currencies WHERE code = $1`, string(code)))
	if errors.Is(err, pgx.ErrNoRows) {
		return Currency{}, ErrNotFound
	}
	return c, err
}

func selectCurrencies(ctx context.Context, q querier, after money.Currency, limit int) ([]Currency, error) {
	rows, err := q.Query(ctx, `
		SELECT `+currencyColumns+`
		FROM ledger_currencies
		WHERE code > $1
		ORDER BY code
		LIMIT $2`, string(after), limit)
	if err != nil {
		return nil, fmt.Errorf("select currencies: %w", err)
	}
	currencies, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Currency, error) { return scanCurrency(row) })
	if err != nil {
		return nil, fmt.Errorf("select currencies: %w", err)
	}
	return currencies, nil
}

const ledgerColumns = `id, name, description, metadata, closed_before, version, created_at`

func scanLedger(row pgx.Row) (Ledger, error) {
	var (
		l        Ledger
		metadata []byte
	)
	if err := row.Scan(&l.ID, &l.Name, &l.Description, &metadata, &l.ClosedBefore, &l.Version, &l.CreatedAt); err != nil {
		return Ledger{}, err
	}
	l.Metadata = bytes.Clone(metadata)
	return l, nil
}

func insertLedger(ctx context.Context, q querier, id uuid.UUID, in CreateLedgerInput) (Ledger, error) {
	return scanLedger(q.QueryRow(ctx, `
		INSERT INTO ledger_ledgers (id, name, description, metadata)
		VALUES ($1, $2, $3, $4::text::jsonb)
		RETURNING `+ledgerColumns,
		id, in.Name, in.Description, string(normalizeMetadata(in.Metadata))))
}

func queryLedger(ctx context.Context, q querier, where string, arg any) (Ledger, error) {
	l, err := scanLedger(q.QueryRow(ctx, `SELECT `+ledgerColumns+` FROM ledger_ledgers WHERE `+where, arg))
	if errors.Is(err, pgx.ErrNoRows) {
		return Ledger{}, ErrNotFound
	}
	return l, err
}

func selectLedgers(ctx context.Context, q querier, in ListLedgersInput) ([]Ledger, error) {
	rows, err := q.Query(ctx, `
		SELECT `+ledgerColumns+`
		FROM ledger_ledgers
		WHERE ($1::uuid IS NULL OR id < $1) AND ($2::jsonb IS NULL OR metadata @> $2)
		ORDER BY id DESC
		LIMIT $3`, nullUUID(in.Before), metadataFilter(in.Metadata), in.Limit)
	if err != nil {
		return nil, fmt.Errorf("select ledgers: %w", err)
	}
	ledgers, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Ledger, error) { return scanLedger(row) })
	if err != nil {
		return nil, fmt.Errorf("select ledgers: %w", err)
	}
	return ledgers, nil
}

func updateLedger(ctx context.Context, q querier, l Ledger) (Ledger, error) {
	return scanLedger(q.QueryRow(ctx, `
		UPDATE ledger_ledgers
		SET name = $2, description = $3, metadata = $4::text::jsonb, version = version + 1
		WHERE id = $1
		RETURNING `+ledgerColumns,
		l.ID, l.Name, l.Description, string(l.Metadata)))
}

const (
	accountColumns = `a.id, a.ledger_id, a.code, a.name, a.description, a.metadata, a.currency, c.exponent,
	a.normal_side, a.allow_negative, a.overdraft_limit, a.posted_debits, a.posted_credits, a.pending_debits,
	a.pending_credits, a.held, a.status, a.status_changed_at, a.version, a.created_at`
	currencyJoin = ` JOIN ledger_currencies AS c ON c.code = a.currency`
)

func scanAccount(row pgx.Row) (Account, error) {
	var (
		acc        Account
		a          accountState
		metadata   []byte
		currency   string
		normalSide string
		status     string
	)
	err := row.Scan(&acc.ID, &acc.LedgerID, &acc.Code, &acc.Name, &acc.Description, &metadata, &currency,
		&acc.CurrencyExponent, &normalSide, &acc.AllowNegative, &acc.OverdraftLimit, &a.postedDebits, &a.postedCredits,
		&a.pendingDebits, &a.pendingCredits, &acc.Held, &status, &acc.StatusChangedAt, &acc.Version, &acc.CreatedAt)
	if err != nil {
		return Account{}, err
	}
	acc.Metadata = bytes.Clone(metadata)
	acc.Currency = money.Currency(currency)
	acc.NormalSide = Side(normalSide)
	acc.Status = AccountStatus(status)
	a.normalSide, a.held = acc.NormalSide, acc.Held
	if acc.Posted, acc.Pending, acc.Available, err = a.balances(); err != nil {
		return Account{}, err
	}
	return acc, nil
}

func insertAccount(ctx context.Context, q querier, id uuid.UUID, in CreateAccountInput) (Account, bool, error) {
	row := q.QueryRow(ctx, `
		WITH a AS (
			INSERT INTO ledger_accounts
				(id, ledger_id, code, name, description, metadata, currency, normal_side, allow_negative, overdraft_limit)
			VALUES ($1, $2, $3, $4, $5, $6::text::jsonb, $7, $8, $9, $10)
			ON CONFLICT (ledger_id, code) DO NOTHING
			RETURNING *)
		SELECT `+accountColumns+` FROM a`+currencyJoin,
		id, in.LedgerID, in.Code, in.Name, in.Description, string(normalizeMetadata(in.Metadata)),
		string(in.Currency), string(in.NormalSide), in.AllowNegative, in.OverdraftLimit)

	acc, err := scanAccount(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Account{}, false, nil
	}
	return acc, err == nil, err
}

func selectAccount(ctx context.Context, q querier, id uuid.UUID) (Account, error) {
	return queryAccount(ctx, q, `a.id = $1`, id)
}

func selectAccounts(ctx context.Context, q querier, in ListAccountsInput) ([]Account, error) {
	rows, err := q.Query(ctx, `
		SELECT `+accountColumns+`
		FROM ledger_accounts AS a`+currencyJoin+`
		WHERE ($1::uuid IS NULL OR a.id < $1)
		  AND ($2::text IS NULL OR a.status = $2)
		  AND ($3::text IS NULL OR a.currency = $3)
		  AND ($4::uuid IS NULL OR a.ledger_id = $4)
		  AND ($5::text IS NULL OR a.code = $5)
		  AND ($6::jsonb IS NULL OR a.metadata @> $6)
		  AND ($7::uuid IS NULL OR a.id IN (`+fmt.Sprintf(categoryMembers, "$7")+`))
		ORDER BY a.id DESC
		LIMIT $8`,
		nullUUID(in.Before), nullString(string(in.Status)), nullString(string(in.Currency)),
		nullUUID(in.LedgerID), nullString(in.Code), metadataFilter(in.Metadata), nullUUID(in.CategoryID), in.Limit)
	if err != nil {
		return nil, fmt.Errorf("select accounts: %w", err)
	}
	accounts, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Account, error) { return scanAccount(row) })
	if err != nil {
		return nil, fmt.Errorf("select accounts: %w", err)
	}
	return accounts, nil
}

func updateAccountStatus(ctx context.Context, q querier, id uuid.UUID, status AccountStatus) (Account, error) {
	return scanAccount(q.QueryRow(ctx, `
		WITH a AS (
			UPDATE ledger_accounts
			SET status = $2, status_changed_at = now(), version = version + 1
			WHERE id = $1
			RETURNING *)
		SELECT `+accountColumns+` FROM a`+currencyJoin, id, string(status)))
}

func updateAccountDetails(ctx context.Context, q querier, acc Account) (Account, error) {
	return scanAccount(q.QueryRow(ctx, `
		WITH a AS (
			UPDATE ledger_accounts
			SET name = $2, description = $3, metadata = $4::text::jsonb, version = version + 1
			WHERE id = $1
			RETURNING *)
		SELECT `+accountColumns+` FROM a`+currencyJoin,
		acc.ID, acc.Name, acc.Description, string(acc.Metadata)))
}

func selectAccountByCode(ctx context.Context, q querier, ledgerID uuid.UUID, code string) (Account, error) {
	acc, err := scanAccount(q.QueryRow(ctx, `
		SELECT `+accountColumns+`
		FROM ledger_accounts AS a`+currencyJoin+`
		WHERE a.ledger_id = $1 AND a.code = $2`, ledgerID, code))
	if errors.Is(err, pgx.ErrNoRows) {
		return Account{}, ErrNotFound
	}
	return acc, err
}

func queryAccount(ctx context.Context, q querier, where string, arg any) (Account, error) {
	acc, err := scanAccount(q.QueryRow(ctx, `SELECT `+accountColumns+` FROM ledger_accounts AS a`+currencyJoin+` WHERE `+where, arg))
	if errors.Is(err, pgx.ErrNoRows) {
		return Account{}, ErrNotFound
	}
	return acc, err
}

func queueLockAccounts(b *pgx.Batch, ids []uuid.UUID, out *[]*accountState) {
	ids = sortedUnique(ids)
	b.Queue(`
		SELECT a.id, a.ledger_id, a.currency, a.normal_side, a.allow_negative, a.overdraft_limit, a.posted_debits,
			a.posted_credits, a.pending_debits, a.pending_credits, a.held, a.status, a.version, l.closed_before
		FROM ledger_accounts AS a
		JOIN ledger_ledgers AS l ON l.id = a.ledger_id
		WHERE a.id = ANY($1)
		ORDER BY a.id
		FOR UPDATE OF a`, ids,
	).Query(func(rows pgx.Rows) error {
		var (
			a          accountState
			currency   string
			normalSide string
			status     string
		)
		_, err := pgx.ForEachRow(rows,
			[]any{&a.id, &a.ledgerID, &currency, &normalSide, &a.allowNegative, &a.overdraftLimit, &a.postedDebits,
				&a.postedCredits, &a.pendingDebits, &a.pendingCredits, &a.held, &status, &a.version, &a.closedBefore},
			func() error {
				a.currency = money.Currency(currency)
				a.normalSide = Side(normalSide)
				a.status = AccountStatus(status)
				locked := a
				*out = append(*out, &locked)
				return nil
			})
		return err
	})
}

func lockAccounts(ctx context.Context, tx pgx.Tx, ids []uuid.UUID) (*ledgerState, error) {
	var (
		accounts []*accountState
		monitors []BalanceMonitor
	)
	b := &pgx.Batch{}
	queueLockAccounts(b, ids, &accounts)
	queueSelectMonitors(b, ids, &monitors)
	if err := tx.SendBatch(ctx, b).Close(); err != nil {
		return nil, fmt.Errorf("lock accounts: %w", err)
	}
	return newLedgerState(accounts, monitors), nil
}

func queueSelectMonitors(b *pgx.Batch, ids []uuid.UUID, out *[]BalanceMonitor) {
	b.Queue(`SELECT `+monitorColumns+` FROM ledger_balance_monitors WHERE account_id = ANY($1)`, sortedUnique(ids)).
		Query(func(rows pgx.Rows) error {
			monitors, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (BalanceMonitor, error) { return scanMonitor(row) })
			*out = append(*out, monitors...)
			return err
		})
}

func queueWriteAccounts(b *pgx.Batch, state *ledgerState) error {
	dirty := state.dirty()
	if len(dirty) == 0 {
		return nil
	}
	triggered, err := monitorEvents(state)
	if err != nil {
		return err
	}
	events.Queue(b, triggered...)
	var (
		ids            = make([]uuid.UUID, len(dirty))
		postedDebits   = make([]money.Amount, len(dirty))
		postedCredits  = make([]money.Amount, len(dirty))
		pendingDebits  = make([]money.Amount, len(dirty))
		pendingCredits = make([]money.Amount, len(dirty))
		held           = make([]money.Amount, len(dirty))
		versions       = make([]int64, len(dirty))
		expected       = make([]int64, len(dirty))
	)
	for i, a := range dirty {
		ids[i] = a.id
		postedDebits[i], postedCredits[i] = a.postedDebits, a.postedCredits
		pendingDebits[i], pendingCredits[i] = a.pendingDebits, a.pendingCredits
		held[i] = a.held
		versions[i] = a.version
		expected[i] = a.originalVersion
	}
	b.Queue(`
		UPDATE ledger_accounts AS a
		SET posted_debits = d.posted_debits, posted_credits = d.posted_credits, pending_debits = d.pending_debits,
			pending_credits = d.pending_credits, held = d.held, version = d.version
		FROM unnest($1::uuid[], $2::numeric[], $3::numeric[], $4::numeric[], $5::numeric[], $6::numeric[],
			$7::bigint[], $8::bigint[])
			AS d(id, posted_debits, posted_credits, pending_debits, pending_credits, held, version, expected)
		WHERE a.id = d.id AND a.version = d.expected`,
		ids, postedDebits, postedCredits, pendingDebits, pendingCredits, held, versions, expected,
	).Exec(func(tag pgconn.CommandTag) error {
		if tag.RowsAffected() != int64(len(dirty)) {
			return fmt.Errorf("%w: account version moved while locked", db.ErrRetry)
		}
		return nil
	})
	return nil
}

func writeAccounts(ctx context.Context, tx pgx.Tx, state *ledgerState) error {
	b := &pgx.Batch{}
	if err := queueWriteAccounts(b, state); err != nil {
		return err
	}
	if b.Len() == 0 {
		return nil
	}
	return tx.SendBatch(ctx, b).Close()
}

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

func nullUUID(id uuid.UUID) *uuid.UUID {
	if id == uuid.Nil {
		return nil
	}
	return &id
}

func nullString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
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

const holdColumns = `id, idempotency_key, account_id, currency, amount, status, description,
	expires_at, created_at, resolved_at, captured_amount, capture_transaction_id`

func scanHold(row pgx.Row) (Hold, error) {
	var (
		h        Hold
		currency string
		status   string
	)
	err := row.Scan(&h.ID, &h.IdempotencyKey, &h.AccountID, &currency, &h.Amount, &status, &h.Description,
		&h.ExpiresAt, &h.CreatedAt, &h.ResolvedAt, &h.CapturedAmount, &h.CaptureTransactionID)
	if err != nil {
		return Hold{}, err
	}
	h.Currency = money.Currency(currency)
	h.Status = HoldStatus(status)
	return h, nil
}

func selectHold(ctx context.Context, q querier, where string, arg any) (Hold, error) {
	h, err := scanHold(q.QueryRow(ctx, `SELECT `+holdColumns+` FROM ledger_holds WHERE `+where, arg))
	if errors.Is(err, pgx.ErrNoRows) {
		return Hold{}, ErrNotFound
	}
	return h, err
}

func insertHold(ctx context.Context, tx pgx.Tx, h Hold) (Hold, error) {
	return scanHold(tx.QueryRow(ctx, `
		INSERT INTO ledger_holds (id, idempotency_key, account_id, currency, amount, description, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING `+holdColumns,
		h.ID, h.IdempotencyKey, h.AccountID, string(h.Currency), h.Amount, h.Description, h.ExpiresAt))
}

func resolveHold(ctx context.Context, tx pgx.Tx, id uuid.UUID, status HoldStatus, captured *money.Amount, txnID *uuid.UUID) (Hold, error) {
	return scanHold(tx.QueryRow(ctx, `
		UPDATE ledger_holds
		SET status = $2, resolved_at = now(), captured_amount = $3, capture_transaction_id = $4
		WHERE id = $1
		RETURNING `+holdColumns,
		id, string(status), captured, txnID))
}

const scheduleColumns = `id, idempotency_key, execute_at, request, status, transaction_id, failure,
	created_at, resolved_at`

func scanSchedule(row pgx.Row) (ScheduledTransaction, error) {
	var (
		s       ScheduledTransaction
		request []byte
		status  string
	)
	err := row.Scan(&s.ID, &s.IdempotencyKey, &s.ExecuteAt, &request, &status, &s.TransactionID, &s.Failure,
		&s.CreatedAt, &s.ResolvedAt)
	if err != nil {
		return ScheduledTransaction{}, err
	}
	if err := json.Unmarshal(request, &s.Request); err != nil {
		return ScheduledTransaction{}, fmt.Errorf("decode scheduled request %s: %w", s.ID, err)
	}
	s.Status = ScheduleStatus(status)
	return s, nil
}

func selectSchedule(ctx context.Context, q querier, where string, arg any) (ScheduledTransaction, error) {
	s, err := scanSchedule(q.QueryRow(ctx, `SELECT `+scheduleColumns+` FROM ledger_scheduled_transactions WHERE `+where, arg))
	if errors.Is(err, pgx.ErrNoRows) {
		return ScheduledTransaction{}, ErrNotFound
	}
	return s, err
}

func sortedUnique(ids []uuid.UUID) []uuid.UUID {
	ids = slices.Clone(ids)
	slices.SortFunc(ids, func(a, b uuid.UUID) int { return bytes.Compare(a[:], b[:]) })
	return slices.Compact(ids)
}

func queueNow(b *pgx.Batch, out *time.Time) {
	b.Queue(`SELECT now()`).QueryRow(func(row pgx.Row) error { return row.Scan(out) })
}

func metadataFilter(m map[string]string) *string {
	if len(m) == 0 {
		return nil
	}
	raw, _ := json.Marshal(m, json.Deterministic(true))
	return new(string(raw))
}

func xidBound(bound uint64) *string {
	if bound == 0 {
		return nil
	}
	return new(strconv.FormatUint(bound, 10))
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
