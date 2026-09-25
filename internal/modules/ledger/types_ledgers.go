package ledger

import (
	"encoding/json/jsontext"
	"time"

	"github.com/google/uuid"
	"github.com/pandabase/astrum/internal/money"
)

type Currency struct {
	Code      money.Currency `json:"code"`
	Exponent  int            `json:"exponent"`
	CreatedAt time.Time      `json:"created_at"`
}

type CreateCurrencyInput struct {
	Code     money.Currency `json:"code"`
	Exponent int            `json:"exponent"`
}

type Ledger struct {
	ID           uuid.UUID      `json:"id"`
	Name         string         `json:"name"`
	Description  string         `json:"description"`
	Metadata     jsontext.Value `json:"metadata"`
	ClosedBefore *time.Time     `json:"closed_before"`
	Version      int64          `json:"version"`
	CreatedAt    time.Time      `json:"created_at"`
}

type CreateLedgerInput struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Metadata    jsontext.Value `json:"metadata,omitzero"`
}

type ListLedgersInput struct {
	Metadata map[string]string
	Before   uuid.UUID
	Limit    int
}

type VerifyReport struct {
	OK     bool     `json:"ok"`
	Issues []string `json:"issues"`

	ChainHead string `json:"chain_head"`
}
