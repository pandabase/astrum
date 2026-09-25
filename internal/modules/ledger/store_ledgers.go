package ledger

import (
	"bytes"
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pandabase/astrum/internal/money"
)

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
