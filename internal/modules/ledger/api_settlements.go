package ledger

import (
	"encoding/json/jsontext"
	"time"

	"github.com/pandabase/astrum/internal/money"
)

type bulkResource struct {
	Object         string     `json:"object"`
	ID             bulkID     `json:"id"`
	IdempotencyKey string     `json:"idempotency_key"`
	Status         BulkStatus `json:"status"`
	Total          int        `json:"total"`
	Processed      int        `json:"processed"`
	Succeeded      int        `json:"succeeded"`
	Failed         int        `json:"failed"`
	CreatedAt      time.Time  `json:"created_at"`
	StartedAt      *time.Time `json:"started_at"`
	CompletedAt    *time.Time `json:"completed_at"`
}

func toBulk(b BulkRequest) bulkResource {
	return bulkResource{
		Object:         "bulk_request",
		ID:             bulkID(b.ID),
		IdempotencyKey: b.IdempotencyKey,
		Status:         b.Status,
		Total:          b.Total,
		Processed:      b.Processed,
		Succeeded:      b.Succeeded,
		Failed:         b.Failed,
		CreatedAt:      b.CreatedAt,
		StartedAt:      b.StartedAt,
		CompletedAt:    b.CompletedAt,
	}
}

type bulkResultResource struct {
	Object        string         `json:"object"`
	Index         int            `json:"index"`
	Status        string         `json:"status"`
	TransactionID *transactionID `json:"transaction_id"`
	Error         *batchError    `json:"error"`
}

func toBulkResult(r BulkResult) bulkResultResource {
	out := bulkResultResource{Object: "bulk_result", Index: r.Index, Status: "pending", TransactionID: (*transactionID)(r.TransactionID)}
	switch {
	case r.TransactionID != nil:
		out.Status = "succeeded"
	case r.ErrorCode != nil:
		out.Status = "failed"
		out.Error = &batchError{Code: *r.ErrorCode, Detail: *r.ErrorDetail}
	}
	return out
}

type bulkRequest struct {
	Transactions []transactionRequest `json:"transactions"`
}

type settlementResource struct {
	Object                string         `json:"object"`
	ID                    settlementID   `json:"id"`
	IdempotencyKey        string         `json:"idempotency_key"`
	LedgerID              ledgerID       `json:"ledger_id"`
	SettledAccountID      accountID      `json:"settled_account_id"`
	ContraAccountID       accountID      `json:"contra_account_id"`
	Currency              money.Currency `json:"currency"`
	EffectiveAtUpperBound *time.Time     `json:"effective_at_upper_bound"`
	Amount                money.Amount   `json:"amount"`
	EntryCount            int            `json:"entry_count"`
	TransactionID         *transactionID `json:"transaction_id"`
	Description           string         `json:"description"`
	Metadata              jsontext.Value `json:"metadata"`
	CreatedAt             time.Time      `json:"created_at"`
}

func toSettlement(st Settlement) settlementResource {
	return settlementResource{
		Object:                "settlement",
		ID:                    settlementID(st.ID),
		IdempotencyKey:        st.IdempotencyKey,
		LedgerID:              ledgerID(st.LedgerID),
		SettledAccountID:      accountID(st.SettledAccountID),
		ContraAccountID:       accountID(st.ContraAccountID),
		Currency:              st.Currency,
		EffectiveAtUpperBound: st.UpperBound,
		Amount:                st.Amount,
		EntryCount:            st.EntryCount,
		TransactionID:         (*transactionID)(st.TransactionID),
		Description:           st.Description,
		Metadata:              st.Metadata,
		CreatedAt:             st.CreatedAt,
	}
}

type settlementRequest struct {
	SettledAccountID      accountID      `json:"settled_account_id"`
	ContraAccountID       accountID      `json:"contra_account_id"`
	EffectiveAtUpperBound *time.Time     `json:"effective_at_upper_bound"`
	Description           string         `json:"description"`
	Metadata              jsontext.Value `json:"metadata,omitzero"`
}
