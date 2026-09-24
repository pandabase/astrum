package tests

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/pandabase/astrum/internal/modules/ledger"
	"github.com/pandabase/astrum/internal/money"
)

func bound(n int64) *money.Amount {
	a := money.NewAmount(n)
	return &a
}

func locked(in ledger.PostInput, account uuid.UUID, set func(*ledger.Posting)) ledger.PostInput {
	postings := append([]ledger.Posting(nil), in.Postings...)
	for i := range postings {
		if postings[i].AccountID == account {
			set(&postings[i])
		}
	}
	in.Postings = postings
	return in
}

func availableAtLeast(n int64) func(*ledger.Posting) {
	return func(p *ledger.Posting) { p.AvailableBalance = &ledger.BalanceCondition{GTE: bound(n)} }
}

func TestBalanceConditions(t *testing.T) {
	t.Parallel()
	e := setup(t)
	ctx := context.Background()

	a := e.account(t, "USD", ledger.Debit, unrestricted)
	b := e.account(t, "USD", ledger.Debit, unrestricted)
	e.post(t, transfer("fund", e.open.ID, a.ID, 100))

	t.Run("passing lock returns resulting balances", func(t *testing.T) {
		txn := e.post(t, locked(transfer("ok", a.ID, b.ID, 60), a.ID, availableAtLeast(0)))
		got := txn.Postings[1].Resulting
		if got == nil || got.Posted.Amount != amt(40) || got.Available.Amount != amt(40) {
			t.Fatalf("resulting = %+v", got)
		}
		if b := txn.Postings[0].Resulting; b == nil || b.Posted.Amount != amt(60) {
			t.Fatalf("receiver resulting = %+v", b)
		}
	})

	for name, tt := range map[string]struct {
		in   ledger.PostInput
		want error
	}{
		"available below zero": {locked(transfer("overdraw", a.ID, b.ID, 41), a.ID, availableAtLeast(0)), ledger.ErrBalanceLock},
		"receiver cap": {locked(transfer("cap", a.ID, b.ID, 1), b.ID, func(p *ledger.Posting) {
			p.PostedBalance = &ledger.BalanceCondition{LTE: bound(60)}
		}), ledger.ErrBalanceLock},
		"exact balance": {locked(transfer("exact", a.ID, b.ID, 10), a.ID, func(p *ledger.Posting) {
			p.PostedBalance = &ledger.BalanceCondition{EQ: bound(31)}
		}), ledger.ErrBalanceLock},
		"must not empty": {locked(transfer("empty", a.ID, b.ID, 40), a.ID, func(p *ledger.Posting) {
			p.PostedBalance = &ledger.BalanceCondition{NotEQ: bound(0)}
		}), ledger.ErrBalanceLock},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := e.m.Post(ctx, tt.in)
			wantErr(t, err, tt.want)
		})
	}
	if got := e.get(t, a.ID).Posted.Amount; got != amt(40) {
		t.Fatalf("a = %s after rejected locks, want 40", got)
	}

	t.Run("locks see the whole transaction", func(t *testing.T) {

		in := ledger.PostInput{IdempotencyKey: "net", Postings: []ledger.Posting{
			{AccountID: a.ID, Side: ledger.Credit, Amount: amt(50), AvailableBalance: &ledger.BalanceCondition{GTE: bound(0)}},
			{AccountID: b.ID, Side: ledger.Debit, Amount: amt(50)},
			{AccountID: b.ID, Side: ledger.Credit, Amount: amt(30)},
			{AccountID: a.ID, Side: ledger.Debit, Amount: amt(30)},
		}}
		txn := e.post(t, in)
		if got := txn.Postings[0].Resulting.Posted.Amount; got != amt(20) {
			t.Fatalf("resulting = %s, want 20", got)
		}
	})

	t.Run("pending balance locks", func(t *testing.T) {
		in := locked(pending(transfer("hold-all", a.ID, b.ID, 20)), a.ID, func(p *ledger.Posting) {
			p.PendingBalance = &ledger.BalanceCondition{GTE: bound(1)}
		})
		_, err := e.m.Post(ctx, in)
		wantErr(t, err, ledger.ErrBalanceLock)
	})
	e.verify(t)
}

