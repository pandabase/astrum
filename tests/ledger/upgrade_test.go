package tests

import (
	"context"
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pandabase/astrum/internal/kernel/db"
	"github.com/pandabase/astrum/internal/kernel/events"
	"github.com/pandabase/astrum/internal/kernel/testdb"
	"github.com/pandabase/astrum/internal/modules/ledger"
)

func TestUpgradeToWideAmounts(t *testing.T) {
	ctx := context.Background()
	key := []byte("test-seal-key-0123456789abcdef-0123456789")
	m, err := ledger.New(nil, testdb.Logger(), ledger.Config{SealKey: key})
	if err != nil {
		t.Fatal(err)
	}
	all := m.Migrations()
	before := fstest.MapFS{}
	for _, name := range []string{"0001_init.sql", "0002_account_status.sql"} {
		data, err := fs.ReadFile(all, name)
		if err != nil {
			t.Fatal(err)
		}
		before[name] = &fstest.MapFile{Data: data}
	}
	pool := testdb.New(t, map[string]fs.FS{"ledger": before, "events": events.Migrations()})

	cash, legacy, deposits := uuid.New(), uuid.New(), uuid.New()
	err = pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		txn := uuid.New()
		for _, sql := range []string{
			`INSERT INTO ledger_accounts (id, code, currency, normal_side, allow_negative, overdraft_limit)
			 VALUES ('` + cash.String() + `', 'cash', 'USD', 'debit', false, 25),
			        ('` + deposits.String() + `', 'deposits', 'USD', 'credit', true, 0),
			        ('` + legacy.String() + `', 'legacy', 'XYZ', 'debit', true, 0)`,
			`INSERT INTO ledger_transactions (id, idempotency_key) VALUES ('` + txn.String() + `', 'old')`,
			`INSERT INTO ledger_postings (transaction_id, account_id, currency, side, amount, balance_after)
			 VALUES ('` + txn.String() + `', '` + cash.String() + `', 'USD', 'debit', 9000000000000000000, 9000000000000000000),
			        ('` + txn.String() + `', '` + deposits.String() + `', 'USD', 'credit', 9000000000000000000, -9000000000000000000)`,
			`UPDATE ledger_accounts SET balance = 9000000000000000000, held = 100, version = 1 WHERE id = '` + cash.String() + `'`,
			`UPDATE ledger_accounts SET balance = -9000000000000000000, version = 1 WHERE id = '` + deposits.String() + `'`,
			`INSERT INTO ledger_holds (id, idempotency_key, account_id, currency, amount, expires_at)
			 VALUES (gen_random_uuid(), 'old-hold', '` + cash.String() + `', 'USD', 100, now() + interval '1 hour')`,
		} {
			if _, err := tx.Exec(ctx, sql); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	pool.Reset()
	if err := db.Migrate(ctx, pool, testdb.Logger(), "ledger", all); err != nil {
		t.Fatal(err)
	}
	m, err = ledger.New(pool, testdb.Logger(), ledger.Config{SealKey: key, SweepInterval: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { _ = m.Run(runCtx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })

	acc := mustAccount(t, m, cash)
	if acc.Posted.Amount != amt(9_000_000_000_000_000_000) || acc.Held != amt(100) || acc.OverdraftLimit != amt(25) || acc.CurrencyExponent != 2 {
		t.Fatalf("cash after upgrade = %+v", acc)
	}
	if acc := mustAccount(t, m, legacy); acc.Currency != "XYZ" || acc.CurrencyExponent != 2 {
		t.Fatalf("legacy currency account = %+v", acc)
	}
	ledgers, err := m.ListLedgers(ctx, ledger.ListLedgersInput{Limit: 10})
	if err != nil || len(ledgers) != 1 || ledgers[0].Name != "Default" {
		t.Fatalf("ledgers after upgrade = %+v, %v", ledgers, err)
	}
	for _, id := range []uuid.UUID{cash, legacy, deposits} {
		if got := mustAccount(t, m, id).LedgerID; got != ledgers[0].ID {
			t.Fatalf("account %s is in ledger %s, want the default ledger", id, got)
		}
	}

	in := transfer("new", deposits, cash, 9_000_000_000_000_000_000)
	if _, err := m.Post(ctx, in); err != nil {
		t.Fatal(err)
	}
	if got := mustAccount(t, m, cash).Posted.Amount.String(); got != "18000000000000000000" {
		t.Fatalf("cash = %s", got)
	}
	if _, err := m.Seal(ctx); err != nil {
		t.Fatal(err)
	}
	report, err := m.Verify(ctx)
	if err != nil || !report.OK {
		t.Fatalf("verify = %+v, %v", report, err)
	}
	if !strings.HasPrefix(report.ChainHead, "2:") {
		t.Fatalf("chain head = %s, want both transactions sealed", report.ChainHead)
	}
}

func mustAccount(t *testing.T, m *ledger.Module, id uuid.UUID) ledger.Account {
	t.Helper()
	acc, err := m.Account(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return acc
}
