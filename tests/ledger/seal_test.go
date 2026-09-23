package tests

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/pandabase/astrum/internal/kernel/testdb"
	"github.com/pandabase/astrum/internal/modules/ledger"
)

func asAttacker(t *testing.T, e *env, sql string, args ...any) {
	t.Helper()
	ctx := context.Background()
	err := pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SET LOCAL session_replication_role = replica`); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, sql, args...)
		return err
	})
	if err != nil {
		if strings.Contains(err.Error(), "permission denied") {
			t.Skip("tamper tests need a superuser connection")
		}
		t.Fatalf("tamper: %v", err)
	}
}

func verifyIssues(t *testing.T, e *env) []string {
	t.Helper()
	report, err := e.m.Verify(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return report.Issues
}

func requireIssue(t *testing.T, issues []string, fragment string) {
	t.Helper()
	for _, issue := range issues {
		if strings.Contains(issue, fragment) {
			return
		}
	}
	t.Fatalf("expected an issue containing %q, got %v", fragment, issues)
}

func sealedLedger(t *testing.T) (*env, ledger.Transaction, ledger.Account, ledger.Account) {
	t.Helper()
	e := setup(t)
	a := e.funded(t, 1_000)
	b := e.account(t, "USD", ledger.Debit)
	txn := e.post(t, transfer("victim", a.ID, b.ID, 400))
	e.post(t, transfer("after", a.ID, b.ID, 1))
	e.verify(t)
	return e, txn, a, b
}

func TestSealChainIsComplete(t *testing.T) {
	e, _, _, _ := sealedLedger(t)
	ctx := context.Background()

	var txns, seals int
	if err := e.pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM ledger_transactions), (SELECT count(*) FROM ledger_seals)`).Scan(&txns, &seals); err != nil {
		t.Fatal(err)
	}
	if txns != seals {
		t.Fatalf("transactions = %d, seals = %d", txns, seals)
	}
	if n, err := e.m.Seal(ctx); err != nil || n != 0 {
		t.Fatalf("resealing sealed ledger = %d, %v", n, err)
	}
}

func TestSealDetectsConsistentRewrite(t *testing.T) {
	e, txn, a, b := sealedLedger(t)

	asAttacker(t, e, `UPDATE ledger_postings SET amount = 40, balance_after = balance_after + CASE side WHEN 'debit' THEN -360 ELSE 360 END WHERE transaction_id = $1`, txn.ID)
	asAttacker(t, e, `UPDATE ledger_postings SET balance_after = balance_after + CASE WHEN account_id = $1 THEN 360 ELSE -360 END WHERE transaction_id <> $2 AND account_id IN ($1, $3) AND id > (SELECT max(id) FROM ledger_postings WHERE transaction_id = $2)`, a.ID, txn.ID, b.ID)
	asAttacker(t, e, `UPDATE ledger_accounts SET posted_credits = posted_credits - 360 WHERE id = $1`, a.ID)
	asAttacker(t, e, `UPDATE ledger_accounts SET posted_debits = posted_debits - 360 WHERE id = $1`, b.ID)

	issues := verifyIssues(t, e)
	requireIssue(t, issues, "content was altered")
	for _, issue := range issues {
		if strings.Contains(issue, "balance") {
			t.Fatalf("rewrite was supposed to be balance-consistent, but got %q", issue)
		}
	}
}

func TestSealDetectsMetadataEdit(t *testing.T) {
	e, txn, _, _ := sealedLedger(t)
	asAttacker(t, e, `UPDATE ledger_transactions SET description = 'nothing to see' WHERE id = $1`, txn.ID)
	requireIssue(t, verifyIssues(t, e), "content was altered")
}

func TestSealDetectsDeletion(t *testing.T) {
	e, txn, _, _ := sealedLedger(t)
	asAttacker(t, e, `
		WITH s AS (DELETE FROM ledger_seals WHERE transaction_id = $1),
		     p AS (DELETE FROM ledger_postings WHERE transaction_id = $1)
		DELETE FROM ledger_transactions WHERE id = $1`, txn.ID)
	requireIssue(t, verifyIssues(t, e), "expected seq")
}

func TestSealDetectsWipedChain(t *testing.T) {
	e, _, _, _ := sealedLedger(t)
	asAttacker(t, e, `DELETE FROM ledger_seals`)
	asAttacker(t, e, `UPDATE ledger_transactions SET posted_at = posted_at - interval '2 minutes'`)
	requireIssue(t, verifyIssues(t, e), "has been unsealed since")
}

