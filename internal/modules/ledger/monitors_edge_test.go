package ledger

import (
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestMonitorsEdgeConditionHolds(t *testing.T) {
	balances := Balances{
		Pending:   Balance{Amount: amt(20)},
		Posted:    Balance{Amount: amt(10)},
		Available: Balance{Amount: amt(-5)},
	}
	amounts := map[string]int64{"pending": 20, "posted": 10, "available": -5, "unknown": -5, "": -5}
	ops := map[string]func(cmp int) bool{
		"gt":     func(c int) bool { return c > 0 },
		"gte":    func(c int) bool { return c >= 0 },
		"eq":     func(c int) bool { return c == 0 },
		"lt":     func(c int) bool { return c < 0 },
		"lte":    func(c int) bool { return c <= 0 },
		"not_eq": func(c int) bool { return c != 0 },
		"bogus":  func(c int) bool { return c != 0 },
	}
	for field, amount := range amounts {
		for op, want := range ops {
			for _, delta := range []int64{-1, 0, 1} {
				c := AlertCondition{Field: field, Operator: op, Value: amt(amount - delta)}
				cmp := 0
				if delta > 0 {
					cmp = 1
				} else if delta < 0 {
					cmp = -1
				}
				t.Run(fmt.Sprintf("%s %s value%+d", field, op, -delta), func(t *testing.T) {
					if got := c.holds(balances); got != want(cmp) {
						t.Fatalf("holds() = %v, want %v", got, want(cmp))
					}
				})
			}
		}
	}
}

func TestMonitorsEdgeValidate(t *testing.T) {
	id := uuid.New()
	tests := []struct {
		name string
		in   CreateBalanceMonitorInput
		ok   bool
	}{
		{"minimal", CreateBalanceMonitorInput{AccountID: id, Condition: AlertCondition{Field: "posted", Operator: "eq"}}, true},
		{"no account", CreateBalanceMonitorInput{Condition: AlertCondition{Field: "posted", Operator: "eq"}}, false},
		{"api spelling", CreateBalanceMonitorInput{AccountID: id, Condition: AlertCondition{Field: "available_balance_amount", Operator: "eq"}}, false},
		{"mixed case field", CreateBalanceMonitorInput{AccountID: id, Condition: AlertCondition{Field: "Posted", Operator: "eq"}}, false},
		{"ne alias", CreateBalanceMonitorInput{AccountID: id, Condition: AlertCondition{Field: "posted", Operator: "ne"}}, false},
		{"description at limit", CreateBalanceMonitorInput{AccountID: id, Condition: AlertCondition{Field: "posted", Operator: "eq"}, Description: strings.Repeat("d", 1024)}, true},
		{"description over limit", CreateBalanceMonitorInput{AccountID: id, Condition: AlertCondition{Field: "posted", Operator: "eq"}, Description: strings.Repeat("d", 1025)}, false},
		{"metadata scalar", CreateBalanceMonitorInput{AccountID: id, Condition: AlertCondition{Field: "posted", Operator: "eq"}, Metadata: jsontext.Value(`1`)}, false},
	}
	for _, tt := range tests {
		err := validateMonitor(tt.in)
		if (err == nil) != tt.ok || (err != nil && !errors.Is(err, ErrInvalid)) {
			t.Errorf("%s: validateMonitor() = %v, want ok=%v", tt.name, err, tt.ok)
		}
	}
}

func TestMonitorsEdgeCrossedFiresOnEntryOnly(t *testing.T) {
	id := uuid.New()
	m := BalanceMonitor{ID: uuid.New(), AccountID: id, Condition: AlertCondition{Field: "posted", Operator: "gte", Value: amt(10)}}
	tests := []struct {
		name          string
		before, after int64
		fired         bool
	}{
		{"below to exactly", 9, 10, true},
		{"below to above", 0, 100, true},
		{"exactly to above", 10, 11, false},
		{"above to below", 11, 9, false},
		{"below to below", 1, 9, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			acc := &accountState{id: id, normalSide: Debit, postedDebits: amt(tt.before), version: 1}
			s := newLedgerState([]*accountState{acc}, []BalanceMonitor{m})
			next := *acc
			next.postedDebits = amt(tt.after)
			next.version++
			s.accounts[id] = &next
			fired, balances, err := s.crossed()
			if err != nil {
				t.Fatal(err)
			}
			if (len(fired) == 1) != tt.fired {
				t.Fatalf("fired = %+v, want fired=%v", fired, tt.fired)
			}
			if tt.fired && (!fired[0].Triggered || balances[0].Posted.Amount != amt(tt.after)) {
				t.Fatalf("fired monitor = %+v with balances %+v", fired[0], balances[0])
			}
		})
	}

	t.Run("untouched accounts never fire", func(t *testing.T) {
		acc := &accountState{id: id, normalSide: Debit, postedDebits: amt(100), version: 3}
		s := newLedgerState([]*accountState{acc}, []BalanceMonitor{m})
		if fired, _, err := s.crossed(); err != nil || len(fired) != 0 {
			t.Fatalf("crossed() = %+v, %v", fired, err)
		}
	})
}
