package tests

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/pandabase/astrum/internal/kernel/httpx"
	"github.com/pandabase/astrum/internal/kernel/testdb"
	"github.com/pandabase/astrum/internal/kernel/typeid"
)

type heResp struct {
	status int
	header http.Header
	raw    []byte
	body   map[string]any
}

type heClient struct {
	t   *testing.T
	srv *httptest.Server
}

func heNewLedgerServer(t *testing.T) (*env, *heClient) {
	t.Helper()
	e := setup(t)
	mux := http.NewServeMux()
	e.m.Routes(mux)
	srv := httptest.NewServer(httpx.Logging(testdb.Logger(), mux))
	t.Cleanup(srv.Close)
	return e, &heClient{t: t, srv: srv}
}

func (c *heClient) do(method, path string, header http.Header, body string) heResp {
	c.t.Helper()
	req, err := http.NewRequest(method, c.srv.URL+path, strings.NewReader(body))
	if err != nil {
		c.t.Fatal(err)
	}
	for k, v := range header {
		req.Header[k] = v
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		c.t.Fatal(err)
	}
	out := heResp{status: resp.StatusCode, header: resp.Header, raw: raw}
	_ = json.Unmarshal(raw, &out.body)
	return out
}

func heKeyHeader(key string) http.Header {
	if key == "" {
		return nil
	}
	return http.Header{"Idempotency-Key": {key}}
}

func heID(prefix string) string {
	return typeid.Encode(prefix, uuid.Must(uuid.NewV7()))
}

func heMalformed(prefix string) map[string]string {
	valid := heID(prefix)
	suffix := strings.TrimPrefix(valid, prefix+"_")
	other := "ldg"
	if prefix == "ldg" {
		other = "acct"
	}
	return map[string]string{
		"wrong prefix":      typeid.Encode(other, uuid.Must(uuid.NewV7())),
		"raw uuid":          uuid.NewString(),
		"uppercase":         strings.ToUpper(valid),
		"uppercase suffix":  prefix + "_" + strings.ToUpper(suffix),
		"short":             valid[:len(valid)-1],
		"long":              valid + "0",
		"overflow":          prefix + "_8" + suffix[1:],
		"excluded letter":   prefix + "_" + suffix[:25] + "u",
		"prefix only":       prefix + "_",
		"no separator":      prefix + suffix,
		"doubled prefix":    prefix + "_" + valid,
		"hyphen separator":  prefix + "-" + suffix,
		"encoded traversal": "x%2F..%2Fapi_keys",
		"encoded dot dot":   "..%2Fapi_keys",
	}
}

type heRoute struct {
	method, pattern, body string
	key                   bool
}

func heIDRoutes() map[string][]heRoute {
	return map[string][]heRoute{
		"ldg": {
			{http.MethodGet, "/v1/ledgers/%s", "", false},
			{http.MethodPatch, "/v1/ledgers/%s", `{"name":"x"}`, false},
		},
		"cat": {
			{http.MethodGet, "/v1/account_categories/%s", "", false},
			{http.MethodPatch, "/v1/account_categories/%s", `{"name":"x"}`, false},
			{http.MethodDelete, "/v1/account_categories/%s", "", false},
			{http.MethodPut, "/v1/account_categories/%s/accounts/" + heID("acct"), "", false},
			{http.MethodDelete, "/v1/account_categories/%s/accounts/" + heID("acct"), "", false},
			{http.MethodPut, "/v1/account_categories/%s/categories/" + heID("cat"), "", false},
			{http.MethodDelete, "/v1/account_categories/%s/categories/" + heID("cat"), "", false},
		},
		"bm": {
			{http.MethodGet, "/v1/balance_monitors/%s", "", false},
			{http.MethodPatch, "/v1/balance_monitors/%s", `{"description":"x"}`, false},
			{http.MethodDelete, "/v1/balance_monitors/%s", "", false},
		},
		"acct": {
			{http.MethodGet, "/v1/accounts/%s", "", false},
			{http.MethodPatch, "/v1/accounts/%s", `{"name":"x"}`, false},
			{http.MethodPost, "/v1/accounts/%s/freeze", "", false},
			{http.MethodPost, "/v1/accounts/%s/unfreeze", "", false},
			{http.MethodPost, "/v1/accounts/%s/close", "", false},
			{http.MethodGet, "/v1/accounts/%s/entries", "", false},
			{http.MethodGet, "/v1/accounts/%s/balances", "", false},
		},
		"blk": {
			{http.MethodGet, "/v1/bulk_requests/%s", "", false},
			{http.MethodGet, "/v1/bulk_requests/%s/results", "", false},
		},
		"stl":  {{http.MethodGet, "/v1/settlements/%s", "", false}},
		"stmt": {{http.MethodGet, "/v1/statements/%s", "", false}},
		"txn": {
			{http.MethodGet, "/v1/transactions/%s", "", false},
			{http.MethodPatch, "/v1/transactions/%s", `{"description":"x"}`, false},
			{http.MethodPost, "/v1/transactions/%s/post", "", false},
			{http.MethodPost, "/v1/transactions/%s/archive", "", false},
			{http.MethodPost, "/v1/transactions/%s/reverse", "", true},
		},
		"hold": {
			{http.MethodGet, "/v1/holds/%s", "", false},
			{http.MethodPost, "/v1/holds/%s/capture", fmt.Sprintf(`{"destination_account_id":%q,"amount":"1"}`, heID("acct")), true},
			{http.MethodPost, "/v1/holds/%s/void", "", false},
		},
		"sched": {
			{http.MethodGet, "/v1/scheduled_transactions/%s", "", false},
			{http.MethodPost, "/v1/scheduled_transactions/%s/cancel", "", false},
		},
	}
}

