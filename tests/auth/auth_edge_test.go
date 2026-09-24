package auth_test

import (
	"context"
	"encoding/base64"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pandabase/astrum/internal/kernel/auth"
	"github.com/pandabase/astrum/internal/kernel/httpx"
	"github.com/pandabase/astrum/internal/kernel/testdb"
	"github.com/pandabase/astrum/internal/kernel/typeid"
)

type edgeEnv struct {
	t   *testing.T
	svc *auth.Service
	srv *httptest.Server
}

func newEdgeEnv(t *testing.T) *edgeEnv {
	t.Helper()
	probe := auth.New(nil, testdb.Logger())
	pool := testdb.New(t, map[string]fs.FS{probe.Name(): probe.Migrations()})
	svc := auth.New(pool, testdb.Logger())
	mux := http.NewServeMux()
	svc.Routes(mux)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ok") })
	for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodOptions} {
		mux.HandleFunc(method+" /v1/things", func(w http.ResponseWriter, r *http.Request) {
			httpx.JSON(w, r, http.StatusOK, map[string]string{"scope": auth.Scope(r)})
		})
	}
	srv := httptest.NewServer(httpx.Logging(testdb.Logger(), svc.Middleware([]string{"/healthz"}, mux)))
	t.Cleanup(srv.Close)
	return &edgeEnv{t: t, svc: svc, srv: srv}
}

func (e *edgeEnv) key(name string, role auth.Role) (auth.Key, string) {
	e.t.Helper()
	k, token, err := e.svc.Create(context.Background(), auth.CreateInput{Name: name, Role: role})
	if err != nil {
		e.t.Fatal(err)
	}
	return k, token
}

type edgeResp struct {
	status int
	header http.Header
	raw    []byte
	body   map[string]any
}

func (e *edgeEnv) raw(method, path string, header http.Header, body string) edgeResp {
	e.t.Helper()
	req, err := http.NewRequest(method, e.srv.URL+path, strings.NewReader(body))
	if err != nil {
		e.t.Fatal(err)
	}
	maps.Copy(req.Header, header)
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		e.t.Fatal(err)
	}
	out := edgeResp{status: resp.StatusCode, header: resp.Header, raw: raw}
	_ = json.Unmarshal(raw, &out.body)
	return out
}

func (e *edgeEnv) call(method, path, token, body string) edgeResp {
	e.t.Helper()
	h := http.Header{}
	if token != "" {
		h.Set("Authorization", "Bearer "+token)
	}
	return e.raw(method, path, h, body)
}

func (e *edgeEnv) must(want int, method, path, token, body string) map[string]any {
	e.t.Helper()
	resp := e.call(method, path, token, body)
	if resp.status != want {
		e.t.Fatalf("%s %s = %d %s, want %d", method, path, resp.status, resp.raw, want)
	}
	return resp.body
}

