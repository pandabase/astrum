package tests

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/pandabase/astrum/internal/kernel/auth"
	"github.com/pandabase/astrum/internal/kernel/db"
	"github.com/pandabase/astrum/internal/kernel/events"
	"github.com/pandabase/astrum/internal/kernel/httpx"
	"github.com/pandabase/astrum/internal/kernel/idempotency"
	"github.com/pandabase/astrum/internal/kernel/testdb"
	"github.com/pandabase/astrum/internal/kernel/typeid"
)

type heStack struct {
	*heClient
	e      *env
	authn  *auth.Service
	tokens map[string]string
	ledger string
}

func heNewStack(t *testing.T) *heStack {
	t.Helper()
	e := setup(t)
	ctx := context.Background()
	authn := auth.New(e.pool, testdb.Logger())
	idem := idempotency.New(e.pool, testdb.Logger())
	idem.Scope = auth.Scope
	for name, m := range map[string]fs.FS{authn.Name(): authn.Migrations(), idem.Name(): idem.Migrations()} {
		if err := db.Migrate(ctx, e.pool, testdb.Logger(), name, m); err != nil {
			t.Fatal(err)
		}
	}
	evs := events.NewService(e.pool, testdb.Logger(), events.Config{})
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	authn.Routes(mux)
	evs.Routes(mux)
	e.m.Routes(mux)
	srv := httptest.NewServer(httpx.Logging(testdb.Logger(), authn.Middleware([]string{"/healthz"}, idem.Middleware(mux))))
	t.Cleanup(srv.Close)

	s := &heStack{heClient: &heClient{t: t, srv: srv}, e: e, authn: authn, tokens: map[string]string{}}
	for _, role := range []auth.Role{auth.RoleAdmin, auth.RoleWrite, auth.RoleRead} {
		_, token, err := authn.Create(ctx, auth.CreateInput{Name: string(role), Role: role})
		if err != nil {
			t.Fatal(err)
		}
		s.tokens[string(role)] = token
	}
	_, second, err := authn.Create(ctx, auth.CreateInput{Name: "write2", Role: auth.RoleWrite})
	if err != nil {
		t.Fatal(err)
	}
	s.tokens["write2"] = second
	s.ledger = typeid.Encode("ldg", e.ledger.ID)
	return s
}

func (s *heStack) as(role, method, path, key, body string) heResp {
	s.t.Helper()
	h := http.Header{}
	if role != "" {
		h.Set("Authorization", "Bearer "+s.tokens[role])
	}
	if key != "" {
		h.Set(idempotency.Header, key)
	}
	return s.do(method, path, h, body)
}

func (s *heStack) account(code string, allowNegative bool) string {
	s.t.Helper()
	r := s.as("write", http.MethodPost, "/v1/accounts", "", fmt.Sprintf(`{"ledger_id":%q,"code":%q,"currency":"USD","normal_side":"debit","allow_negative":%v}`, s.ledger, code, allowNegative))
	if r.status != http.StatusCreated {
		s.t.Fatalf("create account = %d %s", r.status, r.raw)
	}
	return r.body["id"].(string)
}

func heTransfer(from, to, amount string) string {
	return fmt.Sprintf(`{"entries":[{"account_id":%q,"side":"debit","amount":%q},{"account_id":%q,"side":"credit","amount":%q}]}`, to, amount, from, amount)
}

