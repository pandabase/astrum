package tests

import (
	"context"
	"encoding/json/jsontext"
	"fmt"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pandabase/astrum/internal/kernel/db"
	"github.com/pandabase/astrum/internal/modules/ledger"
)

var day = func(d int) time.Time { return time.Date(2026, 1, d, 0, 0, 0, 0, time.UTC) }

func dated(key string, from, to uuid.UUID, amount int64, d int, metadata string) ledger.PostInput {
	in := transfer(key, from, to, amount)
	in.EffectiveAt = new(day(d).Add(12 * time.Hour))
	if metadata != "" {
		in.Metadata = jsontext.Value(metadata)
	}
	return in
}

func TestListEntries(t *testing.T) {
	t.Parallel()
	e := setup(t)
	ctx := context.Background()
	a := e.account(t, "USD", ledger.Credit, unrestricted)
	b := e.account(t, "USD", ledger.Debit, unrestricted)
	c := e.account(t, "USD", ledger.Debit, unrestricted)

	first := e.post(t, dated("t1", a.ID, b.ID, 100, 1, `{"order":"1"}`))
	e.post(t, dated("t2", a.ID, c.ID, 40, 2, `{"order":"2"}`))
	e.post(t, dated("t3", b.ID, c.ID, 10, 3, `{"order":"1","kind":"fee"}`))
	pendingTxn := e.post(t, pending(dated("t4", a.ID, b.ID, 7, 4, "")))
	archived, err := e.m.ArchiveTransaction(ctx, e.post(t, pending(dated("t5", a.ID, c.ID, 3, 5, ""))).ID)
	if err != nil {
		t.Fatal(err)
	}
	other, err := e.m.CreateLedger(ctx, ledger.CreateLedgerInput{Name: "other"})
	if err != nil {
		t.Fatal(err)
	}
	e.post(t, transfer("elsewhere", e.accountIn(t, other.ID).ID, e.accountIn(t, other.ID).ID, 1))

	list := func(in ledger.ListEntriesInput) []ledger.Entry {
		t.Helper()
		if in.Limit == 0 {
			in.Limit = 100
		}
		entries, err := e.m.ListEntries(ctx, in)
		if err != nil {
			t.Fatal(err)
		}
		return entries
	}
	amounts := func(entries []ledger.Entry) string {
		var s []string
		for _, en := range entries {
			s = append(s, fmt.Sprintf("%s%s", en.Side[:1], en.Amount))
		}
		return fmt.Sprint(s)
	}

	tests := []struct {
		name string
		in   ledger.ListEntriesInput
		want string
	}{
		{"account in journal order", ledger.ListEntriesInput{AccountID: b.ID}, "[d100 c10]"},
		{"one transaction", ledger.ListEntriesInput{TransactionID: first.ID}, "[d100 c100]"},
		{"credits only", ledger.ListEntriesInput{LedgerID: e.ledger.ID, AccountID: a.ID, Side: ledger.Credit}, "[c100 c40]"},
		{"metadata", ledger.ListEntriesInput{AccountID: c.ID, Metadata: map[string]string{"order": "1"}}, "[d10]"},
		{"two metadata keys", ledger.ListEntriesInput{Metadata: map[string]string{"order": "1", "kind": "fee"}}, "[d10 c10]"},
		{"effective window", ledger.ListEntriesInput{AccountID: c.ID, Effective: ledger.EffectiveRange{From: new(day(2)), Until: new(day(3))}}, "[d40]"},
		{"pending", ledger.ListEntriesInput{Status: ledger.TransactionPending, AccountID: b.ID}, "[d7]"},
		{"archived", ledger.ListEntriesInput{Status: ledger.TransactionArchived, TransactionID: archived.ID}, "[d3 c3]"},
		{"other ledger", ledger.ListEntriesInput{LedgerID: other.ID}, "[d1 c1]"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := amounts(list(tt.in)); got != tt.want {
				t.Fatalf("entries = %s, want %s", got, tt.want)
			}
		})
	}

	t.Run("balance after on the normal side", func(t *testing.T) {
		entries := list(ledger.ListEntriesInput{AccountID: a.ID})
		if len(entries) != 2 || *entries[0].BalanceAfter != amt(100) || *entries[1].BalanceAfter != amt(140) {
			t.Fatalf("a entries = %+v", entries)
		}
		if p := list(ledger.ListEntriesInput{Status: ledger.TransactionPending, TransactionID: pendingTxn.ID}); p[0].BalanceAfter != nil {
			t.Fatalf("pending entry has a balance after: %+v", p[0])
		}
	})

	t.Run("pages by sequence", func(t *testing.T) {
		page1 := list(ledger.ListEntriesInput{LedgerID: e.ledger.ID, Limit: 4})
		page2 := list(ledger.ListEntriesInput{LedgerID: e.ledger.ID, After: page1[3].Sequence, Limit: 4})
		if len(page1) != 4 || len(page2) != 2 || page2[0].Sequence <= page1[3].Sequence {
			t.Fatalf("pages = %d then %d", len(page1), len(page2))
		}
	})

	t.Run("validation", func(t *testing.T) {
		for _, in := range []ledger.ListEntriesInput{
			{Status: "done", Limit: 10},
			{Side: "up", Limit: 10},
			{Effective: ledger.EffectiveRange{From: new(day(3)), Until: new(day(3))}, Limit: 10},
			{Metadata: map[string]string{"": "x"}, Limit: 10},
		} {
			_, err := e.m.ListEntries(ctx, in)
			wantErr(t, err, ledger.ErrInvalid)
		}
	})
}