func TestAuthEdgeAuthorizationHeader(t *testing.T) {
	e := newEdgeEnv(t)
	_, token := e.key("reader", auth.RoleRead)
	e.key("remaining admin", auth.RoleAdmin)
	revokedKey, revoked := e.key("revoked", auth.RoleAdmin)
	if _, err := e.svc.Revoke(context.Background(), revokedKey.ID); err != nil {
		t.Fatal(err)
	}
	unknown := "sk_" + strings.TrimPrefix(typeid.Encode("key", uuid.Must(uuid.NewV7())), "key_") + token[29:]
	tampered := token[:len(token)-1] + map[bool]string{true: "B", false: "A"}[strings.HasSuffix(token, "A")]

	tests := []struct {
		name   string
		values []string
		status int
		errTok bool
	}{
		{"missing", nil, 401, false},
		{"empty", []string{""}, 401, false},
		{"scheme only", []string{"Bearer"}, 401, false},
		{"scheme and trailing space", []string{"Bearer "}, 401, false},
		{"lowercase scheme", []string{"bearer " + token}, 200, false},
		{"uppercase scheme", []string{"BEARER " + token}, 200, false},
		{"basic scheme", []string{"Basic " + base64.StdEncoding.EncodeToString([]byte(token+":"))}, 401, false},
		{"raw token", []string{token}, 401, false},
		{"token without prefix", []string{"Bearer " + strings.TrimPrefix(token, "sk_")}, 401, true},
		{"extra spaces before token", []string{"Bearer    " + token}, 200, false},
		{"trailing spaces", []string{"Bearer " + token + "   "}, 200, false},
		{"tab separated", []string{"Bearer\t" + token}, 401, false},
		{"token then garbage", []string{"Bearer " + token + " extra"}, 401, true},
		{"two tokens", []string{"Bearer " + token + ",Bearer " + token}, 401, true},
		{"first of two headers wins", []string{"Bearer " + token, "Bearer nope"}, 200, false},
		{"second of two headers ignored", []string{"Bearer nope", "Bearer " + token}, 401, true},
		{"truncated token", []string{"Bearer " + token[:len(token)-1]}, 401, true},
		{"tampered secret", []string{"Bearer " + tampered}, 401, true},
		{"unknown key id", []string{"Bearer " + unknown}, 401, true},
		{"revoked key", []string{"Bearer " + revoked}, 401, true},
		{"uppercased token", []string{"Bearer " + strings.ToUpper(token)}, 401, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := http.Header{}
			for _, v := range tt.values {
				h.Add("Authorization", v)
			}
			resp := e.raw(http.MethodGet, "/v1/things", h, "")
			if resp.status != tt.status {
				t.Fatalf("status = %d %s, want %d", resp.status, resp.raw, tt.status)
			}
			if tt.status != 401 {
				return
			}
			want := `Bearer realm="astrum"`
			if tt.errTok {
				want += `, error="invalid_token"`
			}
			if got := resp.header.Get("WWW-Authenticate"); got != want {
				t.Fatalf("WWW-Authenticate = %q, want %q", got, want)
			}
			if resp.body["code"] != "unauthorized" || resp.header.Get("Content-Type") != "application/problem+json" {
				t.Fatalf("problem = %s", resp.raw)
			}
			if strings.Contains(string(resp.raw), token) {
				t.Fatal("response echoes the token")
			}
		})
	}

	t.Run("public path needs no key but lookalikes do", func(t *testing.T) {
		if resp := e.call(http.MethodGet, "/healthz", "", ""); resp.status != 200 {
			t.Fatalf("healthz = %d", resp.status)
		}
		for _, p := range []string{"/healthz/", "/healthz?x=1#", "/HEALTHZ", "/v1/../healthz"} {
			resp := e.call(http.MethodGet, p, "", "")
			if p == "/healthz?x=1#" {
				if resp.status != 200 {
					t.Fatalf("%s = %d, want the query to be ignored", p, resp.status)
				}
				continue
			}
			if resp.status != 401 {
				t.Fatalf("%s = %d, want 401", p, resp.status)
			}
		}
	})
}

