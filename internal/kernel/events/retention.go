package events

import (
	"context"
	"encoding/binary"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func (s *Service) Prune(ctx context.Context) (int, error) {
	cutoff := idAt(time.Now().Add(-s.cfg.Retention))
	total := 0
	for {
		var n int
		err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
			rows, err := tx.Query(ctx, `
				SELECT e.id
				FROM events AS e, event_dispatch_cursor AS c
				WHERE e.id < $1
				  AND c.last_xid IS NOT NULL
				  AND (e.created_xid, e.id) <= (c.last_xid, c.last_id)
				  AND NOT EXISTS (SELECT 1 FROM webhook_deliveries AS d WHERE d.event_id = e.id AND d.status = 'pending')
				ORDER BY e.id
				LIMIT $2
				FOR UPDATE OF e SKIP LOCKED`, cutoff, s.cfg.PruneBatch)
			if err != nil {
				return err
			}
			ids, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
			if err != nil || len(ids) == 0 {
				return err
			}
			if _, err := tx.Exec(ctx, `SET LOCAL astrum.prune_events = 'on'`); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `DELETE FROM webhook_deliveries WHERE event_id = ANY($1)`, ids); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `DELETE FROM events WHERE id = ANY($1)`, ids); err != nil {
				return err
			}
			n = len(ids)
			return nil
		})
		if err != nil {
			return total, err
		}
		total += n
		if n < s.cfg.PruneBatch {
			if total > 0 {
				s.log.Info("events pruned", "count", total, "older_than", s.cfg.Retention)
			}
			return total, nil
		}
	}
}

func idAt(t time.Time) uuid.UUID {
	var id uuid.UUID
	var ms [8]byte
	binary.BigEndian.PutUint64(ms[:], uint64(t.UnixMilli()))
	copy(id[:6], ms[2:])
	id[6] = 0x70
	id[8] = 0x80
	return id
}
