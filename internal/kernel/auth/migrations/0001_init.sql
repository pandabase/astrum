CREATE TABLE api_keys (
    id           uuid        PRIMARY KEY,
    name         text        NOT NULL CHECK (name <> ''),
    role         text        NOT NULL CHECK (role IN ('admin', 'write', 'read')),
    secret_hash  bytea       NOT NULL CHECK (length(secret_hash) = 32),
    -- the last characters of the key, so people can tell keys apart without seeing them
    hint         text        NOT NULL,
    created_by   uuid        REFERENCES api_keys (id),
    created_at   timestamptz NOT NULL DEFAULT now(),
    expires_at   timestamptz,
    revoked_at   timestamptz,
    last_used_at timestamptz
);

CREATE FUNCTION api_keys_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'api keys are revoked, not deleted'
            USING ERRCODE = 'restrict_violation';
    END IF;
    IF NEW.id <> OLD.id OR NEW.role <> OLD.role OR NEW.secret_hash <> OLD.secret_hash OR NEW.created_at <> OLD.created_at
        OR (OLD.revoked_at IS NOT NULL AND NEW.revoked_at IS DISTINCT FROM OLD.revoked_at) THEN
        RAISE EXCEPTION 'api key identity is immutable and revocation is final'
            USING ERRCODE = 'restrict_violation';
    END IF;
    RETURN NEW;
END
$$;

CREATE TRIGGER api_keys_guard
    BEFORE UPDATE OR DELETE ON api_keys
    FOR EACH ROW EXECUTE FUNCTION api_keys_guard();
