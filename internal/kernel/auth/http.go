package auth

import (
	"encoding/base64"
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/pandabase/astrum/internal/kernel/httpx"
	"github.com/pandabase/astrum/internal/kernel/typeid"
)

func (s *Service) Routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/me", s.handleMe)
	mux.HandleFunc("POST /v1/api_keys", s.handleCreate)
	mux.HandleFunc("GET /v1/api_keys", s.handleList)
	mux.HandleFunc("GET /v1/api_keys/{id}", s.handleGet)
	mux.HandleFunc("POST /v1/api_keys/{id}/revoke", s.handleRevoke)
}

type keyPrefix struct{}

func (keyPrefix) Prefix() string { return idPrefix }

type keyID = typeid.ID[keyPrefix]

type keyResource struct {
	Object     string     `json:"object"`
	ID         keyID      `json:"id"`
	Name       string     `json:"name"`
	Role       Role       `json:"role"`
	Hint       string     `json:"hint"`
	CreatedBy  *keyID     `json:"created_by"`
	CreatedAt  time.Time  `json:"created_at"`
	ExpiresAt  *time.Time `json:"expires_at"`
	RevokedAt  *time.Time `json:"revoked_at"`
	LastUsedAt *time.Time `json:"last_used_at"`

	Secret string `json:"secret,omitzero"`
}

func toKey(k Key) keyResource {
	return keyResource{
		Object:     "api_key",
		ID:         keyID(k.ID),
		Name:       k.Name,
		Role:       k.Role,
		Hint:       "…" + k.Hint,
		CreatedBy:  (*keyID)(k.CreatedBy),
		CreatedAt:  k.CreatedAt,
		ExpiresAt:  k.ExpiresAt,
		RevokedAt:  k.RevokedAt,
		LastUsedAt: k.LastUsedAt,
	}
}

type createRequest struct {
	Name      string     `json:"name"`
	Role      Role       `json:"role"`
	ExpiresAt *time.Time `json:"expires_at"`
}

func (s *Service) handleCreate(w http.ResponseWriter, r *http.Request) {
	var in createRequest
	if err := httpx.Decode(w, r, &in); err != nil {
		httpx.Error(w, r, http.StatusBadRequest, httpx.CodeInvalidRequest, err.Error())
		return
	}
	var creator *uuid.UUID
	if k, ok := FromContext(r.Context()); ok {
		creator = &k.ID
	}
	k, token, err := s.Create(r.Context(), CreateInput{Name: in.Name, Role: in.Role, ExpiresAt: in.ExpiresAt, CreatedBy: creator})
	if err != nil {
		writeError(w, r, err)
		return
	}
	out := toKey(k)
	out.Secret = token
	httpx.JSON(w, r, http.StatusCreated, out)
}

func (s *Service) handleList(w http.ResponseWriter, r *http.Request) {
	limit, err := httpx.PageLimit(r)
	if err != nil {
		httpx.Error(w, r, http.StatusBadRequest, httpx.CodeInvalidRequest, err.Error())
		return
	}
	var before uuid.UUID
	if raw := r.URL.Query().Get("cursor"); raw != "" {
		b, err := base64.RawURLEncoding.DecodeString(raw)
		id, idErr := uuid.FromBytes(b)
		if err != nil || idErr != nil {
			httpx.Error(w, r, http.StatusBadRequest, httpx.CodeInvalidRequest, "cursor is invalid")
			return
		}
		before = id
	}
	keys, err := s.List(r.Context(), before, limit+1)
	if err != nil {
		writeError(w, r, err)
		return
	}
	list := httpx.NewList(keys, limit, func(k Key) string { return base64.RawURLEncoding.EncodeToString(k.ID[:]) })
	out := make([]keyResource, len(list.Data))
	for i, k := range list.Data {
		out[i] = toKey(k)
	}
	httpx.JSON(w, r, http.StatusOK, httpx.List[keyResource]{Object: list.Object, Data: out, HasMore: list.HasMore, NextCursor: list.NextCursor})
}

func (s *Service) handleMe(w http.ResponseWriter, r *http.Request) {
	k, ok := FromContext(r.Context())
	if !ok {
		httpx.Error(w, r, http.StatusUnauthorized, "unauthorized", "send an API key as Authorization: Bearer sk_...")
		return
	}
	httpx.JSON(w, r, http.StatusOK, toKey(k))
}

func (s *Service) handleGet(w http.ResponseWriter, r *http.Request) {
	s.withID(w, r, func(id uuid.UUID) {
		k, err := s.Key(r.Context(), id)
		if err != nil {
			writeError(w, r, err)
			return
		}
		httpx.JSON(w, r, http.StatusOK, toKey(k))
	})
}

func (s *Service) handleRevoke(w http.ResponseWriter, r *http.Request) {
	s.withID(w, r, func(id uuid.UUID) {
		k, err := s.Revoke(r.Context(), id)
		if err != nil {
			writeError(w, r, err)
			return
		}
		httpx.JSON(w, r, http.StatusOK, toKey(k))
	})
}

func (s *Service) withID(w http.ResponseWriter, r *http.Request, fn func(uuid.UUID)) {
	var id keyID
	if err := id.UnmarshalText([]byte(r.PathValue("id"))); err != nil {
		httpx.Error(w, r, http.StatusBadRequest, httpx.CodeInvalidRequest, err.Error())
		return
	}
	fn(id.UUID())
}

func writeError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, ErrNotFound):
		httpx.Error(w, r, http.StatusNotFound, httpx.CodeNotFound, err.Error())
	case errors.Is(err, ErrInvalid):
		httpx.Error(w, r, http.StatusUnprocessableEntity, "validation_error", err.Error())
	default:
		httpx.Error(w, r, http.StatusInternalServerError, httpx.CodeInternal, "")
	}
}