func TestHistoricalBalances(t *testing.T) {
	t.Parallel()
	e := setup(t)
	ctx := context.Background()
	a := e.account(t, "USD", ledger.Credit, unrestricted)
	b := e.account(t, "USD", ledger.Debit, unrestricted)

	e.post(t, dated("jan1", a.ID, b.ID, 100, 1, ""))
	e.post(t, dated("jan3", b.ID, a.ID, 30, 3, ""))

	e.post(t, dated("backdated", a.ID, b.ID, 50, 2, ""))
	e.post(t, pending(dated("pending", b.ID, a.ID, 20, 4, "")))

	balances := func(r ledger.EffectiveRange) ledger.Balances {
		t.Helper()
		got, err := e.m.Balances(ctx, b.ID, r)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	tests := []struct {
		name                 string
		r                    ledger.EffectiveRange
		posted, pending, out int64
	}{
		{"end of day 1", ledger.EffectiveRange{Until: new(day(2))}, 100, 100, 100},
		{"end of day 2 includes the backdated entry", ledger.EffectiveRange{Until: new(day(3))}, 150, 150, 150},
		{"day 3 alone", ledger.EffectiveRange{From: new(day(3)), Until: new(day(4))}, -30, -30, -30},
		{"everything", ledger.EffectiveRange{}, 120, 100, 100},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := balances(tt.r)
			if got.Posted.Amount != amt(tt.posted) || got.Pending.Amount != amt(tt.pending) || got.Available.Amount != amt(tt.out) {
				t.Fatalf("posted %s pending %s available %s, want %d %d %d",
					got.Posted.Amount, got.Pending.Amount, got.Available.Amount, tt.posted, tt.pending, tt.out)
			}
		})
	}
	now := e.get(t, b.ID)
	if all := balances(ledger.EffectiveRange{}); all.Posted != now.Posted || all.Pending != now.Pending {
		t.Fatalf("unbounded history %+v differs from the account %+v", all, now)
	}
	_, err := e.m.Balances(ctx, uuid.New(), ledger.EffectiveRange{})
	wantErr(t, err, ledger.ErrNotFound)
}

