CREATE TABLE ledger_balance_monitors (
    id          uuid          PRIMARY KEY,
    account_id  uuid          NOT NULL REFERENCES ledger_accounts (id),
    field       text          NOT NULL CHECK (field IN ('pending', 'posted', 'available')),
    operator    text          NOT NULL CHECK (operator IN ('gt', 'gte', 'eq', 'lt', 'lte', 'not_eq')),
    value       numeric(38,0) NOT NULL,
    description text          NOT NULL DEFAULT '',
    metadata    jsonb         NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(metadata) = 'object'),
    version     bigint        NOT NULL DEFAULT 0,
    created_at  timestamptz   NOT NULL DEFAULT now()
);

CREATE INDEX ledger_balance_monitors_account_idx ON ledger_balance_monitors (account_id, id);

CREATE FUNCTION ledger_balance_monitors_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.id <> OLD.id OR NEW.account_id <> OLD.account_id OR NEW.field <> OLD.field
        OR NEW.operator <> OLD.operator OR NEW.value <> OLD.value OR NEW.created_at <> OLD.created_at THEN
        RAISE EXCEPTION 'balance monitor condition is immutable; create a new monitor instead'
            USING ERRCODE = 'restrict_violation';
    END IF;
    RETURN NEW;
END
$$;

CREATE TRIGGER ledger_balance_monitors_guard
    BEFORE UPDATE ON ledger_balance_monitors
    FOR EACH ROW EXECUTE FUNCTION ledger_balance_monitors_guard();
