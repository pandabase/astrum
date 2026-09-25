package ledger

import (
	"encoding/json/jsontext"
	"time"

	"github.com/pandabase/astrum/internal/money"
)

type entryLine struct {
	AccountID              accountID         `json:"account_id"`
	Side                   Side              `json:"side"`
	Amount                 money.Amount      `json:"amount"`
	Currency               money.Currency    `json:"currency,omitzero"`
	PendingBalanceAmount   *BalanceCondition `json:"pending_balance_amount,omitzero"`
	PostedBalanceAmount    *BalanceCondition `json:"posted_balance_amount,omitzero"`
	AvailableBalanceAmount *BalanceCondition `json:"available_balance_amount,omitzero"`
	LockVersion            *int64            `json:"lock_version,omitzero"`
	ResultingBalances      *Balances         `json:"resulting_balances,omitzero"`
}

func toEntries(postings []Posting) []entryLine {
	out := make([]entryLine, len(postings))
	for i, p := range postings {
		out[i] = entryLine{
			AccountID:         accountID(p.AccountID),
			Side:              p.Side,
			Amount:            p.Amount,
			Currency:          p.Currency,
			ResultingBalances: p.Resulting,
		}
	}
	return out
}

func fromEntries(entries []entryLine) []Posting {
	out := make([]Posting, len(entries))
	for i, e := range entries {
		out[i] = Posting{
			AccountID:        e.AccountID.UUID(),
			Side:             e.Side,
			Amount:           e.Amount,
			Currency:         e.Currency,
			PendingBalance:   e.PendingBalanceAmount,
			PostedBalance:    e.PostedBalanceAmount,
			AvailableBalance: e.AvailableBalanceAmount,
			LockVersion:      e.LockVersion,
		}
	}
	return out
}

type transactionResource struct {
	Object         string            `json:"object"`
	ID             transactionID     `json:"id"`
	LedgerID       ledgerID          `json:"ledger_id"`
	IdempotencyKey string            `json:"idempotency_key"`
	ExternalID     *string           `json:"external_id"`
	Status         TransactionStatus `json:"status"`
	Version        int               `json:"version"`
	Description    string            `json:"description"`
	Metadata       jsontext.Value    `json:"metadata"`
	ReversesID     *transactionID    `json:"reverses_id"`
	Entries        []entryLine       `json:"entries"`
	EffectiveAt    time.Time         `json:"effective_at"`
	CreatedAt      time.Time         `json:"created_at"`
	PostedAt       *time.Time        `json:"posted_at"`
	ArchivedAt     *time.Time        `json:"archived_at"`
}

func toTransaction(t Transaction) transactionResource {
	return transactionResource{
		Object:         "transaction",
		ID:             transactionID(t.ID),
		LedgerID:       ledgerID(t.LedgerID),
		IdempotencyKey: t.IdempotencyKey,
		ExternalID:     nullString(t.ExternalID),
		Status:         t.Status,
		Version:        t.Version,
		Description:    t.Description,
		Metadata:       t.Metadata,
		ReversesID:     (*transactionID)(t.ReversesID),
		Entries:        toEntries(t.Postings),
		EffectiveAt:    t.EffectiveAt,
		CreatedAt:      t.CreatedAt,
		PostedAt:       t.PostedAt,
		ArchivedAt:     t.ArchivedAt,
	}
}

type balancesResource struct {
	Object                string     `json:"object"`
	AccountID             accountID  `json:"account_id"`
	EffectiveAtLowerBound *time.Time `json:"effective_at_lower_bound"`
	EffectiveAtUpperBound *time.Time `json:"effective_at_upper_bound"`
	Balances
}

type transactionRequest struct {
	Description string            `json:"description"`
	Metadata    jsontext.Value    `json:"metadata,omitzero"`
	Entries     []entryLine       `json:"entries"`
	Status      TransactionStatus `json:"status"`
	EffectiveAt *time.Time        `json:"effective_at"`
	ExternalID  string            `json:"external_id"`

	ArchiveOnBalanceLockFailure bool `json:"archive_on_balance_lock_failure"`
}

func (in transactionRequest) post(key string) PostInput {
	return PostInput{
		IdempotencyKey: key,
		Description:    in.Description,
		Metadata:       in.Metadata,
		Postings:       fromEntries(in.Entries),
		Status:         in.Status,
		EffectiveAt:    in.EffectiveAt,
		ExternalID:     in.ExternalID,

		ArchiveOnLockFailure: in.ArchiveOnBalanceLockFailure,
	}
}

type updateTransactionRequest struct {
	Description *string        `json:"description"`
	Metadata    jsontext.Value `json:"metadata,omitzero"`
	Entries     []entryLine    `json:"entries"`
	EffectiveAt *time.Time     `json:"effective_at"`
}

type postRequest struct {
	Entries []entryLine `json:"entries"`
}

type batchRequest struct {
	Atomic       *bool                `json:"atomic"`
	Transactions []transactionRequest `json:"transactions"`
}

type batchResponse struct {
	Object  string        `json:"object"`
	Atomic  bool          `json:"atomic"`
	Results []batchResult `json:"results"`
}

type batchResult struct {
	Transaction *transactionResource `json:"transaction"`
	Error       *batchError          `json:"error"`
}

type batchError struct {
	Code   string `json:"code"`
	Detail string `json:"detail"`
}

type reverseRequest struct {
	Description string         `json:"description"`
	Metadata    jsontext.Value `json:"metadata,omitzero"`
}
