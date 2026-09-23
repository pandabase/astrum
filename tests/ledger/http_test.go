package tests

import (
	"encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pandabase/astrum/internal/kernel/httpx"
	"github.com/pandabase/astrum/internal/kernel/testdb"
	"github.com/pandabase/astrum/internal/kernel/typeid"
)

type api struct {
	t      *testing.T
	e      *env
	srv    *httptest.Server
	ledger string
}

func newAPI(t *testing.T) *api {
	t.Helper()
	e := setup(t)
	mux := http.NewServeMux()
	e.m.Routes(mux)
	srv := httptest.NewServer(httpx.Logging(testdb.Logger(), mux))
	t.Cleanup(srv.Close)
	a := &api{t: t, e: e, srv: srv}
	a.ledger = a.must(http.StatusCreated, http.MethodPost, "/v1/ledgers", "", `{"name":"test"}`)["id"].(string)
	return a
}

type response struct {
	status int
	header http.Header
	body   map[string]any
}

func (a *api) do(method, path, key, body string) response {
	a.t.Helper()
	req, err := http.NewRequest(method, a.srv.URL+path, strings.NewReader(body))
	if err != nil {
		a.t.Fatal(err)
	}
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	resp, err := a.srv.Client().Do(req)
	if err != nil {
		a.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		a.t.Fatal(err)
	}
	out := response{status: resp.StatusCode, header: resp.Header}
	if err := json.Unmarshal(raw, &out.body); err != nil {
		a.t.Fatalf("%s %s: decode %q: %v", method, path, raw, err)
	}
	return out
}

func (a *api) must(want int, method, path, key, body string) map[string]any {
	a.t.Helper()
	resp := a.do(method, path, key, body)
	if resp.status != want {
		a.t.Fatalf("%s %s = %d %v, want %d", method, path, resp.status, resp.body, want)
	}
	return resp.body
}

func (a *api) account(code, side string, extra string) string {
	a.t.Helper()
	out := a.must(http.StatusCreated, http.MethodPost, "/v1/accounts", "",
		fmt.Sprintf(`{"ledger_id":%q,"code":%q,"currency":"USD","normal_side":%q%s}`, a.ledger, code, side, extra))
	return out["id"].(string)
}

func (a *api) inLedger(body string) string {
	return fmt.Sprintf(`{"ledger_id":%q,`, a.ledger) + strings.TrimPrefix(body, "{")
}

func (a *api) list(path string) []map[string]any {
	a.t.Helper()
	var all []map[string]any
	sep := "?"
	if strings.Contains(path, "?") {
		sep = "&"
	}
	next := path
	for {
		page := a.must(http.StatusOK, http.MethodGet, next, "", "")
		if page["object"] != "list" {
			a.t.Fatalf("GET %s object = %v", next, page["object"])
		}
		for _, item := range page["data"].([]any) {
			all = append(all, item.(map[string]any))
		}
		if page["has_more"] != true {
			if page["next_cursor"] != nil {
				a.t.Fatalf("last page has next_cursor %v", page["next_cursor"])
			}
			return all
		}
		next = path + sep + "cursor=" + page["next_cursor"].(string)
	}
}

func transferJSON(from, to, amount string) string {
	return fmt.Sprintf(`{"entries":[
		{"account_id":%q,"side":"debit","amount":%q},
		{"account_id":%q,"side":"credit","amount":%q}]}`, to, amount, from, amount)
}

func balanceAmount(acc map[string]any, kind string) any {
	return acc["balances"].(map[string]any)[kind].(map[string]any)["amount"]
}

func requirePrefix(t *testing.T, v any, prefix string) string {
	t.Helper()
	s, ok := v.(string)
	if !ok || !strings.HasPrefix(s, prefix+"_") {
		t.Fatalf("id = %v, want a %s_ id", v, prefix)
	}
	return s
}