func TestLockVersion(t *testing.T) {
	t.Parallel()
	e := setup(t)
	ctx := context.Background()
	a := e.funded(t, 100)
	b := e.account(t, "USD", ledger.Debit)
	version := e.get(t, a.ID).Version
	at := func(v int64) func(*ledger.Posting) { return func(p *ledger.Posting) { p.LockVersion = &v } }

	e.post(t, locked(transfer("first", a.ID, b.ID, 1), a.ID, at(version)))
	_, err := e.m.Post(ctx, locked(transfer("stale", a.ID, b.ID, 1), a.ID, at(version)))
	wantErr(t, err, ledger.ErrLockVersion)

	current := e.get(t, a.ID).Version
	results, err := e.m.PostBatch(ctx, []ledger.PostInput{
		locked(transfer("batch-1", a.ID, b.ID, 1), a.ID, at(current)),
		locked(transfer("batch-2", a.ID, b.ID, 1), a.ID, at(current)),
	}, false)
	if err != nil || results[0].Err != nil || !errors.Is(results[1].Err, ledger.ErrLockVersion) {
		t.Fatalf("batch = %+v, %v", results, err)
	}
	if _, err := e.m.UpdateAccount(ctx, a.ID, ledger.UpdateInput{Name: new("renamed")}); err != nil {
		t.Fatal(err)
	}
	_, err = e.m.Post(ctx, locked(transfer("after-rename", a.ID, b.ID, 1), a.ID, at(current+1)))
	wantErr(t, err, ledger.ErrLockVersion)
	e.verify(t)
}

func TestArchiveOnLockFailure(t *testing.T) {
	t.Parallel()
	e := setup(t)
	ctx := context.Background()
	a := e.funded(t, 100)
	b := e.account(t, "USD", ledger.Debit)

	in := locked(transfer("audit", a.ID, b.ID, 50), a.ID, func(p *ledger.Posting) {
		p.PostedBalance = &ledger.BalanceCondition{GTE: bound(60)}
	})
	in.ArchiveOnLockFailure = true
	txn, err := e.m.Post(ctx, in)
	if err != nil || txn.Status != ledger.TransactionArchived || txn.ArchivedAt == nil || len(txn.Postings) != 2 {
		t.Fatalf("archived = %+v, %v", txn, err)
	}
	if got := e.get(t, a.ID); got.Posted.Amount != amt(100) || got.Available.Amount != amt(100) {
		t.Fatalf("a = %+v after archived lock failure", got)
	}
	if again, err := e.m.Post(ctx, in); err != nil || again.ID != txn.ID {
		t.Fatalf("replay = %+v, %v", again, err)
	}
	stored, err := e.m.Transaction(ctx, txn.ID)
	if err != nil || stored.Status != ledger.TransactionArchived || stored.Postings[0].Currency != "USD" {
		t.Fatalf("stored = %+v, %v", stored, err)
	}

	passing := locked(transfer("fine", a.ID, b.ID, 10), a.ID, availableAtLeast(0))
	passing.ArchiveOnLockFailure = true
	if txn := e.post(t, passing); txn.Status != ledger.TransactionPosted {
		t.Fatalf("passing lock status = %s", txn.Status)
	}

	stale := int64(0)
	versioned := locked(transfer("stale", a.ID, b.ID, 10), a.ID, func(p *ledger.Posting) { p.LockVersion = &stale })
	versioned.ArchiveOnLockFailure = true
	if txn := e.post(t, versioned); txn.Status != ledger.TransactionArchived {
		t.Fatalf("stale lock_version status = %s", txn.Status)
	}

	broke := transfer("broke", a.ID, b.ID, 1_000)
	broke.ArchiveOnLockFailure = true
	_, err = e.m.Post(ctx, broke)
	wantErr(t, err, ledger.ErrInsufficientFunds)
	e.verify(t)
}

