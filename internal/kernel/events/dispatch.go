package events

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const (
	dispatchLockID  = 7_341_902_120
	SignatureHeader = "Astrum-Signature"
	EventIDHeader   = "Astrum-Event-Id"
	maxErrorLen     = 1024
)

func (s *Service) Dispatch(ctx context.Context) (int, error) {
	var n int
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		n = 0
		var locked bool
		if err := tx.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock(hashtextextended(current_schema(), $1))`, dispatchLockID).Scan(&locked); err != nil || !locked {
			return err
		}
		var (
			lastXID *string
			lastID  *uuid.UUID
		)
		if err := tx.QueryRow(ctx, `SELECT last_xid::text, last_id FROM event_dispatch_cursor FOR UPDATE`).Scan(&lastXID, &lastID); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `
			SELECT id, type, created_xid::text
			FROM events
			WHERE created_xid < pg_snapshot_xmin(pg_current_snapshot())
			  AND ($1::xid8 IS NULL OR (created_xid, id) > ($1::xid8, $2::uuid))
			ORDER BY created_xid, id
			LIMIT $3`, lastXID, lastID, s.cfg.BatchSize)
		if err != nil {
			return err
		}
		type pending struct {
			id  uuid.UUID
			typ string
			xid string
		}
		var batch []pending
		var p pending
		if _, err := pgx.ForEachRow(rows, []any{&p.id, &p.typ, &p.xid}, func() error {
			batch = append(batch, p)
			return nil
		}); err != nil || len(batch) == 0 {
			return err
		}

		endpoints, err := enabledEndpoints(ctx, tx)
		if err != nil {
			return err
		}
		var ids, endpointIDs, eventIDs []uuid.UUID
		for _, ev := range batch {
			for _, ep := range endpoints {
				if !subscribed(ep.EventTypes, ev.typ) {
					continue
				}
				id, err := uuid.NewV7()
				if err != nil {
					return err
				}
				ids, endpointIDs, eventIDs = append(ids, id), append(endpointIDs, ep.ID), append(eventIDs, ev.id)
			}
		}
		if len(ids) > 0 {
			if _, err := tx.Exec(ctx, `
				INSERT INTO webhook_deliveries (id, endpoint_id, event_id)
				SELECT * FROM unnest($1::uuid[], $2::uuid[], $3::uuid[])
				ON CONFLICT DO NOTHING`, ids, endpointIDs, eventIDs); err != nil {
				return err
			}
		}
		last := batch[len(batch)-1]
		if _, err := tx.Exec(ctx, `UPDATE event_dispatch_cursor SET last_xid = $1::xid8, last_id = $2`, last.xid, last.id); err != nil {
			return err
		}
		n = len(batch)
		return nil
	})
	if err == nil && n > 0 {
		s.log.Debug("events dispatched", "count", n)
	}
	return n, err
}

func enabledEndpoints(ctx context.Context, q pgx.Tx) ([]Endpoint, error) {
	rows, err := q.Query(ctx, `SELECT `+endpointColumns+` FROM webhook_endpoints WHERE enabled`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Endpoint, error) { return scanEndpoint(row) })
}

func subscribed(types []string, eventType string) bool {
	if len(types) == 0 {
		return true
	}
	for _, t := range types {
		if prefix, ok := strings.CutSuffix(t, "*"); ok && strings.HasPrefix(eventType, prefix) {
			return true
		}
		if t == eventType {
			return true
		}
	}
	return false
}

type claimed struct {
	id       uuid.UUID
	attempts int
	url      string
	secret   string
	event    Event
}

func (s *Service) claim(ctx context.Context) ([]claimed, error) {
	rows, err := s.pool.Query(ctx, `
		WITH due AS (
			SELECT d.id
			FROM webhook_deliveries AS d
			JOIN webhook_endpoints AS w ON w.id = d.endpoint_id
			WHERE d.status = 'pending' AND d.next_attempt_at <= now() AND w.enabled
			ORDER BY d.next_attempt_at
			LIMIT $1
			FOR UPDATE OF d SKIP LOCKED
		), leased AS (
			UPDATE webhook_deliveries AS d
			SET next_attempt_at = now() + make_interval(secs => $2)
			FROM due
			WHERE d.id = due.id
			RETURNING d.id, d.attempts, d.endpoint_id, d.event_id
		)
		SELECT l.id, l.attempts, w.url, w.secret, e.id, e.type, e.data, e.created_at
		FROM leased AS l
		JOIN webhook_endpoints AS w ON w.id = l.endpoint_id
		JOIN events AS e ON e.id = l.event_id`, s.cfg.BatchSize, s.cfg.Lease.Seconds())

	if err != nil {
		return nil, fmt.Errorf("claim deliveries: %w", err)
	}

	var (
		out  []claimed
		c    claimed
		data []byte
	)
	_, err = pgx.ForEachRow(rows, []any{&c.id, &c.attempts, &c.url, &c.secret, &c.event.ID, &c.event.Type, &data, &c.event.CreatedAt}, func() error {
		c.event.Data = bytes.Clone(data)
		out = append(out, c)
		return nil
	})

	if err != nil {
		return nil, fmt.Errorf("claim deliveries: %w", err)
	}

	return out, nil
}

func (s *Service) send(ctx context.Context, d claimed) {
	l := s.log.With("delivery_id", d.id, "event_id", d.event.ID, "type", d.event.Type, "attempt", d.attempts+1)
	start := time.Now()

	status, sendErr := s.post(ctx, d)
	attempts := d.attempts + 1

	var err error

	switch {
	case sendErr == nil:
		_, err = s.pool.Exec(ctx, `
			UPDATE webhook_deliveries
			SET status = 'succeeded', attempts = $2, last_attempt_at = now(), last_status_code = $3, last_error = NULL,
				delivered_at = now()
			WHERE id = $1`, d.id, attempts, status)
		l.Info("webhook delivered", "status", status, "duration", time.Since(start))
	case attempts > len(s.cfg.Retries):
		_, err = s.pool.Exec(ctx, `
			UPDATE webhook_deliveries
			SET status = 'failed', attempts = $2, last_attempt_at = now(), last_status_code = $3, last_error = $4
			WHERE id = $1`, d.id, attempts, nullStatus(status), truncate(sendErr.Error()))
		l.Error("webhook failed for good", "status", status, "err", sendErr)
	default:
		_, err = s.pool.Exec(ctx, `
			UPDATE webhook_deliveries
			SET attempts = $2, last_attempt_at = now(), last_status_code = $3, last_error = $4,
				next_attempt_at = now() + make_interval(secs => $5)
			WHERE id = $1`, d.id, attempts, nullStatus(status), truncate(sendErr.Error()), s.cfg.Retries[attempts-1].Seconds())
		l.Warn("webhook will be retried", "status", status, "err", sendErr, "in", s.cfg.Retries[attempts-1])
	}

	if err != nil {
		l.Error("record delivery outcome failed", "err", err)
	}
}

func (s *Service) post(ctx context.Context, d claimed) (int, error) {
	body, err := json.Marshal(render(d.event), json.Deterministic(true))

	if err != nil {
		return 0, err
	}

	ctx, cancel := context.WithTimeout(ctx, s.cfg.Timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.url, bytes.NewReader(body))

	if err != nil {
		return 0, err
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Astrum-Webhooks/1")
	req.Header.Set(EventIDHeader, eventID(d.event.ID).String())
	req.Header.Set(SignatureHeader, Sign(d.secret, time.Now(), body))

	resp, err := s.cfg.Client.Do(req)
	if err != nil {
		return 0, err
	}

	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return resp.StatusCode, fmt.Errorf("endpoint responded %d", resp.StatusCode)
	}

	return resp.StatusCode, nil
}

func Sign(secret string, at time.Time, body []byte) string {
	ts := strconv.FormatInt(at.Unix(), 10)

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(ts))
	mac.Write([]byte("."))
	mac.Write(body)

	return "t=" + ts + ",v1=" + hex.EncodeToString(mac.Sum(nil))
}

func Verify(secret, header string, body []byte, now time.Time, tolerance time.Duration) bool {
	var ts, sig string

	for part := range strings.SplitSeq(header, ",") {
		k, v, _ := strings.Cut(part, "=")

		switch k {
		case "t":
			ts = v
		case "v1":
			sig = v
		}
	}

	unix, err := strconv.ParseInt(ts, 10, 64)
	if err != nil || now.Sub(time.Unix(unix, 0)).Abs() > tolerance {
		return false
	}

	want := Sign(secret, time.Unix(unix, 0), body)

	return hmac.Equal([]byte(want), []byte("t="+ts+",v1="+sig))
}

func nullStatus(status int) *int {
	if status == 0 {
		return nil
	}

	return &status
}

func truncate(s string) string {
	if len(s) > maxErrorLen {
		s = s[:maxErrorLen]
	}

	return strings.ToValidUTF8(s, "")
}
