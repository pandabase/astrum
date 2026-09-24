package ledger

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/pandabase/astrum/internal/kernel/httpx"
	"github.com/pandabase/astrum/internal/kernel/idempotency"
	"github.com/pandabase/astrum/internal/kernel/typeid"
	"github.com/pandabase/astrum/internal/money"
)

const codeIdempotencyKeyRequired = "idempotency_key_required"

type handler struct {
	svc *service
}

func (h *handler) routes(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/ledgers", h.createLedger)
	mux.HandleFunc("GET /v1/ledgers", h.listLedgers)
	mux.HandleFunc("GET /v1/ledgers/{id}", h.getLedger)
	mux.HandleFunc("PATCH /v1/ledgers/{id}", h.updateLedger)
	mux.HandleFunc("POST /v1/ledgers/{id}/close_period", h.closePeriod)

	mux.HandleFunc("POST /v1/account_categories", h.createCategory)
	mux.HandleFunc("GET /v1/account_categories", h.listCategories)
	mux.HandleFunc("GET /v1/account_categories/{id}", h.getCategory)
	mux.HandleFunc("PATCH /v1/account_categories/{id}", h.updateCategory)
	mux.HandleFunc("DELETE /v1/account_categories/{id}", h.deleteCategory)
	mux.HandleFunc("PUT /v1/account_categories/{id}/accounts/{member}", h.setMember(true))
	mux.HandleFunc("DELETE /v1/account_categories/{id}/accounts/{member}", h.setMember(false))
	mux.HandleFunc("PUT /v1/account_categories/{id}/categories/{member}", h.setChild(true))
	mux.HandleFunc("DELETE /v1/account_categories/{id}/categories/{member}", h.setChild(false))

	mux.HandleFunc("POST /v1/balance_monitors", h.createMonitor)
	mux.HandleFunc("GET /v1/balance_monitors", h.listMonitors)
	mux.HandleFunc("GET /v1/balance_monitors/{id}", h.getMonitor)
	mux.HandleFunc("PATCH /v1/balance_monitors/{id}", h.updateMonitor)
	mux.HandleFunc("DELETE /v1/balance_monitors/{id}", h.deleteMonitor)

	mux.HandleFunc("POST /v1/currencies", h.createCurrency)
	mux.HandleFunc("GET /v1/currencies", h.listCurrencies)
	mux.HandleFunc("GET /v1/currencies/{code}", h.getCurrency)

	mux.HandleFunc("POST /v1/accounts", h.createAccount)
	mux.HandleFunc("GET /v1/accounts", h.listAccounts)
	mux.HandleFunc("GET /v1/accounts/{id}", h.getAccount)
	mux.HandleFunc("PATCH /v1/accounts/{id}", h.updateAccount)
	mux.HandleFunc("POST /v1/accounts/{id}/freeze", h.setAccountStatus(AccountFrozen))
	mux.HandleFunc("POST /v1/accounts/{id}/unfreeze", h.setAccountStatus(AccountOpen))
	mux.HandleFunc("POST /v1/accounts/{id}/close", h.setAccountStatus(AccountClosed))
	mux.HandleFunc("GET /v1/accounts/{id}/entries", h.listAccountEntries)
	mux.HandleFunc("GET /v1/accounts/{id}/balances", h.getBalances)

	mux.HandleFunc("GET /v1/entries", h.listEntries)

	mux.HandleFunc("POST /v1/bulk_requests", h.createBulk)
	mux.HandleFunc("GET /v1/bulk_requests/{id}", h.getBulk)
	mux.HandleFunc("GET /v1/bulk_requests/{id}/results", h.listBulkResults)

	mux.HandleFunc("POST /v1/settlements", h.createSettlement)
	mux.HandleFunc("GET /v1/settlements", h.listSettlements)
	mux.HandleFunc("GET /v1/settlements/{id}", h.getSettlement)

	mux.HandleFunc("POST /v1/statements", h.createStatement)
	mux.HandleFunc("GET /v1/statements", h.listStatements)
	mux.HandleFunc("GET /v1/statements/{id}", h.getStatement)

	mux.HandleFunc("POST /v1/transactions", h.createTransaction)
	mux.HandleFunc("GET /v1/transactions", h.listTransactions)
	mux.HandleFunc("POST /v1/transactions/batch", h.createBatch)
	mux.HandleFunc("GET /v1/transactions/{id}", h.getTransaction)
	mux.HandleFunc("PATCH /v1/transactions/{id}", h.updateTransaction)
	mux.HandleFunc("POST /v1/transactions/{id}/post", h.postTransaction)
	mux.HandleFunc("POST /v1/transactions/{id}/archive", h.archiveTransaction)
	mux.HandleFunc("POST /v1/transactions/{id}/reverse", h.reverse)

	mux.HandleFunc("POST /v1/holds", h.createHold)
	mux.HandleFunc("GET /v1/holds", h.listHolds)
	mux.HandleFunc("GET /v1/holds/{id}", h.getHold)
	mux.HandleFunc("POST /v1/holds/{id}/capture", h.captureHold)
	mux.HandleFunc("POST /v1/holds/{id}/void", h.voidHold)

	mux.HandleFunc("POST /v1/scheduled_transactions", h.createSchedule)
	mux.HandleFunc("GET /v1/scheduled_transactions", h.listSchedules)
	mux.HandleFunc("GET /v1/scheduled_transactions/{id}", h.getSchedule)
	mux.HandleFunc("POST /v1/scheduled_transactions/{id}/cancel", h.cancelSchedule)

	mux.HandleFunc("GET /v1/integrity", h.integrity)
}

