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
	"github.com/jackc/pgx/v5"
	"github.com/pandabase/astrum/internal/kernel/db"
	"github.com/pandabase/astrum/internal/modules/ledger"
	"github.com/pandabase/astrum/internal/money"
)

func pending(in ledger.PostInput) ledger.PostInput {
	in.Status = ledger.TransactionPending
	return in
}

func wantBalance(t *testing.T, name string, got ledger.Balance, debits, credits, amount int64) {
	t.Helper()
	if got != (ledger.Balance{Debits: amt(debits), Credits: amt(credits), Amount: amt(amount)}) {
		t.Fatalf("%s = %s/%s/%s, want %d/%d/%d", name, got.Debits, got.Credits, got.Amount, debits, credits, amount)
	}
}

func TestPendingLifecycle(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	a := e.funded(t, 1_000)
	b := e.account(t, "USD", ledger.Debit)

	yesterday := time.Now().Add(-24 * time.Hour).Truncate(time.Microsecond)
	create := pending(transfer("invoice-1", a.ID, b.ID, 300))
	create.ExternalID = "inv-1"
	create.EffectiveAt = &yesterday
	create.Metadata = json.RawMessage(`{"order":"1"}`)
	txn := e.post(t, create)
	if txn.Status != ledger.TransactionPending || txn.Version != 1 || txn.PostedAt != nil || !txn.EffectiveAt.Equal(yesterday) {
		t.Fatalf("created = %+v", txn)
	}

	t.Run("pending outflows are reserved, inflows are not available", func(t *testing.T) {
		acc := e.get(t, a.ID)
		wantBalance(t, "a posted", acc.Posted, 1_000, 0, 1_000)
		wantBalance(t, "a pending", acc.Pending, 1_000, 300, 700)
		wantBalance(t, "a available", acc.Available, 1_000, 300, 700)
		acc = e.get(t, b.ID)
		wantBalance(t, "b posted", acc.Posted, 0, 0, 0)
		wantBalance(t, "b pending", acc.Pending, 300, 0, 300)
		wantBalance(t, "b available", acc.Available, 0, 0, 0)

		_, err := e.m.Post(ctx, transfer("too-much", a.ID, b.ID, 701))
		wantErr(t, err, ledger.ErrInsufficientFunds)
	})

	t.Run("create replays by key", func(t *testing.T) {
		again, err := e.m.Post(ctx, create)
		if err != nil || again.ID != txn.ID {
			t.Fatalf("replay = %+v, %v", again, err)
		}
		changed := create
		changed.Status = ledger.TransactionPosted
		_, err = e.m.Post(ctx, changed)
		wantErr(t, err, ledger.ErrIdempotencyConflict)
	})

	t.Run("update replaces entries as a new version", func(t *testing.T) {
		updated, err := e.m.UpdateTransaction(ctx, txn.ID, ledger.UpdateTransactionInput{
			Description: str("smaller invoice"),
			Metadata:    json.RawMessage(`{"order":null,"note":"revised"}`),
			Postings:    transfer("", a.ID, b.ID, 250).Postings,
		})
		if err != nil || updated.Version != 2 || updated.Postings[0].Amount != amt(250) || updated.Description != "smaller invoice" {
			t.Fatalf("updated = %+v, %v", updated, err)
		}
		if !jsonSame(t, updated.Metadata, `{"note":"revised"}`) {
			t.Fatalf("metadata = %s", updated.Metadata)
		}
		wantBalance(t, "a available", e.get(t, a.ID).Available, 1_000, 250, 750)

		same, err := e.m.UpdateTransaction(ctx, txn.ID, ledger.UpdateTransactionInput{Description: str("smaller invoice")})
		if err != nil || same.Version != 2 {
			t.Fatalf("no-op update = %+v, %v", same, err)
		}

		_, err = e.m.UpdateTransaction(ctx, txn.ID, ledger.UpdateTransactionInput{Postings: transfer("", a.ID, b.ID, 1_001).Postings})
		wantErr(t, err, ledger.ErrInsufficientFunds)
	})

	t.Run("post moves pending into the journal", func(t *testing.T) {
		posted, err := e.m.PostTransaction(ctx, txn.ID, ledger.PostPendingInput{})
		if err != nil || posted.Status != ledger.TransactionPosted || posted.Version != 3 || posted.PostedAt == nil {
			t.Fatalf("posted = %+v, %v", posted, err)
		}
		acc := e.get(t, a.ID)
		wantBalance(t, "a posted", acc.Posted, 1_000, 250, 750)
		if acc.Pending != acc.Posted || acc.Available != acc.Posted {
			t.Fatalf("a still has pending money: %+v", acc)
		}
		wantBalance(t, "b available", e.get(t, b.ID).Available, 250, 0, 250)

		again, err := e.m.PostTransaction(ctx, txn.ID, ledger.PostPendingInput{})
		if err != nil || again.Version != 3 {
			t.Fatalf("second post = %+v, %v", again, err)
		}
		_, err = e.m.ArchiveTransaction(ctx, txn.ID)
		wantErr(t, err, ledger.ErrNotPending)
		_, err = e.m.UpdateTransaction(ctx, txn.ID, ledger.UpdateTransactionInput{Description: str("x")})
		wantErr(t, err, ledger.ErrNotPending)

		replay, err := e.m.Post(ctx, create)
		if err != nil || replay.ID != txn.ID || replay.Status != ledger.TransactionPosted {
			t.Fatalf("original create replayed after posting = %+v, %v", replay, err)
		}
		lines, err := e.m.AccountEntries(ctx, b.ID, 0, 10)
		if err != nil || len(lines) != 1 || lines[0].TransactionID != txn.ID || lines[0].BalanceAfter != amt(250) {
			t.Fatalf("b statement = %+v, %v", lines, err)
		}
	})
	e.verify(t)
}

