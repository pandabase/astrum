package ledger

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestHistoryEdgeNarrow(t *testing.T) {
	d := func(n int) *time.Time {
		v := time.Date(2026, 1, n, 0, 0, 0, 0, time.UTC)
		return &v
	}
	tests := []struct {
		name      string
		a, b      EffectiveRange
		wantFrom  *time.Time
		wantUntil *time.Time
	}{
		{"both open", EffectiveRange{}, EffectiveRange{}, nil, nil},
		{"statement bounds an open request", EffectiveRange{}, EffectiveRange{From: d(2), Until: d(9)}, d(2), d(9)},
		{"request inside statement", EffectiveRange{From: d(3), Until: d(5)}, EffectiveRange{From: d(2), Until: d(9)}, d(3), d(5)},
		{"request wider than statement", EffectiveRange{From: d(1), Until: d(20)}, EffectiveRange{From: d(2), Until: d(9)}, d(2), d(9)},
		{"equal bounds", EffectiveRange{From: d(2), Until: d(9)}, EffectiveRange{From: d(2), Until: d(9)}, d(2), d(9)},
		{"disjoint collapses", EffectiveRange{From: d(10), Until: d(12)}, EffectiveRange{From: d(2), Until: d(9)}, d(10), d(9)},
		{"only lower bound", EffectiveRange{From: d(5)}, EffectiveRange{Until: d(9)}, d(5), d(9)},
	}
	for _, tt := range tests {
		got := narrow(tt.a, tt.b)
		eq := func(x, y *time.Time) bool { return (x == nil && y == nil) || (x != nil && y != nil && x.Equal(*y)) }
		if !eq(got.From, tt.wantFrom) || !eq(got.Until, tt.wantUntil) {
			t.Errorf("%s: narrow() = %v..%v, want %v..%v", tt.name, got.From, got.Until, tt.wantFrom, tt.wantUntil)
		}
	}
}

func TestHistoryEdgeValidateRange(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	later, earlier := now.Add(time.Nanosecond), now.Add(-time.Nanosecond)
	tests := []struct {
		name string
		r    EffectiveRange
		ok   bool
	}{
		{"open", EffectiveRange{}, true},
		{"from only", EffectiveRange{From: &now}, true},
		{"until only", EffectiveRange{Until: &now}, true},
		{"one nanosecond wide", EffectiveRange{From: &now, Until: &later}, true},
		{"empty", EffectiveRange{From: &now, Until: &now}, false},
		{"inverted", EffectiveRange{From: &now, Until: &earlier}, false},
	}
	for _, tt := range tests {
		err := validateRange(tt.r)
		if (err == nil) != tt.ok || (err != nil && !errors.Is(err, ErrInvalid)) {
			t.Errorf("%s: validateRange() = %v, want ok=%v", tt.name, err, tt.ok)
		}
	}
}

func TestHistoryEdgeValidateStatement(t *testing.T) {
	from := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	id := uuid.New()
	tests := []struct {
		name string
		in   CreateStatementInput
		ok   bool
	}{
		{"valid", CreateStatementInput{AccountID: id, From: from, Until: from.Add(time.Microsecond)}, true},
		{"no account", CreateStatementInput{From: from, Until: from.Add(time.Hour)}, false},
		{"zero from", CreateStatementInput{AccountID: id, Until: from}, false},
		{"zero until", CreateStatementInput{AccountID: id, From: from}, false},
		{"equal", CreateStatementInput{AccountID: id, From: from, Until: from}, false},
		{"inverted", CreateStatementInput{AccountID: id, From: from, Until: from.Add(-time.Hour)}, false},
		{"NUL description", CreateStatementInput{AccountID: id, From: from, Until: from.Add(time.Hour), Description: "\x00"}, false},
	}
	for _, tt := range tests {
		err := validateStatement(tt.in)
		if (err == nil) != tt.ok || (err != nil && !errors.Is(err, ErrInvalid)) {
			t.Errorf("%s: validateStatement() = %v, want ok=%v", tt.name, err, tt.ok)
		}
	}
}
