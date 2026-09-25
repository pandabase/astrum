package ledger

import (
	"encoding/json/jsontext"
	"time"

	"github.com/pandabase/astrum/internal/money"
)

type holdResource struct {
	Object               string         `json:"object"`
	ID                   holdID         `json:"id"`
	IdempotencyKey       string         `json:"idempotency_key"`
	AccountID            accountID      `json:"account_id"`
	Amount               money.Amount   `json:"amount"`
	Currency             money.Currency `json:"currency"`
	Status               HoldStatus     `json:"status"`
	Description          string         `json:"description"`
	ExpiresAt            time.Time      `json:"expires_at"`
	CapturedAmount       *money.Amount  `json:"captured_amount"`
	CaptureTransactionID *transactionID `json:"capture_transaction_id"`
	CreatedAt            time.Time      `json:"created_at"`
	ResolvedAt           *time.Time     `json:"resolved_at"`
}

func toHold(h Hold) holdResource {
	return holdResource{
		Object:               "hold",
		ID:                   holdID(h.ID),
		IdempotencyKey:       h.IdempotencyKey,
		AccountID:            accountID(h.AccountID),
		Amount:               h.Amount,
		Currency:             h.Currency,
		Status:               h.Status,
		Description:          h.Description,
		ExpiresAt:            h.ExpiresAt,
		CapturedAmount:       h.CapturedAmount,
		CaptureTransactionID: (*transactionID)(h.CaptureTransactionID),
		CreatedAt:            h.CreatedAt,
		ResolvedAt:           h.ResolvedAt,
	}
}

type scheduleResource struct {
	Object         string         `json:"object"`
	ID             scheduleID     `json:"id"`
	IdempotencyKey string         `json:"idempotency_key"`
	ExecuteAt      time.Time      `json:"execute_at"`
	Status         ScheduleStatus `json:"status"`
	Description    string         `json:"description"`
	Metadata       jsontext.Value `json:"metadata"`
	Entries        []entryLine    `json:"entries"`
	TransactionID  *transactionID `json:"transaction_id"`
	Failure        *string        `json:"failure"`
	CreatedAt      time.Time      `json:"created_at"`
	ResolvedAt     *time.Time     `json:"resolved_at"`
}

func toSchedule(st ScheduledTransaction) scheduleResource {
	metadata := st.Request.Metadata
	if len(metadata) == 0 {
		metadata = jsontext.Value(`{}`)
	}
	return scheduleResource{
		Object:         "scheduled_transaction",
		ID:             scheduleID(st.ID),
		IdempotencyKey: st.IdempotencyKey,
		ExecuteAt:      st.ExecuteAt,
		Status:         st.Status,
		Description:    st.Request.Description,
		Metadata:       metadata,
		Entries:        toEntries(st.Request.Postings),
		TransactionID:  (*transactionID)(st.TransactionID),
		Failure:        st.Failure,
		CreatedAt:      st.CreatedAt,
		ResolvedAt:     st.ResolvedAt,
	}
}

type holdRequest struct {
	AccountID   accountID      `json:"account_id"`
	Amount      money.Amount   `json:"amount"`
	Currency    money.Currency `json:"currency,omitzero"`
	Description string         `json:"description"`
	ExpiresAt   time.Time      `json:"expires_at"`
}

type captureRequest struct {
	DestinationAccountID accountID      `json:"destination_account_id"`
	Amount               money.Amount   `json:"amount"`
	Description          string         `json:"description"`
	Metadata             jsontext.Value `json:"metadata,omitzero"`
}

type scheduleRequest struct {
	transactionRequest
	ExecuteAt time.Time `json:"execute_at"`
}
