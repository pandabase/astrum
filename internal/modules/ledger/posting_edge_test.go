package ledger

import (
	"errors"
	"fmt"
	"maps"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pandabase/astrum/internal/money"
)

func peResolve(state *ledgerState, in PostInput) (o outcome, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("resolveEntry panicked: %v", r)
		}
	}()
	o = resolveEntry(&entry{in: in}, 0, state, map[string]Transaction{}, map[externalKey]string{}, map[string]int{}, make([]outcome, 1), time.Now())
	return o, nil
}

func TestPostingEdgeLockVersionDoesNotMaskValidation(t *testing.T) {
	stale := int64(1 << 40)
	tests := []struct {
		name  string
		other accountState
		leg   func(f *fixture) Posting
		want  error
	}{
		{"unknown account", accountState{}, func(*fixture) Posting { return Posting{AccountID: uuid.New(), Side: Debit, Amount: amt(5)} }, ErrNotFound},
		{"declared currency mismatch", accountState{}, func(f *fixture) Posting {
			return Posting{AccountID: f.ids["b"], Side: Debit, Amount: amt(5), Currency: "EUR"}
		}, ErrInvalid},
		{"frozen account", accountState{status: AccountFrozen}, func(f *fixture) Posting { return f.posting("b", Debit, 5) }, ErrAccountNotOpen},
		{"cross ledger", accountState{ledgerID: uuid.New()}, func(f *fixture) Posting { return f.posting("b", Debit, 5) }, ErrCrossLedger},
		{"unbalanced", accountState{}, func(f *fixture) Posting { return f.posting("b", Debit, 6) }, ErrUnbalanced},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t, map[string]accountState{"a": {postedDebits: amt(100)}, "b": tt.other})
			first := f.posting("a", Credit, 5)
			first.LockVersion = &stale
			in := PostInput{IdempotencyKey: "k", ArchiveOnLockFailure: true, Postings: []Posting{first, tt.leg(f)}}
			o, err := peResolve(f.state, in)
			if err != nil {
				t.Fatal(err)
			}
			if !errors.Is(o.err, tt.want) {
				t.Fatalf("outcome = status %q err %v, want %v", o.txn.Status, o.err, tt.want)
			}
		})
	}
}

func TestPostingEdgeFundsBoundaries(t *testing.T) {
	tests := []struct {
		name    string
		account accountState
		spend   int64
		ok      bool
	}{
		{"debit normal held and pending at exact availability", accountState{postedDebits: amt(100), held: amt(30), pendingCredits: amt(20)}, 50, true},
		{"debit normal held and pending one past", accountState{postedDebits: amt(100), held: amt(30), pendingCredits: amt(20)}, 51, false},
		{"debit normal with limit at exact floor", accountState{postedDebits: amt(100), held: amt(30), pendingCredits: amt(20), overdraftLimit: amt(10)}, 60, true},
		{"debit normal with limit one past", accountState{postedDebits: amt(100), held: amt(30), pendingCredits: amt(20), overdraftLimit: amt(10)}, 61, false},
		{"pending inflow does not help", accountState{postedDebits: amt(10), pendingDebits: amt(1_000)}, 11, false},
		{"credit normal held and pending at exact availability", accountState{normalSide: Credit, postedCredits: amt(100), held: amt(30), pendingDebits: amt(20)}, 50, true},
		{"credit normal held and pending one past", accountState{normalSide: Credit, postedCredits: amt(100), held: amt(30), pendingDebits: amt(20)}, 51, false},
		{"credit normal pending inflow does not help", accountState{normalSide: Credit, postedCredits: amt(10), pendingCredits: amt(1_000)}, 11, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t, map[string]accountState{"a": tt.account, "sink": {allowNegative: true}})
			out := tt.account.normalSide
			if out == "" {
				out = Debit
			}
			in := PostInput{Postings: []Posting{f.posting("a", out.opposite(), tt.spend), f.posting("sink", out, tt.spend)}}
			_, err := f.state.apply(in, nil)
			if tt.ok && err != nil {
				t.Fatalf("apply() error = %v", err)
			}
			if !tt.ok && !errors.Is(err, ErrInsufficientFunds) {
				t.Fatalf("apply() error = %v, want ErrInsufficientFunds", err)
			}
		})
	}

	t.Run("over-held account may still receive", func(t *testing.T) {
		f := newFixture(t, map[string]accountState{"a": {postedDebits: amt(100), held: amt(150)}, "src": {allowNegative: true}})
		if _, err := f.state.apply(f.transfer("src", "a", 10), nil); err != nil {
			t.Fatalf("inflow to over-held account: %v", err)
		}
		if got := f.available(t, "a"); got != -40 {
			t.Fatalf("available = %d, want -40", got)
		}
		if _, err := f.state.apply(f.transfer("a", "src", 1), nil); !errors.Is(err, ErrInsufficientFunds) {
			t.Fatalf("outflow error = %v, want ErrInsufficientFunds", err)
		}
	})
}

func TestPostingEdgeRejectedTransitionIsAtomic(t *testing.T) {
	f := newFixture(t, map[string]accountState{
		"a":    {postedDebits: amt(100), pendingCredits: amt(10), version: 3},
		"b":    {version: 5},
		"c":    {version: 9},
		"open": {allowNegative: true, normalSide: Credit},
	})
	snapshot := func() map[uuid.UUID]accountState {
		out := make(map[uuid.UUID]accountState)
		for id, a := range f.state.accounts {
			out[id] = *a
		}
		return out
	}
	before := snapshot()
	one := amt(1)

	failing := map[string]change{
		"lock on the last leg": {status: TransactionPosted, add: []Posting{
			f.posting("b", Debit, 20), f.posting("a", Credit, 20),
			{AccountID: f.ids["c"], Side: Debit, Amount: amt(1), PostedBalance: &BalanceCondition{GT: &one}},
			f.posting("open", Credit, 1),
		}},
		"unpend more than pending": {status: TransactionPosted,
			unpend: []Posting{f.posting("a", Credit, 11)},
			add:    []Posting{f.posting("b", Debit, 11), f.posting("a", Credit, 11)}},
		"release more than held": {status: TransactionPosted,
			add:      []Posting{f.posting("b", Debit, 1), f.posting("a", Credit, 1)},
			releases: map[uuid.UUID]money.Amount{f.ids["a"]: amt(1)}},
		"funds on a later leg": {status: TransactionPosted, add: []Posting{
			f.posting("b", Debit, 5), f.posting("open", Credit, 5),
			f.posting("c", Debit, 91), f.posting("a", Credit, 91),
		}},
	}
	for name, c := range failing {
		t.Run(name, func(t *testing.T) {
			if _, _, err := f.state.transition(c); err == nil {
				t.Fatal("transition succeeded")
			}
			if got := snapshot(); !maps.Equal(got, before) {
				t.Fatalf("rejected transition changed state")
			}
			if len(f.state.dirty()) != 0 {
				t.Fatal("rejected transition left dirty accounts")
			}
		})
	}
}
