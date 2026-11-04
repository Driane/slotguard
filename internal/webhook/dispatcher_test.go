package webhook

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Driane/slotguard/internal/booking"
	"github.com/Driane/slotguard/internal/outbox"
	"github.com/Driane/slotguard/internal/storage/memory"
)

var testSecret = []byte("test-secret")

// clock is a settable time source, safe to read from several goroutines.
type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// receiver is a fake webhook endpoint. It verifies every signature, counts
// deliveries per event and answers with the status chosen by respond.
type receiver struct {
	t          *testing.T
	clock      *clock
	respond    func(call int) int
	calls      atomic.Int64
	mu         sync.Mutex
	deliveries map[int64]int
	envelopes  []Envelope
}

func (r *receiver) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	body, _ := io.ReadAll(req.Body)
	if err := Verify(testSecret, req.Header.Get(SignatureHeader), body, r.clock.Now(), 5*time.Minute); err != nil {
		r.t.Errorf("invalid signature: %v", err)
	}
	var env Envelope
	if err := json.Unmarshal(body, &env); err != nil {
		r.t.Errorf("invalid body %q: %v", body, err)
	}
	if got := req.Header.Get(EventIDHeader); got != strconv.FormatInt(env.ID, 10) {
		r.t.Errorf("event id header %q does not match body id %d", got, env.ID)
	}

	call := int(r.calls.Add(1))
	status := r.respond(call)
	if status >= 200 && status < 300 {
		r.mu.Lock()
		r.deliveries[env.ID]++
		r.envelopes = append(r.envelopes, env)
		r.mu.Unlock()
	}
	w.WriteHeader(status)
}

// received returns the successfully delivered envelopes and how many times
// each event was delivered. It locks, because the server writes from its own
// goroutines.
func (r *receiver) received() ([]Envelope, map[int64]int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	counts := make(map[int64]int, len(r.deliveries))
	for id, n := range r.deliveries {
		counts[id] = n
	}
	return append([]Envelope(nil), r.envelopes...), counts
}

// setup returns a store holding `events` booking.created events, a receiver
// answering with respond, and a dispatcher pointing at it.
func setup(t *testing.T, events int, respond func(call int) int, cfg Config) (*memory.Store, *receiver, *Dispatcher, *clock) {
	t.Helper()
	store := memory.New()
	for range events {
		createBooking(t, store)
	}
	return setupWithStore(t, store, respond, cfg)
}

// setupWithStore is setup for a store the test has already filled.
func setupWithStore(t *testing.T, store *memory.Store, respond func(call int) int, cfg Config) (*memory.Store, *receiver, *Dispatcher, *clock) {
	t.Helper()
	clk := &clock{now: time.Now().UTC().Add(time.Minute)} // after the bookings
	rcv := &receiver{t: t, clock: clk, respond: respond, deliveries: map[int64]int{}}
	server := httptest.NewServer(rcv)
	t.Cleanup(server.Close)

	cfg.URL = server.URL
	cfg.Secret = testSecret
	d, err := NewDispatcher(store, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("NewDispatcher: %v", err)
	}
	d.now = clk.Now
	d.backoff = func(int) time.Duration { return 10 * time.Second } // predictable
	return store, rcv, d, clk
}

func createBooking(t *testing.T, store booking.Store) booking.Booking {
	t.Helper()
	svc := booking.NewService(store)
	start := time.Now().Add(24 * time.Hour)
	slot, err := svc.CreateSlot(context.Background(), booking.Slot{StartsAt: start, EndsAt: start.Add(time.Hour)})
	if err != nil {
		t.Fatalf("CreateSlot: %v", err)
	}
	res, err := svc.Book(context.Background(), booking.BookingRequest{SlotID: slot.ID, CustomerID: "alice", IdempotencyKey: booking.NewID()})
	if err != nil {
		t.Fatalf("Book: %v", err)
	}
	return res.Booking
}

func runOnce(t *testing.T, d *Dispatcher) int {
	t.Helper()
	n, err := d.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	return n
}

func pending(t *testing.T, store outbox.Store, at time.Time) int {
	t.Helper()
	events, err := store.Claim(context.Background(), at, 1000, time.Second)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	return len(events)
}

func TestDeliversASignedEvent(t *testing.T) {
	store := memory.New()
	b := createBooking(t, store)
	_, rcv, d, clk := setupWithStore(t, store, func(int) int { return http.StatusNoContent }, Config{})

	if n := runOnce(t, d); n != 1 {
		t.Fatalf("claimed %d events, want 1", n)
	}
	envelopes, _ := rcv.received()
	if len(envelopes) != 1 {
		t.Fatalf("receiver got %d events, want 1", len(envelopes))
	}
	env := envelopes[0]
	var data struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(env.Data, &data); err != nil || env.Type != booking.EventBookingCreated || data.ID != b.ID {
		t.Fatalf("got envelope %+v (data %s), want booking.created for %s", env, env.Data, b.ID)
	}
	if n := pending(t, store, clk.Now().Add(24*time.Hour)); n != 0 {
		t.Fatalf("%d events still pending after delivery, want 0", n)
	}
}

