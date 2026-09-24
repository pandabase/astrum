package tests

import (
	"context"
	"encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pandabase/astrum/internal/kernel/events"
	"github.com/pandabase/astrum/internal/kernel/testdb"
	"github.com/pandabase/astrum/internal/kernel/typeid"
	"github.com/pandabase/astrum/internal/modules/ledger"
)

func (e *env) published(t *testing.T, types ...string) []map[string]any {
	t.Helper()
	svc := events.NewService(e.pool, testdb.Logger(), events.Config{})
	var out []map[string]any
	var before uuid.UUID
	for {
		page, err := svc.ListEvents(context.Background(), events.ListEventsInput{Before: before, Limit: 100})
		if err != nil {
			t.Fatal(err)
		}
		for _, ev := range page {
			if len(types) > 0 && !slices.Contains(types, ev.Type) {
				continue
			}
			var data map[string]any
			if err := json.Unmarshal(ev.Data, &data); err != nil {
				t.Fatal(err)
			}
			data["_type"] = ev.Type
			out = append(out, data)
		}
		if len(page) < 100 {
			break
		}
		before = page[len(page)-1].ID
	}
	slices.Reverse(out)
	return out
}

func countTypes(evs []map[string]any) map[string]int {
	counts := map[string]int{}
	for _, ev := range evs {
		counts[ev["_type"].(string)]++
	}
	return counts
}

