package tests

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pandabase/astrum/internal/modules/ledger"
)

func feAt(key string, from, to uuid.UUID, amount int64, when time.Time) ledger.PostInput {
	in := transfer(key, from, to, amount)
	in.EffectiveAt = at(when)
	return in
}

func feAwaitSettledSnapshot(t *testing.T, e *env) {
	t.Helper()
	deadline := time.Now().Add(time.Minute)
	for {
		var settled bool
		if err := e.pool.QueryRow(context.Background(), `
			SELECT coalesce(max(posted_xid) < pg_snapshot_xmin(pg_current_snapshot()), true)
			FROM ledger_transactions WHERE status = 'posted'`).Scan(&settled); err != nil {
			t.Fatal(err)
		}
		if settled {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("posted transactions never dropped below the snapshot horizon")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func feStatement(t *testing.T, e *env, account uuid.UUID, from, until time.Time) ledger.Statement {
	t.Helper()
	feAwaitSettledSnapshot(t, e)
	st, err := e.m.CreateStatement(context.Background(), ledger.CreateStatementInput{AccountID: account, From: from, Until: until})
	if err != nil {
		t.Fatalf("CreateStatement() error = %v", err)
	}
	return st
}

func TestHistoryEdgeBalanceWindows(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	src := e.account(t, "USD", ledger.Credit, unrestricted)
	b := e.account(t, "USD", ledger.Debit, unrestricted)
	e.post(t, feAt("w-d1", src.ID, b.ID, 1, day(1)))
	e.post(t, feAt("w-d2", src.ID, b.ID, 10, day(2)))
	e.post(t, feAt("w-d2-last", src.ID, b.ID, 100, day(3).Add(-time.Microsecond)))
	e.post(t, feAt("w-d3", b.ID, src.ID, 1_000, day(3)))
	e.post(t, pending(feAt("w-pending", src.ID, b.ID, 5, day(4))))
	e.post(t, feAt("w-backdated", src.ID, b.ID, 20_000, day(1).Add(time.Hour)))

	tests := []struct {
		name            string
		r               ledger.EffectiveRange
		posted, pending int64
		err             error
	}{
		{"from equals until", ledger.EffectiveRange{From: at(day(2)), Until: at(day(2))}, 0, 0, ledger.ErrInvalid},
		{"from after until", ledger.EffectiveRange{From: at(day(3)), Until: at(day(2))}, 0, 0, ledger.ErrInvalid},
		{"until is exclusive", ledger.EffectiveRange{Until: at(day(1))}, 0, 0, nil},
		{"one microsecond past the first entry", ledger.EffectiveRange{Until: at(day(1).Add(time.Microsecond))}, 1, 1, nil},
		{"from is inclusive", ledger.EffectiveRange{From: at(day(3))}, -1_000, -995, nil},
		{"from one microsecond late", ledger.EffectiveRange{From: at(day(3).Add(time.Microsecond))}, 0, 5, nil},
		{"day 1 includes the backdated entry", ledger.EffectiveRange{From: at(day(1)), Until: at(day(2))}, 20_001, 20_001, nil},
		{"day 2 up to its last microsecond", ledger.EffectiveRange{From: at(day(2)), Until: at(day(3))}, 110, 110, nil},
		{"one microsecond window", ledger.EffectiveRange{From: at(day(3).Add(-time.Microsecond)), Until: at(day(3))}, 100, 100, nil},
		{"empty window between entries", ledger.EffectiveRange{From: at(day(2).Add(time.Microsecond)), Until: at(day(3).Add(-time.Microsecond))}, 0, 0, nil},
		{"before everything", ledger.EffectiveRange{From: at(day(1).AddDate(-1, 0, 0)), Until: at(day(1))}, 0, 0, nil},
		{"after everything", ledger.EffectiveRange{From: at(day(20))}, 0, 0, nil},
		{"everything", ledger.EffectiveRange{}, 19_111, 19_116, nil},
		{"far bounds equal everything", ledger.EffectiveRange{From: at(time.Date(1970, 1, 1, 0, 0, 0, 0, time.UTC)), Until: at(time.Date(2999, 1, 1, 0, 0, 0, 0, time.UTC))}, 19_111, 19_116, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := e.m.Balances(ctx, b.ID, tt.r)
			if !errors.Is(err, tt.err) {
				t.Fatalf("Balances() error = %v, want %v", err, tt.err)
			}
			if err != nil {
				return
			}
			if got.Posted.Amount != amt(tt.posted) || got.Pending.Amount != amt(tt.pending) || got.Available.Amount != amt(tt.posted) {
				t.Fatalf("posted %s pending %s available %s, want %d %d %d",
					got.Posted.Amount, got.Pending.Amount, got.Available.Amount, tt.posted, tt.pending, tt.posted)
			}
		})
	}

	t.Run("windows partition the whole history", func(t *testing.T) {
		cuts := []time.Time{day(1), day(2), day(3), day(4)}
		total := amt(0)
		ranges := []ledger.EffectiveRange{{Until: at(cuts[0])}}
		for i := 1; i < len(cuts); i++ {
			ranges = append(ranges, ledger.EffectiveRange{From: at(cuts[i-1]), Until: at(cuts[i])})
		}
		ranges = append(ranges, ledger.EffectiveRange{From: at(cuts[len(cuts)-1])})
		for _, r := range ranges {
			got, err := e.m.Balances(ctx, b.ID, r)
			if err != nil {
				t.Fatal(err)
			}
			total, _ = total.Add(got.Posted.Amount)
		}
		if total != e.get(t, b.ID).Posted.Amount {
			t.Fatalf("windows sum to %s, account holds %s", total, e.get(t, b.ID).Posted.Amount)
		}
	})

	t.Run("account with no entries", func(t *testing.T) {
		empty := e.account(t, "USD", ledger.Debit)
		got, err := e.m.Balances(ctx, empty.ID, ledger.EffectiveRange{From: at(day(1)), Until: at(day(2))})
		if err != nil || !got.Posted.Amount.IsZero() || !got.Pending.Amount.IsZero() || !got.Available.Amount.IsZero() {
			t.Fatalf("empty account = %+v, %v", got, err)
		}
	})
	_, err := e.m.Balances(ctx, uuid.New(), ledger.EffectiveRange{Until: at(day(1))})
	wantErr(t, err, ledger.ErrNotFound)
}

func TestHistoryEdgeSubMicrosecondBalanceWindowTruncates(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	src := e.account(t, "USD", ledger.Credit, unrestricted)
	b := e.account(t, "USD", ledger.Debit, unrestricted)
	e.post(t, feAt("sub-us", src.ID, b.ID, 7, day(5)))

	for _, tt := range []struct {
		from, until time.Time
		want        int64
	}{
		{day(5), day(5).Add(500 * time.Nanosecond), 0},
		{day(5).Add(500 * time.Nanosecond), day(5).Add(time.Microsecond + 500*time.Nanosecond), 7},
		{day(5), day(5).Add(time.Microsecond), 7},
	} {
		got, err := e.m.Balances(ctx, b.ID, ledger.EffectiveRange{From: at(tt.from), Until: at(tt.until)})
		if err != nil {
			t.Fatalf("Balances() error = %v", err)
		}
		if got.Posted.Amount != amt(tt.want) {
			t.Fatalf("posted = %s for [%s, %s), want %d", got.Posted.Amount, tt.from.Format(time.RFC3339Nano), tt.until.Format(time.RFC3339Nano), tt.want)
		}
	}
}

func TestHistoryEdgeStatementBoundaries(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	src := e.account(t, "USD", ledger.Credit, unrestricted)
	b := e.account(t, "USD", ledger.Debit, unrestricted)
	e.post(t, feAt("s-open", src.ID, b.ID, 1_000, day(1).Add(-time.Microsecond)))
	e.post(t, feAt("s-at-from", src.ID, b.ID, 1, day(1)))
	e.post(t, feAt("s-last", b.ID, src.ID, 10, day(5).Add(-time.Microsecond)))
	e.post(t, feAt("s-at-until", src.ID, b.ID, 100, day(5)))
	e.post(t, pending(feAt("s-pending", src.ID, b.ID, 3, day(2))))

	first := feStatement(t, e, b.ID, day(1), day(5))
	second := feStatement(t, e, b.ID, day(5), day(10))
	wantBalance(t, "first starting", first.Starting, 1_000, 0, 1_000)
	wantBalance(t, "first ending", first.Ending, 1_001, 10, 991)
	wantBalance(t, "second starting", second.Starting, 1_001, 10, 991)
	wantBalance(t, "second ending", second.Ending, 1_101, 10, 1_091)
	if first.EntryCount != 2 || second.EntryCount != 1 {
		t.Fatalf("entry counts = %d and %d, want 2 and 1", first.EntryCount, second.EntryCount)
	}
	if first.Ending != second.Starting {
		t.Fatalf("adjacent statements do not chain: %+v then %+v", first.Ending, second.Starting)
	}

	t.Run("empty window", func(t *testing.T) {
		st := feStatement(t, e, b.ID, day(20), day(21))
		if st.EntryCount != 0 || st.Starting != st.Ending {
			t.Fatalf("empty statement = %+v", st)
		}
		wantBalance(t, "empty ending", st.Ending, 1_101, 10, 1_091)
		entries, err := e.m.ListEntries(ctx, ledger.ListEntriesInput{StatementID: st.ID, Limit: 10})
		if err != nil || len(entries) != 0 {
			t.Fatalf("entries = %+v, %v", entries, err)
		}
	})

	t.Run("one microsecond window", func(t *testing.T) {
		st := feStatement(t, e, b.ID, day(1), day(1).Add(time.Microsecond))
		if st.EntryCount != 1 || st.Ending.Amount != amt(1_001) {
			t.Fatalf("statement = %+v", st)
		}
	})

	t.Run("account with no entries", func(t *testing.T) {
		empty := e.account(t, "USD", ledger.Debit)
		st := feStatement(t, e, empty.ID, day(1), day(2))
		wantBalance(t, "starting", st.Starting, 0, 0, 0)
		wantBalance(t, "ending", st.Ending, 0, 0, 0)
	})

	t.Run("credit normal account", func(t *testing.T) {
		st := feStatement(t, e, src.ID, day(1), day(5))
		wantBalance(t, "starting", st.Starting, 0, 1_000, 1_000)
		wantBalance(t, "ending", st.Ending, 10, 1_001, 991)
	})

	t.Run("backdated post lands in a new statement only", func(t *testing.T) {
		e.post(t, feAt("s-backdated", src.ID, b.ID, 7, day(3)))
		again, err := e.m.Statement(ctx, first.ID)
		if err != nil || again.Ending != first.Ending || again.EntryCount != 2 {
			t.Fatalf("stored statement changed: %+v, %v", again, err)
		}
		fresh := feStatement(t, e, b.ID, day(1), day(5))
		if fresh.EntryCount != 3 || fresh.Ending.Amount != amt(998) {
			t.Fatalf("fresh statement = %+v", fresh)
		}
		later := feStatement(t, e, b.ID, day(5), day(10))
		if later.Starting.Amount != amt(998) || later.EntryCount != 1 {
			t.Fatalf("following statement = %+v", later)
		}
	})

	t.Run("statement entries", func(t *testing.T) {
		list := func(in ledger.ListEntriesInput) ([]ledger.Entry, error) {
			in.StatementID = first.ID
			if in.Limit == 0 {
				in.Limit = 100
			}
			return e.m.ListEntries(ctx, in)
		}
		tests := []struct {
			name string
			in   ledger.ListEntriesInput
			want int
			err  error
		}{
			{"all", ledger.ListEntriesInput{}, 2, nil},
			{"matching account", ledger.ListEntriesInput{AccountID: b.ID}, 2, nil},
			{"other account", ledger.ListEntriesInput{AccountID: src.ID}, 0, nil},
			{"pending status", ledger.ListEntriesInput{Status: ledger.TransactionPending}, 0, nil},
			{"narrower window", ledger.ListEntriesInput{Effective: ledger.EffectiveRange{From: at(day(2))}}, 1, nil},
			{"wider window is clamped", ledger.ListEntriesInput{Effective: ledger.EffectiveRange{From: at(day(1).AddDate(-1, 0, 0)), Until: at(day(28))}}, 2, nil},
			{"disjoint window", ledger.ListEntriesInput{Effective: ledger.EffectiveRange{From: at(day(6)), Until: at(day(7))}}, 0, nil},
			{"limit one", ledger.ListEntriesInput{Limit: 1}, 1, nil},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				got, err := list(tt.in)
				if !errors.Is(err, tt.err) || len(got) != tt.want {
					t.Fatalf("entries = %d, %v; want %d, %v", len(got), err, tt.want, tt.err)
				}
			})
		}
		_, err := e.m.ListEntries(ctx, ledger.ListEntriesInput{StatementID: uuid.New(), Limit: 10})
		wantErr(t, err, ledger.ErrNotFound)
	})
	e.verify(t)
}

