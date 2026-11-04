// Load test for slotguard. Two scenarios run at the same time:
//
//   spread  a steady flow of customers booking random slots among 200,000,
//           at a fixed arrival rate (RATE requests per second);
//   rush    VUS customers hammering 10 very popular slots as fast as they can.
//
// In both, 10% of the requests are sent twice with the same Idempotency-Key,
// like a client retrying after a timeout. Every answer must be 201 (created
// or replayed) or 409 (slot taken); anything else fails the test.
//
// Prepare the data with seed.sql, then check the database with verify.sql:
// see loadtest/README.md.
import http from 'k6/http';
import { check } from 'k6';
import { Counter } from 'k6/metrics';

const BASE = __ENV.BASE_URL || 'http://localhost:8080';
const DURATION = __ENV.DURATION || '60s';
const SPREAD_SLOTS = 200000; // lt-1 … lt-200000
const HOT_SLOTS = 10; // lt-hot-1 … lt-hot-10
const RETRY_RATE = 0.1;

const created = new Counter('bookings_created');
const replayed = new Counter('bookings_replayed');
const taken = new Counter('bookings_taken');
const unexpected = new Counter('unexpected_responses');

export const options = {
  scenarios: {
    spread: {
      executor: 'constant-arrival-rate',
      exec: 'spread',
      rate: Number(__ENV.RATE || 2000),
      timeUnit: '1s',
      duration: DURATION,
      preAllocatedVUs: 200,
      maxVUs: 2000,
    },
    rush: {
      executor: 'constant-vus',
      exec: 'rush',
      vus: Number(__ENV.VUS || 200),
      duration: DURATION,
    },
  },
  thresholds: {
    unexpected_responses: ['count==0'],
    checks: ['rate==1'],
    // Lenient bounds: their purpose is to print the latency of each scenario
    // separately in the summary.
    'http_req_duration{scenario:spread}': ['p(99)<1000'],
    'http_req_duration{scenario:rush}': ['p(99)<1000'],
  },
  summaryTrendStats: ['avg', 'p(50)', 'p(95)', 'p(99)', 'max'],
};

// 409 slot_taken is a normal answer here, not a failed request.
http.setResponseCallback(http.expectedStatuses(201, 409));

function newKey() {
  return `${__VU}-${__ITER}-${Date.now()}-${Math.random().toString(36).slice(2)}`;
}

function book(slot, customer, key) {
  const res = http.post(`${BASE}/bookings`, JSON.stringify({ slot_id: slot, customer_id: customer }), {
    headers: { 'Content-Type': 'application/json', 'Idempotency-Key': key },
    tags: { name: 'POST /bookings' },
  });

  if (res.status === 201) {
    if (res.headers['Idempotent-Replayed'] === 'true') {
      replayed.add(1);
    } else {
      created.add(1);
    }
  } else if (res.status === 409) {
    taken.add(1);
  } else {
    unexpected.add(1);
  }
  check(res, { 'status is 201 or 409': (r) => r.status === 201 || r.status === 409 });
}

function attempt(slot) {
  const customer = `customer-${__VU}`;
  const key = newKey();
  book(slot, customer, key);
  if (Math.random() < RETRY_RATE) {
    book(slot, customer, key); // a client retrying with the same key
  }
}

export function spread() {
  attempt(`lt-${1 + Math.floor(Math.random() * SPREAD_SLOTS)}`);
}

export function rush() {
  attempt(`lt-hot-${1 + Math.floor(Math.random() * HOT_SLOTS)}`);
}
