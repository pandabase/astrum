CREATE FUNCTION ledger_reject_mutation() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION '% is append-only', TG_TABLE_NAME
        USING ERRCODE = 'restrict_violation';
END
$$;

-- Accounts --

CREATE TABLE ledger_accounts (
    id              uuid        PRIMARY KEY,
    code            text        NOT NULL CHECK (code <> ''),
    currency        char(3)     NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
    normal_side     text        NOT NULL CHECK (normal_side IN ('debit', 'credit')),
    allow_negative  boolean     NOT NULL DEFAULT false,
    overdraft_limit bigint      NOT NULL DEFAULT 0 CHECK (overdraft_limit >= 0),
    -- debits minus credits, in minor units
    balance         bigint      NOT NULL DEFAULT 0,
    -- funds reserved by pending holds, on the normal side
    held            bigint      NOT NULL DEFAULT 0 CHECK (held >= 0),
    version         bigint      NOT NULL DEFAULT 0,
    created_at      timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT ledger_accounts_code_key UNIQUE (code),
    CONSTRAINT ledger_accounts_id_currency_key UNIQUE (id, currency),
    -- last line of defence against overdrawing, independent of application code
    CONSTRAINT ledger_accounts_funds_check CHECK (
        allow_negative
        OR (CASE normal_side WHEN 'debit' THEN balance ELSE -balance END) - held + overdraft_limit >= 0
    )
);

CREATE FUNCTION ledger_accounts_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.id <> OLD.id OR NEW.code <> OLD.code OR NEW.currency <> OLD.currency
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

CREATE TRIGGER ledger_accounts_guard
    BEFORE UPDATE ON ledger_accounts
    FOR EACH ROW EXECUTE FUNCTION ledger_accounts_guard();

CREATE TRIGGER ledger_accounts_no_delete
    BEFORE DELETE ON ledger_accounts
    FOR EACH ROW EXECUTE FUNCTION ledger_reject_mutation();

CREATE TRIGGER ledger_accounts_no_truncate
    BEFORE TRUNCATE ON ledger_accounts
    FOR EACH STATEMENT EXECUTE FUNCTION ledger_reject_mutation();

-- Transactions --

CREATE TABLE ledger_transactions (
    id              uuid        PRIMARY KEY,
    idempotency_key text        NOT NULL CHECK (idempotency_key <> ''),
    description     text        NOT NULL DEFAULT '',
    metadata        jsonb       NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(metadata) = 'object'),
    reverses_id     uuid        REFERENCES ledger_transactions (id),
    created_xid     xid8        NOT NULL DEFAULT pg_current_xact_id(),
    created_at      timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT ledger_transactions_idempotency_key_key UNIQUE (idempotency_key),
    CONSTRAINT ledger_transactions_reverses_id_key UNIQUE (reverses_id),
    CHECK (reverses_id <> id)
);

CREATE TABLE ledger_postings (
    id             bigint  GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    transaction_id uuid    NOT NULL,
    account_id     uuid    NOT NULL,
    currency       char(3) NOT NULL,
    side           text    NOT NULL CHECK (side IN ('debit', 'credit')),
    amount         bigint  NOT NULL CHECK (amount > 0),
    -- account balance (debits minus credits) immediately after this posting
    balance_after  bigint  NOT NULL
);

CREATE INDEX ledger_postings_transaction_id_idx ON ledger_postings (transaction_id);
CREATE INDEX ledger_postings_account_id_idx ON ledger_postings (account_id, id);

CREATE FUNCTION ledger_postings_check() RETURNS trigger LANGUAGE plpgsql AS $$
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

CREATE TRIGGER ledger_postings_check
    AFTER INSERT ON ledger_postings
    REFERENCING NEW TABLE AS new_postings
    FOR EACH STATEMENT EXECUTE FUNCTION ledger_postings_check();

CREATE TRIGGER ledger_transactions_immutable
    BEFORE UPDATE OR DELETE ON ledger_transactions
    FOR EACH ROW EXECUTE FUNCTION ledger_reject_mutation();

CREATE TRIGGER ledger_transactions_no_truncate
    BEFORE TRUNCATE ON ledger_transactions
    FOR EACH STATEMENT EXECUTE FUNCTION ledger_reject_mutation();

CREATE TRIGGER ledger_postings_immutable
    BEFORE UPDATE OR DELETE ON ledger_postings
    FOR EACH ROW EXECUTE FUNCTION ledger_reject_mutation();

CREATE TRIGGER ledger_postings_no_truncate
    BEFORE TRUNCATE ON ledger_postings
    FOR EACH STATEMENT EXECUTE FUNCTION ledger_reject_mutation();

-- Seals --

CREATE TABLE ledger_seals (
    seq            bigint      PRIMARY KEY CHECK (seq > 0),
    transaction_id uuid        NOT NULL REFERENCES ledger_transactions (id),
    entry_hash     bytea       NOT NULL CHECK (length(entry_hash) = 32),
    chain_hash     bytea       NOT NULL CHECK (length(chain_hash) = 32),
    sealed_at      timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT ledger_seals_transaction_id_key UNIQUE (transaction_id)
);

CREATE INDEX ledger_transactions_commit_order_idx ON ledger_transactions (created_xid, id);

