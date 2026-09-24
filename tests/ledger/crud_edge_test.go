package tests

import (
	"context"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pandabase/astrum/internal/modules/ledger"
	"github.com/pandabase/astrum/internal/money"
)

func feAccountInput(e *env, code string) ledger.CreateAccountInput {
	return ledger.CreateAccountInput{LedgerID: e.ledger.ID, Code: code, Name: "n", Currency: "USD", NormalSide: ledger.Debit}
}

func TestCrudEdgeCurrencies(t *testing.T) {
	t.Parallel()
	e := setup(t)
	ctx := context.Background()

	tests := []struct {
		code     money.Currency
		exponent int
		want     error
	}{
		{"ZEROX", 0, nil},
		{"MAXEXP", 30, nil},
		{"OVEREXP", 31, ledger.ErrInvalid},
		{"NEGEXP", -1, ledger.ErrInvalid},
		{"ABC", 2, nil},
		{"ABCDEFGHIJKLMNOP", 2, nil},
		{"A_1", 2, nil},
		{"A__", 2, nil},
		{"AB", 2, ledger.ErrInvalid},
		{"1AB", 2, ledger.ErrInvalid},
		{"_AB", 2, ledger.ErrInvalid},
		{"ABc", 2, ledger.ErrInvalid},
		{"AB-", 2, ledger.ErrInvalid},
		{"AB ", 2, ledger.ErrInvalid},
		{"ÄBC", 2, ledger.ErrInvalid},
		{"", 2, ledger.ErrInvalid},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("%q exponent %d", tt.code, tt.exponent), func(t *testing.T) {
			c, err := e.m.CreateCurrency(ctx, ledger.CreateCurrencyInput{Code: tt.code, Exponent: tt.exponent})
			if !errors.Is(err, tt.want) {
				t.Fatalf("CreateCurrency() error = %v, want %v", err, tt.want)
			}
			if err != nil {
				if _, err := e.m.Currency(ctx, tt.code); !errors.Is(err, ledger.ErrNotFound) {
					t.Fatalf("rejected currency is readable: %v", err)
				}
				return
			}
			if c.Code != tt.code || c.Exponent != tt.exponent {
				t.Fatalf("currency = %+v", c)
			}
			again, err := e.m.CreateCurrency(ctx, ledger.CreateCurrencyInput{Code: tt.code, Exponent: tt.exponent})
			if err != nil || !again.CreatedAt.Equal(c.CreatedAt) {
				t.Fatalf("replay = %+v, %v", again, err)
			}
			_, err = e.m.CreateCurrency(ctx, ledger.CreateCurrencyInput{Code: tt.code, Exponent: (tt.exponent + 1) % 31})
			wantErr(t, err, ledger.ErrCurrencyExists)
		})
	}

	t.Run("accounts carry the exponent", func(t *testing.T) {
		for code, exp := range map[money.Currency]int{"ZEROX": 0, "MAXEXP": 30} {
			acc := e.account(t, code, ledger.Debit)
			if acc.CurrencyExponent != exp {
				t.Fatalf("%s account exponent = %d, want %d", code, acc.CurrencyExponent, exp)
			}
		}
	})

	t.Run("list bounds", func(t *testing.T) {
		for _, limit := range []int{0, -1, 1001} {
			_, err := e.m.ListCurrencies(ctx, "", limit)
			wantErr(t, err, ledger.ErrInvalid)
		}
		all, err := e.m.ListCurrencies(ctx, "", 1000)
		if err != nil || len(all) < 160 || len(all) >= 1000 {
			t.Fatalf("all currencies = %d, %v", len(all), err)
		}
		last := all[len(all)-1].Code
		if tail, err := e.m.ListCurrencies(ctx, last, 10); err != nil || len(tail) != 0 {
			t.Fatalf("after the last = %+v, %v", tail, err)
		}
		if one, err := e.m.ListCurrencies(ctx, "", 1); err != nil || len(one) != 1 || one[0].Code != all[0].Code {
			t.Fatalf("first = %+v, %v", one, err)
		}
		exact, err := e.m.ListCurrencies(ctx, all[len(all)-4].Code, 3)
		if err != nil || len(exact) != 3 || exact[2].Code != last {
			t.Fatalf("exact page = %+v, %v", exact, err)
		}
		if lower, err := e.m.ListCurrencies(ctx, "zzz", 10); err != nil || len(lower) != 0 {
			t.Fatalf("after a lowercase cursor = %+v, %v", lower, err)
		}
	})
}