func (h *handler) createLedger(w http.ResponseWriter, r *http.Request) {
	var in ledgerRequest
	if !decode(w, r, &in) {
		return
	}
	l, err := h.svc.createLedger(r.Context(), CreateLedgerInput{Name: in.Name, Description: in.Description, Metadata: in.Metadata})
	respond(w, r, http.StatusCreated, l, toLedger, err)
}

func (h *handler) listLedgers(w http.ResponseWriter, r *http.Request) {
	limit, before, ok := page(w, r, uuidCursor)
	if !ok {
		return
	}
	metadata, ok := queryMetadata(w, r)
	if !ok {
		return
	}
	ledgers, err := h.svc.listLedgers(r.Context(), ListLedgersInput{Metadata: metadata, Before: before, Limit: limit + 1})
	respondList(w, r, ledgers, limit, toLedger, func(l Ledger) string { return encodeUUIDCursor(l.ID) }, err)
}

func (h *handler) getLedger(w http.ResponseWriter, r *http.Request) {
	withID[ledgerPrefix](w, r, func(id uuid.UUID) {
		l, err := h.svc.ledger(r.Context(), id)
		respond(w, r, http.StatusOK, l, toLedger, err)
	})
}

func (h *handler) closePeriod(w http.ResponseWriter, r *http.Request) {
	withID[ledgerPrefix](w, r, func(id uuid.UUID) {
		var in struct {
			ClosedBefore jsontext.Value `json:"closed_before"`
		}
		if !decode(w, r, &in) {
			return
		}
		var closedBefore *time.Time
		switch {
		case len(in.ClosedBefore) == 0:
			httpx.Error(w, r, http.StatusBadRequest, httpx.CodeInvalidRequest, "closed_before is required; use null to reopen every period")
			return
		case string(in.ClosedBefore) != "null":
			closedBefore = new(time.Time)
			if err := json.Unmarshal(in.ClosedBefore, closedBefore); err != nil {
				httpx.Error(w, r, http.StatusBadRequest, httpx.CodeInvalidRequest, "closed_before must be an RFC 3339 timestamp or null")
				return
			}
		}
		l, err := h.svc.closePeriod(r.Context(), id, closedBefore)
		respond(w, r, http.StatusOK, l, toLedger, err)
	})
}

func (h *handler) updateLedger(w http.ResponseWriter, r *http.Request) {
	withID[ledgerPrefix](w, r, func(id uuid.UUID) {
		var in UpdateInput
		if !decode(w, r, &in) {
			return
		}
		l, err := h.svc.updateLedger(r.Context(), id, in)
		respond(w, r, http.StatusOK, l, toLedger, err)
	})
}

func (h *handler) createCategory(w http.ResponseWriter, r *http.Request) {
	var in categoryRequest
	if !decode(w, r, &in) {
		return
	}
	c, err := h.svc.createCategory(r.Context(), CreateCategoryInput{
		LedgerID:    in.LedgerID.UUID(),
		Currency:    in.Currency,
		NormalSide:  in.NormalSide,
		Name:        in.Name,
		Description: in.Description,
		Metadata:    in.Metadata,
	})
	respond(w, r, http.StatusCreated, c, toCategory, err)
}

func (h *handler) listCategories(w http.ResponseWriter, r *http.Request) {
	limit, before, ok := page(w, r, uuidCursor)
	if !ok {
		return
	}
	in := ListCategoriesInput{Before: before, Limit: limit + 1}
	for _, f := range []struct {
		name string
		dst  *uuid.UUID
		read func(http.ResponseWriter, *http.Request, string) (uuid.UUID, bool)
	}{
		{"ledger_id", &in.LedgerID, queryID[ledgerPrefix]},
		{"parent_id", &in.ParentID, queryID[categoryPrefix]},
		{"account_id", &in.AccountID, queryID[accountPrefix]},
	} {
		if *f.dst, ok = f.read(w, r, f.name); !ok {
			return
		}
	}
	if in.Metadata, ok = queryMetadata(w, r); !ok {
		return
	}
	categories, err := h.svc.listCategories(r.Context(), in)
	respondList(w, r, categories, limit, toCategory, func(c Category) string { return encodeUUIDCursor(c.ID) }, err)
}

func (h *handler) getCategory(w http.ResponseWriter, r *http.Request) {
	withID[categoryPrefix](w, r, func(id uuid.UUID) {
		effective, ok := queryEffective(w, r)
		if !ok {
			return
		}
		c, err := h.svc.category(r.Context(), id, effective)
		respond(w, r, http.StatusOK, c, toCategory, err)
	})
}

func (h *handler) updateCategory(w http.ResponseWriter, r *http.Request) {
	withID[categoryPrefix](w, r, func(id uuid.UUID) {
		var in UpdateInput
		if !decode(w, r, &in) {
			return
		}
		c, err := h.svc.updateCategory(r.Context(), id, in)
		respond(w, r, http.StatusOK, c, toCategory, err)
	})
}

func (h *handler) deleteCategory(w http.ResponseWriter, r *http.Request) {
	withID[categoryPrefix](w, r, func(id uuid.UUID) {
		err := h.svc.deleteCategory(r.Context(), id)
		respond(w, r, http.StatusOK, id, func(id uuid.UUID) deleted {
			return deleted{Object: "account_category", ID: categoryID(id).String(), Deleted: true}
		}, err)
	})
}

