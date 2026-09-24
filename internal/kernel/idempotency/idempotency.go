package idempotency

import (
	"bytes"
	"context"
	"crypto/sha256"
	"embed"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/charmbracelet/log"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pandabase/astrum/internal/kernel/httpx"
	"github.com/pandabase/astrum/internal/kernel/logger"
)

const (
	Header         = "Idempotency-Key"
	ReplayedHeader = "Idempotent-Replayed"

	maxKeyLen   = 255
	maxBodySize = httpx.MaxBulkBodyBytes
)

//go:embed migrations/*.sql
var migrations embed.FS

type Service struct {
	pool        *pgxpool.Pool
	log         *log.Logger
	lockTimeout time.Duration
	retention   time.Duration
	Scope       func(*http.Request) string
}

func New(pool *pgxpool.Pool, logger *log.Logger) *Service {
	return &Service{
		pool:        pool,
		log:         logger.WithPrefix("idempotency"),
		lockTimeout: time.Minute,
		retention:   24 * time.Hour,
	}
}

func (s *Service) Run(ctx context.Context) error {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}

		n, err := s.Prune(ctx)
		if err != nil {
			s.log.Error("prune failed", "err", err)
			continue
		}

		if n > 0 {
			s.log.Info("pruned expired keys", "count", n)
		}
	}
}

func (s *Service) Prune(ctx context.Context) (int64, error) {
	tag, err := s.pool.Exec(ctx, `
		DELETE FROM idempotency_keys
		WHERE status = 'completed' AND created_at < now() - make_interval(secs => $1)`,
		s.retention.Seconds())

	return tag.RowsAffected(), err
}

func (s *Service) Name() string { return "idempotency" }

func (s *Service) Migrations() fs.FS {
	sub, err := fs.Sub(migrations, "migrations")

	if err != nil {
		panic(err)
	}

	return sub
}

type record struct {
	requestHash []byte
	status      string
	respStatus  *int
	contentType *string
	body        []byte
}

func (s *Service) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get(Header)
		if key == "" || !mutating(r.Method) {
			next.ServeHTTP(w, r)
			return
		}

		l := logger.For(r.Context(), s.log).With("key", key, "method", r.Method, "path", r.URL.Path)

		if len(key) > maxKeyLen {
			l.Warn("key rejected", "reason", "too long")
			httpx.Error(w, r, http.StatusBadRequest, httpx.CodeInvalidRequest, fmt.Sprintf("%s must be at most %d characters", Header, maxKeyLen))
			return
		}

		if !utf8.ValidString(key) {
			l.Warn("key rejected", "reason", "invalid utf-8")
			httpx.Error(w, r, http.StatusBadRequest, httpx.CodeInvalidRequest, fmt.Sprintf("%s must be valid UTF-8", Header))
			return
		}

		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodySize))
		if err != nil {
			l.Warn("request body rejected", "err", err)
			if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
				httpx.Error(w, r, http.StatusRequestEntityTooLarge, httpx.CodeRequestTooLarge, "request body too large")
				return
			}
			httpx.Error(w, r, http.StatusBadRequest, httpx.CodeInvalidRequest, "invalid request body")
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		hash := requestHash(r, body)
		if s.Scope != nil {
			if scope := s.Scope(r); scope != "" {
				key = scope + ":" + key
			}
		}

		token := uuid.New()
		claimed, err := s.claim(r.Context(), key, hash, token)
		if err != nil {
			l.Error("claim failed", "err", err)
			httpx.Error(w, r, http.StatusInternalServerError, httpx.CodeInternal, "")
			return
		}
		if !claimed {
			s.resolveExisting(w, r, l, key, hash)
			return
		}
		l.Debug("key claimed")

		rec := &recorder{ResponseWriter: w, status: http.StatusOK}
		defer func() {
			if p := recover(); p != nil {
				rec.status = http.StatusInternalServerError
				s.finish(context.WithoutCancel(r.Context()), r, l, key, token, rec)
				panic(p)
			}
		}()
		next.ServeHTTP(rec, r)
		s.finish(context.WithoutCancel(r.Context()), r, l, key, token, rec)
	})
}

func (s *Service) claim(ctx context.Context, key string, hash []byte, token uuid.UUID) (bool, error) {
	var claimed bool
	err := s.pool.QueryRow(ctx, `
		INSERT INTO idempotency_keys (key, request_hash, status, lock_token)
		VALUES ($1, $2, 'processing', $4)
		ON CONFLICT (key) DO UPDATE SET locked_at = now(), lock_token = EXCLUDED.lock_token
		WHERE idempotency_keys.status = 'processing'
		  AND idempotency_keys.request_hash = EXCLUDED.request_hash
		  AND idempotency_keys.locked_at < now() - make_interval(secs => $3)
		RETURNING true`,
		key, hash, s.lockTimeout.Seconds(), token,
	).Scan(&claimed)

	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}

	return claimed, err
}

