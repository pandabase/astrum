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
	"github.com/pandabase/astrum/internal/modules/ledger"
	"github.com/pandabase/astrum/internal/money"
)

func feCategory(t *testing.T, e *env, name string, currency money.Currency, side ledger.Side) ledger.Category {
	t.Helper()
	c, err := e.m.CreateCategory(context.Background(), ledger.CreateCategoryInput{LedgerID: e.ledger.ID, Currency: currency, NormalSide: side, Name: name})
	if err != nil {
		t.Fatalf("CreateCategory(%s) error = %v", name, err)
	}
	return c
}

func feRollUp(t *testing.T, e *env, id uuid.UUID) ledger.Balances {
	t.Helper()
	c, err := e.m.Category(context.Background(), id, ledger.EffectiveRange{})
	if err != nil {
		t.Fatalf("Category() error = %v", err)
	}
	return c.Balances
}

func TestCategoriesEdgeUnnest(t *testing.T) {
	t.Parallel()
	e := setup(t)
	ctx := context.Background()
	x := e.account(t, "USD", ledger.Debit, unrestricted)
	y := e.account(t, "USD", ledger.Debit, unrestricted)
	e.post(t, transfer("un-x", e.open.ID, x.ID, 10))
	e.post(t, transfer("un-y", e.open.ID, y.ID, 100))

	root := e.addAccount(t, e.category(t, "root", ledger.Debit), x)
	child := e.addAccount(t, e.category(t, "child", ledger.Debit), y)
	nested := e.nest(t, root, child)
	wantBalance(t, "nested", nested.Balances.Posted, 110, 0, 110)

	un, err := e.m.UnnestCategory(ctx, root.ID, child.ID)
	if err != nil || un.ID != root.ID {
		t.Fatalf("UnnestCategory() = %+v, %v", un, err)
	}
	wantBalance(t, "after unnest", un.Balances.Posted, 10, 0, 10)
	wantBalance(t, "child keeps its own", feRollUp(t, e, child.ID).Posted, 100, 0, 100)
	if kids, err := e.m.ListCategories(ctx, ledger.ListCategoriesInput{ParentID: root.ID, Limit: 10}); err != nil || len(kids) != 0 {
		t.Fatalf("children after unnest = %+v, %v", kids, err)
	}
	if accs, err := e.m.ListAccounts(ctx, ledger.ListAccountsInput{CategoryID: root.ID, Limit: 10}); err != nil || len(accs) != 1 || accs[0].ID != x.ID {
		t.Fatalf("root accounts after unnest = %+v, %v", accs, err)
	}

	tests := []struct {
		name          string
		parent, child uuid.UUID
		want          error
	}{
		{"again", root.ID, child.ID, nil},
		{"never nested", child.ID, root.ID, nil},
		{"unknown child", root.ID, uuid.New(), nil},
		{"self", root.ID, root.ID, nil},
		{"unknown parent", uuid.New(), child.ID, ledger.ErrNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := e.m.UnnestCategory(ctx, tt.parent, tt.child)
			if !errors.Is(err, tt.want) {
				t.Fatalf("UnnestCategory() error = %v, want %v", err, tt.want)
			}
			if err == nil && got.ID != tt.parent {
				t.Fatalf("returned %s, want the parent %s", got.ID, tt.parent)
			}
		})
	}

	t.Run("reverse nesting is allowed after unnest", func(t *testing.T) {
		flipped, err := e.m.NestCategory(ctx, child.ID, root.ID)
		if err != nil {
			t.Fatal(err)
		}
		wantBalance(t, "flipped", flipped.Balances.Posted, 110, 0, 110)
		_, err = e.m.NestCategory(ctx, root.ID, child.ID)
		wantErr(t, err, ledger.ErrCategoryCycle)
		if _, err := e.m.UnnestCategory(ctx, child.ID, root.ID); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("one path of a diamond", func(t *testing.T) {
		top := e.category(t, "top", ledger.Debit)
		left, right := e.category(t, "left", ledger.Debit), e.category(t, "right", ledger.Debit)
		leaf := e.addAccount(t, e.category(t, "leaf", ledger.Debit), y)
		e.nest(t, top, left)
		e.nest(t, top, right)
		e.nest(t, left, leaf)
		e.nest(t, right, leaf)
		wantBalance(t, "diamond", feRollUp(t, e, top.ID).Posted, 100, 0, 100)
		if _, err := e.m.UnnestCategory(ctx, left.ID, leaf.ID); err != nil {
			t.Fatal(err)
		}
		wantBalance(t, "one path left", feRollUp(t, e, top.ID).Posted, 100, 0, 100)
		wantBalance(t, "left emptied", feRollUp(t, e, left.ID).Posted, 0, 0, 0)
		if _, err := e.m.UnnestCategory(ctx, right.ID, leaf.ID); err != nil {
			t.Fatal(err)
		}
		wantBalance(t, "no path left", feRollUp(t, e, top.ID).Posted, 0, 0, 0)
	})
}

func TestCategoriesEdgeGraph(t *testing.T) {
	t.Parallel()
	e := setup(t)
	ctx := context.Background()

	t.Run("two cycle", func(t *testing.T) {
		a, b := e.category(t, "a", ledger.Debit), e.category(t, "b", ledger.Debit)
		e.nest(t, a, b)
		_, err := e.m.NestCategory(ctx, b.ID, a.ID)
		wantErr(t, err, ledger.ErrCategoryCycle)
	})

	t.Run("self", func(t *testing.T) {
		a := e.category(t, "self", ledger.Debit)
		_, err := e.m.NestCategory(ctx, a.ID, a.ID)
		wantErr(t, err, ledger.ErrCategoryCycle)
	})

	t.Run("long cycle", func(t *testing.T) {
		chain := make([]ledger.Category, 5)
		for i := range chain {
			chain[i] = e.category(t, fmt.Sprint("lc", i), ledger.Debit)
			if i > 0 {
				e.nest(t, chain[i-1], chain[i])
			}
		}
		_, err := e.m.NestCategory(ctx, chain[4].ID, chain[0].ID)
		wantErr(t, err, ledger.ErrCategoryCycle)
		_, err = e.m.NestCategory(ctx, chain[3].ID, chain[1].ID)
		wantErr(t, err, ledger.ErrCategoryCycle)
		if _, err := e.m.NestCategory(ctx, chain[0].ID, chain[4].ID); err != nil {
			t.Fatalf("a shortcut edge is not a cycle: %v", err)
		}
	})

	t.Run("nesting twice is idempotent", func(t *testing.T) {
		acc := e.account(t, "USD", ledger.Debit, unrestricted)
		e.post(t, transfer("twice", e.open.ID, acc.ID, 9))
		p := e.category(t, "twice-parent", ledger.Debit)
		c := e.addAccount(t, e.category(t, "twice-child", ledger.Debit), acc)
		first := e.nest(t, p, c)
		second := e.nest(t, p, c)
		if first.Balances != second.Balances || second.Balances.Posted.Amount != amt(9) {
			t.Fatalf("balances %+v then %+v", first.Balances, second.Balances)
		}
		kids, err := e.m.ListCategories(ctx, ledger.ListCategoriesInput{ParentID: p.ID, Limit: 10})
		if err != nil || len(kids) != 1 {
			t.Fatalf("children = %d, %v", len(kids), err)
		}
	})

	t.Run("joining chains respects depth", func(t *testing.T) {
		mk := func(prefix string, n int) []ledger.Category {
			out := make([]ledger.Category, n)
			for i := range out {
				out[i] = e.category(t, fmt.Sprint(prefix, i), ledger.Debit)
				if i > 0 {
					e.nest(t, out[i-1], out[i])
				}
			}
			return out
		}
		upper, lower := mk("up", 4), mk("low", 3)
		if _, err := e.m.NestCategory(ctx, upper[3].ID, lower[0].ID); err != nil {
			t.Fatalf("a chain of exactly 7 was refused: %v", err)
		}
		upper2, lower2 := mk("up2", 4), mk("low2", 4)
		_, err := e.m.NestCategory(ctx, upper2[3].ID, lower2[0].ID)
		wantErr(t, err, ledger.ErrCategoryDepth)
		_, err = e.m.NestCategory(ctx, lower2[3].ID, upper2[0].ID)
		wantErr(t, err, ledger.ErrCategoryDepth)
		if _, err := e.m.NestCategory(ctx, upper2[0].ID, lower2[1].ID); err != nil {
			t.Fatalf("nesting a 3-chain under the root of a 4-chain: %v", err)
		}
	})

	t.Run("unknown or mismatched", func(t *testing.T) {
		usd := e.category(t, "usd", ledger.Debit)
		eur := feCategory(t, e, "eur", "EUR", ledger.Debit)
		other, err := e.m.CreateLedger(ctx, ledger.CreateLedgerInput{Name: "other"})
		if err != nil {
			t.Fatal(err)
		}
		foreign, err := e.m.CreateCategory(ctx, ledger.CreateCategoryInput{LedgerID: other.ID, Currency: "USD", NormalSide: ledger.Debit, Name: "foreign"})
		if err != nil {
			t.Fatal(err)
		}
		for name, tt := range map[string]struct {
			parent, child uuid.UUID
			want          error
		}{
			"unknown child":    {usd.ID, uuid.New(), ledger.ErrNotFound},
			"unknown parent":   {uuid.New(), usd.ID, ledger.ErrNotFound},
			"other currency":   {eur.ID, usd.ID, ledger.ErrCategoryMismatch},
			"other ledger":     {usd.ID, foreign.ID, ledger.ErrCategoryMismatch},
			"other ledger too": {foreign.ID, usd.ID, ledger.ErrCategoryMismatch},
		} {
			t.Run(name, func(t *testing.T) {
				_, err := e.m.NestCategory(ctx, tt.parent, tt.child)
				wantErr(t, err, tt.want)
			})
		}
	})
}

func TestCategoriesEdgeNormalSides(t *testing.T) {
	t.Parallel()
	e := setup(t)
	debit := e.account(t, "USD", ledger.Debit, unrestricted)
	credit := e.account(t, "USD", ledger.Credit, unrestricted)
	e.post(t, transfer("ns", credit.ID, debit.ID, 40))
	e.post(t, pending(transfer("ns-pending", credit.ID, debit.ID, 5)))

	tests := []struct {
		name     string
		side     ledger.Side
		accounts []ledger.Account
		posted   [3]int64
		pending  [3]int64
	}{
		{"debit category of debit account", ledger.Debit, []ledger.Account{debit}, [3]int64{40, 0, 40}, [3]int64{45, 0, 45}},
		{"credit category of debit account", ledger.Credit, []ledger.Account{debit}, [3]int64{40, 0, -40}, [3]int64{45, 0, -45}},
		{"debit category of credit account", ledger.Debit, []ledger.Account{credit}, [3]int64{0, 40, -40}, [3]int64{0, 45, -45}},
		{"credit category of credit account", ledger.Credit, []ledger.Account{credit}, [3]int64{0, 40, 40}, [3]int64{0, 45, 45}},
		{"both sides net to zero", ledger.Credit, []ledger.Account{debit, credit}, [3]int64{40, 40, 0}, [3]int64{45, 45, 0}},
		{"empty category", ledger.Credit, nil, [3]int64{0, 0, 0}, [3]int64{0, 0, 0}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := e.addAccount(t, e.category(t, tt.name, tt.side), tt.accounts...)
			wantBalance(t, "posted", c.Balances.Posted, tt.posted[0], tt.posted[1], tt.posted[2])
			wantBalance(t, "pending", c.Balances.Pending, tt.pending[0], tt.pending[1], tt.pending[2])
		})
	}
}

