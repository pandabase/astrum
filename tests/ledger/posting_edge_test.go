package tests

import (
	"context"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pandabase/astrum/internal/modules/ledger"
	"github.com/pandabase/astrum/internal/money"
)

func peLeg(id uuid.UUID, side ledger.Side, n int64) ledger.Posting {
	return ledger.Posting{AccountID: id, Side: side, Amount: amt(n)}
}

func peLegs(key string, legs ...ledger.Posting) ledger.PostInput {
	return ledger.PostInput{IdempotencyKey: key, Postings: legs}
}

func peOpposite(s ledger.Side) ledger.Side {
	if s == ledger.Debit {
		return ledger.Credit
	}
	return ledger.Debit
}

func peKeyExists(t testing.TB, e *env, key string) bool {
	t.Helper()
	var exists bool
	if err := e.pool.QueryRow(context.Background(),
		`SELECT EXISTS (SELECT 1 FROM ledger_transactions WHERE idempotency_key = $1)`, key).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	return exists
}

func peViews(t testing.TB, e *env, id uuid.UUID) [3]money.Amount {
	t.Helper()
	acc := e.get(t, id)
	return [3]money.Amount{acc.Posted.Amount, acc.Pending.Amount, acc.Available.Amount}
}

func TestPostingEdgeValidation(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	a := e.funded(t, 1_000)
	b := e.account(t, "USD", ledger.Debit)
	metadataOf := func(size int) jsontext.Value {
		return jsontext.Value(`{"a":"` + strings.Repeat("x", size-8) + `"}`)
	}

	tests := []struct {
		name   string
		mutate func(*ledger.PostInput)
		want   error
	}{
		{"minimum amount of one", func(*ledger.PostInput) {}, nil},
		{"key exactly 255 bytes", func(in *ledger.PostInput) { in.IdempotencyKey = strings.Repeat("k", 255) }, nil},
		{"key 256 bytes", func(in *ledger.PostInput) { in.IdempotencyKey = strings.Repeat("k", 256) }, ledger.ErrInvalid},
		{"key 255 bytes of multibyte runes", func(in *ledger.PostInput) { in.IdempotencyKey = strings.Repeat("€", 85) }, nil},
		{"key 258 bytes of multibyte runes", func(in *ledger.PostInput) { in.IdempotencyKey = strings.Repeat("€", 86) }, ledger.ErrInvalid},
		{"key empty", func(in *ledger.PostInput) { in.IdempotencyKey = "" }, ledger.ErrInvalid},
		{"key whitespace only", func(in *ledger.PostInput) { in.IdempotencyKey = " \t\n" }, ledger.ErrInvalid},
		{"key with surrounding whitespace", func(in *ledger.PostInput) { in.IdempotencyKey = " padded " }, nil},
		{"key with NUL", func(in *ledger.PostInput) { in.IdempotencyKey = "k\x00" }, ledger.ErrInvalid},
		{"key invalid utf8", func(in *ledger.PostInput) { in.IdempotencyKey = "k\xff" }, ledger.ErrInvalid},
		{"description exactly 1024 bytes", func(in *ledger.PostInput) { in.Description = strings.Repeat("d", 1024) }, nil},
		{"description 1025 bytes", func(in *ledger.PostInput) { in.Description = strings.Repeat("d", 1025) }, ledger.ErrInvalid},
		{"description with NUL", func(in *ledger.PostInput) { in.Description = "a\x00b" }, ledger.ErrInvalid},
		{"description invalid utf8", func(in *ledger.PostInput) { in.Description = "\xc3\x28" }, ledger.ErrInvalid},
		{"description encoded surrogate", func(in *ledger.PostInput) { in.Description = "\xed\xa0\x80" }, ledger.ErrInvalid},
		{"metadata exactly 16384 bytes", func(in *ledger.PostInput) { in.Metadata = metadataOf(16 << 10) }, nil},
		{"metadata 16385 bytes", func(in *ledger.PostInput) { in.Metadata = metadataOf(16<<10 + 1) }, ledger.ErrInvalid},
		{"metadata whitespace only", func(in *ledger.PostInput) { in.Metadata = jsontext.Value(" \n\t") }, nil},
		{"metadata empty object", func(in *ledger.PostInput) { in.Metadata = jsontext.Value(`{}`) }, nil},
		{"metadata escaped NUL value", func(in *ledger.PostInput) { in.Metadata = jsontext.Value(`{"a":"\u0000"}`) }, ledger.ErrInvalid},
		{"metadata escaped NUL name", func(in *ledger.PostInput) { in.Metadata = jsontext.Value(`{"\u0000":1}`) }, ledger.ErrInvalid},
		{"metadata raw invalid utf8", func(in *ledger.PostInput) { in.Metadata = jsontext.Value("{\"a\":\"\xff\"}") }, ledger.ErrInvalid},
		{"metadata lone surrogate escape", func(in *ledger.PostInput) { in.Metadata = jsontext.Value(`{"a":"\ud800"}`) }, ledger.ErrInvalid},
		{"metadata duplicate names", func(in *ledger.PostInput) { in.Metadata = jsontext.Value(`{"a":1,"a":2}`) }, ledger.ErrInvalid},
		{"metadata array", func(in *ledger.PostInput) { in.Metadata = jsontext.Value(`[]`) }, ledger.ErrInvalid},
		{"metadata null", func(in *ledger.PostInput) { in.Metadata = jsontext.Value(`null`) }, ledger.ErrInvalid},
		{"metadata string", func(in *ledger.PostInput) { in.Metadata = jsontext.Value(`"x"`) }, ledger.ErrInvalid},
		{"metadata number", func(in *ledger.PostInput) { in.Metadata = jsontext.Value(`1`) }, ledger.ErrInvalid},
		{"metadata trailing value", func(in *ledger.PostInput) { in.Metadata = jsontext.Value(`{} {}`) }, ledger.ErrInvalid},
		{"metadata truncated", func(in *ledger.PostInput) { in.Metadata = jsontext.Value(`{"a":`) }, ledger.ErrInvalid},
		{"external_id exactly 255 bytes", func(in *ledger.PostInput) { in.ExternalID = strings.Repeat("e", 255) }, nil},
		{"external_id 256 bytes", func(in *ledger.PostInput) { in.ExternalID = strings.Repeat("e", 256) }, ledger.ErrInvalid},
		{"external_id with NUL", func(in *ledger.PostInput) { in.ExternalID = "e\x00" }, ledger.ErrInvalid},
		{"external_id invalid utf8", func(in *ledger.PostInput) { in.ExternalID = "e\xfe" }, ledger.ErrInvalid},
		{"status archived", func(in *ledger.PostInput) { in.Status = ledger.TransactionArchived }, ledger.ErrInvalid},
		{"status uppercase", func(in *ledger.PostInput) { in.Status = "POSTED" }, ledger.ErrInvalid},
		{"nil postings", func(in *ledger.PostInput) { in.Postings = nil }, ledger.ErrInvalid},
		{"empty postings", func(in *ledger.PostInput) { in.Postings = []ledger.Posting{} }, ledger.ErrInvalid},
		{"single posting", func(in *ledger.PostInput) { in.Postings = in.Postings[:1] }, ledger.ErrInvalid},
		{"zero amounts", func(in *ledger.PostInput) {
			in.Postings = []ledger.Posting{peLeg(b.ID, ledger.Debit, 0), peLeg(a.ID, ledger.Credit, 0)}
		}, ledger.ErrInvalid},
		{"negative amounts on both sides", func(in *ledger.PostInput) {
			in.Postings = []ledger.Posting{peLeg(b.ID, ledger.Debit, -5), peLeg(a.ID, ledger.Credit, -5)}
		}, ledger.ErrInvalid},
		{"negative amount that would balance", func(in *ledger.PostInput) {
			in.Postings = []ledger.Posting{peLeg(b.ID, ledger.Debit, 1), peLeg(a.ID, ledger.Credit, 2), peLeg(b.ID, ledger.Credit, -1)}
		}, ledger.ErrInvalid},
		{"nil account", func(in *ledger.PostInput) { in.Postings[0].AccountID = uuid.Nil }, ledger.ErrInvalid},
		{"empty side", func(in *ledger.PostInput) { in.Postings[0].Side = "" }, ledger.ErrInvalid},
		{"uppercase side", func(in *ledger.PostInput) { in.Postings[0].Side = "DEBIT" }, ledger.ErrInvalid},
		{"lowercase currency", func(in *ledger.PostInput) { in.Postings[0].Currency = "usd" }, ledger.ErrInvalid},
		{"well formed unregistered currency", func(in *ledger.PostInput) { in.Postings[0].Currency = "ZZZQ" }, ledger.ErrInvalid},
		{"negative lock_version", func(in *ledger.PostInput) { v := int64(-1); in.Postings[1].LockVersion = &v }, ledger.ErrInvalid},
		{"unknown account on second leg", func(in *ledger.PostInput) { in.Postings[1].AccountID = uuid.New() }, ledger.ErrNotFound},
		{"unknown account on both legs", func(in *ledger.PostInput) {
			in.Postings[0].AccountID, in.Postings[1].AccountID = uuid.New(), uuid.New()
		}, ledger.ErrNotFound},
	}

	moved := int64(0)
	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := transfer(fmt.Sprintf("valid-%d", i), a.ID, b.ID, 1)
			tt.mutate(&in)
			txn, err := e.m.Post(ctx, in)
			if tt.want != nil {
				wantErr(t, err, tt.want)
				return
			}
			if err != nil {
				t.Fatalf("Post() error = %v", err)
			}
			moved++
			stored, err := e.m.Transaction(ctx, txn.ID)
			if err != nil || stored.IdempotencyKey != in.IdempotencyKey || stored.Description != in.Description || stored.ExternalID != in.ExternalID {
				t.Fatalf("stored = %+v, %v", stored, err)
			}
		})
	}
	if got := e.balance(t, b.ID); got != moved {
		t.Fatalf("b = %d, want %d (only accepted posts move money)", got, moved)
	}
	if got := e.balance(t, a.ID); got != 1_000-moved {
		t.Fatalf("a = %d, want %d", got, 1_000-moved)
	}
	e.verify(t)
}

