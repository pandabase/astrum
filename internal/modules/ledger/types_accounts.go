package ledger

import (
	"encoding/json/jsontext"
	"time"

	"github.com/google/uuid"
	"github.com/pandabase/astrum/internal/money"
)

type AccountStatus string

const (
	AccountOpen AccountStatus = "open"

	AccountFrozen AccountStatus = "frozen"

	AccountClosed AccountStatus = "closed"
)

type Account struct {
	ID               uuid.UUID      `json:"id"`
	LedgerID         uuid.UUID      `json:"ledger_id"`
	Code             string         `json:"code"`
	Name             string         `json:"name"`
	Description      string         `json:"description"`
	Metadata         jsontext.Value `json:"metadata"`
	Currency         money.Currency `json:"currency"`
	CurrencyExponent int            `json:"currency_exponent"`
	NormalSide       Side           `json:"normal_side"`
	AllowNegative    bool           `json:"allow_negative"`

	OverdraftLimit money.Amount `json:"overdraft_limit"`

	Posted          Balance       `json:"posted"`
	Pending         Balance       `json:"pending"`
	Available       Balance       `json:"available"`
	Held            money.Amount  `json:"held"`
	Status          AccountStatus `json:"status"`
	StatusChangedAt *time.Time    `json:"status_changed_at,omitempty"`
	Version         int64         `json:"version"`
	CreatedAt       time.Time     `json:"created_at"`
}

type CreateAccountInput struct {
	LedgerID uuid.UUID `json:"ledger_id"`

	Code           string         `json:"code"`
	Name           string         `json:"name"`
	Description    string         `json:"description"`
	Metadata       jsontext.Value `json:"metadata,omitzero"`
	Currency       money.Currency `json:"currency"`
	NormalSide     Side           `json:"normal_side"`
	AllowNegative  bool           `json:"allow_negative"`
	OverdraftLimit money.Amount   `json:"overdraft_limit"`
}

type ListAccountsInput struct {
	LedgerID uuid.UUID

	CategoryID uuid.UUID
	Code       string
	Status     AccountStatus
	Currency   money.Currency
	Metadata   map[string]string
	Before     uuid.UUID
	Limit      int
}

type BalanceMonitor struct {
	ID          uuid.UUID      `json:"id"`
	AccountID   uuid.UUID      `json:"account_id"`
	Condition   AlertCondition `json:"alert_condition"`
	Description string         `json:"description"`
	Metadata    jsontext.Value `json:"metadata"`
	Version     int64          `json:"version"`
	CreatedAt   time.Time      `json:"created_at"`

	Triggered bool `json:"triggered"`
}

type AlertCondition struct {
	Field    string       `json:"field"`
	Operator string       `json:"operator"`
	Value    money.Amount `json:"value"`
}

func (c AlertCondition) holds(b Balances) bool {
	amount := b.Available.Amount
	switch c.Field {
	case "pending":
		amount = b.Pending.Amount
	case "posted":
		amount = b.Posted.Amount
	}
	cmp := amount.Cmp(c.Value)
	switch c.Operator {
	case "gt":
		return cmp > 0
	case "gte":
		return cmp >= 0
	case "eq":
		return cmp == 0
	case "lt":
		return cmp < 0
	case "lte":
		return cmp <= 0
	default:
		return cmp != 0
	}
}

type CreateBalanceMonitorInput struct {
	AccountID   uuid.UUID      `json:"account_id"`
	Condition   AlertCondition `json:"alert_condition"`
	Description string         `json:"description"`
	Metadata    jsontext.Value `json:"metadata,omitzero"`
}

type ListBalanceMonitorsInput struct {
	AccountID uuid.UUID
	Before    uuid.UUID
	Limit     int
}

type Category struct {
	ID          uuid.UUID      `json:"id"`
	LedgerID    uuid.UUID      `json:"ledger_id"`
	Currency    money.Currency `json:"currency"`
	NormalSide  Side           `json:"normal_side"`
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Metadata    jsontext.Value `json:"metadata"`
	Version     int64          `json:"version"`
	CreatedAt   time.Time      `json:"created_at"`
	Balances    Balances       `json:"balances"`
}

type CreateCategoryInput struct {
	LedgerID    uuid.UUID      `json:"ledger_id"`
	Currency    money.Currency `json:"currency"`
	NormalSide  Side           `json:"normal_side"`
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Metadata    jsontext.Value `json:"metadata,omitzero"`
}

type ListCategoriesInput struct {
	LedgerID  uuid.UUID
	ParentID  uuid.UUID
	AccountID uuid.UUID
	Metadata  map[string]string
	Before    uuid.UUID
	Limit     int
}
