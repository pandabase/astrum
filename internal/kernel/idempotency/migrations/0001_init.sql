CREATE TABLE idempotency_keys (
    key                   text        PRIMARY KEY CHECK (key <> ''),
    request_hash          bytea       NOT NULL,
    status                text        NOT NULL CHECK (status IN ('processing', 'completed')),
    lock_token            uuid        NOT NULL,
    response_status       integer,
    response_content_type text,
    response_body         bytea,
    locked_at             timestamptz NOT NULL DEFAULT now(),
    created_at            timestamptz NOT NULL DEFAULT now(),
    completed_at          timestamptz,
    CHECK (status <> 'completed' OR (response_status IS NOT NULL AND response_body IS NOT NULL))
);

CREATE INDEX idempotency_keys_created_at_idx ON idempotency_keys (created_at);
