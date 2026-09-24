package events

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/pandabase/astrum/internal/kernel/httpx"
	"github.com/pandabase/astrum/internal/kernel/typeid"
)

func (s *Service) Routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/events", s.handleListEvents)
	mux.HandleFunc("GET /v1/events/{id}", s.handleGetEvent)

	mux.HandleFunc("POST /v1/webhook_endpoints", s.handleCreateEndpoint)
	mux.HandleFunc("GET /v1/webhook_endpoints", s.handleListEndpoints)
	mux.HandleFunc("GET /v1/webhook_endpoints/{id}", s.handleGetEndpoint)
	mux.HandleFunc("PATCH /v1/webhook_endpoints/{id}", s.handleUpdateEndpoint)
	mux.HandleFunc("DELETE /v1/webhook_endpoints/{id}", s.handleDeleteEndpoint)

	mux.HandleFunc("GET /v1/webhook_deliveries", s.handleListDeliveries)
	mux.HandleFunc("GET /v1/webhook_deliveries/{id}", s.handleGetDelivery)
	mux.HandleFunc("POST /v1/webhook_deliveries/{id}/retry", s.handleRetryDelivery)
}

type endpointResource struct {
	Object      string     `json:"object"`
	ID          endpointID `json:"id"`
	URL         string     `json:"url"`
	Description string     `json:"description"`
	EventTypes  []string   `json:"event_types"`
	Enabled     bool       `json:"enabled"`
	Version     int64      `json:"version"`
	CreatedAt   time.Time  `json:"created_at"`

	Secret string `json:"secret,omitzero"`
}

func toEndpoint(e Endpoint) endpointResource {
	return endpointResource{
		Object:      "webhook_endpoint",
		ID:          endpointID(e.ID),
		URL:         e.URL,
		Description: e.Description,
		EventTypes:  e.EventTypes,
		Enabled:     e.Enabled,
		Version:     e.Version,
		CreatedAt:   e.CreatedAt,
		Secret:      e.Secret,
	}
}

type deliveryResource struct {
	Object         string     `json:"object"`
	ID             deliveryID `json:"id"`
	EndpointID     endpointID `json:"endpoint_id"`
	EventID        eventID    `json:"event_id"`
	EventType      string     `json:"event_type"`
	Status         string     `json:"status"`
	Attempts       int        `json:"attempts"`
	NextAttemptAt  *time.Time `json:"next_attempt_at"`
	LastAttemptAt  *time.Time `json:"last_attempt_at"`
	LastStatusCode *int       `json:"last_status_code"`
	LastError      *string    `json:"last_error"`
	DeliveredAt    *time.Time `json:"delivered_at"`
	CreatedAt      time.Time  `json:"created_at"`
}

func toDelivery(d Delivery) deliveryResource {
	return deliveryResource{
		Object:         "webhook_delivery",
		ID:             deliveryID(d.ID),
		EndpointID:     endpointID(d.EndpointID),
		EventID:        eventID(d.EventID),
		EventType:      d.EventType,
		Status:         d.Status,
		Attempts:       d.Attempts,
		NextAttemptAt:  d.NextAttemptAt,
		LastAttemptAt:  d.LastAttemptAt,
		LastStatusCode: d.LastStatusCode,
		LastError:      d.LastError,
		DeliveredAt:    d.DeliveredAt,
		CreatedAt:      d.CreatedAt,
	}
}

type endpointRequest struct {
	URL         *string   `json:"url"`
	Description *string   `json:"description"`
	EventTypes  *[]string `json:"event_types"`
	Enabled     *bool     `json:"enabled"`
}

func (s *Service) handleListEvents(w http.ResponseWriter, r *http.Request) {
	limit, before, ok := page(w, r)
	if !ok {
		return
	}
	evs, err := s.ListEvents(r.Context(), ListEventsInput{Type: r.URL.Query().Get("type"), Before: before, Limit: limit + 1})
	respondList(w, r, evs, limit, render, func(e Event) uuid.UUID { return e.ID }, err)
}

func (s *Service) handleGetEvent(w http.ResponseWriter, r *http.Request) {
	withID[eventPrefix](w, r, func(id uuid.UUID) {
		e, err := s.Event(r.Context(), id)
		respond(w, r, http.StatusOK, e, render, err)
	})
}

func (s *Service) handleCreateEndpoint(w http.ResponseWriter, r *http.Request) {
	var in endpointRequest
	if err := httpx.Decode(w, r, &in); err != nil {
		httpx.Error(w, r, http.StatusBadRequest, httpx.CodeInvalidRequest, err.Error())
		return
	}
	create := EndpointInput{Enabled: in.Enabled}
	if in.URL != nil {
		create.URL = *in.URL
	}
	if in.Description != nil {
		create.Description = *in.Description
	}
	if in.EventTypes != nil {
		create.EventTypes = *in.EventTypes
	}
	e, err := s.CreateEndpoint(r.Context(), create)
	w.Header().Set("Cache-Control", "no-store")
	respond(w, r, http.StatusCreated, e, toEndpoint, err)
}

func (s *Service) handleListEndpoints(w http.ResponseWriter, r *http.Request) {
	limit, before, ok := page(w, r)
	if !ok {
		return
	}
	eps, err := s.ListEndpoints(r.Context(), before, limit+1)
	respondList(w, r, eps, limit, toEndpoint, func(e Endpoint) uuid.UUID { return e.ID }, err)
}