func TestHeStackRoles(t *testing.T) {
	s := heNewStack(t)
	src := s.account("src", true)
	dst := s.account("dst", false)
	unknownCat := heID("cat")

	type check struct {
		name, method, path, key, body string
		want                          map[string]int
	}
	all := func(n int) map[string]int { return map[string]int{"admin": n, "write": n, "read": n, "": 401} }
	writes := func(n int) map[string]int { return map[string]int{"admin": n, "write": n, "read": 403, "": 401} }
	admins := func(n int) map[string]int { return map[string]int{"admin": n, "write": 403, "read": 403, "": 401} }
	checks := []check{
		{"list ledgers", http.MethodGet, "/v1/ledgers", "", "", all(200)},
		{"get ledger", http.MethodGet, "/v1/ledgers/" + s.ledger, "", "", all(200)},
		{"head accounts", http.MethodHead, "/v1/accounts", "", "", all(200)},
		{"integrity", http.MethodGet, "/v1/integrity", "", "", all(200)},
		{"events", http.MethodGet, "/v1/events", "", "", all(200)},
		{"me", http.MethodGet, "/v1/me", "", "", all(200)},
		{"create ledger", http.MethodPost, "/v1/ledgers", "", `{"name":"x"}`, writes(201)},
		{"patch ledger", http.MethodPatch, "/v1/ledgers/" + s.ledger, "", `{"description":"d"}`, writes(200)},
		{"post transaction", http.MethodPost, "/v1/transactions", "", heTransfer(src, dst, "1"), writes(201)},
		{"delete unknown category", http.MethodDelete, "/v1/account_categories/" + unknownCat, "", "", writes(404)},
		{"webhook endpoint", http.MethodPost, "/v1/webhook_endpoints", "", `{"url":"https://example.com/h"}`, admins(201)},
		{"list webhook endpoints", http.MethodGet, "/v1/webhook_endpoints", "", "", admins(200)},
		{"list keys", http.MethodGet, "/v1/api_keys", "", "", admins(200)},
		{"create key", http.MethodPost, "/v1/api_keys", "", `{"name":"k","role":"read"}`, admins(201)},
	}
	n := 0
	for _, c := range checks {
		for _, role := range []string{"", "read", "write", "admin"} {
			t.Run(c.name+"/"+role, func(t *testing.T) {
				n++
				key := c.key
				if c.name == "post transaction" {
					key = fmt.Sprint("role-txn-", n)
				}
				r := s.as(role, c.method, c.path, key, c.body)
				if r.status != c.want[role] {
					t.Fatalf("%s %s as %q = %d %s, want %d", c.method, c.path, role, r.status, r.raw, c.want[role])
				}
			})
		}
	}

	t.Run("rejections with an idempotency key are not stored", func(t *testing.T) {
		body := `{"name":"stored?"}`
		heWantProblem(t, s.as("read", http.MethodPost, "/v1/ledgers", "rejected", body), 403, "forbidden")
		heWantProblem(t, s.as("", http.MethodPost, "/v1/ledgers", "rejected", body), 401, "unauthorized")
		r := s.as("read", http.MethodPost, "/v1/ledgers", "rejected", body)
		if r.status != 403 || r.header.Get(idempotency.ReplayedHeader) != "" {
			t.Fatalf("second rejection = %d replayed=%q", r.status, r.header.Get(idempotency.ReplayedHeader))
		}
	})
}

