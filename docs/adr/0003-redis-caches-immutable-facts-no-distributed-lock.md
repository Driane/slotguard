# 3. Redis caches immutable facts; no distributed lock

- **Status:** Accepted
- **Date:** 2026-11-04

## Context

When a slot is popular, thousands of requests arrive after it is already booked. Each one costs PostgreSQL an idempotency lookup and a failed `INSERT`. We want to absorb that load.

Two common answers are a **distributed lock** in Redis (only the lock holder may try to book) and a **cache** of what is already known.

## Decision

1. **No distributed lock.** Correctness already comes from the PostgreSQL constraints (ADR 0002). A Redis lock would add failure modes (expiry during a slow request, a Redis failover losing the lock) without making anything more correct. The database stays the only judge.
2. **Cache only facts that never change once true:**
   - `slotguard:booking-by-key:<key>`: the booking produced by an idempotency key;
   - `slotguard:slot-holder:<slot>`: the booking that holds a slot.

   A stale entry cannot exist, because these facts never become false (bookings cannot be cancelled yet). A missing entry only sends the request to PostgreSQL.
3. **The slot entry holds the winning booking itself, not a "taken" flag.** Writing the two entries is not atomic: a retry of the winning request can see the slot entry before the key entry. With a flag it would wrongly get `slot_taken`. With the booking, it recognises its own idempotency key and gets its booking back. The real-Redis contract test (1,000 concurrent retries) found this race; it is fixed by design, not by timing.
4. **The cache is best effort.** Any Redis error is logged and the request goes to the store: an outage slows the service down but never breaks it. The contract tests run against a working Redis, a fake one and one that always fails.
5. **The cache is a decorator.** `cache.Store` implements `booking.Store` around another store and passes the same contract tests.

## Consequences

- Competing requests on a booked slot, and retries, are answered without touching PostgreSQL once the winner is cached.
- When cancellation is added, it must delete both cache entries in the same operation as the database change, or this decision must be revisited.
- In one rare case the error code differs from the store: a client reusing an idempotency key whose cache entry has expired, on a slot that is already taken, gets `slot_taken` instead of `idempotency_conflict`. Both reject the request; no wrong booking is possible.
- Entries expire after 24 hours, which bounds Redis memory.
