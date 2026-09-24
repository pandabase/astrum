package tests

import (
	"context"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pandabase/astrum/internal/kernel/typeid"
	"github.com/pandabase/astrum/internal/modules/ledger"
)

func feMonitor(t *testing.T, e *env, account uuid.UUID, field, op string, value int64) ledger.BalanceMonitor {
	t.Helper()
	m, err := e.m.CreateBalanceMonitor(context.Background(), ledger.CreateBalanceMonitorInput{
		AccountID: account, Condition: ledger.AlertCondition{Field: field, Operator: op, Value: amt(value)},
	})
	if err != nil {
		t.Fatalf("CreateBalanceMonitor(%s %s %d) error = %v", field, op, value, err)
	}
	return m
}

func feFired(t *testing.T, e *env, id uuid.UUID) int {
	t.Helper()
	want := typeid.Encode("bm", id)
	n := 0
	for _, ev := range e.published(t, "balance_monitor.triggered") {
		if ev["id"] == want {
			n++
		}
	}
	return n
}

func TestMonitorsEdgeThresholds(t *testing.T) {
	t.Parallel()
	e := setup(t)
	ctx := context.Background()
	acc := e.funded(t, 100)
	if _, err := e.m.CreateHold(ctx, feHoldAt("mt-hold", acc.ID, 30, time.Now().Add(time.Hour))); err != nil {
		t.Fatal(err)
	}
	e.post(t, pending(transfer("mt-pending", e.open.ID, acc.ID, 50)))

	tests := []struct {
		field string
		op    string
		value int64
		want  bool
	}{
		{"posted", "gt", 100, false},
		{"posted", "gte", 100, true},
		{"posted", "eq", 100, true},
		{"posted", "lt", 100, false},
		{"posted", "lte", 100, true},
		{"posted", "not_eq", 100, false},
		{"posted", "gt", 99, true},
		{"posted", "lt", 101, true},
		{"posted", "eq", 101, false},
		{"posted", "not_eq", 101, true},
		{"pending", "eq", 150, true},
		{"pending", "gt", 150, false},
		{"pending", "gte", 150, true},
		{"available", "eq", 70, true},
		{"available", "lt", 70, false},
		{"available", "lte", 70, true},
		{"available", "gt", -1, true},
		{"available", "lt", 0, false},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("%s %s %d", tt.field, tt.op, tt.value), func(t *testing.T) {
			m := feMonitor(t, e, acc.ID, tt.field, tt.op, tt.value)
			if m.Triggered != tt.want {
				t.Fatalf("created Triggered = %v, want %v", m.Triggered, tt.want)
			}
			got, err := e.m.BalanceMonitor(ctx, m.ID)
			if err != nil || got.Triggered != tt.want || got.Condition != m.Condition {
				t.Fatalf("BalanceMonitor() = %+v, %v", got, err)
			}
			if feFired(t, e, m.ID) != 0 {
				t.Fatal("creating a monitor inside its condition fired an event")
			}
		})
	}
}

func TestMonitorsEdgeCrossingAtThreshold(t *testing.T) {
	t.Parallel()
	e := setup(t)
	ctx := context.Background()
	acc := e.funded(t, 100)
	sink := e.account(t, "USD", ledger.Debit)
	m := feMonitor(t, e, acc.ID, "posted", "lte", 50)
	strict := feMonitor(t, e, acc.ID, "posted", "lt", 50)

	steps := []struct {
		name          string
		act           func()
		lte, lt       int
		lteIn, ltIn   bool
		wantPostedBal int64
	}{
		{"one above", func() { e.post(t, transfer("c1", acc.ID, sink.ID, 49)) }, 0, 0, false, false, 51},
		{"exactly at", func() { e.post(t, transfer("c2", acc.ID, sink.ID, 1)) }, 1, 0, true, false, 50},
		{"one below", func() { e.post(t, transfer("c3", acc.ID, sink.ID, 1)) }, 1, 1, true, true, 49},
		{"stays below", func() { e.post(t, transfer("c4", acc.ID, sink.ID, 9)) }, 1, 1, true, true, 40},
		{"re-armed above", func() { e.post(t, transfer("c5", e.open.ID, acc.ID, 11)) }, 1, 1, false, false, 51},
		{"back exactly at", func() { e.post(t, transfer("c6", acc.ID, sink.ID, 1)) }, 2, 1, true, false, 50},
		{"straight through", func() {
			e.post(t, transfer("c7", e.open.ID, acc.ID, 50))
			e.post(t, transfer("c8", acc.ID, sink.ID, 100))
		}, 3, 2, true, true, 0},
	}
	for _, step := range steps {
		step.act()
		if bal := e.balance(t, acc.ID); bal != step.wantPostedBal {
			t.Fatalf("%s: balance %d, want %d", step.name, bal, step.wantPostedBal)
		}
		if got := feFired(t, e, m.ID); got != step.lte {
			t.Fatalf("%s: lte fired %d, want %d", step.name, got, step.lte)
		}
		if got := feFired(t, e, strict.ID); got != step.lt {
			t.Fatalf("%s: lt fired %d, want %d", step.name, got, step.lt)
		}
		for id, want := range map[uuid.UUID]bool{m.ID: step.lteIn, strict.ID: step.ltIn} {
			if got, err := e.m.BalanceMonitor(ctx, id); err != nil || got.Triggered != want {
				t.Fatalf("%s: monitor %s = %+v, %v; want triggered %v", step.name, id, got, err, want)
			}
		}
	}
	e.verify(t)
}

