package ledger

import (
	"encoding/json/jsontext"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestIdempotencyEdgeNumberEquality(t *testing.T) {
	t.Parallel()
	tests := []struct {
		a, b string
		want bool
	}{
		{`1`, `1.0`, true},
		{`1`, `1e0`, true},
		{`1`, `10e-1`, true},
		{`1`, `0.1E+1`, true},
		{`100`, `1E+2`, true},
		{`0`, `0e10`, true},
		{`0`, `-0.0e-5`, true},
		{`0.5`, `5e-1`, true},
		{`1.10`, `11e-1`, true},
		{`0.12345678901234567890123456789`, `123456789012345678901234567890e-30`, true},
		{`12345678901234567890`, `1.2345678901234567890e19`, true},
		{`-12.50`, `-1250e-2`, true},
		{`12345678901234567890`, `12345678901234567891`, false},
		{`12345678901234567890`, `12345678901234567000`, false},
		{`1`, `1.0000000000000000000001`, false},
		{`1e-400`, `0`, false},
		{`0.5`, `-0.5`, false},
		{`1e2`, `1e3`, false},
	}
	for _, tt := range tests {
		t.Run(tt.a+" vs "+tt.b, func(t *testing.T) {
			a := jsontext.Value(`{"n":` + tt.a + `}`)
			b := jsontext.Value(`{"n":` + tt.b + `}`)
			if got := jsonEqual(a, b); got != tt.want {
				t.Fatalf("jsonEqual = %v, want %v", got, tt.want)
			}
			if got := jsonEqual(b, a); got != tt.want {
				t.Fatalf("jsonEqual reversed = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestIdempotencyEdgeStructuralEquality(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		a, b string
		want bool
	}{
		{"whitespace only equals empty object", " \n", `{}`, true},
		{"null member differs from absent", `{"a":null}`, `{}`, false},
		{"false differs from null", `{"a":false}`, `{"a":null}`, false},
		{"empty array differs from empty object", `{"a":[]}`, `{"a":{}}`, false},
		{"escaped and literal strings match", `{"a":"é"}`, `{"a":"é"}`, true},
		{"escaped names match", `{"a":1}`, `{"a":1}`, true},
		{"nested order", `{"a":{"x":1,"y":[1,{"z":2}]}}`, `{"a":{"y":[1,{"z":2.0}],"x":1}}`, true},
		{"trailing garbage never matches", `{"a":1} x`, `{"a":1}`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := jsonEqual(jsontext.Value(tt.a), jsontext.Value(tt.b)); got != tt.want {
				t.Fatalf("jsonEqual(%s, %s) = %v, want %v", tt.a, tt.b, got, tt.want)
			}
		})
	}
}

func TestIdempotencyEdgePendingRequestMatching(t *testing.T) {
	t.Parallel()
	a, b := uuid.New(), uuid.New()
	at := time.Date(2026, 5, 6, 7, 8, 9, 123_456_000, time.UTC)
	request := PostInput{
		IdempotencyKey: "k",
		Status:         TransactionPending,
		EffectiveAt:    &at,
		Postings:       []Posting{{AccountID: a, Side: Debit, Amount: amt(5)}, {AccountID: b, Side: Credit, Amount: amt(5)}},
	}
	stored := Transaction{Status: TransactionPosted, Postings: []Posting{
		{AccountID: a, Side: Debit, Amount: amt(2), Currency: "USD"},
		{AccountID: b, Side: Credit, Amount: amt(2), Currency: "USD"},
	}, request: &request}

	tests := []struct {
		name   string
		mutate func(*PostInput)
		want   bool
	}{
		{"original request matches after a partial post", func(*PostInput) {}, true},
		{"same instant in another zone", func(in *PostInput) { v := at.In(time.FixedZone("Y", 3600)); in.EffectiveAt = &v }, true},
		{"posted amounts do not match the request", func(in *PostInput) {
			in.Postings = []Posting{{AccountID: a, Side: Debit, Amount: amt(2)}, {AccountID: b, Side: Credit, Amount: amt(2)}}
		}, false},
		{"status omitted means posted", func(in *PostInput) { in.Status = "" }, false},
		{"effective_at omitted", func(in *PostInput) { in.EffectiveAt = nil }, false},
		{"effective_at one microsecond later", func(in *PostInput) { v := at.Add(time.Microsecond); in.EffectiveAt = &v }, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := request
			in.Postings = append([]Posting(nil), request.Postings...)
			tt.mutate(&in)
			if got := stored.matches(in); got != tt.want {
				t.Fatalf("matches() = %v, want %v", got, tt.want)
			}
		})
	}
}