func TestCategoriesEdgeCurrenciesAndHolds(t *testing.T) {
	t.Parallel()
	e := setup(t)
	ctx := context.Background()
	eurSrc := e.account(t, "EUR", ledger.Credit, unrestricted)
	eurA := e.account(t, "EUR", ledger.Debit, unrestricted)
	usd := e.account(t, "USD", ledger.Debit, unrestricted)
	e.post(t, transfer("eur", eurSrc.ID, eurA.ID, 70))
	e.post(t, transfer("usd", e.open.ID, usd.ID, 3))

	eurCat := feCategory(t, e, "eur", "EUR", ledger.Debit)
	eurCat, err := e.m.AddCategoryAccount(ctx, eurCat.ID, eurA.ID)
	if err != nil {
		t.Fatal(err)
	}
	wantBalance(t, "eur", eurCat.Balances.Posted, 70, 0, 70)
	_, err = e.m.AddCategoryAccount(ctx, eurCat.ID, usd.ID)
	wantErr(t, err, ledger.ErrCategoryMismatch)
	eurChild := feCategory(t, e, "eur child", "EUR", ledger.Debit)
	eurChild = e.addAccount(t, eurChild, eurSrc)
	parent := e.nest(t, eurCat, eurChild)
	wantBalance(t, "eur tree", parent.Balances.Posted, 70, 70, 0)
	if _, err := e.m.CreateCategory(ctx, ledger.CreateCategoryInput{LedgerID: e.ledger.ID, Currency: "ZZZ", NormalSide: ledger.Debit, Name: "x"}); !errors.Is(err, ledger.ErrUnknownCurrency) {
		t.Fatalf("unregistered currency error = %v", err)
	}

	t.Run("holds count only without a window", func(t *testing.T) {
		held := e.account(t, "USD", ledger.Debit, unrestricted)
		e.post(t, transfer("held-fund", e.open.ID, held.ID, 50))
		if _, err := e.m.CreateHold(ctx, feHoldAt("cat-hold", held.ID, 20, time.Now().Add(time.Hour))); err != nil {
			t.Fatal(err)
		}
		c := e.addAccount(t, e.category(t, "held", ledger.Debit), held)
		wantBalance(t, "unbounded available", c.Balances.Available, 50, 20, 30)
		windowed, err := e.m.Category(ctx, c.ID, ledger.EffectiveRange{From: new(time.Now().Add(-time.Hour))})
		if err != nil {
			t.Fatal(err)
		}
		wantBalance(t, "windowed available", windowed.Balances.Available, 50, 0, 50)
	})

	t.Run("range validation", func(t *testing.T) {
		now := time.Now()
		for _, r := range []ledger.EffectiveRange{{From: &now, Until: &now}, {From: new(now.Add(time.Second)), Until: &now}} {
			_, err := e.m.Category(ctx, eurCat.ID, r)
			wantErr(t, err, ledger.ErrInvalid)
		}
		_, err := e.m.Category(ctx, uuid.New(), ledger.EffectiveRange{})
		wantErr(t, err, ledger.ErrNotFound)
	})
}