func TestStatements(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	a := e.account(t, "USD", ledger.Credit, unrestricted)
	b := e.account(t, "USD", ledger.Debit, unrestricted)
	e.post(t, dated("before", a.ID, b.ID, 1_000, 1, ""))
	e.post(t, dated("in-1", a.ID, b.ID, 100, 5, ""))
	e.post(t, dated("in-2", b.ID, a.ID, 40, 6, ""))
	e.post(t, dated("after", a.ID, b.ID, 7, 20, ""))

	e.settle(t)
	st, err := e.m.CreateStatement(ctx, ledger.CreateStatementInput{
		AccountID: b.ID, Description: "January", From: day(2), Until: day(10),
	})
	if err != nil {
		t.Fatal(err)
	}
	wantBalance(t, "starting", st.Starting, 1_000, 0, 1_000)
	wantBalance(t, "ending", st.Ending, 1_100, 40, 1_060)
	if st.EntryCount != 2 || st.LedgerID != e.ledger.ID || st.Currency != "USD" {
		t.Fatalf("statement = %+v", st)
	}

	e.post(t, dated("late", a.ID, b.ID, 5, 7, ""))
	again, err := e.m.Statement(ctx, st.ID)
	if err != nil || again.Ending != st.Ending || again.EntryCount != 2 {
		t.Fatalf("statement after a backdated post = %+v, %v", again, err)
	}
	entries, err := e.m.ListEntries(ctx, ledger.ListEntriesInput{StatementID: st.ID, Limit: 100})
	if err != nil || len(entries) != 2 {
		t.Fatalf("statement entries = %+v, %v", entries, err)
	}
	e.settle(t)
	fresh, err := e.m.CreateStatement(ctx, ledger.CreateStatementInput{AccountID: b.ID, From: day(2), Until: day(10)})
	if err != nil || fresh.EntryCount != 3 || fresh.Ending.Amount != amt(1_065) {
		t.Fatalf("new statement = %+v, %v", fresh, err)
	}

	list, err := e.m.ListStatements(ctx, ledger.ListStatementsInput{AccountID: b.ID, Limit: 10})
	if err != nil || len(list) != 2 || list[0].ID != fresh.ID {
		t.Fatalf("statements = %+v, %v", list, err)
	}

	t.Run("validation", func(t *testing.T) {
		for _, in := range []ledger.CreateStatementInput{
			{AccountID: b.ID, From: day(3), Until: day(3)},
			{AccountID: b.ID, Until: day(3)},
			{From: day(1), Until: day(3)},
		} {
			_, err := e.m.CreateStatement(ctx, in)
			wantErr(t, err, ledger.ErrInvalid)
		}
		_, err := e.m.CreateStatement(ctx, ledger.CreateStatementInput{AccountID: uuid.New(), From: day(1), Until: day(3)})
		wantErr(t, err, ledger.ErrNotFound)
	})

	t.Run("statements are immutable", func(t *testing.T) {
		err := pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `UPDATE ledger_statements SET entry_count = 0 WHERE id = $1`, st.ID)
			return err
		})
		if db.Code(err) != "23001" {
			t.Fatalf("error = %v, want restrict_violation", err)
		}
	})
	e.verify(t)
}

func TestStatementIgnoresInFlight(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	a := e.account(t, "USD", ledger.Credit, unrestricted)
	b := e.account(t, "USD", ledger.Debit, unrestricted)
	e.post(t, dated("seen", a.ID, b.ID, 100, 5, ""))

	slow, err := e.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer slow.Rollback(ctx)
	if _, err := slow.Exec(ctx, `
		WITH t AS (`+e.insertPosted(`'slow'`)+` RETURNING id)
		INSERT INTO ledger_postings (transaction_id, account_id, currency, side, amount, balance_after)
		SELECT t.id, v.account, 'USD', v.side, 9, 0
		FROM t, (VALUES ($1::uuid, 'debit'), ($2::uuid, 'credit')) AS v(account, side)`, b.ID, a.ID); err != nil {
		t.Fatal(err)
	}

	st, err := e.m.CreateStatement(ctx, ledger.CreateStatementInput{AccountID: b.ID, From: day(1), Until: day(28).AddDate(1, 0, 0)})
	if err != nil {
		t.Fatal(err)
	}
	if err := slow.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	entries, err := e.m.ListEntries(ctx, ledger.ListEntriesInput{StatementID: st.ID, Limit: 100})
	if err != nil || len(entries) != int(st.EntryCount) || st.EntryCount != 1 {
		t.Fatalf("statement counted %d entries, lists %d (%v)", st.EntryCount, len(entries), err)
	}
}

func TestMetadataFilters(t *testing.T) {
	t.Parallel()
	e := setup(t)
	ctx := context.Background()
	a := e.account(t, "USD", ledger.Debit, unrestricted, func(in *ledger.CreateAccountInput) {
		in.Metadata = jsontext.Value(`{"vendor":"acme","tier":"gold"}`)
	})
	b := e.account(t, "USD", ledger.Debit, unrestricted, func(in *ledger.CreateAccountInput) {
		in.Metadata = jsontext.Value(`{"vendor":"globex"}`)
	})
	e.post(t, dated("x", a.ID, b.ID, 1, 1, `{"invoice":"9","channel":"web"}`))
	e.post(t, dated("y", a.ID, b.ID, 1, 1, `{"invoice":"10"}`))

	accounts, err := e.m.ListAccounts(ctx, ledger.ListAccountsInput{Metadata: map[string]string{"vendor": "acme"}, Limit: 10})
	if err != nil || len(accounts) != 1 || accounts[0].ID != a.ID {
		t.Fatalf("accounts = %+v, %v", accounts, err)
	}
	txns, err := e.m.ListTransactions(ctx, ledger.ListTransactionsInput{Metadata: map[string]string{"invoice": "9", "channel": "web"}, Limit: 10})
	if err != nil || len(txns) != 1 || txns[0].IdempotencyKey != "x" {
		t.Fatalf("transactions = %+v, %v", txns, err)
	}
	none, err := e.m.ListTransactions(ctx, ledger.ListTransactionsInput{Metadata: map[string]string{"invoice": "9", "channel": "app"}, Limit: 10})
	if err != nil || len(none) != 0 {
		t.Fatalf("mismatched filter = %+v, %v", none, err)
	}
	ledgers, err := e.m.ListLedgers(ctx, ledger.ListLedgersInput{Metadata: map[string]string{"missing": "x"}, Limit: 10})
	if err != nil || len(ledgers) != 0 {
		t.Fatalf("ledgers = %+v, %v", ledgers, err)
	}
}

