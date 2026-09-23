package bench

import (
	"context"
	"fmt"
	"math/rand/v2"
	"strconv"
	"time"
)

type step func(ctx context.Context, rng *rand.Rand) (transactions int, err error)

var scenarios = map[string]func(*run) step{
	"transfer": (*run).transfer,
	"batch":    (*run).batch,
	"pending":  (*run).pending,
	"hold":     (*run).hold,
	"read":     (*run).read,
}

func Scenarios() []string {
	return []string{"transfer", "batch", "pending", "hold", "read"}
}

type entry struct {
	AccountID string `json:"account_id"`
	Side      string `json:"side"`
	Amount    string `json:"amount"`
}

type transaction struct {
	Description string  `json:"description"`
	Status      string  `json:"status,omitempty"`
	Entries     []entry `json:"entries"`
}

func (r *run) pair(rng *rand.Rand) (string, string) {
	from := rng.IntN(len(r.accounts))
	to := rng.IntN(len(r.accounts) - 1)
	if to >= from {
		to++
	}
	return r.accounts[from], r.accounts[to]
}

func (r *run) amount(rng *rand.Rand) string {
	return strconv.Itoa(rng.IntN(10_000) + 1)
}

func (r *run) transferBody(rng *rand.Rand, status string) transaction {
	from, to := r.pair(rng)
	amount := r.amount(rng)
	return transaction{
		Description: "bench transfer",
		Status:      status,
		Entries: []entry{
			{AccountID: to, Side: "debit", Amount: amount},
			{AccountID: from, Side: "credit", Amount: amount},
		},
	}
}

func (r *run) transfer() step {
	return func(ctx context.Context, rng *rand.Rand) (int, error) {
		return 1, r.client.do(ctx, "POST", "/v1/transactions", r.transferBody(rng, ""), true, nil)
	}
}

func (r *run) batch() step {
	return func(ctx context.Context, rng *rand.Rand) (int, error) {
		txns := make([]transaction, r.cfg.BatchSize)
		for i := range txns {
			txns[i] = r.transferBody(rng, "")
		}
		body := map[string]any{"transactions": txns, "atomic": true}
		return len(txns), r.client.do(ctx, "POST", "/v1/transactions/batch", body, true, nil)
	}
}

func (r *run) pending() step {
	return func(ctx context.Context, rng *rand.Rand) (int, error) {
		var created resource
		if err := r.client.do(ctx, "POST", "/v1/transactions", r.transferBody(rng, "pending"), true, &created); err != nil {
			return 0, err
		}
		return 1, r.client.do(ctx, "POST", "/v1/transactions/"+created.ID+"/post", nil, false, nil)
	}
}

func (r *run) hold() step {
	return func(ctx context.Context, rng *rand.Rand) (int, error) {
		from, to := r.pair(rng)
		amount := r.amount(rng)
		var created resource
		body := map[string]any{
			"account_id":  from,
			"amount":      amount,
			"description": "bench hold",
			"expires_at":  time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
		}
		if err := r.client.do(ctx, "POST", "/v1/holds", body, true, &created); err != nil {
			return 0, err
		}
		capture := map[string]any{"destination_account_id": to, "amount": amount}
		return 1, r.client.do(ctx, "POST", "/v1/holds/"+created.ID+"/capture", capture, true, nil)
	}
}

func (r *run) read() step {
	return func(ctx context.Context, rng *rand.Rand) (int, error) {
		return 0, r.client.do(ctx, "GET", "/v1/accounts/"+r.accounts[rng.IntN(len(r.accounts))], nil, false, nil)
	}
}

func scenarioStep(r *run) (step, error) {
	build, ok := scenarios[r.cfg.Scenario]
	if !ok {
		return nil, fmt.Errorf("bench: unknown scenario %q; choose one of %v", r.cfg.Scenario, Scenarios())
	}
	return build(r), nil
}
