package ledger

import (
	"encoding/json/jsontext"
	"time"

	"github.com/google/uuid"
	"github.com/pandabase/astrum/internal/money"
)

type BulkStatus string

const (
	BulkPending    BulkStatus = "pending"
	BulkProcessing BulkStatus = "processing"
	BulkCompleted  BulkStatus = "completed"
)

type BulkRequest struct {
	ID             uuid.UUID  `json:"id"`
	IdempotencyKey string     `json:"idempotency_key"`
	Status         BulkStatus `json:"status"`
	Total          int        `json:"total"`
	Processed      int        `json:"processed"`
	Succeeded      int        `json:"succeeded"`
	Failed         int        `json:"failed"`
	CreatedAt      time.Time  `json:"created_at"`
	StartedAt      *time.Time `json:"started_at,omitempty"`
	CompletedAt    *time.Time `json:"completed_at,omitempty"`
}

type CreateBulkInput struct {
	IdempotencyKey string      `json:"idempotency_key"`
	Transactions   []PostInput `json:"transactions"`
}

type BulkResult struct {
	Index         int        `json:"index"`
	TransactionID *uuid.UUID `json:"transaction_id,omitempty"`
	ErrorCode     *string    `json:"error_code,omitzero"`
	ErrorDetail   *string    `json:"error_detail,omitzero"`
}

type Settlement struct {
	ID               uuid.UUID      `json:"id"`
	IdempotencyKey   string         `json:"idempotency_key"`
	LedgerID         uuid.UUID      `json:"ledger_id"`
	SettledAccountID uuid.UUID      `json:"settled_account_id"`
	ContraAccountID  uuid.UUID      `json:"contra_account_id"`
	Currency         money.Currency `json:"currency"`
	UpperBound       *time.Time     `json:"effective_at_upper_bound,omitempty"`

	Amount        money.Amount   `json:"amount"`
	EntryCount    int            `json:"entry_count"`
	TransactionID *uuid.UUID     `json:"transaction_id,omitempty"`
	Description   string         `json:"description"`
	Metadata      jsontext.Value `json:"metadata"`
	CreatedAt     time.Time      `json:"created_at"`
}

type CreateSettlementInput struct {
	IdempotencyKey   string    `json:"idempotency_key"`
	SettledAccountID uuid.UUID `json:"settled_account_id"`
	ContraAccountID  uuid.UUID `json:"contra_account_id"`

	UpperBound  *time.Time     `json:"effective_at_upper_bound,omitempty"`
	Description string         `json:"description"`
	Metadata    jsontext.Value `json:"metadata,omitzero"`
}

type ListSettlementsInput struct {
	AccountID uuid.UUID
	Before    uuid.UUID
	Limit     int
}