func TestCrudEdgeAccountValidation(t *testing.T) {
	t.Parallel()
	e := setup(t)
	ctx := context.Background()
	tests := []struct {
		name   string
		mutate func(*ledger.CreateAccountInput)
		want   error
	}{
		{"code at 128", func(in *ledger.CreateAccountInput) { in.Code = strings.Repeat("c", 128) }, nil},
		{"code at 129", func(in *ledger.CreateAccountInput) { in.Code = strings.Repeat("c", 129) }, ledger.ErrInvalid},
		{"multibyte code at 126 bytes", func(in *ledger.CreateAccountInput) { in.Code = strings.Repeat("€", 42) }, nil},
		{"multibyte code at 129 bytes", func(in *ledger.CreateAccountInput) { in.Code = strings.Repeat("€", 43) }, ledger.ErrInvalid},
		{"empty code", func(in *ledger.CreateAccountInput) { in.Code = "" }, ledger.ErrInvalid},
		{"blank code", func(in *ledger.CreateAccountInput) { in.Code = " \t" }, ledger.ErrInvalid},
		{"code with NUL", func(in *ledger.CreateAccountInput) { in.Code = "a\x00b" }, ledger.ErrInvalid},
		{"code with invalid UTF-8", func(in *ledger.CreateAccountInput) { in.Code = "a\xffb" }, ledger.ErrInvalid},
		{"code with spaces and symbols", func(in *ledger.CreateAccountInput) { in.Code = " assets:cash/eu #1 " }, nil},
		{"name at 255", func(in *ledger.CreateAccountInput) { in.Name = strings.Repeat("n", 255) }, nil},
		{"name at 256", func(in *ledger.CreateAccountInput) { in.Name = strings.Repeat("n", 256) }, ledger.ErrInvalid},
		{"empty name is allowed", func(in *ledger.CreateAccountInput) { in.Name = "" }, nil},
		{"no ledger", func(in *ledger.CreateAccountInput) { in.LedgerID = uuid.Nil }, ledger.ErrInvalid},
		{"unknown ledger", func(in *ledger.CreateAccountInput) { in.LedgerID = uuid.New() }, ledger.ErrUnknownLedger},
		{"no side", func(in *ledger.CreateAccountInput) { in.NormalSide = "" }, ledger.ErrInvalid},
		{"lowercase currency", func(in *ledger.CreateAccountInput) { in.Currency = "usd" }, ledger.ErrInvalid},
		{"unregistered currency", func(in *ledger.CreateAccountInput) { in.Currency = "QQQ" }, ledger.ErrUnknownCurrency},
		{"negative overdraft", func(in *ledger.CreateAccountInput) { in.OverdraftLimit = amt(-1) }, ledger.ErrInvalid},
		{"metadata array", func(in *ledger.CreateAccountInput) { in.Metadata = jsontext.Value(`[]`) }, ledger.ErrInvalid},
		{"metadata null", func(in *ledger.CreateAccountInput) { in.Metadata = jsontext.Value(`null`) }, ledger.ErrInvalid},
		{"metadata malformed", func(in *ledger.CreateAccountInput) { in.Metadata = jsontext.Value(`{"a"`) }, ledger.ErrInvalid},
		{"metadata with escaped NUL", func(in *ledger.CreateAccountInput) { in.Metadata = jsontext.Value(`{"a":"\u0000"}`) }, ledger.ErrInvalid},
		{"metadata over 16KiB", func(in *ledger.CreateAccountInput) {
			in.Metadata = jsontext.Value(`{"a":"` + strings.Repeat("x", 16<<10) + `"}`)
		}, ledger.ErrInvalid},
		{"whitespace metadata is empty", func(in *ledger.CreateAccountInput) { in.Metadata = jsontext.Value("  ") }, nil},
		{"description at 1025", func(in *ledger.CreateAccountInput) { in.Description = strings.Repeat("d", 1025) }, ledger.ErrInvalid},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := feAccountInput(e, "code:"+uuid.NewString())
			tt.mutate(&in)
			acc, err := e.m.CreateAccount(ctx, in)
			if !errors.Is(err, tt.want) {
				t.Fatalf("CreateAccount() error = %v, want %v", err, tt.want)
			}
			if err == nil && (acc.Code != in.Code || acc.Version != 0 || acc.Status != ledger.AccountOpen || acc.StatusChangedAt != nil) {
				t.Fatalf("account = %+v", acc)
			}
		})
	}
}