func TestPostingEdgePostingCount(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	a := e.funded(t, 10_000)
	b := e.account(t, "USD", ledger.Debit)
	legs := func(pairs int, extra int) []ledger.Posting {
		out := make([]ledger.Posting, 0, 2*pairs+extra)
		for range pairs {
			out = append(out, peLeg(b.ID, ledger.Debit, 1), peLeg(a.ID, ledger.Credit, 1))
		}
		if extra > 0 {
			out = append(out, peLeg(b.ID, ledger.Debit, 1))
		}
		return out
	}

	t.Run("1001 postings rejected", func(t *testing.T) {
		_, err := e.m.Post(ctx, ledger.PostInput{IdempotencyKey: "too-many", Postings: legs(500, 1)})
		wantErr(t, err, ledger.ErrInvalid)
	})

	t.Run("1000 postings accepted", func(t *testing.T) {
		txn, err := e.m.Post(ctx, ledger.PostInput{IdempotencyKey: "max-legs", Postings: legs(500, 0)})
		if err != nil {
			t.Fatal(err)
		}
		if len(txn.Postings) != 1000 {
			t.Fatalf("postings = %d, want 1000", len(txn.Postings))
		}
		stored, err := e.m.Transaction(ctx, txn.ID)
		if err != nil || len(stored.Postings) != 1000 {
			t.Fatalf("stored postings = %d, %v", len(stored.Postings), err)
		}
	})

	t.Run("two postings are the minimum", func(t *testing.T) {
		e.post(t, ledger.PostInput{IdempotencyKey: "min-legs", Postings: legs(1, 0)})
	})

	if got := e.balance(t, b.ID); got != 501 {
		t.Fatalf("b = %d, want 501", got)
	}
	e.verify(t)
}