func TestHTTPEndToEnd(t *testing.T) {
	a := newAPI(t)
	equity := a.account("equity", "credit", `,"allow_negative":true`)
	cash := requirePrefix(t, a.account("cash", "debit", ""), "acct")
	merchant := a.account("merchant", "debit", "")

	fund := a.must(http.StatusCreated, http.MethodPost, "/v1/transactions", "fund", transferJSON(equity, cash, "9007199254740993"))
	requirePrefix(t, fund["id"], "txn")
	if fund["object"] != "transaction" || fund["idempotency_key"] != "fund" {
		t.Fatalf("transaction = %v", fund)
	}
	again := a.must(http.StatusCreated, http.MethodPost, "/v1/transactions", "fund", transferJSON(equity, cash, "9007199254740993"))
	if again["id"] != fund["id"] {
		t.Fatalf("replay id = %v, want %v", again["id"], fund["id"])
	}
	acc := a.must(http.StatusOK, http.MethodGet, "/v1/accounts/"+cash, "", "")
	if acc["object"] != "account" || acc["status"] != "open" ||
		balanceAmount(acc, "posted") != "9007199254740993" || balanceAmount(acc, "available") != "9007199254740993" {
		t.Fatalf("account = %v", acc)
	}

	hold := a.must(http.StatusCreated, http.MethodPost, "/v1/holds", "auth", fmt.Sprintf(
		`{"account_id":%q,"amount":"500","expires_at":%q}`, cash, time.Now().Add(time.Hour).Format(time.RFC3339Nano)))
	holdID := requirePrefix(t, hold["id"], "hold")
	a.must(http.StatusOK, http.MethodGet, "/v1/holds/"+holdID, "", "")
	captured := a.must(http.StatusOK, http.MethodPost, "/v1/holds/"+holdID+"/capture", "cap", fmt.Sprintf(
		`{"destination_account_id":%q,"amount":"300"}`, merchant))
	if captured["status"] != "captured" || captured["captured_amount"] != "300" {
		t.Fatalf("capture = %v", captured)
	}
	requirePrefix(t, captured["capture_transaction_id"], "txn")

	batchBody := fmt.Sprintf(`{"transactions":[%s,%s]}`, transferJSON(cash, merchant, "1"), transferJSON(cash, merchant, "2"))
	batch := a.must(http.StatusCreated, http.MethodPost, "/v1/transactions/batch", "batch", batchBody)
	results := batch["results"].([]any)
	if batch["object"] != "batch" || batch["atomic"] != true || len(results) != 2 {
		t.Fatalf("batch = %v", batch)
	}
	first := results[0].(map[string]any)["transaction"].(map[string]any)
	if first["idempotency_key"] != "batch/0" {
		t.Fatalf("batch entry key = %v, want batch/0", first["idempotency_key"])
	}
	replayed := a.must(http.StatusCreated, http.MethodPost, "/v1/transactions/batch", "batch", batchBody)
	if replayed["results"].([]any)[0].(map[string]any)["transaction"].(map[string]any)["id"] != first["id"] {
		t.Fatal("batch retry posted new transactions instead of replaying")
	}

	txnID := first["id"].(string)
	a.must(http.StatusOK, http.MethodGet, "/v1/transactions/"+txnID, "", "")
	rev := a.must(http.StatusCreated, http.MethodPost, "/v1/transactions/"+txnID+"/reverse", "rev", "")
	if rev["reverses_id"] != txnID {
		t.Fatalf("reverse = %v", rev)
	}

	st := a.must(http.StatusCreated, http.MethodPost, "/v1/scheduled_transactions", "sched", fmt.Sprintf(
		`{"execute_at":%q,"description":"rent","entries":[
			{"account_id":%q,"side":"debit","amount":"5"},
			{"account_id":%q,"side":"credit","amount":"5"}]}`,
		time.Now().Add(time.Hour).Format(time.RFC3339), merchant, cash))
	stID := requirePrefix(t, st["id"], "sched")
	if st["object"] != "scheduled_transaction" || st["description"] != "rent" || len(st["entries"].([]any)) != 2 {
		t.Fatalf("schedule = %v", st)
	}
	a.must(http.StatusOK, http.MethodGet, "/v1/scheduled_transactions/"+stID, "", "")
	if c := a.must(http.StatusOK, http.MethodPost, "/v1/scheduled_transactions/"+stID+"/cancel", "", ""); c["status"] != "canceled" {
		t.Fatalf("cancel = %v", c)
	}

	t.Run("account entries page oldest first", func(t *testing.T) {
		page := a.must(http.StatusOK, http.MethodGet, "/v1/accounts/"+merchant+"/entries?limit=3", "", "")
		if len(page["data"].([]any)) != 3 || page["has_more"] != true {
			t.Fatalf("first page = %v", page)
		}
		entries := a.list("/v1/accounts/" + merchant + "/entries?limit=3")
		var balances []string
		for _, e := range entries {
			balances = append(balances, e["balance_after"].(string))
		}
		if got := strings.Join(balances, ","); got != "300,301,303,302" {
			t.Fatalf("balances after = %s, want 300,301,303,302", got)
		}
	})

	t.Run("accounts page newest first", func(t *testing.T) {
		accounts := a.list("/v1/accounts?limit=1")
		if len(accounts) != 4 || accounts[0]["id"] != merchant {
			t.Fatalf("accounts = %d, first %v; want 4 starting with %s", len(accounts), accounts[0]["id"], merchant)
		}
		if frozen := a.list("/v1/accounts?status=frozen"); len(frozen) != 0 {
			t.Fatalf("frozen accounts = %d", len(frozen))
		}
		if usd := a.list("/v1/accounts?currency=USD&limit=2"); len(usd) != 4 {
			t.Fatalf("USD accounts = %d", len(usd))
		}
	})

	t.Run("transactions filter by account", func(t *testing.T) {
		txns := a.list("/v1/transactions?limit=2&account_id=" + merchant)
		if len(txns) != 4 || txns[0]["id"] != rev["id"] {
			t.Fatalf("merchant transactions = %d, first %v; want 4 starting with the reversal", len(txns), txns[0]["id"])
		}
		if all := a.list("/v1/transactions"); len(all) != 5 {
			t.Fatalf("all transactions = %d, want 5", len(all))
		}
	})

	if v := a.must(http.StatusOK, http.MethodGet, "/v1/integrity", "", ""); v["ok"] != true || v["object"] != "integrity_report" {
		t.Fatalf("integrity = %v", v)
	}
}

