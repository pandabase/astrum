package ledger

import (
	"net/http"

	"github.com/google/uuid"
)

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
