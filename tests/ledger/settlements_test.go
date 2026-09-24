package tests

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pandabase/astrum/internal/kernel/db"
	"github.com/pandabase/astrum/internal/modules/ledger"
)

func settle(key string, settled, contra uuid.UUID) ledger.CreateSettlementInput {
	return ledger.CreateSettlementInput{IdempotencyKey: key, SettledAccountID: settled, ContraAccountID: contra, Description: "payout"}
}

func TestSettlement(t *testing.T) {
	t.Parallel()
	e := setup(t)
	ctx := context.Background()
	cash := e.account(t, "USD", ledger.Debit, unrestricted)
	acme := e.account(t, "USD", ledger.Credit, unrestricted)
	payouts := e.account(t, "USD", ledger.Debit, unrestricted)

	e.post(t, transfer("sale-1", acme.ID, cash.ID, 100))
	e.post(t, transfer("sale-2", acme.ID, cash.ID, 50))
	e.post(t, transfer("refund", cash.ID, acme.ID, 30))

	first, err := e.m.CreateSettlement(ctx, settle("payout-1", acme.ID, payouts.ID))
	if err != nil || first.Amount != amt(120) || first.EntryCount != 3 || first.TransactionID == nil {
		t.Fatalf("settlement = %+v, %v", first, err)
	}
	wantBalance(t, "acme after payout", e.get(t, acme.ID).Posted, 30+120, 150, 0)

	wantBalance(t, "payouts", e.get(t, payouts.ID).Posted, 0, 120, -120)

	entries := func(in ledger.ListEntriesInput) []ledger.Entry {
		t.Helper()
		in.Limit = 100
		got, err := e.m.ListEntries(ctx, in)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	if got := entries(ledger.ListEntriesInput{SettlementID: first.ID}); len(got) != 4 || *got[0].SettlementID != first.ID {
		t.Fatalf("settled entries = %+v", got)
	}
	if got := entries(ledger.ListEntriesInput{AccountID: acme.ID, Settled: new(false)}); len(got) != 0 {
		t.Fatalf("unsettled after payout = %+v", got)
	}

	t.Run("later sales settle alone", func(t *testing.T) {
		e.post(t, transfer("sale-3", acme.ID, cash.ID, 40))
		if got := entries(ledger.ListEntriesInput{AccountID: acme.ID, Settled: new(false)}); len(got) != 1 {
			t.Fatalf("unsettled = %+v", got)
		}
		second, err := e.m.CreateSettlement(ctx, settle("payout-2", acme.ID, payouts.ID))
		if err != nil || second.Amount != amt(40) || second.EntryCount != 1 {
			t.Fatalf("second = %+v, %v", second, err)
		}
	})

	t.Run("nothing left settles to zero without a transaction", func(t *testing.T) {
		empty, err := e.m.CreateSettlement(ctx, settle("payout-3", acme.ID, payouts.ID))
		if err != nil || !empty.Amount.IsZero() || empty.TransactionID != nil || empty.EntryCount != 0 {
			t.Fatalf("empty = %+v, %v", empty, err)
		}
	})

	t.Run("a net debit settles the other way", func(t *testing.T) {
		e.post(t, transfer("chargeback", cash.ID, acme.ID, 70))
		owed, err := e.m.CreateSettlement(ctx, settle("recover", acme.ID, payouts.ID))
		if err != nil || owed.Amount != amt(-70) {
			t.Fatalf("owed = %+v, %v", owed, err)
		}
		if got := e.get(t, acme.ID).Posted.Amount; !got.IsZero() {
			t.Fatalf("acme = %s after recovering the chargeback", got)
		}
	})

	t.Run("upper bound", func(t *testing.T) {
		e.post(t, dated("old", acme.ID, cash.ID, 11, 1, ""))
		e.post(t, transfer("new", acme.ID, cash.ID, 22))
		in := settle("bounded", acme.ID, payouts.ID)
		in.UpperBound = new(day(2))
		bounded, err := e.m.CreateSettlement(ctx, in)
		if err != nil || bounded.Amount != amt(11) || bounded.EntryCount != 1 {
			t.Fatalf("bounded = %+v, %v", bounded, err)
		}
		if got := entries(ledger.ListEntriesInput{AccountID: acme.ID, Settled: new(false)}); len(got) != 1 || got[0].Amount != amt(22) {
			t.Fatalf("left unsettled = %+v", got)
		}
	})

	t.Run("replay and validation", func(t *testing.T) {
		again, err := e.m.CreateSettlement(ctx, settle("payout-1", acme.ID, payouts.ID))
		if err != nil || again.ID != first.ID {
			t.Fatalf("replay = %+v, %v", again, err)
		}
		_, err = e.m.CreateSettlement(ctx, settle("payout-1", acme.ID, cash.ID))
		wantErr(t, err, ledger.ErrIdempotencyConflict)
		_, err = e.m.CreateSettlement(ctx, settle("self", acme.ID, acme.ID))
		wantErr(t, err, ledger.ErrInvalid)
		_, err = e.m.CreateSettlement(ctx, settle("eur", acme.ID, e.account(t, "EUR", ledger.Debit).ID))
		wantErr(t, err, ledger.ErrInvalid)
		_, err = e.m.CreateSettlement(ctx, settle("ghost", acme.ID, uuid.New()))
		wantErr(t, err, ledger.ErrNotFound)
	})

	t.Run("schema refuses double settlement", func(t *testing.T) {
		tests := []struct {
			name, code, sql string
		}{
			{"settle an entry twice", "23505", `
				INSERT INTO ledger_settlement_entries (posting_id, settlement_id)
				SELECT posting_id, '` + first.ID.String() + `' FROM ledger_settlement_entries LIMIT 1`},
			{"settle another account's entry", "23514", `
				INSERT INTO ledger_settlement_entries (posting_id, settlement_id)
				SELECT p.id, '` + first.ID.String() + `' FROM ledger_postings AS p
				WHERE p.account_id = '` + cash.ID.String() + `' LIMIT 1`},
			{"unsettle", "23001", `DELETE FROM ledger_settlement_entries`},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				err := pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) error {
					_, err := tx.Exec(ctx, tt.sql)
					return err
				})
				if got := db.Code(err); got != tt.code {
					t.Fatalf("error = %v (code %q), want %s", err, got, tt.code)
				}
			})
		}
	})
	e.verify(t)
}

