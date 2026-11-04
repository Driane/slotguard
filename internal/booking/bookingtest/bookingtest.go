// Package bookingtest provides the test suite that every booking.Store
// implementation must pass. Running the same tests against the in-memory and
// the PostgreSQL stores proves they honour the same contract.
package bookingtest

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Driane/slotguard/internal/booking"
)

// ConcurrentAttempts is the number of goroutines racing for the same slot in
// the concurrency tests.
const ConcurrentAttempts = 1000

// RunStoreTests runs the store contract against stores built by newStore.
func RunStoreTests(t *testing.T, newStore func(t *testing.T) booking.Store) {
	t.Run("get unknown slot", func(t *testing.T) {
		store := newStore(t)
		_, err := store.GetSlot(context.Background(), booking.NewID())
		if !errors.Is(err, booking.ErrSlotNotFound) {
			t.Fatalf("err = %v, want %v", err, booking.ErrSlotNotFound)
		}
	})

	t.Run("create slot twice", func(t *testing.T) {
		store := newStore(t)
		slot := createSlot(t, store)
		err := store.CreateSlot(context.Background(), slot)
		if !errors.Is(err, booking.ErrSlotExists) {
			t.Fatalf("err = %v, want %v", err, booking.ErrSlotExists)
		}
	})

	t.Run("new slot is available", func(t *testing.T) {
		store := newStore(t)
		slot := createSlot(t, store)

		got, err := store.GetSlot(context.Background(), slot.ID)
		if err != nil {
			t.Fatalf("GetSlot: %v", err)
		}
		if !got.Available || !got.StartsAt.Equal(slot.StartsAt) || !got.EndsAt.Equal(slot.EndsAt) {
			t.Fatalf("got %+v, want %+v available", got, slot)
		}
	})

	t.Run("book a free slot", func(t *testing.T) {
		store := newStore(t)
		slot := createSlot(t, store)
		b := newBooking(slot.ID, "alice")

		res, err := store.Book(context.Background(), b)
		if err != nil {
			t.Fatalf("Book: %v", err)
		}
		if res.Replayed || res.Booking.ID != b.ID {
			t.Fatalf("got %+v, want the new booking %s", res, b.ID)
		}

		got, err := store.GetSlot(context.Background(), slot.ID)
		if err != nil {
			t.Fatalf("GetSlot: %v", err)
		}
		if got.Available {
			t.Fatal("slot is still available after being booked")
		}
	})

	t.Run("book an unknown slot", func(t *testing.T) {
		store := newStore(t)
		_, err := store.Book(context.Background(), newBooking(booking.NewID(), "alice"))
		if !errors.Is(err, booking.ErrSlotNotFound) {
			t.Fatalf("err = %v, want %v", err, booking.ErrSlotNotFound)
		}
	})

	t.Run("book a taken slot", func(t *testing.T) {
		store := newStore(t)
		slot := createSlot(t, store)
		mustBook(t, store, newBooking(slot.ID, "alice"))

		_, err := store.Book(context.Background(), newBooking(slot.ID, "bob"))
		if !errors.Is(err, booking.ErrSlotTaken) {
			t.Fatalf("err = %v, want %v", err, booking.ErrSlotTaken)
		}
	})

	t.Run("retry with the same idempotency key", func(t *testing.T) {
		store := newStore(t)
		slot := createSlot(t, store)
		first := mustBook(t, store, newBooking(slot.ID, "alice"))

		retry := newBooking(slot.ID, "alice")
		retry.IdempotencyKey = first.IdempotencyKey
		res, err := store.Book(context.Background(), retry)
		if err != nil {
			t.Fatalf("Book: %v", err)
		}
		if !res.Replayed || res.Booking.ID != first.ID {
			t.Fatalf("got %+v, want booking %s replayed", res, first.ID)
		}
	})

	t.Run("reuse an idempotency key for another request", func(t *testing.T) {
		store := newStore(t)
		first := mustBook(t, store, newBooking(createSlot(t, store).ID, "alice"))

		other := newBooking(createSlot(t, store).ID, "alice")
		other.IdempotencyKey = first.IdempotencyKey
		_, err := store.Book(context.Background(), other)
		if !errors.Is(err, booking.ErrIdempotencyConflict) {
			t.Fatalf("err = %v, want %v", err, booking.ErrIdempotencyConflict)
		}
	})

	t.Run("concurrent customers race for one slot", func(t *testing.T) {
		store := newStore(t)
		slot := createSlot(t, store)

		results := race(t, func(i int) booking.Booking {
			return newBooking(slot.ID, fmt.Sprintf("customer-%d", i))
		}, store)

		if results.created != 1 || results.taken != ConcurrentAttempts-1 {
			t.Fatalf("created %d and rejected %d bookings, want exactly 1 and %d",
				results.created, results.taken, ConcurrentAttempts-1)
		}
	})

	t.Run("concurrent retries of one request", func(t *testing.T) {
		store := newStore(t)
		slot := createSlot(t, store)
		key := booking.NewID()

		results := race(t, func(int) booking.Booking {
			b := newBooking(slot.ID, "alice")
			b.IdempotencyKey = key
			return b
		}, store)

		if results.created != 1 || results.replayed != ConcurrentAttempts-1 {
			t.Fatalf("created %d and replayed %d bookings, want exactly 1 and %d",
				results.created, results.replayed, ConcurrentAttempts-1)
		}
		if len(results.bookingIDs) != 1 {
			t.Fatalf("retries returned %d different bookings, want 1", len(results.bookingIDs))
		}
	})
}

