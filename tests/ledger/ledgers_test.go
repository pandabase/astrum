package tests

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pandabase/astrum/internal/kernel/db"
	"github.com/pandabase/astrum/internal/modules/ledger"
)

func str(s string) *string { return &s }

func TestLedgers(t *testing.T) {
	e := setup(t)
	ctx := context.Background()

	l, err := e.m.CreateLedger(ctx, ledger.CreateLedgerInput{
		Name:        "Payments",
		Description: "card acquiring",
		Metadata:    json.RawMessage(`{"region":"eu","limits":{"daily":"100","monthly":"1000"}}`),
	})
	if err != nil || l.Name != "Payments" || l.Version != 0 {
		t.Fatalf("CreateLedger = %+v, %v", l, err)
	}
	if got, err := e.m.Ledger(ctx, l.ID); err != nil || got.Description != "card acquiring" {
		t.Fatalf("Ledger = %+v, %v", got, err)
	}

	t.Run("update merges metadata", func(t *testing.T) {
		got, err := e.m.UpdateLedger(ctx, l.ID, ledger.UpdateInput{
			Name:     str("Payments EU"),
			Metadata: json.RawMessage(`{"region":null,"limits":{"daily":"200"},"tier":"gold"}`),
		})
		if err != nil || got.Name != "Payments EU" || got.Description != "card acquiring" || got.Version != 1 {
			t.Fatalf("UpdateLedger = %+v, %v", got, err)
		}
		want := `{"limits":{"daily":"200","monthly":"1000"},"tier":"gold"}`
		if !jsonSame(t, got.Metadata, want) {
			t.Fatalf("metadata = %s, want %s", got.Metadata, want)
		}
	})

	t.Run("unchanged update keeps the version", func(t *testing.T) {
		got, err := e.m.UpdateLedger(ctx, l.ID, ledger.UpdateInput{Name: str("Payments EU"), Metadata: json.RawMessage(`{"tier":"gold"}`)})
		if err != nil || got.Version != 1 {
			t.Fatalf("UpdateLedger = %+v, %v; want version 1", got, err)
		}
	})

	t.Run("validation", func(t *testing.T) {
		_, err := e.m.CreateLedger(ctx, ledger.CreateLedgerInput{Name: " "})
		wantErr(t, err, ledger.ErrInvalid)
		_, err = e.m.CreateLedger(ctx, ledger.CreateLedgerInput{Name: "x", Metadata: json.RawMessage(`"no"`)})
		wantErr(t, err, ledger.ErrInvalid)
		_, err = e.m.UpdateLedger(ctx, l.ID, ledger.UpdateInput{Name: str("")})
		wantErr(t, err, ledger.ErrInvalid)
		_, err = e.m.UpdateLedger(ctx, uuid.New(), ledger.UpdateInput{Name: str("x")})
		wantErr(t, err, ledger.ErrNotFound)
		_, err = e.m.Ledger(ctx, uuid.New())
		wantErr(t, err, ledger.ErrNotFound)
	})

	t.Run("list pages newest first", func(t *testing.T) {
		for i := range 3 {
			if _, err := e.m.CreateLedger(ctx, ledger.CreateLedgerInput{Name: fmt.Sprint("extra ", i)}); err != nil {
				t.Fatal(err)
			}
		}
		var names []string
		var before uuid.UUID
		for {
			page, err := e.m.ListLedgers(ctx, ledger.ListLedgersInput{Before: before, Limit: 2})
			if err != nil {
				t.Fatal(err)
			}
			for _, l := range page {
				names = append(names, l.Name)
			}
			if len(page) < 2 {
				break
			}
			before = page[len(page)-1].ID
		}
		if got := strings.Join(names, ","); got != "extra 2,extra 1,extra 0,Payments EU,test" {
			t.Fatalf("ledgers = %s", got)
		}
	})
}

