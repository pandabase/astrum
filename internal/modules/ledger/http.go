package ledger

import (
	"encoding/base64"
	"encoding/binary"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/pandabase/astrum/internal/kernel/httpx"
	"github.com/pandabase/astrum/internal/kernel/idempotency"
	"github.com/pandabase/astrum/internal/kernel/typeid"
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
