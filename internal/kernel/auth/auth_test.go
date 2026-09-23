package auth

import (
	"context"
	"encoding/json/v2"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/pandabase/astrum/internal/kernel/db"
	"github.com/pandabase/astrum/internal/kernel/httpx"
	"github.com/pandabase/astrum/internal/kernel/testdb"
)

func setup(t *testing.T) *Service {
	t.Helper()
	s := New(nil, testdb.Logger())
	s.pool = testdb.New(t, map[string]fs.FS{s.Name(): s.Migrations()})
	return s
}

func create(t *testing.T, s *Service, name string, role Role) (Key, string) {
	t.Helper()
	k, token, err := s.Create(context.Background(), CreateInput{Name: name, Role: role})
	if err != nil {
		t.Fatal(err)
	}
	return k, token
}

func TestParseToken(t *testing.T) {
	valid := "sk_01h455vb4pex5vsknk084sn02q_" + strings.Repeat("A", 43)
	if _, ok := parseToken(valid); !ok {
		t.Fatalf("parseToken(%s) failed", valid)
	}
	for _, bad := range []string{
		"",
		"sk_",
		"pk_01h455vb4pex5vsknk084sn02q_" + strings.Repeat("A", 43),
		"sk_01h455vb4pex5vsknk084sn02q_" + strings.Repeat("A", 42),
		"sk_01h455vb4pex5vsknk084sn02q-" + strings.Repeat("A", 43),
		"sk_81h455vb4pex5vsknk084sn02q_" + strings.Repeat("A", 43),
		"sk_01h455vb4pex5vsknk084sn0!q_" + strings.Repeat("A", 43),
	} {
		if _, ok := parseToken(bad); ok {
			t.Errorf("parseToken(%q) accepted", bad)
		}
	}
}

func TestAuthenticate(t *testing.T) {
	s := setup(t)
	ctx := context.Background()
	k, token := create(t, s, "ops", RoleAdmin)
	if !strings.HasPrefix(token, "sk_") || k.Hint != token[len(token)-4:] {
		t.Fatalf("token = %s, hint %s", token, k.Hint)
	}

	got, err := s.Authenticate(ctx, token)
	if err != nil || got.ID != k.ID || got.Role != RoleAdmin {
		t.Fatalf("Authenticate = %+v, %v", got, err)
	}

	t.Run("wrong secret with a real id", func(t *testing.T) {
		last := "A"
		if strings.HasSuffix(token, "A") {
			last = "B"
		}
		_, err := s.Authenticate(ctx, token[:len(token)-1]+last)
		wantUnauthenticated(t, err)
	})

	t.Run("unknown id", func(t *testing.T) {
		_, other := create(t, New(s.pool, testdb.Logger()), "other", RoleRead)
		_, err := s.Authenticate(ctx, "sk_01h455vb4pex5vsknk084sn02q"+other[29:])
		wantUnauthenticated(t, err)
	})

	t.Run("revocation takes effect at once on this instance", func(t *testing.T) {
		if _, err := s.Revoke(ctx, k.ID); err != nil {
			t.Fatal(err)
		}
		_, err := s.Authenticate(ctx, token)
		wantUnauthenticated(t, err)
		again, err := s.Revoke(ctx, k.ID)
		if err != nil || again.RevokedAt == nil {
			t.Fatalf("second revoke = %+v, %v", again, err)
		}
	})

	t.Run("expired", func(t *testing.T) {
		soon := time.Now().Add(100 * time.Millisecond)
		_, token, err := s.Create(ctx, CreateInput{Name: "short", Role: RoleWrite, ExpiresAt: &soon})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.Authenticate(ctx, token); err != nil {
			t.Fatal(err)
		}
		time.Sleep(150 * time.Millisecond)
		_, err = s.Authenticate(ctx, token)
		wantUnauthenticated(t, err)
	})

	t.Run("records last use", func(t *testing.T) {
		k, token := create(t, s, "used", RoleRead)
		if _, err := s.Authenticate(ctx, token); err != nil {
			t.Fatal(err)
		}
		for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(20 * time.Millisecond) {
			got, _ := s.Key(ctx, k.ID)
			if got.LastUsedAt != nil {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("last_used_at never recorded")
			}
		}
	})

	t.Run("validation", func(t *testing.T) {
		past := time.Now().Add(-time.Hour)
		for _, in := range []CreateInput{
			{Name: "", Role: RoleAdmin},
			{Name: "x", Role: "owner"},
			{Name: "x", Role: RoleRead, ExpiresAt: &past},
		} {
			if _, _, err := s.Create(ctx, in); err == nil {
				t.Errorf("Create(%+v) succeeded", in)
			}
		}
	})

	t.Run("schema keeps keys and revocations", func(t *testing.T) {
		for _, sql := range []string{
			`DELETE FROM api_keys WHERE id = '` + k.ID.String() + `'`,
			`UPDATE api_keys SET revoked_at = NULL WHERE id = '` + k.ID.String() + `'`,
			`UPDATE api_keys SET role = 'admin' WHERE role = 'read'`,
		} {
			err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
				_, err := tx.Exec(ctx, sql)
				return err
			})
			if db.Code(err) != "23001" {
				t.Fatalf("%s: error = %v, want restrict_violation", sql, err)
			}
		}
	})
}