func TestHTTPHistory(t *testing.T) {
	t.Parallel()
	a := newAPI(t)
	equity := a.account("equity", "credit", `,"allow_negative":true`)
	cash := a.account("cash", "debit", "")
	for i, d := range []int{1, 2, 3} {
		body := fmt.Sprintf(`{"effective_at":%q,"metadata":{"day":"%d"},`, day(d).Format(time.RFC3339), d) + transferJSON(equity, cash, "100")[1:]
		a.must(http.StatusCreated, http.MethodPost, "/v1/transactions", fmt.Sprint("k", i), body)
	}

	entries := a.list("/v1/entries?limit=2&account_id=" + cash)
	if len(entries) != 3 || entries[0]["object"] != "entry" || entries[2]["balance_after"] != "300" {
		t.Fatalf("entries = %v", entries)
	}
	if got := a.list("/v1/entries?account_id=" + cash + "&metadata[day]=2"); len(got) != 1 {
		t.Fatalf("metadata filtered entries = %v", got)
	}
	if got := a.list("/v1/transactions?metadata[day]=3"); len(got) != 1 {
		t.Fatalf("metadata filtered transactions = %v", got)
	}

	q := url.Values{"effective_at_upper_bound": {day(3).Format(time.RFC3339)}}
	b := a.must(http.StatusOK, http.MethodGet, "/v1/accounts/"+cash+"/balances?"+q.Encode(), "", "")
	if b["object"] != "balances" || b["posted"].(map[string]any)["amount"] != "200" {
		t.Fatalf("balances = %v", b)
	}

	a.e.settle(t)
	st := a.must(http.StatusCreated, http.MethodPost, "/v1/statements", "", fmt.Sprintf(
		`{"account_id":%q,"effective_at_lower_bound":%q,"effective_at_upper_bound":%q}`,
		cash, day(2).Format(time.RFC3339), day(4).Format(time.RFC3339)))
	id := requirePrefix(t, st["id"], "stmt")
	if st["entry_count"] != float64(2) || st["starting_balance"].(map[string]any)["amount"] != "100" ||
		st["ending_balance"].(map[string]any)["amount"] != "300" {
		t.Fatalf("statement = %v", st)
	}
	a.must(http.StatusOK, http.MethodGet, "/v1/statements/"+id, "", "")
	if got := a.list("/v1/entries?statement_id=" + id); len(got) != 2 {
		t.Fatalf("statement entries = %v", got)
	}
	if got := a.list("/v1/statements?account_id=" + cash); len(got) != 1 {
		t.Fatalf("statements = %v", got)
	}

	tests := []struct {
		name, method, path, body string
		status                   int
		code                     string
	}{
		{"bad time", http.MethodGet, "/v1/entries?effective_at_lower_bound=yesterday", "", 400, "invalid_request"},
		{"repeated metadata key", http.MethodGet, "/v1/entries?metadata[day]=1&metadata[day]=2", "", 400, "invalid_request"},
		{"bad metadata syntax", http.MethodGet, "/v1/transactions?metadata[day=1", "", 400, "invalid_request"},
		{"wrong statement id kind", http.MethodGet, "/v1/entries?statement_id=" + cash, "", 400, "invalid_request"},
		{"empty period", http.MethodPost, "/v1/statements", fmt.Sprintf(
			`{"account_id":%q,"effective_at_lower_bound":%q,"effective_at_upper_bound":%q}`,
			cash, day(2).Format(time.RFC3339), day(2).Format(time.RFC3339)), 422, "validation_error"},
		{"bad side", http.MethodGet, "/v1/entries?side=up", "", 422, "validation_error"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := a.do(tt.method, tt.path, "", tt.body)
			if resp.status != tt.status || resp.body["code"] != tt.code {
				t.Fatalf("%s %s = %d %v, want %d %s", tt.method, tt.path, resp.status, resp.body, tt.status, tt.code)
			}
		})
	}
}
