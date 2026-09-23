package ledger

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/pandabase/astrum/internal/money"
)

var (
	ErrNotFound            = errors.New("ledger: not found")
	ErrAccountExists       = errors.New("ledger: account code already exists with different terms")
	ErrAccountNotOpen      = errors.New("ledger: account is not open")
	ErrAccountNotEmpty     = errors.New("ledger: account must have zero balance and no holds to close")
	ErrCurrencyExists      = errors.New("ledger: currency already exists with a different exponent")
	ErrUnknownCurrency     = errors.New("ledger: currency is not registered")
	ErrUnknownLedger       = errors.New("ledger: ledger does not exist")
	ErrCrossLedger         = errors.New("ledger: transaction spans more than one ledger")
	ErrNotPending          = errors.New("ledger: transaction is not pending")
	ErrNotPosted           = errors.New("ledger: transaction is not posted")
	ErrExternalIDExists    = errors.New("ledger: external_id already belongs to another transaction in this ledger")
	ErrBalanceLock         = errors.New("ledger: balance lock failed")
	ErrLockVersion         = errors.New("ledger: account lock_version has moved")
	ErrCategoryCycle       = errors.New("ledger: category would contain itself")
	ErrCategoryDepth       = errors.New("ledger: categories nest at most 7 levels deep")
	ErrCategoryMismatch    = errors.New("ledger: category members must share its ledger and currency")
	ErrInvalid             = errors.New("ledger: invalid input")
	ErrUnbalanced          = errors.New("ledger: transaction is unbalanced")
	ErrInsufficientFunds   = errors.New("ledger: insufficient funds")
	ErrIdempotencyConflict = errors.New("ledger: idempotency key reused with a different payload")
	ErrAlreadyReversed     = errors.New("ledger: transaction already reversed")
	ErrHoldNotPending      = errors.New("ledger: hold is not pending")
	ErrScheduleNotPending  = errors.New("ledger: scheduled transaction is not pending")
	ErrBatchAborted        = errors.New("ledger: batch aborted by another entry")
	ErrStopped             = errors.New("ledger: module is not running")
)

type Side string

const (
	Debit  Side = "debit"
	Credit Side = "credit"
)

func (s Side) valid() bool {
	return s == Debit || s == Credit
}

func (s Side) opposite() Side {
	if s == Debit {
		return Credit
	}
	return Debit
}

type AccountStatus string