func TestPostingEdgeAmountBounds(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	top := money.MaxAmount()
	fresh := func() (ledger.Account, ledger.Account) {
		return e.account(t, "USD", ledger.Debit, unrestricted), e.account(t, "USD", ledger.Debit, unrestricted)
	}

	t.Run("negative side overflow", func(t *testing.T) {
		x, y := fresh()
		z := e.account(t, "USD", ledger.Debit)
		e.post(t, transferAmount("neg-max", x.ID, y.ID, top))
		if got := e.get(t, x.ID).Posted.Amount; got != top.Neg() {
			t.Fatalf("x = %s, want %s", got, top.Neg())
		}
		_, err := e.m.Post(ctx, transfer("neg-over", x.ID, z.ID, 1))
		wantErr(t, err, money.ErrOverflow)
		if got := e.balance(t, z.ID); got != 0 {
			t.Fatalf("z = %d after overflow", got)
		}
	})

	t.Run("legs summing past max in one transaction", func(t *testing.T) {
		x, y := fresh()
		_, err := e.m.Post(ctx, ledger.PostInput{IdempotencyKey: "sum-over", Postings: []ledger.Posting{
			{AccountID: y.ID, Side: ledger.Debit, Amount: top},
			peLeg(y.ID, ledger.Debit, 1),
			{AccountID: x.ID, Side: ledger.Credit, Amount: top},
			peLeg(x.ID, ledger.Credit, 1),
		}})
		wantErr(t, err, money.ErrOverflow)
		if e.balance(t, x.ID) != 0 || e.balance(t, y.ID) != 0 || peKeyExists(t, e, "sum-over") {
			t.Fatal("overflowing transaction left a trace")
		}
	})

	t.Run("offsetting max legs", func(t *testing.T) {
		x, y := fresh()
		txn := e.post(t, ledger.PostInput{IdempotencyKey: "max-offset", Postings: []ledger.Posting{
			{AccountID: y.ID, Side: ledger.Debit, Amount: top},
			{AccountID: x.ID, Side: ledger.Credit, Amount: top},
			{AccountID: x.ID, Side: ledger.Debit, Amount: top},
			{AccountID: y.ID, Side: ledger.Credit, Amount: top},
		}})
		acc := e.get(t, y.ID)
		if acc.Posted.Debits != top || acc.Posted.Credits != top || !acc.Posted.Amount.IsZero() || len(txn.Postings) != 4 {
			t.Fatalf("y posted = %+v", acc.Posted)
		}
	})

	t.Run("pending on top of max posted overflows the pending view", func(t *testing.T) {
		x, y := fresh()
		e.post(t, transferAmount("pend-max", x.ID, y.ID, top))
		_, err := e.m.Post(ctx, pending(transfer("pend-over", x.ID, y.ID, 1)))
		wantErr(t, err, money.ErrOverflow)
		if acc := e.get(t, y.ID); acc.Pending.Amount != top || acc.Posted.Amount != top {
			t.Fatalf("y = %+v", acc)
		}
	})

	t.Run("39 digit amounts are unrepresentable", func(t *testing.T) {
		if got := money.MustParseAmount(strings.Repeat("9", 38)); got != top {
			t.Fatalf("38 nines = %s, want max", got)
		}
		for _, s := range []string{strings.Repeat("9", 39), "1" + strings.Repeat("0", 38), "-" + strings.Repeat("9", 39)} {
			if _, err := money.ParseAmount(s); !errors.Is(err, money.ErrInvalidAmount) {
				t.Errorf("ParseAmount(%s) error = %v, want ErrInvalidAmount", s, err)
			}
		}
	})

	t.Run("http boundary at 38 digits", func(t *testing.T) {
		api := newAPI(t)
		from := api.account("edge-from", "debit", `,"allow_negative":true`)
		to := api.account("edge-to", "debit", `,"allow_negative":true`)
		api.must(http.StatusCreated, http.MethodPost, "/v1/transactions", "digits-38", transferJSON(from, to, strings.Repeat("9", 38)))
		for key, amount := range map[string]string{
			"digits-39":     strings.Repeat("9", 39),
			"leading-zero":  "01",
			"fractional":    "1.5",
			"exponent":      "1e3",
			"negative-zero": "-0",
		} {
			resp := api.do(http.MethodPost, "/v1/transactions", key, transferJSON(to, from, amount))
			if resp.status != http.StatusBadRequest {
				t.Errorf("%s: status = %d %v, want 400", amount, resp.status, resp.body)
			}
		}
	})
	e.verify(t)
}

