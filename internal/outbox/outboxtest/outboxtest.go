// Package outboxtest provides the test suite every store that writes outbox
// events must pass: events are written atomically with bookings, and workers
// never claim the same event twice.
package outboxtest

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Driane/slotguard/internal/booking"
	"github.com/Driane/slotguard/internal/outbox"
)

// Store is a booking store that also exposes its outbox.
type Store interface {
	booking.Store
	outbox.Store
}

// RunStoreTests runs the outbox contract against stores built by newStore.
// Each store must start with an empty outbox.
func RunStoreTests(t *testing.T, newStore func(t *testing.T) Store) {
	const lease = time.Minute

	t.Run("a new booking writes one event", func(t *testing.T) {
		store := newStore(t)
		b := book(t, store, "alice")

		events := claim(t, store, later(time.Second), 10, lease)
		if len(events) != 1 {
			t.Fatalf("claimed %d events, want 1", len(events))
		}
		e := events[0]
		if e.Type != booking.EventBookingCreated || e.Attempts != 1 {
			t.Fatalf("got type %q and %d attempts, want %q and 1", e.Type, e.Attempts, booking.EventBookingCreated)
		}
		var payload struct {
			ID     string `json:"id"`
			SlotID string `json:"slot_id"`
		}
		if err := json.Unmarshal(e.Payload, &payload); err != nil {
			t.Fatalf("payload %q is not JSON: %v", e.Payload, err)
		}
		if payload.ID != b.ID || payload.SlotID != b.SlotID {
			t.Fatalf("payload %s does not describe booking %s", e.Payload, b.ID)
		}
	})

	t.Run("replays and rejected bookings write no event", func(t *testing.T) {
		store := newStore(t)
		first := book(t, store, "alice")

		retry := newBooking(first.SlotID, "alice")
		retry.IdempotencyKey = first.IdempotencyKey
		if _, err := store.Book(context.Background(), retry); err != nil {
			t.Fatalf("retry: %v", err)
		}
		if _, err := store.Book(context.Background(), newBooking(first.SlotID, "bob")); !errors.Is(err, booking.ErrSlotTaken) {
			t.Fatalf("second customer: err = %v, want %v", err, booking.ErrSlotTaken)
		}

		if events := claim(t, store, later(time.Second), 10, lease); len(events) != 1 {
			t.Fatalf("claimed %d events, want only the first booking's", len(events))
		}
	})

	t.Run("a claimed event is hidden until its lease expires", func(t *testing.T) {
		store := newStore(t)
		book(t, store, "alice")
		now := later(time.Second)

		claim(t, store, now, 10, lease)
		if events := claim(t, store, now.Add(lease/2), 10, lease); len(events) != 0 {
			t.Fatalf("claimed %d events during the lease, want 0", len(events))
		}
		events := claim(t, store, now.Add(lease+time.Second), 10, lease)
		if len(events) != 1 || events[0].Attempts != 2 {
			t.Fatalf("after the lease: got %+v, want the event back with 2 attempts", events)
		}
	})

	t.Run("a delivered event is never claimed again", func(t *testing.T) {
		store := newStore(t)
		book(t, store, "alice")
		now := later(time.Second)

		e := claim(t, store, now, 10, lease)[0]
		if err := store.MarkDelivered(context.Background(), e.ID, now); err != nil {
			t.Fatalf("MarkDelivered: %v", err)
		}
		if events := claim(t, store, now.Add(24*time.Hour), 10, lease); len(events) != 0 {
			t.Fatalf("claimed %d delivered events, want 0", len(events))
		}
	})

	t.Run("a retried event comes back at its next attempt", func(t *testing.T) {
		store := newStore(t)
		book(t, store, "alice")
		now := later(time.Second)

		e := claim(t, store, now, 10, lease)[0]
		next := now.Add(10 * time.Minute)
		if err := store.Retry(context.Background(), e.ID, next, "HTTP 500"); err != nil {
			t.Fatalf("Retry: %v", err)
		}
		if events := claim(t, store, next.Add(-time.Second), 10, lease); len(events) != 0 {
			t.Fatalf("claimed %d events before the next attempt, want 0", len(events))
		}
		if events := claim(t, store, next, 10, lease); len(events) != 1 {
			t.Fatalf("claimed %d events at the next attempt, want 1", len(events))
		}
	})

	t.Run("a buried event is never claimed again", func(t *testing.T) {
		store := newStore(t)
		book(t, store, "alice")
		now := later(time.Second)

		e := claim(t, store, now, 10, lease)[0]
		if err := store.Bury(context.Background(), e.ID, now, "too many failures"); err != nil {
			t.Fatalf("Bury: %v", err)
		}
		if events := claim(t, store, now.Add(24*time.Hour), 10, lease); len(events) != 0 {
			t.Fatalf("claimed %d buried events, want 0", len(events))
		}
	})

	t.Run("claim respects the limit, oldest first", func(t *testing.T) {
		store := newStore(t)
		for range 5 {
			book(t, store, "alice")
		}

		events := claim(t, store, later(time.Second), 3, lease)
		if len(events) != 3 {
			t.Fatalf("claimed %d events, want 3", len(events))
		}
		for i := 1; i < len(events); i++ {
			if events[i].ID <= events[i-1].ID {
				t.Fatalf("events not in creation order: %d before %d", events[i-1].ID, events[i].ID)
			}
		}
	})

	t.Run("unknown event", func(t *testing.T) {
		store := newStore(t)
		if err := store.MarkDelivered(context.Background(), 987654321, time.Now()); !errors.Is(err, outbox.ErrNotFound) {
			t.Fatalf("err = %v, want %v", err, outbox.ErrNotFound)
		}
	})

	t.Run("concurrent workers never claim the same event", func(t *testing.T) {
		store := newStore(t)
		const total, workers = 60, 12
		for range total {
			book(t, store, "alice")
		}

		var (
			wg    sync.WaitGroup
			mu    sync.Mutex
			seen  = map[int64]int{}
			start = make(chan struct{})
			now   = later(time.Second)
		)
		for range workers {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				events, err := store.Claim(context.Background(), now, 10, lease)
				if err != nil {
					t.Errorf("Claim: %v", err)
					return
				}
				mu.Lock()
				defer mu.Unlock()
				for _, e := range events {
					seen[e.ID]++
				}
			}()
		}
		close(start)
		wg.Wait()

		if len(seen) != total {
			t.Fatalf("workers claimed %d distinct events, want %d", len(seen), total)
		}
		for id, n := range seen {
			if n != 1 {
				t.Fatalf("event %d was claimed %d times", id, n)
			}
		}
	})
}