func TestMonitorsEdgeHoldReleases(t *testing.T) {
	t.Parallel()
	e := setup(t)
	ctx := context.Background()
	acc := e.funded(t, 100)
	merchant := e.account(t, "USD", ledger.Debit)
	full := feMonitor(t, e, acc.ID, "available", "gte", 100)
	if !full.Triggered {
		t.Fatal("monitor should start inside")
	}

	h1 := feHold(t, e, feHoldAt("mh-1", acc.ID, 10, time.Now().Add(time.Hour)))
	if feFired(t, e, full.ID) != 0 {
		t.Fatal("leaving the condition fired")
	}
	if _, err := e.m.VoidHold(ctx, h1.ID); err != nil {
		t.Fatal(err)
	}
	if got := feFired(t, e, full.ID); got != 1 {
		t.Fatalf("void fired %d, want 1", got)
	}
	feHold(t, e, feHoldAt("mh-2", acc.ID, 10, time.Now().Add(-time.Minute)))
	if n, err := e.m.ExpireHolds(ctx); err != nil || n != 1 {
		t.Fatalf("ExpireHolds() = %d, %v", n, err)
	}
	if got := feFired(t, e, full.ID); got != 2 {
		t.Fatalf("expiry fired %d, want 2", got)
	}

	low := feMonitor(t, e, acc.ID, "posted", "lt", 100)
	h3 := feHold(t, e, feHoldAt("mh-3", acc.ID, 40, time.Now().Add(time.Hour)))
	if feFired(t, e, low.ID) != 0 {
		t.Fatal("a hold changed the posted balance monitor")
	}
	if _, err := e.m.CaptureHold(ctx, h3.ID, ledger.CaptureInput{IdempotencyKey: "mh-cap", Destination: merchant.ID, Amount: amt(1)}); err != nil {
		t.Fatal(err)
	}
	if got := feFired(t, e, low.ID); got != 1 {
		t.Fatalf("capture fired %d, want 1", got)
	}
	if got := feFired(t, e, full.ID); got != 2 {
		t.Fatalf("capture below 100 fired the gte monitor: %d", got)
	}
	e.verify(t)
}

func TestMonitorsEdgeMany(t *testing.T) {
	t.Parallel()
	e := setup(t)
	a := e.funded(t, 100)
	b := e.account(t, "USD", ledger.Debit)
	onA1 := feMonitor(t, e, a.ID, "posted", "lte", 60)
	onA2 := feMonitor(t, e, a.ID, "available", "eq", 50)
	onB := feMonitor(t, e, b.ID, "posted", "gte", 50)
	untouched := feMonitor(t, e, b.ID, "posted", "gt", 1_000)

	e.post(t, transfer("many", a.ID, b.ID, 50))
	for id, want := range map[uuid.UUID]int{onA1.ID: 1, onA2.ID: 1, onB.ID: 1, untouched.ID: 0} {
		if got := feFired(t, e, id); got != want {
			t.Fatalf("monitor %s fired %d, want %d", id, got, want)
		}
	}
	e.post(t, transfer("many-2", a.ID, b.ID, 1))
	for id, want := range map[uuid.UUID]int{onA1.ID: 1, onA2.ID: 1, onB.ID: 1} {
		if got := feFired(t, e, id); got != want {
			t.Fatalf("after staying inside: monitor %s fired %d, want %d", id, got, want)
		}
	}
}

