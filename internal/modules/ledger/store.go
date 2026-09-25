package ledger

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"slices"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
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

func selectClock(ctx context.Context, q querier) (time.Time, error) {
	var now time.Time
	err := q.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now)
	return now, err
}

func selectSnapshotXmin(ctx context.Context, q querier) (uint64, error) {
	var xmin string
	if err := q.QueryRow(ctx, `SELECT pg_snapshot_xmin(pg_current_snapshot())::text`).Scan(&xmin); err != nil {
		return 0, err
	}
	return strconv.ParseUint(xmin, 10, 64)
}
