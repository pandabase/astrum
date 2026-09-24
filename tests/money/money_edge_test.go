package money_test

import (
	"encoding/json/v2"
	"errors"
	"math"
	"math/big"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/pandabase/astrum/internal/money"
)

func TestEdgeParseAmountAccepts(t *testing.T) {
	for _, s := range []string{
		"9999999999999999999",
		"-9999999999999999999",
		"18446744073709551615",
		"18446744073709551616",
		"-18446744073709551616",
		"-9223372036854775809",
		"100000000000000000000000000000000000",
		"1" + strings.Repeat("0", 37),
		"-1" + strings.Repeat("0", 37),
		"12345678901234567890123456789012345678",
		"10000000000000000000000000000000000001",
	} {
		t.Run(s, func(t *testing.T) {
			a, err := money.ParseAmount(s)
			if err != nil {
				t.Fatal(err)
			}
			if a.String() != s || a.Big().String() != s {
				t.Fatalf("round trip = %s / %s", a, a.Big())
			}
			var back money.Amount
			if err := back.UnmarshalText([]byte(s)); err != nil || back != a {
				t.Fatalf("UnmarshalText = %v, %v", back, err)
			}
			fromBig, err := money.AmountFromBig(a.Big())
			if err != nil || fromBig != a {
				t.Fatalf("AmountFromBig = %v, %v", fromBig, err)
			}
		})
	}
}

func TestEdgeParseAmountRejects(t *testing.T) {
	for _, s := range []string{
		"1" + strings.Repeat("0", 38),
		"-1" + strings.Repeat("0", 38),
		"0" + strings.Repeat("9", 38),
		"000",
		"-00",
		"-01",
		"--1",
		"-+1",
		"+-1",
		"+0",
		"1_000",
		"1,000",
		"\t1",
		"1\n",
		"1\x00",
		"１",
		"1E3",
		"Infinity",
		"NaN",
		"-",
		"- 1",
		"0.",
		".5",
	} {
		t.Run(strconv.Quote(s), func(t *testing.T) {
			a, err := money.ParseAmount(s)
			if !errors.Is(err, money.ErrInvalidAmount) {
				t.Fatalf("ParseAmount(%q) = %v, %v", s, a, err)
			}
			if !a.IsZero() {
				t.Fatalf("ParseAmount(%q) returned %v on error", s, a)
			}
			if !strings.Contains(err.Error(), strconv.Quote(s)) {
				t.Fatalf("error %q does not quote the input", err)
			}
		})
	}
}

func TestEdgeNewAmountInt64(t *testing.T) {
	for _, n := range []int64{0, 1, -1, math.MaxInt64, math.MinInt64, math.MinInt64 + 1} {
		a := money.NewAmount(n)
		got, ok := a.Int64()
		if !ok || got != n || a.String() != strconv.FormatInt(n, 10) {
			t.Fatalf("NewAmount(%d) = %s, Int64 = %d %v", n, a, got, ok)
		}
	}
	for _, s := range []string{"9223372036854775808", "-9223372036854775809", "18446744073709551616"} {
		if _, ok := money.MustParseAmount(s).Int64(); ok {
			t.Fatalf("%s fits in int64", s)
		}
	}
}

func TestEdgeZeroSign(t *testing.T) {
	zero := money.NewAmount(0)
	if zero.Neg() != zero || zero.Abs() != zero || zero.Sign() != 0 || !zero.IsZero() || zero.String() != "0" {
		t.Fatalf("zero = %v", zero)
	}
	var unset money.Amount
	if unset != zero || unset.String() != "0" {
		t.Fatalf("zero value = %s", unset)
	}
	diff, err := money.MustParseAmount("5").Sub(money.MustParseAmount("5"))
	if err != nil || diff != zero || diff.Sign() != 0 {
		t.Fatalf("5 - 5 = %v, %v", diff, err)
	}
}

