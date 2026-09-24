package ledger

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/google/uuid"
	"github.com/pandabase/astrum/internal/kernel/httpx"
)

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