const (
	AccountOpen AccountStatus = "open"

	AccountFrozen AccountStatus = "frozen"

	AccountClosed AccountStatus = "closed"
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
	ID          uuid.UUID       `json:"id"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Metadata    json.RawMessage `json:"metadata"`
	Version     int64           `json:"version"`
	CreatedAt   time.Time       `json:"created_at"`
}

type CreateLedgerInput struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Metadata    json.RawMessage `json:"metadata,omitempty"`
}

type UpdateInput struct {
	Name        *string         `json:"name"`
	Description *string         `json:"description"`
	Metadata    json.RawMessage `json:"metadata,omitempty"`
}

type ListLedgersInput struct {
	Metadata map[string]string
	Before   uuid.UUID
	Limit    int
}

type Account struct {
	ID               uuid.UUID       `json:"id"`
	LedgerID         uuid.UUID       `json:"ledger_id"`
	Code             string          `json:"code"`
	Name             string          `json:"name"`
	Description      string          `json:"description"`
	Metadata         json.RawMessage `json:"metadata"`
	Currency         money.Currency  `json:"currency"`
	CurrencyExponent int             `json:"currency_exponent"`
	NormalSide       Side            `json:"normal_side"`
	AllowNegative    bool            `json:"allow_negative"`

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

	Code           string          `json:"code"`
	Name           string          `json:"name"`
	Description    string          `json:"description"`
	Metadata       json.RawMessage `json:"metadata,omitempty"`
	Currency       money.Currency  `json:"currency"`
	NormalSide     Side            `json:"normal_side"`
	AllowNegative  bool            `json:"allow_negative"`
	OverdraftLimit money.Amount    `json:"overdraft_limit"`
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

type ListTransactionsInput struct {
	LedgerID   uuid.UUID
	Status     TransactionStatus
	ExternalID string
	AccountID  uuid.UUID
	Metadata   map[string]string
	Effective  EffectiveRange
	Before     uuid.UUID
	Limit      int
}

type EffectiveRange struct {
	From  *time.Time
	Until *time.Time
}

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

type Posting struct {
	AccountID uuid.UUID      `json:"account_id"`
	Side      Side           `json:"side"`
	Amount    money.Amount   `json:"amount"`
	Currency  money.Currency `json:"currency,omitempty"`

	PendingBalance   *BalanceCondition `json:"pending_balance_amount,omitempty"`
	PostedBalance    *BalanceCondition `json:"posted_balance_amount,omitempty"`
	AvailableBalance *BalanceCondition `json:"available_balance_amount,omitempty"`

	LockVersion *int64 `json:"lock_version,omitempty"`

	Resulting *Balances `json:"resulting_balances,omitempty"`

	balanceAfter money.Amount
}

type BalanceMonitor struct {
	ID          uuid.UUID       `json:"id"`
	AccountID   uuid.UUID       `json:"account_id"`
	Condition   AlertCondition  `json:"alert_condition"`
	Description string          `json:"description"`
	Metadata    json.RawMessage `json:"metadata"`
	Version     int64           `json:"version"`
	CreatedAt   time.Time       `json:"created_at"`

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
	AccountID   uuid.UUID       `json:"account_id"`
	Condition   AlertCondition  `json:"alert_condition"`
	Description string          `json:"description"`
	Metadata    json.RawMessage `json:"metadata,omitempty"`
}

type ListBalanceMonitorsInput struct {
	AccountID uuid.UUID
	Before    uuid.UUID
	Limit     int
}

type Balances struct {
	Pending   Balance `json:"pending"`
	Posted    Balance `json:"posted"`
	Available Balance `json:"available"`
}

type BalanceCondition struct {
	GT    *money.Amount `json:"gt,omitempty"`
	GTE   *money.Amount `json:"gte,omitempty"`
	EQ    *money.Amount `json:"eq,omitempty"`
	LT    *money.Amount `json:"lt,omitempty"`
	LTE   *money.Amount `json:"lte,omitempty"`
	NotEQ *money.Amount `json:"not_eq,omitempty"`
}

func (c *BalanceCondition) violation(v money.Amount) string {
	if c == nil {
		return ""
	}
	bounds := []struct {
		op    string
		bound *money.Amount
		holds func(cmp int) bool
	}{
		{"gt", c.GT, func(cmp int) bool { return cmp > 0 }},
		{"gte", c.GTE, func(cmp int) bool { return cmp >= 0 }},
		{"eq", c.EQ, func(cmp int) bool { return cmp == 0 }},
		{"lt", c.LT, func(cmp int) bool { return cmp < 0 }},
		{"lte", c.LTE, func(cmp int) bool { return cmp <= 0 }},
		{"not_eq", c.NotEQ, func(cmp int) bool { return cmp != 0 }},
	}
	for _, b := range bounds {
		if b.bound != nil && !b.holds(v.Cmp(*b.bound)) {
			return fmt.Sprintf("%s is not %s %s", v, b.op, *b.bound)
		}
	}
	return ""
}

func (p Posting) checkLocks(i int, b Balances) error {
	for _, lock := range []struct {
		name      string
		condition *BalanceCondition
		amount    money.Amount
	}{
		{"pending_balance_amount", p.PendingBalance, b.Pending.Amount},
		{"posted_balance_amount", p.PostedBalance, b.Posted.Amount},
		{"available_balance_amount", p.AvailableBalance, b.Available.Amount},
	} {
		if v := lock.condition.violation(lock.amount); v != "" {
			return fmt.Errorf("%w: entry %d %s: %s", ErrBalanceLock, i, lock.name, v)
		}
	}
	return nil
}

func (p Posting) signedAmount() money.Amount {
	if p.Side == Credit {
		return p.Amount.Neg()
	}
	return p.Amount
}

type Category struct {
	ID          uuid.UUID       `json:"id"`
	LedgerID    uuid.UUID       `json:"ledger_id"`
	Currency    money.Currency  `json:"currency"`
	NormalSide  Side            `json:"normal_side"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Metadata    json.RawMessage `json:"metadata"`
	Version     int64           `json:"version"`
	CreatedAt   time.Time       `json:"created_at"`
	Balances    Balances        `json:"balances"`
}

