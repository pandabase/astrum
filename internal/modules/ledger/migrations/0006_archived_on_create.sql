CREATE OR REPLACE FUNCTION ledger_pending_entries_check() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM new_entries
        GROUP BY transaction_id, version, currency
        HAVING sum(CASE side WHEN 'debit' THEN amount ELSE -amount END) <> 0
    ) THEN
        RAISE EXCEPTION 'ledger pending entries are unbalanced'
            USING ERRCODE = 'check_violation', CONSTRAINT = 'ledger_postings_balanced';
    END IF;

    IF EXISTS (
        SELECT 1
        FROM new_entries AS n
        LEFT JOIN ledger_accounts AS a ON a.id = n.account_id AND a.currency = n.currency
        LEFT JOIN ledger_transactions AS t ON t.id = n.transaction_id
        WHERE a.id IS NULL OR t.id IS NULL OR a.ledger_id <> t.ledger_id
    ) THEN
        RAISE EXCEPTION 'ledger pending entry references a missing account, the wrong currency or another ledger'
            USING ERRCODE = 'foreign_key_violation', CONSTRAINT = 'ledger_postings_account_fkey';
    END IF;

    IF EXISTS (
        SELECT 1
        FROM new_entries AS n
        JOIN ledger_transactions AS t ON t.id = n.transaction_id
        WHERE n.version <> t.entries_version
           OR NOT (t.status = 'pending'
                   OR (t.status = 'archived' AND t.version = 1 AND t.created_xid = pg_current_xact_id()))
    ) THEN
        RAISE EXCEPTION 'pending entries can only be added as the current version of a pending transaction'
            USING ERRCODE = 'restrict_violation';
    END IF;

    RETURN NULL;
END
$$;