func TestHistoryEdgeStatementValidation(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	b := e.account(t, "USD", ledger.Debit)
	tests := []struct {
		name string
		in   ledger.CreateStatementInput
		want error
	}{
		{"from equals until", ledger.CreateStatementInput{AccountID: b.ID, From: day(2), Until: day(2)}, ledger.ErrInvalid},
		{"from after until", ledger.CreateStatementInput{AccountID: b.ID, From: day(3), Until: day(2)}, ledger.ErrInvalid},
		{"no from", ledger.CreateStatementInput{AccountID: b.ID, Until: day(2)}, ledger.ErrInvalid},
		{"no until", ledger.CreateStatementInput{AccountID: b.ID, From: day(2)}, ledger.ErrInvalid},
		{"no account", ledger.CreateStatementInput{From: day(1), Until: day(2)}, ledger.ErrInvalid},
		{"description too long", ledger.CreateStatementInput{AccountID: b.ID, From: day(1), Until: day(2), Description: strings.Repeat("d", 1025)}, ledger.ErrInvalid},
		{"description with NUL", ledger.CreateStatementInput{AccountID: b.ID, From: day(1), Until: day(2), Description: "a\x00"}, ledger.ErrInvalid},
		{"unknown account", ledger.CreateStatementInput{AccountID: uuid.New(), From: day(1), Until: day(2)}, ledger.ErrNotFound},
		{"sub-microsecond window", ledger.CreateStatementInput{AccountID: b.ID, From: day(2), Until: day(2).Add(500 * time.Nanosecond)}, ledger.ErrInvalid},
		{"description at limit", ledger.CreateStatementInput{AccountID: b.ID, From: day(1), Until: day(2), Description: strings.Repeat("d", 1024)}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := e.m.CreateStatement(ctx, tt.in)
			if !errors.Is(err, tt.want) {
				t.Fatalf("CreateStatement() error = %v, want %v", err, tt.want)
			}
		})
	}
	_, err := e.m.Statement(ctx, uuid.New())
	wantErr(t, err, ledger.ErrNotFound)
}