func heWantProblem(t *testing.T, r heResp, status int, code string) {
	t.Helper()
	if r.status != status {
		t.Fatalf("status = %d %s, want %d", r.status, r.raw, status)
	}
	if r.body["code"] != code || r.body["status"] != float64(status) || r.header.Get("Content-Type") != "application/problem+json" {
		t.Fatalf("problem = %s (%s), want code %s", r.raw, r.header.Get("Content-Type"), code)
	}
}

func TestHeRoutesMalformedIDs(t *testing.T) {
	_, c := heNewLedgerServer(t)
	for prefix, routes := range heIDRoutes() {
		for _, rt := range routes {
			for name, id := range heMalformed(prefix) {
				t.Run(fmt.Sprintf("%s %s/%s", rt.method, rt.pattern, name), func(t *testing.T) {
					key := ""
					if rt.key {
						key = "k-" + uuid.NewString()
					}
					r := c.do(rt.method, fmt.Sprintf(rt.pattern, id), heKeyHeader(key), rt.body)
					heWantProblem(t, r, http.StatusBadRequest, httpx.CodeInvalidRequest)
					if !strings.Contains(r.body["detail"].(string), "typeid: ") {
						t.Fatalf("detail = %v", r.body["detail"])
					}
				})
			}
		}
	}
}

func TestHeRoutesUnknownIDs(t *testing.T) {
	_, c := heNewLedgerServer(t)
	for prefix, routes := range heIDRoutes() {
		for _, rt := range routes {
			t.Run(rt.method+" "+rt.pattern, func(t *testing.T) {
				key := ""
				if rt.key {
					key = "k-" + uuid.NewString()
				}
				r := c.do(rt.method, fmt.Sprintf(rt.pattern, heID(prefix)), heKeyHeader(key), rt.body)
				heWantProblem(t, r, http.StatusNotFound, httpx.CodeNotFound)
			})
		}
	}
}

func TestHeRoutesMalformedMembers(t *testing.T) {
	_, c := heNewLedgerServer(t)
	cat := heID("cat")
	for _, method := range []string{http.MethodPut, http.MethodDelete} {
		for _, kind := range []struct{ segment, prefix string }{{"accounts", "acct"}, {"categories", "cat"}} {
			for name, member := range heMalformed(kind.prefix) {
				t.Run(method+" "+kind.segment+"/"+name, func(t *testing.T) {
					r := c.do(method, "/v1/account_categories/"+cat+"/"+kind.segment+"/"+member, nil, "")
					heWantProblem(t, r, http.StatusBadRequest, httpx.CodeInvalidRequest)
				})
			}
		}
	}
}

