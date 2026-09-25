package ledger

import (
	"strconv"
	"time"

	"github.com/pandabase/astrum/internal/money"
)

type accountEntry struct {
	Object        string         `json:"object"`
	TransactionID transactionID  `json:"transaction_id"`
	Side          Side           `json:"side"`
	Amount        money.Amount   `json:"amount"`
	Currency      money.Currency `json:"currency"`
	BalanceAfter  money.Amount   `json:"balance_after"`
	CreatedAt     time.Time      `json:"created_at"`
}

func toAccountEntry(l StatementLine) accountEntry {
	return accountEntry{
		Object:        "account_entry",
		TransactionID: transactionID(l.TransactionID),
		Side:          l.Side,
		Amount:        l.Amount,
		Currency:      l.Currency,
		BalanceAfter:  l.BalanceAfter,
		CreatedAt:     l.CreatedAt,
	}
}

type entryResource struct {
	Object        string            `json:"object"`
	Sequence      string            `json:"sequence"`
	TransactionID transactionID     `json:"transaction_id"`
	LedgerID      ledgerID          `json:"ledger_id"`
	AccountID     accountID         `json:"account_id"`
	Status        TransactionStatus `json:"status"`
	Side          Side              `json:"side"`
	Amount        money.Amount      `json:"amount"`
	Currency      money.Currency    `json:"currency"`
	BalanceAfter  *money.Amount     `json:"balance_after"`
	EffectiveAt   time.Time         `json:"effective_at"`
	CreatedAt     time.Time         `json:"created_at"`
	SettlementID  *settlementID     `json:"settlement_id"`
}

func toEntry(e Entry) entryResource {
	return entryResource{
		Object:        "entry",
		Sequence:      strconv.FormatInt(e.Sequence, 10),
		TransactionID: transactionID(e.TransactionID),
		LedgerID:      ledgerID(e.LedgerID),
		AccountID:     accountID(e.AccountID),
		Status:        e.Status,
		Side:          e.Side,
		Amount:        e.Amount,
		Currency:      e.Currency,
		BalanceAfter:  e.BalanceAfter,
		EffectiveAt:   e.EffectiveAt,
		CreatedAt:     e.CreatedAt,
		SettlementID:  (*settlementID)(e.SettlementID),
	}
}

type statementResource struct {
	Object                string         `json:"object"`
	ID                    statementID    `json:"id"`
	LedgerID              ledgerID       `json:"ledger_id"`
	AccountID             accountID      `json:"account_id"`
	Currency              money.Currency `json:"currency"`
	Description           string         `json:"description"`
	EffectiveAtLowerBound time.Time      `json:"effective_at_lower_bound"`
	EffectiveAtUpperBound time.Time      `json:"effective_at_upper_bound"`
	StartingBalance       Balance        `json:"starting_balance"`
	EndingBalance         Balance        `json:"ending_balance"`
	EntryCount            int64          `json:"entry_count"`
	CreatedAt             time.Time      `json:"created_at"`
}

func toStatement(st Statement) statementResource {
	return statementResource{
		Object:                "statement",
		ID:                    statementID(st.ID),
		LedgerID:              ledgerID(st.LedgerID),
		AccountID:             accountID(st.AccountID),
		Currency:              st.Currency,
		Description:           st.Description,
		EffectiveAtLowerBound: st.From,
		EffectiveAtUpperBound: st.Until,
		StartingBalance:       st.Starting,
		EndingBalance:         st.Ending,
		EntryCount:            st.EntryCount,
		CreatedAt:             st.CreatedAt,
	}
}

type statementRequest struct {
	AccountID             accountID `json:"account_id"`
	Description           string    `json:"description"`
	EffectiveAtLowerBound time.Time `json:"effective_at_lower_bound"`
	EffectiveAtUpperBound time.Time `json:"effective_at_upper_bound"`
}
