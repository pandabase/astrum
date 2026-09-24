package ledger

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/pandabase/astrum/internal/kernel/httpx"
	"github.com/pandabase/astrum/internal/money"
)

func TestProblemForEdge(t *testing.T) {
	t.Parallel()
	tests := []struct {
		err    error
		status int
		code   string
	}{
		{ErrNotFound, 404, "not_found"},
		{ErrAccountExists, 409, "account_exists"},
		{ErrCurrencyExists, 409, "currency_exists"},
		{ErrAccountNotEmpty, 409, "account_not_empty"},
		{ErrNotPending, 409, "transaction_not_pending"},
		{ErrNotPosted, 409, "transaction_not_posted"},
		{ErrExternalIDExists, 409, "external_id_exists"},
		{ErrLockVersion, 409, "lock_version_conflict"},
		{ErrAlreadyReversed, 409, "already_reversed"},
		{ErrHoldNotPending, 409, "hold_not_pending"},
		{ErrScheduleNotPending, 409, "schedule_not_pending"},
		{ErrIdempotencyConflict, 422, "idempotency_key_reused"},
		{ErrUnknownCurrency, 422, "unknown_currency"},
		{ErrUnknownLedger, 422, "unknown_ledger"},
		{ErrCrossLedger, 422, "cross_ledger_transaction"},
		{ErrAccountNotOpen, 422, "account_not_open"},
		{ErrInsufficientFunds, 422, "insufficient_funds"},
		{ErrCategoryCycle, 422, "category_cycle"},
		{ErrCategoryDepth, 422, "category_too_deep"},
		{ErrCategoryMismatch, 422, "category_mismatch"},
		{ErrBalanceLock, 422, "balance_lock_failed"},
		{ErrUnbalanced, 422, "unbalanced_transaction"},
		{ErrBatchAborted, 422, "batch_aborted"},
		{money.ErrOverflow, 422, "amount_overflow"},
		{ErrInvalid, 422, "validation_error"},
		{ErrStopped, 503, "service_unavailable"},
		{context.DeadlineExceeded, 504, "timeout"},
		{context.Canceled, 504, "timeout"},
		{money.ErrInvalidAmount, 500, "internal_error"},
		{money.ErrInvalidCurrency, 500, "internal_error"},
		{errors.New("pq: SQLSTATE 40001 secret detail"), 500, "internal_error"},
	}
	codes := map[string]error{}
	for _, tt := range tests {
		t.Run(tt.err.Error(), func(t *testing.T) {
			for name, err := range map[string]error{
				"bare":    tt.err,
				"wrapped": fmt.Errorf("outer: %w", tt.err),
				"joined":  errors.Join(errors.New("context"), tt.err),
			} {
				status, code, detail := problemFor(err)
				if status != tt.status || code != tt.code {
					t.Fatalf("%s: problemFor = %d %s, want %d %s", name, status, code, tt.status, tt.code)
				}
				if tt.status == 500 && detail != "" {
					t.Fatalf("%s: 500 leaks detail %q", name, detail)
				}
				if tt.status != 500 && detail != err.Error() {
					t.Fatalf("%s: detail = %q, want %q", name, detail, err.Error())
				}
			}
		})
		if prev, dup := codes[tt.code]; dup && tt.status != 500 && tt.status != 504 {
			t.Errorf("code %s used by both %v and %v", tt.code, prev, tt.err)
		}
		codes[tt.code] = tt.err
	}

	t.Run("first match wins for wrapped pairs", func(t *testing.T) {
		pairs := []struct {
			err  error
			code string
		}{
			{fmt.Errorf("%w: %w", ErrInvalid, ErrNotFound), "not_found"},
			{fmt.Errorf("%w: %w", ErrInvalid, ErrInsufficientFunds), "insufficient_funds"},
			{fmt.Errorf("%w: %w", ErrBatchAborted, ErrInsufficientFunds), "insufficient_funds"},
			{fmt.Errorf("%w: %w", context.Canceled, ErrStopped), "service_unavailable"},
			{fmt.Errorf("%w: %w", money.ErrOverflow, ErrInvalid), "amount_overflow"},
		}
		for _, p := range pairs {
			if _, code, _ := problemFor(p.err); code != p.code {
				t.Errorf("problemFor(%v) code = %s, want %s", p.err, code, p.code)
			}
		}
	})
}

func TestWriteErrorEdge(t *testing.T) {
	t.Parallel()
	w := httptest.NewRecorder()
	writeError(w, httptest.NewRequest(http.MethodGet, "/", nil), fmt.Errorf("%w: code is required", ErrInvalid))
	var p httpx.Problem
	if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil {
		t.Fatal(err)
	}
	if w.Code != 422 || p.Code != "validation_error" || p.Detail != "ledger: invalid input: code is required" || p.Type != "urn:astrum:error:validation_error" {
		t.Fatalf("problem = %d %+v", w.Code, p)
	}
}

func TestCursorCodecsEdge(t *testing.T) {
	t.Parallel()
	for _, v := range []int64{0, 1, -1, 1 << 62, -1 << 63} {
		b, err := decodeEdgeCursor(encodeInt64Cursor(v))
		if err != nil {
			t.Fatal(err)
		}
		if got, ok := int64Cursor(b); !ok || got != v {
			t.Fatalf("int64 cursor round trip %d = %d %v", v, got, ok)
		}
	}
	for _, n := range []int{0, 7, 9, 16} {
		if _, ok := int64Cursor(make([]byte, n)); ok {
			t.Fatalf("int64Cursor accepted %d bytes", n)
		}
		if _, ok := uuidCursor(make([]byte, n)); ok && n != 16 {
			t.Fatalf("uuidCursor accepted %d bytes", n)
		}
	}
}

func decodeEdgeCursor(s string) ([]byte, error) {
	r := httptest.NewRequest(http.MethodGet, "/?cursor="+s, nil)
	var out []byte
	_, _, ok := page(httptest.NewRecorder(), r, func(b []byte) (int, bool) {
		out = b
		return 0, true
	})
	if !ok {
		return nil, errors.New("cursor rejected")
	}
	return out, nil
}
