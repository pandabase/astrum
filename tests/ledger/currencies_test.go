package tests

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/pandabase/astrum/internal/kernel/db"
	"github.com/pandabase/astrum/internal/modules/ledger"
	"github.com/pandabase/astrum/internal/money"
)

func TestCurrencies(t *testing.T) {
	t.Parallel()
	e := setup(t)
	ctx := context.Background()

	t.Run("iso currencies are seeded", func(t *testing.T) {
		for code, exponent := range map[money.Currency]int{"USD": 2, "JPY": 0, "BHD": 3, "CLF": 4} {
			c, err := e.m.Currency(ctx, code)
			if err != nil || c.Exponent != exponent {
				t.Fatalf("Currency(%s) = %+v, %v; want exponent %d", code, c, err, exponent)
			}
		}
	})

	t.Run("custom currency", func(t *testing.T) {
		eth, err := e.m.CreateCurrency(ctx, ledger.CreateCurrencyInput{Code: "ETH", Exponent: 18})
		if err != nil || eth.Code != "ETH" || eth.Exponent != 18 {
			t.Fatalf("CreateCurrency = %+v, %v", eth, err)
		}
		again, err := e.m.CreateCurrency(ctx, ledger.CreateCurrencyInput{Code: "ETH", Exponent: 18})
		if err != nil || !again.CreatedAt.Equal(eth.CreatedAt) {
			t.Fatalf("replay = %+v, %v", again, err)
		}
		_, err = e.m.CreateCurrency(ctx, ledger.CreateCurrencyInput{Code: "ETH", Exponent: 8})
		wantErr(t, err, ledger.ErrCurrencyExists)
		_, err = e.m.CreateCurrency(ctx, ledger.CreateCurrencyInput{Code: "USD", Exponent: 3})
		wantErr(t, err, ledger.ErrCurrencyExists)
	})

	t.Run("validation", func(t *testing.T) {
		for _, in := range []ledger.CreateCurrencyInput{
			{Code: "eth", Exponent: 18},
			{Code: "E", Exponent: 0},
			{Code: "ABCDEFGHIJKLMNOPQ", Exponent: 0},
			{Code: "PTS", Exponent: -1},
			{Code: "PTS", Exponent: 31},
		} {
			_, err := e.m.CreateCurrency(ctx, in)
			wantErr(t, err, ledger.ErrInvalid)
		}
		_, err := e.m.Currency(ctx, "NOPE")
		wantErr(t, err, ledger.ErrNotFound)
	})

	t.Run("list pages in code order", func(t *testing.T) {
		var all []ledger.Currency
		var after money.Currency
		for {
			page, err := e.m.ListCurrencies(ctx, after, 50)
			if err != nil {
				t.Fatal(err)
			}
			all = append(all, page...)
			if len(page) < 50 {
				break
			}
			after = page[len(page)-1].Code
		}
		if len(all) < 160 {
			t.Fatalf("listed %d currencies, want the ISO set", len(all))
		}
		for i := 1; i < len(all); i++ {
			if all[i-1].Code >= all[i].Code {
				t.Fatalf("out of order: %s before %s", all[i-1].Code, all[i].Code)
			}
		}
	})

	t.Run("accounts need a registered currency", func(t *testing.T) {
		_, err := e.m.CreateAccount(ctx, ledger.CreateAccountInput{LedgerID: e.ledger.ID, Code: "zzz", Currency: "ZZZ", NormalSide: ledger.Debit})
		wantErr(t, err, ledger.ErrUnknownCurrency)

		jpy := e.account(t, "JPY", ledger.Debit)
		if jpy.CurrencyExponent != 0 {
			t.Fatalf("JPY account exponent = %d", jpy.CurrencyExponent)
		}
		eth := e.account(t, "ETH", ledger.Debit)
		if got := e.get(t, eth.ID); got.CurrencyExponent != 18 {
			t.Fatalf("ETH account exponent = %d", got.CurrencyExponent)
		}
	})

	t.Run("currencies are immutable in the schema", func(t *testing.T) {
		for _, sql := range []string{
			`UPDATE ledger_currencies SET exponent = 0 WHERE code = 'ETH'`,
			`DELETE FROM ledger_currencies WHERE code = 'ETH'`,
			`TRUNCATE ledger_currencies CASCADE`,
		} {
			err := pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) error {
				_, err := tx.Exec(ctx, sql)
				return err
			})
			if db.Code(err) != "23001" {
				t.Fatalf("%s: error = %v, want restrict_violation", sql, err)
			}
		}
	})
}

