package ledger

import (
	"context"
	"fmt"

	"github.com/charmbracelet/log"
	"github.com/jackc/pgx/v5"
)

var integrityChecks = []struct {
	name string
	sql  string
}{
	{
		name: "trial balance",
		sql: `
			SELECT format('currency %s: debits minus credits = %s', currency, total)
			FROM (
				SELECT currency, sum(CASE side WHEN 'debit' THEN amount ELSE -amount END) AS total
				FROM ledger_postings
				GROUP BY currency
			) AS t
			WHERE total <> 0`,
	},
	{
		name: "empty transactions",
		sql: `
			SELECT format('transaction %s (%s) has no entries', t.id, t.status)
			FROM ledger_transactions AS t
			WHERE NOT EXISTS (SELECT 1 FROM ledger_postings AS p WHERE p.transaction_id = t.id)
			  AND NOT EXISTS (
				SELECT 1 FROM ledger_pending_entries AS e WHERE e.transaction_id = t.id AND e.version = t.entries_version)`,
	},
	{
		name: "journal status",
		sql: `
			SELECT format('transaction %s is %s but has journal postings', t.id, t.status)
			FROM ledger_transactions AS t
			WHERE t.status <> 'posted' AND EXISTS (SELECT 1 FROM ledger_postings AS p WHERE p.transaction_id = t.id)`,
	},
	{
		name: "posted totals",
		sql: `
			SELECT format('account %s: posted debits %s credits %s but postings sum to %s and %s',
				a.id, a.posted_debits, a.posted_credits, coalesce(p.debits, 0), coalesce(p.credits, 0))
			FROM ledger_accounts AS a
			LEFT JOIN (
				SELECT account_id,
				       sum(amount) FILTER (WHERE side = 'debit') AS debits,
				       sum(amount) FILTER (WHERE side = 'credit') AS credits
				FROM ledger_postings
				GROUP BY account_id
			) AS p ON p.account_id = a.id
			WHERE a.posted_debits <> coalesce(p.debits, 0) OR a.posted_credits <> coalesce(p.credits, 0)`,
	},
	{
		name: "pending totals",
		sql: `
			SELECT format('account %s: pending debits %s credits %s but pending entries sum to %s and %s',
				a.id, a.pending_debits, a.pending_credits, coalesce(p.debits, 0), coalesce(p.credits, 0))
			FROM ledger_accounts AS a
			LEFT JOIN (
				SELECT e.account_id,
				       sum(e.amount) FILTER (WHERE e.side = 'debit') AS debits,
				       sum(e.amount) FILTER (WHERE e.side = 'credit') AS credits
				FROM ledger_pending_entries AS e
				JOIN ledger_transactions AS t ON t.id = e.transaction_id
				WHERE t.status = 'pending' AND e.version = t.entries_version
				GROUP BY e.account_id
			) AS p ON p.account_id = a.id
			WHERE a.pending_debits <> coalesce(p.debits, 0) OR a.pending_credits <> coalesce(p.credits, 0)`,
	},
	{
		name: "held funds",
		sql: `
			SELECT format('account %s: held %s but pending holds sum to %s', a.id, a.held, coalesce(h.total, 0))
			FROM ledger_accounts AS a
			LEFT JOIN (
				SELECT account_id, sum(amount) AS total
				FROM ledger_holds
				WHERE status = 'pending'
				GROUP BY account_id
			) AS h ON h.account_id = a.id
			WHERE a.held <> coalesce(h.total, 0)`,
	},
	{
		name: "settlements",
		sql: `
			SELECT format('settlement %s: settled entries net to %s', s.id, e.net)
			FROM ledger_settlements AS s
			JOIN LATERAL (
				SELECT coalesce(sum(CASE p.side WHEN 'debit' THEN p.amount ELSE -p.amount END), 0) AS net
				FROM ledger_settlement_entries AS se
				JOIN ledger_postings AS p ON p.id = se.posting_id
				WHERE se.settlement_id = s.id
			) AS e ON true
			WHERE e.net <> 0`,
	},
	{
		name: "running balance",
		sql: `
			SELECT format('account %s: last posting balance_after %s but posted balance %s',
				a.id, p.balance_after, a.posted_debits - a.posted_credits)
			FROM ledger_accounts AS a
			JOIN LATERAL (
				SELECT balance_after FROM ledger_postings WHERE account_id = a.id ORDER BY id DESC LIMIT 1
			) AS p ON true
			WHERE p.balance_after <> a.posted_debits - a.posted_credits`,
	},
}

func (s *service) verify(ctx context.Context) (VerifyReport, error) {
	op := s.begin(ctx, "verify")

	report := VerifyReport{Issues: []string{}}
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly},
		func(tx pgx.Tx) error {
			for _, check := range integrityChecks {
				rows, err := tx.Query(ctx, check.sql)
				if err != nil {
					return fmt.Errorf("%s: %w", check.name, err)
				}
				issues, err := pgx.CollectRows(rows, pgx.RowTo[string])
				if err != nil {
					return fmt.Errorf("%s: %w", check.name, err)
				}
				report.Issues = append(report.Issues, issues...)
			}
			issues, head, err := s.verifyChain(ctx, tx)
			if err != nil {
				return fmt.Errorf("seal chain: %w", err)
			}
			report.ChainHead = head.String()
			report.Issues = append(report.Issues, issues...)
			return nil
		})

	if err != nil {
		return VerifyReport{}, op.fail(err)
	}

	report.OK = len(report.Issues) == 0
	if !report.OK {
		op.logAt(log.ErrorLevel, "ledger integrity violated", "issues", len(report.Issues), "first", report.Issues[0])
		return report, nil
	}

	op.info("ledger verified", "chain_head", report.ChainHead)
	return report, nil
}