func TestHeStackJSONRules(t *testing.T) {
	s := heNewStack(t)
	deep := func(n int) string { return strings.Repeat(`{"a":`, n) + `1` + strings.Repeat(`}`, n) }
	tests := []struct {
		name        string
		body        string
		contentType string
		status      int
		code        string
	}{
		{"valid", `{"name":"ok","metadata":{"k":"v"}}`, "application/json", 201, ""},
		{"text plain content type", `{"name":"ok"}`, "text/plain", 201, ""},
		{"no content type", `{"name":"ok"}`, "", 201, ""},
		{"xml content type", `{"name":"ok"}`, "application/xml", 201, ""},
		{"wrong case member", `{"Name":"x"}`, "application/json", 400, httpx.CodeInvalidRequest},
		{"wrong case nested is fine", `{"name":"x","metadata":{"Name":"y"}}`, "application/json", 201, ""},
		{"duplicate top level", `{"name":"x","name":"y"}`, "application/json", 400, httpx.CodeInvalidRequest},
		{"duplicate in metadata", `{"name":"x","metadata":{"k":"1","k":"2"}}`, "application/json", 400, httpx.CodeInvalidRequest},
		{"duplicate nested in metadata", `{"name":"x","metadata":{"o":{"k":1,"k":1}}}`, "application/json", 400, httpx.CodeInvalidRequest},
		{"duplicate via escapes", `{"name":"x","metadata":{"k":"1","k":"2"}}`, "application/json", 400, httpx.CodeInvalidRequest},
		{"invalid utf8 in name", "{\"name\":\"\xff\"}", "application/json", 400, httpx.CodeInvalidRequest},
		{"invalid utf8 in metadata", "{\"name\":\"x\",\"metadata\":{\"k\":\"\xc0\xaf\"}}", "application/json", 400, httpx.CodeInvalidRequest},
		{"nul escape in name", `{"name":"a\u0000b"}`, "application/json", 422, "validation_error"},
		{"nul escape in metadata", `{"name":"x","metadata":{"k":"\u0000"}}`, "application/json", 422, "validation_error"},
		{"trailing data", `{"name":"x"}{"name":"y"}`, "application/json", 400, httpx.CodeInvalidRequest},
		{"trailing whitespace", "{\"name\":\"x\"}\r\n\t ", "application/json", 201, ""},
		{"empty body", ``, "application/json", 400, httpx.CodeInvalidRequest},
		{"whitespace body", "   ", "application/json", 400, httpx.CodeInvalidRequest},
		{"array", `[{"name":"x"}]`, "application/json", 400, httpx.CodeInvalidRequest},
		{"number", `1`, "application/json", 400, httpx.CodeInvalidRequest},
		{"string", `"x"`, "application/json", 400, httpx.CodeInvalidRequest},
		{"null", `null`, "application/json", 422, "validation_error"},
		{"metadata null", `{"name":"x","metadata":null}`, "application/json", 422, "validation_error"},
		{"metadata array", `{"name":"x","metadata":[]}`, "application/json", 422, "validation_error"},
		{"metadata string", `{"name":"x","metadata":"x"}`, "application/json", 422, "validation_error"},
		{"metadata empty object", `{"name":"x","metadata":{}}`, "application/json", 201, ""},
		{"metadata nested 100 deep", `{"name":"x","metadata":` + deep(100) + `}`, "application/json", 201, ""},
		{"metadata nested past json depth", `{"name":"x","metadata":` + strings.Repeat("[", 10001) + strings.Repeat("]", 10001) + `}`, "application/json", 400, httpx.CodeInvalidRequest},
		{"metadata at size limit", `{"name":"x","metadata":{"k":"` + strings.Repeat("m", 16<<10-8) + `"}}`, "application/json", 201, ""},
		{"metadata over size limit", `{"name":"x","metadata":{"k":"` + strings.Repeat("m", 16<<10-7) + `"}}`, "application/json", 422, "validation_error"},
		{"unknown member", `{"name":"x","id":"ldg_x"}`, "application/json", 400, httpx.CodeInvalidRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := http.Header{"Authorization": {"Bearer " + s.tokens["write"]}}
			if tt.contentType != "" {
				h.Set("Content-Type", tt.contentType)
			}
			r := s.do(http.MethodPost, "/v1/ledgers", h, tt.body)
			if tt.status == 201 {
				if r.status != 201 {
					t.Fatalf("status = %d %s", r.status, r.raw)
				}
				return
			}
			heWantProblem(t, r, tt.status, tt.code)
		})
	}

	t.Run("metadata round trips byte for byte", func(t *testing.T) {
		meta := `{"z":1,"a":{"n":[true,null,"é"]},"m":" "}`
		r := s.as("write", http.MethodPost, "/v1/ledgers", "", `{"name":"rt","metadata":`+meta+`}`)
		if r.status != 201 {
			t.Fatalf("create = %d %s", r.status, r.raw)
		}
		got := s.as("read", http.MethodGet, "/v1/ledgers/"+r.body["id"].(string), "", "")
		if !bytes.Contains(got.raw, []byte(`"metadata":`)) {
			t.Fatalf("ledger = %s", got.raw)
		}
		md := got.body["metadata"].(map[string]any)
		if md["z"] != float64(1) || md["m"] != " " || md["a"].(map[string]any)["n"].([]any)[2] != "é" {
			t.Fatalf("metadata = %v", md)
		}
	})

	t.Run("optional bodies", func(t *testing.T) {
		src, dst := s.account("opt-src", true), s.account("opt-dst", false)
		posted := s.as("write", http.MethodPost, "/v1/transactions", "opt-post", heTransfer(src, dst, "5"))
		if posted.status != 201 {
			t.Fatalf("post = %d %s", posted.status, posted.raw)
		}
		txn := posted.body["id"].(string)
		for i, body := range []string{"", "  \n", `{}`, `null`} {
			pending := s.as("write", http.MethodPost, "/v1/transactions", fmt.Sprint("opt-pending-", i), strings.TrimSuffix(heTransfer(src, dst, "1"), "}")+`,"status":"pending"}`)
			if pending.status != 201 {
				t.Fatalf("pending = %d %s", pending.status, pending.raw)
			}
			r := s.as("write", http.MethodPost, "/v1/transactions/"+pending.body["id"].(string)+"/post", "", body)
			if r.status != 200 || r.body["status"] != "posted" {
				t.Fatalf("post pending with %q = %d %s", body, r.status, r.raw)
			}
		}
		for _, body := range []string{`[]`, `{"entries":1}`, `{"nope":1}`, `{}x`} {
			heWantProblem(t, s.as("write", http.MethodPost, "/v1/transactions/"+txn+"/reverse", "rev-bad-"+body, body), 400, httpx.CodeInvalidRequest)
		}
		r := s.as("write", http.MethodPost, "/v1/transactions/"+txn+"/reverse", "rev-empty", "")
		if r.status != 201 || r.body["reverses_id"] != txn {
			t.Fatalf("reverse with empty body = %d %s", r.status, r.raw)
		}
	})
}