func TestPartialPost(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	a := e.funded(t, 1_000)
	b := e.account(t, "USD", ledger.Debit)
	c := e.account(t, "USD", ledger.Debit)
	txn := e.post(t, pending(transfer("auth", a.ID, b.ID, 500)))

	for name, tt := range map[string]struct {
		postings []ledger.Posting
		want     error
	}{
		"more than pending":   {transfer("", a.ID, b.ID, 501).Postings, ledger.ErrInvalid},
		"account not pending": {transfer("", a.ID, c.ID, 100).Postings, ledger.ErrInvalid},
		"unbalanced": {[]ledger.Posting{
			{AccountID: b.ID, Side: ledger.Debit, Amount: amt(100)},
			{AccountID: a.ID, Side: ledger.Credit, Amount: amt(99)},
		}, ledger.ErrUnbalanced},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := e.m.PostTransaction(ctx, txn.ID, ledger.PostPendingInput{Postings: tt.postings})
			wantErr(t, err, tt.want)
		})
	}

	posted, err := e.m.PostTransaction(ctx, txn.ID, ledger.PostPendingInput{Postings: transfer("", a.ID, b.ID, 200).Postings})
	if err != nil || posted.Status != ledger.TransactionPosted || posted.Postings[0].Amount != amt(200) {
		t.Fatalf("partial post = %+v, %v", posted, err)
	}
	acc := e.get(t, a.ID)
	wantBalance(t, "a posted", acc.Posted, 1_000, 200, 800)
	wantBalance(t, "a available", acc.Available, 1_000, 200, 800)
	wantBalance(t, "b posted", e.get(t, b.ID).Posted, 200, 0, 200)

	again, err := e.m.PostTransaction(ctx, txn.ID, ledger.PostPendingInput{Postings: transfer("", a.ID, b.ID, 200).Postings})
	if err != nil || again.Version != posted.Version {
		t.Fatalf("retried partial post = %+v, %v", again, err)
	}
	_, err = e.m.PostTransaction(ctx, txn.ID, ledger.PostPendingInput{Postings: transfer("", a.ID, b.ID, 300).Postings})
	wantErr(t, err, ledger.ErrNotPending)
	e.verify(t)
}

func TestArchive(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	a := e.funded(t, 100)
	b := e.account(t, "USD", ledger.Debit)
	txn := e.post(t, pending(transfer("p", a.ID, b.ID, 60)))

	_, err := e.m.Reverse(ctx, txn.ID, ledger.ReverseInput{IdempotencyKey: "r"})
	wantErr(t, err, ledger.ErrNotPosted)

	archived, err := e.m.ArchiveTransaction(ctx, txn.ID)
	if err != nil || archived.Status != ledger.TransactionArchived || archived.ArchivedAt == nil || len(archived.Postings) != 2 {
		t.Fatalf("archived = %+v, %v", archived, err)
	}
	wantBalance(t, "a available", e.get(t, a.ID).Available, 100, 0, 100)
	wantBalance(t, "b pending", e.get(t, b.ID).Pending, 0, 0, 0)

	if again, err := e.m.ArchiveTransaction(ctx, txn.ID); err != nil || again.Version != archived.Version {
		t.Fatalf("second archive = %+v, %v", again, err)
	}
	_, err = e.m.PostTransaction(ctx, txn.ID, ledger.PostPendingInput{})
	wantErr(t, err, ledger.ErrNotPending)
	_, err = e.m.ArchiveTransaction(ctx, uuid.New())
	wantErr(t, err, ledger.ErrNotFound)

	txns, err := e.m.ListTransactions(ctx, ledger.ListTransactionsInput{Status: ledger.TransactionArchived, Limit: 10})
	if err != nil || len(txns) != 1 || txns[0].ID != txn.ID {
		t.Fatalf("archived list = %+v, %v", txns, err)
	}
	e.verify(t)
}