func TestHTTPErrors(t *testing.T) {
	a := newAPI(t)
	x := a.account("x", "debit", "")
	y := a.account("y", "debit", "")
	open := a.account("open", "debit", `,"allow_negative":true`)
	a.must(http.StatusCreated, http.MethodPost, "/v1/transactions", "used", transferJSON(open, y, "1"))

	tests := []struct {
		name, method, path, key, body string
		status                        int
		code                          string
	}{
		{"malformed json", http.MethodPost, "/v1/accounts", "", `{`, 400, "invalid_request"},
		{"trailing garbage", http.MethodPost, "/v1/accounts", "", a.inLedger(`{"code":"z","currency":"USD","normal_side":"debit"}`) + "}", 400, "invalid_request"},
		{"key in body", http.MethodPost, "/v1/transactions", "k", `{"idempotency_key":"k","entries":[]}`, 400, "invalid_request"},
		{"numeric amount", http.MethodPost, "/v1/transactions", "k", strings.ReplaceAll(transferJSON(x, y, "1"), `"1"`, `1`), 400, "invalid_request"},
		{"raw uuid in body", http.MethodPost, "/v1/transactions", "k", transferJSON(uuid.NewString(), y, "1"), 400, "invalid_request"},
		{"missing idempotency key", http.MethodPost, "/v1/transactions", "", transferJSON(x, y, "1"), 400, "idempotency_key_required"},
		{"invalid account", http.MethodPost, "/v1/accounts", "", a.inLedger(`{"code":"z","currency":"usd","normal_side":"debit"}`), 422, "validation_error"},
		{"account terms conflict", http.MethodPost, "/v1/accounts", "", a.inLedger(`{"code":"x","currency":"USD","normal_side":"credit"}`), 409, "account_exists"},
		{"insufficient funds", http.MethodPost, "/v1/transactions", "f", transferJSON(x, y, "1"), 422, "insufficient_funds"},
		{"unbalanced", http.MethodPost, "/v1/transactions", "u", strings.Replace(transferJSON(open, y, "2"), `"2"`, `"1"`, 1), 422, "unbalanced_transaction"},
		{"key reused with different body", http.MethodPost, "/v1/transactions", "used", transferJSON(open, y, "2"), 422, "idempotency_key_reused"},
		{"malformed id", http.MethodGet, "/v1/accounts/nope", "", "", 400, "invalid_request"},
		{"raw uuid id", http.MethodGet, "/v1/accounts/" + uuid.NewString(), "", "", 400, "invalid_request"},
		{"wrong kind of id", http.MethodGet, "/v1/accounts/" + typeid.Encode("txn", uuid.New()), "", "", 400, "invalid_request"},
		{"unknown account", http.MethodGet, "/v1/accounts/" + typeid.Encode("acct", uuid.New()), "", "", 404, "not_found"},
		{"unknown hold", http.MethodPost, "/v1/holds/" + typeid.Encode("hold", uuid.New()) + "/void", "", "", 404, "not_found"},
		{"limit not a number", http.MethodGet, "/v1/accounts?limit=abc", "", "", 400, "invalid_request"},
		{"limit too big", http.MethodGet, "/v1/accounts/" + x + "/entries?limit=101", "", "", 400, "invalid_request"},
		{"bad cursor", http.MethodGet, "/v1/transactions?cursor=!!!", "", "", 400, "invalid_request"},
		{"bad status filter", http.MethodGet, "/v1/accounts?status=deleted", "", "", 422, "validation_error"},
		{"bad account filter", http.MethodGet, "/v1/transactions?account_id=" + typeid.Encode("hold", uuid.New()), "", "", 400, "invalid_request"},
		{"atomic batch failure", http.MethodPost, "/v1/transactions/batch", "ab", fmt.Sprintf(`{"transactions":[%s]}`, transferJSON(x, y, "5")), 422, "insufficient_funds"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := a.do(tt.method, tt.path, tt.key, tt.body)
			if resp.status != tt.status {
				t.Fatalf("status = %d %v, want %d", resp.status, resp.body, tt.status)
			}
			if _, isBatch := resp.body["results"]; isBatch {
				return
			}
			if resp.body["code"] != tt.code || resp.body["status"] != float64(tt.status) || resp.body["request_id"] == "" {
				t.Fatalf("problem = %v, want code %s", resp.body, tt.code)
			}
			if ct := resp.header.Get("Content-Type"); ct != "application/problem+json" {
				t.Fatalf("Content-Type = %q", ct)
			}
		})
	}

	t.Run("internal details never leak", func(t *testing.T) {
		resp := a.do(http.MethodPost, "/v1/transactions", "f", transferJSON(x, y, "1"))
		if d, _ := resp.body["detail"].(string); strings.Contains(d, "SQLSTATE") {
			t.Fatalf("detail leaks database error: %s", d)
		}
	})

	t.Run("partial batch is multi-status", func(t *testing.T) {
		body := fmt.Sprintf(`{"atomic":false,"transactions":[%s,%s]}`, transferJSON(x, y, "5"), transferJSON(open, x, "1"))
		resp := a.do(http.MethodPost, "/v1/transactions/batch", "pb", body)
		if resp.status != http.StatusMultiStatus {
			t.Fatalf("status = %d %v, want 207", resp.status, resp.body)
		}
		results := resp.body["results"].([]any)
		failed := results[0].(map[string]any)
		if failed["transaction"] != nil || failed["error"].(map[string]any)["code"] != "insufficient_funds" {
			t.Fatalf("failed result = %v", failed)
		}
		if ok := results[1].(map[string]any); ok["error"] != nil || ok["transaction"] == nil {
			t.Fatalf("ok result = %v", ok)
		}
	})
}

