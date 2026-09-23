package httpx

import (
	"bytes"
	"encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/charmbracelet/log"
	"github.com/google/uuid"
	"github.com/pandabase/astrum/internal/kernel/logger"
)

const (
	maxBodyBytes     = 1 << 20
	requestIDHeader  = "X-Request-ID"
	MaxBulkBodyBytes = 16 << 20
)

const (
	CodeInvalidRequest   = "invalid_request"
	CodeNotFound         = "not_found"
	CodeRequestTooLarge  = "request_too_large"
	CodeInternal         = "internal_error"
	CodeUnavailable      = "service_unavailable"
	CodeIdempotencyReuse = "idempotency_key_reused"
	CodeIdempotencyBusy  = "idempotency_key_in_use"
)

const (
	DefaultPageLimit = 25
	MaxPageLimit     = 100
)

func Decode(w http.ResponseWriter, r *http.Request, v any) error {
	return decode(w, r, v, false, maxBodyBytes)
}

func DecodeOptional(w http.ResponseWriter, r *http.Request, v any) error {
	return decode(w, r, v, true, maxBodyBytes)
}

func DecodeBulk(w http.ResponseWriter, r *http.Request, v any) error {
	return decode(w, r, v, false, MaxBulkBodyBytes)
}

func decode(w http.ResponseWriter, r *http.Request, v any, optional bool, limit int64) error {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
	if err != nil {
		return fmt.Errorf("invalid request body: %w", err)
	}
	if optional && len(bytes.TrimSpace(body)) == 0 {
		return nil
	}
	if err := json.Unmarshal(body, v, json.RejectUnknownMembers(true)); err != nil {
		return fmt.Errorf("invalid request body: %w", err)
	}
	return nil
}

func JSON(w http.ResponseWriter, r *http.Request, status int, v any) {
	write(w, r, status, "application/json", v)
}

type Problem struct {
	Type      string `json:"type"`
	Title     string `json:"title"`
	Status    int    `json:"status"`
	Code      string `json:"code"`
	Detail    string `json:"detail,omitzero"`
	RequestID string `json:"request_id,omitzero"`
}

func NewProblem(r *http.Request, status int, code, detail string) Problem {
	return Problem{
		Type:      "urn:astrum:error:" + code,
		Title:     http.StatusText(status),
		Status:    status,
		Code:      code,
		Detail:    detail,
		RequestID: logger.RequestID(r.Context()),
	}
}

func Error(w http.ResponseWriter, r *http.Request, status int, code, detail string) {
	write(w, r, status, "application/problem+json", NewProblem(r, status, code, detail))
}

func write(w http.ResponseWriter, r *http.Request, status int, contentType string, v any) {
	w.Header().Set("Content-Type", contentType)
	w.WriteHeader(status)
	if err := json.MarshalWrite(w, v, json.Deterministic(true)); err != nil {
		log.FromContext(r.Context()).Error("encode response", "err", err)
		return
	}
	if _, err := io.WriteString(w, "\n"); err != nil {
		log.FromContext(r.Context()).Error("encode response", "err", err)
	}
}

type List[T any] struct {
	Object     string  `json:"object"`
	Data       []T     `json:"data"`
	HasMore    bool    `json:"has_more"`
	NextCursor *string `json:"next_cursor"`
}

func NewList[T any](items []T, limit int, cursor func(T) string) List[T] {
	list := List[T]{Object: "list", Data: items}
	if len(items) > limit {
		list.Data, list.HasMore = items[:limit], true
		next := cursor(list.Data[limit-1])
		list.NextCursor = &next
	}
	if list.Data == nil {
		list.Data = []T{}
	}
	return list
}

func PageLimit(r *http.Request) (int, error) {
	raw := r.URL.Query().Get("limit")
	if raw == "" {
		return DefaultPageLimit, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 || n > MaxPageLimit {
		return 0, fmt.Errorf("limit must be an integer from 1 to %d", MaxPageLimit)
	}
	return n, nil
}

type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	n, err := s.ResponseWriter.Write(b)
	s.bytes += n
	return n, err
}

func Logging(base *log.Logger, next http.Handler) http.Handler {
	base = base.WithPrefix("http")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		id := requestID(r.Header.Get(requestIDHeader))
		w.Header().Set(requestIDHeader, id)

		reqLogger := base.With("request_id", id)
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}

		defer func() {
			if p := recover(); p != nil {
				reqLogger.Error("panic", "method", r.Method, "path", r.URL.Path, "panic", p)
				if rec.bytes == 0 {
					Error(rec, r, http.StatusInternalServerError, CodeInternal, "")
				}
			}
			level := log.InfoLevel
			switch {
			case rec.status >= 500:
				level = log.ErrorLevel
			case rec.status >= 400:
				level = log.WarnLevel
			}
			reqLogger.Log(level, "request",
				"method", r.Method,
				"path", r.URL.Path,
				"status", rec.status,
				"bytes", rec.bytes,
				"duration", time.Since(start),
				"remote", r.RemoteAddr,
			)
		}()

		ctx := logger.WithRequestID(log.WithContext(r.Context(), reqLogger), id)
		next.ServeHTTP(rec, r.WithContext(ctx))
	})
}

func requestID(upstream string) string {
	if id, err := uuid.Parse(upstream); err == nil && id.Version() == 4 && len(upstream) == 36 {
		return id.String()
	}
	return uuid.NewString()
}
