package ledger

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestHoldsEdgeMatches(t *testing.T) {
	t.Parallel()
	account := uuid.New()
	expires := time.Date(2026, 3, 1, 12, 0, 0, 123456000, time.UTC)
	h := Hold{AccountID: account, Amount: amt(50), Currency: "USD", Description: "auth", ExpiresAt: expires}
	base := CreateHoldInput{IdempotencyKey: "k", AccountID: account, Amount: amt(50), Description: "auth", ExpiresAt: expires}
	tests := []struct {
		name   string
		mutate func(*CreateHoldInput)
		want   bool
	}{
		{"identical", func(in *CreateHoldInput) {}, true},
		{"explicit currency", func(in *CreateHoldInput) { in.Currency = "USD" }, true},
		{"other time zone", func(in *CreateHoldInput) { in.ExpiresAt = expires.In(time.FixedZone("x", 3600)) }, true},
		{"other currency", func(in *CreateHoldInput) { in.Currency = "EUR" }, false},
		{"other account", func(in *CreateHoldInput) { in.AccountID = uuid.New() }, false},
		{"other amount", func(in *CreateHoldInput) { in.Amount = amt(51) }, false},
		{"other description", func(in *CreateHoldInput) { in.Description = "Auth" }, false},
		{"one microsecond later", func(in *CreateHoldInput) { in.ExpiresAt = expires.Add(time.Microsecond) }, false},
		{"untruncated nanoseconds", func(in *CreateHoldInput) { in.ExpiresAt = expires.Add(1) }, false},
	}
	for _, tt := range tests {
		in := base
		tt.mutate(&in)
		if got := h.matches(in); got != tt.want {
			t.Errorf("%s: matches() = %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestHoldsEdgeReserveRelease(t *testing.T) {
	t.Parallel()
	id := uuid.New()
	tests := []struct {
		name    string
		account accountState
		reserve int64
		ok      bool
	}{
		{"exactly available", accountState{normalSide: Debit, postedDebits: amt(100), status: AccountOpen}, 100, true},
		{"one over", accountState{normalSide: Debit, postedDebits: amt(100), status: AccountOpen}, 101, false},
		{"within overdraft", accountState{normalSide: Debit, postedDebits: amt(100), overdraftLimit: amt(10), status: AccountOpen}, 110, true},
		{"past overdraft", accountState{normalSide: Debit, postedDebits: amt(100), overdraftLimit: amt(10), status: AccountOpen}, 111, false},
		{"pending outflow counts", accountState{normalSide: Debit, postedDebits: amt(100), pendingCredits: amt(30), status: AccountOpen}, 71, false},
		{"pending inflow does not count", accountState{normalSide: Debit, postedDebits: amt(100), pendingDebits: amt(30), status: AccountOpen}, 101, false},
		{"credit normal", accountState{normalSide: Credit, postedCredits: amt(40), status: AccountOpen}, 40, true},
		{"credit normal one over", accountState{normalSide: Credit, postedCredits: amt(40), status: AccountOpen}, 41, false},
		{"allow negative", accountState{normalSide: Debit, allowNegative: true, status: AccountOpen}, 1_000, true},
		{"frozen", accountState{normalSide: Debit, postedDebits: amt(100), status: AccountFrozen}, 1, false},
		{"closed", accountState{normalSide: Debit, status: AccountClosed, allowNegative: true}, 1, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			acc := tt.account
			acc.id = id
			acc.currency = "USD"
			s := newLedgerState([]*accountState{&acc}, nil)
			err := s.reserve(id, "", amt(tt.reserve))
			if (err == nil) != tt.ok {
				t.Fatalf("reserve() = %v, want ok=%v", err, tt.ok)
			}
			if !tt.ok {
				if s.accounts[id].held != amt(0) || s.accounts[id].version != 0 {
					t.Fatalf("rejected reserve changed state: %+v", s.accounts[id])
				}
				return
			}
			if s.accounts[id].held != amt(tt.reserve) || s.accounts[id].version != 1 {
				t.Fatalf("after reserve = %+v", s.accounts[id])
			}
			if err := s.release(id, amt(tt.reserve+1)); err == nil {
				t.Fatal("released more than held")
			}
			if err := s.release(id, amt(tt.reserve)); err != nil || s.accounts[id].held != amt(0) {
				t.Fatalf("release() = %v, held %s", err, s.accounts[id].held)
			}
		})
	}
	s := newLedgerState(nil, nil)
	if err := s.reserve(id, "", amt(1)); err == nil {
		t.Fatal("reserve on an unknown account succeeded")
	}
	if err := s.release(id, amt(1)); err == nil {
		t.Fatal("release on an unknown account succeeded")
	}
}