CREATE TRIGGER ledger_seals_immutable
    BEFORE UPDATE OR DELETE ON ledger_seals
    FOR EACH ROW EXECUTE FUNCTION ledger_reject_mutation();

CREATE TRIGGER ledger_seals_no_truncate
    BEFORE TRUNCATE ON ledger_seals
    FOR EACH STATEMENT EXECUTE FUNCTION ledger_reject_mutation();

-- Holds --

CREATE TABLE ledger_holds (
    id                     uuid        PRIMARY KEY,
    idempotency_key        text        NOT NULL CHECK (idempotency_key <> ''),
    account_id             uuid        NOT NULL,
    currency               char(3)     NOT NULL,
    amount                 bigint      NOT NULL CHECK (amount > 0),
    status                 text        NOT NULL DEFAULT 'pending'
                                       CHECK (status IN ('pending', 'captured', 'voided', 'expired')),
    description            text        NOT NULL DEFAULT '',
    expires_at             timestamptz NOT NULL,
    created_at             timestamptz NOT NULL DEFAULT now(),
    resolved_at            timestamptz,
    captured_amount        bigint      CHECK (captured_amount > 0 AND captured_amount <= amount),
    capture_transaction_id uuid        REFERENCES ledger_transactions (id),
    CONSTRAINT ledger_holds_idempotency_key_key UNIQUE (idempotency_key),
    FOREIGN KEY (account_id, currency) REFERENCES ledger_accounts (id, currency),
    CHECK ((status = 'pending') = (resolved_at IS NULL)),
    CHECK ((status = 'captured') = (capture_transaction_id IS NOT NULL AND captured_amount IS NOT NULL))
);

CREATE INDEX ledger_holds_expiry_idx ON ledger_holds (expires_at) WHERE status = 'pending';
CREATE INDEX ledger_holds_account_idx ON ledger_holds (account_id) WHERE status = 'pending';

CREATE FUNCTION ledger_holds_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF OLD.status <> 'pending' THEN
        RAISE EXCEPTION 'ledger hold % is already %', OLD.id, OLD.status
            USING ERRCODE = 'restrict_violation';
    END IF;
    IF NEW.id <> OLD.id OR NEW.idempotency_key <> OLD.idempotency_key OR NEW.account_id <> OLD.account_id
        OR NEW.currency <> OLD.currency OR NEW.amount <> OLD.amount OR NEW.expires_at <> OLD.expires_at
        OR NEW.created_at <> OLD.created_at THEN
        RAISE EXCEPTION 'ledger hold terms are immutable'
            USING ERRCODE = 'restrict_violation';
    END IF;
    RETURN NEW;
END
$$;

CREATE TRIGGER ledger_holds_guard
    BEFORE UPDATE ON ledger_holds
    FOR EACH ROW EXECUTE FUNCTION ledger_holds_guard();

CREATE TRIGGER ledger_holds_no_delete
    BEFORE DELETE ON ledger_holds
    FOR EACH ROW EXECUTE FUNCTION ledger_reject_mutation();

-- Scheduled transactions --

CREATE TABLE ledger_scheduled_transactions (
    id              uuid        PRIMARY KEY,
    idempotency_key text        NOT NULL CHECK (idempotency_key <> ''),
    execute_at      timestamptz NOT NULL,
    request         jsonb       NOT NULL,
    status          text        NOT NULL DEFAULT 'scheduled'
                                CHECK (status IN ('scheduled', 'executed', 'failed', 'canceled')),
    transaction_id  uuid        REFERENCES ledger_transactions (id),
    failure         text,
    created_at      timestamptz NOT NULL DEFAULT now(),
    resolved_at     timestamptz,
    CONSTRAINT ledger_scheduled_transactions_idempotency_key_key UNIQUE (idempotency_key),
    CHECK ((status = 'scheduled') = (resolved_at IS NULL)),
    CHECK ((status = 'executed') = (transaction_id IS NOT NULL)),
    CHECK ((status = 'failed') = (failure IS NOT NULL))
);

CREATE INDEX ledger_scheduled_due_idx ON ledger_scheduled_transactions (execute_at) WHERE status = 'scheduled';

CREATE FUNCTION ledger_scheduled_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF OLD.status <> 'scheduled' THEN
        RAISE EXCEPTION 'scheduled transaction % is already %', OLD.id, OLD.status
            USING ERRCODE = 'restrict_violation';
    END IF;
    IF NEW.id <> OLD.id OR NEW.idempotency_key <> OLD.idempotency_key OR NEW.execute_at <> OLD.execute_at
        OR NEW.request <> OLD.request OR NEW.created_at <> OLD.created_at THEN
        RAISE EXCEPTION 'scheduled transaction terms are immutable'
            USING ERRCODE = 'restrict_violation';
    END IF;
    RETURN NEW;
END
$$;

CREATE TRIGGER ledger_scheduled_guard
    BEFORE UPDATE ON ledger_scheduled_transactions
    FOR EACH ROW EXECUTE FUNCTION ledger_scheduled_guard();

CREATE TRIGGER ledger_scheduled_no_delete
    BEFORE DELETE ON ledger_scheduled_transactions
    FOR EACH ROW EXECUTE FUNCTION ledger_reject_mutation();