func (s *Service) handleGetEndpoint(w http.ResponseWriter, r *http.Request) {
	withID[endpointPrefix](w, r, func(id uuid.UUID) {
		e, err := s.Endpoint(r.Context(), id)
		respond(w, r, http.StatusOK, e, toEndpoint, err)
	})
}

func (s *Service) handleUpdateEndpoint(w http.ResponseWriter, r *http.Request) {
	withID[endpointPrefix](w, r, func(id uuid.UUID) {
		var in endpointRequest
		if err := httpx.Decode(w, r, &in); err != nil {
			httpx.Error(w, r, http.StatusBadRequest, httpx.CodeInvalidRequest, err.Error())
			return
		}
		e, err := s.UpdateEndpoint(r.Context(), id, EndpointUpdate(in))
		respond(w, r, http.StatusOK, e, toEndpoint, err)
	})
}

func (s *Service) handleDeleteEndpoint(w http.ResponseWriter, r *http.Request) {
	withID[endpointPrefix](w, r, func(id uuid.UUID) {
		err := s.DeleteEndpoint(r.Context(), id)
		respond(w, r, http.StatusOK, id, func(id uuid.UUID) any {
			return map[string]any{"object": "webhook_endpoint", "id": endpointID(id), "deleted": true}
		}, err)
	})
}

func (s *Service) handleListDeliveries(w http.ResponseWriter, r *http.Request) {
	limit, before, ok := page(w, r)
	if !ok {
		return
	}
	in := ListDeliveriesInput{Status: r.URL.Query().Get("status"), Before: before, Limit: limit + 1}
	if in.EndpointID, ok = queryID[endpointPrefix](w, r, "endpoint_id"); !ok {
		return
	}
	if in.EventID, ok = queryID[eventPrefix](w, r, "event_id"); !ok {
		return
	}
	ds, err := s.ListDeliveries(r.Context(), in)
	respondList(w, r, ds, limit, toDelivery, func(d Delivery) uuid.UUID { return d.ID }, err)
}

func (s *Service) handleGetDelivery(w http.ResponseWriter, r *http.Request) {
	withID[deliveryPrefix](w, r, func(id uuid.UUID) {
		d, err := s.Delivery(r.Context(), id)
		respond(w, r, http.StatusOK, d, toDelivery, err)
	})
}

func (s *Service) handleRetryDelivery(w http.ResponseWriter, r *http.Request) {
	withID[deliveryPrefix](w, r, func(id uuid.UUID) {
		d, err := s.RetryDelivery(r.Context(), id)
		respond(w, r, http.StatusOK, d, toDelivery, err)
	})
}

func withID[P typeid.Prefix](w http.ResponseWriter, r *http.Request, fn func(uuid.UUID)) {
	var id typeid.ID[P]
	if err := id.UnmarshalText([]byte(r.PathValue("id"))); err != nil {
		httpx.Error(w, r, http.StatusBadRequest, httpx.CodeInvalidRequest, err.Error())
		return
	}
	fn(id.UUID())
}

func queryID[P typeid.Prefix](w http.ResponseWriter, r *http.Request, name string) (uuid.UUID, bool) {
	raw := r.URL.Query().Get(name)
	if raw == "" {
		return uuid.Nil, true
	}
	var id typeid.ID[P]
	if err := id.UnmarshalText([]byte(raw)); err != nil {
		httpx.Error(w, r, http.StatusBadRequest, httpx.CodeInvalidRequest, name+": "+err.Error())
		return uuid.Nil, false
	}
	return id.UUID(), true
}

func page(w http.ResponseWriter, r *http.Request) (int, uuid.UUID, bool) {
	limit, err := httpx.PageLimit(r)
	if err != nil {
		httpx.Error(w, r, http.StatusBadRequest, httpx.CodeInvalidRequest, err.Error())
		return 0, uuid.Nil, false
	}
	raw := r.URL.Query().Get("cursor")
	if raw == "" {
		return limit, uuid.Nil, true
	}
	b, err := base64.RawURLEncoding.DecodeString(raw)
	id, idErr := uuid.FromBytes(b)
	if err != nil || idErr != nil {
		httpx.Error(w, r, http.StatusBadRequest, httpx.CodeInvalidRequest, "cursor is invalid")
		return 0, uuid.Nil, false
	}
	return limit, id, true
}

func respond[T, R any](w http.ResponseWriter, r *http.Request, status int, v T, render func(T) R, err error) {
	if err != nil {
		writeError(w, r, err)
		return
	}
	httpx.JSON(w, r, status, render(v))
}

func respondList[T, R any](w http.ResponseWriter, r *http.Request, items []T, limit int, render func(T) R, id func(T) uuid.UUID, err error) {
	if err != nil {
		writeError(w, r, err)
		return
	}
	list := httpx.NewList(items, limit, func(item T) string {
		key := id(item)
		return base64.RawURLEncoding.EncodeToString(key[:])
	})
	out := make([]R, len(list.Data))
	for i, item := range list.Data {
		out[i] = render(item)
	}
	httpx.JSON(w, r, http.StatusOK, httpx.List[R]{Object: list.Object, Data: out, HasMore: list.HasMore, NextCursor: list.NextCursor})
}

func writeError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, ErrNotFound):
		httpx.Error(w, r, http.StatusNotFound, httpx.CodeNotFound, err.Error())
	case errors.Is(err, ErrInvalid):
		httpx.Error(w, r, http.StatusUnprocessableEntity, "validation_error", err.Error())
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		httpx.Error(w, r, http.StatusGatewayTimeout, "timeout", err.Error())
	default:
		httpx.Error(w, r, http.StatusInternalServerError, httpx.CodeInternal, "")
	}
}
