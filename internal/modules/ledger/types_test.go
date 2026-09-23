package ledger

import (
	"encoding/json/jsontext"
	"testing"

	"github.com/pandabase/astrum/internal/money"
)

func TestJSONEqual(t *testing.T) {
	tests := []struct {
		a, b string
		want bool
	}{
		{`{"n":1e2}`, `{"n":100}`, true},
		{`{"n":1.50}`, `{"n":1.5}`, true},
		{`{"n":-0}`, `{"n":0}`, true},
		{`{"n":0.001}`, `{"n":1E-3}`, true},
		{`{"n":12345678901234567890123}`, `{"n":12345678901234567890123.0}`, true},
		{`{"n":12345678901234567890123}`, `{"n":12345678901234567890124}`, false},
		{`{"n":1e999999999}`, `{"n":1e999999998}`, false},
		{`{"n":100}`, `{"n":"100"}`, false},
		{`{"n":-1}`, `{"n":1}`, false},
		{`{"a":[1,{"b":2e0}]}`, `{"a":[1,{"b":2}]}`, true},
		{`{"a":[1,2]}`, `{"a":[2,1]}`, false},
		{`{"a":1}`, `{"a":1,"b":null}`, false},
		{``, `{}`, true},
	}
	for _, tt := range tests {
		if got := jsonEqual(jsontext.Value(tt.a), jsontext.Value(tt.b)); got != tt.want {
			t.Errorf("jsonEqual(%s, %s) = %v, want %v", tt.a, tt.b, got, tt.want)
		}
	}
}

func TestBalanceConditionViolation(t *testing.T) {
	p := func(n int64) *money.Amount { a := money.NewAmount(n); return &a }
	tests := []struct {
		name string
		c    *BalanceCondition
		v    int64
		ok   bool
	}{
		{"nil", nil, -5, true},
		{"empty", &BalanceCondition{}, -5, true},
		{"gte holds at bound", &BalanceCondition{GTE: p(0)}, 0, true},
		{"gte fails", &BalanceCondition{GTE: p(0)}, -1, false},
		{"gt fails at bound", &BalanceCondition{GT: p(0)}, 0, false},
		{"lt holds", &BalanceCondition{LT: p(10)}, 9, true},
		{"lte fails", &BalanceCondition{LTE: p(10)}, 11, false},
		{"eq", &BalanceCondition{EQ: p(7)}, 7, true},
		{"eq fails", &BalanceCondition{EQ: p(7)}, 8, false},
		{"not_eq fails", &BalanceCondition{NotEQ: p(0)}, 0, false},
		{"range holds", &BalanceCondition{GTE: p(0), LTE: p(100)}, 100, true},
		{"range fails high", &BalanceCondition{GTE: p(0), LTE: p(100)}, 101, false},
		{"negative bound", &BalanceCondition{GTE: p(-50)}, -50, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.c.violation(money.NewAmount(tt.v)); (got == "") != tt.ok {
				t.Fatalf("violation(%d) = %q, want ok %v", tt.v, got, tt.ok)
			}
		})
	}
}