func TestPostingEdgeRepeatedAccounts(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	a := e.funded(t, 100)
	b := e.account(t, "USD", ledger.Debit)

	t.Run("self transfer nets to zero and bumps version once", func(t *testing.T) {
		before := e.get(t, a.ID)
		e.post(t, peLegs("self", peLeg(a.ID, ledger.Debit, 40), peLeg(a.ID, ledger.Credit, 40)))
		after := e.get(t, a.ID)
		if after.Posted.Amount != before.Posted.Amount || after.Posted.Debits != amt(140) || after.Posted.Credits != amt(40) {
			t.Fatalf("a posted = %+v", after.Posted)
		}
		if after.Version != before.Version+1 {
			t.Fatalf("version = %d, want %d", after.Version, before.Version+1)
		}
	})

	t.Run("self transfer on an empty non-negative account", func(t *testing.T) {
		empty := e.account(t, "USD", ledger.Debit)
		e.post(t, peLegs("self-empty", peLeg(empty.ID, ledger.Credit, 5), peLeg(empty.ID, ledger.Debit, 5)))
		if got := e.balance(t, empty.ID); got != 0 {
			t.Fatalf("empty = %d", got)
		}
	})

	t.Run("repeated inflow legs accumulate", func(t *testing.T) {
		e.post(t, peLegs("split-in", peLeg(b.ID, ledger.Debit, 10), peLeg(b.ID, ledger.Debit, 15), peLeg(a.ID, ledger.Credit, 25)))
		if got := e.balance(t, b.ID); got != 25 {
			t.Fatalf("b = %d, want 25", got)
		}
	})

	t.Run("repeated outflow legs are checked in total", func(t *testing.T) {
		_, err := e.m.Post(ctx, peLegs("split-out",
			peLeg(b.ID, ledger.Debit, 50), peLeg(a.ID, ledger.Credit, 50),
			peLeg(b.ID, ledger.Debit, 26), peLeg(a.ID, ledger.Credit, 26)))
		wantErr(t, err, ledger.ErrInsufficientFunds)
		e.post(t, peLegs("split-out-exact",
			peLeg(b.ID, ledger.Debit, 50), peLeg(a.ID, ledger.Credit, 50),
			peLeg(b.ID, ledger.Debit, 25), peLeg(a.ID, ledger.Credit, 25)))
		if got := e.balance(t, a.ID); got != 0 {
			t.Fatalf("a = %d, want 0", got)
		}
	})

	for name, order := range map[string][]int{"inflow first": {0, 1, 2, 3}, "outflow first": {2, 3, 0, 1}} {
		t.Run("funds are checked on the final state "+name, func(t *testing.T) {
			relay := e.account(t, "USD", ledger.Debit)
			sink := e.account(t, "USD", ledger.Debit)
			legs := []ledger.Posting{
				peLeg(relay.ID, ledger.Debit, 30), peLeg(e.open.ID, ledger.Credit, 30),
				peLeg(relay.ID, ledger.Credit, 30), peLeg(sink.ID, ledger.Debit, 30),
			}
			ordered := make([]ledger.Posting, len(legs))
			for i, j := range order {
				ordered[i] = legs[j]
			}
			e.post(t, ledger.PostInput{IdempotencyKey: "relay-" + name, Postings: ordered})
			if e.balance(t, relay.ID) != 0 || e.balance(t, sink.ID) != 30 {
				t.Fatal("relay did not pass funds through")
			}
		})
	}
	e.verify(t)
}

