package money_test

import (
	"encoding/json/v2"
	"errors"
	"math"
	"math/big"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/pandabase/astrum/internal/money"
)

var (
	max38 = strings.Repeat("9", 38)
	big38 = func() *big.Int { b, _ := new(big.Int).SetString(max38, 10); return b }()
)

func TestCurrencyValidate(t *testing.T) {
	tests := []struct {
		currency money.Currency
		wantErr  bool
	}{
		{"USD", false},
		{"JPY", false},
		{"USDC", false},
		{"LOYALTY_POINTS", false},
		{"ABCDEFGHIJKLMNOP", false},
		{"", true},
		{"US", true},
		{"ABCDEFGHIJKLMNOPQ", true},
		{"usd", true},
		{"1BTC", true},
		{"_USD", true},
		{"U$D", true},
		{"US D", true},
		{"ÜSD", true},
	}
	for _, tt := range tests {
		t.Run(string(tt.currency), func(t *testing.T) {
			err := tt.currency.Validate()
			if (err != nil) != tt.wantErr {
				t.Fatalf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
			if err != nil && !errors.Is(err, money.ErrInvalidCurrency) {
				t.Fatalf("Validate() error = %v, want ErrInvalidCurrency", err)
			}
		})
	}
}

func TestParseAmount(t *testing.T) {
	valid := []string{"0", "1", "-1", "100", "9223372036854775807", "-9223372036854775808", "9223372036854775808",
		"10000000000000000000", "1000000000000000000000000000000000000", max38, "-" + max38}
	for _, s := range valid {
		t.Run(s, func(t *testing.T) {
			a, err := money.ParseAmount(s)
			if err != nil {
				t.Fatal(err)
			}
			if a.String() != s {
				t.Fatalf("String() = %s, want %s", a, s)
			}
			if a.Big().String() != s {
				t.Fatalf("Big() = %s, want %s", a.Big(), s)
			}
		})
	}

	invalid := []string{"", "-", "-0", "00", "01", "+1", " 1", "1 ", "1.0", "1e3", "0x10", "١", max38 + "9", "-1" + max38}
	for _, s := range invalid {
		t.Run("reject "+s, func(t *testing.T) {
			if _, err := money.ParseAmount(s); !errors.Is(err, money.ErrInvalidAmount) {
				t.Fatalf("ParseAmount(%q) error = %v, want ErrInvalidAmount", s, err)
			}
		})
	}
}

func TestAmountArithmetic(t *testing.T) {
	m := money.MustParseAmount
	tests := []struct {
		name    string
		a, b    string
		sum     string
		diff    string
		sumErr  error
		diffErr error
	}{
		{"zero", "0", "0", "0", "0", nil, nil},
		{"small", "100", "-250", "-150", "350", nil, nil},
		{"crosses int64", "9223372036854775807", "1", "9223372036854775808", "9223372036854775806", nil, nil},
		{"crosses 64 bits", "18446744073709551615", "1", "18446744073709551616", "18446744073709551614", nil, nil},
		{"at max", max38, "0", max38, max38, nil, nil},
		{"past max", max38, "1", "", strings.Repeat("9", 37) + "8", money.ErrOverflow, nil},
		{"past min", "-" + max38, "1", "-" + strings.Repeat("9", 37) + "8", "", nil, money.ErrOverflow},
		{"max plus max", max38, max38, "", "0", money.ErrOverflow, nil},
		{"min minus max", "-" + max38, max38, "0", "", nil, money.ErrOverflow},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sum, err := m(tt.a).Add(m(tt.b))
			if !errors.Is(err, tt.sumErr) || (err == nil && sum.String() != tt.sum) {
				t.Fatalf("%s + %s = %s, %v; want %s, %v", tt.a, tt.b, sum, err, tt.sum, tt.sumErr)
			}
			diff, err := m(tt.a).Sub(m(tt.b))
			if !errors.Is(err, tt.diffErr) || (err == nil && diff.String() != tt.diff) {
				t.Fatalf("%s - %s = %s, %v; want %s, %v", tt.a, tt.b, diff, err, tt.diff, tt.diffErr)
			}
		})
	}
}

func TestAmountCompare(t *testing.T) {
	ordered := []string{"-" + max38, "-18446744073709551616", "-9223372036854775809", "-1", "0", "1",
		"9223372036854775808", "18446744073709551616", max38}
	for i, a := range ordered {
		for j, b := range ordered {
			want := 0
			if i < j {
				want = -1
			} else if i > j {
				want = 1
			}
			if got := money.MustParseAmount(a).Cmp(money.MustParseAmount(b)); got != want {
				t.Fatalf("Cmp(%s, %s) = %d, want %d", a, b, got, want)
			}
		}
	}
	if money.MustParseAmount("12") != money.NewAmount(12) {
		t.Fatal("equal amounts are not ==")
	}
}