func TestHistoryEdgeListStatements(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	a := e.account(t, "USD", ledger.Debit)
	b := e.account(t, "USD", ledger.Debit)
	var as []ledger.Statement
	for i := range 3 {
		as = append(as, feStatement(t, e, a.ID, day(i+1), day(i+2)))
	}
	bs := feStatement(t, e, b.ID, day(1), day(2))

	ids := func(sts []ledger.Statement) string {
		out := make([]string, len(sts))
		for i, st := range sts {
			out[i] = st.ID.String()
		}
		return strings.Join(out, ",")
	}
	want := func(sts ...ledger.Statement) string { return ids(sts) }
	tests := []struct {
		name string
		in   ledger.ListStatementsInput
		want string
		err  error
	}{
		{"limit zero", ledger.ListStatementsInput{}, "", ledger.ErrInvalid},
		{"negative limit", ledger.ListStatementsInput{Limit: -1}, "", ledger.ErrInvalid},
		{"limit over max", ledger.ListStatementsInput{Limit: 1001}, "", ledger.ErrInvalid},
		{"limit at max", ledger.ListStatementsInput{Limit: 1000}, want(bs, as[2], as[1], as[0]), nil},
		{"limit one", ledger.ListStatementsInput{Limit: 1}, want(bs), nil},
		{"by account", ledger.ListStatementsInput{AccountID: a.ID, Limit: 10}, want(as[2], as[1], as[0]), nil},
		{"exact page", ledger.ListStatementsInput{AccountID: a.ID, Limit: 3}, want(as[2], as[1], as[0]), nil},
		{"after exact page", ledger.ListStatementsInput{AccountID: a.ID, Before: as[0].ID, Limit: 3}, "", nil},
		{"cursor", ledger.ListStatementsInput{Before: as[2].ID, Limit: 10}, want(as[1], as[0]), nil},
		{"unknown account", ledger.ListStatementsInput{AccountID: uuid.New(), Limit: 10}, "", nil},
		{"max cursor", ledger.ListStatementsInput{Before: uuid.Max, Limit: 10}, want(bs, as[2], as[1], as[0]), nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := e.m.ListStatements(ctx, tt.in)
			if !errors.Is(err, tt.err) {
				t.Fatalf("ListStatements() error = %v, want %v", err, tt.err)
			}
			if err == nil && ids(got) != tt.want {
				t.Fatalf("statements = %s, want %s", ids(got), tt.want)
			}
		})
	}
}

