-- Period close --

ALTER TABLE ledger_ledgers ADD COLUMN closed_before timestamptz;

-- archiving stays allowed so pending transactions in a closed period can still be voided
CREATE FUNCTION ledger_transactions_period_check() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.status <> 'archived' AND EXISTS (
        SELECT 1 FROM ledger_ledgers WHERE id = NEW.ledger_id AND NEW.effective_at < closed_before
    ) THEN
        RAISE EXCEPTION 'ledger transaction % is dated in a closed period', NEW.id
            USING ERRCODE = 'check_violation', CONSTRAINT = 'ledger_transactions_period_open';
    END IF;
    RETURN NEW;
END
$$;

CREATE TRIGGER ledger_transactions_period_check
    BEFORE INSERT OR UPDATE ON ledger_transactions
    FOR EACH ROW EXECUTE FUNCTION ledger_transactions_period_check();