func TestConcurrentLocksHold(t *testing.T) {
	t.Parallel()
	e := setup(t)
	ctx := context.Background()
	a := e.account(t, "USD", ledger.Debit, unrestricted)
	b := e.account(t, "USD", ledger.Debit, unrestricted)
	e.post(t, transfer("fund", e.open.ID, a.ID, 100))

	var (
		wg              sync.WaitGroup
		posted, refused atomic.Int64
	)
	for i := range 40 {
		wg.Go(func() {
			_, err := e.m.Post(ctx, locked(transfer(fmt.Sprint("spend-", i), a.ID, b.ID, 7), a.ID, availableAtLeast(0)))
			switch {
			case err == nil:
				posted.Add(1)
			case errors.Is(err, ledger.ErrBalanceLock):
				refused.Add(1)
			default:
				t.Errorf("unexpected: %v", err)
			}
		})
	}
	wg.Wait()
	if posted.Load() != 14 || refused.Load() != 26 {
		t.Fatalf("posted %d, refused %d; want 14 and 26", posted.Load(), refused.Load())
	}
	if got := e.get(t, a.ID).Posted.Amount; got != amt(2) {
		t.Fatalf("a = %s, want 2", got)
	}
	e.verify(t)
}

func TestHTTPBalanceLocks(t *testing.T) {
	t.Parallel()
	a := newAPI(t)
	equity := a.account("equity", "credit", `,"allow_negative":true`)
	cash := a.account("cash", "debit", `,"allow_negative":true`)
	a.must(http.StatusCreated, http.MethodPost, "/v1/transactions", "fund", transferJSON(equity, cash, "100"))
	version := a.must(http.StatusOK, http.MethodGet, "/v1/accounts/"+cash, "", "")["lock_version"]

	guarded := func(amount string, lock string) string {
		return fmt.Sprintf(`{"entries":[
			{"account_id":%q,"side":"debit","amount":%q},
			{"account_id":%q,"side":"credit","amount":%q,%s}]}`, equity, amount, cash, amount, lock)
	}
	txn := a.must(http.StatusCreated, http.MethodPost, "/v1/transactions", "spend",
		guarded("30", fmt.Sprintf(`"available_balance_amount":{"gte":"0"},"lock_version":%v`, version)))
	resulting := txn["entries"].([]any)[1].(map[string]any)["resulting_balances"].(map[string]any)
	if resulting["available"].(map[string]any)["amount"] != "70" {
		t.Fatalf("resulting = %v", resulting)
	}
	if got := a.must(http.StatusOK, http.MethodGet, "/v1/transactions/"+txn["id"].(string), "", "")["entries"].([]any)[1]; got.(map[string]any)["resulting_balances"] != nil {
		t.Fatalf("read returned resulting balances: %v", got)
	}

	archived := a.must(http.StatusCreated, http.MethodPost, "/v1/transactions", "audit",
		`{"archive_on_balance_lock_failure":true,`+guarded("80", `"available_balance_amount":{"gte":"0"}`)[1:])
	if archived["status"] != "archived" {
		t.Fatalf("archived = %v", archived)
	}

	tests := []struct {
		name, key, body string
		status          int
		code            string
	}{
		{"lock fails", "k1", guarded("71", `"available_balance_amount":{"gte":"0"}`), 422, "balance_lock_failed"},
		{"stale lock_version", "k2", guarded("1", fmt.Sprintf(`"lock_version":%v`, version)), 409, "lock_version_conflict"},
		{"bad bound", "k3", guarded("1", `"available_balance_amount":{"gte":0}`), 400, "invalid_request"},
		{"negative lock_version", "k4", guarded("1", `"lock_version":-1`), 422, "validation_error"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := a.do(http.MethodPost, "/v1/transactions", tt.key, tt.body)
			if resp.status != tt.status || resp.body["code"] != tt.code {
				t.Fatalf("= %d %v, want %d %s", resp.status, resp.body, tt.status, tt.code)
			}
		})
	}
}
