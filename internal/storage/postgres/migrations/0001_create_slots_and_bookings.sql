-- Slots that customers can book.
CREATE TABLE slots (
    id         TEXT PRIMARY KEY,
    starts_at  TIMESTAMPTZ NOT NULL,
    ends_at    TIMESTAMPTZ NOT NULL,
    CONSTRAINT slots_valid_period CHECK (ends_at > starts_at)
);

-- One row per booking. The two UNIQUE constraints are the real guarantee:
-- whatever the application does, PostgreSQL refuses a second booking for the
-- same slot and a second booking for the same idempotency key.
CREATE TABLE bookings (
    id               TEXT PRIMARY KEY,
    slot_id          TEXT NOT NULL REFERENCES slots (id),
    customer_id      TEXT NOT NULL,
    idempotency_key  TEXT NOT NULL,
    created_at       TIMESTAMPTZ NOT NULL,
    CONSTRAINT bookings_slot_unique UNIQUE (slot_id),
    CONSTRAINT bookings_idempotency_key_unique UNIQUE (idempotency_key)
);
