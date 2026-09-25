package ledger

import (
	"encoding/json/jsontext"
	"time"

	"github.com/google/uuid"
	"github.com/pandabase/astrum/internal/money"
)

type ListHoldsInput struct {
	AccountID uuid.UUID
	Status    HoldStatus
	Before    uuid.UUID
	Limit     int
}

type ListSchedulesInput struct {
	Status ScheduleStatus
	Before uuid.UUID
	Limit  int
}

type HoldStatus string

const (
	HoldPending  HoldStatus = "pending"
	HoldCaptured HoldStatus = "captured"
	HoldVoided   HoldStatus = "voided"
	HoldExpired  HoldStatus = "expired"
)

type Hold struct {
	ID                   uuid.UUID      `json:"id"`
	IdempotencyKey       string         `json:"idempotency_key"`
	AccountID            uuid.UUID      `json:"account_id"`
	Currency             money.Currency `json:"currency"`
	Amount               money.Amount   `json:"amount"`
	Status               HoldStatus     `json:"status"`
	Description          string         `json:"description"`
	ExpiresAt            time.Time      `json:"expires_at"`
	CreatedAt            time.Time      `json:"created_at"`
	ResolvedAt           *time.Time     `json:"resolved_at,omitempty"`
	CapturedAmount       *money.Amount  `json:"captured_amount,omitempty"`
	CaptureTransactionID *uuid.UUID     `json:"capture_transaction_id,omitempty"`
}

type CreateHoldInput struct {
	IdempotencyKey string         `json:"idempotency_key"`
	AccountID      uuid.UUID      `json:"account_id"`
	Amount         money.Amount   `json:"amount"`
	Currency       money.Currency `json:"currency,omitzero"`
	Description    string         `json:"description"`
	ExpiresAt      time.Time      `json:"expires_at"`
}

type CaptureInput struct {
	IdempotencyKey string         `json:"idempotency_key"`
	Destination    uuid.UUID      `json:"destination_account_id"`
	Amount         money.Amount   `json:"amount"`
	Description    string         `json:"description"`
	Metadata       jsontext.Value `json:"metadata,omitzero"`
}

type ScheduleStatus string

const (
	ScheduleScheduled ScheduleStatus = "scheduled"
	ScheduleExecuted  ScheduleStatus = "executed"
	ScheduleFailed    ScheduleStatus = "failed"
	ScheduleCanceled  ScheduleStatus = "canceled"
)

type ScheduleInput struct {
	PostInput
	ExecuteAt time.Time `json:"execute_at"`
}

type ScheduledTransaction struct {
	ID             uuid.UUID      `json:"id"`
	IdempotencyKey string         `json:"idempotency_key"`
	ExecuteAt      time.Time      `json:"execute_at"`
	Request        PostInput      `json:"request"`
	Status         ScheduleStatus `json:"status"`
	TransactionID  *uuid.UUID     `json:"transaction_id,omitempty"`
	Failure        *string        `json:"failure,omitzero"`
	CreatedAt      time.Time      `json:"created_at"`
	ResolvedAt     *time.Time     `json:"resolved_at,omitempty"`
}
