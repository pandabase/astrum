package tests

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pandabase/astrum/internal/modules/ledger"
)

func (e *env) category(t *testing.T, name string, side ledger.Side) ledger.Category {
	t.Helper()
	c, err := e.m.CreateCategory(context.Background(), ledger.CreateCategoryInput{
		LedgerID: e.ledger.ID, Currency: "USD", NormalSide: side, Name: name,
	})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func (e *env) addAccount(t *testing.T, c ledger.Category, accounts ...ledger.Account) ledger.Category {
	t.Helper()
	for _, a := range accounts {
		var err error
		if c, err = e.m.AddCategoryAccount(context.Background(), c.ID, a.ID); err != nil {
			t.Fatal(err)
		}
	}
	return c
}

func (e *env) nest(t *testing.T, parent, child ledger.Category) ledger.Category {
	t.Helper()
	c, err := e.m.NestCategory(context.Background(), parent.ID, child.ID)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestCategoryRollUp(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	cash := e.account(t, "USD", ledger.Debit, unrestricted)
	acme := e.account(t, "USD", ledger.Credit, unrestricted)
	globex := e.account(t, "USD", ledger.Credit, unrestricted)
	fees := e.account(t, "USD", ledger.Credit, unrestricted)

	e.post(t, dated("sale-1", acme.ID, cash.ID, 100, 2, ""))
	e.post(t, dated("sale-2", globex.ID, cash.ID, 50, 3, ""))
	e.post(t, dated("fee", fees.ID, cash.ID, 7, 3, ""))
	e.post(t, pending(dated("payout", cash.ID, acme.ID, 30, 4, "")))
	if _, err := e.m.CreateHold(ctx, holdInput("reserve", globex.ID, 10, time.Hour)); err != nil {
		t.Fatal(err)
	}

	vendors := e.addAccount(t, e.category(t, "Vendor payables", ledger.Credit), acme, globex)
	wantBalance(t, "vendors posted", vendors.Balances.Posted, 0, 150, 150)
	wantBalance(t, "vendors pending", vendors.Balances.Pending, 30, 150, 120)
	wantBalance(t, "vendors available", vendors.Balances.Available, 40, 150, 110)

	t.Run("nested and shared accounts count once", func(t *testing.T) {
		liabilities := e.addAccount(t, e.category(t, "Liabilities", ledger.Credit), fees, acme)
		liabilities = e.nest(t, liabilities, vendors)
		wantBalance(t, "liabilities posted", liabilities.Balances.Posted, 0, 157, 157)
	})

	t.Run("mixed normal sides net out", func(t *testing.T) {
		all := e.addAccount(t, e.category(t, "Everything", ledger.Debit), cash, acme, globex, fees)
		wantBalance(t, "posted", all.Balances.Posted, 157, 157, 0)

		wantBalance(t, "pending", all.Balances.Pending, 187, 187, 0)

		wantBalance(t, "available", all.Balances.Available, 197, 187, 10)
	})

	t.Run("effective window", func(t *testing.T) {
		got, err := e.m.Category(ctx, vendors.ID, ledger.EffectiveRange{Until: at(day(3))})
		if err != nil {
			t.Fatal(err)
		}
		wantBalance(t, "vendors by day 2", got.Balances.Posted, 0, 100, 100)
	})

	t.Run("membership changes are idempotent", func(t *testing.T) {
		again := e.addAccount(t, vendors, acme)
		if again.Balances != vendors.Balances {
			t.Fatalf("re-adding acme changed balances: %+v", again.Balances)
		}
		removed, err := e.m.RemoveCategoryAccount(ctx, vendors.ID, globex.ID)
		if err != nil {
			t.Fatal(err)
		}
		wantBalance(t, "without globex", removed.Balances.Posted, 0, 100, 100)
		if _, err := e.m.RemoveCategoryAccount(ctx, vendors.ID, globex.ID); err != nil {
			t.Fatalf("second removal: %v", err)
		}
	})
	e.verify(t)
}

func TestCategoryGraphRules(t *testing.T) {
	e := setup(t)
	ctx := context.Background()

	t.Run("cycles", func(t *testing.T) {
		a, b, c := e.category(t, "a", ledger.Debit), e.category(t, "b", ledger.Debit), e.category(t, "c", ledger.Debit)
		e.nest(t, a, b)
		e.nest(t, b, c)
		_, err := e.m.NestCategory(ctx, c.ID, a.ID)
		wantErr(t, err, ledger.ErrCategoryCycle)
		_, err = e.m.NestCategory(ctx, a.ID, a.ID)
		wantErr(t, err, ledger.ErrCategoryCycle)
		if _, err := e.m.NestCategory(ctx, a.ID, c.ID); err != nil {
			t.Fatalf("a diamond is not a cycle: %v", err)
		}
	})

	t.Run("depth", func(t *testing.T) {
		chain := make([]ledger.Category, 8)
		for i := range chain {
			chain[i] = e.category(t, fmt.Sprint("level ", i), ledger.Debit)
		}
		for i := 1; i < 7; i++ {
			e.nest(t, chain[i-1], chain[i])
		}
		_, err := e.m.NestCategory(ctx, chain[6].ID, chain[7].ID)
		wantErr(t, err, ledger.ErrCategoryDepth)
		_, err = e.m.NestCategory(ctx, chain[7].ID, chain[0].ID)
		wantErr(t, err, ledger.ErrCategoryDepth)
	})

	t.Run("members share ledger and currency", func(t *testing.T) {
		usd := e.category(t, "usd", ledger.Debit)
		_, err := e.m.AddCategoryAccount(ctx, usd.ID, e.account(t, "EUR", ledger.Debit).ID)
		wantErr(t, err, ledger.ErrCategoryMismatch)
		other, err := e.m.CreateLedger(ctx, ledger.CreateLedgerInput{Name: "other"})
		if err != nil {
			t.Fatal(err)
		}
		_, err = e.m.AddCategoryAccount(ctx, usd.ID, e.accountIn(t, other.ID).ID)
		wantErr(t, err, ledger.ErrCategoryMismatch)
		eur, err := e.m.CreateCategory(ctx, ledger.CreateCategoryInput{LedgerID: e.ledger.ID, Currency: "EUR", NormalSide: ledger.Debit, Name: "eur"})
		if err != nil {
			t.Fatal(err)
		}
		_, err = e.m.NestCategory(ctx, usd.ID, eur.ID)
		wantErr(t, err, ledger.ErrCategoryMismatch)
		_, err = e.m.AddCategoryAccount(ctx, usd.ID, uuid.New())
		wantErr(t, err, ledger.ErrNotFound)
	})

	t.Run("create validation", func(t *testing.T) {
		for _, tt := range []struct {
			in   ledger.CreateCategoryInput
			want error
		}{
			{ledger.CreateCategoryInput{LedgerID: e.ledger.ID, Currency: "USD", NormalSide: ledger.Debit}, ledger.ErrInvalid},
			{ledger.CreateCategoryInput{LedgerID: e.ledger.ID, Currency: "USD", NormalSide: "up", Name: "x"}, ledger.ErrInvalid},
			{ledger.CreateCategoryInput{LedgerID: uuid.New(), Currency: "USD", NormalSide: ledger.Debit, Name: "x"}, ledger.ErrUnknownLedger},
			{ledger.CreateCategoryInput{LedgerID: e.ledger.ID, Currency: "ZZZ", NormalSide: ledger.Debit, Name: "x"}, ledger.ErrUnknownCurrency},
		} {
			_, err := e.m.CreateCategory(ctx, tt.in)
			wantErr(t, err, tt.want)
		}
	})
}

func TestCategoryQueries(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	a := e.account(t, "USD", ledger.Debit)
	b := e.account(t, "USD", ledger.Debit)
	root := e.addAccount(t, e.category(t, "root", ledger.Debit), a)
	child := e.addAccount(t, e.category(t, "child", ledger.Debit), b)
	e.nest(t, root, child)
	updated, err := e.m.UpdateCategory(ctx, child.ID, ledger.UpdateInput{Metadata: json.RawMessage(`{"team":"ops"}`)})
	if err != nil || updated.Version != 1 {
		t.Fatalf("update = %+v, %v", updated, err)
	}

	accounts, err := e.m.ListAccounts(ctx, ledger.ListAccountsInput{CategoryID: root.ID, Limit: 10})
	if err != nil || len(accounts) != 2 {
		t.Fatalf("accounts in root tree = %d, %v", len(accounts), err)
	}
	for name, tt := range map[string]struct {
		in   ledger.ListCategoriesInput
		want uuid.UUID
	}{
		"children":          {ledger.ListCategoriesInput{ParentID: root.ID}, child.ID},
		"by account":        {ledger.ListCategoriesInput{AccountID: a.ID}, root.ID},
		"by metadata":       {ledger.ListCategoriesInput{Metadata: map[string]string{"team": "ops"}}, child.ID},
		"by ledger, newest": {ledger.ListCategoriesInput{LedgerID: e.ledger.ID}, child.ID},
	} {
		t.Run(name, func(t *testing.T) {
			tt.in.Limit = 10
			got, err := e.m.ListCategories(ctx, tt.in)
			if err != nil || len(got) == 0 || got[0].ID != tt.want {
				t.Fatalf("categories = %+v, %v", got, err)
			}
		})
	}

	if err := e.m.DeleteCategory(ctx, child.ID); err != nil {
		t.Fatal(err)
	}
	_, err = e.m.Category(ctx, child.ID, ledger.EffectiveRange{})
	wantErr(t, err, ledger.ErrNotFound)
	if accounts, _ := e.m.ListAccounts(ctx, ledger.ListAccountsInput{CategoryID: root.ID, Limit: 10}); len(accounts) != 1 {
		t.Fatalf("root after deleting child has %d accounts", len(accounts))
	}
	if acc := e.get(t, b.ID); acc.ID != b.ID {
		t.Fatal("deleting a category touched its account")
	}
	err = e.m.DeleteCategory(ctx, child.ID)
	wantErr(t, err, ledger.ErrNotFound)
}

func TestConcurrentOppositeNesting(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	for i := range 10 {
		a := e.category(t, fmt.Sprint("a", i), ledger.Debit)
		b := e.category(t, fmt.Sprint("b", i), ledger.Debit)
		var (
			wg         sync.WaitGroup
			errA, errB error
		)
		wg.Go(func() { _, errA = e.m.NestCategory(ctx, a.ID, b.ID) })
		wg.Go(func() { _, errB = e.m.NestCategory(ctx, b.ID, a.ID) })
		wg.Wait()
		if (errA == nil) == (errB == nil) {
			t.Fatalf("round %d: a→b %v, b→a %v; want exactly one", i, errA, errB)
		}
		if lost := errors.Join(errA, errB); !errors.Is(lost, ledger.ErrCategoryCycle) {
			t.Fatalf("loser error = %v, want ErrCategoryCycle", lost)
		}
	}
}

func TestHTTPCategories(t *testing.T) {
	a := newAPI(t)
	equity := a.account("equity", "credit", `,"allow_negative":true`)
	acme := a.account("acme", "credit", `,"allow_negative":true`)
	a.must(http.StatusCreated, http.MethodPost, "/v1/transactions", "sale", transferJSON(acme, equity, "250"))

	create := func(name string) string {
		return requirePrefix(t, a.must(http.StatusCreated, http.MethodPost, "/v1/account_categories", "",
			fmt.Sprintf(`{"ledger_id":%q,"currency":"USD","normal_side":"credit","name":%q}`, a.ledger, name))["id"], "cat")
	}
	vendors, liabilities := create("Vendors"), create("Liabilities")
	got := a.must(http.StatusOK, http.MethodPut, "/v1/account_categories/"+vendors+"/accounts/"+acme, "", "")
	if got["object"] != "account_category" || got["balances"].(map[string]any)["posted"].(map[string]any)["amount"] != "250" {
		t.Fatalf("vendors = %v", got)
	}
	a.must(http.StatusOK, http.MethodPut, "/v1/account_categories/"+liabilities+"/categories/"+vendors, "", "")
	got = a.must(http.StatusOK, http.MethodGet, "/v1/account_categories/"+liabilities, "", "")
	if got["balances"].(map[string]any)["posted"].(map[string]any)["amount"] != "250" {
		t.Fatalf("liabilities = %v", got)
	}
	if n := len(a.list("/v1/accounts?category_id=" + liabilities)); n != 1 {
		t.Fatalf("accounts under liabilities = %d", n)
	}
	if n := len(a.list("/v1/account_categories?parent_id=" + liabilities)); n != 1 {
		t.Fatalf("children = %d", n)
	}

	tests := []struct {
		name, method, path, body string
		status                   int
		code                     string
	}{
		{"cycle", http.MethodPut, "/v1/account_categories/" + vendors + "/categories/" + liabilities, "", 422, "category_cycle"},
		{"account id kind", http.MethodPut, "/v1/account_categories/" + vendors + "/accounts/" + vendors, "", 400, "invalid_request"},
		{"missing account", http.MethodPut, "/v1/account_categories/" + vendors + "/accounts/acct_01h455vb4pex5vsknk084sn02q", "", 404, "not_found"},
		{"name required", http.MethodPatch, "/v1/account_categories/" + vendors, `{"name":""}`, 422, "validation_error"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := a.do(tt.method, tt.path, "", tt.body)
			if resp.status != tt.status || resp.body["code"] != tt.code {
				t.Fatalf("%s %s = %d %v, want %d %s", tt.method, tt.path, resp.status, resp.body, tt.status, tt.code)
			}
		})
	}

	a.must(http.StatusOK, http.MethodDelete, "/v1/account_categories/"+liabilities+"/categories/"+vendors, "", "")
	gone := a.must(http.StatusOK, http.MethodDelete, "/v1/account_categories/"+vendors, "", "")
	if gone["deleted"] != true || gone["id"] != vendors {
		t.Fatalf("delete = %v", gone)
	}
	a.must(http.StatusNotFound, http.MethodGet, "/v1/account_categories/"+vendors, "", "")
}