func TestHeRoutesIdempotencyKeyRequired(t *testing.T) {
	_, c := heNewLedgerServer(t)
	for _, rt := range []heRoute{
		{http.MethodPost, "/v1/transactions", `{}`, true},
		{http.MethodPost, "/v1/transactions/batch", `{}`, true},
		{http.MethodPost, "/v1/holds", `{}`, true},
		{http.MethodPost, "/v1/scheduled_transactions", `{}`, true},
		{http.MethodPost, "/v1/bulk_requests", `{}`, true},
		{http.MethodPost, "/v1/settlements", `{}`, true},
		{http.MethodPost, "/v1/transactions/" + heID("txn") + "/reverse", ``, true},
		{http.MethodPost, "/v1/holds/" + heID("hold") + "/capture", `{}`, true},
		{http.MethodPost, "/v1/transactions", `not even json`, true},
	} {
		t.Run(rt.method+" "+rt.pattern, func(t *testing.T) {
			for _, h := range []http.Header{nil, {"Idempotency-Key": {""}}, {"Idempotency-Key": {"   "}}, {"idempotency-key": {""}}} {
				r := c.do(rt.method, rt.pattern, h, rt.body)
				heWantProblem(t, r, http.StatusBadRequest, "idempotency_key_required")
				if r.body["detail"] != "Idempotency-Key header is required" {
					t.Fatalf("detail = %v", r.body["detail"])
				}
			}
		})
	}
	t.Run("malformed id wins over missing key", func(t *testing.T) {
		heWantProblem(t, c.do(http.MethodPost, "/v1/transactions/nope/reverse", nil, ""), http.StatusBadRequest, httpx.CodeInvalidRequest)
	})
	t.Run("lowercase header name is canonicalized", func(t *testing.T) {
		r := c.do(http.MethodPost, "/v1/transactions", http.Header{"idempotency-key": {"x"}}, `{"entries":[]}`)
		if r.body["code"] == "idempotency_key_required" {
			t.Fatalf("lowercase header ignored: %s", r.raw)
		}
	})
}

func TestHeRoutesMethodNotAllowed(t *testing.T) {
	_, c := heNewLedgerServer(t)
	tests := []struct {
		method, path string
		status       int
		allow        []string
	}{
		{http.MethodDelete, "/v1/ledgers", 405, []string{"GET", "HEAD", "POST"}},
		{http.MethodPut, "/v1/ledgers/" + heID("ldg"), 405, []string{"GET", "HEAD", "PATCH"}},
		{http.MethodDelete, "/v1/accounts/" + heID("acct"), 405, []string{"GET", "HEAD", "PATCH"}},
		{http.MethodGet, "/v1/accounts/" + heID("acct") + "/freeze", 405, []string{"POST"}},
		{http.MethodPost, "/v1/integrity", 405, []string{"GET", "HEAD"}},
		{http.MethodPatch, "/v1/holds/" + heID("hold"), 405, []string{"GET", "HEAD"}},
		{http.MethodPost, "/v1/entries", 405, []string{"GET", "HEAD"}},
		{http.MethodDelete, "/v1/currencies/USD", 405, []string{"GET", "HEAD"}},
		{http.MethodGet, "/v1/account_categories/" + heID("cat") + "/accounts/" + heID("acct"), 405, []string{"DELETE", "PUT"}},
		{http.MethodPut, "/v1/transactions/batch", 405, []string{"GET", "HEAD", "PATCH", "POST"}},
		{http.MethodGet, "/v1/nope", 404, nil},
		{http.MethodGet, "/v1/ledgers/" + heID("ldg") + "/accounts", 404, nil},
		{http.MethodGet, "/v1/LEDGERS", 404, nil},
	}
	for _, tt := range tests {
		t.Run(tt.method+" "+tt.path, func(t *testing.T) {
			r := c.do(tt.method, tt.path, nil, "")
			if r.status != tt.status {
				t.Fatalf("status = %d %s, want %d", r.status, r.raw, tt.status)
			}
			if !strings.HasPrefix(r.header.Get("Content-Type"), "text/plain") {
				t.Fatalf("Content-Type = %q, want the mux's plain text", r.header.Get("Content-Type"))
			}
			if tt.allow == nil {
				return
			}
			got := strings.Split(r.header.Get("Allow"), ", ")
			if strings.Join(got, ",") != strings.Join(tt.allow, ",") {
				t.Fatalf("Allow = %v, want %v", got, tt.allow)
			}
		})
	}

	t.Run("batch is not an id", func(t *testing.T) {
		heWantProblem(t, c.do(http.MethodGet, "/v1/transactions/batch", nil, ""), http.StatusBadRequest, httpx.CodeInvalidRequest)
	})
	t.Run("head on a list", func(t *testing.T) {
		r := c.do(http.MethodHead, "/v1/ledgers", nil, "")
		if r.status != 200 || len(r.raw) != 0 {
			t.Fatalf("HEAD = %d %q", r.status, r.raw)
		}
	})
	t.Run("dot segments redirect", func(t *testing.T) {
		r := c.do(http.MethodGet, "/v1/ledgers/../integrity", nil, "")
		if r.status != http.StatusTemporaryRedirect || r.header.Get("Location") != "/v1/integrity" {
			t.Fatalf("status = %d Location %q", r.status, r.header.Get("Location"))
		}
	})
	t.Run("encoded dot segment id", func(t *testing.T) {
		r := c.do(http.MethodGet, "/v1/ledgers/%2e%2e", nil, "")
		if r.status == http.StatusOK {
			t.Fatalf("encoded .. resolved to %s", r.raw)
		}
	})
}