func TestAuthEdgeRoles(t *testing.T) {
	e := newEdgeEnv(t)
	_, admin := e.key("admin", auth.RoleAdmin)
	_, writer := e.key("writer", auth.RoleWrite)
	_, reader := e.key("reader", auth.RoleRead)
	target, _ := e.key("target", auth.RoleRead)
	targetID := typeid.Encode("key", target.ID)

	type route struct{ method, path, body string }
	routes := map[string]route{
		"get thing":      {http.MethodGet, "/v1/things", ""},
		"head thing":     {http.MethodHead, "/v1/things", ""},
		"options thing":  {http.MethodOptions, "/v1/things", ""},
		"post thing":     {http.MethodPost, "/v1/things", ""},
		"put thing":      {http.MethodPut, "/v1/things", ""},
		"patch thing":    {http.MethodPatch, "/v1/things", ""},
		"delete thing":   {http.MethodDelete, "/v1/things", ""},
		"me":             {http.MethodGet, "/v1/me", ""},
		"list keys":      {http.MethodGet, "/v1/api_keys", ""},
		"get key":        {http.MethodGet, "/v1/api_keys/" + targetID, ""},
		"create key":     {http.MethodPost, "/v1/api_keys", `{"name":"n","role":"read"}`},
		"key subpath":    {http.MethodGet, "/v1/api_keys/", ""},
		"unrouted admin": {http.MethodDelete, "/v1/api_keys/" + targetID, ""},
		"revoke key":     {http.MethodPost, "/v1/api_keys/" + targetID + "/revoke", ""},
	}
	want := map[string]map[string]int{
		"get thing":      {"admin": 200, "write": 200, "read": 200},
		"head thing":     {"admin": 200, "write": 200, "read": 200},
		"options thing":  {"admin": 200, "write": 200, "read": 403},
		"post thing":     {"admin": 200, "write": 200, "read": 403},
		"put thing":      {"admin": 200, "write": 200, "read": 403},
		"patch thing":    {"admin": 200, "write": 200, "read": 403},
		"delete thing":   {"admin": 200, "write": 200, "read": 403},
		"me":             {"admin": 200, "write": 200, "read": 200},
		"list keys":      {"admin": 200, "write": 403, "read": 403},
		"get key":        {"admin": 200, "write": 403, "read": 403},
		"create key":     {"admin": 201, "write": 403, "read": 403},
		"key subpath":    {"admin": 404, "write": 403, "read": 403},
		"unrouted admin": {"admin": 405, "write": 403, "read": 403},
		"revoke key":     {"admin": 200, "write": 403, "read": 403},
	}
	tokens := map[string]string{"admin": admin, "write": writer, "read": reader}
	order := []string{"get thing", "head thing", "options thing", "post thing", "put thing", "patch thing", "delete thing", "me", "list keys", "get key", "create key", "key subpath", "unrouted admin", "revoke key"}
	for _, role := range []string{"read", "write", "admin"} {
		for _, name := range order {
			rt := routes[name]
			t.Run(role+"/"+name, func(t *testing.T) {
				resp := e.call(rt.method, rt.path, tokens[role], rt.body)
				if resp.status != want[name][role] {
					t.Fatalf("%s %s as %s = %d %s, want %d", rt.method, rt.path, role, resp.status, resp.raw, want[name][role])
				}
				if resp.status == 403 {
					detail := fmt.Sprintf("a %s key cannot %s %s", role, rt.method, strings.SplitN(rt.path, "?", 2)[0])
					if resp.body["code"] != "forbidden" || resp.body["detail"] != detail {
						t.Fatalf("problem = %s, want detail %q", resp.raw, detail)
					}
				}
			})
		}
	}
}

func TestAuthEdgeKeyAdminPathTricks(t *testing.T) {
	e := newEdgeEnv(t)
	e.key("admin", auth.RoleAdmin)
	_, writer := e.key("writer", auth.RoleWrite)

	tests := []struct {
		name, method, path string
		status             int
	}{
		{"dot segment into key admin", http.MethodGet, "/v1/things/../api_keys", http.StatusTemporaryRedirect},
		{"double slash", http.MethodGet, "/v1//api_keys", http.StatusTemporaryRedirect},
		{"post via double slash", http.MethodPost, "/v1//api_keys", http.StatusTemporaryRedirect},
		{"encoded slash traversal", http.MethodGet, "/v1/things%2F..%2Fapi_keys", http.StatusNotFound},
		{"encoded key admin path", http.MethodGet, "/v1/api%5Fkeys", http.StatusForbidden},
		{"uppercase key admin path", http.MethodGet, "/v1/API_KEYS", http.StatusNotFound},
		{"dot segment out of key admin", http.MethodGet, "/v1/api_keys/../things", http.StatusForbidden},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := e.call(tt.method, tt.path, writer, `{"name":"sneaky","role":"admin"}`)
			if resp.status != tt.status {
				t.Fatalf("%s %s = %d %s, want %d", tt.method, tt.path, resp.status, resp.raw, tt.status)
			}
			if strings.Contains(string(resp.raw), `"api_key"`) || strings.Contains(string(resp.raw), "sk_") {
				t.Fatalf("write key reached key admin: %s", resp.raw)
			}
			if loc := resp.header.Get("Location"); resp.status == http.StatusTemporaryRedirect && loc != "/v1/api_keys" {
				t.Fatalf("Location = %q", loc)
			}
		})
	}

	t.Run("following the redirect is re-authorized", func(t *testing.T) {
		req, err := http.NewRequest(http.MethodPost, e.srv.URL+"/v1//api_keys", strings.NewReader(`{"name":"sneaky","role":"admin"}`))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+writer)
		resp, err := e.srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden || resp.Request.URL.Path != "/v1/api_keys" {
			t.Fatalf("followed redirect = %d at %s, want 403 at /v1/api_keys", resp.StatusCode, resp.Request.URL.Path)
		}
	})

	keys, err := e.svc.List(context.Background(), uuid.Nil, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range keys {
		if k.Name == "sneaky" {
			t.Fatal("write key created an API key through a path trick")
		}
	}
}