func TestHeStackBodyLimits(t *testing.T) {
	s := heNewStack(t)
	pad := func(body string, size int) string { return body + strings.Repeat(" ", size-len(body)) }
	ledger := `{"name":"big"}`

	t.Run("1 MiB decode limit", func(t *testing.T) {
		if r := s.as("write", http.MethodPost, "/v1/ledgers", "", pad(ledger, 1<<20)); r.status != 201 {
			t.Fatalf("at limit = %d %s", r.status, r.raw)
		}
		heWantProblem(t, s.as("write", http.MethodPost, "/v1/ledgers", "", pad(ledger, 1<<20+1)), 400, httpx.CodeInvalidRequest)
		over := s.as("write", http.MethodPost, "/v1/ledgers", "over-1m", pad(ledger, 1<<20+1))
		heWantProblem(t, over, 400, httpx.CodeInvalidRequest)
		if !strings.Contains(over.body["detail"].(string), "request body too large") {
			t.Fatalf("detail = %v", over.body["detail"])
		}
		replay := s.as("write", http.MethodPost, "/v1/ledgers", "over-1m", pad(ledger, 1<<20+1))
		if replay.status != 400 || replay.header.Get(idempotency.ReplayedHeader) != "true" || !bytes.Equal(replay.raw, over.raw) {
			t.Fatalf("stored 400 replay = %d %q", replay.status, replay.raw)
		}
		heWantProblem(t, s.as("write", http.MethodPost, "/v1/ledgers", "over-1m", ledger), 422, httpx.CodeIdempotencyReuse)
	})

	t.Run("16 MiB middleware limit", func(t *testing.T) {
		heWantProblem(t, s.as("write", http.MethodPost, "/v1/ledgers", "over-16m", pad(ledger, httpx.MaxBulkBodyBytes+1)), 413, httpx.CodeRequestTooLarge)
		heWantProblem(t, s.as("write", http.MethodPost, "/v1/ledgers", "", pad(ledger, httpx.MaxBulkBodyBytes+1)), 400, httpx.CodeInvalidRequest)
		if r := s.as("write", http.MethodPost, "/v1/ledgers", "over-16m", ledger); r.status != 201 {
			t.Fatalf("key after 413 = %d %s, want it unused", r.status, r.raw)
		}
	})

	t.Run("bulk decode limit", func(t *testing.T) {
		bulk := `{"transactions":[]}`
		for name, size := range map[string]int{"over 1 MiB": 2 << 20, "at 16 MiB": httpx.MaxBulkBodyBytes} {
			r := s.as("write", http.MethodPost, "/v1/bulk_requests", "bulk-"+name, pad(bulk, size))
			if r.status != 422 || r.body["code"] != "validation_error" {
				t.Fatalf("%s = %d %s, want the body decoded and rejected as empty", name, r.status, r.raw)
			}
		}
		heWantProblem(t, s.as("write", http.MethodPost, "/v1/bulk_requests", "bulk-over", pad(bulk, httpx.MaxBulkBodyBytes+1)), 413, httpx.CodeRequestTooLarge)
	})
}