func TestExternalIDs(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	a := e.account(t, "USD", ledger.Debit, unrestricted)
	b := e.account(t, "USD", ledger.Debit, unrestricted)
	withExternal := func(key, external string) ledger.PostInput {
		in := transfer(key, a.ID, b.ID, 1)
		in.ExternalID = external
		return in
	}

	first := e.post(t, withExternal("k1", "invoice-9"))
	_, err := e.m.Post(ctx, withExternal("k2", "invoice-9"))
	wantErr(t, err, ledger.ErrExternalIDExists)
	_, err = e.m.Post(ctx, pending(withExternal("k3", "invoice-9")))
	wantErr(t, err, ledger.ErrExternalIDExists)

	results, err := e.m.PostBatch(ctx, []ledger.PostInput{withExternal("b1", "dup"), withExternal("b2", "dup")}, false)
	if err != nil || results[0].Err != nil || !errors.Is(results[1].Err, ledger.ErrExternalIDExists) {
		t.Fatalf("batch = %+v, %v", results, err)
	}

	other, err := e.m.CreateLedger(ctx, ledger.CreateLedgerInput{Name: "other"})
	if err != nil {
		t.Fatal(err)
	}
	x := e.accountIn(t, other.ID)
	y := e.accountIn(t, other.ID)
	in := transfer("other-ledger", x.ID, y.ID, 1)
	in.ExternalID = "invoice-9"
	e.post(t, in)

	found, err := e.m.ListTransactions(ctx, ledger.ListTransactionsInput{LedgerID: e.ledger.ID, ExternalID: "invoice-9", Limit: 10})
	if err != nil || len(found) != 1 || found[0].ID != first.ID {
		t.Fatalf("lookup by external id = %+v, %v", found, err)
	}
	e.verify(t)
}