func TestCrudEdgeDuplicateCodes(t *testing.T) {
	t.Parallel()
	e := setup(t)
	ctx := context.Background()
	base := feAccountInput(e, "dup")
	base.Description = "original"
	base.Metadata = jsontext.Value(`{"v":1}`)
	base.OverdraftLimit = amt(10)
	orig, err := e.m.CreateAccount(ctx, base)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name   string
		mutate func(*ledger.CreateAccountInput)
		want   error
	}{
		{"identical", func(in *ledger.CreateAccountInput) {}, nil},
		{"different name", func(in *ledger.CreateAccountInput) { in.Name = "other" }, nil},
		{"different description", func(in *ledger.CreateAccountInput) { in.Description = "other" }, nil},
		{"different metadata", func(in *ledger.CreateAccountInput) { in.Metadata = jsontext.Value(`{"v":2}`) }, nil},
		{"different currency", func(in *ledger.CreateAccountInput) { in.Currency = "EUR" }, ledger.ErrAccountExists},
		{"different side", func(in *ledger.CreateAccountInput) { in.NormalSide = ledger.Credit }, ledger.ErrAccountExists},
		{"different allow negative", func(in *ledger.CreateAccountInput) { in.AllowNegative = true }, ledger.ErrAccountExists},
		{"different overdraft", func(in *ledger.CreateAccountInput) { in.OverdraftLimit = amt(11) }, ledger.ErrAccountExists},
		{"code differs by case", func(in *ledger.CreateAccountInput) { in.Code = "DUP"; in.Currency = "EUR" }, nil},
		{"code differs by trailing space", func(in *ledger.CreateAccountInput) { in.Code = "dup "; in.Currency = "EUR" }, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := base
			tt.mutate(&in)
			acc, err := e.m.CreateAccount(ctx, in)
			if !errors.Is(err, tt.want) {
				t.Fatalf("CreateAccount() error = %v, want %v", err, tt.want)
			}
			if err != nil {
				return
			}
			if in.Code == base.Code {
				if acc.ID != orig.ID || acc.Name != "n" || acc.Description != "original" || !jsonSame(t, acc.Metadata, `{"v":1}`) {
					t.Fatalf("replay returned %+v, want the original untouched", acc)
				}
			} else if acc.ID == orig.ID {
				t.Fatal("a distinct code resolved to the original account")
			}
		})
	}

	t.Run("concurrent creates", func(t *testing.T) {
		in := feAccountInput(e, "race-code")
		var (
			wg  sync.WaitGroup
			mu  sync.Mutex
			ids = map[uuid.UUID]bool{}
		)
		for range 10 {
			wg.Go(func() {
				acc, err := e.m.CreateAccount(ctx, in)
				if err != nil {
					t.Error(err)
					return
				}
				mu.Lock()
				ids[acc.ID] = true
				mu.Unlock()
			})
		}
		wg.Wait()
		if len(ids) != 1 {
			t.Fatalf("concurrent creates produced %d accounts", len(ids))
		}
	})
}