func TestAccountsBelongToOneLedger(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	other, err := e.m.CreateLedger(ctx, ledger.CreateLedgerInput{Name: "other"})
	if err != nil {
		t.Fatal(err)
	}
	create := func(ledgerID uuid.UUID, code string) ledger.Account {
		t.Helper()
		acc, err := e.m.CreateAccount(ctx, ledger.CreateAccountInput{
			LedgerID: ledgerID, Code: code, Name: "Cash " + code, Currency: "USD", NormalSide: ledger.Debit, AllowNegative: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		return acc
	}
	cash := create(e.ledger.ID, "cash")
	otherCash := create(other.ID, "cash")
	if cash.ID == otherCash.ID || otherCash.LedgerID != other.ID {
		t.Fatalf("same code in two ledgers = %s and %s", cash.ID, otherCash.ID)
	}
	if again := create(e.ledger.ID, "cash"); again.ID != cash.ID {
		t.Fatalf("replay created %s, want %s", again.ID, cash.ID)
	}

	t.Run("unknown ledger", func(t *testing.T) {
		_, err := e.m.CreateAccount(ctx, ledger.CreateAccountInput{LedgerID: uuid.New(), Code: "x", Currency: "USD", NormalSide: ledger.Debit})
		wantErr(t, err, ledger.ErrUnknownLedger)
	})

	t.Run("money cannot cross ledgers", func(t *testing.T) {
		_, err := e.m.Post(ctx, transfer("cross", cash.ID, otherCash.ID, 10))
		wantErr(t, err, ledger.ErrCrossLedger)

		funded := e.funded(t, 100)
		hold, err := e.m.CreateHold(ctx, holdInput("h", funded.ID, 50, time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		_, err = e.m.CaptureHold(ctx, hold.ID, ledger.CaptureInput{IdempotencyKey: "c", Destination: otherCash.ID, Amount: amt(50)})
		wantErr(t, err, ledger.ErrCrossLedger)
		if h, _ := e.m.Hold(ctx, hold.ID); h.Status != ledger.HoldPending {
			t.Fatalf("hold = %s after rejected capture", h.Status)
		}
	})

	t.Run("filters", func(t *testing.T) {
		e.post(t, transfer("in-other", otherCash.ID, create(other.ID, "bank").ID, 5))

		accounts, err := e.m.ListAccounts(ctx, ledger.ListAccountsInput{LedgerID: other.ID, Limit: 10})
		if err != nil || len(accounts) != 2 {
			t.Fatalf("accounts in other = %d, %v", len(accounts), err)
		}
		byCode, err := e.m.ListAccounts(ctx, ledger.ListAccountsInput{Code: "cash", Limit: 10})
		if err != nil || len(byCode) != 2 {
			t.Fatalf("accounts coded cash = %d, %v", len(byCode), err)
		}
		exact, err := e.m.ListAccounts(ctx, ledger.ListAccountsInput{LedgerID: other.ID, Code: "cash", Limit: 10})
		if err != nil || len(exact) != 1 || exact[0].ID != otherCash.ID {
			t.Fatalf("other/cash = %+v, %v", exact, err)
		}
		txns, err := e.m.ListTransactions(ctx, ledger.ListTransactionsInput{LedgerID: other.ID, Limit: 10})
		if err != nil || len(txns) != 1 || txns[0].IdempotencyKey != "in-other" {
			t.Fatalf("transactions in other = %+v, %v", txns, err)
		}
	})

	t.Run("update descriptive fields", func(t *testing.T) {
		before := e.get(t, cash.ID)
		acc, err := e.m.UpdateAccount(ctx, cash.ID, ledger.UpdateInput{
			Description: str("till"),
			Metadata:    json.RawMessage(`{"branch":"soho"}`),
		})
		if err != nil || acc.Name != "Cash cash" || acc.Description != "till" || acc.Version != before.Version+1 {
			t.Fatalf("UpdateAccount = %+v, %v", acc, err)
		}
		if !jsonSame(t, acc.Metadata, `{"branch":"soho"}`) || acc.Posted.Amount != before.Posted.Amount {
			t.Fatalf("after update = %+v", acc)
		}
		_, err = e.m.UpdateAccount(ctx, cash.ID, ledger.UpdateInput{Metadata: json.RawMessage(`[]`)})
		wantErr(t, err, ledger.ErrInvalid)
		_, err = e.m.UpdateAccount(ctx, uuid.New(), ledger.UpdateInput{Name: str("x")})
		wantErr(t, err, ledger.ErrNotFound)
	})

	t.Run("schema enforces ledgers", func(t *testing.T) {
		tests := []struct {
			name       string
			wantCode   string
			constraint string
			sql        string
		}{
			{"move account between ledgers", "23001", "",
				`UPDATE ledger_accounts SET ledger_id = '` + other.ID.String() + `', version = version + 1 WHERE id = '` + cash.ID.String() + `'`},
			{"delete ledger", "23001", "", `DELETE FROM ledger_ledgers WHERE id = '` + other.ID.String() + `'`},
			{"cross-ledger postings", "23514", "ledger_postings_same_ledger", `
				WITH t AS (` + e.insertPosted(`'raw'`) + ` RETURNING id)
				INSERT INTO ledger_postings (transaction_id, account_id, currency, side, amount, balance_after)
				SELECT t.id, a.id, 'USD', a.side, 1, 0
				FROM t, (VALUES ('` + cash.ID.String() + `'::uuid, 'debit'), ('` + otherCash.ID.String() + `'::uuid, 'credit')) AS a(id, side)`},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				err := pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) error {
					_, err := tx.Exec(ctx, tt.sql)
					return err
				})
				if got := db.Code(err); got != tt.wantCode || db.Constraint(err) != tt.constraint {
					t.Fatalf("error = %v (code %q), want %s %s", err, got, tt.wantCode, tt.constraint)
				}
			})
		}
	})
	e.verify(t)
}

