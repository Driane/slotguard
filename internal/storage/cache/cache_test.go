package cache_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/Driane/slotguard/internal/booking"
	"github.com/Driane/slotguard/internal/booking/bookingtest"
	"github.com/Driane/slotguard/internal/storage/cache"
	"github.com/Driane/slotguard/internal/storage/memory"
)

var discard = slog.New(slog.NewTextHandler(io.Discard, nil))

// mapBackend is an in-memory Backend for tests. It ignores expiry.
type mapBackend struct {
	mu     sync.Mutex
	values map[string]string
}

func newMapBackend() *mapBackend { return &mapBackend{values: map[string]string{}} }

func (m *mapBackend) Get(_ context.Context, key string) (string, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.values[key]
	return v, ok, nil
}

func (m *mapBackend) Set(_ context.Context, key, value string, _ time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.values[key] = value
	return nil
}

// brokenBackend fails every call, like a Redis that is down.
type brokenBackend struct{}

func (brokenBackend) Get(context.Context, string) (string, bool, error) {
	return "", false, errors.New("connection refused")
}

func (brokenBackend) Set(context.Context, string, string, time.Duration) error {
	return errors.New("connection refused")
}

// countingStore counts the Book calls that reach the inner store.
type countingStore struct {
	booking.Store
	books atomic.Int64
}

func (c *countingStore) Book(ctx context.Context, b booking.Booking) (booking.BookingResult, error) {
	c.books.Add(1)
	return c.Store.Book(ctx, b)
}

// The cached store must honour exactly the same contract as the store it wraps.
func TestContract(t *testing.T) {
	bookingtest.RunStoreTests(t, func(*testing.T) booking.Store {
		return cache.New(memory.New(), newMapBackend(), time.Hour, discard)
	})
}

func TestContractWithBrokenCache(t *testing.T) {
	bookingtest.RunStoreTests(t, func(*testing.T) booking.Store {
		return cache.New(memory.New(), brokenBackend{}, time.Hour, discard)
	})
}

func TestContractWithRedis(t *testing.T) {
	url := os.Getenv("SLOTGUARD_TEST_REDIS_URL")
	if url == "" {
		t.Skip("SLOTGUARD_TEST_REDIS_URL is not set: skipping Redis integration tests")
	}
	opts, err := redis.ParseURL(url)
	if err != nil {
		t.Fatalf("parse Redis URL: %v", err)
	}
	client := redis.NewClient(opts)
	t.Cleanup(func() { client.Close() })

	// Keys are random per test, so the tests can share one Redis.
	bookingtest.RunStoreTests(t, func(*testing.T) booking.Store {
		return cache.New(memory.New(), cache.NewRedis(client), time.Minute, discard)
	})
}

func TestCacheAnswersWithoutTheStore(t *testing.T) {
	inner := &countingStore{Store: memory.New()}
	store := cache.New(inner, newMapBackend(), time.Hour, discard)
	svc := booking.NewService(store)
	ctx := context.Background()

	start := time.Now().Add(24 * time.Hour)
	slot, err := svc.CreateSlot(ctx, booking.Slot{StartsAt: start, EndsAt: start.Add(time.Hour)})
	if err != nil {
		t.Fatalf("CreateSlot: %v", err)
	}
	alice := booking.BookingRequest{SlotID: slot.ID, CustomerID: "alice", IdempotencyKey: "key-alice"}
	first, err := svc.Book(ctx, alice)
	if err != nil {
		t.Fatalf("Book: %v", err)
	}

	// A retry and a hundred competitors on the booked slot...
	retry, err := svc.Book(ctx, alice)
	if err != nil || !retry.Replayed || retry.Booking.ID != first.Booking.ID {
		t.Fatalf("retry: got %+v, %v; want booking %s replayed", retry, err, first.Booking.ID)
	}
	for i := range 100 {
		_, err := svc.Book(ctx, booking.BookingRequest{SlotID: slot.ID, CustomerID: "bob", IdempotencyKey: booking.NewID()})
		if !errors.Is(err, booking.ErrSlotTaken) {
			t.Fatalf("competitor %d: err = %v, want %v", i, err, booking.ErrSlotTaken)
		}
	}

	// ...never reached the store: only the first booking did.
	if got := inner.books.Load(); got != 1 {
		t.Fatalf("the inner store received %d bookings, want 1", got)
	}
}