func TestCrudEdgeAccountStatus(t *testing.T) {
	t.Parallel()
	e := setup(t)
	ctx := context.Background()

	t.Run("freeze and unfreeze are idempotent", func(t *testing.T) {
		acc := e.funded(t, 5)
		steps := []struct {
			name    string
			act     func(context.Context, uuid.UUID) (ledger.Account, error)
			status  ledger.AccountStatus
			version int64
		}{
			{"unfreeze an open account", e.m.UnfreezeAccount, ledger.AccountOpen, 1},
			{"freeze", e.m.FreezeAccount, ledger.AccountFrozen, 2},
			{"freeze again", e.m.FreezeAccount, ledger.AccountFrozen, 2},
			{"unfreeze", e.m.UnfreezeAccount, ledger.AccountOpen, 3},
			{"unfreeze again", e.m.UnfreezeAccount, ledger.AccountOpen, 3},
		}
		var changedAt *time.Time
		for _, step := range steps {
			got, err := step.act(ctx, acc.ID)
			if err != nil || got.Status != step.status || got.Version != step.version {
				t.Fatalf("%s = status %s version %d, %v; want %s %d", step.name, got.Status, got.Version, err, step.status, step.version)
			}
			if step.version > 1 && got.StatusChangedAt == nil {
				t.Fatalf("%s: status_changed_at not set", step.name)
			}
			if changedAt != nil && step.name == "freeze again" && !got.StatusChangedAt.Equal(*changedAt) {
				t.Fatalf("no-op freeze moved status_changed_at")
			}
			changedAt = got.StatusChangedAt
		}
		if e.balance(t, acc.ID) != 5 {
			t.Fatal("status changes moved money")
		}
	})

	t.Run("close requires an empty account", func(t *testing.T) {
		negative := e.account(t, "USD", ledger.Debit, unrestricted)
		e.post(t, transfer("neg", negative.ID, e.open.ID, 5))
		pendingIn := e.account(t, "USD", ledger.Debit)
		e.post(t, pending(transfer("pend-in", e.open.ID, pendingIn.ID, 5)))
		pendingOut := e.account(t, "USD", ledger.Debit, unrestricted)
		e.post(t, pending(transfer("pend-out", pendingOut.ID, e.open.ID, 5)))
		drained := e.funded(t, 5)
		e.post(t, transfer("drain", drained.ID, e.open.ID, 5))
		for name, tt := range map[string]struct {
			id   uuid.UUID
			want error
		}{
			"positive balance":       {e.funded(t, 1).ID, ledger.ErrAccountNotEmpty},
			"negative balance":       {negative.ID, ledger.ErrAccountNotEmpty},
			"pending inflow":         {pendingIn.ID, ledger.ErrAccountNotEmpty},
			"pending outflow":        {pendingOut.ID, ledger.ErrAccountNotEmpty},
			"never used":             {e.account(t, "USD", ledger.Credit).ID, nil},
			"drained to zero":        {drained.ID, nil},
			"unknown account":        {uuid.New(), ledger.ErrNotFound},
			"credit normal positive": {feCreditFunded(t, e, 3).ID, ledger.ErrAccountNotEmpty},
		} {
			t.Run(name, func(t *testing.T) {
				got, err := e.m.CloseAccount(ctx, tt.id)
				if !errors.Is(err, tt.want) {
					t.Fatalf("CloseAccount() error = %v, want %v", err, tt.want)
				}
				if err == nil && (got.Status != ledger.AccountClosed || got.StatusChangedAt == nil) {
					t.Fatalf("closed = %+v", got)
				}
			})
		}
	})

	t.Run("closed is terminal", func(t *testing.T) {
		acc := e.account(t, "USD", ledger.Debit)
		closed, err := e.m.CloseAccount(ctx, acc.ID)
		if err != nil {
			t.Fatal(err)
		}
		for name, act := range map[string]func(context.Context, uuid.UUID) (ledger.Account, error){
			"freeze": e.m.FreezeAccount, "unfreeze": e.m.UnfreezeAccount,
		} {
			_, err := act(ctx, acc.ID)
			if !errors.Is(err, ledger.ErrAccountNotOpen) {
				t.Fatalf("%s a closed account: %v", name, err)
			}
		}
		again, err := e.m.CloseAccount(ctx, acc.ID)
		if err != nil || again.Version != closed.Version {
			t.Fatalf("close again = %+v, %v", again, err)
		}
		updated, err := e.m.UpdateAccount(ctx, acc.ID, ledger.UpdateInput{Description: new("archived")})
		if err != nil || updated.Description != "archived" || updated.Status != ledger.AccountClosed {
			t.Fatalf("updating a closed account's details = %+v, %v", updated, err)
		}
	})

	for name, act := range map[string]func(context.Context, uuid.UUID) (ledger.Account, error){
		"freeze": e.m.FreezeAccount, "unfreeze": e.m.UnfreezeAccount,
	} {
		t.Run(name+" unknown", func(t *testing.T) {
			_, err := act(ctx, uuid.New())
			wantErr(t, err, ledger.ErrNotFound)
		})
	}
	e.verify(t)
}