func TestPostingEdgeCurrencies(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	usdA := e.funded(t, 100)
	usdB := e.account(t, "USD", ledger.Debit)
	eurIssuer := e.account(t, "EUR", ledger.Credit, unrestricted)
	eurB := e.account(t, "EUR", ledger.Debit)

	t.Run("balanced per currency", func(t *testing.T) {
		txn := e.post(t, peLegs("fx",
			peLeg(usdB.ID, ledger.Debit, 10), peLeg(usdA.ID, ledger.Credit, 10),
			peLeg(eurB.ID, ledger.Debit, 7), peLeg(eurIssuer.ID, ledger.Credit, 7)))
		wantCurrencies := []money.Currency{"USD", "USD", "EUR", "EUR"}
		for i, p := range txn.Postings {
			if p.Currency != wantCurrencies[i] {
				t.Fatalf("leg %d currency = %s, want %s", i, p.Currency, wantCurrencies[i])
			}
		}
		if e.balance(t, usdB.ID) != 10 || e.balance(t, eurB.ID) != 7 || e.balance(t, eurIssuer.ID) != 7 {
			t.Fatal("multi-currency balances wrong")
		}
	})

	tests := []struct {
		name string
		in   ledger.PostInput
		want error
	}{
		{"zero net across currencies but not per currency", peLegs("fx-net",
			peLeg(usdB.ID, ledger.Debit, 13), peLeg(usdA.ID, ledger.Credit, 10),
			peLeg(eurB.ID, ledger.Debit, 7), peLeg(eurIssuer.ID, ledger.Credit, 10)), ledger.ErrUnbalanced},
		{"one currency unbalanced", peLegs("fx-one",
			peLeg(usdB.ID, ledger.Debit, 10), peLeg(usdA.ID, ledger.Credit, 10),
			peLeg(eurB.ID, ledger.Debit, 7), peLeg(eurIssuer.ID, ledger.Credit, 6)), ledger.ErrUnbalanced},
		{"declared currency on the wrong leg", ledger.PostInput{IdempotencyKey: "fx-declared", Postings: []ledger.Posting{
			{AccountID: eurB.ID, Side: ledger.Debit, Amount: amt(1), Currency: "USD"},
			{AccountID: eurIssuer.ID, Side: ledger.Credit, Amount: amt(1), Currency: "EUR"},
		}}, ledger.ErrInvalid},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := e.m.Post(ctx, tt.in)
			wantErr(t, err, tt.want)
		})
	}

	t.Run("declared currencies matching every leg", func(t *testing.T) {
		e.post(t, ledger.PostInput{IdempotencyKey: "fx-ok", Postings: []ledger.Posting{
			{AccountID: eurB.ID, Side: ledger.Debit, Amount: amt(1), Currency: "EUR"},
			{AccountID: eurIssuer.ID, Side: ledger.Credit, Amount: amt(1), Currency: "EUR"},
		}})
	})

	if e.balance(t, usdA.ID) != 90 || e.balance(t, usdB.ID) != 10 || e.balance(t, eurB.ID) != 8 {
		t.Fatal("rejected currency transactions moved money")
	}
	e.verify(t)
}

