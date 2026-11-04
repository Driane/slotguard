# slotguard

A booking service in Go that guarantees **zero double-booking under heavy concurrent load**.

> **Status: work in progress.** This repository is being built step by step; the roadmap below shows what is done and what comes next.

## Why

Booking systems fail quietly when many requests target the same slot at the same time: two customers end up holding the same appointment, a retried request creates a second booking, a webhook is delivered twice. slotguard is a small, focused service that solves those problems explicitly and measures the result.

## Roadmap

- [x] Project skeleton, health endpoint, CI (format, vet, race-detector tests)
- [ ] Booking domain model, test-first
- [ ] Concurrency-safe in-memory store, proven with a race test
- [ ] HTTP API with idempotency keys
- [ ] PostgreSQL storage with a uniqueness guarantee
- [ ] Redis availability cache and distributed locking
- [ ] Transactional outbox and signed webhooks with retries
- [ ] Load test (k6) with published throughput and latency figures
- [ ] Metrics and tracing

## Run locally

Requirements: Go, Docker.

```bash
docker compose up -d     # PostgreSQL and Redis
go run ./cmd/api         # API on http://localhost:8080
```

```bash
curl http://localhost:8080/health
```

## Tests

```bash
go test -race ./...
```

## Design decisions

Architecture decisions are recorded in [`docs/adr`](docs/adr).