func TestHistoryEdgeAccountEntries(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	src := e.account(t, "USD", ledger.Credit, unrestricted)
	b := e.account(t, "USD", ledger.Debit, unrestricted)
	for i := range 5 {
		e.post(t, transfer(fmt.Sprint("ae-", i), src.ID, b.ID, int64(i+1)))
	}
	e.post(t, pending(transfer("ae-pending", src.ID, b.ID, 50)))

	all, err := e.m.AccountEntries(ctx, b.ID, 0, 1000)
	if err != nil || len(all) != 5 {
		t.Fatalf("entries = %d, %v", len(all), err)
	}
	for i, line := range all {
		want := int64((i + 1) * (i + 2) / 2)
		if line.BalanceAfter != amt(want) || line.Amount != amt(int64(i+1)) {
			t.Fatalf("line %d = %+v, want balance after %d", i, line, want)
		}
	}
	credit, err := e.m.AccountEntries(ctx, src.ID, 0, 1)
	if err != nil || len(credit) != 1 || credit[0].BalanceAfter != amt(1) {
		t.Fatalf("credit normal lines = %+v, %v", credit, err)
	}

	tests := []struct {
		name  string
		after int64
		limit int
		want  int
		err   error
	}{
		{"limit zero", 0, 0, 0, ledger.ErrInvalid},
		{"negative limit", 0, -1, 0, ledger.ErrInvalid},
		{"limit over max", 0, 1001, 0, ledger.ErrInvalid},
		{"negative after", -1, 10, 0, ledger.ErrInvalid},
		{"limit one", 0, 1, 1, nil},
		{"exact page", 0, 5, 5, nil},
		{"after exact page", all[4].PostingID, 5, 0, nil},
		{"middle", all[1].PostingID, 2, 2, nil},
		{"far cursor", 1 << 62, 10, 0, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := e.m.AccountEntries(ctx, b.ID, tt.after, tt.limit)
			if !errors.Is(err, tt.err) || len(got) != tt.want {
				t.Fatalf("AccountEntries() = %d, %v; want %d, %v", len(got), err, tt.want, tt.err)
			}
			if tt.name == "middle" && (got[0].PostingID != all[2].PostingID || got[1].PostingID != all[3].PostingID) {
				t.Fatalf("middle page = %+v", got)
			}
		})
	}
	_, err = e.m.AccountEntries(ctx, uuid.New(), 0, 10)
	wantErr(t, err, ledger.ErrNotFound)
}