func TestCategoriesEdgeDeleteAndMembership(t *testing.T) {
	t.Parallel()
	e := setup(t)
	ctx := context.Background()
	acc := e.account(t, "USD", ledger.Debit, unrestricted)
	e.post(t, transfer("del", e.open.ID, acc.ID, 12))

	parent := e.category(t, "parent", ledger.Debit)
	child := e.addAccount(t, e.category(t, "child", ledger.Debit), acc)
	e.nest(t, parent, child)

	if err := e.m.DeleteCategory(ctx, parent.ID); err != nil {
		t.Fatal(err)
	}
	wantBalance(t, "orphaned child", feRollUp(t, e, child.ID).Posted, 12, 0, 12)
	if kids, err := e.m.ListCategories(ctx, ledger.ListCategoriesInput{ParentID: parent.ID, Limit: 10}); err != nil || len(kids) != 0 {
		t.Fatalf("children of a deleted category = %+v, %v", kids, err)
	}

	for name, act := range map[string]func() error{
		"delete again": func() error { return e.m.DeleteCategory(ctx, parent.ID) },
		"get":          func() error { _, err := e.m.Category(ctx, parent.ID, ledger.EffectiveRange{}); return err },
		"update": func() error {
			_, err := e.m.UpdateCategory(ctx, parent.ID, ledger.UpdateInput{Name: new("x")})
			return err
		},
		"nest into":           func() error { _, err := e.m.NestCategory(ctx, parent.ID, child.ID); return err },
		"nest deleted":        func() error { _, err := e.m.NestCategory(ctx, child.ID, parent.ID); return err },
		"unnest":              func() error { _, err := e.m.UnnestCategory(ctx, parent.ID, child.ID); return err },
		"add account":         func() error { _, err := e.m.AddCategoryAccount(ctx, parent.ID, acc.ID); return err },
		"remove account":      func() error { _, err := e.m.RemoveCategoryAccount(ctx, parent.ID, acc.ID); return err },
		"delete unknown":      func() error { return e.m.DeleteCategory(ctx, uuid.New()) },
		"add unknown account": func() error { _, err := e.m.AddCategoryAccount(ctx, child.ID, uuid.New()); return err },
	} {
		t.Run(name, func(t *testing.T) { wantErr(t, act(), ledger.ErrNotFound) })
	}

	t.Run("removing a non-member is a no-op", func(t *testing.T) {
		other := e.account(t, "USD", ledger.Debit)
		got, err := e.m.RemoveCategoryAccount(ctx, child.ID, other.ID)
		if err != nil {
			t.Fatal(err)
		}
		wantBalance(t, "unchanged", got.Balances.Posted, 12, 0, 12)
		got, err = e.m.RemoveCategoryAccount(ctx, child.ID, uuid.New())
		if err != nil || got.ID != child.ID {
			t.Fatalf("removing an unknown account = %+v, %v", got, err)
		}
	})

	t.Run("closed and frozen accounts can be members", func(t *testing.T) {
		closed := e.account(t, "USD", ledger.Debit)
		if _, err := e.m.CloseAccount(ctx, closed.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := e.m.FreezeAccount(ctx, acc.ID); err != nil {
			t.Fatal(err)
		}
		c := e.addAccount(t, e.category(t, "statuses", ledger.Debit), closed, acc)
		wantBalance(t, "members", c.Balances.Posted, 12, 0, 12)
	})

	t.Run("deleting a member category keeps the account", func(t *testing.T) {
		if err := e.m.DeleteCategory(ctx, child.ID); err != nil {
			t.Fatal(err)
		}
		if cats, err := e.m.ListCategories(ctx, ledger.ListCategoriesInput{AccountID: acc.ID, Limit: 10}); err != nil || len(cats) != 1 {
			t.Fatalf("categories of the account = %d, %v", len(cats), err)
		}
		if e.balance(t, acc.ID) != 12 {
			t.Fatal("deleting a category changed an account")
		}
	})
}

func TestCategoriesEdgeUpdate(t *testing.T) {
	t.Parallel()
	e := setup(t)
	ctx := context.Background()
	c, err := e.m.CreateCategory(ctx, ledger.CreateCategoryInput{
		LedgerID: e.ledger.ID, Currency: "USD", NormalSide: ledger.Debit, Name: "c", Description: "d",
		Metadata: jsontext.Value(`{"a":{"b":1,"c":2},"list":[1,2],"keep":"k"}`),
	})
	if err != nil {
		t.Fatal(err)
	}

	steps := []struct {
		name     string
		in       ledger.UpdateInput
		version  int64
		metadata string
		err      error
	}{
		{"empty update", ledger.UpdateInput{}, 0, `{"a":{"b":1,"c":2},"list":[1,2],"keep":"k"}`, nil},
		{"same values", ledger.UpdateInput{Name: new("c"), Description: new("d"), Metadata: jsontext.Value(`{"keep":"k"}`)}, 0, `{"a":{"b":1,"c":2},"list":[1,2],"keep":"k"}`, nil},
		{"empty patch object", ledger.UpdateInput{Metadata: jsontext.Value(`{}`)}, 0, `{"a":{"b":1,"c":2},"list":[1,2],"keep":"k"}`, nil},
		{"nested merge", ledger.UpdateInput{Metadata: jsontext.Value(`{"a":{"c":null,"d":3}}`)}, 1, `{"a":{"b":1,"d":3},"list":[1,2],"keep":"k"}`, nil},
		{"arrays are replaced", ledger.UpdateInput{Metadata: jsontext.Value(`{"list":[3]}`)}, 2, `{"a":{"b":1,"d":3},"list":[3],"keep":"k"}`, nil},
		{"object replaces scalar", ledger.UpdateInput{Metadata: jsontext.Value(`{"keep":{"x":null,"y":1}}`)}, 3, `{"a":{"b":1,"d":3},"list":[3],"keep":{"y":1}}`, nil},
		{"scalar replaces object", ledger.UpdateInput{Metadata: jsontext.Value(`{"a":"flat"}`)}, 4, `{"a":"flat","list":[3],"keep":{"y":1}}`, nil},
		{"deleting a missing key", ledger.UpdateInput{Metadata: jsontext.Value(`{"missing":null}`)}, 4, `{"a":"flat","list":[3],"keep":{"y":1}}`, nil},
		{"null resets", ledger.UpdateInput{Metadata: jsontext.Value(`null`)}, 5, `{}`, nil},
		{"null on empty is unchanged", ledger.UpdateInput{Metadata: jsontext.Value(` null `)}, 5, `{}`, nil},
		{"rename", ledger.UpdateInput{Name: new("renamed")}, 6, `{}`, nil},
		{"empty name", ledger.UpdateInput{Name: new("")}, 6, `{}`, ledger.ErrInvalid},
		{"blank name", ledger.UpdateInput{Name: new("  ")}, 6, `{}`, ledger.ErrInvalid},
		{"name too long", ledger.UpdateInput{Name: new(strings.Repeat("n", 256))}, 6, `{}`, ledger.ErrInvalid},
		{"name at limit", ledger.UpdateInput{Name: new(strings.Repeat("n", 255))}, 7, `{}`, nil},
		{"patch is an array", ledger.UpdateInput{Metadata: jsontext.Value(`[{"a":1}]`)}, 7, `{}`, ledger.ErrInvalid},
		{"patch is a string", ledger.UpdateInput{Metadata: jsontext.Value(`"x"`)}, 7, `{}`, ledger.ErrInvalid},
		{"patch is malformed", ledger.UpdateInput{Metadata: jsontext.Value(`{"a":`)}, 7, `{}`, ledger.ErrInvalid},
	}
	for _, step := range steps {
		got, err := e.m.UpdateCategory(ctx, c.ID, step.in)
		if !errors.Is(err, step.err) {
			t.Fatalf("%s: error = %v, want %v", step.name, err, step.err)
		}
		if err != nil {
			got, err = e.m.Category(ctx, c.ID, ledger.EffectiveRange{})
			if err != nil {
				t.Fatal(err)
			}
		}
		if got.Version != step.version || !jsonSame(t, got.Metadata, step.metadata) {
			t.Fatalf("%s: version %d metadata %s, want %d %s", step.name, got.Version, got.Metadata, step.version, step.metadata)
		}
	}
	_, err = e.m.UpdateCategory(ctx, uuid.New(), ledger.UpdateInput{Name: new("x")})
	wantErr(t, err, ledger.ErrNotFound)
}

func TestCategoriesEdgeCreateAndList(t *testing.T) {
	t.Parallel()
	e := setup(t)
	ctx := context.Background()

	for name, tt := range map[string]struct {
		in   ledger.CreateCategoryInput
		want error
	}{
		"no ledger":        {ledger.CreateCategoryInput{Currency: "USD", NormalSide: ledger.Debit, Name: "x"}, ledger.ErrInvalid},
		"bad currency":     {ledger.CreateCategoryInput{LedgerID: e.ledger.ID, Currency: "usd", NormalSide: ledger.Debit, Name: "x"}, ledger.ErrInvalid},
		"name too long":    {ledger.CreateCategoryInput{LedgerID: e.ledger.ID, Currency: "USD", NormalSide: ledger.Debit, Name: strings.Repeat("n", 256)}, ledger.ErrInvalid},
		"metadata array":   {ledger.CreateCategoryInput{LedgerID: e.ledger.ID, Currency: "USD", NormalSide: ledger.Debit, Name: "x", Metadata: jsontext.Value(`[]`)}, ledger.ErrInvalid},
		"no side":          {ledger.CreateCategoryInput{LedgerID: e.ledger.ID, Currency: "USD", Name: "x"}, ledger.ErrInvalid},
		"name at limit":    {ledger.CreateCategoryInput{LedgerID: e.ledger.ID, Currency: "USD", NormalSide: ledger.Credit, Name: strings.Repeat("n", 255)}, nil},
		"custom currency":  {ledger.CreateCategoryInput{LedgerID: e.ledger.ID, Currency: "JPY", NormalSide: ledger.Credit, Name: "yen"}, nil},
		"unknown currency": {ledger.CreateCategoryInput{LedgerID: e.ledger.ID, Currency: "QQQ", NormalSide: ledger.Credit, Name: "q"}, ledger.ErrUnknownCurrency},
	} {
		t.Run(name, func(t *testing.T) {
			c, err := e.m.CreateCategory(ctx, tt.in)
			if !errors.Is(err, tt.want) {
				t.Fatalf("CreateCategory() error = %v, want %v", err, tt.want)
			}
			if err == nil && (c.Version != 0 || !jsonSame(t, c.Metadata, `{}`)) {
				t.Fatalf("created = %+v", c)
			}
		})
	}

	fresh := setup(t)
	cats := make([]ledger.Category, 4)
	for i := range cats {
		cats[i] = fresh.category(t, fmt.Sprint("list-", i), ledger.Debit)
	}
	ids := func(cs []ledger.Category) string {
		out := make([]string, len(cs))
		for i, c := range cs {
			out[i] = c.Name
		}
		return strings.Join(out, ",")
	}
	filters := map[string]string{}
	for i := range 21 {
		filters[fmt.Sprint("k", i)] = "v"
	}
	tests := []struct {
		name string
		in   ledger.ListCategoriesInput
		want string
		err  error
	}{
		{"limit zero", ledger.ListCategoriesInput{}, "", ledger.ErrInvalid},
		{"negative limit", ledger.ListCategoriesInput{Limit: -1}, "", ledger.ErrInvalid},
		{"limit over max", ledger.ListCategoriesInput{Limit: 1001}, "", ledger.ErrInvalid},
		{"too many metadata filters", ledger.ListCategoriesInput{Metadata: filters, Limit: 10}, "", ledger.ErrInvalid},
		{"limit at max", ledger.ListCategoriesInput{Limit: 1000}, "list-3,list-2,list-1,list-0", nil},
		{"limit one", ledger.ListCategoriesInput{Limit: 1}, "list-3", nil},
		{"exact page", ledger.ListCategoriesInput{Limit: 4}, "list-3,list-2,list-1,list-0", nil},
		{"after exact page", ledger.ListCategoriesInput{Before: cats[0].ID, Limit: 4}, "", nil},
		{"cursor", ledger.ListCategoriesInput{Before: cats[2].ID, Limit: 1}, "list-1", nil},
		{"other ledger", ledger.ListCategoriesInput{LedgerID: uuid.New(), Limit: 10}, "", nil},
		{"unknown parent", ledger.ListCategoriesInput{ParentID: uuid.New(), Limit: 10}, "", nil},
		{"unknown account", ledger.ListCategoriesInput{AccountID: uuid.New(), Limit: 10}, "", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := fresh.m.ListCategories(ctx, tt.in)
			if !errors.Is(err, tt.err) {
				t.Fatalf("ListCategories() error = %v, want %v", err, tt.err)
			}
			if err == nil && ids(got) != tt.want {
				t.Fatalf("categories = %s, want %s", ids(got), tt.want)
			}
		})
	}
}