func TestHTTPAccountStatus(t *testing.T) {
	a := newAPI(t)
	equity := a.account("equity", "credit", `,"allow_negative":true`)
	cash := a.account("cash", "debit", "")
	a.must(http.StatusCreated, http.MethodPost, "/v1/transactions", "fund", transferJSON(equity, cash, "10"))

	if acc := a.must(http.StatusOK, http.MethodPost, "/v1/accounts/"+cash+"/freeze", "", ""); acc["status"] != "frozen" || acc["status_changed_at"] == nil {
		t.Fatalf("freeze = %v", acc)
	}
	if body := a.must(http.StatusUnprocessableEntity, http.MethodPost, "/v1/transactions", "blocked", transferJSON(equity, cash, "1")); body["code"] != "account_not_open" {
		t.Fatalf("post to frozen = %v", body)
	}
	if body := a.must(http.StatusConflict, http.MethodPost, "/v1/accounts/"+cash+"/close", "", ""); body["code"] != "account_not_empty" {
		t.Fatalf("close funded = %v", body)
	}
	if acc := a.must(http.StatusOK, http.MethodPost, "/v1/accounts/"+cash+"/unfreeze", "", ""); acc["status"] != "open" {
		t.Fatalf("unfreeze = %v", acc)
	}
	a.must(http.StatusCreated, http.MethodPost, "/v1/transactions", "empty", transferJSON(cash, equity, "10"))
	if acc := a.must(http.StatusOK, http.MethodPost, "/v1/accounts/"+cash+"/close", "", ""); acc["status"] != "closed" {
		t.Fatalf("close = %v", acc)
	}
	a.must(http.StatusUnprocessableEntity, http.MethodPost, "/v1/accounts/"+cash+"/unfreeze", "", "")
	if closed := a.list("/v1/accounts?status=closed"); len(closed) != 1 || closed[0]["id"] != cash {
		t.Fatalf("closed accounts = %v", closed)
	}
	a.must(http.StatusNotFound, http.MethodPost, "/v1/accounts/"+typeid.Encode("acct", uuid.New())+"/freeze", "", "")
}