func TestHTTPLedgers(t *testing.T) {
	a := newAPI(t)

	l := a.must(http.StatusCreated, http.MethodPost, "/v1/ledgers", "", `{"name":"Wallets","metadata":{"env":"eu"}}`)
	id := requirePrefix(t, l["id"], "ldg")
	if l["object"] != "ledger" || l["name"] != "Wallets" {
		t.Fatalf("ledger = %v", l)
	}
	patched := a.must(http.StatusOK, http.MethodPatch, "/v1/ledgers/"+id, "", `{"description":"user wallets","metadata":{"env":null,"team":"core"}}`)
	if patched["description"] != "user wallets" || fmt.Sprint(patched["metadata"]) != "map[team:core]" || patched["version"] != float64(1) {
		t.Fatalf("patched = %v", patched)
	}
	if got := a.must(http.StatusOK, http.MethodGet, "/v1/ledgers/"+id, "", ""); got["description"] != "user wallets" {
		t.Fatalf("get = %v", got)
	}
	if n := len(a.list("/v1/ledgers?limit=1")); n != 3 {
		t.Fatalf("listed %d ledgers, want the setup, API and Wallets ledgers", n)
	}

	acct := a.must(http.StatusCreated, http.MethodPost, "/v1/accounts", "",
		fmt.Sprintf(`{"ledger_id":%q,"code":"cash","name":"Cash","currency":"USD","normal_side":"debit","allow_negative":true}`, id))
	acctID := requirePrefix(t, acct["id"], "acct")
	if acct["ledger_id"] != id || acct["name"] != "Cash" || fmt.Sprint(acct["metadata"]) != "map[]" {
		t.Fatalf("account = %v", acct)
	}
	updated := a.must(http.StatusOK, http.MethodPatch, "/v1/accounts/"+acctID, "", `{"name":"Main cash","metadata":{"gl":"1000"}}`)
	if updated["name"] != "Main cash" || fmt.Sprint(updated["metadata"]) != "map[gl:1000]" {
		t.Fatalf("updated = %v", updated)
	}
	mine := a.account("cash", "debit", `,"allow_negative":true`)

	found := a.list("/v1/accounts?ledger_id=" + id + "&code=cash")
	if len(found) != 1 || found[0]["id"] != acctID {
		t.Fatalf("lookup by ledger and code = %v", found)
	}
	if n := len(a.list("/v1/accounts?code=cash")); n != 2 {
		t.Fatalf("accounts coded cash = %d, want 2", n)
	}

	tests := []struct {
		name, method, path, body string
		status                   int
		code                     string
	}{
		{"ledger name required", http.MethodPost, "/v1/ledgers", `{"name":""}`, 422, "validation_error"},
		{"ledger id prefix", http.MethodGet, "/v1/ledgers/" + acctID, "", 400, "invalid_request"},
		{"missing ledger", http.MethodGet, "/v1/ledgers/ldg_01h455vb4pex5vsknk084sn02q", "", 404, "not_found"},
		{"unknown ledger", http.MethodPost, "/v1/accounts",
			`{"ledger_id":"ldg_01h455vb4pex5vsknk084sn02q","code":"x","currency":"USD","normal_side":"debit"}`, 422, "unknown_ledger"},
		{"no ledger", http.MethodPost, "/v1/accounts", `{"code":"x","currency":"USD","normal_side":"debit"}`, 422, "validation_error"},
		{"bad ledger filter", http.MethodGet, "/v1/accounts?ledger_id=acct_01h455vb4pex5vsknk084sn02q", "", 400, "invalid_request"},
		{"bad metadata patch", http.MethodPatch, "/v1/accounts/" + acctID, `{"metadata":"x"}`, 422, "validation_error"},
		{"cross ledger", http.MethodPost, "/v1/transactions", transferJSON(acctID, mine, "5"), 422, "cross_ledger_transaction"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := a.do(tt.method, tt.path, "key-"+tt.name, tt.body)
			if resp.status != tt.status || resp.body["code"] != tt.code {
				t.Fatalf("%s %s = %d %v, want %d %s", tt.method, tt.path, resp.status, resp.body, tt.status, tt.code)
			}
		})
	}
}

func jsonSame(t *testing.T, got json.RawMessage, want string) bool {
	t.Helper()
	var g, w any
	if err := json.Unmarshal(got, &g); err != nil {
		t.Fatalf("decode %s: %v", got, err)
	}
	if err := json.Unmarshal([]byte(want), &w); err != nil {
		t.Fatal(err)
	}
	return fmt.Sprint(g) == fmt.Sprint(w)
}
