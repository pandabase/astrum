CREATE INDEX ledger_transactions_metadata_idx ON ledger_transactions USING gin (metadata jsonb_path_ops);
CREATE INDEX ledger_accounts_metadata_idx ON ledger_accounts USING gin (metadata jsonb_path_ops);

-- Statements --

CREATE TABLE ledger_statements (
    id               uuid          PRIMARY KEY,
    ledger_id        uuid          NOT NULL REFERENCES ledger_ledgers (id),
    account_id       uuid          NOT NULL REFERENCES ledger_accounts (id),
    description      text          NOT NULL DEFAULT '',
    effective_from   timestamptz   NOT NULL,
    effective_until  timestamptz   NOT NULL,
    posted_before    xid8          NOT NULL,
    starting_debits  numeric(38,0) NOT NULL CHECK (starting_debits >= 0),
    starting_credits numeric(38,0) NOT NULL CHECK (starting_credits >= 0),
    ending_debits    numeric(38,0) NOT NULL CHECK (ending_debits >= starting_debits),
    ending_credits   numeric(38,0) NOT NULL CHECK (ending_credits >= starting_credits),
    entry_count      bigint        NOT NULL CHECK (entry_count >= 0),
    created_at       timestamptz   NOT NULL DEFAULT now(),
    CHECK (effective_until > effective_from)
);

CREATE INDEX ledger_statements_account_idx ON ledger_statements (account_id, id);

CREATE TRIGGER ledger_statements_immutable
    BEFORE UPDATE OR DELETE ON ledger_statements
    FOR EACH ROW EXECUTE FUNCTION ledger_reject_mutation();

CREATE TRIGGER ledger_statements_no_truncate
    BEFORE TRUNCATE ON ledger_statements
    FOR EACH STATEMENT EXECUTE FUNCTION ledger_reject_mutation();
