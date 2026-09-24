package ledger

import (
	"net/http"

	"github.com/google/uuid"
	"github.com/pandabase/astrum/internal/kernel/httpx"
	"github.com/pandabase/astrum/internal/money"
)

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
