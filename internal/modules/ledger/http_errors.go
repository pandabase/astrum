package ledger

import (
	"context"
	"errors"
	"net/http"

	"github.com/pandabase/astrum/internal/kernel/httpx"
	"github.com/pandabase/astrum/internal/money"
)

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
