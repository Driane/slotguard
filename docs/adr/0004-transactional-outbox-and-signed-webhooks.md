# 4. Transactional outbox and signed webhooks, delivered at least once

- **Status:** Accepted
- **Date:** 2026-11-04

## Context

Other systems want to know when a booking is created. Calling them directly from the booking request has two failure modes: the booking is saved but the call fails (the event is lost), or the call succeeds but the booking is rolled back (the event describes something that does not exist). Receivers also need to trust that a request really comes from slotguard.

## Decision

1. **Transactional outbox.** The booking and a `booking.created` row in `outbox_events` are written in the same PostgreSQL transaction (the same lock in memory). Either both exist or neither does.
2. **Background delivery.** A dispatcher claims due events with `FOR UPDATE SKIP LOCKED` and a lease: several workers, in several processes, never claim the same event, and an event claimed by a worker that crashes reappears when its lease expires.
3. **Retries with backoff, then a dead letter.** A failed delivery is retried after an exponential delay (about 2s, 4s, 8s…, capped at one hour) with jitter, so that events failing together do not retry together. After 10 attempts the event is buried: kept for inspection, never claimed again.
4. **At-least-once delivery.** If the process stops between a successful delivery and its recording, the event is sent again. Every request carries `Slotguard-Event-Id`, and receivers deduplicate on it (see `cmd/webhook-receiver`). Exactly-once delivery over HTTP is not achievable; at-least-once with deduplication is the standard answer.
5. **Signed requests.** `Slotguard-Signature: t=<unix>,v1=<hex HMAC-SHA256 of "<t>.<body>">`, the scheme used by Stripe. Receivers verify the HMAC in constant time and reject timestamps older than five minutes, which stops replays of captured requests.

## Consequences

- No booking is ever lost to the outside world, and no event describes a booking that does not exist.
- Delivery is delayed by up to the poll interval (one second by default).
- The `outbox_events` table grows; delivered events should be purged periodically. This is left for later.
- The idempotency key is not part of the event payload: it is a client secret, not something to broadcast.
