package ledger

import (
	"bytes"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/pandabase/astrum/internal/money"
)

var (
	ErrNotFound            = errors.New("ledger: not found")
	ErrAccountExists       = errors.New("ledger: account code already exists with different terms")
	ErrAccountNotOpen      = errors.New("ledger: account is not open")
	ErrAccountNotEmpty     = errors.New("ledger: account must have zero balance and no holds to close")
	ErrCurrencyExists      = errors.New("ledger: currency already exists with a different exponent")
	ErrUnknownCurrency     = errors.New("ledger: currency is not registered")
	ErrUnknownLedger       = errors.New("ledger: ledger does not exist")
	ErrCrossLedger         = errors.New("ledger: transaction spans more than one ledger")
	ErrNotPending          = errors.New("ledger: transaction is not pending")
	ErrNotPosted           = errors.New("ledger: transaction is not posted")
	ErrExternalIDExists    = errors.New("ledger: external_id already belongs to another transaction in this ledger")
	ErrBalanceLock         = errors.New("ledger: balance lock failed")
	ErrLockVersion         = errors.New("ledger: account lock_version has moved")
	ErrCategoryCycle       = errors.New("ledger: category would contain itself")
	ErrCategoryDepth       = errors.New("ledger: categories nest at most 7 levels deep")
	ErrCategoryMismatch    = errors.New("ledger: category members must share its ledger and currency")
	ErrInvalid             = errors.New("ledger: invalid input")
	ErrUnbalanced          = errors.New("ledger: transaction is unbalanced")
	ErrInsufficientFunds   = errors.New("ledger: insufficient funds")
	ErrIdempotencyConflict = errors.New("ledger: idempotency key reused with a different payload")
	ErrAlreadyReversed     = errors.New("ledger: transaction already reversed")
	ErrHoldNotPending      = errors.New("ledger: hold is not pending")
	ErrScheduleNotPending  = errors.New("ledger: scheduled transaction is not pending")
	ErrBatchAborted        = errors.New("ledger: batch aborted by another entry")
	ErrStopped             = errors.New("ledger: module is not running")
	ErrPeriodClosed        = errors.New("ledger: accounting period is closed")
)

type Side string

const (
	Debit  Side = "debit"
	Credit Side = "credit"
)

func (s Side) valid() bool {
	return s == Debit || s == Credit
}

func (s Side) opposite() Side {
	if s == Debit {
		return Credit
	}
	return Debit
}

type UpdateInput struct {
	Name        *string        `json:"name"`
	Description *string        `json:"description"`
	Metadata    jsontext.Value `json:"metadata,omitzero"`
}

type EffectiveRange struct {
	From  *time.Time
	Until *time.Time
}

type Balances struct {
	Pending   Balance `json:"pending"`
	Posted    Balance `json:"posted"`
	Available Balance `json:"available"`
}

type BalanceCondition struct {
	GT    *money.Amount `json:"gt,omitempty"`
	GTE   *money.Amount `json:"gte,omitempty"`
	EQ    *money.Amount `json:"eq,omitempty"`
	LT    *money.Amount `json:"lt,omitempty"`
	LTE   *money.Amount `json:"lte,omitempty"`
	NotEQ *money.Amount `json:"not_eq,omitempty"`
}

func (c *BalanceCondition) violation(v money.Amount) string {
	if c == nil {
		return ""
	}
	bounds := []struct {
		op    string
		bound *money.Amount
		holds func(cmp int) bool
	}{
		{"gt", c.GT, func(cmp int) bool { return cmp > 0 }},
		{"gte", c.GTE, func(cmp int) bool { return cmp >= 0 }},
		{"eq", c.EQ, func(cmp int) bool { return cmp == 0 }},
		{"lt", c.LT, func(cmp int) bool { return cmp < 0 }},
		{"lte", c.LTE, func(cmp int) bool { return cmp <= 0 }},
		{"not_eq", c.NotEQ, func(cmp int) bool { return cmp != 0 }},
	}
	for _, b := range bounds {
		if b.bound != nil && !b.holds(v.Cmp(*b.bound)) {
			return fmt.Sprintf("%s is not %s %s", v, b.op, *b.bound)
		}
	}
	return ""
}