func TestPostingEdgeCrossLedger(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	a := e.funded(t, 100)
	b := e.account(t, "USD", ledger.Debit)
	other, err := e.m.CreateLedger(ctx, ledger.CreateLedgerInput{Name: "other"})
	if err != nil {
		t.Fatal(err)
	}
	x := e.accountIn(t, other.ID)

	tests := []struct {
		name string
		in   ledger.PostInput
	}{
		{"foreign credit leg", transfer("cross-1", x.ID, b.ID, 1)},
		{"foreign debit leg", transfer("cross-2", a.ID, x.ID, 1)},
		{"foreign third leg", peLegs("cross-3", peLeg(b.ID, ledger.Debit, 1), peLeg(a.ID, ledger.Credit, 2), peLeg(x.ID, ledger.Debit, 1))},
		{"foreign leg on a pending transaction", pending(transfer("cross-4", a.ID, x.ID, 1))},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := e.m.Post(ctx, tt.in)
			wantErr(t, err, ledger.ErrCrossLedger)
		})
	}
	if e.balance(t, a.ID) != 100 || e.balance(t, x.ID) != 0 || e.get(t, a.ID).Available.Amount != amt(100) {
		t.Fatal("cross-ledger rejection moved money")
	}
	e.verify(t)
}

func TestPostingEdgeEffectiveAt(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	a := e.funded(t, 1_000)
	b := e.account(t, "USD", ledger.Debit)
	india := time.FixedZone("IST", 5*3600+1800)

	valid := []struct {
		name string
		at   time.Time
	}{
		{"year one", time.Date(1, 1, 1, 0, 0, 0, 0, time.UTC)},
		{"last microsecond of year 9999", time.Date(9999, 12, 31, 23, 59, 59, 999_999_999, time.UTC)},
		{"unix epoch", time.Unix(0, 0).UTC()},
		{"nanoseconds truncated in a non-utc zone", time.Date(2026, 1, 2, 3, 4, 5, 123_456_789, india)},
		{"far future", time.Date(2999, 6, 1, 12, 0, 0, 1, time.UTC)},
	}
	for i, tt := range valid {
		t.Run(tt.name, func(t *testing.T) {
			in := transfer(fmt.Sprintf("at-%d", i), a.ID, b.ID, 1)
			at := tt.at
			in.EffectiveAt = &at
			want := at.Truncate(time.Microsecond)
			txn := e.post(t, in)
			if !txn.EffectiveAt.Equal(want) {
				t.Fatalf("returned effective_at = %s, want %s", txn.EffectiveAt, want)
			}
			stored, err := e.m.Transaction(ctx, txn.ID)
			if err != nil || !stored.EffectiveAt.Equal(want) {
				t.Fatalf("stored effective_at = %s, %v; want %s", stored.EffectiveAt, err, want)
			}
		})
	}

	t.Run("omitted effective_at uses the commit time", func(t *testing.T) {
		txn := e.post(t, transfer("at-now", a.ID, b.ID, 1))
		if !txn.EffectiveAt.Equal(txn.CreatedAt) || txn.PostedAt == nil || !txn.PostedAt.Equal(txn.CreatedAt) {
			t.Fatalf("effective %s created %s posted %v", txn.EffectiveAt, txn.CreatedAt, txn.PostedAt)
		}
	})

	for _, at := range []time.Time{
		time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC),
		time.Date(-1, 1, 1, 0, 0, 0, 0, time.UTC),
	} {
		t.Run(fmt.Sprintf("unrepresentable year %d is rejected as invalid", at.Year()), func(t *testing.T) {
			key := fmt.Sprintf("at-year-%d", at.Year())
			in := transfer(key, a.ID, b.ID, 1)
			in.EffectiveAt = &at
			_, err := e.m.Post(ctx, in)
			if !errors.Is(err, ledger.ErrInvalid) {
				t.Errorf("error = %v, want ErrInvalid", err)
			}
			if peKeyExists(t, e, key) {
				t.Fatal("rejected transaction was written")
			}
		})
	}

	if got := e.balance(t, b.ID); got != int64(len(valid))+1 {
		t.Fatalf("b = %d, want %d", got, len(valid)+1)
	}
	e.verify(t)
}