func TestWideAmounts(t *testing.T) {
	t.Parallel()
	e := setup(t)
	ctx := context.Background()
	if _, err := e.m.CreateCurrency(ctx, ledger.CreateCurrencyInput{Code: "ETH", Exponent: 18}); err != nil {
		t.Fatal(err)
	}
	m := money.MustParseAmount
	treasury := e.account(t, "ETH", ledger.Credit, unrestricted)
	wallet := e.account(t, "ETH", ledger.Debit)
	merchant := e.account(t, "ETH", ledger.Debit)

	fifty := m("50000000000000000000000000000")
	e.post(t, transferAmount("mint", treasury.ID, wallet.ID, fifty))

	hold, err := e.m.CreateHold(ctx, ledger.CreateHoldInput{
		IdempotencyKey: "auth",
		AccountID:      wallet.ID,
		Amount:         m("20000000000000000000000000000"),
		ExpiresAt:      time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	acc := e.get(t, wallet.ID)
	if acc.Available.Amount != m("30000000000000000000000000000") {
		t.Fatalf("available = %s", acc.Available.Amount)
	}
	_, err = e.m.Post(ctx, transferAmount("too-much", wallet.ID, merchant.ID, m("30000000000000000000000000001")))
	wantErr(t, err, ledger.ErrInsufficientFunds)

	if _, err := e.m.CaptureHold(ctx, hold.ID, ledger.CaptureInput{
		IdempotencyKey: "capture",
		Destination:    merchant.ID,
		Amount:         m("12345678901234567890123456789"),
	}); err != nil {
		t.Fatal(err)
	}
	if got := e.get(t, merchant.ID).Posted.Amount; got != m("12345678901234567890123456789") {
		t.Fatalf("merchant = %s", got)
	}
	if got := e.get(t, wallet.ID); got.Posted.Amount != m("37654321098765432109876543211") || !got.Held.IsZero() {
		t.Fatalf("wallet = balance %s held %s", got.Posted.Amount, got.Held)
	}
	if got := e.get(t, treasury.ID).Posted.Amount; got != fifty {
		t.Fatalf("treasury = %s, want %s on its credit normal side", got, fifty)
	}

	lines, err := e.m.AccountEntries(ctx, wallet.ID, 0, 10)
	if err != nil || len(lines) != 2 || lines[1].BalanceAfter != m("37654321098765432109876543211") {
		t.Fatalf("statement = %+v, %v", lines, err)
	}

	txn, err := e.m.Post(ctx, transferAmount("mint", treasury.ID, wallet.ID, fifty))
	if err != nil || txn.Postings[0].Amount != fifty {
		t.Fatalf("replay = %+v, %v", txn, err)
	}
	e.verify(t)
}

func TestHTTPCurrencies(t *testing.T) {
	t.Parallel()
	a := newAPI(t)

	eth := a.must(http.StatusCreated, http.MethodPost, "/v1/currencies", "", `{"code":"ETH","exponent":18}`)
	if eth["object"] != "currency" || eth["code"] != "ETH" || eth["exponent"] != float64(18) {
		t.Fatalf("currency = %v", eth)
	}
	a.must(http.StatusCreated, http.MethodPost, "/v1/currencies", "", `{"code":"ETH","exponent":18}`)
	if got := a.must(http.StatusOK, http.MethodGet, "/v1/currencies/JPY", "", ""); got["exponent"] != float64(0) {
		t.Fatalf("JPY = %v", got)
	}

	codes := map[string]bool{}
	for _, c := range a.list("/v1/currencies?limit=100") {
		codes[c["code"].(string)] = true
	}
	if !codes["ETH"] || !codes["USD"] || !codes["ZWG"] {
		t.Fatalf("listed %d currencies without ETH, USD or ZWG", len(codes))
	}

	id := a.must(http.StatusCreated, http.MethodPost, "/v1/accounts", "",
		a.inLedger(`{"code":"eth:treasury","currency":"ETH","normal_side":"credit","allow_negative":true}`))["id"].(string)
	wallet := a.must(http.StatusCreated, http.MethodPost, "/v1/accounts", "",
		a.inLedger(`{"code":"eth:wallet","currency":"ETH","normal_side":"debit"}`))
	if wallet["currency_exponent"] != float64(18) {
		t.Fatalf("account = %v", wallet)
	}
	wei := "99999999999999999999999999999999999999"
	a.must(http.StatusCreated, http.MethodPost, "/v1/transactions", "mint", transferJSON(id, wallet["id"].(string), wei))
	acc := a.must(http.StatusOK, http.MethodGet, "/v1/accounts/"+wallet["id"].(string), "", "")
	if balanceAmount(acc, "posted") != wei || balanceAmount(acc, "available") != wei {
		t.Fatalf("balances = %v", acc["balances"])
	}

	tests := []struct {
		name, method, path, body string
		status                   int
		code                     string
	}{
		{"exponent required", http.MethodPost, "/v1/currencies", `{"code":"PTS"}`, http.StatusUnprocessableEntity, "validation_error"},
		{"bad code", http.MethodPost, "/v1/currencies", `{"code":"pts","exponent":0}`, http.StatusUnprocessableEntity, "validation_error"},
		{"exponent changed", http.MethodPost, "/v1/currencies", `{"code":"ETH","exponent":9}`, http.StatusConflict, "currency_exists"},
		{"unknown currency", http.MethodGet, "/v1/currencies/NOPE", "", http.StatusNotFound, "not_found"},
		{"bad cursor", http.MethodGet, "/v1/currencies?cursor=!!!", "", http.StatusBadRequest, "invalid_request"},
		{"unregistered account currency", http.MethodPost, "/v1/accounts",
			a.inLedger(`{"code":"x","currency":"ZZZ","normal_side":"debit"}`), http.StatusUnprocessableEntity, "unknown_currency"},
		{"amount beyond 38 digits", http.MethodPost, "/v1/transactions",
			transferJSON(id, wallet["id"].(string), wei+"9"), http.StatusBadRequest, "invalid_request"},
		{"amount as number", http.MethodPost, "/v1/transactions",
			strings.ReplaceAll(transferJSON(id, wallet["id"].(string), "5"), `"5"`, `5`), http.StatusBadRequest, "invalid_request"},
		{"balance overflow", http.MethodPost, "/v1/transactions",
			transferJSON(id, wallet["id"].(string), "1"), http.StatusUnprocessableEntity, "amount_overflow"},
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
