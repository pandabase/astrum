ALTER TABLE ledger_accounts
    ADD CONSTRAINT ledger_accounts_id_ledger_currency_key UNIQUE (id, ledger_id, currency);

CREATE TABLE ledger_categories (
    id          uuid        PRIMARY KEY,
    ledger_id   uuid        NOT NULL REFERENCES ledger_ledgers (id),
    currency    text        NOT NULL REFERENCES ledger_currencies (code),
    normal_side text        NOT NULL CHECK (normal_side IN ('debit', 'credit')),
    name        text        NOT NULL CHECK (name <> ''),
    description text        NOT NULL DEFAULT '',
    metadata    jsonb       NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(metadata) = 'object'),
    version     bigint      NOT NULL DEFAULT 0,
    created_at  timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT ledger_categories_id_ledger_currency_key UNIQUE (id, ledger_id, currency)
);

CREATE INDEX ledger_categories_ledger_idx ON ledger_categories (ledger_id, id);
CREATE INDEX ledger_categories_metadata_idx ON ledger_categories USING gin (metadata jsonb_path_ops);

CREATE FUNCTION ledger_categories_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.id <> OLD.id OR NEW.ledger_id <> OLD.ledger_id OR NEW.currency <> OLD.currency
        OR NEW.normal_side <> OLD.normal_side OR NEW.created_at <> OLD.created_at THEN
        RAISE EXCEPTION 'ledger category identity is immutable'
            USING ERRCODE = 'restrict_violation';
    END IF;
    RETURN NEW;
END
$$;

CREATE TRIGGER ledger_categories_guard
    BEFORE UPDATE ON ledger_categories
    FOR EACH ROW EXECUTE FUNCTION ledger_categories_guard();

CREATE TABLE ledger_category_accounts (
    category_id uuid        NOT NULL,
    account_id  uuid        NOT NULL,
    ledger_id   uuid        NOT NULL,
    currency    text        NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (category_id, account_id),
    CONSTRAINT ledger_category_accounts_category_fkey FOREIGN KEY (category_id, ledger_id, currency)
        REFERENCES ledger_categories (id, ledger_id, currency) ON DELETE CASCADE,
    CONSTRAINT ledger_category_accounts_account_fkey FOREIGN KEY (account_id, ledger_id, currency)
        REFERENCES ledger_accounts (id, ledger_id, currency)
);

CREATE INDEX ledger_category_accounts_account_idx ON ledger_category_accounts (account_id);

CREATE TABLE ledger_category_edges (
    parent_id  uuid        NOT NULL,
    child_id   uuid        NOT NULL,
    ledger_id  uuid        NOT NULL,
    currency   text        NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (parent_id, child_id),
    CHECK (parent_id <> child_id),
    CONSTRAINT ledger_category_edges_parent_fkey FOREIGN KEY (parent_id, ledger_id, currency)
        REFERENCES ledger_categories (id, ledger_id, currency) ON DELETE CASCADE,
    CONSTRAINT ledger_category_edges_child_fkey FOREIGN KEY (child_id, ledger_id, currency)
        REFERENCES ledger_categories (id, ledger_id, currency) ON DELETE CASCADE
);

CREATE INDEX ledger_category_edges_child_idx ON ledger_category_edges (child_id);