func TestAuthEdgeInvalidUTF8PathRejected(t *testing.T) {
	e := newEdgeEnv(t)
	_, reader := e.key("reader", auth.RoleRead)
	resp := e.call(http.MethodPost, "/v1/%ff", reader, "")
	if resp.status != http.StatusBadRequest {
		t.Fatalf("status = %d", resp.status)
	}
	var p httpx.Problem
	if err := json.Unmarshal(resp.raw, &p); err != nil || p.Code != httpx.CodeInvalidRequest {
		t.Fatalf("400 body %q is not a valid problem document: %v", resp.raw, err)
	}
}

func TestAuthEdgeMe(t *testing.T) {
	e := newEdgeEnv(t)
	k, admin := e.key("ops", auth.RoleAdmin)
	me := e.must(200, http.MethodGet, "/v1/me", admin, "")
	keys := []string{"object", "id", "name", "role", "hint", "created_by", "created_at", "expires_at", "revoked_at", "last_used_at"}
	if len(me) != len(keys) {
		t.Fatalf("me has %d members, want %d: %v", len(me), len(keys), me)
	}
	for _, name := range keys {
		if _, ok := me[name]; !ok {
			t.Fatalf("me lacks %s: %v", name, me)
		}
	}
	if me["object"] != "api_key" || me["id"] != typeid.Encode("key", k.ID) || me["name"] != "ops" || me["role"] != "admin" ||
		me["hint"] != "…"+admin[len(admin)-4:] || me["created_by"] != nil || me["revoked_at"] != nil || me["expires_at"] != nil {
		t.Fatalf("me = %v", me)
	}
	if _, err := time.Parse(time.RFC3339Nano, me["created_at"].(string)); err != nil {
		t.Fatalf("created_at = %v", me["created_at"])
	}

	exp := time.Now().Add(time.Hour).UTC().Truncate(time.Microsecond)
	_, expiring, err := e.svc.Create(context.Background(), auth.CreateInput{Name: "exp", Role: auth.RoleRead, ExpiresAt: &exp})
	if err != nil {
		t.Fatal(err)
	}
	got := e.must(200, http.MethodGet, "/v1/me", expiring, "")
	if at, err := time.Parse(time.RFC3339Nano, got["expires_at"].(string)); err != nil || !at.Equal(exp) {
		t.Fatalf("expires_at = %v, want %v", got["expires_at"], exp)
	}

	for _, m := range []string{http.MethodPost, http.MethodDelete} {
		if resp := e.call(m, "/v1/me", admin, ""); resp.status != http.StatusMethodNotAllowed {
			t.Fatalf("%s /v1/me = %d, want 405", m, resp.status)
		}
	}
	if resp := e.call(http.MethodGet, "/v1/me", "", ""); resp.status != 401 {
		t.Fatalf("anonymous me = %d", resp.status)
	}
}

func TestAuthEdgeMeWithoutMiddleware(t *testing.T) {
	svc := auth.New(nil, testdb.Logger())
	mux := http.NewServeMux()
	svc.Routes(mux)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/me", nil))
	if w.Code != http.StatusUnauthorized || !strings.Contains(w.Body.String(), `"code":"unauthorized"`) {
		t.Fatalf("me without a key in context = %d %s", w.Code, w.Body.String())
	}
	if scope := auth.Scope(httptest.NewRequest(http.MethodGet, "/", nil)); scope != "" {
		t.Fatalf("Scope without a key = %q", scope)
	}
}

