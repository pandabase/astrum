package ledger

import (
	"bytes"
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/pandabase/astrum/internal/kernel/db"
	"github.com/pandabase/astrum/internal/kernel/events"
	"github.com/pandabase/astrum/internal/money"
)

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
	acc.Posted, acc.Pending, acc.Available, err = a.balances()
	if err != nil {
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

func selectNormalSide(ctx context.Context, q querier, accountID uuid.UUID) (Side, error) {
	var side string
	err := q.QueryRow(ctx, `SELECT normal_side FROM ledger_accounts WHERE id = $1`, accountID).Scan(&side)
	return Side(side), err
}