func (h *handler) setMember(member bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		withID[categoryPrefix](w, r, func(id uuid.UUID) {
			var account accountID
			if err := account.UnmarshalText([]byte(r.PathValue("member"))); err != nil {
				httpx.Error(w, r, http.StatusBadRequest, httpx.CodeInvalidRequest, err.Error())
				return
			}
			c, err := h.svc.setMember(r.Context(), id, account.UUID(), member)
			respond(w, r, http.StatusOK, c, toCategory, err)
		})
	}
}

func (h *handler) setChild(nested bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		withID[categoryPrefix](w, r, func(id uuid.UUID) {
			var child categoryID
			if err := child.UnmarshalText([]byte(r.PathValue("member"))); err != nil {
				httpx.Error(w, r, http.StatusBadRequest, httpx.CodeInvalidRequest, err.Error())
				return
			}
			c, err := h.svc.setChild(r.Context(), id, child.UUID(), nested)
			respond(w, r, http.StatusOK, c, toCategory, err)
		})
	}
}

func renderMonitor(m BalanceMonitor) monitorResource { return toMonitor(m, nil) }

func (h *handler) createMonitor(w http.ResponseWriter, r *http.Request) {
	var in monitorRequest
	if !decode(w, r, &in) {
		return
	}
	m, err := h.svc.createMonitor(r.Context(), in.create())
	respond(w, r, http.StatusCreated, m, renderMonitor, err)
}

func (h *handler) listMonitors(w http.ResponseWriter, r *http.Request) {
	limit, before, ok := page(w, r, uuidCursor)
	if !ok {
		return
	}
	account, ok := queryID[accountPrefix](w, r, "account_id")
	if !ok {
		return
	}
	monitors, err := h.svc.listMonitors(r.Context(), ListBalanceMonitorsInput{AccountID: account, Before: before, Limit: limit + 1})
	respondList(w, r, monitors, limit, renderMonitor, func(m BalanceMonitor) string { return encodeUUIDCursor(m.ID) }, err)
}

func (h *handler) getMonitor(w http.ResponseWriter, r *http.Request) {
	withID[monitorPrefix](w, r, func(id uuid.UUID) {
		m, err := h.svc.monitor(r.Context(), id)
		respond(w, r, http.StatusOK, m, renderMonitor, err)
	})
}

func (h *handler) updateMonitor(w http.ResponseWriter, r *http.Request) {
	withID[monitorPrefix](w, r, func(id uuid.UUID) {
		var in UpdateInput
		if !decode(w, r, &in) {
			return
		}
		m, err := h.svc.updateMonitor(r.Context(), id, in)
		respond(w, r, http.StatusOK, m, renderMonitor, err)
	})
}

func (h *handler) deleteMonitor(w http.ResponseWriter, r *http.Request) {
	withID[monitorPrefix](w, r, func(id uuid.UUID) {
		err := h.svc.deleteMonitor(r.Context(), id)
		respond(w, r, http.StatusOK, id, func(id uuid.UUID) deleted {
			return deleted{Object: "balance_monitor", ID: monitorID(id).String(), Deleted: true}
		}, err)
	})
}

func (h *handler) createCurrency(w http.ResponseWriter, r *http.Request) {
	var in currencyRequest
	if !decode(w, r, &in) {
		return
	}
	if in.Exponent == nil {
		writeError(w, r, fmt.Errorf("%w: exponent is required", ErrInvalid))
		return
	}
	c, err := h.svc.createCurrency(r.Context(), CreateCurrencyInput{Code: in.Code, Exponent: *in.Exponent})
	respond(w, r, http.StatusCreated, c, toCurrency, err)
}

func (h *handler) listCurrencies(w http.ResponseWriter, r *http.Request) {
	limit, after, ok := page(w, r, func(b []byte) (money.Currency, bool) {
		c := money.Currency(b)
		return c, c.Validate() == nil
	})
	if !ok {
		return
	}
	currencies, err := h.svc.listCurrencies(r.Context(), after, limit+1)
	respondList(w, r, currencies, limit, toCurrency, func(c Currency) string {
		return base64.RawURLEncoding.EncodeToString([]byte(c.Code))
	}, err)
}

func (h *handler) getCurrency(w http.ResponseWriter, r *http.Request) {
	c, err := h.svc.currency(r.Context(), money.Currency(r.PathValue("code")))
	respond(w, r, http.StatusOK, c, toCurrency, err)
}

func (h *handler) createAccount(w http.ResponseWriter, r *http.Request) {
	var in accountRequest
	if !decode(w, r, &in) {
		return
	}
	acc, err := h.svc.createAccount(r.Context(), in.create())
	respond(w, r, http.StatusCreated, acc, toAccount, err)
}

func (h *handler) listAccounts(w http.ResponseWriter, r *http.Request) {
	limit, before, ok := page(w, r, uuidCursor)
	if !ok {
		return
	}
	ledger, ok := queryID[ledgerPrefix](w, r, "ledger_id")
	if !ok {
		return
	}
	metadata, ok := queryMetadata(w, r)
	if !ok {
		return
	}
	category, ok := queryID[categoryPrefix](w, r, "category_id")
	if !ok {
		return
	}
	q := r.URL.Query()
	accounts, err := h.svc.listAccounts(r.Context(), ListAccountsInput{
		LedgerID:   ledger,
		CategoryID: category,
		Metadata:   metadata,
		Code:       q.Get("code"),
		Status:     AccountStatus(q.Get("status")),
		Currency:   money.Currency(q.Get("currency")),
		Before:     before,
		Limit:      limit + 1,
	})
	respondList(w, r, accounts, limit, toAccount, func(a Account) string { return encodeUUIDCursor(a.ID) }, err)
}

