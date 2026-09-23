CREATE TABLE ledger_ledgers (
    id          uuid        PRIMARY KEY,
    name        text        NOT NULL CHECK (name <> ''),
    description text        NOT NULL DEFAULT '',
    metadata    jsonb       NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(metadata) = 'object'),
    version     bigint      NOT NULL DEFAULT 0,
    created_at  timestamptz NOT NULL DEFAULT now()
);

CREATE TRIGGER ledger_ledgers_no_delete
    BEFORE DELETE ON ledger_ledgers
    FOR EACH ROW EXECUTE FUNCTION ledger_reject_mutation();

CREATE TRIGGER ledger_ledgers_no_truncate
    BEFORE TRUNCATE ON ledger_ledgers
    FOR EACH STATEMENT EXECUTE FUNCTION ledger_reject_mutation();

INSERT INTO ledger_ledgers (id, name)
SELECT gen_random_uuid(), 'Default'
WHERE EXISTS (SELECT 1 FROM ledger_accounts);

ALTER TABLE ledger_accounts
    ADD COLUMN ledger_id   uuid,
    ADD COLUMN name        text  NOT NULL DEFAULT '',
    ADD COLUMN description text  NOT NULL DEFAULT '',
    ADD COLUMN metadata    jsonb NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(metadata) = 'object');

UPDATE ledger_accounts SET ledger_id = (SELECT id FROM ledger_ledgers), version = version + 1;

ALTER TABLE ledger_accounts
    ALTER COLUMN ledger_id SET NOT NULL,
    ADD CONSTRAINT ledger_accounts_ledger_id_fkey FOREIGN KEY (ledger_id) REFERENCES ledger_ledgers (id),
    DROP CONSTRAINT ledger_accounts_code_key,
    ADD CONSTRAINT ledger_accounts_ledger_id_code_key UNIQUE (ledger_id, code);

CREATE OR REPLACE FUNCTION ledger_accounts_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.id <> OLD.id OR NEW.ledger_id <> OLD.ledger_id OR NEW.code <> OLD.code OR NEW.currency <> OLD.currency
        OR NEW.normal_side <> OLD.normal_side OR NEW.created_at <> OLD.created_at THEN
        RAISE EXCEPTION 'ledger account identity is immutable'
            USING ERRCODE = 'restrict_violation';
    END IF;
    IF NEW.version <= OLD.version THEN
        RAISE EXCEPTION 'ledger account version must increase'
            USING ERRCODE = 'restrict_violation';
    END IF;
    RETURN NEW;
END
$$;

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
        GROUP BY n.transaction_id
        HAVING count(DISTINCT a.ledger_id) > 1
    ) THEN
        RAISE EXCEPTION 'ledger transaction spans more than one ledger'
            USING ERRCODE = 'check_violation', CONSTRAINT = 'ledger_postings_same_ledger';
    END IF;

    IF EXISTS (
        SELECT 1
        FROM (SELECT DISTINCT transaction_id FROM new_postings) AS n
        LEFT JOIN ledger_transactions AS t ON t.id = n.transaction_id
        WHERE t.id IS NULL OR t.created_xid <> pg_current_xact_id()
    ) THEN
        RAISE EXCEPTION 'postings can only be added to a transaction created in the same database transaction'
            USING ERRCODE = 'restrict_violation';
    END IF;

    RETURN NULL;
END
$$;