func TestAuthEdgeCreate(t *testing.T) {
	e := newEdgeEnv(t)
	adminKey, admin := e.key("admin", auth.RoleAdmin)
	tests := []struct {
		name   string
		body   string
		status int
		code   string
	}{
		{"valid", `{"name":"ci","role":"write"}`, 201, ""},
		{"with expiry", fmt.Sprintf(`{"name":"ci","role":"read","expires_at":%q}`, time.Now().Add(time.Hour).Format(time.RFC3339)), 201, ""},
		{"null expiry", `{"name":"ci","role":"read","expires_at":null}`, 201, ""},
		{"name at limit", fmt.Sprintf(`{"name":%q,"role":"read"}`, strings.Repeat("n", 255)), 201, ""},
		{"multibyte name over byte limit", fmt.Sprintf(`{"name":%q,"role":"read"}`, strings.Repeat("é", 128)), 422, "validation_error"},
		{"name over limit", fmt.Sprintf(`{"name":%q,"role":"read"}`, strings.Repeat("n", 256)), 422, "validation_error"},
		{"blank name", `{"name":" \t ","role":"read"}`, 422, "validation_error"},
		{"missing role", `{"name":"x"}`, 422, "validation_error"},
		{"role wrong case", `{"name":"x","role":"Admin"}`, 422, "validation_error"},
		{"past expiry", `{"name":"x","role":"read","expires_at":"2000-01-01T00:00:00Z"}`, 422, "validation_error"},
		{"bad expiry", `{"name":"x","role":"read","expires_at":"tomorrow"}`, 400, httpx.CodeInvalidRequest},
		{"unknown member", `{"name":"x","role":"read","secret":"sk_x"}`, 400, httpx.CodeInvalidRequest},
		{"created_by is not settable", `{"name":"x","role":"read","created_by":"key_01h455vb4pex5vsknk084sn02q"}`, 400, httpx.CodeInvalidRequest},
		{"wrong case member", `{"Name":"x","role":"read"}`, 400, httpx.CodeInvalidRequest},
		{"duplicate role", `{"name":"x","role":"read","role":"admin"}`, 400, httpx.CodeInvalidRequest},
		{"empty body", ``, 400, httpx.CodeInvalidRequest},
		{"array", `[]`, 400, httpx.CodeInvalidRequest},
		{"null", `null`, 422, "validation_error"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := e.call(http.MethodPost, "/v1/api_keys", admin, tt.body)
			if resp.status != tt.status {
				t.Fatalf("status = %d %s, want %d", resp.status, resp.raw, tt.status)
			}
			if tt.status != 201 {
				if resp.body["code"] != tt.code {
					t.Fatalf("code = %v, want %s", resp.body["code"], tt.code)
				}
				return
			}
			secret, _ := resp.body["secret"].(string)
			if !strings.HasPrefix(secret, "sk_") || resp.body["created_by"] != typeid.Encode("key", adminKey.ID) || resp.body["hint"] != "…"+secret[len(secret)-4:] {
				t.Fatalf("created = %s", resp.raw)
			}
			if got := e.must(200, http.MethodGet, "/v1/me", secret, ""); got["id"] != resp.body["id"] {
				t.Fatalf("new key authenticates as %v", got["id"])
			}
		})
	}
}

func TestAuthEdgeListKeys(t *testing.T) {
	e := newEdgeEnv(t)
	_, admin := e.key("admin", auth.RoleAdmin)
	for i := range 6 {
		e.key(fmt.Sprint("k", i), auth.RoleRead)
	}

	var ids []string
	next := "/v1/api_keys?limit=2"
	for pages := 0; ; pages++ {
		if pages > 10 {
			t.Fatal("pagination does not terminate")
		}
		page := e.must(200, http.MethodGet, next, admin, "")
		data := page["data"].([]any)
		for _, item := range data {
			k := item.(map[string]any)
			if k["secret"] != nil || k["object"] != "api_key" {
				t.Fatalf("listed key = %v", k)
			}
			ids = append(ids, k["id"].(string))
		}
		if page["has_more"] != true {
			if page["next_cursor"] != nil {
				t.Fatalf("last page cursor = %v", page["next_cursor"])
			}
			break
		}
		if len(data) != 2 {
			t.Fatalf("page size = %d", len(data))
		}
		next = "/v1/api_keys?limit=2&cursor=" + page["next_cursor"].(string)
	}
	if len(ids) != 7 {
		t.Fatalf("listed %d keys, want 7", len(ids))
	}
	for i := 1; i < len(ids); i++ {
		if ids[i-1] <= ids[i] {
			t.Fatalf("keys not newest first: %v", ids)
		}
	}

	if all := e.must(200, http.MethodGet, "/v1/api_keys?limit=100", admin, ""); len(all["data"].([]any)) != 7 || all["has_more"] != false {
		t.Fatalf("full page = %v", all)
	}
	nilCursor := base64.RawURLEncoding.EncodeToString(uuid.Nil[:])
	if page := e.must(200, http.MethodGet, "/v1/api_keys?cursor="+nilCursor, admin, ""); len(page["data"].([]any)) != 7 {
		t.Fatalf("nil uuid cursor = %v, want it treated as no cursor", page)
	}
	maxCursor := base64.RawURLEncoding.EncodeToString(uuid.Max[:])
	if page := e.must(200, http.MethodGet, "/v1/api_keys?cursor="+maxCursor, admin, ""); len(page["data"].([]any)) != 7 {
		t.Fatalf("max cursor = %v", page)
	}
	oldest, _ := uuid.Parse(strings.Repeat("0", 31) + "1")
	if page := e.must(200, http.MethodGet, "/v1/api_keys?cursor="+base64.RawURLEncoding.EncodeToString(oldest[:]), admin, ""); len(page["data"].([]any)) != 0 || page["has_more"] != false {
		t.Fatalf("cursor below every key = %v", page)
	}

	for _, q := range []string{
		"limit=0", "limit=101", "limit=x", "limit=-5",
		"cursor=!!", "cursor=" + base64.RawURLEncoding.EncodeToString(make([]byte, 15)),
		"cursor=" + base64.RawURLEncoding.EncodeToString(make([]byte, 17)),
		"cursor=" + base64.StdEncoding.EncodeToString(uuid.Max[:]),
		"cursor=" + typeid.Encode("key", uuid.Max),
	} {
		resp := e.call(http.MethodGet, "/v1/api_keys?"+q, admin, "")
		if resp.status != 400 || resp.body["code"] != httpx.CodeInvalidRequest {
			t.Errorf("?%s = %d %s, want 400", q, resp.status, resp.raw)
		}
	}
}

