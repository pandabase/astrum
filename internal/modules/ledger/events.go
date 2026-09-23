package ledger

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/pandabase/astrum/internal/kernel/events"
)

const (
	eventTransactionCreated  = "transaction.created"
	eventTransactionUpdated  = "transaction.updated"
	eventTransactionPosted   = "transaction.posted"
	eventTransactionArchived = "transaction.archived"
	eventAccountCreated      = "account.created"
	eventAccountUpdated      = "account.updated"
	eventHoldCreated         = "hold.created"
	eventHoldCaptured        = "hold.captured"
	eventHoldVoided          = "hold.voided"
	eventHoldExpired         = "hold.expired"
	eventMonitorTriggered    = "balance_monitor.triggered"
	eventBulkCompleted       = "bulk_request.completed"
	eventSettlementCreated   = "settlement.created"
)

func transactionEvents(eventType string, txns ...Transaction) ([]events.Event, error) {
	return render(eventType, txns, transactionResourceOnly)
}

func transactionResourceOnly(t Transaction) transactionResource {
	r := toTransaction(t)
	for i := range r.Entries {
		r.Entries[i].ResultingBalances = nil
	}
	return r
}

func emit[T, R any](ctx context.Context, tx pgx.Tx, eventType string, resource func(T) R, items ...T) error {
	evs, err := render(eventType, items, resource)
	if err != nil {
		return err
	}
	return events.Insert(ctx, tx, evs...)
}

func render[T, R any](eventType string, items []T, resource func(T) R) ([]events.Event, error) {
	out := make([]events.Event, len(items))
	for i, item := range items {
		ev, err := events.New(eventType, resource(item))
		if err != nil {
			return nil, err
		}
		out[i] = ev
	}
	return out, nil
}

func monitorEvents(state *ledgerState) ([]events.Event, error) {
	fired, balances, err := state.crossed()
	if err != nil {
		return nil, err
	}
	out := make([]events.Event, len(fired))
	for i, m := range fired {
		if out[i], err = events.New(eventMonitorTriggered, toMonitor(m, &balances[i])); err != nil {
			return nil, err
		}
	}
	return out, nil
}
