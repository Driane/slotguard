package outbox_test

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/Driane/slotguard/internal/booking"
	"github.com/Driane/slotguard/internal/outbox"
	"github.com/Driane/slotguard/internal/storage/memory"
)

// spyStore reports every Purge result on a channel. Embedding outbox.Store
// gives it all the other methods unchanged.
type spyStore struct {
	outbox.Store
	purged chan int64
}

func (s *spyStore) Purge(ctx context.Context, deliveredBefore time.Time) (int64, error) {
	n, err := s.Store.Purge(ctx, deliveredBefore)
	s.purged <- n
	return n, err
}

func TestRunPurgerDeletesOldDeliveredEventsAndStops(t *testing.T) {
	store := memory.New()
	svc := booking.NewService(store)
	ctx := context.Background()
	start := time.Now().Add(24 * time.Hour)
	slot, err := svc.CreateSlot(ctx, booking.Slot{StartsAt: start, EndsAt: start.Add(time.Hour)})
	if err != nil {
		t.Fatalf("CreateSlot: %v", err)
	}
	if _, err := svc.Book(ctx, booking.BookingRequest{SlotID: slot.ID, CustomerID: "alice", IdempotencyKey: "k"}); err != nil {
		t.Fatalf("Book: %v", err)
	}
	events, err := store.Claim(ctx, time.Now().Add(time.Second), 10, time.Minute)
	if err != nil || len(events) != 1 {
		t.Fatalf("Claim: %v, %d events", err, len(events))
	}
	// Delivered two hours ago, with a one-hour retention: it must go.
	if err := store.MarkDelivered(ctx, events[0].ID, time.Now().Add(-2*time.Hour)); err != nil {
		t.Fatalf("MarkDelivered: %v", err)
	}

	spy := &spyStore{Store: store, purged: make(chan int64, 10)}
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		outbox.RunPurger(runCtx, spy, time.Hour, time.Hour, slog.New(slog.NewTextHandler(io.Discard, nil)))
		close(done)
	}()

	// The purger runs once at start.
	select {
	case n := <-spy.purged:
		if n != 1 {
			t.Fatalf("first purge deleted %d events, want 1", n)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the purger did not run within 5s")
	}

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("RunPurger did not return after its context was cancelled")
	}
}