func TestEdgeArithmeticBoundaries(t *testing.T) {
	maxA := money.MaxAmount()
	minA := maxA.Neg()
	one := money.NewAmount(1)
	tests := []struct {
		name    string
		got     func() (money.Amount, error)
		want    string
		wantErr error
	}{
		{"max minus one", func() (money.Amount, error) { return maxA.Sub(one) }, strings.Repeat("9", 37) + "8", nil},
		{"max plus zero", func() (money.Amount, error) { return maxA.Add(money.Amount{}) }, max38, nil},
		{"max plus min", func() (money.Amount, error) { return maxA.Add(minA) }, "0", nil},
		{"max minus max", func() (money.Amount, error) { return maxA.Sub(maxA) }, "0", nil},
		{"min minus min", func() (money.Amount, error) { return minA.Sub(minA) }, "0", nil},
		{"min plus one", func() (money.Amount, error) { return minA.Add(one) }, "-" + strings.Repeat("9", 37) + "8", nil},
		{"max plus one", func() (money.Amount, error) { return maxA.Add(one) }, "", money.ErrOverflow},
		{"min minus one", func() (money.Amount, error) { return minA.Sub(one) }, "", money.ErrOverflow},
		{"max minus min", func() (money.Amount, error) { return maxA.Sub(minA) }, "", money.ErrOverflow},
		{"min minus max", func() (money.Amount, error) { return minA.Sub(maxA) }, "", money.ErrOverflow},
		{"int64 max plus one", func() (money.Amount, error) { return money.NewAmount(math.MaxInt64).Add(one) }, "9223372036854775808", nil},
		{"int64 min minus one", func() (money.Amount, error) { return money.NewAmount(math.MinInt64).Sub(one) }, "-9223372036854775809", nil},
		{"uint64 carry", func() (money.Amount, error) {
			return money.MustParseAmount("18446744073709551615").Add(one)
		}, "18446744073709551616", nil},
		{"uint64 borrow", func() (money.Amount, error) {
			return money.MustParseAmount("18446744073709551616").Sub(one)
		}, "18446744073709551615", nil},
		{"negative crossing zero", func() (money.Amount, error) {
			return money.MustParseAmount("-18446744073709551616").Add(money.MustParseAmount("18446744073709551617"))
		}, "1", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.got()
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			if tt.wantErr != nil {
				if !got.IsZero() {
					t.Fatalf("overflow returned %s", got)
				}
				return
			}
			if got.String() != tt.want {
				t.Fatalf("= %s, want %s", got, tt.want)
			}
		})
	}
}

func TestEdgeCompareAcrossWords(t *testing.T) {
	ordered := []string{"-" + max38, "-18446744073709551616", "-18446744073709551615", "-1", "0", "1", "18446744073709551615", "18446744073709551616", max38}
	for i := range ordered {
		for j := range ordered {
			a, b := money.MustParseAmount(ordered[i]), money.MustParseAmount(ordered[j])
			want := 0
			if i < j {
				want = -1
			} else if i > j {
				want = 1
			}
			if got := a.Cmp(b); got != want {
				t.Fatalf("Cmp(%s, %s) = %d, want %d", ordered[i], ordered[j], got, want)
			}
		}
	}
}

func TestEdgeAmountFromBig(t *testing.T) {
	over := new(big.Int).Add(big38, big.NewInt(1))
	tests := []struct {
		name string
		in   *big.Int
		want string
		err  error
	}{
		{"zero", new(big.Int), "0", nil},
		{"max", big38, max38, nil},
		{"min", new(big.Int).Neg(big38), "-" + max38, nil},
		{"max plus one", over, "", money.ErrOverflow},
		{"min minus one", new(big.Int).Neg(over), "", money.ErrOverflow},
		{"two to 127", new(big.Int).Lsh(big.NewInt(1), 127), "", money.ErrOverflow},
		{"two to 64", new(big.Int).Lsh(big.NewInt(1), 64), "18446744073709551616", nil},
		{"minus two to 64", new(big.Int).Neg(new(big.Int).Lsh(big.NewInt(1), 64)), "-18446744073709551616", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before := new(big.Int).Set(tt.in)
			got, err := money.AmountFromBig(tt.in)
			if !errors.Is(err, tt.err) || (tt.err == nil && got.String() != tt.want) {
				t.Fatalf("AmountFromBig(%s) = %s, %v", tt.in, got, err)
			}
			if tt.in.Cmp(before) != 0 {
				t.Fatalf("AmountFromBig mutated its input to %s", tt.in)
			}
		})
	}
}

func TestEdgeMustParseAmountPanics(t *testing.T) {
	defer func() {
		r := recover()
		err, ok := r.(error)
		if !ok || !errors.Is(err, money.ErrInvalidAmount) {
			t.Fatalf("recover() = %v", r)
		}
	}()
	money.MustParseAmount("1.5")
}

func TestEdgeUnmarshalTextKeepsValueOnError(t *testing.T) {
	a := money.NewAmount(42)
	if err := a.UnmarshalText([]byte("nope")); err == nil {
		t.Fatal("accepted")
	}
	if a != money.NewAmount(42) {
		t.Fatalf("value changed to %s", a)
	}
}

func TestEdgeAmountJSON(t *testing.T) {
	var v struct {
		A money.Amount `json:"a"`
	}
	for _, raw := range []string{`{"a":123}`, `{"a":"1.0"}`, `{"a":"-0"}`, `{"a":"` + max38 + `9"}`, `{"a":true}`, `{"a":" 1"}`} {
		if err := json.Unmarshal([]byte(raw), &v); err == nil {
			t.Fatalf("Unmarshal(%s) accepted as %s", raw, v.A)
		}
	}
	if err := json.Unmarshal([]byte(`{"a":"-`+max38+`"}`), &v); err != nil || v.A.String() != "-"+max38 {
		t.Fatalf("Unmarshal(min) = %s, %v", v.A, err)
	}
	raw, err := json.Marshal(v)
	if err != nil || string(raw) != `{"a":"-`+max38+`"}` {
		t.Fatalf("Marshal = %s, %v", raw, err)
	}
}

