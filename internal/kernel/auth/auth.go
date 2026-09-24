package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/log"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pandabase/astrum/internal/kernel/httpx"
	"github.com/pandabase/astrum/internal/kernel/logger"
	"github.com/pandabase/astrum/internal/kernel/typeid"
)

//go:embed migrations/*.sql
var migrations embed.FS

type Role string

const (
	RoleAdmin Role = "admin"
	RoleWrite Role = "write"
	RoleRead  Role = "read"
)

func (r Role) valid() bool { return r == RoleAdmin || r == RoleWrite || r == RoleRead }

const (
	idPrefix    = "key"
	tokenPrefix = "sk_"
	secretBytes = 32

	cacheTTL = 10 * time.Second

	revokeLockID = 0x61737472756d6b

	touchEvery = time.Minute
	maxNameLen = 255
)

var (
	ErrUnauthenticated = errors.New("auth: invalid or missing API key")
	ErrNotFound        = errors.New("auth: api key not found")
	ErrInvalid         = errors.New("auth: invalid input")
	ErrLastAdmin       = errors.New("auth: the last active admin key cannot be revoked")
)

type Key struct {
	ID         uuid.UUID
	Name       string
	Role       Role
	Hint       string
	CreatedBy  *uuid.UUID
	CreatedAt  time.Time
	ExpiresAt  *time.Time
	RevokedAt  *time.Time
	LastUsedAt *time.Time
}

func (k Key) active(now time.Time) bool {
	return k.RevokedAt == nil && (k.ExpiresAt == nil || now.Before(*k.ExpiresAt))
}

type CreateInput struct {
	Name      string
	Role      Role
	ExpiresAt *time.Time

	CreatedBy *uuid.UUID
}

type Service struct {
	pool *pgxpool.Pool
	log  *log.Logger

	mu    sync.Mutex
	cache map[uuid.UUID]cached
}

type cached struct {
	key     Key
	hash    []byte
	expires time.Time
	touched time.Time
}

func New(pool *pgxpool.Pool, logger *log.Logger) *Service {
	return &Service{pool: pool, log: logger.WithPrefix("auth"), cache: map[uuid.UUID]cached{}}
}

func (s *Service) Name() string { return "auth" }

func (s *Service) Migrations() fs.FS {
	sub, err := fs.Sub(migrations, "migrations")
	if err != nil {
		panic(err)
	}
	return sub
}

func (s *Service) Create(ctx context.Context, in CreateInput) (Key, string, error) {
	switch {
	case strings.TrimSpace(in.Name) == "" || len(in.Name) > maxNameLen:
		return Key{}, "", fmt.Errorf("%w: name must be 1-%d characters", ErrInvalid, maxNameLen)
	case !in.Role.valid():
		return Key{}, "", fmt.Errorf("%w: role must be admin, write or read", ErrInvalid)
	case in.ExpiresAt != nil && !in.ExpiresAt.After(time.Now()):
		return Key{}, "", fmt.Errorf("%w: expires_at must be in the future", ErrInvalid)
	}
	id, err := uuid.NewV7()
	if err != nil {
		return Key{}, "", err
	}
	secret := make([]byte, secretBytes)
	if _, err := rand.Read(secret); err != nil {
		return Key{}, "", err
	}
	token := tokenPrefix + strings.TrimPrefix(typeid.Encode(idPrefix, id), idPrefix+"_") + "_" +
		base64.RawURLEncoding.EncodeToString(secret)
	hash := sha256.Sum256([]byte(token))
	k, err := scanKey(s.pool.QueryRow(ctx, `
		INSERT INTO api_keys (id, name, role, secret_hash, hint, created_by, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING `+keyColumns,
		id, in.Name, string(in.Role), hash[:], token[len(token)-4:], in.CreatedBy, in.ExpiresAt))
	if err != nil {
		return Key{}, "", fmt.Errorf("auth: create key: %w", err)
	}
	s.log.Info("api key created", "key_id", k.ID, "name", k.Name, "role", k.Role, "created_by", k.CreatedBy)
	return k, token, nil
}

func (s *Service) Authenticate(ctx context.Context, token string) (Key, error) {
	id, ok := parseToken(token)
	if !ok {
		return Key{}, ErrUnauthenticated
	}
	hash := sha256.Sum256([]byte(token))
	now := time.Now()

	s.mu.Lock()
	c, hit := s.cache[id]
	s.mu.Unlock()
	if !hit || now.After(c.expires) {
		var stored []byte
		k, err := scanKey(s.pool.QueryRow(ctx, `SELECT `+keyColumns+`, secret_hash FROM api_keys WHERE id = $1`, id), &stored)
		if errors.Is(err, pgx.ErrNoRows) {
			return Key{}, ErrUnauthenticated
		}
		if err != nil {
			return Key{}, err
		}
		c = cached{key: k, hash: stored, expires: now.Add(cacheTTL), touched: c.touched}
		s.mu.Lock()
		s.cache[id] = c
		s.mu.Unlock()
	}
	if subtle.ConstantTimeCompare(c.hash, hash[:]) != 1 || !c.key.active(now) {
		return Key{}, ErrUnauthenticated
	}
	if now.Sub(c.touched) > touchEvery {
		s.touch(id, now)
	}
	return c.key, nil
}

func (s *Service) touch(id uuid.UUID, now time.Time) {
	s.mu.Lock()
	c := s.cache[id]
	c.touched = now
	s.cache[id] = c
	s.mu.Unlock()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := s.pool.Exec(ctx, `UPDATE api_keys SET last_used_at = now() WHERE id = $1`, id); err != nil {
			s.log.Warn("record key use failed", "key_id", id, "err", err)
		}
	}()
}

