package tests

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/pandabase/astrum/internal/kernel/testdb"
	"github.com/pandabase/astrum/internal/modules/ledger"
)

func feSealAll(t *testing.T, e *env) {
	t.Helper()
	ctx := context.Background()
	deadline := time.Now().Add(time.Minute)
	for {
		if _, err := e.m.Seal(ctx); err != nil {
			t.Fatalf("Seal() error = %v", err)
		}
		var unsealed int
		if err := e.pool.QueryRow(ctx, `
			SELECT count(*) FROM ledger_transactions AS t
			WHERE t.status = 'posted' AND NOT EXISTS (SELECT 1 FROM ledger_seals AS s WHERE s.transaction_id = t.id)`).Scan(&unsealed); err != nil {
			t.Fatal(err)
		}
		if unsealed == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d transactions still unsealed", unsealed)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func feSealedLedger(t *testing.T) (*env, ledger.Transaction) {
	t.Helper()
	e := setup(t)
	a := e.funded(t, 1_000)
	b := e.account(t, "USD", ledger.Debit)
	txn := e.post(t, transfer("victim", a.ID, b.ID, 400))
	e.post(t, transfer("after", a.ID, b.ID, 1))
	feSealAll(t, e)
	if issues := verifyIssues(t, e); len(issues) != 0 {
		t.Fatalf("untampered ledger has issues: %v", issues)
	}
	return e, txn
}

func TestSealEdgeEmptyChain(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	if n, err := e.m.Seal(ctx); err != nil || n != 0 {
		t.Fatalf("Seal() on an empty ledger = %d, %v", n, err)
	}
	report, err := e.m.Verify(ctx)
	if err != nil || !report.OK || report.Issues == nil || len(report.Issues) != 0 {
		t.Fatalf("Verify() = %+v, %v", report, err)
	}
	if want := "0:" + strings.Repeat("0", 64); report.ChainHead != want {
		t.Fatalf("chain head = %s, want %s", report.ChainHead, want)
	}
	other, err := ledger.New(e.pool, testdb.Logger(), ledger.Config{SealKey: []byte(strings.Repeat("o", 32))})
	if err != nil {
		t.Fatal(err)
	}
	if err := other.CheckSealKey(ctx); err != nil {
		t.Fatalf("any key matches an empty chain: %v", err)
	}
	if report, err := other.Verify(ctx); err != nil || !report.OK {
		t.Fatalf("Verify() with another key on an empty chain = %+v, %v", report, err)
	}

	t.Run("pending and archived transactions are not sealed", func(t *testing.T) {
		a := e.account(t, "USD", ledger.Debit, unrestricted)
		b := e.account(t, "USD", ledger.Debit, unrestricted)
		p := e.post(t, pending(transfer("seal-pending", a.ID, b.ID, 1)))
		archived := e.post(t, pending(transfer("seal-archived", a.ID, b.ID, 1)))
		if _, err := e.m.ArchiveTransaction(ctx, archived.ID); err != nil {
			t.Fatal(err)
		}
		if n, err := e.m.Seal(ctx); err != nil || n != 0 {
			t.Fatalf("Seal() = %d, %v; want 0", n, err)
		}
		var sealable int
		if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM ledger_transactions WHERE status = 'posted'`).Scan(&sealable); err != nil || sealable != 0 {
			t.Fatalf("posted transactions = %d, %v", sealable, err)
		}
		if _, err := e.m.PostTransaction(ctx, p.ID, ledger.PostPendingInput{}); err != nil {
			t.Fatal(err)
		}
		feSealAll(t, e)
		report, err := e.m.Verify(ctx)
		if err != nil || !report.OK || !strings.HasPrefix(report.ChainHead, "1:") {
			t.Fatalf("Verify() = %+v, %v", report, err)
		}
	})
}

func TestSealEdgeKeyLength(t *testing.T) {
	tests := []struct {
		name string
		key  []byte
		ok   bool
	}{
		{"nil", nil, false},
		{"empty", []byte{}, false},
		{"31 bytes", []byte(strings.Repeat("k", 31)), false},
		{"32 bytes", []byte(strings.Repeat("k", 32)), true},
		{"64 bytes", []byte(strings.Repeat("k", 64)), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, err := ledger.New(nil, testdb.Logger(), ledger.Config{SealKey: tt.key})
			if (err == nil) != tt.ok || (m != nil) != tt.ok {
				t.Fatalf("New() = %v, %v; want ok=%v", m, err, tt.ok)
			}
		})
	}

	t.Run("the key is copied", func(t *testing.T) {
		key := []byte(strings.Repeat("s", 40))
		e := setupWith(t, ledger.Config{SealKey: key})
		a := e.funded(t, 10)
		e.post(t, transfer("copied", a.ID, e.open.ID, 1))
		for i := range key {
			key[i] = 'x'
		}
		feSealAll(t, e)
		if issues := verifyIssues(t, e); len(issues) != 0 {
			t.Fatalf("issues after mutating the caller's key: %v", issues)
		}
		if err := e.m.CheckSealKey(context.Background()); err != nil {
			t.Fatalf("mutating the caller's key slice broke the module: %v", err)
		}
	})
}

func TestSealEdgeTamperDetection(t *testing.T) {
	tests := []struct {
		name  string
		sql   string
		wants []string
	}{
		{
			"posting amount alone",
			`UPDATE ledger_postings SET amount = amount + 1 WHERE transaction_id = $1 AND side = 'debit'`,
			[]string{"content was altered", "debits minus credits", "postings sum to"},
		},
		{
			"external id",
			`UPDATE ledger_transactions SET external_id = 'forged' WHERE id = $1`,
			[]string{"content was altered"},
		},
		{
			"effective at",
			`UPDATE ledger_transactions SET effective_at = effective_at - interval '1 day' WHERE id = $1`,
			[]string{"content was altered"},
		},
		{
			"idempotency key",
			`UPDATE ledger_transactions SET idempotency_key = 'other' WHERE id = $1`,
			[]string{"content was altered"},
		},
		{
			"metadata",
			`UPDATE ledger_transactions SET metadata = '{"x":1}' WHERE id = $1`,
			[]string{"content was altered"},
		},
		{
			"posting side swap",
			`UPDATE ledger_postings SET side = CASE side WHEN 'debit' THEN 'credit' ELSE 'debit' END WHERE transaction_id = $1`,
			[]string{"content was altered", "postings sum to"},
		},
		{
			"balance after",
			`UPDATE ledger_postings SET balance_after = balance_after + 1 WHERE transaction_id = $1`,
			[]string{"content was altered"},
		},
		{
			"entry hash",
			`UPDATE ledger_seals SET entry_hash = sha256(entry_hash) WHERE transaction_id = $1`,
			[]string{"content was altered", "chain hash does not verify"},
		},
		{
			"legacy encoding",
			`UPDATE ledger_seals SET encoding = NULL WHERE transaction_id = $1`,
			[]string{"content was altered"},
		},
		{
			"sequence gap",
			`UPDATE ledger_seals SET seq = seq + 100 WHERE seq = (SELECT max(seq) FROM ledger_seals) AND $1::uuid IS NOT NULL`,
			[]string{"expected seq"},
		},
		{
			"held funds",
			`UPDATE ledger_accounts SET held = held + 1 WHERE id IN (SELECT account_id FROM ledger_postings WHERE transaction_id = $1 AND side = 'credit')`,
			[]string{"pending holds sum to"},
		},
		{
			"pending totals",
			`UPDATE ledger_accounts SET pending_debits = pending_debits + 1 WHERE id IN (SELECT account_id FROM ledger_postings WHERE transaction_id = $1 AND side = 'debit')`,
			[]string{"pending entries sum to"},
		},
		{
			"last running balance",
			`UPDATE ledger_postings SET balance_after = balance_after + 7 WHERE id = (SELECT max(id) FROM ledger_postings) AND $1::uuid IS NOT NULL`,
			[]string{"last posting balance_after", "content was altered"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e, txn := feSealedLedger(t)
			asAttacker(t, e, tt.sql, txn.ID)
			issues := verifyIssues(t, e)
			for _, want := range tt.wants {
				requireIssue(t, issues, want)
			}
			report, err := e.m.Verify(context.Background())
			if err != nil || report.OK {
				t.Fatalf("Verify() = %+v, %v; want not OK", report, err)
			}
		})
	}
}

func TestSealEdgeSettlementTamper(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	acme := e.account(t, "USD", ledger.Credit, unrestricted)
	payouts := e.account(t, "USD", ledger.Debit, unrestricted)
	e.post(t, transfer("st-1", acme.ID, payouts.ID, 10))
	e.post(t, transfer("st-2", acme.ID, payouts.ID, 5))
	st, err := e.m.CreateSettlement(ctx, settle("st", acme.ID, payouts.ID))
	if err != nil {
		t.Fatal(err)
	}
	feSealAll(t, e)
	asAttacker(t, e, `DELETE FROM ledger_settlement_entries WHERE settlement_id = $1 AND posting_id = (
		SELECT min(posting_id) FROM ledger_settlement_entries WHERE settlement_id = $1)`, st.ID)
	requireIssue(t, verifyIssues(t, e), "settled entries net to")
}
