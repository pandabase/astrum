package ledger

import (
	"errors"
	"math"
	"testing"

	"github.com/google/uuid"
	"github.com/pandabase/astrum/internal/money"
)

type fixture struct {
	state *ledgerState
	ids   map[string]uuid.UUID
}

func newFixture(t *testing.T, accounts map[string]accountState) *fixture {
	t.Helper()
	f := &fixture{ids: map[string]uuid.UUID{}}
	var list []*accountState
	for name, a := range accounts {
		a.id = uuid.New()
		if a.currency == "" {
			a.currency = "USD"
		}
		if a.normalSide == "" {
			a.normalSide = Debit
		}
		if a.status == "" {
			a.status = AccountOpen
		}
		f.ids[name] = a.id
		list = append(list, &a)
	}
	f.state = newLedgerState(list, nil)
	return f
}

func (f *fixture) posting(name string, side Side, amount int64) Posting {
	return Posting{AccountID: f.ids[name], Side: side, Amount: amt(amount)}
}

func (f *fixture) transfer(from, to string, amount int64) PostInput {
	return PostInput{IdempotencyKey: "k", Postings: []Posting{
		f.posting(from, Credit, amount),
		f.posting(to, Debit, amount),
	}}
}

func (f *fixture) available(t *testing.T, name string) int64 {
	t.Helper()
	v, err := f.state.accounts[f.ids[name]].available()
	if err != nil {
		t.Fatal(err)
	}
	n, ok := v.Int64()
	if !ok {
		t.Fatalf("available %s does not fit in int64", v)
	}
	return n
}

func TestApplyFundsChecks(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		accounts map[string]accountState
		from     string
		amount   int64
		wantErr  error
	}{
		{
			name:     "funded",
			accounts: map[string]accountState{"a": {postedDebits: amt(100)}, "b": {}},
			from:     "a", amount: 100,
		},
		{
			name:     "insufficient",
			accounts: map[string]accountState{"a": {postedDebits: amt(99)}, "b": {}},
			from:     "a", amount: 100,
			wantErr: ErrInsufficientFunds,
		},
		{
			name:     "within overdraft",
			accounts: map[string]accountState{"a": {postedDebits: amt(50), overdraftLimit: amt(50)}, "b": {}},
			from:     "a", amount: 100,
		},
		{
			name:     "beyond overdraft",
			accounts: map[string]accountState{"a": {postedDebits: amt(50), overdraftLimit: amt(49)}, "b": {}},
			from:     "a", amount: 100,
			wantErr: ErrInsufficientFunds,
		},
		{
			name:     "allow negative",
			accounts: map[string]accountState{"a": {allowNegative: true}, "b": {}},
			from:     "a", amount: math.MaxInt64,
		},
		{
			name:     "held funds are not spendable",
			accounts: map[string]accountState{"a": {postedDebits: amt(100), held: amt(60)}, "b": {}},
			from:     "a", amount: 41,
			wantErr: ErrInsufficientFunds,
		},
		{
			name:     "unheld remainder is spendable",
			accounts: map[string]accountState{"a": {postedDebits: amt(100), held: amt(60)}, "b": {}},
			from:     "a", amount: 40,
		},
		{
			name: "credit normal account",
			accounts: map[string]accountState{
				"a": {normalSide: Credit, postedCredits: amt(100)},
				"b": {normalSide: Credit},
			},
			from: "a", amount: 100,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t, tt.accounts)
			in := f.transfer(tt.from, "b", tt.amount)
			if tt.accounts[tt.from].normalSide == Credit {
				in.Postings[0].Side, in.Postings[1].Side = Debit, Credit
			}
			before := *f.state.accounts[f.ids[tt.from]]

			_, err := f.state.apply(in, nil)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("apply() error = %v, want %v", err, tt.wantErr)
			}
			if err != nil && *f.state.accounts[f.ids[tt.from]] != before {
				t.Fatal("failed apply mutated state")
			}
		})
	}
}

