package events

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pandabase/astrum/internal/kernel/typeid"
)

const (
	maxURLLen         = 2048
	maxDescriptionLen = 1024
	maxEventTypes     = 64
)

var eventTypePattern = regexp.MustCompile(`^(\*|[a-z_]+(\.[a-z_]+)*(\.\*)?)$`)

type (
	eventPrefix    struct{}
	endpointPrefix struct{}
	deliveryPrefix struct{}
)

func (eventPrefix) Prefix() string    { return "evt" }
func (endpointPrefix) Prefix() string { return "we" }
func (deliveryPrefix) Prefix() string { return "wd" }

type (
	eventID    = typeid.ID[eventPrefix]
	endpointID = typeid.ID[endpointPrefix]
	deliveryID = typeid.ID[deliveryPrefix]
)

type eventResource struct {
	Object    string         `json:"object"`
	ID        eventID        `json:"id"`
	Type      string         `json:"type"`
	Data      jsontext.Value `json:"data"`
	CreatedAt time.Time      `json:"created_at"`
}

func render(e Event) eventResource {
	return eventResource{Object: "event", ID: eventID(e.ID), Type: e.Type, Data: e.Data, CreatedAt: e.CreatedAt}
}

type Endpoint struct {
	ID          uuid.UUID
	URL         string
	Description string
	EventTypes  []string
	Enabled     bool
	Version     int64
	CreatedAt   time.Time
	Secret      string
}

type EndpointInput struct {
	URL         string
	Description string
	EventTypes  []string
	Enabled     *bool
}

type EndpointUpdate struct {
	URL         *string
	Description *string
	EventTypes  *[]string
	Enabled     *bool
}

const endpointColumns = `id, url, description, event_types, enabled, version, created_at`

func scanEndpoint(row pgx.Row) (Endpoint, error) {
	var e Endpoint
	err := row.Scan(&e.ID, &e.URL, &e.Description, &e.EventTypes, &e.Enabled, &e.Version, &e.CreatedAt)
	return e, err
}

func (s *Service) CreateEndpoint(ctx context.Context, in EndpointInput) (Endpoint, error) {
	enabled := in.Enabled == nil || *in.Enabled
	if in.EventTypes == nil {
		in.EventTypes = []string{}
	}
	if err := s.validateEndpoint(in.URL, in.Description, in.EventTypes); err != nil {
		return Endpoint{}, err
	}
	id, err := uuid.NewV7()
	if err != nil {
		return Endpoint{}, err
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return Endpoint{}, err
	}
	e, err := scanEndpoint(s.pool.QueryRow(ctx, `
		INSERT INTO webhook_endpoints (id, url, secret, description, event_types, enabled)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING `+endpointColumns,
		id, in.URL, "whsec_"+base64.RawURLEncoding.EncodeToString(secret), in.Description, in.EventTypes, enabled))
	if err != nil {
		return Endpoint{}, fmt.Errorf("events: create endpoint: %w", err)
	}
	e.Secret = "whsec_" + base64.RawURLEncoding.EncodeToString(secret)
	s.log.Info("webhook endpoint created", "endpoint_id", e.ID, "url", e.URL, "event_types", e.EventTypes)
	return e, nil
}

func (s *Service) Endpoint(ctx context.Context, id uuid.UUID) (Endpoint, error) {
	return notFound(scanEndpoint(s.pool.QueryRow(ctx, `SELECT `+endpointColumns+` FROM webhook_endpoints WHERE id = $1`, id)))
}