func TestHistoryEdgeListEntriesBounds(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	src := e.account(t, "USD", ledger.Credit, unrestricted)
	b := e.account(t, "USD", ledger.Debit, unrestricted)
	for i := range 3 {
		e.post(t, transfer(fmt.Sprint("le-", i), src.ID, b.ID, 1))
	}
	filters := func(n int) map[string]string {
		m := map[string]string{}
		for i := range n {
			m[fmt.Sprint("k", i)] = "v"
		}
		return m
	}
	tests := []struct {
		name string
		in   ledger.ListEntriesInput
		want int
		err  error
	}{
		{"limit zero", ledger.ListEntriesInput{AccountID: b.ID}, 0, ledger.ErrInvalid},
		{"negative limit", ledger.ListEntriesInput{AccountID: b.ID, Limit: -1}, 0, ledger.ErrInvalid},
		{"limit over max", ledger.ListEntriesInput{AccountID: b.ID, Limit: 1001}, 0, ledger.ErrInvalid},
		{"limit at max", ledger.ListEntriesInput{AccountID: b.ID, Limit: 1000}, 3, nil},
		{"twenty metadata filters", ledger.ListEntriesInput{Metadata: filters(20), Limit: 10}, 0, nil},
		{"twenty one metadata filters", ledger.ListEntriesInput{Metadata: filters(21), Limit: 10}, 0, ledger.ErrInvalid},
		{"invalid UTF-8 filter value", ledger.ListEntriesInput{Metadata: map[string]string{"k": "\xff"}, Limit: 10}, 0, ledger.ErrInvalid},
		{"from after until", ledger.ListEntriesInput{Effective: ledger.EffectiveRange{From: at(day(2)), Until: at(day(1))}, Limit: 10}, 0, ledger.ErrInvalid},
		{"cursor past the end", ledger.ListEntriesInput{AccountID: b.ID, After: 1 << 62, Limit: 10}, 0, nil},
		{"unknown account", ledger.ListEntriesInput{AccountID: uuid.New(), Limit: 10}, 0, nil},
		{"unknown settlement", ledger.ListEntriesInput{SettlementID: uuid.New(), Limit: 10}, 0, nil},
		{"settled only", ledger.ListEntriesInput{AccountID: b.ID, Settled: new(true), Limit: 10}, 0, nil},
		{"unsettled only", ledger.ListEntriesInput{AccountID: b.ID, Settled: new(false), Limit: 10}, 3, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := e.m.ListEntries(ctx, tt.in)
			if !errors.Is(err, tt.err) || len(got) != tt.want {
				t.Fatalf("ListEntries() = %d, %v; want %d, %v", len(got), err, tt.want, tt.err)
			}
		})
	}
}