func TestPostingEdgeAccountStatus(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	src := e.funded(t, 1_000)
	dst := e.account(t, "USD", ledger.Debit)
	frozen := e.funded(t, 100)
	if _, err := e.m.FreezeAccount(ctx, frozen.ID); err != nil {
		t.Fatal(err)
	}
	closed := e.account(t, "USD", ledger.Debit)
	if _, err := e.m.CloseAccount(ctx, closed.ID); err != nil {
		t.Fatal(err)
	}

	for _, target := range []struct {
		name string
		id   uuid.UUID
	}{{"frozen", frozen.ID}, {"closed", closed.ID}} {
		for _, status := range []ledger.TransactionStatus{ledger.TransactionPosted, ledger.TransactionPending} {
			cases := map[string]ledger.PostInput{
				"sender":   transfer(fmt.Sprintf("%s-out-%s", target.name, status), target.id, dst.ID, 1),
				"receiver": transfer(fmt.Sprintf("%s-in-%s", target.name, status), src.ID, target.id, 1),
				"self":     peLegs(fmt.Sprintf("%s-self-%s", target.name, status), peLeg(target.id, ledger.Debit, 1), peLeg(target.id, ledger.Credit, 1)),
			}
			for role, in := range cases {
				t.Run(fmt.Sprintf("%s %s %s", target.name, role, status), func(t *testing.T) {
					in.Status = status
					_, err := e.m.Post(ctx, in)
					wantErr(t, err, ledger.ErrAccountNotOpen)
				})
			}
		}
	}

	if got := peViews(t, e, src.ID); got != [3]money.Amount{amt(1_000), amt(1_000), amt(1_000)} {
		t.Fatalf("src views = %v", got)
	}
	if got := peViews(t, e, frozen.ID); got != [3]money.Amount{amt(100), amt(100), amt(100)} {
		t.Fatalf("frozen views = %v", got)
	}
	if acc := e.get(t, dst.ID); !acc.Pending.Amount.IsZero() || !acc.Posted.Amount.IsZero() {
		t.Fatalf("dst = %+v", acc)
	}

	t.Run("unfrozen account moves money again", func(t *testing.T) {
		if _, err := e.m.UnfreezeAccount(ctx, frozen.ID); err != nil {
			t.Fatal(err)
		}
		e.post(t, transfer("thawed", frozen.ID, dst.ID, 100))
		if e.balance(t, frozen.ID) != 0 || e.balance(t, dst.ID) != 100 {
			t.Fatal("thawed transfer did not apply")
		}
	})
	e.verify(t)
}