func (s *Service) ListEndpoints(ctx context.Context, before uuid.UUID, limit int) ([]Endpoint, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT `+endpointColumns+` FROM webhook_endpoints
		WHERE ($1::uuid IS NULL OR id < $1)
		ORDER BY id DESC
		LIMIT $2`, nullUUID(before), limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Endpoint, error) { return scanEndpoint(row) })
}

func (s *Service) UpdateEndpoint(ctx context.Context, id uuid.UUID, in EndpointUpdate) (Endpoint, error) {
	var e Endpoint
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		current, err := notFound(scanEndpoint(tx.QueryRow(ctx, `SELECT `+endpointColumns+` FROM webhook_endpoints WHERE id = $1 FOR UPDATE`, id)))
		if err != nil {
			return err
		}
		next := current
		if in.URL != nil {
			next.URL = *in.URL
		}
		if in.Description != nil {
			next.Description = *in.Description
		}
		if in.EventTypes != nil {
			next.EventTypes = *in.EventTypes
		}
		if in.Enabled != nil {
			next.Enabled = *in.Enabled
		}
		if err := s.validateEndpoint(next.URL, next.Description, next.EventTypes); err != nil {
			return err
		}
		e, err = scanEndpoint(tx.QueryRow(ctx, `
			UPDATE webhook_endpoints
			SET url = $2, description = $3, event_types = $4, enabled = $5, version = version + 1
			WHERE id = $1
			RETURNING `+endpointColumns, id, next.URL, next.Description, next.EventTypes, next.Enabled))
		return err
	})
	if err != nil {
		return Endpoint{}, err
	}
	s.log.Info("webhook endpoint updated", "endpoint_id", e.ID, "enabled", e.Enabled, "version", e.Version)
	return e, nil
}

func (s *Service) DeleteEndpoint(ctx context.Context, id uuid.UUID) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM webhook_endpoints WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	s.log.Info("webhook endpoint deleted", "endpoint_id", id)
	return nil
}

func (s *Service) validateEndpoint(raw, description string, types []string) error {
	u, err := url.Parse(raw)
	switch {
	case err != nil || len(raw) > maxURLLen || u.Host == "" || u.User != nil:
		return fmt.Errorf("%w: url must be an absolute URL without credentials, up to %d characters", ErrInvalid, maxURLLen)
	case u.Scheme != "https" && !(u.Scheme == "http" && s.cfg.AllowInsecureURLs):
		return fmt.Errorf("%w: url must use https", ErrInvalid)
	case len(description) > maxDescriptionLen:
		return fmt.Errorf("%w: description exceeds %d characters", ErrInvalid, maxDescriptionLen)
	case len(types) > maxEventTypes:
		return fmt.Errorf("%w: at most %d event types", ErrInvalid, maxEventTypes)
	}
	for _, t := range types {
		if !eventTypePattern.MatchString(t) {
			return fmt.Errorf("%w: event type %q must look like transaction.posted or transaction.*", ErrInvalid, t)
		}
	}
	return nil
}

type ListEventsInput struct {
	Type   string
	Before uuid.UUID
	Limit  int
}

const eventColumns = `id, type, data, created_at`

func scanEvent(row pgx.Row) (Event, error) {
	var (
		e    Event
		data []byte
	)
	if err := row.Scan(&e.ID, &e.Type, &data, &e.CreatedAt); err != nil {
		return Event{}, err
	}
	e.Data = bytes.Clone(data)
	return e, nil
}

func (s *Service) Event(ctx context.Context, id uuid.UUID) (Event, error) {
	return notFound(scanEvent(s.pool.QueryRow(ctx, `SELECT `+eventColumns+` FROM events WHERE id = $1`, id)))
}

func (s *Service) ListEvents(ctx context.Context, in ListEventsInput) ([]Event, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT `+eventColumns+` FROM events
		WHERE ($1::uuid IS NULL OR id < $1) AND ($2::text IS NULL OR type = $2)
		ORDER BY id DESC
		LIMIT $3`, nullUUID(in.Before), nullString(in.Type), in.Limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Event, error) { return scanEvent(row) })
}

type Delivery struct {
	ID             uuid.UUID
	EndpointID     uuid.UUID
	EventID        uuid.UUID
	EventType      string
	Status         string
	Attempts       int
	NextAttemptAt  *time.Time
	LastAttemptAt  *time.Time
	LastStatusCode *int
	LastError      *string
	DeliveredAt    *time.Time
	CreatedAt      time.Time
}

type ListDeliveriesInput struct {
	EndpointID uuid.UUID
	EventID    uuid.UUID
	Status     string
	Before     uuid.UUID
	Limit      int
}

const deliveryColumns = `d.id, d.endpoint_id, d.event_id, e.type, d.status, d.attempts,
	CASE WHEN d.status = 'pending' THEN d.next_attempt_at END, d.last_attempt_at, d.last_status_code, d.last_error,
	d.delivered_at, d.created_at`

func scanDelivery(row pgx.Row) (Delivery, error) {
	var d Delivery
	err := row.Scan(&d.ID, &d.EndpointID, &d.EventID, &d.EventType, &d.Status, &d.Attempts, &d.NextAttemptAt,
		&d.LastAttemptAt, &d.LastStatusCode, &d.LastError, &d.DeliveredAt, &d.CreatedAt)
	return d, err
}

func (s *Service) Delivery(ctx context.Context, id uuid.UUID) (Delivery, error) {
	return notFound(scanDelivery(s.pool.QueryRow(ctx, `
		SELECT `+deliveryColumns+` FROM webhook_deliveries AS d JOIN events AS e ON e.id = d.event_id
		WHERE d.id = $1`, id)))
}

func (s *Service) ListDeliveries(ctx context.Context, in ListDeliveriesInput) ([]Delivery, error) {
	switch in.Status {
	case "", "pending", "succeeded", "failed":
	default:
		return nil, fmt.Errorf("%w: status must be pending, succeeded or failed", ErrInvalid)
	}
	rows, err := s.pool.Query(ctx, `
		SELECT `+deliveryColumns+` FROM webhook_deliveries AS d JOIN events AS e ON e.id = d.event_id
		WHERE ($1::uuid IS NULL OR d.id < $1)
		  AND ($2::uuid IS NULL OR d.endpoint_id = $2)
		  AND ($3::uuid IS NULL OR d.event_id = $3)
		  AND ($4::text IS NULL OR d.status = $4)
		ORDER BY d.id DESC
		LIMIT $5`, nullUUID(in.Before), nullUUID(in.EndpointID), nullUUID(in.EventID), nullString(in.Status), in.Limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Delivery, error) { return scanDelivery(row) })
}

func (s *Service) RetryDelivery(ctx context.Context, id uuid.UUID) (Delivery, error) {
	if _, err := s.pool.Exec(ctx, `
		UPDATE webhook_deliveries
		SET status = 'pending', next_attempt_at = now()
		WHERE id = $1 AND status <> 'succeeded'`, id); err != nil {
		return Delivery{}, err
	}
	d, err := s.Delivery(ctx, id)
	if err == nil {
		s.log.Info("webhook delivery retry requested", "delivery_id", id, "status", d.Status)
	}
	return d, err
}

func notFound[T any](v T, err error) (T, error) {
	if errors.Is(err, pgx.ErrNoRows) {
		return v, ErrNotFound
	}
	return v, err
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
