-- Bulk requests --

CREATE TABLE ledger_bulk_requests (
    id              uuid        PRIMARY KEY,
    idempotency_key text        NOT NULL CHECK (idempotency_key <> ''),
    request_hash    bytea       NOT NULL,
    status          text        NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'processing', 'completed')),
    total           integer     NOT NULL CHECK (total > 0),
    processed       integer     NOT NULL DEFAULT 0 CHECK (processed BETWEEN 0 AND total),
    succeeded       integer     NOT NULL DEFAULT 0 CHECK (succeeded >= 0),
    failed          integer     NOT NULL DEFAULT 0 CHECK (failed >= 0),
    lease_until     timestamptz,
    created_at      timestamptz NOT NULL DEFAULT now(),
    started_at      timestamptz,
    completed_at    timestamptz,
    CONSTRAINT ledger_bulk_requests_idempotency_key_key UNIQUE (idempotency_key),
    CHECK (succeeded + failed = processed),
    CHECK ((status = 'completed') = (processed = total AND completed_at IS NOT NULL))
);

CREATE INDEX ledger_bulk_requests_queue_idx ON ledger_bulk_requests (created_at) WHERE status <> 'completed';

CREATE TABLE ledger_bulk_items (
    bulk_id        uuid    NOT NULL REFERENCES ledger_bulk_requests (id),
    index          integer NOT NULL CHECK (index >= 0),
    request        jsonb   NOT NULL,
    transaction_id uuid    REFERENCES ledger_transactions (id),
    error_code     text,
    error_detail   text,
    PRIMARY KEY (bulk_id, index),
    CHECK (transaction_id IS NULL OR error_code IS NULL)
);

-- Settlements --

CREATE TABLE ledger_settlements (
    id                       uuid          PRIMARY KEY,
    idempotency_key          text          NOT NULL CHECK (idempotency_key <> ''),
    ledger_id                uuid          NOT NULL REFERENCES ledger_ledgers (id),
    settled_account_id       uuid          NOT NULL REFERENCES ledger_accounts (id),
    contra_account_id        uuid          NOT NULL REFERENCES ledger_accounts (id),
    currency                 text          NOT NULL,
    effective_at_upper_bound timestamptz,
    -- the settled account's net on its normal side before settling; this much moved to the contra account
    amount                   numeric(38,0) NOT NULL,
    entry_count              integer       NOT NULL CHECK (entry_count >= 0),
    transaction_id           uuid          REFERENCES ledger_transactions (id),
    description              text          NOT NULL DEFAULT '',
    metadata                 jsonb         NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(metadata) = 'object'),
    created_at               timestamptz   NOT NULL DEFAULT now(),
    CONSTRAINT ledger_settlements_idempotency_key_key UNIQUE (idempotency_key),
    CHECK (settled_account_id <> contra_account_id),
    CHECK ((amount = 0) = (transaction_id IS NULL))
);

CREATE INDEX ledger_settlements_settled_idx ON ledger_settlements (settled_account_id, id);

CREATE TRIGGER ledger_settlements_immutable
    BEFORE UPDATE OR DELETE ON ledger_settlements
    FOR EACH ROW EXECUTE FUNCTION ledger_reject_mutation();

CREATE TABLE ledger_settlement_entries (
    posting_id    bigint NOT NULL PRIMARY KEY REFERENCES ledger_postings (id),
    settlement_id uuid   NOT NULL REFERENCES ledger_settlements (id)
);

CREATE INDEX ledger_settlement_entries_settlement_idx ON ledger_settlement_entries (settlement_id, posting_id);

CREATE TRIGGER ledger_settlement_entries_immutable
    BEFORE UPDATE OR DELETE ON ledger_settlement_entries
    FOR EACH ROW EXECUTE FUNCTION ledger_reject_mutation();

CREATE FUNCTION ledger_settlement_entries_check() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM new_entries AS n
        JOIN ledger_postings AS p ON p.id = n.posting_id
        JOIN ledger_settlements AS s ON s.id = n.settlement_id
        WHERE p.account_id <> s.settled_account_id
    ) THEN
        RAISE EXCEPTION 'a settlement can only include entries of its settled account'
            USING ERRCODE = 'check_violation', CONSTRAINT = 'ledger_settlement_entries_account';
    END IF;
    RETURN NULL;
END
$$;

CREATE TRIGGER ledger_settlement_entries_check
    AFTER INSERT ON ledger_settlement_entries
    REFERENCING NEW TABLE AS new_entries
    FOR EACH STATEMENT EXECUTE FUNCTION ledger_settlement_entries_check();