func wantUnauthenticated(t *testing.T, err error) {
	t.Helper()
	if err != ErrUnauthenticated {
		t.Fatalf("error = %v, want ErrUnauthenticated", err)
	}
}

func TestMiddleware(t *testing.T) {
	s := setup(t)
	mux := http.NewServeMux()
	s.Routes(mux)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("GET /v1/things", func(w http.ResponseWriter, r *http.Request) {
		k, _ := FromContext(r.Context())
		httpx.JSON(w, r, http.StatusOK, map[string]string{"role": string(k.Role), "scope": Scope(r)})
	})
	mux.HandleFunc("POST /v1/things", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusCreated) })
	srv := httptest.NewServer(httpx.Logging(testdb.Logger(), s.Middleware([]string{"/healthz"}, mux)))
	t.Cleanup(srv.Close)

	_, admin := create(t, s, "admin", RoleAdmin)
	_, writer := create(t, s, "writer", RoleWrite)
	_, reader := create(t, s, "reader", RoleRead)

	call := func(method, path, token, body string) (int, map[string]any, http.Header) {
		t.Helper()
		req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out map[string]any
		_ = json.UnmarshalRead(resp.Body, &out)
		return resp.StatusCode, out, resp.Header
	}

	tests := []struct {
		name, method, path, token string
		status                    int
	}{
		{"health is public", http.MethodGet, "/healthz", "", 200},
		{"no key", http.MethodGet, "/v1/things", "", 401},
		{"garbage key", http.MethodGet, "/v1/things", "sk_nope", 401},
		{"reader reads", http.MethodGet, "/v1/things", reader, 200},
		{"reader cannot write", http.MethodPost, "/v1/things", reader, 403},
		{"writer writes", http.MethodPost, "/v1/things", writer, 201},
		{"writer cannot manage keys", http.MethodGet, "/v1/api_keys", writer, 403},
		{"reader cannot list keys", http.MethodGet, "/v1/api_keys", reader, 403},
		{"admin manages keys", http.MethodGet, "/v1/api_keys", admin, 200},
		{"reader sees itself", http.MethodGet, "/v1/me", reader, 200},
		{"me needs a key", http.MethodGet, "/v1/me", "", 401},
		{"lookalike path is not key admin", http.MethodGet, "/v1/api_keysx", writer, 404},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status, body, header := call(tt.method, tt.path, tt.token, "")
			if status != tt.status {
				t.Fatalf("%s %s = %d %v, want %d", tt.method, tt.path, status, body, tt.status)
			}
			if status == 401 && (body["code"] != "unauthorized" || !strings.HasPrefix(header.Get("WWW-Authenticate"), "Bearer")) {
				t.Fatalf("401 = %v, %v", body, header)
			}
			if status == 403 && body["code"] != "forbidden" {
				t.Fatalf("403 = %v", body)
			}
		})
	}

	t.Run("requests carry their key", func(t *testing.T) {
		_, body, _ := call(http.MethodGet, "/v1/things", writer, "")
		if body["role"] != "write" || body["scope"] == "" {
			t.Fatalf("context = %v", body)
		}
	})

	t.Run("me describes the calling key", func(t *testing.T) {
		_, body, _ := call(http.MethodGet, "/v1/me", writer, "")
		if body["object"] != "api_key" || body["name"] != "writer" || body["role"] != "write" || body["secret"] != nil {
			t.Fatalf("me = %v", body)
		}
	})

	t.Run("key management api", func(t *testing.T) {
		status, created, _ := call(http.MethodPost, "/v1/api_keys", admin, `{"name":"ci","role":"write"}`)
		if status != 201 || !strings.HasPrefix(created["id"].(string), "key_") || !strings.HasPrefix(created["secret"].(string), "sk_") ||
			created["created_by"] == nil {
			t.Fatalf("create = %d %v", status, created)
		}
		id, secret := created["id"].(string), created["secret"].(string)
		if status, _, _ := call(http.MethodPost, "/v1/things", secret, ""); status != 201 {
			t.Fatalf("new key write = %d", status)
		}
		status, got, _ := call(http.MethodGet, "/v1/api_keys/"+id, admin, "")
		if status != 200 || got["secret"] != nil || !strings.HasPrefix(got["hint"].(string), "…") {
			t.Fatalf("get = %d %v", status, got)
		}
		if status, _, _ := call(http.MethodPost, "/v1/api_keys/"+id+"/revoke", admin, ""); status != 200 {
			t.Fatalf("revoke = %d", status)
		}
		if status, _, _ := call(http.MethodPost, "/v1/things", secret, ""); status != 401 {
			t.Fatalf("revoked key = %d", status)
		}
		if status, _, _ := call(http.MethodPost, "/v1/api_keys", admin, `{"name":"x","role":"root"}`); status != 422 {
			t.Fatalf("bad role = %d", status)
		}
		if status, _, _ := call(http.MethodGet, "/v1/api_keys/key_01h455vb4pex5vsknk084sn02q", admin, ""); status != 404 {
			t.Fatalf("missing key = %d", status)
		}
	})
}