func TestHTTPListHoldsAndSchedules(t *testing.T) {
	a := newAPI(t)
	equity := a.account("equity", "credit", `,"allow_negative":true`)
	cash := a.account("cash", "debit", "")
	a.must(http.StatusCreated, http.MethodPost, "/v1/transactions", "fund", transferJSON(equity, cash, "1000"))
	expires := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	for i := range 3 {
		a.must(http.StatusCreated, http.MethodPost, "/v1/holds", fmt.Sprint("hold-", i),
			fmt.Sprintf(`{"account_id":%q,"amount":"10","expires_at":%q}`, cash, expires))
	}

	holds := a.list("/v1/holds?limit=2&status=pending&account_id=" + cash)
	if len(holds) != 3 || holds[0]["object"] != "hold" || holds[0]["id"].(string) <= holds[2]["id"].(string) {
		t.Fatalf("holds = %v", holds)
	}
	if n := len(a.list("/v1/holds?status=captured")); n != 0 {
		t.Fatalf("captured holds = %d", n)
	}
	a.must(http.StatusUnprocessableEntity, http.MethodGet, "/v1/holds?status=held", "", "")
	a.must(http.StatusBadRequest, http.MethodGet, "/v1/holds?account_id=ldg_01h455vb4pex5vsknk084sn02q", "", "")

	executeAt := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	body := strings.TrimSuffix(transferJSON(cash, equity, "5"), "}") + fmt.Sprintf(`,"execute_at":%q}`, executeAt)
	a.must(http.StatusCreated, http.MethodPost, "/v1/scheduled_transactions", "sched-1", body)
	schedules := a.list("/v1/scheduled_transactions?status=scheduled")
	if len(schedules) != 1 || schedules[0]["object"] != "scheduled_transaction" {
		t.Fatalf("schedules = %v", schedules)
	}
	a.must(http.StatusUnprocessableEntity, http.MethodGet, "/v1/scheduled_transactions?status=pending", "", "")
}
