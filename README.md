# slotguard

A booking service in Go that guarantees **zero double-booking under heavy concurrent load**, and tells other systems about every booking through signed webhooks.

> **Status: work in progress.** This repository is being built step by step; the roadmap below shows what is done and what comes next.

## Why

Booking systems fail quietly when many requests target the same slot at the same time: two customers end up holding the same appointment, a retried request creates a second booking, a webhook is lost or delivered twice. slotguard is a small, focused service that solves those problems explicitly and measures the result.

## How it works

- **PostgreSQL enforces the rules.** `UNIQUE (slot_id)` and `UNIQUE (idempotency_key)` reject any second booking, however many requests or service instances race. See [ADR 0002](docs/adr/0002-database-constraints-and-idempotency-keys.md).
- **Every booking request is idempotent.** Clients send an `Idempotency-Key` header; a retry returns the original booking instead of creating a new one.
- **Redis absorbs the load on popular slots.** It caches facts that never change once true (who holds a slot, which booking a key produced), so competing requests and retries are answered without touching PostgreSQL. There is deliberately no distributed lock. See [ADR 0003](docs/adr/0003-redis-caches-immutable-facts-no-distributed-lock.md).
- **No event is ever lost.** Each booking and its `booking.created` event are written in the same transaction (transactional outbox). Background workers deliver the events as signed webhooks, with exponential backoff and a dead letter after 10 attempts. See [ADR 0004](docs/adr/0004-transactional-outbox-and-signed-webhooks.md).
- **The rules are test suites.** Every store implementation runs the same contract tests, including 1,000 goroutines racing for one slot and 1,000 concurrent retries of one request, against PostgreSQL and real Redis in CI.

## Roadmap

- [x] Project skeleton, health endpoint, CI (format, vet, race-detector tests)
- [x] Booking domain model and validation, test-first
- [x] Concurrency-safe in-memory store, proven with race tests
- [x] HTTP API with idempotency keys
- [x] PostgreSQL storage with a uniqueness guarantee and integration tests
- [x] Redis cache for slot holders and idempotency keys (no distributed lock, by design)
- [x] Transactional outbox and signed webhooks with retries and a dead letter
- [ ] Load test (k6) with published throughput and latency figures
- [ ] Metrics and tracing

## API

| Method | Path | Description |
|---|---|---|
| `GET` | `/health` | Liveness check |
| `POST` | `/slots` | Create a slot: `{"id", "starts_at", "ends_at"}` (`id` optional) |
| `GET` | `/slots/{id}` | Read a slot and whether it is still available |
| `POST` | `/bookings` | Book a slot: `{"slot_id", "customer_id"}` + `Idempotency-Key` header |

Errors use stable codes: `invalid_request` (400), `slot_not_found` (404), `slot_exists` and `slot_taken` (409), `idempotency_conflict` (422).

```bash
curl -X POST localhost:8080/slots -d '{"id":"slot-1","starts_at":"2026-12-01T10:00:00Z","ends_at":"2026-12-01T11:00:00Z"}'
curl -X POST localhost:8080/bookings -H 'Idempotency-Key: 7f9c2b' -d '{"slot_id":"slot-1","customer_id":"alice"}'
curl localhost:8080/slots/slot-1
```

## Webhooks

Every new booking is POSTed to `WEBHOOK_URL` as:

```json
{"id": 42, "type": "booking.created", "created_at": "...", "data": {"id": "...", "slot_id": "...", "customer_id": "...", "created_at": "..."}}
```

with two headers:

- `Slotguard-Event-Id`: delivery is at least once, so deduplicate on it;
- `Slotguard-Signature: t=<unix>,v1=<hex HMAC-SHA256 of "<t>.<body>">`: verify it with the shared secret and reject old timestamps. `webhook.Verify` does both; [`cmd/webhook-receiver`](cmd/webhook-receiver/main.go) is a complete example.

## Configuration

| Variable | Effect |
|---|---|
| `PORT` | HTTP port (default `8080`) |
| `DATABASE_URL` | PostgreSQL; without it, data is kept in memory |
| `REDIS_URL` | Redis cache in front of the store |
| `WEBHOOK_URL` | Endpoint receiving booking events |
| `WEBHOOK_SECRET` | Key used to sign webhooks (required with `WEBHOOK_URL`) |

## Run locally

Requirements: Go, Docker.

```bash
docker compose up -d   # PostgreSQL and Redis

# Terminal 1: a webhook receiver that verifies signatures and logs events
WEBHOOK_SECRET=dev-secret go run ./cmd/webhook-receiver

# Terminal 2: the API with every feature enabled
DATABASE_URL='postgres://slotguard:slotguard@localhost:5432/slotguard?sslmode=disable' \
REDIS_URL='redis://localhost:6379/0' \
WEBHOOK_URL='http://localhost:9090/webhooks' WEBHOOK_SECRET=dev-secret \
go run ./cmd/api
```

## Tests

```bash
go test ./...
```

The integration tests run when they find a database and a Redis (the CI provides both):

```bash
SLOTGUARD_TEST_DATABASE_URL='postgres://slotguard:slotguard@localhost:5432/slotguard?sslmode=disable' \
SLOTGUARD_TEST_REDIS_URL='redis://localhost:6379/0' \
go test -race ./...
```

## Project layout

```
cmd/api                       entry point: configuration, wiring, graceful shutdown
cmd/webhook-receiver          example receiver: signature check and deduplication
internal/booking              domain model, rules and the Store interface
internal/booking/bookingtest  contract tests every store must pass
internal/outbox               outbox events and the Store workers claim them from
internal/outbox/outboxtest    contract tests for outbox stores
internal/storage/memory       in-memory store (reference implementation)
internal/storage/postgres     PostgreSQL store, outbox and embedded migrations
internal/storage/cache        Redis cache in front of any store
internal/webhook              signing, verification and the delivery dispatcher
internal/httpapi              HTTP handlers and error mapping
docs/adr                      architecture decision records
```

## Design decisions

Architecture decisions are recorded in [`docs/adr`](docs/adr).
