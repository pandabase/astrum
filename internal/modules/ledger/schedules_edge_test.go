package ledger

import (
	"encoding/json/jsontext"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestSchedulesEdgeMatches(t *testing.T) {
	a, b := uuid.New(), uuid.New()
	when := time.Date(2026, 5, 1, 9, 0, 0, 0, time.UTC)
	request := PostInput{
		Description: "rent",
		Metadata:    jsontext.Value(`{"n":1}`),
		Postings: []Posting{
			{AccountID: a, Side: Credit, Amount: amt(10)},
			{AccountID: b, Side: Debit, Amount: amt(10)},
		},
	}
	st := ScheduledTransaction{ExecuteAt: when, Request: request}
	tests := []struct {
		name   string
		mutate func(*ScheduleInput)
		want   bool
	}{
		{"identical", func(in *ScheduleInput) {}, true},
		{"equivalent metadata", func(in *ScheduleInput) { in.Metadata = jsontext.Value(`{ "n" : 1.0 }`) }, true},
		{"explicit posted status", func(in *ScheduleInput) { in.Status = TransactionPosted }, true},
		{"explicit currency on a posting", func(in *ScheduleInput) {
			in.Postings = []Posting{{AccountID: a, Side: Credit, Amount: amt(10), Currency: "USD"}, request.Postings[1]}
		}, true},
		{"same instant in another zone", func(in *ScheduleInput) { in.ExecuteAt = when.In(time.FixedZone("x", -7200)) }, true},
		{"later execute_at", func(in *ScheduleInput) { in.ExecuteAt = when.Add(time.Microsecond) }, false},
		{"other description", func(in *ScheduleInput) { in.Description = "Rent" }, false},
		{"other metadata", func(in *ScheduleInput) { in.Metadata = jsontext.Value(`{"n":2}`) }, false},
		{"swapped postings", func(in *ScheduleInput) {
			in.Postings = []Posting{request.Postings[1], request.Postings[0]}
		}, false},
		{"extra posting", func(in *ScheduleInput) {
			in.Postings = append(append([]Posting(nil), request.Postings...), Posting{AccountID: a, Side: Debit, Amount: amt(1)})
		}, false},
	}
	for _, tt := range tests {
		in := ScheduleInput{PostInput: request, ExecuteAt: when}
		in.Postings = append([]Posting(nil), request.Postings...)
		tt.mutate(&in)
		if got := st.matches(in); got != tt.want {
			t.Errorf("%s: matches() = %v, want %v", tt.name, got, tt.want)
		}
	}
}