func TestLedgerEmitsEvents(t *testing.T) {
	t.Parallel()
	e := setup(t)
	ctx := context.Background()
	a := e.funded(t, 1_000)
	b := e.account(t, "USD", ledger.Debit)

	p := e.post(t, pending(transfer("p", a.ID, b.ID, 10)))
	if _, err := e.m.UpdateTransaction(ctx, p.ID, ledger.UpdateTransactionInput{Description: new("edited")}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.PostTransaction(ctx, p.ID, ledger.PostPendingInput{}); err != nil {
		t.Fatal(err)
	}
	q := e.post(t, pending(transfer("q", a.ID, b.ID, 10)))
	if _, err := e.m.ArchiveTransaction(ctx, q.ID); err != nil {
		t.Fatal(err)
	}
	h, err := e.m.CreateHold(ctx, holdInput("h", a.ID, 50, time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.CaptureHold(ctx, h.ID, ledger.CaptureInput{IdempotencyKey: "cap", Destination: b.ID, Amount: amt(20)}); err != nil {
		t.Fatal(err)
	}
	v, err := e.m.CreateHold(ctx, holdInput("v", a.ID, 5, time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.VoidHold(ctx, v.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.CreateHold(ctx, holdInput("x", a.ID, 5, 20*time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	if n, err := e.m.ExpireHolds(ctx); err != nil || n != 1 {
		t.Fatalf("expire = %d, %v", n, err)
	}
	if _, err := e.m.FreezeAccount(ctx, b.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.UpdateAccount(ctx, a.ID, ledger.UpdateInput{Name: new("wallet")}); err != nil {
		t.Fatal(err)
	}

	e.post(t, pending(transfer("p", a.ID, b.ID, 10)))
	if _, err := e.m.FreezeAccount(ctx, b.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.UpdateAccount(ctx, a.ID, ledger.UpdateInput{Name: new("wallet")}); err != nil {
		t.Fatal(err)
	}
	results, err := e.m.PostBatch(ctx, []ledger.PostInput{transfer("ok", a.ID, e.open.ID, 1), transfer("broke", a.ID, e.open.ID, 1_000_000)}, true)
	if err != nil || results[0].Err == nil {
		t.Fatalf("atomic batch = %+v, %v", results, err)
	}

	got := countTypes(e.published(t))
	want := map[string]int{
		"account.created":      3,
		"account.updated":      2,
		"transaction.created":  4,
		"transaction.updated":  1,
		"transaction.posted":   1,
		"transaction.archived": 1,
		"hold.created":         3,
		"hold.captured":        1,
		"hold.voided":          1,
		"hold.expired":         1,
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("events = %v\nwant     %v", got, want)
	}

	t.Run("payloads are API resources as of the change", func(t *testing.T) {
		posted := e.published(t, "transaction.posted")[0]
		if posted["object"] != "transaction" || posted["status"] != "posted" || posted["description"] != "edited" ||
			!strings.HasPrefix(posted["id"].(string), "txn_") {
			t.Fatalf("transaction.posted = %v", posted)
		}
		frozen := e.published(t, "account.updated")[0]
		if frozen["object"] != "account" || frozen["status"] != "frozen" {
			t.Fatalf("account.updated = %v", frozen)
		}
		captured := e.published(t, "hold.captured")[0]
		if captured["status"] != "captured" || captured["captured_amount"] != "20" {
			t.Fatalf("hold.captured = %v", captured)
		}
	})
}

func TestBalanceMonitorCrossings(t *testing.T) {
	t.Parallel()
	e := setup(t)
	ctx := context.Background()
	a := e.funded(t, 150)
	b := e.account(t, "USD", ledger.Debit)

	low, err := e.m.CreateBalanceMonitor(ctx, ledger.CreateBalanceMonitorInput{
		AccountID:   a.ID,
		Condition:   ledger.AlertCondition{Field: "available", Operator: "lt", Value: amt(100)},
		Description: "float running low",
	})
	if err != nil || low.Triggered {
		t.Fatalf("monitor = %+v, %v", low, err)
	}
	fired := func() int {
		t.Helper()
		return len(e.published(t, "balance_monitor.triggered"))
	}

	steps := []struct {
		name   string
		act    func()
		fired  int
		inside bool
	}{
		{"stays above", func() { e.post(t, transfer("s1", a.ID, b.ID, 40)) }, 0, false},
		{"crosses below", func() { e.post(t, transfer("s2", a.ID, b.ID, 20)) }, 1, true},
		{"stays below", func() { e.post(t, transfer("s3", a.ID, b.ID, 10)) }, 1, true},
		{"back above", func() { e.post(t, transfer("s4", e.open.ID, a.ID, 50)) }, 1, false},
		{"a pending outflow crosses", func() { e.post(t, pending(transfer("s5", a.ID, b.ID, 40))) }, 2, true},
		{"archiving leaves", func() {
			txns, _ := e.m.ListTransactions(ctx, ledger.ListTransactionsInput{Status: ledger.TransactionPending, Limit: 1})
			if _, err := e.m.ArchiveTransaction(ctx, txns[0].ID); err != nil {
				t.Fatal(err)
			}
		}, 2, false},
		{"a hold crosses", func() {
			if _, err := e.m.CreateHold(ctx, holdInput("h", a.ID, 40, time.Hour)); err != nil {
				t.Fatal(err)
			}
		}, 3, true},
	}
	for _, step := range steps {
		step.act()
		if got := fired(); got != step.fired {
			t.Fatalf("%s: %d triggers, want %d", step.name, got, step.fired)
		}
		m, err := e.m.BalanceMonitor(ctx, low.ID)
		if err != nil || m.Triggered != step.inside {
			t.Fatalf("%s: monitor = %+v, %v", step.name, m, err)
		}
	}

	last := e.published(t, "balance_monitor.triggered")[2]
	if last["object"] != "balance_monitor" || last["triggered"] != true ||
		last["alert_condition"].(map[string]any)["field"] != "available_balance_amount" ||
		last["balances"].(map[string]any)["available"].(map[string]any)["amount"] != "90" {
		t.Fatalf("trigger payload = %v", last)
	}

	t.Run("crud and validation", func(t *testing.T) {
		updated, err := e.m.UpdateBalanceMonitor(ctx, low.ID, ledger.UpdateInput{Description: new("low float")})
		if err != nil || updated.Description != "low float" || updated.Version != 1 {
			t.Fatalf("update = %+v, %v", updated, err)
		}
		list, err := e.m.ListBalanceMonitors(ctx, ledger.ListBalanceMonitorsInput{AccountID: a.ID, Limit: 10})
		if err != nil || len(list) != 1 {
			t.Fatalf("list = %+v, %v", list, err)
		}
		for _, in := range []ledger.CreateBalanceMonitorInput{
			{AccountID: a.ID, Condition: ledger.AlertCondition{Field: "balance", Operator: "lt"}},
			{AccountID: a.ID, Condition: ledger.AlertCondition{Field: "posted", Operator: "below"}},
			{Condition: ledger.AlertCondition{Field: "posted", Operator: "lt"}},
		} {
			_, err := e.m.CreateBalanceMonitor(ctx, in)
			wantErr(t, err, ledger.ErrInvalid)
		}
		_, err = e.m.CreateBalanceMonitor(ctx, ledger.CreateBalanceMonitorInput{
			AccountID: uuid.New(), Condition: ledger.AlertCondition{Field: "posted", Operator: "lt"},
		})
		wantErr(t, err, ledger.ErrNotFound)
		if err := e.m.DeleteBalanceMonitor(ctx, low.ID); err != nil {
			t.Fatal(err)
		}
		e.post(t, transfer("after-delete", e.open.ID, a.ID, 500))
		e.post(t, transfer("after-delete-2", a.ID, b.ID, 550))
		if got := fired(); got != 3 {
			t.Fatalf("deleted monitor fired: %d", got)
		}
	})
	e.verify(t)
}

func TestWebhookEndToEnd(t *testing.T) {
	t.Parallel()
	e := setup(t)
	svc := events.NewService(e.pool, testdb.Logger(), events.Config{AllowInsecureURLs: true})
	var (
		mu     sync.Mutex
		bodies [][]byte
		sigs   []string
	)
	rcv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies, sigs = append(bodies, body), append(sigs, r.Header.Get(events.SignatureHeader))
		mu.Unlock()
	}))
	t.Cleanup(rcv.Close)
	ep, err := svc.CreateEndpoint(context.Background(), events.EndpointInput{URL: rcv.URL, EventTypes: []string{"transaction.*"}})
	if err != nil {
		t.Fatal(err)
	}

	a := e.funded(t, 100)
	txn := e.post(t, transfer("pay", a.ID, e.account(t, "USD", ledger.Debit).ID, 25))

	received := func() int {
		mu.Lock()
		defer mu.Unlock()
		return len(bodies)
	}
	for deadline := time.Now().Add(30 * time.Second); received() < 2; time.Sleep(20 * time.Millisecond) {
		if _, err := svc.Dispatch(context.Background()); err != nil {
			t.Fatal(err)
		}
		if _, err := svc.Deliver(context.Background()); err != nil {
			t.Fatal(err)
		}
		if time.Now().After(deadline) {
			t.Fatalf("received %d of 2 webhooks", received())
		}
	}

	mu.Lock()
	defer mu.Unlock()
	if len(bodies) != 2 {
		t.Fatalf("received %d webhooks, want the funding and the payment", len(bodies))
	}

	payment := slices.IndexFunc(bodies, func(b []byte) bool { return strings.Contains(string(b), `"amount":"25"`) })
	if payment < 0 {
		t.Fatalf("no payment webhook among %s", bodies)
	}
	var ev struct {
		Type string `json:"type"`
		Data struct {
			ID      string           `json:"id"`
			Entries []map[string]any `json:"entries"`
		} `json:"data"`
	}
	if err := json.Unmarshal(bodies[payment], &ev); err != nil {
		t.Fatal(err)
	}
	if ev.Type != "transaction.created" || ev.Data.ID != typeid.Encode("txn", txn.ID) ||
		len(ev.Data.Entries) != 2 || ev.Data.Entries[0]["amount"] != "25" {
		t.Fatalf("webhook = %s", bodies[payment])
	}
	if strings.Contains(string(bodies[payment]), "resulting_balances") {
		t.Fatal("event payload carries resulting balances")
	}
	if !events.Verify(ep.Secret, sigs[payment], bodies[payment], time.Now(), time.Minute) {
		t.Fatal("webhook signature does not verify")
	}
}

func TestHTTPBalanceMonitors(t *testing.T) {
	t.Parallel()
	a := newAPI(t)
	cash := a.account("cash", "debit", `,"allow_negative":true`)
	m := a.must(http.StatusCreated, http.MethodPost, "/v1/balance_monitors", "", fmt.Sprintf(
		`{"account_id":%q,"alert_condition":{"field":"posted_balance_amount","operator":"gte","value":"0"}}`, cash))
	id := requirePrefix(t, m["id"], "bm")
	if m["object"] != "balance_monitor" || m["triggered"] != true {
		t.Fatalf("monitor = %v", m)
	}
	a.must(http.StatusOK, http.MethodGet, "/v1/balance_monitors/"+id, "", "")
	if n := len(a.list("/v1/balance_monitors?account_id=" + cash)); n != 1 {
		t.Fatalf("monitors = %d", n)
	}
	a.must(http.StatusOK, http.MethodPatch, "/v1/balance_monitors/"+id, "", `{"description":"never negative"}`)
	a.must(http.StatusUnprocessableEntity, http.MethodPost, "/v1/balance_monitors", "", fmt.Sprintf(
		`{"account_id":%q,"alert_condition":{"field":"balance","operator":"gte","value":"0"}}`, cash))
	a.must(http.StatusUnprocessableEntity, http.MethodPatch, "/v1/balance_monitors/"+id, "", `{"name":"x"}`)
	if got := a.must(http.StatusOK, http.MethodDelete, "/v1/balance_monitors/"+id, "", ""); got["deleted"] != true {
		t.Fatalf("delete = %v", got)
	}
	a.must(http.StatusNotFound, http.MethodGet, "/v1/balance_monitors/"+id, "", "")
}
