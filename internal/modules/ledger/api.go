package ledger

import (
	"encoding/json/jsontext"
	"strconv"
	"strings"
	"time"

	"github.com/pandabase/astrum/internal/kernel/typeid"
	"github.com/pandabase/astrum/internal/money"
)

type (
	ledgerPrefix      struct{}
	accountPrefix     struct{}
	transactionPrefix struct{}
	holdPrefix        struct{}
	schedulePrefix    struct{}
	statementPrefix   struct{}
	categoryPrefix    struct{}
	monitorPrefix     struct{}
	bulkPrefix        struct{}
	settlementPrefix  struct{}
)

func (ledgerPrefix) Prefix() string      { return "ldg" }
func (accountPrefix) Prefix() string     { return "acct" }
func (transactionPrefix) Prefix() string { return "txn" }
func (holdPrefix) Prefix() string        { return "hold" }
func (schedulePrefix) Prefix() string    { return "sched" }
func (statementPrefix) Prefix() string   { return "stmt" }
func (categoryPrefix) Prefix() string    { return "cat" }
func (monitorPrefix) Prefix() string     { return "bm" }
func (bulkPrefix) Prefix() string        { return "blk" }
func (settlementPrefix) Prefix() string  { return "stl" }

type (
	ledgerID      = typeid.ID[ledgerPrefix]
	accountID     = typeid.ID[accountPrefix]
	transactionID = typeid.ID[transactionPrefix]
	holdID        = typeid.ID[holdPrefix]
	scheduleID    = typeid.ID[schedulePrefix]
	statementID   = typeid.ID[statementPrefix]
	categoryID    = typeid.ID[categoryPrefix]
	monitorID     = typeid.ID[monitorPrefix]
	bulkID        = typeid.ID[bulkPrefix]
	settlementID  = typeid.ID[settlementPrefix]
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

type deleted struct {
	Object  string `json:"object"`
	ID      string `json:"id"`
	Deleted bool   `json:"deleted"`
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

type currencyResource struct {
	Object    string         `json:"object"`
	Code      money.Currency `json:"code"`
	Exponent  int            `json:"exponent"`
	CreatedAt time.Time      `json:"created_at"`
}

func toCurrency(c Currency) currencyResource {
	return currencyResource{Object: "currency", Code: c.Code, Exponent: c.Exponent, CreatedAt: c.CreatedAt}
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

type entryLine struct {
	AccountID              accountID         `json:"account_id"`
	Side                   Side              `json:"side"`
	Amount                 money.Amount      `json:"amount"`
	Currency               money.Currency    `json:"currency,omitzero"`
	PendingBalanceAmount   *BalanceCondition `json:"pending_balance_amount,omitzero"`
	PostedBalanceAmount    *BalanceCondition `json:"posted_balance_amount,omitzero"`
	AvailableBalanceAmount *BalanceCondition `json:"available_balance_amount,omitzero"`
	LockVersion            *int64            `json:"lock_version,omitzero"`
	ResultingBalances      *Balances         `json:"resulting_balances,omitzero"`
}

func toEntries(postings []Posting) []entryLine {
	out := make([]entryLine, len(postings))
	for i, p := range postings {
		out[i] = entryLine{
			AccountID:         accountID(p.AccountID),
			Side:              p.Side,
			Amount:            p.Amount,
			Currency:          p.Currency,
			ResultingBalances: p.Resulting,
		}
	}
	return out
}

func fromEntries(entries []entryLine) []Posting {
	out := make([]Posting, len(entries))
	for i, e := range entries {
		out[i] = Posting{
			AccountID:        e.AccountID.UUID(),
			Side:             e.Side,
			Amount:           e.Amount,
			Currency:         e.Currency,
			PendingBalance:   e.PendingBalanceAmount,
			PostedBalance:    e.PostedBalanceAmount,
			AvailableBalance: e.AvailableBalanceAmount,
			LockVersion:      e.LockVersion,
		}
	}
	return out
}

type transactionResource struct {
	Object         string            `json:"object"`
	ID             transactionID     `json:"id"`
	LedgerID       ledgerID          `json:"ledger_id"`
	IdempotencyKey string            `json:"idempotency_key"`
	ExternalID     *string           `json:"external_id"`
	Status         TransactionStatus `json:"status"`
	Version        int               `json:"version"`
	Description    string            `json:"description"`
	Metadata       jsontext.Value    `json:"metadata"`
	ReversesID     *transactionID    `json:"reverses_id"`
	Entries        []entryLine       `json:"entries"`
	EffectiveAt    time.Time         `json:"effective_at"`
	CreatedAt      time.Time         `json:"created_at"`
	PostedAt       *time.Time        `json:"posted_at"`
	ArchivedAt     *time.Time        `json:"archived_at"`
}

func toTransaction(t Transaction) transactionResource {
	return transactionResource{
		Object:         "transaction",
		ID:             transactionID(t.ID),
		LedgerID:       ledgerID(t.LedgerID),
		IdempotencyKey: t.IdempotencyKey,
		ExternalID:     nullString(t.ExternalID),
		Status:         t.Status,
		Version:        t.Version,
		Description:    t.Description,
		Metadata:       t.Metadata,
		ReversesID:     (*transactionID)(t.ReversesID),
		Entries:        toEntries(t.Postings),
		EffectiveAt:    t.EffectiveAt,
		CreatedAt:      t.CreatedAt,
		PostedAt:       t.PostedAt,
		ArchivedAt:     t.ArchivedAt,
	}
}

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

type balancesResource struct {
	Object                string     `json:"object"`
	AccountID             accountID  `json:"account_id"`
	EffectiveAtLowerBound *time.Time `json:"effective_at_lower_bound"`
	EffectiveAtUpperBound *time.Time `json:"effective_at_upper_bound"`
	Balances
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

type currencyRequest struct {
	Code     money.Currency `json:"code"`
	Exponent *int           `json:"exponent"`
}

type transactionRequest struct {
	Description string            `json:"description"`
	Metadata    jsontext.Value    `json:"metadata,omitzero"`
	Entries     []entryLine       `json:"entries"`
	Status      TransactionStatus `json:"status"`
	EffectiveAt *time.Time        `json:"effective_at"`
	ExternalID  string            `json:"external_id"`

	ArchiveOnBalanceLockFailure bool `json:"archive_on_balance_lock_failure"`
}

func (in transactionRequest) post(key string) PostInput {
	return PostInput{
		IdempotencyKey: key,
		Description:    in.Description,
		Metadata:       in.Metadata,
		Postings:       fromEntries(in.Entries),
		Status:         in.Status,
		EffectiveAt:    in.EffectiveAt,
		ExternalID:     in.ExternalID,

		ArchiveOnLockFailure: in.ArchiveOnBalanceLockFailure,
	}
}

type updateTransactionRequest struct {
	Description *string        `json:"description"`
	Metadata    jsontext.Value `json:"metadata,omitzero"`
	Entries     []entryLine    `json:"entries"`
	EffectiveAt *time.Time     `json:"effective_at"`
}

type postRequest struct {
	Entries []entryLine `json:"entries"`
}

type batchRequest struct {
	Atomic       *bool                `json:"atomic"`
	Transactions []transactionRequest `json:"transactions"`
}

type batchResponse struct {
	Object  string        `json:"object"`
	Atomic  bool          `json:"atomic"`
	Results []batchResult `json:"results"`
}

type batchResult struct {
	Transaction *transactionResource `json:"transaction"`
	Error       *batchError          `json:"error"`
}

type batchError struct {
	Code   string `json:"code"`
	Detail string `json:"detail"`
}

type reverseRequest struct {
	Description string         `json:"description"`
	Metadata    jsontext.Value `json:"metadata,omitzero"`
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