func TestPendingOnFrozenAndClosedAccounts(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	a := e.funded(t, 100)
	b := e.account(t, "USD", ledger.Debit)
	toArchive := e.post(t, pending(transfer("archive-me", a.ID, b.ID, 10)))
	toPost := e.post(t, pending(transfer("post-me", a.ID, b.ID, 10)))

	_, err := e.m.CloseAccount(ctx, b.ID)
	wantErr(t, err, ledger.ErrAccountNotEmpty)

	if _, err := e.m.FreezeAccount(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	_, err = e.m.Post(ctx, pending(transfer("new", a.ID, b.ID, 1)))
	wantErr(t, err, ledger.ErrAccountNotOpen)
	_, err = e.m.PostTransaction(ctx, toPost.ID, ledger.PostPendingInput{})
	wantErr(t, err, ledger.ErrAccountNotOpen)
	if _, err := e.m.ArchiveTransaction(ctx, toArchive.ID); err != nil {
		t.Fatalf("archive on a frozen account: %v", err)
	}
	e.verify(t)
}

func TestConcurrentPostAndArchive(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	a := e.funded(t, 10_000)
	b := e.account(t, "USD", ledger.Debit)

	for i := range 20 {
		txn := e.post(t, pending(transfer(fmt.Sprint("race-", i), a.ID, b.ID, 10)))
		var (
			wg               sync.WaitGroup
			postErr, archErr error
			posted, archived ledger.Transaction
		)
		wg.Go(func() { posted, postErr = e.m.PostTransaction(ctx, txn.ID, ledger.PostPendingInput{}) })
		wg.Go(func() { archived, archErr = e.m.ArchiveTransaction(ctx, txn.ID) })
		wg.Wait()
		switch {
		case postErr == nil && errors.Is(archErr, ledger.ErrNotPending):
			if posted.Status != ledger.TransactionPosted {
				t.Fatalf("post won with status %s", posted.Status)
			}
		case archErr == nil && errors.Is(postErr, ledger.ErrNotPending):
			if archived.Status != ledger.TransactionArchived {
				t.Fatalf("archive won with status %s", archived.Status)
			}
		default:
			t.Fatalf("race %d: post err %v, archive err %v", i, postErr, archErr)
		}
	}
	acc := e.get(t, a.ID)
	if acc.Pending != acc.Posted {
		t.Fatalf("pending money left behind: %+v", acc)
	}
	e.verify(t)
}

func TestLifecycleSchemaInvariants(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	a := e.funded(t, 100)
	b := e.account(t, "USD", ledger.Debit)
	open := e.post(t, pending(transfer("open", a.ID, b.ID, 5)))
	posted := e.post(t, transfer("done", a.ID, b.ID, 5))
	archived, err := e.m.ArchiveTransaction(ctx, e.post(t, pending(transfer("gone", a.ID, b.ID, 5))).ID)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name, wantCode, sql string
		args                []any
	}{
		{"journal postings on a pending transaction", "23001",
			`INSERT INTO ledger_postings (transaction_id, account_id, currency, side, amount, balance_after)
			 VALUES ($1, $2, 'USD', 'debit', 1, 0), ($1, $3, 'USD', 'credit', 1, 0)`, []any{open.ID, b.ID, a.ID}},
		{"pending entries on a posted transaction", "23001",
			`INSERT INTO ledger_pending_entries (transaction_id, version, account_id, currency, side, amount)
			 VALUES ($1, 1, $2, 'USD', 'debit', 1), ($1, 1, $3, 'USD', 'credit', 1)`, []any{posted.ID, b.ID, a.ID}},
		{"entries on an archived transaction", "23001",
			`INSERT INTO ledger_pending_entries (transaction_id, version, account_id, currency, side, amount)
			 VALUES ($1, 1, $2, 'USD', 'debit', 1), ($1, 1, $3, 'USD', 'credit', 1)`, []any{archived.ID, b.ID, a.ID}},
		{"stale pending entry version", "23001",
			`INSERT INTO ledger_pending_entries (transaction_id, version, account_id, currency, side, amount)
			 VALUES ($1, 7, $2, 'USD', 'debit', 1), ($1, 7, $3, 'USD', 'credit', 1)`, []any{open.ID, b.ID, a.ID}},
		{"edit pending entry", "23001", `UPDATE ledger_pending_entries SET amount = 1 WHERE transaction_id = $1`, []any{open.ID}},
		{"change posted transaction", "23001",
			`UPDATE ledger_transactions SET metadata = '{"x":1}', version = version + 1 WHERE id = $1`, []any{posted.ID}},
		{"skip a version", "23001",
			`UPDATE ledger_transactions SET description = 'x', version = version + 2 WHERE id = $1`, []any{open.ID}},
		{"post without journal entries in another transaction", "23001",
			`UPDATE ledger_transactions SET status = 'posted', version = version + 1, posted_at = now(),
			 posted_xid = '1'::xid8 WHERE id = $1`, []any{open.ID}},
		{"spend reserved funds", "23514",
			`UPDATE ledger_accounts SET posted_credits = posted_debits - 4, version = version + 1 WHERE id = $1`, []any{a.ID}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) error {
				_, err := tx.Exec(ctx, tt.sql, tt.args...)
				return err
			})
			if got := db.Code(err); got != tt.wantCode {
				t.Fatalf("error = %v (code %q), want %s", err, got, tt.wantCode)
			}
		})
	}
	e.verify(t)
}

func TestWidePendingAmounts(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	if _, err := e.m.CreateCurrency(ctx, ledger.CreateCurrencyInput{Code: "ETH", Exponent: 18}); err != nil {
		t.Fatal(err)
	}
	treasury := e.account(t, "ETH", ledger.Credit, unrestricted)
	wallet := e.account(t, "ETH", ledger.Debit)
	big := money.MustParseAmount("40000000000000000000000000000")
	e.post(t, transferAmount("mint", treasury.ID, wallet.ID, big))

	out := e.account(t, "ETH", ledger.Debit)
	txn := e.post(t, pending(transferAmount("withdraw", wallet.ID, out.ID, big)))
	if got := e.get(t, wallet.ID).Available.Amount; !got.IsZero() {
		t.Fatalf("available = %s, want 0", got)
	}
	half := money.MustParseAmount("20000000000000000000000000000")
	if _, err := e.m.PostTransaction(ctx, txn.ID, ledger.PostPendingInput{Postings: transferAmount("", wallet.ID, out.ID, half).Postings}); err != nil {
		t.Fatal(err)
	}
	if got := e.get(t, wallet.ID).Available.Amount; got != half {
		t.Fatalf("available after partial post = %s, want %s", got, half)
	}
	e.verify(t)
}

