package tests

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/pandabase/astrum/internal/modules/ledger"
	"github.com/pandabase/astrum/internal/money"
)

func feFilters(n int) map[string]string {
	m := make(map[string]string, n)
	for i := range n {
		m[fmt.Sprint("k", i)] = "v"
	}
	return m
}

func TestPaginationEdgeLedgers(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	var ledgers []ledger.Ledger
	for i := range 3 {
		l, err := e.m.CreateLedger(ctx, ledger.CreateLedgerInput{Name: fmt.Sprint("pl-", i)})
		if err != nil {
			t.Fatal(err)
		}
		ledgers = append(ledgers, l)
	}
	all := "pl-2,pl-1,pl-0,test"

	join := func(ls []ledger.Ledger) string {
		out := make([]string, len(ls))
		for i, l := range ls {
			out[i] = l.Name
		}
		return strings.Join(out, ",")
	}
	tests := []struct {
		name string
		in   ledger.ListLedgersInput
		want string
		err  error
	}{
		{"limit zero", ledger.ListLedgersInput{}, "", ledger.ErrInvalid},
		{"negative limit", ledger.ListLedgersInput{Limit: -1}, "", ledger.ErrInvalid},
		{"limit over max", ledger.ListLedgersInput{Limit: 1001}, "", ledger.ErrInvalid},
		{"twenty one filters", ledger.ListLedgersInput{Metadata: feFilters(21), Limit: 10}, "", ledger.ErrInvalid},
		{"empty filter key", ledger.ListLedgersInput{Metadata: map[string]string{"": "v"}, Limit: 10}, "", ledger.ErrInvalid},
		{"NUL filter value", ledger.ListLedgersInput{Metadata: map[string]string{"k": "\x00"}, Limit: 10}, "", ledger.ErrInvalid},
		{"twenty filters", ledger.ListLedgersInput{Metadata: feFilters(20), Limit: 10}, "", nil},
		{"limit at max", ledger.ListLedgersInput{Limit: 1000}, all, nil},
		{"limit one", ledger.ListLedgersInput{Limit: 1}, "pl-2", nil},
		{"exact page", ledger.ListLedgersInput{Limit: 4}, all, nil},
		{"after exact page", ledger.ListLedgersInput{Before: e.ledger.ID, Limit: 4}, "", nil},
		{"cursor", ledger.ListLedgersInput{Before: ledgers[2].ID, Limit: 2}, "pl-1,pl-0", nil},
		{"max cursor", ledger.ListLedgersInput{Before: uuid.Max, Limit: 10}, all, nil},
		{"cursor below everything", ledger.ListLedgersInput{Before: uuid.MustParse("00000000-0000-7000-8000-000000000001"), Limit: 10}, "", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := e.m.ListLedgers(ctx, tt.in)
			if !errors.Is(err, tt.err) {
				t.Fatalf("ListLedgers() error = %v, want %v", err, tt.err)
			}
			if err == nil && join(got) != tt.want {
				t.Fatalf("ledgers = %s, want %s", join(got), tt.want)
			}
		})
	}
}