func TestConcurrentSettlements(t *testing.T) {
	t.Parallel()
	e := setup(t)
	ctx := context.Background()
	acme := e.account(t, "USD", ledger.Credit, unrestricted)
	payouts := e.account(t, "USD", ledger.Debit, unrestricted)
	for i := range 20 {
		e.post(t, transfer(fmt.Sprint("sale-", i), acme.ID, payouts.ID, 5))
	}
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		settled = amt(0)
	)
	for i := range 8 {
		wg.Go(func() {
			st, err := e.m.CreateSettlement(ctx, settle(fmt.Sprint("race-", i), acme.ID, payouts.ID))
			if err != nil && !errors.Is(err, context.Canceled) {
				t.Error(err)
				return
			}
			mu.Lock()
			defer mu.Unlock()
			settled, _ = settled.Add(st.Amount)
		})
	}
	wg.Wait()
	if settled != amt(100) {
		t.Fatalf("settlements moved %s in total, want exactly 100", settled)
	}
	e.verify(t)
}

func TestHTTPSettlements(t *testing.T) {
	t.Parallel()
	a := newAPI(t)
	cash := a.account("cash", "debit", `,"allow_negative":true`)
	vendor := a.account("vendor", "credit", `,"allow_negative":true`)
	transit := a.account("transit", "debit", `,"allow_negative":true`)
	a.must(http.StatusCreated, http.MethodPost, "/v1/transactions", "sale", transferJSON(vendor, cash, "90"))

	body := fmt.Sprintf(`{"settled_account_id":%q,"contra_account_id":%q,"description":"weekly payout"}`, vendor, transit)
	st := a.must(http.StatusCreated, http.MethodPost, "/v1/settlements", "weekly", body)
	id := requirePrefix(t, st["id"], "stl")
	if st["object"] != "settlement" || st["amount"] != "90" || st["entry_count"] != float64(1) {
		t.Fatalf("settlement = %v", st)
	}
	requirePrefix(t, st["transaction_id"], "txn")
	a.must(http.StatusOK, http.MethodGet, "/v1/settlements/"+id, "", "")
	if n := len(a.list("/v1/settlements?account_id=" + vendor)); n != 1 {
		t.Fatalf("settlements = %d", n)
	}
	settledEntries := a.list("/v1/entries?settlement_id=" + id)
	if len(settledEntries) != 2 || settledEntries[0]["settlement_id"] != id {
		t.Fatalf("settled entries = %v", settledEntries)
	}
	if n := len(a.list("/v1/entries?settled=false&account_id=" + vendor)); n != 0 {
		t.Fatalf("unsettled vendor entries = %d", n)
	}
	a.must(http.StatusBadRequest, http.MethodPost, "/v1/settlements", "", body)
	a.must(http.StatusBadRequest, http.MethodGet, "/v1/entries?settled=maybe", "", "")
	a.must(http.StatusUnprocessableEntity, http.MethodPost, "/v1/settlements", "self",
		fmt.Sprintf(`{"settled_account_id":%q,"contra_account_id":%q}`, vendor, vendor))
}
