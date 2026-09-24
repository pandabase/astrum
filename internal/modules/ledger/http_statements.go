package ledger

import (
	"net/http"

	"github.com/google/uuid"
)

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
