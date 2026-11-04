-- Checks the database after a load test run.

-- 1. Bookings stored: must equal the bookings_created counter printed by k6.
SELECT count(*) AS bookings_stored FROM bookings WHERE slot_id LIKE 'lt-%';

-- 2. Slots booked more than once: must return no row.
SELECT slot_id, count(*) AS bookings
  FROM bookings
 WHERE slot_id LIKE 'lt-%'
 GROUP BY slot_id
HAVING count(*) > 1;

-- 3. The 10 popular slots: each must have exactly one booking.
SELECT count(*) AS hot_slots_booked FROM bookings WHERE slot_id LIKE 'lt-hot-%';

-- 4. Every booking has its outbox event: both numbers must match.
SELECT (SELECT count(*) FROM bookings WHERE slot_id LIKE 'lt-%') AS bookings,
       (SELECT count(*) FROM outbox_events WHERE payload->>'slot_id' LIKE 'lt-%') AS events;
