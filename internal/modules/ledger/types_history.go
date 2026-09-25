package ledger

import (
	"time"

	"github.com/google/uuid"
	"github.com/pandabase/astrum/internal/money"
)

type Entry struct {
	Sequence      int64             `json:"sequence,string"`
	TransactionID uuid.UUID         `json:"transaction_id"`
	LedgerID      uuid.UUID         `json:"ledger_id"`
	AccountID     uuid.UUID         `json:"account_id"`
	Status        TransactionStatus `json:"status"`
	Side          Side              `json:"side"`
	Amount        money.Amount      `json:"amount"`
	Currency      money.Currency    `json:"currency"`

	BalanceAfter *money.Amount `json:"balance_after,omitempty"`
	EffectiveAt  time.Time     `json:"effective_at"`
	CreatedAt    time.Time     `json:"created_at"`

	SettlementID *uuid.UUID `json:"settlement_id,omitempty"`
}

type ListEntriesInput struct {
	Status        TransactionStatus
	LedgerID      uuid.UUID
	AccountID     uuid.UUID
	TransactionID uuid.UUID
	StatementID   uuid.UUID
	SettlementID  uuid.UUID

	Settled   *bool
	Side      Side
	Metadata  map[string]string
	Effective EffectiveRange
	After     int64
	Limit     int
}

type Statement struct {
	ID          uuid.UUID      `json:"id"`
	LedgerID    uuid.UUID      `json:"ledger_id"`
	AccountID   uuid.UUID      `json:"account_id"`
	Currency    money.Currency `json:"currency"`
	Description string         `json:"description"`

	From       time.Time `json:"effective_at_lower_bound"`
	Until      time.Time `json:"effective_at_upper_bound"`
	Starting   Balance   `json:"starting_balance"`
	Ending     Balance   `json:"ending_balance"`
	EntryCount int64     `json:"entry_count"`
	CreatedAt  time.Time `json:"created_at"`

	postedBefore uint64
}

type CreateStatementInput struct {
	AccountID   uuid.UUID `json:"account_id"`
	Description string    `json:"description"`
	From        time.Time `json:"effective_at_lower_bound"`
	Until       time.Time `json:"effective_at_upper_bound"`
}

type ListStatementsInput struct {
	AccountID uuid.UUID
	Before    uuid.UUID
	Limit     int
}

type StatementLine struct {
	PostingID     int64          `json:"posting_id,string"`
	TransactionID uuid.UUID      `json:"transaction_id"`
	Side          Side           `json:"side"`
	Amount        money.Amount   `json:"amount"`
	Currency      money.Currency `json:"currency"`
	BalanceAfter  money.Amount   `json:"balance_after"`
	CreatedAt     time.Time      `json:"created_at"`
}