func TestApplyIsSequential(t *testing.T) {
	t.Parallel()
	f := newFixture(t, map[string]accountState{"a": {}, "b": {}, "open": {allowNegative: true}})

	if _, err := f.state.apply(f.transfer("a", "b", 100), nil); !errors.Is(err, ErrInsufficientFunds) {
		t.Fatalf("a->b before funding: error = %v, want ErrInsufficientFunds", err)
	}
	if _, err := f.state.apply(f.transfer("open", "a", 100), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := f.state.apply(f.transfer("a", "b", 100), nil); err != nil {
		t.Fatalf("a->b after funding: %v", err)
	}
	if got := f.available(t, "b"); got != 100 {
		t.Fatalf("b available = %d, want 100", got)
	}

	f = newFixture(t, map[string]accountState{"a": {}, "b": {}})
	if _, err := f.state.apply(f.transfer("a", "b", 10), nil); !errors.Is(err, ErrInsufficientFunds) {
		t.Fatalf("circular leg error = %v, want ErrInsufficientFunds", err)
	}
}

func TestApplyBalanceAfterAndVersions(t *testing.T) {
	t.Parallel()
	f := newFixture(t, map[string]accountState{"open": {allowNegative: true, version: 7}, "a": {}})

	postings, err := f.state.apply(PostInput{IdempotencyKey: "k", Postings: []Posting{
		f.posting("a", Debit, 30),
		f.posting("a", Debit, 20),
		f.posting("open", Credit, 50),
	}}, nil)
	if err != nil {
		t.Fatal(err)
	}

	wantAfter := []money.Amount{amt(30), amt(50), amt(-50)}
	for i, p := range postings {
		if p.balanceAfter != wantAfter[i] {
			t.Errorf("posting %d balanceAfter = %s, want %s", i, p.balanceAfter, wantAfter[i])
		}
		if p.Currency != "USD" {
			t.Errorf("posting %d currency = %q, want USD", i, p.Currency)
		}
	}
	if v := f.state.accounts[f.ids["open"]].version; v != 8 {
		t.Errorf("version = %d, want 8 (one bump per entry)", v)
	}
	if len(f.state.dirty()) != 2 {
		t.Errorf("dirty accounts = %d, want 2", len(f.state.dirty()))
	}
}

func TestApplyRejections(t *testing.T) {
	t.Parallel()
	f := newFixture(t, map[string]accountState{
		"usd":   {allowNegative: true},
		"usd2":  {allowNegative: true},
		"eur":   {allowNegative: true, currency: "EUR"},
		"other": {allowNegative: true, ledgerID: uuid.New()},
	})
	tests := []struct {
		name     string
		postings []Posting
		wantErr  error
	}{
		{"unbalanced", []Posting{f.posting("usd", Debit, 10), f.posting("usd2", Credit, 9)}, ErrUnbalanced},
		{"cross currency", []Posting{f.posting("usd", Debit, 10), f.posting("eur", Credit, 10)}, ErrUnbalanced},
		{"cross ledger", []Posting{f.posting("usd", Debit, 10), f.posting("other", Credit, 10)}, ErrCrossLedger},
		{"unknown account", []Posting{{AccountID: uuid.New(), Side: Debit, Amount: amt(1)}, f.posting("usd", Credit, 1)}, ErrNotFound},
		{"currency mismatch", []Posting{{AccountID: f.ids["usd"], Side: Debit, Amount: amt(1), Currency: "EUR"}, f.posting("usd2", Credit, 1)}, ErrInvalid},
		{"overflow", []Posting{{AccountID: f.ids["usd"], Side: Debit, Amount: money.MaxAmount()}, f.posting("usd", Debit, 1), f.posting("usd2", Credit, 1)}, money.ErrOverflow},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := f.state.apply(PostInput{IdempotencyKey: "k", Postings: tt.postings}, nil); !errors.Is(err, tt.wantErr) {
				t.Fatalf("apply() error = %v, want %v", err, tt.wantErr)
			}
			if len(f.state.dirty()) != 0 {
				t.Fatal("rejected entry left dirty state")
			}
		})
	}
}

func TestReserveAndRelease(t *testing.T) {
	t.Parallel()
	f := newFixture(t, map[string]accountState{"a": {postedDebits: amt(100)}, "b": {}})
	a := f.ids["a"]

	if err := f.state.reserve(a, "", amt(101)); !errors.Is(err, ErrInsufficientFunds) {
		t.Fatalf("over-reserve error = %v, want ErrInsufficientFunds", err)
	}
	if err := f.state.reserve(a, "EUR", amt(10)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("wrong currency reserve error = %v, want ErrInvalid", err)
	}
	if err := f.state.reserve(a, "USD", amt(70)); err != nil {
		t.Fatal(err)
	}
	if got := f.available(t, "a"); got != 30 {
		t.Fatalf("available = %d, want 30", got)
	}

	if _, err := f.state.apply(f.transfer("a", "b", 50), map[uuid.UUID]money.Amount{a: amt(70)}); err != nil {
		t.Fatalf("capture: %v", err)
	}
	if got := f.available(t, "a"); got != 50 {
		t.Fatalf("available after capture = %d, want 50", got)
	}

	if err := f.state.release(a, amt(1)); err == nil {
		t.Fatal("release beyond held should fail")
	}
}

func TestAccountBelowFloorCanBeRepaired(t *testing.T) {
	t.Parallel()

	f := newFixture(t, map[string]accountState{"a": {postedCredits: amt(500)}, "open": {allowNegative: true}})

	if _, err := f.state.apply(f.transfer("open", "a", 100), nil); err != nil {
		t.Fatalf("credit to underfunded account: %v", err)
	}
	if _, err := f.state.apply(f.transfer("a", "open", 1), nil); !errors.Is(err, ErrInsufficientFunds) {
		t.Fatalf("debit from underfunded account error = %v, want ErrInsufficientFunds", err)
	}
}