func TestAuthEdgeRevoke(t *testing.T) {
	e := newEdgeEnv(t)
	adminKey, admin := e.key("admin", auth.RoleAdmin)
	_, backup := e.key("backup", auth.RoleAdmin)
	victim, victimToken := e.key("victim", auth.RoleWrite)
	victimID := typeid.Encode("key", victim.ID)

	first := e.must(200, http.MethodPost, "/v1/api_keys/"+victimID+"/revoke", admin, "")
	if first["revoked_at"] == nil || first["id"] != victimID || first["secret"] != nil {
		t.Fatalf("revoke = %v", first)
	}
	if resp := e.call(http.MethodGet, "/v1/me", victimToken, ""); resp.status != 401 {
		t.Fatalf("revoked key = %d", resp.status)
	}
	second := e.must(200, http.MethodPost, "/v1/api_keys/"+victimID+"/revoke", admin, "")
	if second["revoked_at"] != first["revoked_at"] {
		t.Fatalf("second revoke moved revoked_at from %v to %v", first["revoked_at"], second["revoked_at"])
	}
	if got := e.must(200, http.MethodGet, "/v1/api_keys/"+victimID, admin, ""); got["revoked_at"] != first["revoked_at"] {
		t.Fatalf("get after revoke = %v", got)
	}
	if resp := e.call(http.MethodPost, "/v1/api_keys/"+victimID+"/revoke", "", ""); resp.status != 401 {
		t.Fatalf("anonymous revoke = %d", resp.status)
	}

	tests := []struct {
		name, id string
		status   int
	}{
		{"unknown", typeid.Encode("key", uuid.Must(uuid.NewV7())), 404},
		{"wrong prefix", typeid.Encode("acct", victim.ID), 400},
		{"raw uuid", victim.ID.String(), 400},
		{"uppercase", strings.ToUpper(victimID), 400},
		{"uppercase suffix", "key_" + strings.ToUpper(strings.TrimPrefix(victimID, "key_")), 400},
		{"short", victimID[:len(victimID)-1], 400},
		{"long", victimID + "0", 400},
		{"overflowing first char", "key_8" + victimID[5:], 400},
		{"excluded letter", "key_" + strings.Repeat("u", 26), 400},
		{"prefix only", "key_", 400},
		{"token as id", victimToken, 400},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, method := range []string{http.MethodGet, http.MethodPost} {
				path := "/v1/api_keys/" + tt.id
				if method == http.MethodPost {
					path += "/revoke"
				}
				resp := e.call(method, path, admin, "")
				if resp.status != tt.status {
					t.Fatalf("%s %s = %d %s, want %d", method, path, resp.status, resp.raw, tt.status)
				}
				want := map[int]string{404: httpx.CodeNotFound, 400: httpx.CodeInvalidRequest}[tt.status]
				if resp.body["code"] != want {
					t.Fatalf("code = %v, want %s", resp.body["code"], want)
				}
			}
		})
	}

	t.Run("revoke self", func(t *testing.T) {
		self := e.must(200, http.MethodPost, "/v1/api_keys/"+typeid.Encode("key", adminKey.ID)+"/revoke", admin, "")
		if self["revoked_at"] == nil {
			t.Fatalf("self revoke = %v", self)
		}
		if resp := e.call(http.MethodGet, "/v1/api_keys", admin, ""); resp.status != 401 {
			t.Fatalf("self-revoked admin = %d, want 401", resp.status)
		}
		if n, err := e.svc.ActiveAdmins(context.Background()); err != nil || n != 1 {
			t.Fatalf("active admins = %d, %v", n, err)
		}
		e.must(200, http.MethodGet, "/v1/api_keys", backup, "")
	})

	t.Run("last admin cannot revoke itself", func(t *testing.T) {
		me := e.must(200, http.MethodGet, "/v1/me", backup, "")
		resp := e.call(http.MethodPost, "/v1/api_keys/"+me["id"].(string)+"/revoke", backup, "")
		if resp.status != http.StatusConflict || resp.body["code"] != "last_admin_key" {
			t.Fatalf("revoke last admin = %d %s", resp.status, resp.raw)
		}
		if n, err := e.svc.ActiveAdmins(context.Background()); err != nil || n != 1 {
			t.Fatalf("active admins = %d, %v", n, err)
		}
		e.must(200, http.MethodGet, "/v1/api_keys", backup, "")
	})
}

