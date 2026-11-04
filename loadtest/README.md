# Load test

`booking.js` runs two [k6](https://k6.io) scenarios at the same time against the full service (API, PostgreSQL, Redis, webhook delivery):

- **spread**: a steady 2,000 booking attempts per second on random slots among 200,000;
- **rush**: 200 virtual users hammering 10 very popular slots as fast as they can.

In both, 10% of the requests are retried with the same `Idempotency-Key`. Every answer must be `201` (created or replayed) or `409` (slot taken); anything else fails the run. Afterwards, `verify.sql` checks the database itself.

## Run it

```bash
docker compose --profile app up -d --build --wait
docker compose exec -T postgres psql -q -U slotguard -d slotguard < loadtest/seed.sql
docker compose exec -T redis redis-cli FLUSHDB   # the cache must not describe bookings seed.sql deleted
docker compose restart api                       # start the metrics from zero
docker compose --profile loadtest run --rm k6    # DURATION, RATE and VUS can be overridden with -e
docker compose exec -T postgres psql -U slotguard -d slotguard < loadtest/verify.sql
curl -s localhost:8080/metrics | grep ^slotguard_
```

## Results

**Environment:** one machine running everything (k6, API, PostgreSQL, Redis and the webhook receiver, all in Docker Desktop on Windows 11): Intel Core Ultra 7 265KF, 20 cores, 32 GB RAM; Docker VM with 20 CPUs and 16 GB. k6 v2.3.0, 60-second run, 4 November 2026. The load generator competes with the service for the same CPUs, so these figures are a floor, not a ceiling.

| | |
|---|---|
| Requests | 3,765,049 in 60 s, **62,750 requests/s** sustained |
| Unexpected responses | **0** (every answer was 201 or 409) |
| Latency, all requests | p50 2.7 ms · p95 7.4 ms · **p99 11.6 ms** |
| Latency, spread (writes to PostgreSQL) | p50 7.0 ms · p95 15.3 ms · **p99 21.0 ms** |
| Latency, rush (popular slots) | p50 2.7 ms · p95 6.7 ms · **p99 10.0 ms** |
| Bookings created | 89,985 (about 1,500/s) |
| Retries replayed | 8,980, each returning the original booking |
| Answered from the Redis cache | 3,675,033 of 3,765,049 booking attempts (**97.6%**) |
| Webhooks | 89,985 delivered in real time, 0 retried, 0 left pending |

**Database checks (`verify.sql`):**

- 89,985 bookings stored, exactly the number of `201 created` answers counted by k6: no booking lost, none invented;
- **0 slots booked twice**, out of 3.7 million competing attempts;
- each of the 10 popular slots holds exactly one booking;
- 89,985 outbox events, one per booking.

## What the load test found

The first 60-second run delivered only about 1,000 webhooks per second, left 27,484 events pending at the end of the run and logged 1,795 failed deliveries: `connect: cannot assign requested address`. The API container had **run out of ephemeral ports**.

Go's default HTTP transport keeps only **2 idle connections per host**. The dispatcher sends a batch of 20 webhooks concurrently, so 18 connections were closed after every request and new ones opened, each leaving a socket in `TIME_WAIT`, until no local port was left. The fix gives the dispatcher's transport one reusable connection per concurrent delivery (`MaxIdleConnsPerHost = BatchSize`), with a regression test.

| | Before the fix | After |
|---|---|---|
| Throughput | 41,276 requests/s | **62,750 requests/s** |
| p99 latency | 21.5 ms | **11.6 ms** |
| Webhook retries | 1,795 | **0** |
| Events pending at the end | 27,484 | **0** |

The guarantees held in both runs: no double booking, every booking stored with its event, every webhook eventually delivered.