func heCursor(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func TestHeRoutesListQueryValidation(t *testing.T) {
	e, c := heNewLedgerServer(t)
	acct := typeid.Encode("acct", e.open.ID)
	uuidRoutes := []string{
		"/v1/ledgers", "/v1/account_categories", "/v1/balance_monitors", "/v1/accounts", "/v1/settlements",
		"/v1/statements", "/v1/transactions", "/v1/holds", "/v1/scheduled_transactions",
	}
	int64Routes := []string{"/v1/entries", "/v1/accounts/" + acct + "/entries"}
	all := append(append([]string{"/v1/currencies"}, uuidRoutes...), int64Routes...)

	for _, path := range all {
		t.Run(path, func(t *testing.T) {
			for _, q := range []string{"limit=1", "limit=100", "limit=", "unknown=1", "cursor="} {
				if r := c.do(http.MethodGet, path+"?"+q, nil, ""); r.status != 200 || r.body["object"] != "list" {
					t.Fatalf("?%s = %d %s", q, r.status, r.raw)
				}
			}
			for _, q := range []string{"limit=0", "limit=101", "limit=-1", "limit=abc", "limit=1.5", "limit=%201", "cursor=!!!", "cursor=a", "cursor=" + heCursor(make([]byte, 3))} {
				r := c.do(http.MethodGet, path+"?"+q, nil, "")
				heWantProblem(t, r, http.StatusBadRequest, httpx.CodeInvalidRequest)
			}
		})
	}
	for _, path := range uuidRoutes {
		t.Run(path+" uuid cursor", func(t *testing.T) {
			for _, n := range []int{8, 15, 17} {
				heWantProblem(t, c.do(http.MethodGet, path+"?cursor="+heCursor(make([]byte, n)), nil, ""), 400, httpx.CodeInvalidRequest)
			}
			if r := c.do(http.MethodGet, path+"?cursor="+heCursor(uuid.Max[:]), nil, ""); r.status != 200 {
				t.Fatalf("max uuid cursor = %d %s", r.status, r.raw)
			}
			heWantProblem(t, c.do(http.MethodGet, path+"?cursor="+base64.StdEncoding.EncodeToString(uuid.Max[:]), nil, ""), 400, httpx.CodeInvalidRequest)
		})
	}
	for _, path := range int64Routes {
		t.Run(path+" int64 cursor", func(t *testing.T) {
			for _, n := range []int{7, 9, 16} {
				heWantProblem(t, c.do(http.MethodGet, path+"?cursor="+heCursor(make([]byte, n)), nil, ""), 400, httpx.CodeInvalidRequest)
			}
			for _, v := range []int64{0, 1 << 62} {
				if r := c.do(http.MethodGet, path+"?cursor="+heCursor(binary.BigEndian.AppendUint64(nil, uint64(v))), nil, ""); r.status != 200 {
					t.Fatalf("cursor %d = %d %s", v, r.status, r.raw)
				}
			}
			negative := c.do(http.MethodGet, path+"?cursor="+heCursor(binary.BigEndian.AppendUint64(nil, 1<<63)), nil, "")
			if path == "/v1/entries" {
				if negative.status != 200 {
					t.Fatalf("negative cursor = %d %s", negative.status, negative.raw)
				}
				return
			}
			heWantProblem(t, negative, http.StatusUnprocessableEntity, "validation_error")
		})
	}
	t.Run("currency cursor", func(t *testing.T) {
		for _, raw := range []string{"usd", "US", "U$D", "1SD", "_SD", "USD\x00", strings.Repeat("U", 64)} {
			heWantProblem(t, c.do(http.MethodGet, "/v1/currencies?cursor="+heCursor([]byte(raw)), nil, ""), 400, httpx.CodeInvalidRequest)
		}
		if r := c.do(http.MethodGet, "/v1/currencies?cursor="+heCursor([]byte("USD")), nil, ""); r.status != 200 {
			t.Fatalf("USD cursor = %d %s", r.status, r.raw)
		}
	})
	t.Run("bulk results cursor", func(t *testing.T) {
		blk := heID("blk")
		heWantProblem(t, c.do(http.MethodGet, "/v1/bulk_requests/"+blk+"/results?cursor="+heCursor(make([]byte, 4)), nil, ""), 400, httpx.CodeInvalidRequest)
		heWantProblem(t, c.do(http.MethodGet, "/v1/bulk_requests/"+blk+"/results?limit=0", nil, ""), 400, httpx.CodeInvalidRequest)
		heWantProblem(t, c.do(http.MethodGet, "/v1/bulk_requests/"+blk+"/results?limit=5", nil, ""), 404, httpx.CodeNotFound)
	})
}

func TestHeRoutesFilterValidation(t *testing.T) {
	e, c := heNewLedgerServer(t)
	acct := typeid.Encode("acct", e.open.ID)
	ldg := typeid.Encode("ldg", e.ledger.ID)
	idFilters := map[string][]string{
		"/v1/account_categories":     {"ledger_id", "parent_id", "account_id"},
		"/v1/balance_monitors":       {"account_id"},
		"/v1/accounts":               {"ledger_id", "category_id"},
		"/v1/transactions":           {"account_id", "ledger_id"},
		"/v1/entries":                {"ledger_id", "account_id", "transaction_id", "statement_id", "settlement_id"},
		"/v1/statements":             {"account_id"},
		"/v1/settlements":            {"account_id"},
		"/v1/holds":                  {"account_id"},
		"/v1/scheduled_transactions": nil,
	}
	for path, params := range idFilters {
		for _, p := range params {
			t.Run(path+" "+p, func(t *testing.T) {
				for _, bad := range []string{"nope", uuid.NewString(), heID("wd"), strings.ToUpper(heID("acct"))} {
					r := c.do(http.MethodGet, path+"?"+p+"="+url.QueryEscape(bad), nil, "")
					heWantProblem(t, r, http.StatusBadRequest, httpx.CodeInvalidRequest)
					if !strings.HasPrefix(r.body["detail"].(string), p+": ") {
						t.Fatalf("detail = %v, want it to name %s", r.body["detail"], p)
					}
				}
			})
		}
	}

	metadataRoutes := []string{"/v1/ledgers", "/v1/account_categories", "/v1/accounts", "/v1/transactions", "/v1/entries"}
	for _, path := range metadataRoutes {
		t.Run(path+" metadata", func(t *testing.T) {
			for _, q := range []string{"metadata[k]=v", "metadata[k]=", "metadata=v", "metadata[a][b]=v", "metadata%5Bk%5D=v", "metadata[k%20x]=v"} {
				if r := c.do(http.MethodGet, path+"?"+q, nil, ""); r.status != 200 {
					t.Fatalf("?%s = %d %s", q, r.status, r.raw)
				}
			}
			for _, q := range []string{"metadata[]=v", "metadata[k]=1&metadata[k]=2", "metadata[k=v", "metadata[=v"} {
				r := c.do(http.MethodGet, path+"?"+q, nil, "")
				heWantProblem(t, r, http.StatusBadRequest, httpx.CodeInvalidRequest)
			}
		})
	}

	effectiveRoutes := []string{"/v1/transactions", "/v1/entries", "/v1/accounts/" + acct + "/balances"}
	for _, path := range effectiveRoutes {
		t.Run(path+" effective bounds", func(t *testing.T) {
			sep := "?"
			for _, q := range []string{
				"effective_at_lower_bound=2024-01-01T00:00:00Z",
				"effective_at_upper_bound=2024-01-01T00:00:00.123456789%2B05:30",
				"effective_at_lower_bound=",
			} {
				if r := c.do(http.MethodGet, path+sep+q, nil, ""); r.status != 200 {
					t.Fatalf("?%s = %d %s", q, r.status, r.raw)
				}
			}
			inverted := c.do(http.MethodGet, path+sep+"effective_at_lower_bound=2030-01-01T00:00:00Z&effective_at_upper_bound=2020-01-01T00:00:00Z", nil, "")
			heWantProblem(t, inverted, http.StatusUnprocessableEntity, "validation_error")
			for _, q := range []string{
				"effective_at_lower_bound=yesterday",
				"effective_at_lower_bound=2024-01-01",
				"effective_at_upper_bound=2024-01-01T00:00:00",
				"effective_at_upper_bound=2024-01-01T00:00:00+05:30",
				"effective_at_upper_bound=1700000000",
				"effective_at_lower_bound=2024-13-01T00:00:00Z",
			} {
				r := c.do(http.MethodGet, path+sep+q, nil, "")
				heWantProblem(t, r, http.StatusBadRequest, httpx.CodeInvalidRequest)
				if !strings.HasSuffix(r.body["detail"].(string), "must be an RFC 3339 time") {
					t.Fatalf("detail = %v", r.body["detail"])
				}
			}
		})
	}

	t.Run("settled", func(t *testing.T) {
		for _, v := range []string{"true", "false", "1", "0", "t", "F", "TRUE", "False", ""} {
			if r := c.do(http.MethodGet, "/v1/entries?settled="+v, nil, ""); r.status != 200 {
				t.Fatalf("settled=%s = %d %s", v, r.status, r.raw)
			}
		}
		for _, v := range []string{"yes", "no", "2", "tRuE", "%20true"} {
			r := c.do(http.MethodGet, "/v1/entries?settled="+v, nil, "")
			heWantProblem(t, r, http.StatusBadRequest, httpx.CodeInvalidRequest)
			if r.body["detail"] != "settled must be true or false" {
				t.Fatalf("detail = %v", r.body["detail"])
			}
		}
	})

	t.Run("enum filters", func(t *testing.T) {
		for _, q := range []string{
			"/v1/accounts?status=OPEN", "/v1/transactions?status=done", "/v1/entries?side=left",
			"/v1/entries?status=nope", "/v1/holds?status=Pending", "/v1/scheduled_transactions?status=x",
		} {
			heWantProblem(t, c.do(http.MethodGet, q, nil, ""), http.StatusUnprocessableEntity, "validation_error")
		}
		for _, q := range []string{"/v1/accounts?ledger_id=" + ldg + "&status=open&currency=USD", "/v1/entries?side=debit&status=posted", "/v1/transactions?status=pending"} {
			if r := c.do(http.MethodGet, q, nil, ""); r.status != 200 {
				t.Fatalf("%s = %d %s", q, r.status, r.raw)
			}
		}
	})

	t.Run("currency path", func(t *testing.T) {
		if r := c.do(http.MethodGet, "/v1/currencies/USD", nil, ""); r.status != 200 || r.body["object"] != "currency" {
			t.Fatalf("USD = %d %s", r.status, r.raw)
		}
		heWantProblem(t, c.do(http.MethodGet, "/v1/currencies/ZZZ", nil, ""), http.StatusNotFound, httpx.CodeNotFound)
		for _, code := range []string{"usd", "U", "%2E%2E", "U%24D", strings.Repeat("A", 300)} {
			heWantProblem(t, c.do(http.MethodGet, "/v1/currencies/"+code, nil, ""), http.StatusNotFound, httpx.CodeNotFound)
		}
	})

	t.Run("nul bytes and invalid utf8 are client errors", func(t *testing.T) {
		for _, target := range []string{
			"/v1/currencies/USD%00",
			"/v1/currencies/%FF%FE%FD",
			"/v1/accounts?code=%FF",
			"/v1/transactions?external_id=%C3",
			"/v1/ledgers?metadata[k]=%FF",
			"/v1/accounts?code=%00",
			"/v1/accounts?currency=US%00",
			"/v1/transactions?external_id=a%00b",
			"/v1/ledgers?metadata[k]=%00",
			"/v1/ledgers?metadata[%00]=v",
		} {
			r := c.do(http.MethodGet, target, nil, "")
			if r.status >= 500 {
				t.Errorf("GET %s = %d %s, want a 4xx", target, r.status, r.raw)
			}
		}
	})
}
