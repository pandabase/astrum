package money

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math/big"
	"math/bits"
	"strconv"

	"github.com/jackc/pgx/v5/pgtype"
)

const MaxDigits = 38

var ErrInvalidAmount = errors.New("money: invalid amount")

const pow19 = 10_000_000_000_000_000_000

var maxAmount = func() Amount {
	hi, lo := bits.Mul64(pow19, pow19)
	lo, borrow := bits.Sub64(lo, 1, 0)
	return Amount{hi: hi - borrow, lo: lo}
}()

func MaxAmount() Amount {
	return maxAmount
}

type Amount struct {
	hi, lo uint64
}

func NewAmount(n int64) Amount {
	return Amount{hi: uint64(n >> 63), lo: uint64(n)}
}

func ParseAmount(s string) (Amount, error) {
	digits, negative := s, false
	if len(digits) > 0 && digits[0] == '-' {
		digits, negative = digits[1:], true
	}
	if len(digits) == 0 || len(digits) > MaxDigits || (digits[0] == '0' && (len(digits) > 1 || negative)) {
		return Amount{}, fmt.Errorf("%w: %q", ErrInvalidAmount, s)
	}
	for i := range len(digits) {
		if digits[i] < '0' || digits[i] > '9' {
			return Amount{}, fmt.Errorf("%w: %q", ErrInvalidAmount, s)
		}
	}

	split := max(len(digits)-19, 0)
	var high, low uint64
	if split > 0 {
		high, _ = strconv.ParseUint(digits[:split], 10, 64)
	}
	low, _ = strconv.ParseUint(digits[split:], 10, 64)
	hi, lo := bits.Mul64(high, pow19)
	lo, carry := bits.Add64(lo, low, 0)
	a := Amount{hi: hi + carry, lo: lo}
	if negative {
		a = a.Neg()
	}
	return a, nil
}

func MustParseAmount(s string) Amount {
	a, err := ParseAmount(s)
	if err != nil {
		panic(err)
	}
	return a
}

func (a Amount) Sign() int {
	switch {
	case int64(a.hi) < 0:
		return -1
	case a.hi == 0 && a.lo == 0:
		return 0
	}
	return 1
}

func (a Amount) IsZero() bool {
	return a == Amount{}
}

func (a Amount) Cmp(b Amount) int {
	switch {
	case int64(a.hi) < int64(b.hi):
		return -1
	case int64(a.hi) > int64(b.hi):
		return 1
	case a.lo < b.lo:
		return -1
	case a.lo > b.lo:
		return 1
	}
	return 0
}

func (a Amount) Neg() Amount {
	lo, borrow := bits.Sub64(0, a.lo, 0)
	hi, _ := bits.Sub64(0, a.hi, borrow)
	return Amount{hi: hi, lo: lo}
}

func (a Amount) Abs() Amount {
	if a.Sign() < 0 {
		return a.Neg()
	}
	return a
}

func (a Amount) Add(b Amount) (Amount, error) {
	lo, carry := bits.Add64(a.lo, b.lo, 0)
	hi, _ := bits.Add64(a.hi, b.hi, carry)
	return checked(a, b, Amount{hi: hi, lo: lo})
}

func (a Amount) Sub(b Amount) (Amount, error) {
	return a.Add(b.Neg())
}

func checked(a, b, sum Amount) (Amount, error) {
	wrapped := a.Sign() == b.Sign() && a.Sign() != 0 && sum.Sign() != a.Sign()
	if wrapped || sum.Abs().Cmp(maxAmount) > 0 {
		return Amount{}, ErrOverflow
	}
	return sum, nil
}

func (a Amount) Int64() (int64, bool) {
	n := int64(a.lo)
	return n, a.hi == uint64(n>>63)
}

func (a Amount) String() string {
	if n, ok := a.Int64(); ok {
		return strconv.FormatInt(n, 10)
	}
	sign := ""
	if a.Sign() < 0 {
		sign, a = "-", a.Neg()
	}

	high, low := bits.Div64(a.hi, a.lo, pow19)
	lowDigits := strconv.FormatUint(low, 10)
	if high == 0 {
		return sign + lowDigits
	}
	return sign + strconv.FormatUint(high, 10) + "0000000000000000000"[len(lowDigits):] + lowDigits
}

func (a Amount) Big() *big.Int {
	if n, ok := a.Int64(); ok {
		return big.NewInt(n)
	}
	abs := a.Abs()
	b := new(big.Int).SetUint64(abs.hi)
	b.Lsh(b, 64).Or(b, new(big.Int).SetUint64(abs.lo))
	if a.Sign() < 0 {
		b.Neg(b)
	}
	return b
}

func AmountFromBig(b *big.Int) (Amount, error) {
	if b.IsInt64() {
		return NewAmount(b.Int64()), nil
	}
	if b.CmpAbs(maxAmount.Big()) > 0 {
		return Amount{}, ErrOverflow
	}
	abs := new(big.Int).Abs(b)
	lo := abs.Uint64()
	a := Amount{hi: abs.Rsh(abs, 64).Uint64(), lo: lo}
	if b.Sign() < 0 {
		a = a.Neg()
	}
	return a, nil
}

func (a Amount) AppendBinary(b []byte) ([]byte, error) {
	return binary.BigEndian.AppendUint64(binary.BigEndian.AppendUint64(b, a.hi), a.lo), nil
}

func (a Amount) MarshalText() ([]byte, error) {
	return []byte(a.String()), nil
}

func (a *Amount) UnmarshalText(text []byte) error {
	parsed, err := ParseAmount(string(text))
	if err != nil {
		return err
	}
	*a = parsed
	return nil
}

func (a Amount) NumericValue() (pgtype.Numeric, error) {
	return pgtype.Numeric{Int: a.Big(), Valid: true}, nil
}

func (a *Amount) ScanNumeric(n pgtype.Numeric) error {
	if !n.Valid || n.NaN || n.InfinityModifier != pgtype.Finite {
		return fmt.Errorf("%w: cannot scan %v", ErrInvalidAmount, n)
	}

	switch {
	case n.Int.Sign() == 0:
		*a = Amount{}
		return nil
	case n.Exp > MaxDigits:
		return ErrOverflow
	case -int64(n.Exp) > int64(n.Int.BitLen()):
		return fmt.Errorf("%w: %v has a fractional part", ErrInvalidAmount, n)
	}

	value := new(big.Int).Set(n.Int)

	if n.Exp != 0 {
		scale := new(big.Int).Exp(big.NewInt(10), big.NewInt(abs32(n.Exp)), nil)
		if n.Exp > 0 {
			value.Mul(value, scale)
		} else if _, rem := value.QuoRem(value, scale, new(big.Int)); rem.Sign() != 0 {
			return fmt.Errorf("%w: %v has a fractional part", ErrInvalidAmount, n)
		}
	}

	parsed, err := AmountFromBig(value)
	if err != nil {
		return err
	}
	*a = parsed

	return nil
}

func abs32(n int32) int64 {
	if n < 0 {
		return -int64(n)
	}

	return int64(n)
}
