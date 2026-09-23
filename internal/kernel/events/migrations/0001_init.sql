CREATE FUNCTION events_reject_mutation() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION '% is append-only', TG_TABLE_NAME
        USING ERRCODE = 'restrict_violation';
END
$$;

CREATE TABLE events (
    id          uuid        PRIMARY KEY,
    type        text        NOT NULL CHECK (type ~ '^[a-z_]+(\.[a-z_]+)+$'),
    data        json        NOT NULL CHECK (json_typeof(data) = 'object'),
    created_xid xid8        NOT NULL DEFAULT pg_current_xact_id(),
    created_at  timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX events_commit_order_idx ON events (created_xid, id);
CREATE INDEX events_type_idx ON events (type, id);

CREATE TRIGGER events_immutable
    BEFORE UPDATE OR DELETE ON events
    FOR EACH ROW EXECUTE FUNCTION events_reject_mutation();

CREATE TABLE event_dispatch_cursor (
    singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
    last_xid  xid8,
    last_id   uuid
);

INSERT INTO event_dispatch_cursor DEFAULT VALUES;

CREATE TABLE webhook_endpoints (
    id          uuid        PRIMARY KEY,
    url         text        NOT NULL CHECK (url ~ '^https?://'),
    secret      text        NOT NULL CHECK (secret <> ''),
    description text        NOT NULL DEFAULT '',
    -- empty means every event; entries are exact types or prefixes such as transaction.*
    event_types text[]      NOT NULL DEFAULT '{}',
    enabled     boolean     NOT NULL DEFAULT true,
    version     bigint      NOT NULL DEFAULT 0,
    created_at  timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE webhook_deliveries (
    id               uuid        PRIMARY KEY,
    endpoint_id      uuid        NOT NULL REFERENCES webhook_endpoints (id) ON DELETE CASCADE,
    event_id         uuid        NOT NULL REFERENCES events (id),
    status           text        NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'succeeded', 'failed')),
    attempts         integer     NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    next_attempt_at  timestamptz NOT NULL DEFAULT now(),
    last_attempt_at  timestamptz,
    last_status_code integer,
    last_error       text,
    delivered_at     timestamptz,
    created_at       timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT webhook_deliveries_endpoint_event_key UNIQUE (endpoint_id, event_id),
    CHECK ((status = 'succeeded') = (delivered_at IS NOT NULL))
);

CREATE INDEX webhook_deliveries_due_idx ON webhook_deliveries (next_attempt_at) WHERE status = 'pending';
CREATE INDEX webhook_deliveries_event_idx ON webhook_deliveries (event_id);
