package ledger

import (
	"net/http"

	"github.com/google/uuid"
	"github.com/pandabase/astrum/internal/kernel/httpx"
)

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