func TestHeStackIdempotency(t *testing.T) {
	s := heNewStack(t)
	src, dst := s.account("idem-src", true), s.account("idem-dst", false)

	t.Run("replay is byte identical", func(t *testing.T) {
		first := s.as("write", http.MethodPost, "/v1/ledgers", "L1", `{"name":"once"}`)
		second := s.as("write", http.MethodPost, "/v1/ledgers", "L1", `{"name":"once"}`)
		if first.status != 201 || second.status != 201 || !bytes.Equal(first.raw, second.raw) || second.header.Get(idempotency.ReplayedHeader) != "true" {
			t.Fatalf("first %d, second %d replayed=%q", first.status, second.status, second.header.Get(idempotency.ReplayedHeader))
		}
		if first.header.Get("Content-Type") != second.header.Get("Content-Type") {
			t.Fatal("content type differs on replay")
		}
		heWantProblem(t, s.as("write", http.MethodPost, "/v1/ledgers", "L1", `{"name":"twice"}`), 422, httpx.CodeIdempotencyReuse)
		heWantProblem(t, s.as("write", http.MethodPatch, "/v1/ledgers/"+first.body["id"].(string), "L1", `{"name":"once"}`), 422, httpx.CodeIdempotencyReuse)
		if n := s.heCountLedgers("once"); n != 1 {
			t.Fatalf("ledgers named once = %d", n)
		}
	})

	t.Run("stored 4xx replays the original request id", func(t *testing.T) {
		first := s.as("write", http.MethodPost, "/v1/ledgers", "L2", `{"name":""}`)
		heWantProblem(t, first, 422, "validation_error")
		second := s.as("write", http.MethodPost, "/v1/ledgers", "L2", `{"name":""}`)
		if !bytes.Equal(first.raw, second.raw) || second.header.Get(idempotency.ReplayedHeader) != "true" {
			t.Fatalf("4xx replay = %d %s", second.status, second.raw)
		}
		if second.body["request_id"] != first.header.Get("X-Request-ID") || second.header.Get("X-Request-ID") == first.header.Get("X-Request-ID") {
			t.Fatalf("replay request ids: body %v, header %s, original %s", second.body["request_id"], second.header.Get("X-Request-ID"), first.header.Get("X-Request-ID"))
		}
	})

	t.Run("keys are scoped per api key", func(t *testing.T) {
		a := s.as("write", http.MethodPost, "/v1/ledgers", "shared", `{"name":"scope-a"}`)
		b := s.as("write2", http.MethodPost, "/v1/ledgers", "shared", `{"name":"scope-b"}`)
		c := s.as("admin", http.MethodPost, "/v1/ledgers", "shared", `{"name":"scope-a"}`)
		if a.status != 201 || b.status != 201 || c.status != 201 || a.body["id"] == c.body["id"] || c.header.Get(idempotency.ReplayedHeader) != "" {
			t.Fatalf("scoped = %d %d %d", a.status, b.status, c.status)
		}
	})

	t.Run("ledger transaction keys are global across api keys", func(t *testing.T) {
		body := heTransfer(src, dst, "3")
		first := s.as("write", http.MethodPost, "/v1/transactions", "txn-shared", body)
		same := s.as("write2", http.MethodPost, "/v1/transactions", "txn-shared", body)
		if first.status != 201 || same.status != 201 || same.body["id"] != first.body["id"] || same.header.Get(idempotency.ReplayedHeader) != "" {
			t.Fatalf("same body from another key = %d %v (first %v)", same.status, same.body["id"], first.body["id"])
		}
		heWantProblem(t, s.as("admin", http.MethodPost, "/v1/transactions", "txn-shared", heTransfer(src, dst, "4")), 422, httpx.CodeIdempotencyReuse)
	})

	t.Run("get ignores the key", func(t *testing.T) {
		for range 2 {
			if r := s.as("read", http.MethodGet, "/v1/ledgers", "get-key", ""); r.status != 200 || r.header.Get(idempotency.ReplayedHeader) != "" {
				t.Fatalf("GET = %d replayed=%q", r.status, r.header.Get(idempotency.ReplayedHeader))
			}
		}
	})

	t.Run("key length", func(t *testing.T) {
		if r := s.as("write", http.MethodPost, "/v1/transactions", strings.Repeat("k", 255), heTransfer(src, dst, "1")); r.status != 201 || r.body["idempotency_key"] != strings.Repeat("k", 255) {
			t.Fatalf("255 byte key = %d %s", r.status, r.raw)
		}
		heWantProblem(t, s.as("write", http.MethodPost, "/v1/transactions", strings.Repeat("k", 256), heTransfer(src, dst, "1")), 400, httpx.CodeInvalidRequest)
	})

	t.Run("concurrent identical requests create one ledger", func(t *testing.T) {
		const clients = 10
		var wg sync.WaitGroup
		statuses := make(chan int, clients)
		for range clients {
			wg.Go(func() {
				statuses <- s.as("write", http.MethodPost, "/v1/ledgers", "race", `{"name":"raced"}`).status
			})
		}
		wg.Wait()
		close(statuses)
		for st := range statuses {
			if st != 201 && st != 409 {
				t.Fatalf("status = %d", st)
			}
		}
		if n := s.heCountLedgers("raced"); n != 1 {
			t.Fatalf("ledgers created = %d, want 1", n)
		}
	})

	t.Run("secret responses are shown once and never stored", func(t *testing.T) {
		for _, c := range []struct{ path, key, body string }{
			{"/v1/api_keys", "mint", `{"name":"minted","role":"read"}`},
			{"/v1/webhook_endpoints", "hook", `{"url":"https://example.com/h"}`},
		} {
			first := s.as("admin", http.MethodPost, c.path, c.key, c.body)
			secret, _ := first.body["secret"].(string)
			if first.status != 201 || secret == "" || first.header.Get("Cache-Control") != "no-store" {
				t.Fatalf("%s create = %d %v", c.path, first.status, first.body)
			}
			second := s.as("admin", http.MethodPost, c.path, c.key, c.body)
			if second.status != 409 || second.body["code"] != "idempotency_key_completed" || second.body["secret"] != nil ||
				second.header.Get(idempotency.ReplayedHeader) != "true" {
				t.Fatalf("%s replay = %d %v", c.path, second.status, second.body)
			}
			var stored []byte
			if err := s.e.pool.QueryRow(context.Background(), `SELECT response_body FROM idempotency_keys WHERE key LIKE '%:'||$1`, c.key).Scan(&stored); err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(stored, []byte(secret)) {
				t.Fatalf("%s: stored response contains the secret", c.path)
			}
		}
	})
}