func TestSealRefusesRewoundChain(t *testing.T) {
	e, _, a, b := sealedLedger(t)
	ctx := context.Background()
	asAttacker(t, e, `DELETE FROM ledger_seals WHERE seq = (SELECT max(seq) FROM ledger_seals)`)
	e.post(t, transfer("after-rewind", a.ID, b.ID, 1))
	if n, err := e.m.Seal(ctx); err == nil || n != 0 {
		t.Fatalf("Seal() after rewind = %d, %v; want refusal", n, err)
	}
}

func TestSealDetectsForgedChain(t *testing.T) {
	e, txn, _, _ := sealedLedger(t)

	asAttacker(t, e, `UPDATE ledger_seals SET chain_hash = sha256(chain_hash) WHERE transaction_id = $1`, txn.ID)
	requireIssue(t, verifyIssues(t, e), "chain hash does not verify")
}

func TestSealDetectsBackdatedInsertion(t *testing.T) {
	e, txn, a, b := sealedLedger(t)
	asAttacker(t, e, `
		WITH t AS (
			INSERT INTO ledger_transactions (id, ledger_id, idempotency_key, created_xid, posted_at, posted_xid)
			SELECT gen_random_uuid(), ledger_id, 'forged', created_xid, posted_at, posted_xid FROM ledger_transactions WHERE id = $1
			RETURNING id
		)
		INSERT INTO ledger_postings (transaction_id, account_id, currency, side, amount, balance_after)
		SELECT t.id, v.account, 'USD', v.side, 5, 0
		FROM t, (VALUES ($2::uuid, 'debit'), ($3::uuid, 'credit')) AS v(account, side)`,
		txn.ID, b.ID, a.ID)
	requireIssue(t, verifyIssues(t, e), "unsealed but precedes the chain head")
}

func TestWrongKeyFailsVerification(t *testing.T) {
	e, _, _, _ := sealedLedger(t)
	other, err := ledger.New(e.pool, testdb.Logger(), ledger.Config{SealKey: []byte("another-key-another-key-another-key!!")})
	if err != nil {
		t.Fatal(err)
	}
	report, err := other.Verify(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	requireIssue(t, report.Issues, "chain hash does not verify")
}

func TestWrongKeyCannotExtendChain(t *testing.T) {
	e, _, a, b := sealedLedger(t)
	ctx := context.Background()
	if err := e.m.CheckSealKey(ctx); err != nil {
		t.Fatalf("CheckSealKey() with the right key = %v", err)
	}

	other, err := ledger.New(e.pool, testdb.Logger(), ledger.Config{SealKey: []byte("another-key-another-key-another-key!!")})
	if err != nil {
		t.Fatal(err)
	}
	if err := other.CheckSealKey(ctx); err == nil {
		t.Fatal("CheckSealKey() with the wrong key succeeded")
	}
	e.post(t, transfer("unsealed", a.ID, b.ID, 1))
	if n, err := other.Seal(ctx); err == nil || n != 0 {
		t.Fatalf("Seal() with the wrong key = %d, %v; want refusal", n, err)
	}
	e.verify(t)
}

func TestSealWaitsForInFlightTransactions(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	a := e.account(t, "USD", ledger.Debit, unrestricted)
	b := e.account(t, "USD", ledger.Debit, unrestricted)

	c := e.account(t, "USD", ledger.Debit, unrestricted)
	d := e.account(t, "USD", ledger.Debit, unrestricted)
	if _, err := e.m.Seal(ctx); err != nil {
		t.Fatal(err)
	}

	slow, err := e.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer slow.Rollback(ctx)
	if _, err := slow.Exec(ctx, `
		WITH t AS (`+e.insertPosted(`'slow'`)+` RETURNING id)
		INSERT INTO ledger_postings (transaction_id, account_id, currency, side, amount, balance_after)
		SELECT t.id, v.account, 'USD', v.side, 1, 0
		FROM t, (VALUES ($1::uuid, 'debit'), ($2::uuid, 'credit')) AS v(account, side)`, c.ID, d.ID); err != nil {
		t.Fatal(err)
	}

	e.post(t, transfer("fast", a.ID, b.ID, 1))
	if n, err := e.m.Seal(ctx); err != nil || n != 0 {
		t.Fatalf("sealed %d while an older transaction was in flight, err %v", n, err)
	}

	if err := slow.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if n, err := e.m.Seal(ctx); err != nil || n != 2 {
		t.Fatalf("sealed %d after commit, want 2 (err %v)", n, err)
	}

	var first string
	if err := e.pool.QueryRow(ctx, `SELECT t.idempotency_key FROM ledger_seals s JOIN ledger_transactions t ON t.id = s.transaction_id ORDER BY s.seq DESC OFFSET 1 LIMIT 1`).Scan(&first); err != nil {
		t.Fatal(err)
	}
	if first != "slow" {
		t.Fatalf("chain order put %q first, want slow (earlier transaction id)", first)
	}
}