func TestAmountJSON(t *testing.T) {
	type doc struct {
		Amount money.Amount `json:"amount"`
	}
	out, err := json.Marshal(doc{money.MustParseAmount(max38)})
	if err != nil || string(out) != `{"amount":"`+max38+`"}` {
		t.Fatalf("Marshal = %s, %v", out, err)
	}
	var d doc
	if err := json.Unmarshal(out, &d); err != nil || d.Amount.String() != max38 {
		t.Fatalf("Unmarshal = %s, %v", d.Amount, err)
	}
	for _, bad := range []string{`{"amount":100}`, `{"amount":"1.5"}`, `{"amount":null}`, `{"amount":"` + max38 + `0"}`} {
		d := doc{Amount: money.NewAmount(7)}
		err := json.Unmarshal([]byte(bad), &d)
		if bad == `{"amount":null}` {
			if err != nil || !d.Amount.IsZero() {
				t.Fatalf("Unmarshal(%s) = %s, %v", bad, d.Amount, err)
			}
			continue
		}
		if err == nil {
			t.Fatalf("Unmarshal(%s) accepted %s", bad, d.Amount)
		}
	}
}

func TestAmountNumeric(t *testing.T) {
	tests := []struct {
		name    string
		in      pgtype.Numeric
		want    string
		wantErr bool
	}{
		{"plain", pgtype.Numeric{Int: big.NewInt(-42), Valid: true}, "-42", false},
		{"positive exponent", pgtype.Numeric{Int: big.NewInt(12), Exp: 3, Valid: true}, "12000", false},
		{"exact negative exponent", pgtype.Numeric{Int: big.NewInt(1200), Exp: -2, Valid: true}, "12", false},
		{"max", pgtype.Numeric{Int: big38, Valid: true}, max38, false},
		{"fraction", pgtype.Numeric{Int: big.NewInt(1201), Exp: -2, Valid: true}, "", true},
		{"too large", pgtype.Numeric{Int: big.NewInt(1), Exp: 38, Valid: true}, "", true},
		{"huge exponent", pgtype.Numeric{Int: big.NewInt(1), Exp: math.MaxInt32, Valid: true}, "", true},
		{"zero with huge exponent", pgtype.Numeric{Int: big.NewInt(0), Exp: math.MaxInt32, Valid: true}, "0", false},
		{"huge negative exponent", pgtype.Numeric{Int: big.NewInt(5), Exp: math.MinInt32, Valid: true}, "", true},
		{"null", pgtype.Numeric{}, "", true},
		{"nan", pgtype.Numeric{NaN: true, Valid: true}, "", true},
		{"infinity", pgtype.Numeric{InfinityModifier: pgtype.Infinity, Valid: true}, "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var a money.Amount
			err := a.ScanNumeric(tt.in)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ScanNumeric error = %v, wantErr %v", err, tt.wantErr)
			}
			if err == nil && a.String() != tt.want {
				t.Fatalf("ScanNumeric = %s, want %s", a, tt.want)
			}
		})
	}

	n, err := money.MustParseAmount("-" + max38).NumericValue()
	if err != nil || n.Int.String() != "-"+max38 || n.Exp != 0 || !n.Valid {
		t.Fatalf("NumericValue = %+v, %v", n, err)
	}
}

func FuzzAmount(f *testing.F) {
	f.Add("0", "0")
	f.Add(max38, "1")
	f.Add("-"+max38, "-1")
	f.Add("9223372036854775807", "9223372036854775807")
	f.Add("18446744073709551616", "-18446744073709551617")

	f.Fuzz(func(t *testing.T, x, y string) {
		a, errA := money.ParseAmount(x)
		b, errB := money.ParseAmount(y)
		if errA != nil || errB != nil {
			return
		}
		if a.String() != x || b.String() != y {
			t.Fatalf("round trip %q %q -> %s %s", x, y, a, b)
		}
		exactSum := new(big.Int).Add(a.Big(), b.Big())
		exactDiff := new(big.Int).Sub(a.Big(), b.Big())
		check := func(op string, got money.Amount, err error, exact *big.Int) {
			if exact.CmpAbs(big38) > 0 {
				if !errors.Is(err, money.ErrOverflow) {
					t.Fatalf("%s %s %s = %s, want ErrOverflow", x, op, y, got)
				}
				return
			}
			if err != nil || got.Big().Cmp(exact) != 0 {
				t.Fatalf("%s %s %s = %s, %v; want %s", x, op, y, got, err, exact)
			}
		}
		sum, err := a.Add(b)
		check("+", sum, err, exactSum)
		diff, err := a.Sub(b)
		check("-", diff, err, exactDiff)
		if got, want := a.Cmp(b), a.Big().Cmp(b.Big()); got != want {
			t.Fatalf("Cmp(%s, %s) = %d, want %d", x, y, got, want)
		}
		if back, err := money.AmountFromBig(a.Big()); err != nil || back != a {
			t.Fatalf("AmountFromBig(%s) = %s, %v", x, back, err)
		}
	})
}
