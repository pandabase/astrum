ALTER TABLE ledger_accounts
    ADD COLUMN status text NOT NULL DEFAULT 'open'
        CONSTRAINT ledger_accounts_status_check CHECK (status IN ('open', 'frozen', 'closed')),
    ADD COLUMN status_changed_at timestamptz,
    ADD CONSTRAINT ledger_accounts_closed_empty CHECK (status <> 'closed' OR (balance = 0 AND held = 0));

CREATE FUNCTION ledger_accounts_status_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF OLD.status = 'closed' AND NEW.status <> 'closed' THEN
        RAISE EXCEPTION 'ledger account % is closed', OLD.id
            USING ERRCODE = 'restrict_violation', CONSTRAINT = 'ledger_accounts_not_open';
    END IF;
    IF OLD.status <> 'open' AND (NEW.balance <> OLD.balance OR NEW.held > OLD.held) THEN
        RAISE EXCEPTION 'ledger account % is %', OLD.id, OLD.status
            USING ERRCODE = 'restrict_violation', CONSTRAINT = 'ledger_accounts_not_open';
    END IF;
    RETURN NEW;
END
$$;

CREATE TRIGGER ledger_accounts_status_guard
    BEFORE UPDATE ON ledger_accounts
    FOR EACH ROW EXECUTE FUNCTION ledger_accounts_status_guard();