func TestHTTPLifecycle(t *testing.T) {
	a := newAPI(t)
	equity := a.account("equity", "credit", `,"allow_negative":true`)
	cash := a.account("cash", "debit", "")
	merchant := a.account("merchant", "debit", "")
	a.must(http.StatusCreated, http.MethodPost, "/v1/transactions", "fund", transferJSON(equity, cash, "1000"))

	body := fmt.Sprintf(`{"status":"pending","external_id":"auth-1","effective_at":"2026-01-02T03:04:05Z","entries":[
		{"account_id":%q,"side":"debit","amount":"400"},
		{"account_id":%q,"side":"credit","amount":"400"}]}`, merchant, cash)
	txn := a.must(http.StatusCreated, http.MethodPost, "/v1/transactions", "auth", body)
	id := requirePrefix(t, txn["id"], "txn")
	effective, _ := time.Parse(time.RFC3339, fmt.Sprint(txn["effective_at"]))
	if txn["status"] != "pending" || txn["external_id"] != "auth-1" || !effective.Equal(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)) || txn["posted_at"] != nil {
		t.Fatalf("pending = %v", txn)
	}
	acc := a.must(http.StatusOK, http.MethodGet, "/v1/accounts/"+cash, "", "")
	if balanceAmount(acc, "available") != "600" || balanceAmount(acc, "posted") != "1000" || balanceAmount(acc, "pending") != "600" {
		t.Fatalf("cash balances = %v", acc["balances"])
	}

	patched := a.must(http.StatusOK, http.MethodPatch, "/v1/transactions/"+id, "", `{"description":"card auth","metadata":{"mcc":"5411"}}`)
	if patched["version"] != float64(2) || patched["description"] != "card auth" {
		t.Fatalf("patched = %v", patched)
	}
	if n := len(a.list("/v1/transactions?status=pending")); n != 1 {
		t.Fatalf("pending transactions = %d, want 1", n)
	}

	posted := a.must(http.StatusOK, http.MethodPost, "/v1/transactions/"+id+"/post", "", fmt.Sprintf(`{"entries":[
		{"account_id":%q,"side":"debit","amount":"150"},
		{"account_id":%q,"side":"credit","amount":"150"}]}`, merchant, cash))
	if posted["status"] != "posted" || posted["posted_at"] == nil {
		t.Fatalf("posted = %v", posted)
	}
	acc = a.must(http.StatusOK, http.MethodGet, "/v1/accounts/"+cash, "", "")
	if balanceAmount(acc, "available") != "850" || balanceAmount(acc, "posted") != "850" {
		t.Fatalf("cash after partial post = %v", acc["balances"])
	}
	a.must(http.StatusOK, http.MethodPost, "/v1/transactions/"+id+"/post", "", "")

	other := a.must(http.StatusCreated, http.MethodPost, "/v1/transactions", "auth-2",
		`{"status":"pending",`+transferJSON(cash, merchant, "10")[1:])
	otherID := other["id"].(string)
	archived := a.must(http.StatusOK, http.MethodPost, "/v1/transactions/"+otherID+"/archive", "", "")
	if archived["status"] != "archived" || archived["archived_at"] == nil {
		t.Fatalf("archived = %v", archived)
	}

	tests := []struct {
		name, method, path, key, body string
		status                        int
		code                          string
	}{
		{"post archived", http.MethodPost, "/v1/transactions/" + otherID + "/post", "", "", 409, "transaction_not_pending"},
		{"patch posted", http.MethodPatch, "/v1/transactions/" + id, "", `{"description":"x"}`, 409, "transaction_not_pending"},
		{"reverse archived", http.MethodPost, "/v1/transactions/" + otherID + "/reverse", "rev", "", 409, "transaction_not_posted"},
		{"external id taken", http.MethodPost, "/v1/transactions", "dup",
			`{"external_id":"auth-1",` + transferJSON(equity, cash, "1")[1:], 409, "external_id_exists"},
		{"bad status", http.MethodPost, "/v1/transactions", "bad-status",
			`{"status":"void",` + transferJSON(equity, cash, "1")[1:], 422, "validation_error"},
		{"bad status filter", http.MethodGet, "/v1/transactions?status=done", "", "", 422, "validation_error"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := a.do(tt.method, tt.path, tt.key, tt.body)
			if resp.status != tt.status || resp.body["code"] != tt.code {
				t.Fatalf("%s %s = %d %v, want %d %s", tt.method, tt.path, resp.status, resp.body, tt.status, tt.code)
			}
		})
	}
}
