package tests

import (
	"context"
	"errors"
	"io/fs"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pandabase/astrum/internal/kernel/events"
	"github.com/pandabase/astrum/internal/kernel/testdb"
	"github.com/pandabase/astrum/internal/modules/ledger"
	"github.com/pandabase/astrum/internal/money"
)

type env struct {
	m      *ledger.Module
	pool   *pgxpool.Pool
	ledger ledger.Ledger

	open ledger.Account
}

func setup(t testing.TB) *env {
	t.Helper()
	return setupWith(t, ledger.Config{SweepInterval: time.Hour})
}

func setupWith(t testing.TB, cfg ledger.Config) *env {
	t.Helper()
	if cfg.SealKey == nil {
		cfg.SealKey = []byte("test-seal-key-0123456789abcdef-0123456789")
	}
	probe, err := ledger.New(nil, testdb.Logger(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	pool := testdb.New(t, map[string]fs.FS{probe.Name(): probe.Migrations(), "events": events.Migrations()})
	m, err := ledger.New(pool, testdb.Logger(), cfg)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Go(func() { _ = m.Run(ctx) })
	t.Cleanup(func() {
		cancel()
		wg.Wait()
	})

	e := &env{m: m, pool: pool}
	if e.ledger, err = m.CreateLedger(context.Background(), ledger.CreateLedgerInput{Name: "test"}); err != nil {
		t.Fatal(err)
	}
	e.open = e.account(t, "USD", ledger.Credit, func(in *ledger.CreateAccountInput) { in.AllowNegative = true })
	return e
}

func (e *env) account(t testing.TB, currency money.Currency, side ledger.Side, opts ...func(*ledger.CreateAccountInput)) ledger.Account {
	t.Helper()
	in := ledger.CreateAccountInput{LedgerID: e.ledger.ID, Code: "acct:" + uuid.NewString(), Currency: currency, NormalSide: side}
	for _, opt := range opts {
		opt(&in)
	}
	acc, err := e.m.CreateAccount(context.Background(), in)
	if err != nil {
		t.Fatalf("CreateAccount() error = %v", err)
	}
	return acc
}

func unrestricted(in *ledger.CreateAccountInput) { in.AllowNegative = true }

func (e *env) accountIn(t testing.TB, ledgerID uuid.UUID) ledger.Account {
	t.Helper()
	return e.account(t, "USD", ledger.Debit, unrestricted, func(in *ledger.CreateAccountInput) { in.LedgerID = ledgerID })
}

func (e *env) funded(t testing.TB, amount int64) ledger.Account {
	t.Helper()
	acc := e.account(t, "USD", ledger.Debit)
	e.post(t, transfer("fund:"+uuid.NewString(), e.open.ID, acc.ID, amount))
	return acc
}

func (e *env) post(t testing.TB, in ledger.PostInput) ledger.Transaction {
	t.Helper()
	txn, err := e.m.Post(context.Background(), in)
	if err != nil {
		t.Fatalf("Post(%s) error = %v", in.IdempotencyKey, err)
	}
	return txn
}

func (e *env) get(t testing.TB, id uuid.UUID) ledger.Account {
	t.Helper()
	acc, err := e.m.Account(context.Background(), id)
	if err != nil {
		t.Fatalf("Account() error = %v", err)
	}
	return acc
}

func (e *env) balance(t testing.TB, id uuid.UUID) int64 {
	t.Helper()
	return small(t, e.get(t, id).Posted.Amount)
}

func (e *env) verify(t testing.TB) {
	t.Helper()
	if _, err := e.m.Seal(context.Background()); err != nil {
		t.Fatalf("Seal() error = %v", err)
	}
	report, err := e.m.Verify(context.Background())
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if !report.OK {
		t.Fatalf("ledger integrity violated: %v", report.Issues)
	}
}

func transfer(key string, from, to uuid.UUID, amount int64) ledger.PostInput {
	return transferAmount(key, from, to, amt(amount))
}

func transferAmount(key string, from, to uuid.UUID, amount money.Amount) ledger.PostInput {
	return ledger.PostInput{
		IdempotencyKey: key,
		Description:    "transfer",
		Postings: []ledger.Posting{
			{AccountID: to, Side: ledger.Debit, Amount: amount},
			{AccountID: from, Side: ledger.Credit, Amount: amount},
		},
	}
}

func wantErr(t testing.TB, err, target error) {
	t.Helper()
	if !errors.Is(err, target) {
		t.Fatalf("error = %v, want %v", err, target)
	}
}

func (e *env) insertPosted(key string) string {
	return `INSERT INTO ledger_transactions (id, ledger_id, idempotency_key, posted_at, posted_xid)
		VALUES (gen_random_uuid(), '` + e.ledger.ID.String() + `', ` + key + `, now(), pg_current_xact_id())`
}

func amt(n int64) money.Amount { return money.NewAmount(n) }

func small(t testing.TB, a money.Amount) int64 {
	t.Helper()
	n, ok := a.Int64()
	if !ok {
		t.Fatalf("amount %s does not fit in int64", a)
	}
	return n
}