func TestCrudEdgeLockVersion(t *testing.T) {
	t.Parallel()
	e := setup(t)
	ctx := context.Background()
	acc := e.account(t, "USD", ledger.Debit)
	other := e.account(t, "USD", ledger.Debit, unrestricted)

	steps := []struct {
		name  string
		act   func()
		delta int64
	}{
		{"post in", func() { e.post(t, transfer("lv-1", e.open.ID, acc.ID, 100)) }, 1},
		{"replayed post", func() { e.post(t, transfer("lv-1", e.open.ID, acc.ID, 100)) }, 0},
		{"rejected post", func() {
			_, err := e.m.Post(ctx, transfer("lv-2", acc.ID, other.ID, 1_000))
			wantErr(t, err, ledger.ErrInsufficientFunds)
		}, 0},
		{"hold", func() {
			if _, err := e.m.CreateHold(ctx, feHoldAt("lv-hold", acc.ID, 10, time.Now().Add(time.Hour))); err != nil {
				t.Fatal(err)
			}
		}, 1},
		{"details update", func() {
			if _, err := e.m.UpdateAccount(ctx, acc.ID, ledger.UpdateInput{Name: new("renamed")}); err != nil {
				t.Fatal(err)
			}
		}, 1},
		{"no-op update", func() {
			if _, err := e.m.UpdateAccount(ctx, acc.ID, ledger.UpdateInput{Name: new("renamed")}); err != nil {
				t.Fatal(err)
			}
		}, 0},
		{"freeze", func() {
			if _, err := e.m.FreezeAccount(ctx, acc.ID); err != nil {
				t.Fatal(err)
			}
		}, 1},
		{"unfreeze", func() {
			if _, err := e.m.UnfreezeAccount(ctx, acc.ID); err != nil {
				t.Fatal(err)
			}
		}, 1},
		{"transaction touching the account twice", func() {
			e.post(t, ledger.PostInput{IdempotencyKey: "lv-twice", Postings: []ledger.Posting{
				{AccountID: acc.ID, Side: ledger.Credit, Amount: amt(5)},
				{AccountID: acc.ID, Side: ledger.Credit, Amount: amt(5)},
				{AccountID: other.ID, Side: ledger.Debit, Amount: amt(10)},
			}})
		}, 1},
	}
	version := e.get(t, acc.ID).Version
	for _, step := range steps {
		step.act()
		got := e.get(t, acc.ID).Version
		if got != version+step.delta {
			t.Fatalf("%s: version %d, want %d", step.name, got, version+step.delta)
		}
		version = got
	}
}

