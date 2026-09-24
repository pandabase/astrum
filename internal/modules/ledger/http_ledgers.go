package ledger

import (
	"encoding/base64"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/pandabase/astrum/internal/kernel/httpx"
	"github.com/pandabase/astrum/internal/money"
)

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

func (h *handler) integrity(w http.ResponseWriter, r *http.Request) {
	report, err := h.svc.verify(r.Context())
	respond(w, r, http.StatusOK, report, func(v VerifyReport) integrityReport {
		return integrityReport{Object: "integrity_report", OK: v.OK, Issues: v.Issues, ChainHead: v.ChainHead}
	}, err)
}