func TestAuthEdgeConcurrentRevokesKeepOneAdmin(t *testing.T) {
	for round := range 5 {
		e := newEdgeEnv(t)
		a, _ := e.key(fmt.Sprintf("a-%d", round), auth.RoleAdmin)
		b, _ := e.key(fmt.Sprintf("b-%d", round), auth.RoleAdmin)
		var (
			wg       sync.WaitGroup
			refused  atomic.Int32
			revoked  atomic.Int32
			failures = make(chan error, 2)
		)
		for _, k := range []auth.Key{a, b} {
			wg.Go(func() {
				_, err := e.svc.Revoke(context.Background(), k.ID)
				switch {
				case err == nil:
					revoked.Add(1)
				case errors.Is(err, auth.ErrLastAdmin):
					refused.Add(1)
				default:
					failures <- err
				}
			})
		}
		wg.Wait()
		close(failures)
		for err := range failures {
			t.Fatal(err)
		}
		if revoked.Load() != 1 || refused.Load() != 1 {
			t.Fatalf("revoked %d, refused %d, want 1 and 1", revoked.Load(), refused.Load())
		}
		if n, err := e.svc.ActiveAdmins(context.Background()); err != nil || n != 1 {
			t.Fatalf("active admins = %d, %v", n, err)
		}
	}
}

func TestAuthEdgeWebhookEndpointsAreAdminOnly(t *testing.T) {
	e := newEdgeEnv(t)
	_, reader := e.key("reader", auth.RoleRead)
	_, writer := e.key("writer", auth.RoleWrite)
	_, admin := e.key("admin", auth.RoleAdmin)
	for _, tt := range []struct {
		method, path string
	}{
		{http.MethodGet, "/v1/webhook_endpoints"},
		{http.MethodPost, "/v1/webhook_endpoints"},
		{http.MethodGet, "/v1/webhook_endpoints/whe_01h455vb4pex5vsknk084sn02q"},
		{http.MethodPatch, "/v1/webhook_endpoints/whe_01h455vb4pex5vsknk084sn02q"},
		{http.MethodDelete, "/v1/webhook_endpoints/whe_01h455vb4pex5vsknk084sn02q"},
	} {
		for _, token := range []string{reader, writer} {
			if resp := e.call(tt.method, tt.path, token, `{}`); resp.status != http.StatusForbidden {
				t.Errorf("%s %s as non-admin = %d, want 403", tt.method, tt.path, resp.status)
			}
		}
		if resp := e.call(tt.method, tt.path, admin, `{}`); resp.status == http.StatusForbidden || resp.status == http.StatusUnauthorized {
			t.Errorf("%s %s as admin = %d", tt.method, tt.path, resp.status)
		}
	}
	if resp := e.call(http.MethodGet, "/v1/webhook_endpointsx", writer, ""); resp.status == http.StatusForbidden {
		t.Errorf("prefix match leaked to a sibling path")
	}
}