type raceResults struct {
	created, replayed, taken int
	bookingIDs               map[string]bool
}

// race starts ConcurrentAttempts goroutines that all call Book at the same
// moment, then counts the outcomes. Any unexpected error fails the test.
func race(t *testing.T, attempt func(i int) booking.Booking, store booking.Store) raceResults {
	t.Helper()

	var (
		wg       sync.WaitGroup
		created  atomic.Int64
		replayed atomic.Int64
		taken    atomic.Int64
		mu       sync.Mutex
		ids      = map[string]bool{}
		start    = make(chan struct{})
		errs     = make(chan error, ConcurrentAttempts)
	)

	for i := range ConcurrentAttempts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			b := attempt(i)
			<-start // every goroutine waits here, then they all go at once

			res, err := store.Book(context.Background(), b)
			switch {
			case errors.Is(err, booking.ErrSlotTaken):
				taken.Add(1)
				return
			case err != nil:
				errs <- err
				return
			case res.Replayed:
				replayed.Add(1)
			default:
				created.Add(1)
			}
			mu.Lock()
			ids[res.Booking.ID] = true
			mu.Unlock()
		}()
	}

	close(start)
	wg.Wait()
	close(errs)

	for err := range errs {
		t.Errorf("unexpected error: %v", err)
	}
	return raceResults{
		created:    int(created.Load()),
		replayed:   int(replayed.Load()),
		taken:      int(taken.Load()),
		bookingIDs: ids,
	}
}

func createSlot(t *testing.T, store booking.Store) booking.Slot {
	t.Helper()
	start := time.Now().UTC().Truncate(time.Microsecond).Add(24 * time.Hour)
	slot := booking.Slot{ID: booking.NewID(), StartsAt: start, EndsAt: start.Add(time.Hour)}
	if err := store.CreateSlot(context.Background(), slot); err != nil {
		t.Fatalf("CreateSlot: %v", err)
	}
	return slot
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

func mustBook(t *testing.T, store booking.Store, b booking.Booking) booking.Booking {
	t.Helper()
	res, err := store.Book(context.Background(), b)
	if err != nil {
		t.Fatalf("Book: %v", err)
	}
	return res.Booking
}
