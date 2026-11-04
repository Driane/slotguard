# 2. Database constraints and idempotency keys guarantee single bookings

- **Status:** Accepted
- **Date:** 2026-11-04

## Context

A slot must never be booked twice, even when hundreds of requests target it at the same instant. Clients also retry: after a timeout, a client cannot know whether its booking was created, so it sends the request again.

The naive approach (read whether the slot is free, then insert) leaves a window between the read and the write in which another request can book the same slot. Closing that window in application code alone requires locks that only work inside one process, and breaks as soon as two instances of the service run side by side.

## Decision

1. **PostgreSQL enforces the rules.** The `bookings` table has `UNIQUE (slot_id)` and `UNIQUE (idempotency_key)`. The store simply attempts the `INSERT`; the database rejects a second booking, whatever the number of service instances.
2. **Every booking request carries an `Idempotency-Key` header.** The key is stored with the booking:
   - the same key with the same request returns the original booking, flagged with `Idempotent-Replayed: true`, and creates nothing;
   - the same key with a different request is rejected with `422 idempotency_conflict`.
3. **When an INSERT hits a unique violation, the key is checked first.** Two concurrent requests with the same key may violate either constraint, and PostgreSQL does not say which one it checks first. Looking up the key before answering `slot_taken` returns the correct result in both cases.
4. **One contract, several stores.** The rules are written once as a test suite (`internal/booking/bookingtest`) that every store implementation must pass, including two races of 1,000 concurrent goroutines.

## Consequences

- Correctness does not depend on a single process: it holds with several API instances behind a load balancer.
- A replayed request answers `201 Created` like the original, so a retrying client needs no special handling; the header tells it what happened.
- Each booking costs one extra `SELECT` (the idempotency lookup) before the `INSERT`. This is measured in the load tests planned for week 3, and a Redis cache can absorb it if needed.
- Idempotency keys are kept forever for now. Expiring them (for example after 24 hours) is a later decision.