func TestEdgeAppendBinary(t *testing.T) {
	tests := []struct {
		in   string
		want []byte
	}{
		{"0", make([]byte, 16)},
		{"1", append(make([]byte, 15), 1)},
		{"-1", []byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff}},
		{"18446744073709551616", append(append(make([]byte, 7), 1), make([]byte, 8)...)},
	}
	for _, tt := range tests {
		got, err := money.MustParseAmount(tt.in).AppendBinary([]byte{0xaa})
		if err != nil || len(got) != 17 || got[0] != 0xaa || string(got[1:]) != string(tt.want) {
			t.Fatalf("AppendBinary(%s) = %x, %v", tt.in, got, err)
		}
	}
}

func TestEdgeScanNumeric(t *testing.T) {
	pow := func(n int64) *big.Int { return new(big.Int).Exp(big.NewInt(10), big.NewInt(n), nil) }
	tests := []struct {
		name string
		in   pgtype.Numeric
		want string
		err  error
	}{
		{"trailing zeros scale", pgtype.Numeric{Int: big.NewInt(100), Exp: -2, Valid: true}, "1", nil},
		{"negative trailing zeros", pgtype.Numeric{Int: big.NewInt(-500), Exp: -2, Valid: true}, "-5", nil},
		{"negative fraction", pgtype.Numeric{Int: big.NewInt(-501), Exp: -2, Valid: true}, "", money.ErrInvalidAmount},
		{"one tenth", pgtype.Numeric{Int: big.NewInt(1), Exp: -1, Valid: true}, "", money.ErrInvalidAmount},
		{"ten to 37 as exponent", pgtype.Numeric{Int: big.NewInt(1), Exp: 37, Valid: true}, "1" + strings.Repeat("0", 37), nil},
		{"max via exponent", pgtype.Numeric{Int: new(big.Int).Set(big38), Exp: 0, Valid: true}, max38, nil},
		{"max scaled down", pgtype.Numeric{Int: new(big.Int).Mul(big38, pow(2)), Exp: -2, Valid: true}, max38, nil},
		{"overflow scaled down", pgtype.Numeric{Int: pow(40), Exp: -2, Valid: true}, "", money.ErrOverflow},
		{"exponent 39", pgtype.Numeric{Int: big.NewInt(1), Exp: 39, Valid: true}, "", money.ErrOverflow},
		{"zero negative exponent", pgtype.Numeric{Int: big.NewInt(0), Exp: math.MinInt32, Valid: true}, "0", nil},
		{"negative infinity", pgtype.Numeric{InfinityModifier: pgtype.NegativeInfinity, Valid: true}, "", money.ErrInvalidAmount},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := money.NewAmount(7)
			err := a.ScanNumeric(tt.in)
			if !errors.Is(err, tt.err) {
				t.Fatalf("ScanNumeric(%v) = %v, want %v", tt.in, err, tt.err)
			}
			if tt.err != nil {
				if a != money.NewAmount(7) {
					t.Fatalf("ScanNumeric changed the value to %s on error", a)
				}
				return
			}
			if a.String() != tt.want {
				t.Fatalf("ScanNumeric = %s, want %s", a, tt.want)
			}
			n, err := a.NumericValue()
			if err != nil || !n.Valid || n.Int.String() != tt.want || n.Exp != 0 {
				t.Fatalf("NumericValue = %+v, %v", n, err)
			}
		})
	}
}

func TestEdgeCurrencyBoundaries(t *testing.T) {
	tests := []struct {
		c  money.Currency
		ok bool
	}{
		{"USD", true},
		{"US", false},
		{"", false},
		{"ABCDEFGHIJKLMNOP", true},
		{"ABCDEFGHIJKLMNOPQ", false},
		{"A_1", true},
		{"A__", true},
		{"_AB", false},
		{"1AB", false},
		{"usd", false},
		{"USd", false},
		{"US D", false},
		{"US-D", false},
		{"ÜSD", false},
		{"USD\x00", false},
	}
	for _, tt := range tests {
		t.Run(string(tt.c), func(t *testing.T) {
			err := tt.c.Validate()
			if tt.ok != (err == nil) || (err != nil && !errors.Is(err, money.ErrInvalidCurrency)) {
				t.Fatalf("Validate(%q) = %v, want ok=%v", tt.c, err, tt.ok)
			}
		})
	}
}