func TestPostingEdgeOverdraft(t *testing.T) {
	e := setup(t)
	ctx := context.Background()

	tests := []struct {
		name       string
		side       ledger.Side
		limit      int64
		fund       int64
		pendingOut int64
		spend      int64
		want       error
	}{
		{"debit normal exactly at limit", ledger.Debit, 50, 0, 0, 50, nil},
		{"debit normal one past limit", ledger.Debit, 50, 0, 0, 51, ledger.ErrInsufficientFunds},
		{"funded plus limit exactly", ledger.Debit, 50, 100, 0, 150, nil},
		{"funded plus limit one past", ledger.Debit, 50, 100, 0, 151, ledger.ErrInsufficientFunds},
		{"pending reservation counts against limit", ledger.Debit, 50, 0, 30, 20, nil},
		{"pending reservation one past limit", ledger.Debit, 50, 0, 30, 21, ledger.ErrInsufficientFunds},
		{"credit normal exactly at limit", ledger.Credit, 50, 0, 0, 50, nil},
		{"credit normal one past limit", ledger.Credit, 50, 0, 0, 51, ledger.ErrInsufficientFunds},
		{"credit normal funded plus limit", ledger.Credit, 50, 100, 0, 150, nil},
		{"credit normal pending one past limit", ledger.Credit, 50, 10, 60, 1, ledger.ErrInsufficientFunds},
		{"zero limit exact balance", ledger.Debit, 0, 10, 0, 10, nil},
		{"zero limit one past", ledger.Debit, 0, 10, 0, 11, ledger.ErrInsufficientFunds},
	}
	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			acc := e.account(t, "USD", tt.side, func(in *ledger.CreateAccountInput) { in.OverdraftLimit = amt(tt.limit) })
			in := func(key string, n int64) ledger.PostInput {
				return peLegs(key, peLeg(acc.ID, tt.side, n), peLeg(e.open.ID, peOpposite(tt.side), n))
			}
			out := func(key string, n int64) ledger.PostInput {
				return peLegs(key, peLeg(acc.ID, peOpposite(tt.side), n), peLeg(e.open.ID, tt.side, n))
			}
			if tt.fund > 0 {
				e.post(t, in(fmt.Sprintf("od-%d-fund", i), tt.fund))
			}
			if tt.pendingOut > 0 {
				e.post(t, pending(out(fmt.Sprintf("od-%d-pending", i), tt.pendingOut)))
			}
			before := peViews(t, e, acc.ID)
			_, err := e.m.Post(ctx, out(fmt.Sprintf("od-%d-spend", i), tt.spend))
			if tt.want != nil {
				wantErr(t, err, tt.want)
				if got := peViews(t, e, acc.ID); got != before {
					t.Fatalf("views = %v after rejection, want %v", got, before)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			available := e.get(t, acc.ID).Available.Amount
			if want := amt(tt.fund - tt.pendingOut - tt.spend); available != want {
				t.Fatalf("available = %s, want %s", available, want)
			}
			if available != amt(-tt.limit) {
				return
			}
			_, err = e.m.Post(ctx, out(fmt.Sprintf("od-%d-extra", i), 1))
			wantErr(t, err, ledger.ErrInsufficientFunds)
			e.post(t, in(fmt.Sprintf("od-%d-repay", i), 1))
			e.post(t, out(fmt.Sprintf("od-%d-respend", i), 1))
		})
	}

	t.Run("allow_negative ignores the limit", func(t *testing.T) {
		acc := e.account(t, "USD", ledger.Debit, unrestricted)
		sink := e.account(t, "USD", ledger.Debit)
		e.post(t, transfer("unlimited", acc.ID, sink.ID, 1_000_000_000_000))
		if got := e.balance(t, acc.ID); got != -1_000_000_000_000 {
			t.Fatalf("acc = %d", got)
		}
	})
	e.verify(t)
}

func TestPostingEdgeNormalSideMath(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	debit := e.account(t, "USD", ledger.Debit)
	credit := e.account(t, "USD", ledger.Credit)

	t.Run("non-negative accounts reject movement against their normal side at zero", func(t *testing.T) {
		_, err := e.m.Post(ctx, peLegs("d-wrong", peLeg(debit.ID, ledger.Credit, 1), peLeg(e.open.ID, ledger.Debit, 1)))
		wantErr(t, err, ledger.ErrInsufficientFunds)
		_, err = e.m.Post(ctx, peLegs("c-wrong", peLeg(credit.ID, ledger.Debit, 1), peLeg(e.open.ID, ledger.Credit, 1)))
		wantErr(t, err, ledger.ErrInsufficientFunds)
	})

	grow := e.post(t, peLegs("grow", peLeg(debit.ID, ledger.Debit, 100), peLeg(credit.ID, ledger.Credit, 100)))
	if r := grow.Postings[1].Resulting; r == nil || r.Posted != (ledger.Balance{Debits: amt(0), Credits: amt(100), Amount: amt(100)}) {
		t.Fatalf("credit resulting = %+v", r)
	}
	e.post(t, peLegs("shrink", peLeg(credit.ID, ledger.Debit, 30), peLeg(debit.ID, ledger.Credit, 30)))
	e.post(t, pending(peLegs("hold", peLeg(credit.ID, ledger.Debit, 20), peLeg(debit.ID, ledger.Credit, 20))))

	d, c := e.get(t, debit.ID), e.get(t, credit.ID)
	wantBalance(t, "debit posted", d.Posted, 100, 30, 70)
	wantBalance(t, "debit pending", d.Pending, 100, 50, 50)
	wantBalance(t, "debit available", d.Available, 100, 50, 50)
	wantBalance(t, "credit posted", c.Posted, 30, 100, 70)
	wantBalance(t, "credit pending", c.Pending, 50, 100, 50)
	wantBalance(t, "credit available", c.Available, 50, 100, 50)

	for name, id := range map[string]uuid.UUID{"debit": debit.ID, "credit": credit.ID} {
		lines, err := e.m.AccountEntries(ctx, id, 0, 10)
		if err != nil || len(lines) != 2 || lines[0].BalanceAfter != amt(100) || lines[1].BalanceAfter != amt(70) {
			t.Fatalf("%s statement = %+v, %v", name, lines, err)
		}
	}
	e.verify(t)
}
