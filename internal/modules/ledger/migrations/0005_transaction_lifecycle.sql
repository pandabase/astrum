-- Transactions --

DROP TRIGGER ledger_transactions_immutable ON ledger_transactions;

ALTER TABLE ledger_transactions
    ADD COLUMN ledger_id       uuid,
    ADD COLUMN status          text        NOT NULL DEFAULT 'posted'
                                           CHECK (status IN ('pending', 'posted', 'archived')),
    ADD COLUMN version         integer     NOT NULL DEFAULT 1 CHECK (version > 0),
    ADD COLUMN entries_version integer     NOT NULL DEFAULT 1 CHECK (entries_version > 0),
    ADD COLUMN effective_at    timestamptz,
    ADD COLUMN external_id     text        CHECK (external_id <> ''),
    ADD COLUMN posted_at       timestamptz,
    ADD COLUMN posted_xid      xid8,
    ADD COLUMN archived_at     timestamptz,
    -- the create request of a transaction that started pending, so retries still match after it changes
    ADD COLUMN pending_request jsonb;

UPDATE ledger_transactions AS t
SET ledger_id = p.ledger_id, effective_at = t.created_at, posted_at = t.created_at, posted_xid = t.created_xid
FROM (
    SELECT DISTINCT ON (p.transaction_id) p.transaction_id, a.ledger_id
    FROM ledger_postings AS p
    JOIN ledger_accounts AS a ON a.id = p.account_id
    ORDER BY p.transaction_id
) AS p
WHERE p.transaction_id = t.id;

ALTER TABLE ledger_transactions
    ALTER COLUMN ledger_id SET NOT NULL,
    ALTER COLUMN effective_at SET NOT NULL,
    ALTER COLUMN effective_at SET DEFAULT now(),
    ADD CONSTRAINT ledger_transactions_external_id_key UNIQUE (ledger_id, external_id),
    ADD CONSTRAINT ledger_transactions_posted_check
        CHECK ((status = 'posted') = (posted_at IS NOT NULL AND posted_xid IS NOT NULL)),
    ADD CONSTRAINT ledger_transactions_archived_check CHECK ((status = 'archived') = (archived_at IS NOT NULL)),
    ADD CONSTRAINT ledger_transactions_entries_version_current CHECK (entries_version <= version);

DROP INDEX ledger_transactions_commit_order_idx;
CREATE INDEX ledger_transactions_posted_order_idx ON ledger_transactions (posted_xid, id) WHERE status = 'posted';
CREATE INDEX ledger_transactions_ledger_id_idx ON ledger_transactions (ledger_id, id);

CREATE FUNCTION ledger_transactions_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF OLD.status <> 'pending' THEN
        RAISE EXCEPTION 'ledger transaction % is %', OLD.id, OLD.status
            USING ERRCODE = 'restrict_violation', CONSTRAINT = 'ledger_transactions_not_pending';
    END IF;
    IF NEW.id <> OLD.id OR NEW.idempotency_key <> OLD.idempotency_key OR NEW.ledger_id <> OLD.ledger_id
        OR NEW.reverses_id IS DISTINCT FROM OLD.reverses_id OR NEW.created_at <> OLD.created_at
        OR NEW.created_xid <> OLD.created_xid OR NEW.pending_request IS DISTINCT FROM OLD.pending_request THEN
        RAISE EXCEPTION 'ledger transaction identity is immutable'
            USING ERRCODE = 'restrict_violation';
    END IF;
    IF NEW.version <> OLD.version + 1 THEN
        RAISE EXCEPTION 'ledger transaction version must increase by one'
            USING ERRCODE = 'restrict_violation';
    END IF;
    IF NEW.status = 'posted' AND NEW.posted_xid <> pg_current_xact_id() THEN
        RAISE EXCEPTION 'ledger transaction must post in the database transaction that writes its entries'
            USING ERRCODE = 'restrict_violation';
    END IF;
    RETURN NEW;
END
$$;

CREATE TRIGGER ledger_transactions_guard
    BEFORE UPDATE ON ledger_transactions
    FOR EACH ROW EXECUTE FUNCTION ledger_transactions_guard();

CREATE TRIGGER ledger_transactions_no_delete
    BEFORE DELETE ON ledger_transactions
    FOR EACH ROW EXECUTE FUNCTION ledger_reject_mutation();

CREATE OR REPLACE FUNCTION ledger_postings_check() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM new_postings
        GROUP BY transaction_id, currency
        HAVING sum(CASE side WHEN 'debit' THEN amount ELSE -amount END) <> 0
    ) THEN
        RAISE EXCEPTION 'ledger postings are unbalanced'
            USING ERRCODE = 'check_violation', CONSTRAINT = 'ledger_postings_balanced';
    END IF;

    IF EXISTS (
        SELECT 1
        FROM new_postings AS n
        LEFT JOIN ledger_accounts AS a ON a.id = n.account_id AND a.currency = n.currency
        WHERE a.id IS NULL
    ) THEN
        RAISE EXCEPTION 'ledger posting references a missing account or the wrong currency'
            USING ERRCODE = 'foreign_key_violation', CONSTRAINT = 'ledger_postings_account_fkey';
    END IF;

    IF EXISTS (
        SELECT 1
        FROM new_postings AS n
        JOIN ledger_accounts AS a ON a.id = n.account_id
        JOIN ledger_transactions AS t ON t.id = n.transaction_id
        WHERE a.ledger_id <> t.ledger_id
    ) THEN
        RAISE EXCEPTION 'ledger transaction spans more than one ledger'
            USING ERRCODE = 'check_violation', CONSTRAINT = 'ledger_postings_same_ledger';
    END IF;

    IF EXISTS (
        SELECT 1
        FROM (SELECT DISTINCT transaction_id FROM new_postings) AS n
        LEFT JOIN ledger_transactions AS t ON t.id = n.transaction_id
        WHERE t.id IS NULL OR t.status <> 'posted' OR t.posted_xid <> pg_current_xact_id()
    ) THEN
        RAISE EXCEPTION 'postings can only be added to a transaction as it posts'
            USING ERRCODE = 'restrict_violation';
    END IF;

    RETURN NULL;