func TestNonOpenAccountsRejectMovement(t *testing.T) {
	t.Parallel()
	for _, status := range []AccountStatus{AccountFrozen, AccountClosed} {
		t.Run(string(status), func(t *testing.T) {
			f := newFixture(t, map[string]accountState{"a": {status: status, postedDebits: amt(100), held: amt(40)}, "b": {}})
			for name, try := range map[string]func() error{
				"debit": func() error {
					_, err := f.state.apply(PostInput{Postings: []Posting{f.posting("b", Debit, 1), f.posting("a", Credit, 1)}}, nil)
					return err
				},
				"credit": func() error {
					_, err := f.state.apply(PostInput{Postings: []Posting{f.posting("a", Debit, 1), f.posting("b", Credit, 1)}}, nil)
					return err
				},
				"reserve": func() error { return f.state.reserve(f.ids["a"], "", amt(1)) },
			} {
				if err := try(); !errors.Is(err, ErrAccountNotOpen) {
					t.Errorf("%s: error = %v, want ErrAccountNotOpen", name, err)
				}
			}
			if err := f.state.release(f.ids["a"], amt(40)); err != nil {
				t.Fatalf("release on %s account: %v", status, err)
			}
		})
	}
}

func amt(n int64) money.Amount { return money.NewAmount(n) }

func TestPendingTransitions(t *testing.T) {
	t.Parallel()
	f := newFixture(t, map[string]accountState{"a": {postedDebits: amt(100)}, "b": {}})
	in := f.transfer("a", "b", 70)
	in.Status = TransactionPending
	if _, err := f.state.apply(in, nil); err != nil {
		t.Fatal(err)
	}
	if got := f.available(t, "a"); got != 30 {
		t.Fatalf("available after pending = %d, want 30", got)
	}
	if _, err := f.state.apply(f.transfer("a", "b", 31), nil); !errors.Is(err, ErrInsufficientFunds) {
		t.Fatalf("spend past reservation error = %v", err)
	}

	_, posted, err := f.state.transition(balanceChange{unpend: in.Postings, add: f.transfer("a", "b", 50).Postings, status: TransactionPosted})
	if err != nil {
		t.Fatal(err)
	}
	a := f.state.accounts[f.ids["a"]]
	if !a.pendingCredits.IsZero() || a.postedCredits != amt(50) || posted[1].balanceAfter != amt(50) {
		t.Fatalf("after post: pending credits %s, posted credits %s, balance after %s", a.pendingCredits, a.postedCredits, posted[1].balanceAfter)
	}
	if got := f.available(t, "a"); got != 50 {
		t.Fatalf("available after post = %d, want 50", got)
	}

	if _, _, err := f.state.transition(balanceChange{unpend: in.Postings}); err == nil {
		t.Fatal("releasing entries that are no longer pending succeeded")
	}
}

func TestBalanceViews(t *testing.T) {
	t.Parallel()
	a := accountState{
		normalSide:     Credit,
		postedDebits:   amt(20),
		postedCredits:  amt(100),
		pendingDebits:  amt(30),
		pendingCredits: amt(5),
		held:           amt(10),
	}
	posted, pending, available, err := a.balances()
	if err != nil {
		t.Fatal(err)
	}
	for name, tt := range map[string]struct {
		got  Balance
		want [3]int64
	}{
		"posted":    {posted, [3]int64{20, 100, 80}},
		"pending":   {pending, [3]int64{50, 105, 55}},
		"available": {available, [3]int64{60, 100, 40}},
	} {
		want := Balance{Debits: amt(tt.want[0]), Credits: amt(tt.want[1]), Amount: amt(tt.want[2])}
		if tt.got != want {
			t.Errorf("%s = %+v, want %+v", name, tt.got, want)
		}
	}
	if got, _ := a.available(); got != available.Amount {
		t.Errorf("available() = %s, balances say %s", got, available.Amount)
	}
}

func TestCheckPartial(t *testing.T) {
	t.Parallel()
	a, b, c := uuid.New(), uuid.New(), uuid.New()
	pending := []Posting{
		{AccountID: a, Side: Credit, Amount: amt(100)},
		{AccountID: b, Side: Debit, Amount: amt(60)},
		{AccountID: c, Side: Debit, Amount: amt(40)},
	}
	tests := []struct {
		name    string
		posted  []Posting
		wantErr bool
	}{
		{"full", pending, false},
		{"part", []Posting{{AccountID: a, Side: Credit, Amount: amt(50)}, {AccountID: b, Side: Debit, Amount: amt(50)}}, false},
		{"over on one account", []Posting{
			{AccountID: a, Side: Credit, Amount: amt(40)}, {AccountID: a, Side: Credit, Amount: amt(40)},
			{AccountID: b, Side: Debit, Amount: amt(80)},
		}, true},
		{"too much", []Posting{{AccountID: a, Side: Credit, Amount: amt(101)}, {AccountID: b, Side: Debit, Amount: amt(101)}}, true},
		{"wrong side", []Posting{{AccountID: a, Side: Debit, Amount: amt(1)}, {AccountID: b, Side: Credit, Amount: amt(1)}}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := checkPartial(pending, tt.posted); (err != nil) != tt.wantErr {
				t.Fatalf("checkPartial() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
