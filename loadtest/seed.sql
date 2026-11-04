-- Prepares a load test run: removes the previous run's data, then creates
-- 200,000 ordinary slots (lt-1 … lt-200000) and 10 popular ones (lt-hot-1 …).
-- Flush Redis too (see README.md): its cached facts would otherwise describe
-- bookings deleted here.
BEGIN;

DELETE FROM outbox_events WHERE payload->>'slot_id' LIKE 'lt-%';
DELETE FROM bookings WHERE slot_id LIKE 'lt-%';
DELETE FROM slots WHERE id LIKE 'lt-%';

INSERT INTO slots (id, starts_at, ends_at)
SELECT 'lt-' || g,
       now() + interval '1 day' + g * interval '1 minute',
       now() + interval '1 day' + g * interval '1 minute' + interval '30 minutes'
  FROM generate_series(1, 200000) AS g;

INSERT INTO slots (id, starts_at, ends_at)
SELECT 'lt-hot-' || g, now() + interval '2 days', now() + interval '2 days 1 hour'
  FROM generate_series(1, 10) AS g;

COMMIT;