func (h *handler) getAccount(w http.ResponseWriter, r *http.Request) {
	withID[accountPrefix](w, r, func(id uuid.UUID) {
		acc, err := h.svc.account(r.Context(), id)
		respond(w, r, http.StatusOK, acc, toAccount, err)
	})
}

func (h *handler) updateAccount(w http.ResponseWriter, r *http.Request) {
	withID[accountPrefix](w, r, func(id uuid.UUID) {
		var in UpdateInput
		if !decode(w, r, &in) {
			return
		}
		acc, err := h.svc.updateAccount(r.Context(), id, in)
		respond(w, r, http.StatusOK, acc, toAccount, err)
	})
}

func (h *handler) setAccountStatus(status AccountStatus) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		withID[accountPrefix](w, r, func(id uuid.UUID) {
			acc, err := h.svc.setAccountStatus(r.Context(), id, status)
			respond(w, r, http.StatusOK, acc, toAccount, err)
		})
	}
}

func (h *handler) listAccountEntries(w http.ResponseWriter, r *http.Request) {
	withID[accountPrefix](w, r, func(id uuid.UUID) {
		limit, after, ok := page(w, r, int64Cursor)
		if !ok {
			return
		}
		lines, err := h.svc.accountEntries(r.Context(), id, after, limit+1)
		respondList(w, r, lines, limit, toAccountEntry, func(l StatementLine) string { return encodeInt64Cursor(l.PostingID) }, err)
	})
}

func (h *handler) createTransaction(w http.ResponseWriter, r *http.Request) {
	key, ok := idempotencyKey(w, r)
	if !ok {
		return
	}
	var in transactionRequest
	if !decode(w, r, &in) {
		return
	}
	txn, err := h.svc.post(r.Context(), in.post(key))
	respond(w, r, http.StatusCreated, txn, toTransaction, err)
}