// later returns a moment safely after the bookings created by the test.
func later(d time.Duration) time.Time {
	return time.Now().UTC().Add(d)
}

func claim(t *testing.T, store outbox.Store, now time.Time, limit int, lease time.Duration) []outbox.Event {
	t.Helper()
	events, err := store.Claim(context.Background(), now, limit, lease)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	return events
}

func book(t *testing.T, store booking.Store, customerID string) booking.Booking {
	t.Helper()
	start := time.Now().UTC().Truncate(time.Microsecond).Add(24 * time.Hour)
	slot := booking.Slot{ID: booking.NewID(), StartsAt: start, EndsAt: start.Add(time.Hour)}
	if err := store.CreateSlot(context.Background(), slot); err != nil {
		t.Fatalf("CreateSlot: %v", err)
	}
	res, err := store.Book(context.Background(), newBooking(slot.ID, customerID))
	if err != nil {
		t.Fatalf("Book: %v", err)
	}
	return res.Booking
}

func newBooking(slotID, customerID string) booking.Booking {
	return booking.Booking{
		ID:             booking.NewID(),
		SlotID:         slotID,
		CustomerID:     customerID,
		IdempotencyKey: booking.NewID(),
		CreatedAt:      time.Now().UTC().Truncate(time.Microsecond),
	}
}
