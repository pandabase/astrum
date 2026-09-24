package ledger

import (
	"encoding/json/jsontext"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestSettlementsEdgeMatches(t *testing.T) {
	settled, contra := uuid.New(), uuid.New()
	bound := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	st := Settlement{SettledAccountID: settled, ContraAccountID: contra, UpperBound: &bound, Description: "d", Metadata: jsontext.Value(`{}`)}
	base := CreateSettlementInput{IdempotencyKey: "k", SettledAccountID: settled, ContraAccountID: contra, UpperBound: &bound, Description: "d"}
	other := bound.Add(time.Microsecond)
	same := bound.In(time.FixedZone("y", 3600))
	tests := []struct {
		name   string
		mutate func(*CreateSettlementInput)
		want   bool
	}{
		{"identical", func(in *CreateSettlementInput) {}, true},
		{"empty metadata equals braces", func(in *CreateSettlementInput) { in.Metadata = jsontext.Value(` {} `) }, true},
		{"same bound in another zone", func(in *CreateSettlementInput) { in.UpperBound = &same }, true},
		{"no bound", func(in *CreateSettlementInput) { in.UpperBound = nil }, false},
		{"other bound", func(in *CreateSettlementInput) { in.UpperBound = &other }, false},
		{"swapped accounts", func(in *CreateSettlementInput) { in.SettledAccountID, in.ContraAccountID = contra, settled }, false},
		{"other description", func(in *CreateSettlementInput) { in.Description = "" }, false},
		{"other metadata", func(in *CreateSettlementInput) { in.Metadata = jsontext.Value(`{"a":1}`) }, false},
	}
	for _, tt := range tests {
		in := base
		tt.mutate(&in)
		if got := st.matches(in); got != tt.want {
			t.Errorf("%s: matches() = %v, want %v", tt.name, got, tt.want)
		}
	}
	unbounded := st
	unbounded.UpperBound = nil
	if !unbounded.matches(CreateSettlementInput{SettledAccountID: settled, ContraAccountID: contra, Description: "d"}) {
		t.Error("unbounded settlement does not match an unbounded request")
	}
	if unbounded.matches(base) {
		t.Error("unbounded settlement matches a bounded request")
	}
}

func TestSettlementsEdgeValidate(t *testing.T) {
	a, b := uuid.New(), uuid.New()
	tests := []struct {
		name string
		in   CreateSettlementInput
		ok   bool
	}{
		{"valid", CreateSettlementInput{IdempotencyKey: "k", SettledAccountID: a, ContraAccountID: b}, true},
		{"key at limit", CreateSettlementInput{IdempotencyKey: strings.Repeat("k", 255), SettledAccountID: a, ContraAccountID: b}, true},
		{"key over limit", CreateSettlementInput{IdempotencyKey: strings.Repeat("k", 256), SettledAccountID: a, ContraAccountID: b}, false},
		{"blank key", CreateSettlementInput{IdempotencyKey: " ", SettledAccountID: a, ContraAccountID: b}, false},
		{"no settled", CreateSettlementInput{IdempotencyKey: "k", ContraAccountID: b}, false},
		{"no contra", CreateSettlementInput{IdempotencyKey: "k", SettledAccountID: a}, false},
		{"same account", CreateSettlementInput{IdempotencyKey: "k", SettledAccountID: a, ContraAccountID: a}, false},
		{"metadata array", CreateSettlementInput{IdempotencyKey: "k", SettledAccountID: a, ContraAccountID: b, Metadata: jsontext.Value(`[]`)}, false},
	}
	for _, tt := range tests {
		err := validateSettlement(tt.in)
		if (err == nil) != tt.ok || (err != nil && !errors.Is(err, ErrInvalid)) {
			t.Errorf("%s: validateSettlement() = %v, want ok=%v", tt.name, err, tt.ok)
		}
	}
}