func (h *handler) listTransactions(w http.ResponseWriter, r *http.Request) {
	limit, before, ok := page(w, r, uuidCursor)
	if !ok {
		return
	}
	account, ok := queryID[accountPrefix](w, r, "account_id")
	if !ok {
		return
	}
	ledger, ok := queryID[ledgerPrefix](w, r, "ledger_id")
	if !ok {
		return
	}
	metadata, ok := queryMetadata(w, r)
	if !ok {
		return
	}
	effective, ok := queryEffective(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	txns, err := h.svc.listTransactions(r.Context(), ListTransactionsInput{
		LedgerID:   ledger,
		Metadata:   metadata,
		Effective:  effective,
		AccountID:  account,
		Status:     TransactionStatus(q.Get("status")),
		ExternalID: q.Get("external_id"),
		Before:     before,
		Limit:      limit + 1,
	})
	respondList(w, r, txns, limit, toTransaction, func(t Transaction) string { return encodeUUIDCursor(t.ID) }, err)
}

func (h *handler) createBatch(w http.ResponseWriter, r *http.Request) {
	key, ok := idempotencyKey(w, r)
	if !ok {
		return
	}
	var in batchRequest
	if !decode(w, r, &in) {
		return
	}
	atomic := in.Atomic == nil || *in.Atomic
	entries := make([]PostInput, len(in.Transactions))
	for i, t := range in.Transactions {
		entries[i] = t.post(fmt.Sprintf("%s/%d", key, i))
	}

	results, err := h.svc.postBatch(r.Context(), entries, atomic)
	if err != nil {
		writeError(w, r, err)
		return
	}

	resp := batchResponse{Object: "batch", Atomic: atomic, Results: make([]batchResult, len(results))}
	var failure error
	for i, res := range results {
		if res.Err != nil {
			_, code, detail := problemFor(res.Err)
			resp.Results[i].Error = &batchError{Code: code, Detail: detail}
			if failure == nil || errors.Is(failure, ErrBatchAborted) {
				failure = res.Err
			}
			continue
		}
		txn := toTransaction(*res.Transaction)
		resp.Results[i].Transaction = &txn
	}

	status := http.StatusCreated
	switch {
	case failure != nil && atomic:
		status, _, _ = problemFor(failure)
	case failure != nil:
		status = http.StatusMultiStatus
	}
	httpx.JSON(w, r, status, resp)
}

func (h *handler) getTransaction(w http.ResponseWriter, r *http.Request) {
	withID[transactionPrefix](w, r, func(id uuid.UUID) {
		txn, err := h.svc.transaction(r.Context(), id)
		respond(w, r, http.StatusOK, txn, toTransaction, err)
	})
}

func (h *handler) updateTransaction(w http.ResponseWriter, r *http.Request) {
	withID[transactionPrefix](w, r, func(id uuid.UUID) {
		var in updateTransactionRequest
		if !decode(w, r, &in) {
			return
		}
		update := UpdateTransactionInput{Description: in.Description, Metadata: in.Metadata, EffectiveAt: in.EffectiveAt}
		if in.Entries != nil {
			update.Postings = fromEntries(in.Entries)
		}
		txn, err := h.svc.updateTransaction(r.Context(), id, update)
		respond(w, r, http.StatusOK, txn, toTransaction, err)
	})
}

func (h *handler) postTransaction(w http.ResponseWriter, r *http.Request) {
	withID[transactionPrefix](w, r, func(id uuid.UUID) {
		var in postRequest
		if err := httpx.DecodeOptional(w, r, &in); err != nil {
			httpx.Error(w, r, http.StatusBadRequest, httpx.CodeInvalidRequest, err.Error())
			return
		}
		var post PostPendingInput
		if in.Entries != nil {
			post.Postings = fromEntries(in.Entries)
		}
		txn, err := h.svc.postPending(r.Context(), id, post)
		respond(w, r, http.StatusOK, txn, toTransaction, err)
	})
}

func (h *handler) archiveTransaction(w http.ResponseWriter, r *http.Request) {
	withID[transactionPrefix](w, r, func(id uuid.UUID) {
		txn, err := h.svc.archiveTransaction(r.Context(), id)
		respond(w, r, http.StatusOK, txn, toTransaction, err)
	})
}

func (h *handler) reverse(w http.ResponseWriter, r *http.Request) {
	withID[transactionPrefix](w, r, func(id uuid.UUID) {
		key, ok := idempotencyKey(w, r)
		if !ok {
			return
		}
		var in reverseRequest
		if err := httpx.DecodeOptional(w, r, &in); err != nil {
			httpx.Error(w, r, http.StatusBadRequest, httpx.CodeInvalidRequest, err.Error())
			return
		}
		txn, err := h.svc.reverse(r.Context(), id, ReverseInput{IdempotencyKey: key, Description: in.Description, Metadata: in.Metadata})
		respond(w, r, http.StatusCreated, txn, toTransaction, err)
	})
}

func (h *handler) listEntries(w http.ResponseWriter, r *http.Request) {
	limit, after, ok := page(w, r, int64Cursor)
	if !ok {
		return
	}
	in := ListEntriesInput{After: after, Limit: limit + 1}
	q := r.URL.Query()
	in.Status, in.Side = TransactionStatus(q.Get("status")), Side(q.Get("side"))
	for _, f := range []struct {
		name string
		dst  *uuid.UUID
		read func(http.ResponseWriter, *http.Request, string) (uuid.UUID, bool)
	}{
		{"ledger_id", &in.LedgerID, queryID[ledgerPrefix]},
		{"account_id", &in.AccountID, queryID[accountPrefix]},
		{"transaction_id", &in.TransactionID, queryID[transactionPrefix]},
		{"statement_id", &in.StatementID, queryID[statementPrefix]},
		{"settlement_id", &in.SettlementID, queryID[settlementPrefix]},
	} {
		if *f.dst, ok = f.read(w, r, f.name); !ok {
			return
		}
	}
	if in.Metadata, ok = queryMetadata(w, r); !ok {
		return
	}
	if in.Effective, ok = queryEffective(w, r); !ok {
		return
	}
	if raw := q.Get("settled"); raw != "" {
		settled, err := strconv.ParseBool(raw)
		if err != nil {
			httpx.Error(w, r, http.StatusBadRequest, httpx.CodeInvalidRequest, "settled must be true or false")
			return
		}
		in.Settled = &settled
	}
	entries, err := h.svc.listEntries(r.Context(), in)
	respondList(w, r, entries, limit, toEntry, func(e Entry) string { return encodeInt64Cursor(e.Sequence) }, err)
}

func (h *handler) getBalances(w http.ResponseWriter, r *http.Request) {
	withID[accountPrefix](w, r, func(id uuid.UUID) {
		effective, ok := queryEffective(w, r)
		if !ok {
			return
		}
		b, err := h.svc.balancesAt(r.Context(), id, effective)
		respond(w, r, http.StatusOK, b, func(b Balances) balancesResource {
			return balancesResource{
				Object:                "balances",
				AccountID:             accountID(id),
				EffectiveAtLowerBound: effective.From,
				EffectiveAtUpperBound: effective.Until,
				Balances:              b,
			}
		}, err)
	})
}

func (h *handler) createStatement(w http.ResponseWriter, r *http.Request) {
	var in statementRequest
	if !decode(w, r, &in) {
		return
	}
	st, err := h.svc.createStatement(r.Context(), CreateStatementInput{
		AccountID:   in.AccountID.UUID(),
		Description: in.Description,
		From:        in.EffectiveAtLowerBound,
		Until:       in.EffectiveAtUpperBound,
	})
	respond(w, r, http.StatusCreated, st, toStatement, err)
}

func (h *handler) getStatement(w http.ResponseWriter, r *http.Request) {
	withID[statementPrefix](w, r, func(id uuid.UUID) {
		st, err := h.svc.statement(r.Context(), id)
		respond(w, r, http.StatusOK, st, toStatement, err)
	})
}

func (h *handler) listStatements(w http.ResponseWriter, r *http.Request) {
	limit, before, ok := page(w, r, uuidCursor)
	if !ok {
		return
	}
	account, ok := queryID[accountPrefix](w, r, "account_id")
	if !ok {
		return
	}
	statements, err := h.svc.listStatements(r.Context(), ListStatementsInput{AccountID: account, Before: before, Limit: limit + 1})
	respondList(w, r, statements, limit, toStatement, func(s Statement) string { return encodeUUIDCursor(s.ID) }, err)
}

func (h *handler) createBulk(w http.ResponseWriter, r *http.Request) {
	key, ok := idempotencyKey(w, r)
	if !ok {
		return
	}
	var in bulkRequest
	if err := httpx.DecodeBulk(w, r, &in); err != nil {
		httpx.Error(w, r, http.StatusBadRequest, httpx.CodeInvalidRequest, err.Error())
		return
	}
	txns := make([]PostInput, len(in.Transactions))
	for i, t := range in.Transactions {
		txns[i] = t.post("")
	}
	b, err := h.svc.createBulk(r.Context(), CreateBulkInput{IdempotencyKey: key, Transactions: txns})
	respond(w, r, http.StatusAccepted, b, toBulk, err)
}

func (h *handler) getBulk(w http.ResponseWriter, r *http.Request) {
	withID[bulkPrefix](w, r, func(id uuid.UUID) {
		b, err := h.svc.bulk(r.Context(), id)
		respond(w, r, http.StatusOK, b, toBulk, err)
	})
}

func (h *handler) listBulkResults(w http.ResponseWriter, r *http.Request) {
	withID[bulkPrefix](w, r, func(id uuid.UUID) {
		limit, after, ok := page(w, r, int64Cursor)
		if !ok {
			return
		}
		if r.URL.Query().Get("cursor") == "" {
			after = -1
		}
		results, err := h.svc.bulkResults(r.Context(), id, int(after), limit+1)
		respondList(w, r, results, limit, toBulkResult, func(b BulkResult) string { return encodeInt64Cursor(int64(b.Index)) }, err)
	})
}

func (h *handler) createSettlement(w http.ResponseWriter, r *http.Request) {
	key, ok := idempotencyKey(w, r)
	if !ok {
		return
	}
	var in settlementRequest
	if !decode(w, r, &in) {
		return
	}
	st, err := h.svc.createSettlement(r.Context(), CreateSettlementInput{
		IdempotencyKey:   key,
		SettledAccountID: in.SettledAccountID.UUID(),
		ContraAccountID:  in.ContraAccountID.UUID(),
		UpperBound:       in.EffectiveAtUpperBound,
		Description:      in.Description,
		Metadata:         in.Metadata,
	})
	respond(w, r, http.StatusCreated, st, toSettlement, err)
}

func (h *handler) getSettlement(w http.ResponseWriter, r *http.Request) {
	withID[settlementPrefix](w, r, func(id uuid.UUID) {
		st, err := h.svc.settlement(r.Context(), id)
		respond(w, r, http.StatusOK, st, toSettlement, err)
	})
}

func (h *handler) listSettlements(w http.ResponseWriter, r *http.Request) {
	limit, before, ok := page(w, r, uuidCursor)
	if !ok {
		return
	}
	account, ok := queryID[accountPrefix](w, r, "account_id")
	if !ok {
		return
	}
	settlements, err := h.svc.listSettlements(r.Context(), ListSettlementsInput{AccountID: account, Before: before, Limit: limit + 1})
	respondList(w, r, settlements, limit, toSettlement, func(st Settlement) string { return encodeUUIDCursor(st.ID) }, err)
}

func (h *handler) createHold(w http.ResponseWriter, r *http.Request) {
	key, ok := idempotencyKey(w, r)
	if !ok {
		return
	}
	var in holdRequest
	if !decode(w, r, &in) {
		return
	}
	hold, err := h.svc.createHold(r.Context(), CreateHoldInput{
		IdempotencyKey: key,
		AccountID:      in.AccountID.UUID(),
		Amount:         in.Amount,
		Currency:       in.Currency,
		Description:    in.Description,
		ExpiresAt:      in.ExpiresAt,
	})
	respond(w, r, http.StatusCreated, hold, toHold, err)
}

func (h *handler) listHolds(w http.ResponseWriter, r *http.Request) {
	limit, before, ok := page(w, r, uuidCursor)
	if !ok {
		return
	}
	account, ok := queryID[accountPrefix](w, r, "account_id")
	if !ok {
		return
	}
	holds, err := h.svc.listHolds(r.Context(), ListHoldsInput{
		AccountID: account,
		Status:    HoldStatus(r.URL.Query().Get("status")),
		Before:    before,
		Limit:     limit + 1,
	})
	respondList(w, r, holds, limit, toHold, func(h Hold) string { return encodeUUIDCursor(h.ID) }, err)
}

func (h *handler) getHold(w http.ResponseWriter, r *http.Request) {
	withID[holdPrefix](w, r, func(id uuid.UUID) {
		hold, err := h.svc.hold(r.Context(), id)
		respond(w, r, http.StatusOK, hold, toHold, err)
	})
}

func (h *handler) captureHold(w http.ResponseWriter, r *http.Request) {
	withID[holdPrefix](w, r, func(id uuid.UUID) {
		key, ok := idempotencyKey(w, r)
		if !ok {
			return
		}
		var in captureRequest
		if !decode(w, r, &in) {
			return
		}
		hold, err := h.svc.captureHold(r.Context(), id, CaptureInput{
			IdempotencyKey: key,
			Destination:    in.DestinationAccountID.UUID(),
			Amount:         in.Amount,
			Description:    in.Description,
			Metadata:       in.Metadata,
		})
		respond(w, r, http.StatusOK, hold, toHold, err)
	})
}

func (h *handler) voidHold(w http.ResponseWriter, r *http.Request) {
	withID[holdPrefix](w, r, func(id uuid.UUID) {
		hold, err := h.svc.voidHold(r.Context(), id)
		respond(w, r, http.StatusOK, hold, toHold, err)
	})
}

func (h *handler) createSchedule(w http.ResponseWriter, r *http.Request) {
	key, ok := idempotencyKey(w, r)
	if !ok {
		return
	}
	var in scheduleRequest
	if !decode(w, r, &in) {
		return
	}
	st, err := h.svc.schedule(r.Context(), ScheduleInput{PostInput: in.post(key), ExecuteAt: in.ExecuteAt})
	respond(w, r, http.StatusCreated, st, toSchedule, err)
}

func (h *handler) listSchedules(w http.ResponseWriter, r *http.Request) {
	limit, before, ok := page(w, r, uuidCursor)
	if !ok {
		return
	}
	schedules, err := h.svc.listSchedules(r.Context(), ListSchedulesInput{
		Status: ScheduleStatus(r.URL.Query().Get("status")),
		Before: before,
		Limit:  limit + 1,
	})
	respondList(w, r, schedules, limit, toSchedule, func(st ScheduledTransaction) string { return encodeUUIDCursor(st.ID) }, err)
}

func (h *handler) getSchedule(w http.ResponseWriter, r *http.Request) {
	withID[schedulePrefix](w, r, func(id uuid.UUID) {
		st, err := h.svc.scheduled(r.Context(), id)
		respond(w, r, http.StatusOK, st, toSchedule, err)
	})
}

func (h *handler) cancelSchedule(w http.ResponseWriter, r *http.Request) {
	withID[schedulePrefix](w, r, func(id uuid.UUID) {
		st, err := h.svc.cancelSchedule(r.Context(), id)
		respond(w, r, http.StatusOK, st, toSchedule, err)
	})
}

func (h *handler) integrity(w http.ResponseWriter, r *http.Request) {
	report, err := h.svc.verify(r.Context())
	respond(w, r, http.StatusOK, report, func(v VerifyReport) integrityReport {
		return integrityReport{Object: "integrity_report", OK: v.OK, Issues: v.Issues, ChainHead: v.ChainHead}
	}, err)
}

func idempotencyKey(w http.ResponseWriter, r *http.Request) (string, bool) {
	key := r.Header.Get(idempotency.Header)
	if key == "" {
		httpx.Error(w, r, http.StatusBadRequest, codeIdempotencyKeyRequired, idempotency.Header+" header is required")
		return "", false
	}
	return key, true
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := httpx.Decode(w, r, v); err != nil {
		httpx.Error(w, r, http.StatusBadRequest, httpx.CodeInvalidRequest, err.Error())
		return false
	}
	return true
}

func withID[P typeid.Prefix](w http.ResponseWriter, r *http.Request, fn func(uuid.UUID)) {
	var id typeid.ID[P]
	if err := id.UnmarshalText([]byte(r.PathValue("id"))); err != nil {
		httpx.Error(w, r, http.StatusBadRequest, httpx.CodeInvalidRequest, err.Error())
		return
	}
	fn(id.UUID())
}

func queryID[P typeid.Prefix](w http.ResponseWriter, r *http.Request, name string) (uuid.UUID, bool) {
	raw := r.URL.Query().Get(name)
	if raw == "" {
		return uuid.Nil, true
	}
	var id typeid.ID[P]
	if err := id.UnmarshalText([]byte(raw)); err != nil {
		httpx.Error(w, r, http.StatusBadRequest, httpx.CodeInvalidRequest, name+": "+err.Error())
		return uuid.Nil, false
	}
	return id.UUID(), true
}

func queryMetadata(w http.ResponseWriter, r *http.Request) (map[string]string, bool) {
	var m map[string]string
	for key, values := range r.URL.Query() {
		name, found := strings.CutPrefix(key, "metadata[")
		if !found {
			continue
		}
		name, found = strings.CutSuffix(name, "]")
		if !found || name == "" || len(values) != 1 {
			httpx.Error(w, r, http.StatusBadRequest, httpx.CodeInvalidRequest, "metadata filters take the form metadata[key]=value, once per key")
			return nil, false
		}
		if m == nil {
			m = make(map[string]string)
		}
		m[name] = values[0]
	}
	return m, true
}

func queryEffective(w http.ResponseWriter, r *http.Request) (EffectiveRange, bool) {
	var er EffectiveRange
	for _, f := range []struct {
		name string
		dst  **time.Time
	}{{"effective_at_lower_bound", &er.From}, {"effective_at_upper_bound", &er.Until}} {
		raw := r.URL.Query().Get(f.name)
		if raw == "" {
			continue
		}
		t, err := time.Parse(time.RFC3339Nano, raw)
		if err != nil {
			httpx.Error(w, r, http.StatusBadRequest, httpx.CodeInvalidRequest, f.name+" must be an RFC 3339 time")
			return EffectiveRange{}, false
		}
		*f.dst = &t
	}
	return er, true
}

func page[K any](w http.ResponseWriter, r *http.Request, parse func([]byte) (K, bool)) (int, K, bool) {
	var zero K
	limit, err := httpx.PageLimit(r)
	if err != nil {
		httpx.Error(w, r, http.StatusBadRequest, httpx.CodeInvalidRequest, err.Error())
		return 0, zero, false
	}
	raw := r.URL.Query().Get("cursor")
	if raw == "" {
		return limit, zero, true
	}
	b, err := base64.RawURLEncoding.DecodeString(raw)
	key, ok := parse(b)
	if err != nil || !ok {
		httpx.Error(w, r, http.StatusBadRequest, httpx.CodeInvalidRequest, "cursor is invalid")
		return 0, zero, false
	}
	return limit, key, true
}

func uuidCursor(b []byte) (uuid.UUID, bool) {
	id, err := uuid.FromBytes(b)
	return id, err == nil
}

func encodeUUIDCursor(id uuid.UUID) string {
	return base64.RawURLEncoding.EncodeToString(id[:])
}

func int64Cursor(b []byte) (int64, bool) {
	if len(b) != 8 {
		return 0, false
	}
	return int64(binary.BigEndian.Uint64(b)), true
}

func encodeInt64Cursor(v int64) string {
	return base64.RawURLEncoding.EncodeToString(binary.BigEndian.AppendUint64(nil, uint64(v)))
}

func respond[T, R any](w http.ResponseWriter, r *http.Request, status int, v T, render func(T) R, err error) {
	if err != nil {
		writeError(w, r, err)
		return
	}
	httpx.JSON(w, r, status, render(v))
}

func respondList[T, R any](w http.ResponseWriter, r *http.Request, items []T, limit int, render func(T) R, cursor func(T) string, err error) {
	if err != nil {
		writeError(w, r, err)
		return
	}
	list := httpx.NewList(items, limit, cursor)
	out := make([]R, len(list.Data))
	for i, item := range list.Data {
		out[i] = render(item)
	}
	httpx.JSON(w, r, http.StatusOK, httpx.List[R]{Object: list.Object, Data: out, HasMore: list.HasMore, NextCursor: list.NextCursor})
}

func writeError(w http.ResponseWriter, r *http.Request, err error) {
	status, code, detail := problemFor(err)
	httpx.Error(w, r, status, code, detail)
}

func problemFor(err error) (int, string, string) {
	status, code := http.StatusInternalServerError, httpx.CodeInternal
	switch {
	case errors.Is(err, ErrNotFound):
		status, code = http.StatusNotFound, httpx.CodeNotFound
	case errors.Is(err, ErrAccountExists):
		status, code = http.StatusConflict, "account_exists"
	case errors.Is(err, ErrCurrencyExists):
		status, code = http.StatusConflict, "currency_exists"
	case errors.Is(err, ErrAccountNotEmpty):
		status, code = http.StatusConflict, "account_not_empty"
	case errors.Is(err, ErrNotPending):
		status, code = http.StatusConflict, "transaction_not_pending"
	case errors.Is(err, ErrNotPosted):
		status, code = http.StatusConflict, "transaction_not_posted"
	case errors.Is(err, ErrExternalIDExists):
		status, code = http.StatusConflict, "external_id_exists"
	case errors.Is(err, ErrLockVersion):
		status, code = http.StatusConflict, "lock_version_conflict"
	case errors.Is(err, ErrPeriodClosed):
		status, code = http.StatusConflict, "period_closed"
	case errors.Is(err, ErrAlreadyReversed):
		status, code = http.StatusConflict, "already_reversed"
	case errors.Is(err, ErrHoldNotPending):
		status, code = http.StatusConflict, "hold_not_pending"
	case errors.Is(err, ErrScheduleNotPending):
		status, code = http.StatusConflict, "schedule_not_pending"
	case errors.Is(err, ErrIdempotencyConflict):
		status, code = http.StatusUnprocessableEntity, httpx.CodeIdempotencyReuse
	case errors.Is(err, ErrUnknownCurrency):
		status, code = http.StatusUnprocessableEntity, "unknown_currency"
	case errors.Is(err, ErrUnknownLedger):
		status, code = http.StatusUnprocessableEntity, "unknown_ledger"
	case errors.Is(err, ErrCrossLedger):
		status, code = http.StatusUnprocessableEntity, "cross_ledger_transaction"
	case errors.Is(err, ErrAccountNotOpen):
		status, code = http.StatusUnprocessableEntity, "account_not_open"
	case errors.Is(err, ErrInsufficientFunds):
		status, code = http.StatusUnprocessableEntity, "insufficient_funds"
	case errors.Is(err, ErrCategoryCycle):
		status, code = http.StatusUnprocessableEntity, "category_cycle"
	case errors.Is(err, ErrCategoryDepth):
		status, code = http.StatusUnprocessableEntity, "category_too_deep"
	case errors.Is(err, ErrCategoryMismatch):
		status, code = http.StatusUnprocessableEntity, "category_mismatch"
	case errors.Is(err, ErrBalanceLock):
		status, code = http.StatusUnprocessableEntity, "balance_lock_failed"
	case errors.Is(err, ErrUnbalanced):
		status, code = http.StatusUnprocessableEntity, "unbalanced_transaction"
	case errors.Is(err, ErrBatchAborted):
		status, code = http.StatusUnprocessableEntity, "batch_aborted"
	case errors.Is(err, money.ErrOverflow):
		status, code = http.StatusUnprocessableEntity, "amount_overflow"
	case errors.Is(err, ErrInvalid):
		status, code = http.StatusUnprocessableEntity, "validation_error"
	case errors.Is(err, ErrStopped):
		status, code = http.StatusServiceUnavailable, httpx.CodeUnavailable
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		status, code = http.StatusGatewayTimeout, "timeout"
	}
	if status == http.StatusInternalServerError {
		return status, code, ""
	}
	return status, code, err.Error()
}
