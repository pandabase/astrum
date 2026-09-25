package ledger

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pandabase/astrum/internal/kernel/db"
)

func (s *service) createLedger(ctx context.Context, in CreateLedgerInput) (Ledger, error) {
	op := s.begin(ctx, "create ledger", "name", in.Name)

	if err := validateDetails(in.Name, in.Description, in.Metadata, true); err != nil {
		return Ledger{}, op.fail(err)
	}
	id, err := uuid.NewV7()
	if err != nil {
		return Ledger{}, op.fail(err)
	}
	ledger, err := insertLedger(ctx, s.pool, id, in)
	if err != nil {
		return Ledger{}, op.fail(err)
	}
	op.info("ledger created", "ledger_id", ledger.ID)
	return ledger, nil
}

func (s *service) ledger(ctx context.Context, id uuid.UUID) (Ledger, error) {
	op := s.begin(ctx, "get ledger", "ledger_id", id)

	ledger, err := queryLedger(ctx, s.pool, `id = $1`, id)
	if err != nil {
		return Ledger{}, op.fail(err)
	}
	return ledger, nil
}

func (s *service) listLedgers(ctx context.Context, in ListLedgersInput) ([]Ledger, error) {
	op := s.begin(ctx, "list ledgers", "limit", in.Limit)

	if in.Limit < 1 || in.Limit > maxListLimit {
		return nil, op.fail(fmt.Errorf("%w: limit must be 1-%d", ErrInvalid, maxListLimit))
	}
	if err := validateMetadataFilter(in.Metadata); err != nil {
		return nil, op.fail(err)
	}
	ledgers, err := selectLedgers(ctx, s.pool, in)
	if err != nil {
		return nil, op.fail(err)
	}
	return ledgers, nil
}

func (s *service) updateLedger(ctx context.Context, id uuid.UUID, in UpdateInput) (Ledger, error) {
	op := s.begin(ctx, "update ledger", "ledger_id", id)

	var (
		ledger  Ledger
		changed bool
	)
	err := db.RunTx(ctx, s.pool, func(tx pgx.Tx) error {
		current, err := queryLedger(ctx, tx, `id = $1 FOR UPDATE`, id)
		if err != nil {
			return err
		}
		next := current
		next.Name, next.Description, next.Metadata, err = applyUpdate(current.Name, current.Description, current.Metadata, in, true)
		if err != nil {
			return err
		}
		changed = !sameDetails(current.Name, current.Description, current.Metadata, next.Name, next.Description, next.Metadata)
		if !changed {
			ledger = current
			return nil
		}
		ledger, err = updateLedger(ctx, tx, next)
		return err
	})
	if err != nil {
		return Ledger{}, op.fail(err)
	}
	op.info("ledger updated", "changed", changed, "version", ledger.Version)
	return ledger, nil
}

func applyUpdate(name, description string, metadata jsontext.Value, in UpdateInput, nameRequired bool) (string, string, jsontext.Value, error) {
	if in.Name != nil {
		name = *in.Name
	}
	if in.Description != nil {
		description = *in.Description
	}
	switch trimmed := bytes.TrimSpace(in.Metadata); {
	case bytes.Equal(trimmed, []byte("null")):
		metadata = jsontext.Value(`{}`)
	case len(trimmed) > 0:
		patch, err := decodeObject(in.Metadata)
		if err != nil {
			return "", "", nil, fmt.Errorf("%w: metadata must be a JSON object", ErrInvalid)
		}
		target, err := decodeObject(metadata)
		if err != nil {
			return "", "", nil, err
		}
		if metadata, err = json.Marshal(mergePatch(target, patch), json.Deterministic(true)); err != nil {
			return "", "", nil, err
		}
	}
	if err := validateDetails(name, description, metadata, nameRequired); err != nil {
		return "", "", nil, err
	}
	return name, description, normalizeMetadata(metadata), nil
}

func sameDetails(name, description string, metadata jsontext.Value, name2, description2 string, metadata2 jsontext.Value) bool {
	return name == name2 && description == description2 && jsonEqual(metadata, metadata2)
}

func mergePatch(target, patch map[string]any) map[string]any {
	if target == nil {
		target = map[string]any{}
	}
	for k, v := range patch {
		switch v := v.(type) {
		case nil:
			delete(target, k)
		case map[string]any:
			existing, _ := target[k].(map[string]any)
			target[k] = mergePatch(existing, v)
		default:
			target[k] = v
		}
	}
	return target
}

func decodeObject(raw jsontext.Value) (map[string]any, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return map[string]any{}, nil
	}
	v, err := decodeJSON(raw)
	obj, ok := v.(map[string]any)
	if err != nil || !ok {
		return nil, fmt.Errorf("%w: expected a JSON object", ErrInvalid)
	}
	return obj, nil
}

func (s *service) closePeriod(ctx context.Context, id uuid.UUID, closedBefore *time.Time) (Ledger, error) {
	op := s.begin(ctx, "close period", "ledger_id", id)

	if closedBefore != nil {
		at := closedBefore.Truncate(time.Microsecond)
		if !representableTime(at) {
			return Ledger{}, op.fail(fmt.Errorf("%w: closed_before is out of range", ErrInvalid))
		}
		closedBefore = &at
	}
	var (
		ledger Ledger
		xid    string
	)
	err := db.RunTx(ctx, s.pool, func(tx pgx.Tx) error {
		current, err := queryLedger(ctx, tx, `id = $1 FOR UPDATE`, id)
		if err != nil {
			return err
		}
		var future bool
		if err := tx.QueryRow(ctx, `SELECT coalesce($1::timestamptz > now(), false), pg_current_xact_id()::text`, closedBefore).Scan(&future, &xid); err != nil {
			return err
		}
		if future {
			return fmt.Errorf("%w: closed_before cannot be in the future", ErrInvalid)
		}
		if sameTime(current.ClosedBefore, closedBefore) {
			ledger = current
			return nil
		}
		ledger, err = scanLedger(tx.QueryRow(ctx, `
			UPDATE ledger_ledgers SET closed_before = $2, version = version + 1
			WHERE id = $1
			RETURNING `+ledgerColumns, id, closedBefore))
		return err
	})
	if err == nil {
		err = waitForOlderTransactions(ctx, s.pool, xid)
	}
	if err != nil {
		return Ledger{}, op.fail(err)
	}
	op.info("period closed", "closed_before", closedBefore, "version", ledger.Version)
	return ledger, nil
}

func sameTime(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Equal(*b)
}

func waitForOlderTransactions(ctx context.Context, q querier, xid string) error {
	for {
		var done bool
		if err := q.QueryRow(ctx, `SELECT pg_snapshot_xmin(pg_current_snapshot()) > $1::xid8`, xid).Scan(&done); err != nil {
			return fmt.Errorf("wait for in-flight transactions: %w", err)
		}
		if done {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("wait for in-flight transactions: %w", ctx.Err())
		case <-time.After(10 * time.Millisecond):
		}
	}
}
