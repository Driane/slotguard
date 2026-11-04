-- Events waiting to be delivered to the webhook endpoint. A row is written in
-- the same transaction as the booking it describes (transactional outbox).
CREATE TABLE outbox_events (
    id               BIGSERIAL PRIMARY KEY,
    event_type       TEXT NOT NULL,
    payload          JSONB NOT NULL,
    created_at       TIMESTAMPTZ NOT NULL,
    attempts         INTEGER NOT NULL DEFAULT 0,
    next_attempt_at  TIMESTAMPTZ NOT NULL,
    delivered_at     TIMESTAMPTZ,
    dead_at          TIMESTAMPTZ,
    last_error       TEXT
);

-- Workers only ever look for events that still have to be delivered.
CREATE INDEX outbox_events_pending ON outbox_events (next_attempt_at)
    WHERE delivered_at IS NULL AND dead_at IS NULL;