func TestPaginationEdgeAccounts(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	other, err := e.m.CreateLedger(ctx, ledger.CreateLedgerInput{Name: "other"})
	if err != nil {
		t.Fatal(err)
	}
	mk := func(code string, currency money.Currency, ledgerID uuid.UUID) ledger.Account {
		t.Helper()
		in := feAccountInput(e, code)
		in.Currency, in.LedgerID = currency, ledgerID
		acc, err := e.m.CreateAccount(ctx, in)
		if err != nil {
			t.Fatal(err)
		}
		return acc
	}
	a := mk("pa-a", "USD", e.ledger.ID)
	b := mk("pa-b", "EUR", e.ledger.ID)
	c := mk("pa-c", "USD", e.ledger.ID)
	mk("pa-d", "USD", other.ID)
	if _, err := e.m.FreezeAccount(ctx, b.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.CloseAccount(ctx, c.ID); err != nil {
		t.Fatal(err)
	}
	cat := e.addAccount(t, e.category(t, "pa", ledger.Debit), a)

	codes := func(accs []ledger.Account) string {
		out := make([]string, len(accs))
		for i, acc := range accs {
			out[i] = acc.Code
		}
		return strings.Join(out, ",")
	}
	mine := ledger.ListAccountsInput{LedgerID: e.ledger.ID}
	with := func(in ledger.ListAccountsInput, mutate func(*ledger.ListAccountsInput)) ledger.ListAccountsInput {
		mutate(&in)
		return in
	}
	tests := []struct {
		name string
		in   ledger.ListAccountsInput
		want string
		err  error
	}{
		{"limit zero", ledger.ListAccountsInput{}, "", ledger.ErrInvalid},
		{"negative limit", ledger.ListAccountsInput{Limit: -1}, "", ledger.ErrInvalid},
		{"limit over max", ledger.ListAccountsInput{Limit: 1001}, "", ledger.ErrInvalid},
		{"unknown status", ledger.ListAccountsInput{Status: "deleted", Limit: 10}, "", ledger.ErrInvalid},
		{"bad currency", ledger.ListAccountsInput{Currency: "us", Limit: 10}, "", ledger.ErrInvalid},
		{"twenty one filters", ledger.ListAccountsInput{Metadata: feFilters(21), Limit: 10}, "", ledger.ErrInvalid},
		{"exact page", with(mine, func(in *ledger.ListAccountsInput) { in.Code = ""; in.Limit = 4 }), "pa-c,pa-b,pa-a," + e.open.Code, nil},
		{"after exact page", with(mine, func(in *ledger.ListAccountsInput) { in.Before = e.open.ID; in.Limit = 4 }), "", nil},
		{"limit one", with(mine, func(in *ledger.ListAccountsInput) { in.Limit = 1 }), "pa-c", nil},
		{"cursor", with(mine, func(in *ledger.ListAccountsInput) { in.Before = c.ID; in.Limit = 1 }), "pa-b", nil},
		{"frozen", with(mine, func(in *ledger.ListAccountsInput) { in.Status = ledger.AccountFrozen; in.Limit = 10 }), "pa-b", nil},
		{"closed", with(mine, func(in *ledger.ListAccountsInput) { in.Status = ledger.AccountClosed; in.Limit = 10 }), "pa-c", nil},
		{"eur", with(mine, func(in *ledger.ListAccountsInput) { in.Currency = "EUR"; in.Limit = 10 }), "pa-b", nil},
		{"unused currency", with(mine, func(in *ledger.ListAccountsInput) { in.Currency = "JPY"; in.Limit = 10 }), "", nil},
		{"other ledger", ledger.ListAccountsInput{LedgerID: other.ID, Limit: 10}, "pa-d", nil},
		{"unknown ledger", ledger.ListAccountsInput{LedgerID: uuid.New(), Limit: 10}, "", nil},
		{"code across ledgers", ledger.ListAccountsInput{Code: "pa-d", Limit: 10}, "pa-d", nil},
		{"code is exact", ledger.ListAccountsInput{Code: "pa-", Limit: 10}, "", nil},
		{"category", ledger.ListAccountsInput{CategoryID: cat.ID, Limit: 10}, "pa-a", nil},
		{"unknown category", ledger.ListAccountsInput{CategoryID: uuid.New(), Limit: 10}, "", nil},
		{"open only", with(mine, func(in *ledger.ListAccountsInput) { in.Status = ledger.AccountOpen; in.Limit = 10 }), "pa-a," + e.open.Code, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := e.m.ListAccounts(ctx, tt.in)
			if !errors.Is(err, tt.err) {
				t.Fatalf("ListAccounts() error = %v, want %v", err, tt.err)
			}
			if err == nil && codes(got) != tt.want {
				t.Fatalf("accounts = %s, want %s", codes(got), tt.want)
			}
		})
	}
}