func TestCrudEdgeMetadataMergePatch(t *testing.T) {
	t.Parallel()
	e := setup(t)
	ctx := context.Background()
	acc, err := e.m.CreateAccount(ctx, ledger.CreateAccountInput{
		LedgerID: e.ledger.ID, Code: "mp", Currency: "USD", NormalSide: ledger.Debit,
		Metadata: jsontext.Value(`{"a":{"b":{"c":1,"d":2},"e":3},"arr":[1,{"x":1}],"n":1e2,"s":"keep"}`),
	})
	if err != nil {
		t.Fatal(err)
	}

	big := strings.Repeat("x", 9_000)
	steps := []struct {
		name     string
		patch    string
		metadata string
		changed  bool
		err      error
	}{
		{"whitespace patch", "   ", `{"a":{"b":{"c":1,"d":2},"e":3},"arr":[1,{"x":1}],"n":100,"s":"keep"}`, false, nil},
		{"equal number spelling", `{"n":100.0}`, `{"a":{"b":{"c":1,"d":2},"e":3},"arr":[1,{"x":1}],"n":100,"s":"keep"}`, false, nil},
		{"deep merge", `{"a":{"b":{"c":null,"z":9}}}`, `{"a":{"b":{"d":2,"z":9},"e":3},"arr":[1,{"x":1}],"n":100,"s":"keep"}`, true, nil},
		{"array replaced not merged", `{"arr":[{"y":2}]}`, `{"a":{"b":{"d":2,"z":9},"e":3},"arr":[{"y":2}],"n":100,"s":"keep"}`, true, nil},
		{"nulls inside arrays survive", `{"arr":[null]}`, `{"a":{"b":{"d":2,"z":9},"e":3},"arr":[null],"n":100,"s":"keep"}`, true, nil},
		{"nulls inside a new object are dropped", `{"fresh":{"gone":null,"kept":{"deeper":null,"v":true}}}`, `{"a":{"b":{"d":2,"z":9},"e":3},"arr":[null],"fresh":{"kept":{"v":true}},"n":100,"s":"keep"}`, true, nil},
		{"remove a whole subtree", `{"a":null,"fresh":null}`, `{"arr":[null],"n":100,"s":"keep"}`, true, nil},
		{"empty object value replaces scalar", `{"s":{}}`, `{"arr":[null],"n":100,"s":{}}`, true, nil},
		{"falsy scalars are values", `{"f":false,"z":0,"e":""}`, `{"arr":[null],"e":"","f":false,"n":100,"s":{},"z":0}`, true, nil},
		{"unicode keys", `{"ключ":"значение"}`, `{"arr":[null],"e":"","f":false,"n":100,"s":{},"z":0,"ключ":"значение"}`, true, nil},
		{"patch array", `[]`, "", false, ledger.ErrInvalid},
		{"patch number", `1`, "", false, ledger.ErrInvalid},
		{"patch true", `true`, "", false, ledger.ErrInvalid},
		{"patch trailing garbage", `{} {}`, "", false, ledger.ErrInvalid},
		{"null resets", `null`, `{}`, true, nil},
		{"null on empty", `null`, `{}`, false, nil},
		{"first half", `{"h1":"` + big + `"}`, `{"h1":"` + big + `"}`, true, nil},
		{"merged result too large", `{"h2":"` + big + `"}`, "", false, ledger.ErrInvalid},
		{"replace keeps it small", `{"h1":null,"h2":"` + big + `"}`, `{"h2":"` + big + `"}`, true, nil},
	}
	for _, step := range steps {
		before := e.get(t, acc.ID)
		got, err := e.m.UpdateAccount(ctx, acc.ID, ledger.UpdateInput{Metadata: jsontext.Value(step.patch)})
		if !errors.Is(err, step.err) {
			t.Fatalf("%s: error = %v, want %v", step.name, err, step.err)
		}
		if err != nil {
			if after := e.get(t, acc.ID); after.Version != before.Version || !jsonSame(t, after.Metadata, string(before.Metadata)) {
				t.Fatalf("%s: rejected patch changed the account", step.name)
			}
			continue
		}
		if !jsonSame(t, got.Metadata, step.metadata) {
			t.Fatalf("%s: metadata = %s, want %s", step.name, got.Metadata, step.metadata)
		}
		if changed := got.Version != before.Version; changed != step.changed {
			t.Fatalf("%s: version %d -> %d, want changed=%v", step.name, before.Version, got.Version, step.changed)
		}
	}
}