func (s *Service) resolveExisting(w http.ResponseWriter, r *http.Request, l *log.Logger, key string, hash []byte) {
	var rec record
	err := s.pool.QueryRow(r.Context(), `
		SELECT request_hash, status, response_status, response_content_type, response_body
		FROM idempotency_keys
		WHERE key = $1`, key,
	).Scan(&rec.requestHash, &rec.status, &rec.respStatus, &rec.contentType, &rec.body)

	switch {
	case errors.Is(err, pgx.ErrNoRows):
		l.Warn("key released concurrently")
		httpx.Error(w, r, http.StatusConflict, httpx.CodeIdempotencyBusy, "a request with this idempotency key is in progress, retry later")
	case err != nil:
		l.Error("lookup failed", "err", err)
		httpx.Error(w, r, http.StatusInternalServerError, httpx.CodeInternal, "")
	case !bytes.Equal(rec.requestHash, hash):
		l.Warn("key reused with a different request")
		httpx.Error(w, r, http.StatusUnprocessableEntity, httpx.CodeIdempotencyReuse, "this idempotency key was used with a different request")
	case rec.status == "processing":
		l.Warn("key in progress")
		httpx.Error(w, r, http.StatusConflict, httpx.CodeIdempotencyBusy, "a request with this idempotency key is in progress, retry later")
	default:
		l.Info("response replayed", "status", *rec.respStatus)
		if rec.contentType != nil {
			w.Header().Set("Content-Type", *rec.contentType)
		}
		w.Header().Set(ReplayedHeader, "true")
		w.WriteHeader(*rec.respStatus)
		if _, err := w.Write(rec.body); err != nil {
			l.Error("write replay failed", "err", err)
		}
	}
}

func (s *Service) finish(ctx context.Context, r *http.Request, l *log.Logger, key string, token uuid.UUID, rec *recorder) {
	if rec.status >= http.StatusInternalServerError {
		tag, err := s.pool.Exec(ctx, `DELETE FROM idempotency_keys WHERE key = $1 AND lock_token = $2`, key, token)
		switch {
		case err != nil:
			l.Error("release failed", "err", err)
		case tag.RowsAffected() == 0:
			l.Warn("key ownership lost before release", "status", rec.status)
		default:
			l.Warn("key released after server error", "status", rec.status)
		}
		return
	}

	status, contentType, body := rec.status, rec.Header().Get("Content-Type"), append([]byte{}, rec.body.Bytes()...)
	if strings.Contains(strings.ToLower(rec.Header().Get("Cache-Control")), "no-store") {
		status, contentType = http.StatusConflict, "application/problem+json"
		problem := httpx.NewProblem(r, status, httpx.CodeIdempotencyDone,
			"this request already succeeded; its response contained a secret, which is shown only once")
		encoded, err := json.Marshal(problem)
		if err != nil {
			l.Error("encode replay problem failed", "err", err)
			return
		}
		body = append(encoded, '\n')
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE idempotency_keys
		SET status = 'completed',
		    response_status = $2,
		    response_content_type = $3,
		    response_body = $4,
		    completed_at = now()
		WHERE key = $1 AND lock_token = $5 AND status = 'processing'`,
		key, status, contentType, body, token)
	if err != nil {
		l.Error("store response failed", "err", err)
		return
	}
	if tag.RowsAffected() == 0 {
		l.Warn("key ownership lost before response was stored", "status", rec.status)
		return
	}
	l.Debug("response stored", "status", rec.status, "bytes", rec.body.Len())
}

func requestHash(r *http.Request, body []byte) []byte {
	h := sha256.New()
	fmt.Fprintf(h, "%s\x00%s\x00", r.Method, r.URL.RequestURI())
	h.Write(body)
	return h.Sum(nil)
}

func mutating(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	}
	return false
}

type recorder struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
	body        bytes.Buffer
}

func (r *recorder) WriteHeader(status int) {
	if !r.wroteHeader {
		r.status = status
		r.wroteHeader = true
	}
	r.ResponseWriter.WriteHeader(status)
}

func (r *recorder) Write(b []byte) (int, error) {
	r.wroteHeader = true
	r.body.Write(b)
	return r.ResponseWriter.Write(b)
}
