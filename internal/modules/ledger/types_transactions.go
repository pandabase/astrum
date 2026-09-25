package ledger

import (
	"encoding/json/jsontext"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/pandabase/astrum/internal/money"
)

type ListTransactionsInput struct {
	LedgerID   uuid.UUID
	Status     TransactionStatus
	ExternalID string
	AccountID  uuid.UUID
	Metadata   map[string]string
	Effective  EffectiveRange
	Before     uuid.UUID
	Limit      int
}

type Posting struct {
	AccountID uuid.UUID      `json:"account_id"`
	Side      Side           `json:"side"`
	Amount    money.Amount   `json:"amount"`
	Currency  money.Currency `json:"currency,omitzero"`

	PendingBalance   *BalanceCondition `json:"pending_balance_amount,omitzero"`
	PostedBalance    *BalanceCondition `json:"posted_balance_amount,omitzero"`
	AvailableBalance *BalanceCondition `json:"available_balance_amount,omitzero"`

	LockVersion *int64 `json:"lock_version,omitzero"`

	Resulting *Balances `json:"resulting_balances,omitzero"`

	balanceAfter money.Amount
}

func (p Posting) checkLocks(i int, b Balances) error {
	for _, lock := range []struct {
		name      string
		condition *BalanceCondition
		amount    money.Amount
	}{
		{"pending_balance_amount", p.PendingBalance, b.Pending.Amount},
		{"posted_balance_amount", p.PostedBalance, b.Posted.Amount},
		{"available_balance_amount", p.AvailableBalance, b.Available.Amount},
	} {
		if v := lock.condition.violation(lock.amount); v != "" {
			return fmt.Errorf("%w: entry %d %s: %s", ErrBalanceLock, i, lock.name, v)
		}
	}
	return nil
}

func (p Posting) signedAmount() money.Amount {
	if p.Side == Credit {
		return p.Amount.Neg()
	}
	return p.Amount
}

type TransactionStatus string

const (
	TransactionPending TransactionStatus = "pending"

	TransactionPosted TransactionStatus = "posted"

	TransactionArchived TransactionStatus = "archived"
)

type PostInput struct {
	IdempotencyKey string         `json:"idempotency_key"`
	Description    string         `json:"description"`
	Metadata       jsontext.Value `json:"metadata,omitzero"`
	Postings       []Posting      `json:"postings"`

	Status TransactionStatus `json:"status,omitzero"`

	EffectiveAt *time.Time `json:"effective_at,omitempty"`

	ExternalID string `json:"external_id,omitzero"`

	ArchiveOnLockFailure bool `json:"archive_on_balance_lock_failure,omitzero"`
}

func (in PostInput) status() TransactionStatus {
	if in.Status == "" {
		return TransactionPosted
	}
	return in.Status
}

type Transaction struct {
	ID             uuid.UUID         `json:"id"`
	LedgerID       uuid.UUID         `json:"ledger_id"`
	IdempotencyKey string            `json:"idempotency_key"`
	ExternalID     string            `json:"external_id,omitzero"`
	Status         TransactionStatus `json:"status"`
	Version        int               `json:"version"`
	Description    string            `json:"description"`
	Metadata       jsontext.Value    `json:"metadata"`
	ReversesID     *uuid.UUID        `json:"reverses_id,omitempty"`

	Postings    []Posting  `json:"postings"`
	EffectiveAt time.Time  `json:"effective_at"`
	CreatedAt   time.Time  `json:"created_at"`
	PostedAt    *time.Time `json:"posted_at,omitempty"`
	ArchivedAt  *time.Time `json:"archived_at,omitempty"`

	request *PostInput

	entriesVersion int
}

func (t Transaction) matches(in PostInput) bool {
	if t.request != nil {
		r := *t.request
		sameTime := (r.EffectiveAt == nil && in.EffectiveAt == nil) ||
			(r.EffectiveAt != nil && in.EffectiveAt != nil && r.EffectiveAt.Equal(*in.EffectiveAt))
		return sameTime && r.status() == in.status() && sameContent(r, in)
	}
	if in.status() != TransactionPosted || (in.EffectiveAt != nil && !in.EffectiveAt.Equal(t.EffectiveAt)) {
		return false
	}
	return sameContent(PostInput{
		Description: t.Description,
		Metadata:    t.Metadata,
		Postings:    t.Postings,
		ExternalID:  t.ExternalID,
	}, in)
}

func sameContent(stored, in PostInput) bool {
	if stored.Description != in.Description || stored.ExternalID != in.ExternalID || len(stored.Postings) != len(in.Postings) {
		return false
	}
	if !jsonEqual(stored.Metadata, in.Metadata) {
		return false
	}
	for i, p := range in.Postings {
		existing := stored.Postings[i]
		if existing.AccountID != p.AccountID || existing.Side != p.Side || existing.Amount != p.Amount {
			return false
		}
		if p.Currency != "" && existing.Currency != "" && p.Currency != existing.Currency {
			return false
		}
	}
	return true
}

type UpdateTransactionInput struct {
	Description *string        `json:"description"`
	Metadata    jsontext.Value `json:"metadata,omitzero"`
	Postings    []Posting      `json:"postings"`
	EffectiveAt *time.Time     `json:"effective_at"`
}

type PostPendingInput struct {
	Postings []Posting `json:"postings"`
}

type BatchResult struct {
	Transaction *Transaction `json:"transaction,omitzero"`
	Replayed    bool         `json:"replayed,omitzero"`
	Error       string       `json:"error,omitzero"`
	Err         error        `json:"-"`
}

type ReverseInput struct {
	IdempotencyKey string         `json:"idempotency_key"`
	Description    string         `json:"description"`
	Metadata       jsontext.Value `json:"metadata,omitzero"`
}