func TestCrudEdgeLedgers(t *testing.T) {
	t.Parallel()
	e := setup(t)
	ctx := context.Background()

	for name, tt := range map[string]struct {
		in   ledger.CreateLedgerInput
		want error
	}{
		"name at 255":        {ledger.CreateLedgerInput{Name: strings.Repeat("n", 255)}, nil},
		"name at 256":        {ledger.CreateLedgerInput{Name: strings.Repeat("n", 256)}, ledger.ErrInvalid},
		"no name":            {ledger.CreateLedgerInput{}, ledger.ErrInvalid},
		"name with NUL":      {ledger.CreateLedgerInput{Name: "a\x00"}, ledger.ErrInvalid},
		"description 1024":   {ledger.CreateLedgerInput{Name: "d", Description: strings.Repeat("d", 1024)}, nil},
		"description 1025":   {ledger.CreateLedgerInput{Name: "d", Description: strings.Repeat("d", 1025)}, ledger.ErrInvalid},
		"metadata null":      {ledger.CreateLedgerInput{Name: "m", Metadata: jsontext.Value(`null`)}, ledger.ErrInvalid},
		"metadata duplicate": {ledger.CreateLedgerInput{Name: "m", Metadata: jsontext.Value(`{"a":1,"a":2}`)}, ledger.ErrInvalid},
	} {
		t.Run(name, func(t *testing.T) {
			l, err := e.m.CreateLedger(ctx, tt.in)
			if !errors.Is(err, tt.want) {
				t.Fatalf("CreateLedger() error = %v, want %v", err, tt.want)
			}
			if err == nil && (l.Version != 0 || !jsonSame(t, l.Metadata, `{}`)) {
				t.Fatalf("ledger = %+v", l)
			}
		})
	}

	l, err := e.m.CreateLedger(ctx, ledger.CreateLedgerInput{Name: "upd", Metadata: jsontext.Value(`{"k":{"a":1}}`)})
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range []struct {
		name     string
		in       ledger.UpdateInput
		version  int64
		metadata string
		err      error
	}{
		{"blank name", ledger.UpdateInput{Name: new(" ")}, 0, `{"k":{"a":1}}`, ledger.ErrInvalid},
		{"same name", ledger.UpdateInput{Name: new("upd")}, 0, `{"k":{"a":1}}`, nil},
		{"description only", ledger.UpdateInput{Description: new("x")}, 1, `{"k":{"a":1}}`, nil},
		{"merge", ledger.UpdateInput{Metadata: jsontext.Value(`{"k":{"b":2}}`)}, 2, `{"k":{"a":1,"b":2}}`, nil},
		{"null resets", ledger.UpdateInput{Metadata: jsontext.Value(`null`)}, 3, `{}`, nil},
	} {
		got, err := e.m.UpdateLedger(ctx, l.ID, step.in)
		if !errors.Is(err, step.err) {
			t.Fatalf("%s: error = %v, want %v", step.name, err, step.err)
		}
		if err != nil {
			if got, err = e.m.Ledger(ctx, l.ID); err != nil {
				t.Fatal(err)
			}
		}
		if got.Version != step.version || !jsonSame(t, got.Metadata, step.metadata) {
			t.Fatalf("%s: ledger = %+v", step.name, got)
		}
	}
}
