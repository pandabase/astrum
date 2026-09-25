package ledger

import (
	"encoding/json/jsontext"
	"strings"
	"time"

	"github.com/pandabase/astrum/internal/money"
)

type categoryResource struct {
	Object      string         `json:"object"`
	ID          categoryID     `json:"id"`
	LedgerID    ledgerID       `json:"ledger_id"`
	Currency    money.Currency `json:"currency"`
	NormalSide  Side           `json:"normal_side"`
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Metadata    jsontext.Value `json:"metadata"`
	Version     int64          `json:"version"`
	Balances    Balances       `json:"balances"`
	CreatedAt   time.Time      `json:"created_at"`
}

func toCategory(c Category) categoryResource {
	return categoryResource{
		Object:      "account_category",
		ID:          categoryID(c.ID),
		LedgerID:    ledgerID(c.LedgerID),
		Currency:    c.Currency,
		NormalSide:  c.NormalSide,
		Name:        c.Name,
		Description: c.Description,
		Metadata:    c.Metadata,
		Version:     c.Version,
		Balances:    c.Balances,
		CreatedAt:   c.CreatedAt,
	}
}

type categoryRequest struct {
	LedgerID    ledgerID       `json:"ledger_id"`
	Currency    money.Currency `json:"currency"`
	NormalSide  Side           `json:"normal_side"`
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Metadata    jsontext.Value `json:"metadata,omitzero"`
}

type alertCondition struct {
	Field    string       `json:"field"`
	Operator string       `json:"operator"`
	Value    money.Amount `json:"value"`
}

type monitorResource struct {
	Object         string         `json:"object"`
	ID             monitorID      `json:"id"`
	AccountID      accountID      `json:"account_id"`
	AlertCondition alertCondition `json:"alert_condition"`
	Description    string         `json:"description"`
	Metadata       jsontext.Value `json:"metadata"`
	Version        int64          `json:"version"`
	Triggered      bool           `json:"triggered"`

	Balances  *Balances `json:"balances,omitzero"`
	CreatedAt time.Time `json:"created_at"`
}

func toMonitor(m BalanceMonitor, balances *Balances) monitorResource {
	return monitorResource{
		Object:    "balance_monitor",
		ID:        monitorID(m.ID),
		AccountID: accountID(m.AccountID),
		AlertCondition: alertCondition{
			Field:    m.Condition.Field + "_balance_amount",
			Operator: m.Condition.Operator,
			Value:    m.Condition.Value,
		},
		Description: m.Description,
		Metadata:    m.Metadata,
		Version:     m.Version,
		Triggered:   m.Triggered,
		Balances:    balances,
		CreatedAt:   m.CreatedAt,
	}
}

type monitorRequest struct {
	AccountID      accountID      `json:"account_id"`
	AlertCondition alertCondition `json:"alert_condition"`
	Description    string         `json:"description"`
	Metadata       jsontext.Value `json:"metadata,omitzero"`
}

func (in monitorRequest) create() CreateBalanceMonitorInput {
	field, _ := strings.CutSuffix(in.AlertCondition.Field, "_balance_amount")
	return CreateBalanceMonitorInput{
		AccountID:   in.AccountID.UUID(),
		Condition:   AlertCondition{Field: field, Operator: in.AlertCondition.Operator, Value: in.AlertCondition.Value},
		Description: in.Description,
		Metadata:    in.Metadata,
	}
}

type accountResource struct {
	Object           string         `json:"object"`
	ID               accountID      `json:"id"`
	LedgerID         ledgerID       `json:"ledger_id"`
	Code             string         `json:"code"`
	Name             string         `json:"name"`
	Description      string         `json:"description"`
	Metadata         jsontext.Value `json:"metadata"`
	Currency         money.Currency `json:"currency"`
	CurrencyExponent int            `json:"currency_exponent"`
	NormalSide       Side           `json:"normal_side"`
	AllowNegative    bool           `json:"allow_negative"`
	OverdraftLimit   money.Amount   `json:"overdraft_limit"`
	Balances         balances       `json:"balances"`
	Held             money.Amount   `json:"held"`
	Status           AccountStatus  `json:"status"`
	LockVersion      int64          `json:"lock_version"`
	StatusChangedAt  *time.Time     `json:"status_changed_at"`
	CreatedAt        time.Time      `json:"created_at"`
}

type balances struct {
	Pending   Balance `json:"pending"`
	Posted    Balance `json:"posted"`
	Available Balance `json:"available"`
}

func toAccount(a Account) accountResource {
	return accountResource{
		Object:           "account",
		ID:               accountID(a.ID),
		LedgerID:         ledgerID(a.LedgerID),
		Code:             a.Code,
		Name:             a.Name,
		Description:      a.Description,
		Metadata:         a.Metadata,
		Currency:         a.Currency,
		CurrencyExponent: a.CurrencyExponent,
		NormalSide:       a.NormalSide,
		AllowNegative:    a.AllowNegative,
		OverdraftLimit:   a.OverdraftLimit,
		Balances:         balances{Pending: a.Pending, Posted: a.Posted, Available: a.Available},
		Held:             a.Held,
		Status:           a.Status,
		LockVersion:      a.Version,
		StatusChangedAt:  a.StatusChangedAt,
		CreatedAt:        a.CreatedAt,
	}
}

type accountRequest struct {
	LedgerID       ledgerID       `json:"ledger_id"`
	Code           string         `json:"code"`
	Name           string         `json:"name"`
	Description    string         `json:"description"`
	Metadata       jsontext.Value `json:"metadata,omitzero"`
	Currency       money.Currency `json:"currency"`
	NormalSide     Side           `json:"normal_side"`
	AllowNegative  bool           `json:"allow_negative"`
	OverdraftLimit money.Amount   `json:"overdraft_limit"`
}

func (in accountRequest) create() CreateAccountInput {
	return CreateAccountInput{
		LedgerID:       in.LedgerID.UUID(),
		Code:           in.Code,
		Name:           in.Name,
		Description:    in.Description,
		Metadata:       in.Metadata,
		Currency:       in.Currency,
		NormalSide:     in.NormalSide,
		AllowNegative:  in.AllowNegative,
		OverdraftLimit: in.OverdraftLimit,
	}
}