func (s *heStack) heCountLedgers(name string) int {
	s.t.Helper()
	var n int
	if err := s.e.pool.QueryRow(context.Background(), `SELECT count(*) FROM ledger_ledgers WHERE name = $1`, name).Scan(&n); err != nil {
		s.t.Fatal(err)
	}
	return n
}

func TestHeStackPathTricks(t *testing.T) {
	s := heNewStack(t)
	for _, tt := range []struct {
		method, path string
		status       int
	}{
		{http.MethodGet, "/v1/ledgers/x%2F..%2Fapi_keys", 400},
		{http.MethodGet, "/v1/ledgers/..%2F..%2Fv1%2Fapi_keys", 400},
		{http.MethodPatch, "/v1/ledgers/x%2F..%2Fapi_keys", 400},
		{http.MethodGet, "/v1/ledgers/../api_keys", 307},
		{http.MethodPost, "/v1/ledgers/..%2Fapi_keys", 405},
		{http.MethodGet, "/v1/api_keys%2F", 403},
	} {
		t.Run(tt.method+" "+tt.path, func(t *testing.T) {
			r := s.as("write", tt.method, tt.path, "", `{"name":"n","role":"admin"}`)
			if r.status != tt.status {
				t.Fatalf("status = %d %s, want %d", r.status, r.raw, tt.status)
			}
			if bytes.Contains(r.raw, []byte(`"api_key"`)) || bytes.Contains(r.raw, []byte("sk_")) {
				t.Fatalf("write key reached key admin: %s", r.raw)
			}
		})
	}
}