END
$$;

-- Pending entries --

CREATE TABLE ledger_pending_entries (
    id             bigint        GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    transaction_id uuid          NOT NULL,
    version        integer       NOT NULL,
    account_id     uuid          NOT NULL,
    currency       text          NOT NULL,
    side           text          NOT NULL CHECK (side IN ('debit', 'credit')),
    amount         numeric(38,0) NOT NULL CHECK (amount > 0)
);

CREATE INDEX ledger_pending_entries_transaction_idx ON ledger_pending_entries (transaction_id, version, id);
CREATE INDEX ledger_pending_entries_account_idx ON ledger_pending_entries (account_id, transaction_id);

CREATE FUNCTION ledger_pending_entries_check() RETURNS trigger LANGUAGE plpgsql AS $$
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
        WHERE t.status <> 'pending' OR n.version <> t.entries_version
    ) THEN
        RAISE EXCEPTION 'pending entries can only be added as the current version of a pending transaction'
            USING ERRCODE = 'restrict_violation';
    END IF;

    RETURN NULL;
END
$$;

CREATE TRIGGER ledger_pending_entries_check
    AFTER INSERT ON ledger_pending_entries
    REFERENCING NEW TABLE AS new_entries
    FOR EACH STATEMENT EXECUTE FUNCTION ledger_pending_entries_check();

CREATE TRIGGER ledger_pending_entries_immutable
    BEFORE UPDATE OR DELETE ON ledger_pending_entries
    FOR EACH ROW EXECUTE FUNCTION ledger_reject_mutation();

CREATE TRIGGER ledger_pending_entries_no_truncate
    BEFORE TRUNCATE ON ledger_pending_entries
    FOR EACH STATEMENT EXECUTE FUNCTION ledger_reject_mutation();

-- Accounts --

ALTER TABLE ledger_accounts
    ADD COLUMN posted_debits   numeric(38,0) NOT NULL DEFAULT 0 CHECK (posted_debits >= 0),
    ADD COLUMN posted_credits  numeric(38,0) NOT NULL DEFAULT 0 CHECK (posted_credits >= 0),
    ADD COLUMN pending_debits  numeric(38,0) NOT NULL DEFAULT 0 CHECK (pending_debits >= 0),
    ADD COLUMN pending_credits numeric(38,0) NOT NULL DEFAULT 0 CHECK (pending_credits >= 0);

UPDATE ledger_accounts AS a
SET posted_debits = p.debits, posted_credits = p.credits, version = a.version + 1
FROM (
    SELECT account_id,
           coalesce(sum(amount) FILTER (WHERE side = 'debit'), 0) AS debits,
           coalesce(sum(amount) FILTER (WHERE side = 'credit'), 0) AS credits
    FROM ledger_postings
    GROUP BY account_id
) AS p
WHERE p.account_id = a.id;

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM ledger_accounts WHERE balance <> posted_debits - posted_credits) THEN
        RAISE EXCEPTION 'account balances do not match their postings; run the integrity check before upgrading';
    END IF;
END
$$;

CREATE OR REPLACE FUNCTION ledger_accounts_status_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF OLD.status = 'closed' AND NEW.status <> 'closed' THEN
        RAISE EXCEPTION 'ledger account % is closed', OLD.id
            USING ERRCODE = 'restrict_violation', CONSTRAINT = 'ledger_accounts_not_open';
    END IF;
    IF OLD.status <> 'open' AND (
        NEW.posted_debits <> OLD.posted_debits OR NEW.posted_credits <> OLD.posted_credits
        OR NEW.pending_debits > OLD.pending_debits OR NEW.pending_credits > OLD.pending_credits
        OR NEW.held > OLD.held
    ) THEN
        RAISE EXCEPTION 'ledger account % is %', OLD.id, OLD.status
            USING ERRCODE = 'restrict_violation', CONSTRAINT = 'ledger_accounts_not_open';
    END IF;
    RETURN NEW;
END
$$;

ALTER TABLE ledger_accounts
    DROP CONSTRAINT ledger_accounts_funds_check,
    DROP CONSTRAINT ledger_accounts_closed_empty,
    DROP COLUMN balance,
    -- available = posted on the normal side, minus pending outflows and holds
    ADD CONSTRAINT ledger_accounts_funds_check CHECK (
        allow_negative
        OR (CASE normal_side
                WHEN 'debit' THEN posted_debits - posted_credits - pending_credits
                ELSE posted_credits - posted_debits - pending_debits
            END) - held + overdraft_limit >= 0
    ),
    ADD CONSTRAINT ledger_accounts_closed_empty CHECK (
        status <> 'closed'
        OR (posted_debits = posted_credits AND pending_debits = 0 AND pending_credits = 0 AND held = 0)
    );

-- Seals --

ALTER TABLE ledger_seals ADD COLUMN encoding smallint CHECK (encoding = 3);
