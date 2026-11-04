# slotguard

A booking service in Go that guarantees **zero double-booking under heavy concurrent load**.

> **Status: work in progress.** This repository is being built step by step; the roadmap below shows what is done and what comes next.

## Why

Booking systems fail quietly when many requests target the same slot at the same time: two customers end up holding the same appointment, a retried request creates a second booking, a webhook is delivered twice. slotguard is a small, focused service that solves those problems explicitly and measures the result.

## How it guarantees a single booking

- **PostgreSQL enforces the rules.** `UNIQUE (slot_id)` and `UNIQUE (idempotency_key)` reject any second booking, however many requests or service instances race. See [ADR 0002](docs/adr/0002-database-constraints-and-idempotency-keys.md).
- **Every booking request is idempotent.** Clients send an `Idempotency-Key` header; a retry returns the original booking instead of creating a new one.
- **The rules are a test suite.** Every storage implementation runs the same contract tests, including 1,000 goroutines racing for one slot: exactly one wins, every other request gets `slot_taken`.

## Roadmap

- [x] Project skeleton, health endpoint, CI (format, vet, race-detector tests)
- [x] Booking domain model and validation, test-first
- [x] Concurrency-safe in-memory store, proven with race tests
- [x] HTTP API with idempotency keys
- [x] PostgreSQL storage with a uniqueness guarantee and integration tests
- [ ] Redis availability cache and distributed locking
- [ ] Transactional outbox and signed webhooks with retries
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

## Run locally

Requirements: Go, Docker.

```bash
docker compose up -d        # PostgreSQL and Redis
DATABASE_URL='postgres://slotguard:slotguard@localhost:5432/slotguard?sslmode=disable' go run ./cmd/api
```

Without `DATABASE_URL`, the API runs on the in-memory store.

## Tests

```bash
go test ./...
```

The PostgreSQL integration tests run when `SLOTGUARD_TEST_DATABASE_URL` points to a database (the CI provides one):

```bash
SLOTGUARD_TEST_DATABASE_URL='postgres://slotguard:slotguard@localhost:5432/slotguard?sslmode=disable' go test -race ./...
```

## Project layout

```
cmd/api                     entry point: configuration, wiring, graceful shutdown
internal/booking            domain model, rules and the Store interface
internal/booking/bookingtest contract tests every store must pass
internal/storage/memory     in-memory store (reference implementation)
internal/storage/postgres   PostgreSQL store and embedded migrations
internal/httpapi            HTTP handlers and error mapping
docs/adr                    architecture decision records
```

## Design decisions

Architecture decisions are recorded in [`docs/adr`](docs/adr).
