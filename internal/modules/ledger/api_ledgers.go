package ledger

import (
	"encoding/json/jsontext"
	"time"

	"github.com/pandabase/astrum/internal/money"
)

type ledgerResource struct {
	Object       string         `json:"object"`
	ID           ledgerID       `json:"id"`
	Name         string         `json:"name"`
	Description  string         `json:"description"`
	Metadata     jsontext.Value `json:"metadata"`
	ClosedBefore *time.Time     `json:"closed_before"`
	Version      int64          `json:"version"`
	CreatedAt    time.Time      `json:"created_at"`
}

func toLedger(l Ledger) ledgerResource {
	return ledgerResource{
		Object:       "ledger",
		ID:           ledgerID(l.ID),
		Name:         l.Name,
		Description:  l.Description,
		Metadata:     l.Metadata,
		ClosedBefore: l.ClosedBefore,
		Version:      l.Version,
		CreatedAt:    l.CreatedAt,
	}
}

type currencyResource struct {
	Object    string         `json:"object"`
	Code      money.Currency `json:"code"`
	Exponent  int            `json:"exponent"`
	CreatedAt time.Time      `json:"created_at"`
}

func toCurrency(c Currency) currencyResource {
	return currencyResource{Object: "currency", Code: c.Code, Exponent: c.Exponent, CreatedAt: c.CreatedAt}
}

type integrityReport struct {
	Object    string   `json:"object"`
	OK        bool     `json:"ok"`
	Issues    []string `json:"issues"`
	ChainHead string   `json:"chain_head"`
}

type ledgerRequest struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Metadata    jsontext.Value `json:"metadata,omitzero"`
}

type currencyRequest struct {
	Code     money.Currency `json:"code"`
	Exponent *int           `json:"exponent"`
}