type CreateCategoryInput struct {
	LedgerID    uuid.UUID       `json:"ledger_id"`
	Currency    money.Currency  `json:"currency"`
	NormalSide  Side            `json:"normal_side"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Metadata    json.RawMessage `json:"metadata,omitempty"`
}

type ListCategoriesInput struct {
	LedgerID  uuid.UUID
	ParentID  uuid.UUID
	AccountID uuid.UUID
	Metadata  map[string]string
	Before    uuid.UUID
	Limit     int
}

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
	ErrorCode     *string    `json:"error_code,omitempty"`
	ErrorDetail   *string    `json:"error_detail,omitempty"`
}

type Settlement struct {
	ID               uuid.UUID      `json:"id"`
	IdempotencyKey   string         `json:"idempotency_key"`
	LedgerID         uuid.UUID      `json:"ledger_id"`
	SettledAccountID uuid.UUID      `json:"settled_account_id"`
	ContraAccountID  uuid.UUID      `json:"contra_account_id"`
	Currency         money.Currency `json:"currency"`
	UpperBound       *time.Time     `json:"effective_at_upper_bound,omitempty"`

	Amount        money.Amount    `json:"amount"`
	EntryCount    int             `json:"entry_count"`
	TransactionID *uuid.UUID      `json:"transaction_id,omitempty"`
	Description   string          `json:"description"`
	Metadata      json.RawMessage `json:"metadata"`
	CreatedAt     time.Time       `json:"created_at"`
}

type CreateSettlementInput struct {
	IdempotencyKey   string    `json:"idempotency_key"`
	SettledAccountID uuid.UUID `json:"settled_account_id"`
	ContraAccountID  uuid.UUID `json:"contra_account_id"`

	UpperBound  *time.Time      `json:"effective_at_upper_bound,omitempty"`
	Description string          `json:"description"`
	Metadata    json.RawMessage `json:"metadata,omitempty"`
}

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

type ListSettlementsInput struct {
	AccountID uuid.UUID
	Before    uuid.UUID
	Limit     int
}

type Balance struct {
	Debits  money.Amount `json:"debits"`
	Credits money.Amount `json:"credits"`
	Amount  money.Amount `json:"amount"`
}

type TransactionStatus string

const (
	TransactionPending TransactionStatus = "pending"

	TransactionPosted TransactionStatus = "posted"

	TransactionArchived TransactionStatus = "archived"
)

type PostInput struct {
	IdempotencyKey string          `json:"idempotency_key"`
	Description    string          `json:"description"`
	Metadata       json.RawMessage `json:"metadata,omitempty"`
	Postings       []Posting       `json:"postings"`

	Status TransactionStatus `json:"status,omitempty"`

	EffectiveAt *time.Time `json:"effective_at,omitempty"`

	ExternalID string `json:"external_id,omitempty"`

	ArchiveOnLockFailure bool `json:"archive_on_balance_lock_failure,omitempty"`
}

func (in PostInput) status() TransactionStatus {
	if in.Status == "" {
		return TransactionPosted
	}
	return in.Status
}

type Transaction struct {
	ID             uuid.UUID         `json:"id"`
	LedgerID       uuid.UUID         `json:"ledger_id"`
	IdempotencyKey string            `json:"idempotency_key"`
	ExternalID     string            `json:"external_id,omitempty"`
	Status         TransactionStatus `json:"status"`
	Version        int               `json:"version"`
	Description    string            `json:"description"`
	Metadata       json.RawMessage   `json:"metadata"`
	ReversesID     *uuid.UUID        `json:"reverses_id,omitempty"`

	Postings    []Posting  `json:"postings"`
	EffectiveAt time.Time  `json:"effective_at"`
	CreatedAt   time.Time  `json:"created_at"`
	PostedAt    *time.Time `json:"posted_at,omitempty"`
	ArchivedAt  *time.Time `json:"archived_at,omitempty"`

	request *PostInput

	entriesVersion int
}

func (t Transaction) matches(in PostInput) bool {
	if t.request != nil {
		r := *t.request
		sameTime := (r.EffectiveAt == nil && in.EffectiveAt == nil) ||
			(r.EffectiveAt != nil && in.EffectiveAt != nil && r.EffectiveAt.Equal(*in.EffectiveAt))
		return sameTime && r.status() == in.status() && sameContent(r, in)
	}
	if in.status() != TransactionPosted || (in.EffectiveAt != nil && !in.EffectiveAt.Equal(t.EffectiveAt)) {
		return false
	}
	return sameContent(PostInput{
		Description: t.Description,
		Metadata:    t.Metadata,
		Postings:    t.Postings,
		ExternalID:  t.ExternalID,
	}, in)
}

func sameContent(stored, in PostInput) bool {
	if stored.Description != in.Description || stored.ExternalID != in.ExternalID || len(stored.Postings) != len(in.Postings) {
		return false
	}
	if !jsonEqual(stored.Metadata, in.Metadata) {
		return false
	}
	for i, p := range in.Postings {
		existing := stored.Postings[i]
		if existing.AccountID != p.AccountID || existing.Side != p.Side || existing.Amount != p.Amount {
			return false
		}
		if p.Currency != "" && existing.Currency != "" && p.Currency != existing.Currency {
			return false
		}
	}
	return true
}

type UpdateTransactionInput struct {
	Description *string         `json:"description"`
	Metadata    json.RawMessage `json:"metadata,omitempty"`
	Postings    []Posting       `json:"postings"`
	EffectiveAt *time.Time      `json:"effective_at"`
}

type PostPendingInput struct {
	Postings []Posting `json:"postings"`
}

type BatchResult struct {
	Transaction *Transaction `json:"transaction,omitempty"`
	Replayed    bool         `json:"replayed,omitempty"`
	Error       string       `json:"error,omitempty"`
	Err         error        `json:"-"`
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
	Currency       money.Currency `json:"currency,omitempty"`
	Description    string         `json:"description"`
	ExpiresAt      time.Time      `json:"expires_at"`
}

type CaptureInput struct {
	IdempotencyKey string          `json:"idempotency_key"`
	Destination    uuid.UUID       `json:"destination_account_id"`
	Amount         money.Amount    `json:"amount"`
	Description    string          `json:"description"`
	Metadata       json.RawMessage `json:"metadata,omitempty"`
}

type ReverseInput struct {
	IdempotencyKey string          `json:"idempotency_key"`
	Description    string          `json:"description"`
	Metadata       json.RawMessage `json:"metadata,omitempty"`
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
	Failure        *string        `json:"failure,omitempty"`
	CreatedAt      time.Time      `json:"created_at"`
	ResolvedAt     *time.Time     `json:"resolved_at,omitempty"`
}

type VerifyReport struct {
	OK     bool     `json:"ok"`
	Issues []string `json:"issues"`

	ChainHead string `json:"chain_head"`
}

func jsonEqual(a, b json.RawMessage) bool {
	decode := func(raw json.RawMessage) (any, bool) {
		if len(bytes.TrimSpace(raw)) == 0 {
			return map[string]any{}, true
		}
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber()
		var v any
		return v, dec.Decode(&v) == nil
	}
	av, okA := decode(a)
	bv, okB := decode(b)
	return okA && okB && jsonValueEqual(av, bv)
}

func jsonValueEqual(a, b any) bool {
	switch a := a.(type) {
	case map[string]any:
		b, ok := b.(map[string]any)
		if !ok || len(a) != len(b) {
			return false
		}
		for k, av := range a {
			bv, ok := b[k]
			if !ok || !jsonValueEqual(av, bv) {
				return false
			}
		}
		return true
	case []any:
		b, ok := b.([]any)
		if !ok || len(a) != len(b) {
			return false
		}
		for i := range a {
			if !jsonValueEqual(a[i], b[i]) {
				return false
			}
		}
		return true
	case json.Number:
		b, ok := b.(json.Number)
		return ok && canonicalNumber(a) == canonicalNumber(b)
	default:
		return a == b
	}
}

func canonicalNumber(n json.Number) string {
	s := string(n)
	sign := ""
	if strings.HasPrefix(s, "-") {
		sign, s = "-", s[1:]
	}
	exp := 0
	if i := strings.IndexAny(s, "eE"); i >= 0 {
		e, err := strconv.Atoi(s[i+1:])
		if err != nil {
			return string(n)
		}
		exp, s = e, s[:i]
	}
	digits := s
	if i := strings.IndexByte(s, '.'); i >= 0 {
		digits = s[:i] + s[i+1:]
		exp -= len(s) - i - 1
	}
	digits = strings.TrimLeft(digits, "0")
	if digits == "" {
		return "0"
	}
	trimmed := strings.TrimRight(digits, "0")
	exp += len(digits) - len(trimmed)
	return sign + trimmed + "e" + strconv.Itoa(exp)
}