type Balance struct {
	Debits  money.Amount `json:"debits"`
	Credits money.Amount `json:"credits"`
	Amount  money.Amount `json:"amount"`
}

func jsonEqual(a, b jsontext.Value) bool {
	decode := func(raw jsontext.Value) (any, bool) {
		if len(bytes.TrimSpace(raw)) == 0 {
			return map[string]any{}, true
		}
		v, err := decodeJSON(raw)
		return v, err == nil
	}
	av, okA := decode(a)
	bv, okB := decode(b)
	return okA && okB && jsonValueEqual(av, bv)
}

func decodeJSON(raw []byte) (any, error) {
	dec := jsontext.NewDecoder(bytes.NewReader(raw), jsontext.AllowDuplicateNames(true))
	v, err := readJSON(dec)
	if err != nil {
		return nil, err
	}
	if _, err := dec.ReadToken(); !errors.Is(err, io.EOF) {
		return nil, errors.New("ledger: trailing data after JSON value")
	}
	return v, nil
}

func readJSON(dec *jsontext.Decoder) (any, error) {
	tok, err := dec.ReadToken()
	if err != nil {
		return nil, err
	}
	switch tok.Kind() {
	case jsontext.KindBeginObject:
		obj := map[string]any{}
		for dec.PeekKind() != jsontext.KindEndObject {
			name, err := dec.ReadToken()
			if err != nil {
				return nil, err
			}
			key := name.String()
			v, err := readJSON(dec)
			if err != nil {
				return nil, err
			}
			obj[key] = v
		}
		_, err := dec.ReadToken()
		return obj, err
	case jsontext.KindBeginArray:
		arr := []any{}
		for dec.PeekKind() != jsontext.KindEndArray {
			v, err := readJSON(dec)
			if err != nil {
				return nil, err
			}
			arr = append(arr, v)
		}
		_, err := dec.ReadToken()
		return arr, err
	case jsontext.KindString:
		return tok.String(), nil
	case jsontext.KindNumber:
		return jsontext.Value(tok.String()), nil
	case jsontext.KindTrue, jsontext.KindFalse:
		return tok.Bool(), nil
	default:
		return nil, nil
	}
}

func jsonValueEqual(a, b any) bool {
	switch a := a.(type) {
	case map[string]any:
		b, ok := b.(map[string]any)
		if !ok || len(a) != len(b) {
			return false
		}
		for k, av := range a {
			bv, ok := b[k]
			if !ok || !jsonValueEqual(av, bv) {
				return false
			}
		}
		return true
	case []any:
		b, ok := b.([]any)
		if !ok || len(a) != len(b) {
			return false
		}
		for i := range a {
			if !jsonValueEqual(a[i], b[i]) {
				return false
			}
		}
		return true
	case jsontext.Value:
		b, ok := b.(jsontext.Value)
		return ok && canonicalNumber(a) == canonicalNumber(b)
	default:
		return a == b
	}
}

func canonicalNumber(n jsontext.Value) string {
	s := string(n)
	sign := ""
	if strings.HasPrefix(s, "-") {
		sign, s = "-", s[1:]
	}
	exp := 0
	if i := strings.IndexAny(s, "eE"); i >= 0 {
		e, err := strconv.Atoi(s[i+1:])
		if err != nil {
			return string(n)
		}
		exp, s = e, s[:i]
	}
	digits := s
	if i := strings.IndexByte(s, '.'); i >= 0 {
		digits = s[:i] + s[i+1:]
		exp -= len(s) - i - 1
	}
	digits = strings.TrimLeft(digits, "0")
	if digits == "" {
		return "0"
	}
	trimmed := strings.TrimRight(digits, "0")
	exp += len(digits) - len(trimmed)
	return sign + trimmed + "e" + strconv.Itoa(exp)
}
