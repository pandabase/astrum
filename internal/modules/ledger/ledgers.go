package ledger

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pandabase/astrum/internal/kernel/db"
	"github.com/pandabase/astrum/internal/kernel/logger"
)

func (s *service) createLedger(ctx context.Context, in CreateLedgerInput) (Ledger, error) {
	l := logger.For(ctx, s.log).With("op", "create_ledger", "name", in.Name)
	start := time.Now()

	if err := validateDetails(in.Name, in.Description, in.Metadata, true); err != nil {
		return Ledger{}, s.fail(l, "create ledger", err, start)
	}
	id, err := uuid.NewV7()
	if err != nil {
		return Ledger{}, s.fail(l, "create ledger", err, start)
	}
	ledger, err := insertLedger(ctx, s.pool, id, in)
	if err != nil {
		return Ledger{}, s.fail(l, "create ledger", err, start)
	}
	l.Info("ledger created", "ledger_id", ledger.ID, "duration", time.Since(start))
	return ledger, nil
}

func (s *service) ledger(ctx context.Context, id uuid.UUID) (Ledger, error) {
	l := logger.For(ctx, s.log).With("op", "get_ledger", "ledger_id", id)
	start := time.Now()

	ledger, err := queryLedger(ctx, s.pool, `id = $1`, id)
	if err != nil {
		return Ledger{}, s.fail(l, "get ledger", err, start)
	}
	return ledger, nil
}

func (s *service) listLedgers(ctx context.Context, in ListLedgersInput) ([]Ledger, error) {
	l := logger.For(ctx, s.log).With("op", "list_ledgers", "limit", in.Limit)
	start := time.Now()

	if in.Limit < 1 || in.Limit > maxListLimit {
		return nil, s.fail(l, "list ledgers", fmt.Errorf("%w: limit must be 1-%d", ErrInvalid, maxListLimit), start)
	}
	if err := validateMetadataFilter(in.Metadata); err != nil {
		return nil, s.fail(l, "list ledgers", err, start)
	}
	ledgers, err := selectLedgers(ctx, s.pool, in)
	if err != nil {
		return nil, s.fail(l, "list ledgers", err, start)
	}
	return ledgers, nil
}

func (s *service) updateLedger(ctx context.Context, id uuid.UUID, in UpdateInput) (Ledger, error) {
	l := logger.For(ctx, s.log).With("op", "update_ledger", "ledger_id", id)
	start := time.Now()

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
		if next.Name, next.Description, next.Metadata, err = applyUpdate(current.Name, current.Description, current.Metadata, in, true); err != nil {
			return err
		}
		if changed = !sameDetails(current.Name, current.Description, current.Metadata, next.Name, next.Description, next.Metadata); !changed {
			ledger = current
			return nil
		}
		ledger, err = updateLedger(ctx, tx, next)
		return err
	})
	if err != nil {
		return Ledger{}, s.fail(l, "update ledger", err, start)
	}
	l.Info("ledger updated", "changed", changed, "version", ledger.Version, "duration", time.Since(start))
	return ledger, nil
}

func applyUpdate(name, description string, metadata json.RawMessage, in UpdateInput, nameRequired bool) (string, string, json.RawMessage, error) {
	if in.Name != nil {
		name = *in.Name
	}
	if in.Description != nil {
		description = *in.Description
	}
	switch trimmed := bytes.TrimSpace(in.Metadata); {
	case bytes.Equal(trimmed, []byte("null")):
		metadata = json.RawMessage(`{}`)
	case len(trimmed) > 0:
		patch, err := decodeObject(in.Metadata)
		if err != nil {
			return "", "", nil, fmt.Errorf("%w: metadata must be a JSON object", ErrInvalid)
		}
		target, err := decodeObject(metadata)
		if err != nil {
			return "", "", nil, err
		}
		if metadata, err = json.Marshal(mergePatch(target, patch)); err != nil {
			return "", "", nil, err
		}
	}
	if err := validateDetails(name, description, metadata, nameRequired); err != nil {
		return "", "", nil, err
	}
	return name, description, normalizeMetadata(metadata), nil
}

func sameDetails(name, description string, metadata json.RawMessage, name2, description2 string, metadata2 json.RawMessage) bool {
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

func decodeObject(raw json.RawMessage) (map[string]any, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return map[string]any{}, nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var obj map[string]any
	if err := dec.Decode(&obj); err != nil || obj == nil {
		return nil, fmt.Errorf("%w: expected a JSON object", ErrInvalid)
	}
	return obj, nil
}