func TestMonitorsEdgeAccountStatus(t *testing.T) {
	t.Parallel()
	e := setup(t)
	ctx := context.Background()
	closed := e.account(t, "USD", ledger.Debit)
	if _, err := e.m.CloseAccount(ctx, closed.ID); err != nil {
		t.Fatal(err)
	}
	onClosed := feMonitor(t, e, closed.ID, "posted", "eq", 0)
	if !onClosed.Triggered {
		t.Fatalf("monitor on a closed empty account = %+v", onClosed)
	}
	list, err := e.m.ListBalanceMonitors(ctx, ledger.ListBalanceMonitorsInput{AccountID: closed.ID, Limit: 10})
	if err != nil || len(list) != 1 || !list[0].Triggered {
		t.Fatalf("list = %+v, %v", list, err)
	}

	frozen := e.funded(t, 100)
	onFrozen := feMonitor(t, e, frozen.ID, "available", "lt", 100)
	h := feHold(t, e, feHoldAt("mf", frozen.ID, 5, time.Now().Add(time.Hour)))
	if feFired(t, e, onFrozen.ID) != 1 {
		t.Fatal("hold did not fire")
	}
	if _, err := e.m.FreezeAccount(ctx, frozen.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.VoidHold(ctx, h.ID); err != nil {
		t.Fatal(err)
	}
	if got, err := e.m.BalanceMonitor(ctx, onFrozen.ID); err != nil || got.Triggered {
		t.Fatalf("after void on a frozen account = %+v, %v", got, err)
	}
	_, err = e.m.CreateHold(ctx, feHoldAt("mf-rejected", frozen.ID, 5, time.Now().Add(time.Hour)))
	wantErr(t, err, ledger.ErrAccountNotOpen)
	if got := feFired(t, e, onFrozen.ID); got != 1 {
		t.Fatalf("fired %d, want 1", got)
	}
}

func TestMonitorsEdgeValidationAndUpdate(t *testing.T) {
	t.Parallel()
	e := setup(t)
	ctx := context.Background()
	acc := e.funded(t, 10)

	for name, tt := range map[string]struct {
		in   ledger.CreateBalanceMonitorInput
		want error
	}{
		"api field name":       {ledger.CreateBalanceMonitorInput{AccountID: acc.ID, Condition: ledger.AlertCondition{Field: "posted_balance_amount", Operator: "lt"}}, ledger.ErrInvalid},
		"empty field":          {ledger.CreateBalanceMonitorInput{AccountID: acc.ID, Condition: ledger.AlertCondition{Operator: "lt"}}, ledger.ErrInvalid},
		"empty operator":       {ledger.CreateBalanceMonitorInput{AccountID: acc.ID, Condition: ledger.AlertCondition{Field: "posted"}}, ledger.ErrInvalid},
		"uppercase operator":   {ledger.CreateBalanceMonitorInput{AccountID: acc.ID, Condition: ledger.AlertCondition{Field: "posted", Operator: "LT"}}, ledger.ErrInvalid},
		"description too long": {ledger.CreateBalanceMonitorInput{AccountID: acc.ID, Condition: ledger.AlertCondition{Field: "posted", Operator: "lt"}, Description: strings.Repeat("d", 1025)}, ledger.ErrInvalid},
		"metadata array":       {ledger.CreateBalanceMonitorInput{AccountID: acc.ID, Condition: ledger.AlertCondition{Field: "posted", Operator: "lt"}, Metadata: jsontext.Value(`[]`)}, ledger.ErrInvalid},
		"negative threshold":   {ledger.CreateBalanceMonitorInput{AccountID: acc.ID, Condition: ledger.AlertCondition{Field: "posted", Operator: "lt", Value: amt(-5)}}, nil},
		"zero threshold":       {ledger.CreateBalanceMonitorInput{AccountID: acc.ID, Condition: ledger.AlertCondition{Field: "pending", Operator: "not_eq"}}, nil},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := e.m.CreateBalanceMonitor(ctx, tt.in)
			wantErr(t, err, tt.want)
		})
	}

	m, err := e.m.CreateBalanceMonitor(ctx, ledger.CreateBalanceMonitorInput{
		AccountID: acc.ID, Condition: ledger.AlertCondition{Field: "posted", Operator: "gt", Value: amt(5)},
		Description: "d", Metadata: jsontext.Value(`{"team":{"name":"ops","pager":"x"}}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	steps := []struct {
		name     string
		in       ledger.UpdateInput
		version  int64
		desc     string
		metadata string
		err      error
	}{
		{"no-op", ledger.UpdateInput{}, 0, "d", `{"team":{"name":"ops","pager":"x"}}`, nil},
		{"same description", ledger.UpdateInput{Description: new("d")}, 0, "d", `{"team":{"name":"ops","pager":"x"}}`, nil},
		{"name is refused", ledger.UpdateInput{Name: new("x")}, 0, "d", `{"team":{"name":"ops","pager":"x"}}`, ledger.ErrInvalid},
		{"empty name is refused", ledger.UpdateInput{Name: new("")}, 0, "d", `{"team":{"name":"ops","pager":"x"}}`, ledger.ErrInvalid},
		{"nested delete", ledger.UpdateInput{Metadata: jsontext.Value(`{"team":{"pager":null}}`)}, 1, "d", `{"team":{"name":"ops"}}`, nil},
		{"clear description", ledger.UpdateInput{Description: new("")}, 2, "", `{"team":{"name":"ops"}}`, nil},
		{"description too long", ledger.UpdateInput{Description: new(strings.Repeat("d", 1025))}, 2, "", `{"team":{"name":"ops"}}`, ledger.ErrInvalid},
		{"null resets metadata", ledger.UpdateInput{Metadata: jsontext.Value(`null`)}, 3, "", `{}`, nil},
	}
	for _, step := range steps {
		got, err := e.m.UpdateBalanceMonitor(ctx, m.ID, step.in)
		if !errors.Is(err, step.err) {
			t.Fatalf("%s: error = %v, want %v", step.name, err, step.err)
		}
		if err != nil {
			if got, err = e.m.BalanceMonitor(ctx, m.ID); err != nil {
				t.Fatal(err)
			}
		}
		if got.Version != step.version || got.Description != step.desc || !jsonSame(t, got.Metadata, step.metadata) || !got.Triggered {
			t.Fatalf("%s: monitor = %+v", step.name, got)
		}
	}

	if err := e.m.DeleteBalanceMonitor(ctx, m.ID); err != nil {
		t.Fatal(err)
	}
	for name, act := range map[string]func() error{
		"delete twice": func() error { return e.m.DeleteBalanceMonitor(ctx, m.ID) },
		"get":          func() error { _, err := e.m.BalanceMonitor(ctx, m.ID); return err },
		"update": func() error {
			_, err := e.m.UpdateBalanceMonitor(ctx, m.ID, ledger.UpdateInput{Description: new("x")})
			return err
		},
	} {
		t.Run(name+" after delete", func(t *testing.T) { wantErr(t, act(), ledger.ErrNotFound) })
	}
}

func TestMonitorsEdgeList(t *testing.T) {
	t.Parallel()
	e := setup(t)
	ctx := context.Background()
	a := e.funded(t, 10)
	b := e.funded(t, 10)
	var ms []ledger.BalanceMonitor
	for i := range 3 {
		ms = append(ms, feMonitor(t, e, a.ID, "posted", "gt", int64(i)))
	}
	mb := feMonitor(t, e, b.ID, "posted", "gt", 100)

	ids := func(list []ledger.BalanceMonitor) string {
		out := make([]string, len(list))
		for i, m := range list {
			out[i] = m.ID.String()
		}
		return strings.Join(out, ",")
	}
	want := func(list ...ledger.BalanceMonitor) string { return ids(list) }
	tests := []struct {
		name string
		in   ledger.ListBalanceMonitorsInput
		want string
		err  error
	}{
		{"limit zero", ledger.ListBalanceMonitorsInput{}, "", ledger.ErrInvalid},
		{"negative limit", ledger.ListBalanceMonitorsInput{Limit: -1}, "", ledger.ErrInvalid},
		{"limit over max", ledger.ListBalanceMonitorsInput{Limit: 1001}, "", ledger.ErrInvalid},
		{"limit at max", ledger.ListBalanceMonitorsInput{Limit: 1000}, want(mb, ms[2], ms[1], ms[0]), nil},
		{"limit one", ledger.ListBalanceMonitorsInput{Limit: 1}, want(mb), nil},
		{"by account", ledger.ListBalanceMonitorsInput{AccountID: a.ID, Limit: 10}, want(ms[2], ms[1], ms[0]), nil},
		{"exact page", ledger.ListBalanceMonitorsInput{AccountID: a.ID, Limit: 3}, want(ms[2], ms[1], ms[0]), nil},
		{"after exact page", ledger.ListBalanceMonitorsInput{AccountID: a.ID, Before: ms[0].ID, Limit: 3}, "", nil},
		{"cursor", ledger.ListBalanceMonitorsInput{Before: ms[2].ID, Limit: 1}, want(ms[1]), nil},
		{"unknown account", ledger.ListBalanceMonitorsInput{AccountID: uuid.New(), Limit: 10}, "", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := e.m.ListBalanceMonitors(ctx, tt.in)
			if !errors.Is(err, tt.err) {
				t.Fatalf("ListBalanceMonitors() error = %v, want %v", err, tt.err)
			}
			if err == nil && ids(got) != tt.want {
				t.Fatalf("monitors = %s, want %s", ids(got), tt.want)
			}
		})
	}
	list, _ := e.m.ListBalanceMonitors(ctx, ledger.ListBalanceMonitorsInput{AccountID: a.ID, Limit: 10})
	for _, m := range list {
		if !m.Triggered {
			t.Fatalf("monitor %+v should be inside for a balance of 10", m)
		}
	}
}