func parseToken(token string) (uuid.UUID, bool) {
	rest, ok := strings.CutPrefix(token, tokenPrefix)
	if !ok || len(rest) != 26+1+base64.RawURLEncoding.EncodedLen(secretBytes) || rest[26] != '_' {
		return uuid.Nil, false
	}
	id, err := typeid.Parse(idPrefix, idPrefix+"_"+rest[:26])
	return id, err == nil
}

func (s *Service) Key(ctx context.Context, id uuid.UUID) (Key, error) {
	k, err := scanKey(s.pool.QueryRow(ctx, `SELECT `+keyColumns+` FROM api_keys WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Key{}, ErrNotFound
	}
	return k, err
}

func (s *Service) List(ctx context.Context, before uuid.UUID, limit int) ([]Key, error) {
	var cursor *uuid.UUID
	if before != uuid.Nil {
		cursor = &before
	}
	rows, err := s.pool.Query(ctx, `
		SELECT `+keyColumns+` FROM api_keys
		WHERE ($1::uuid IS NULL OR id < $1)
		ORDER BY id DESC
		LIMIT $2`, cursor, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Key, error) { return scanKey(row) })
}

func (s *Service) ActiveAdmins(ctx context.Context) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx, `
		SELECT count(*) FROM api_keys
		WHERE role = 'admin' AND revoked_at IS NULL AND (expires_at IS NULL OR expires_at > now())`).Scan(&n)
	return n, err
}

func (s *Service) Revoke(ctx context.Context, id uuid.UUID) (Key, error) {
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, revokeLockID); err != nil {
			return err
		}
		var (
			role    Role
			revoked bool
		)
		err := tx.QueryRow(ctx, `SELECT role, revoked_at IS NOT NULL FROM api_keys WHERE id = $1`, id).Scan(&role, &revoked)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			return ErrNotFound
		case err != nil:
			return err
		case revoked:
			return nil
		}
		if role == RoleAdmin {
			var others int
			if err := tx.QueryRow(ctx, `
				SELECT count(*) FROM api_keys
				WHERE role = 'admin' AND id <> $1 AND revoked_at IS NULL AND (expires_at IS NULL OR expires_at > now())`, id).Scan(&others); err != nil {
				return err
			}
			if others == 0 {
				return ErrLastAdmin
			}
		}
		_, err = tx.Exec(ctx, `UPDATE api_keys SET revoked_at = now() WHERE id = $1`, id)
		return err
	})
	if err != nil {
		return Key{}, err
	}
	s.mu.Lock()
	delete(s.cache, id)
	s.mu.Unlock()
	k, err := s.Key(ctx, id)
	if err == nil {
		s.log.Info("api key revoked", "key_id", id, "name", k.Name)
	}
	return k, err
}

const keyColumns = `id, name, role, hint, created_by, created_at, expires_at, revoked_at, last_used_at`

func scanKey(row pgx.Row, extra ...any) (Key, error) {
	var (
		k    Key
		role string
	)
	dest := append([]any{&k.ID, &k.Name, &role, &k.Hint, &k.CreatedBy, &k.CreatedAt, &k.ExpiresAt, &k.RevokedAt, &k.LastUsedAt}, extra...)
	err := row.Scan(dest...)
	k.Role = Role(role)
	return k, err
}

type keyContext struct{}

func FromContext(ctx context.Context) (Key, bool) {
	k, ok := ctx.Value(keyContext{}).(Key)
	return k, ok
}

func Scope(r *http.Request) string {
	if k, ok := FromContext(r.Context()); ok {
		return k.ID.String()
	}
	return ""
}

func (s *Service) Middleware(public []string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if slices.Contains(public, r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		scheme, token, ok := strings.Cut(r.Header.Get("Authorization"), " ")
		if !ok || !strings.EqualFold(scheme, "Bearer") {
			w.Header().Set("WWW-Authenticate", `Bearer realm="astrum"`)
			httpx.Error(w, r, http.StatusUnauthorized, "unauthorized", "send an API key as Authorization: Bearer sk_...")
			return
		}
		k, err := s.Authenticate(r.Context(), strings.TrimSpace(token))
		if err != nil {
			if !errors.Is(err, ErrUnauthenticated) {
				logger.For(r.Context(), s.log).Error("authenticate failed", "err", err)
				httpx.Error(w, r, http.StatusServiceUnavailable, httpx.CodeUnavailable, "")
				return
			}
			logger.For(r.Context(), s.log).Warn("request rejected", "reason", "invalid key", "path", r.URL.Path)
			w.Header().Set("WWW-Authenticate", `Bearer realm="astrum", error="invalid_token"`)
			httpx.Error(w, r, http.StatusUnauthorized, "unauthorized", "the API key is invalid, expired or revoked")
			return
		}
		ctx := logger.WithActor(context.WithValue(r.Context(), keyContext{}, k), typeid.Encode(idPrefix, k.ID))
		if !allowed(k.Role, r) {
			logger.For(ctx, s.log).Warn("request forbidden", "role", k.Role, "method", r.Method, "path", r.URL.Path)
			httpx.Error(w, r, http.StatusForbidden, "forbidden", fmt.Sprintf("a %s key cannot %s %s", k.Role, r.Method, r.URL.Path))
			return
		}
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func adminPath(path, prefix string) bool {
	return path == prefix || strings.HasPrefix(path, prefix+"/")
}

func allowed(role Role, r *http.Request) bool {
	keyAdmin := adminPath(r.URL.Path, "/v1/api_keys") || adminPath(r.URL.Path, "/v1/webhook_endpoints")
	switch role {
	case RoleAdmin:
		return true
	case RoleWrite:
		return !keyAdmin
	case RoleRead:
		return !keyAdmin && (r.Method == http.MethodGet || r.Method == http.MethodHead)
	}
	return false
}