func TestFailedDeliveryIsRetriedAfterBackoff(t *testing.T) {
	// The endpoint fails once, then accepts.
	respond := func(call int) int {
		if call == 1 {
			return http.StatusInternalServerError
		}
		return http.StatusOK
	}
	_, rcv, d, clk := setup(t, 1, respond, Config{})

	runOnce(t, d) // fails: retry planned 10s later

	clk.Advance(5 * time.Second)
	if n := runOnce(t, d); n != 0 {
		t.Fatalf("claimed %d events before the backoff elapsed, want 0", n)
	}

	clk.Advance(6 * time.Second)
	if n := runOnce(t, d); n != 1 {
		t.Fatalf("claimed %d events after the backoff, want 1", n)
	}
	if got := rcv.calls.Load(); got != 2 {
		t.Fatalf("endpoint called %d times, want 2", got)
	}
	if envelopes, _ := rcv.received(); len(envelopes) != 1 {
		t.Fatalf("%d successful deliveries, want 1", len(envelopes))
	}
}

func TestEventIsBuriedAfterMaxAttempts(t *testing.T) {
	store, rcv, d, clk := setup(t, 1, func(int) int { return http.StatusBadGateway }, Config{MaxAttempts: 3})

	for range 5 {
		runOnce(t, d)
		clk.Advance(time.Hour) // well past any backoff or lease
	}

	if got := rcv.calls.Load(); got != 3 {
		t.Fatalf("endpoint called %d times, want exactly MaxAttempts = 3", got)
	}
	if got, want := d.Stats(), (Stats{Retried: 2, Buried: 1}); got != want {
		t.Fatalf("Stats() = %+v, want %+v", got, want)
	}
	if n := pending(t, store, clk.Now()); n != 0 {
		t.Fatalf("%d events still pending, want the event buried", n)
	}
}

func TestConcurrentDispatchersDeliverEachEventOnce(t *testing.T) {
	const events, dispatchers = 40, 4
	store, rcv, first, clk := setup(t, events, func(int) int { return http.StatusOK }, Config{BatchSize: 5})

	// More dispatchers sharing the same store and endpoint, like several
	// instances of the service.
	all := []*Dispatcher{first}
	for range dispatchers - 1 {
		d, err := NewDispatcher(store, first.cfg, first.logger)
		if err != nil {
			t.Fatalf("NewDispatcher: %v", err)
		}
		d.now = clk.Now
		all = append(all, d)
	}

	var wg sync.WaitGroup
	for _, d := range all {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for runOnce(t, d) > 0 {
			}
		}()
	}
	wg.Wait()

	_, deliveries := rcv.received()
	if len(deliveries) != events {
		t.Fatalf("%d distinct events delivered, want %d", len(deliveries), events)
	}
	for id, n := range deliveries {
		if n != 1 {
			t.Fatalf("event %d delivered %d times, want once", id, n)
		}
	}
}

func TestRunStopsWithItsContext(t *testing.T) {
	_, rcv, d, _ := setup(t, 3, func(int) int { return http.StatusOK }, Config{PollInterval: 10 * time.Millisecond})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		d.Run(ctx)
		close(done)
	}()

	deadline := time.After(5 * time.Second)
	for rcv.calls.Load() < 3 {
		select {
		case <-deadline:
			t.Fatal("events not delivered within 5s")
		case <-time.After(10 * time.Millisecond):
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after its context was cancelled")
	}
}

func TestNewDispatcherValidatesConfig(t *testing.T) {
	store := memory.New()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	tests := []struct {
		name string
		cfg  Config
	}{
		{name: "missing URL", cfg: Config{Secret: testSecret}},
		{name: "missing secret", cfg: Config{URL: "http://localhost"}},
		{name: "lease shorter than timeout", cfg: Config{URL: "http://localhost", Secret: testSecret, Timeout: time.Minute, Lease: 30 * time.Second}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := NewDispatcher(store, tt.cfg, logger); err == nil {
				t.Fatal("NewDispatcher accepted an invalid config")
			}
		})
	}
}

// Regression test: with Go's default of 2 idle connections per host, the load
// test exhausted the ephemeral ports. Each concurrent delivery must be able to
// reuse its connection.
func TestDispatcherReusesOneConnectionPerConcurrentDelivery(t *testing.T) {
	d, err := NewDispatcher(memory.New(), Config{URL: "http://localhost", Secret: testSecret, BatchSize: 50}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("NewDispatcher: %v", err)
	}
	transport, ok := d.client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("client transport is %T, want *http.Transport", d.client.Transport)
	}
	if transport.MaxIdleConnsPerHost < 50 {
		t.Fatalf("MaxIdleConnsPerHost = %d, want at least the batch size (50)", transport.MaxIdleConnsPerHost)
	}
}

func TestBackoff(t *testing.T) {
	tests := []struct {
		attempt  int
		min, max time.Duration
	}{
		{attempt: 1, min: time.Second, max: 2 * time.Second},
		{attempt: 2, min: 2 * time.Second, max: 4 * time.Second},
		{attempt: 5, min: 16 * time.Second, max: 32 * time.Second},
		{attempt: 50, min: 30 * time.Minute, max: time.Hour},
	}
	for _, tt := range tests {
		for range 100 { // the delay is partly random: check it many times
			if got := Backoff(tt.attempt); got < tt.min || got > tt.max {
				t.Fatalf("Backoff(%d) = %s, want between %s and %s", tt.attempt, got, tt.min, tt.max)
			}
		}
	}
}
