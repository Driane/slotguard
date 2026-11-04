// Package cache puts a key-value cache, such as Redis, in front of a
// booking.Store. When a slot becomes popular, thousands of requests hit it
// after it is already booked; the cache answers them without touching
// PostgreSQL.
//
// It only caches facts that never change once true: "this idempotency key
// produced this booking" and "this slot is held by this booking". A missing or
// expired entry therefore never leads to a wrong booking: the request simply
// goes to the inner store, which stays the only judge of what may be booked.
package cache

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/Driane/slotguard/internal/booking"
)

// Backend is a key-value store with expiry.
type Backend interface {
	// Get returns the value stored at key, and whether there was one.
	Get(ctx context.Context, key string) (value string, found bool, err error)
	Set(ctx context.Context, key, value string, ttl time.Duration) error
}

const (
	bookingByKeyPrefix = "slotguard:booking-by-key:"
	slotHolderPrefix   = "slotguard:slot-holder:"
)

// Store is a booking.Store that consults a Backend before its inner store.
// The cache is best effort: when the backend fails, the error is logged and
// the request goes to the inner store, so an outage of the cache slows the
// service down but never breaks it.
type Store struct {
	inner   booking.Store
	backend Backend
	ttl     time.Duration
	logger  *slog.Logger

	hits, misses, errors atomic.Uint64
}

// Stats counts booking attempts answered by the cache (hits), sent on to the
// inner store (misses), and failed cache operations (errors).
type Stats struct {
	Hits, Misses, Errors uint64
}

// Stats returns the counters since the Store was created.
func (s *Store) Stats() Stats {
	return Stats{Hits: s.hits.Load(), Misses: s.misses.Load(), Errors: s.errors.Load()}
}

// New returns a Store caching entries for ttl in front of inner.
func New(inner booking.Store, backend Backend, ttl time.Duration, logger *slog.Logger) *Store {
	return &Store{inner: inner, backend: backend, ttl: ttl, logger: logger}
}

// CreateSlot implements booking.Store.
func (s *Store) CreateSlot(ctx context.Context, slot booking.Slot) error {
	return s.inner.CreateSlot(ctx, slot)
}

// GetSlot implements booking.Store. Slots are read rarely compared with
// booking attempts, so they are not cached.
func (s *Store) GetSlot(ctx context.Context, id string) (booking.Slot, error) {
	return s.inner.GetSlot(ctx, id)
}

// Book implements booking.Store, in the same order as the stores: the
// idempotency key first, then the slot.
func (s *Store) Book(ctx context.Context, b booking.Booking) (booking.BookingResult, error) {
	if existing, ok := s.cachedBooking(ctx, bookingByKeyPrefix+b.IdempotencyKey); ok {
		s.hits.Add(1)
		return booking.Replay(existing, b)
	}

	// The slot entry stores the winning booking itself, not just a "taken"
	// flag. A retry of the winner that reads it before the key entry is
	// written still recognises its own key and gets its booking back.
	if holder, ok := s.cachedBooking(ctx, slotHolderPrefix+b.SlotID); ok {
		s.hits.Add(1)
		if holder.IdempotencyKey == b.IdempotencyKey {
			return booking.Replay(holder, b)
		}
		return booking.BookingResult{}, booking.ErrSlotTaken
	}

	s.misses.Add(1)
	res, err := s.inner.Book(ctx, b)
	if err == nil {
		s.remember(ctx, res.Booking)
	}
	return res, err
}

// remember caches both facts established by a booking.
func (s *Store) remember(ctx context.Context, b booking.Booking) {
	data, err := json.Marshal(b)
	if err != nil {
		s.logger.Warn("cache: encode booking", "booking", b.ID, "err", err)
		return
	}
	s.set(ctx, bookingByKeyPrefix+b.IdempotencyKey, string(data))
	s.set(ctx, slotHolderPrefix+b.SlotID, string(data))
}

func (s *Store) cachedBooking(ctx context.Context, key string) (booking.Booking, bool) {
	value, found, err := s.backend.Get(ctx, key)
	if err != nil {
		s.errors.Add(1)
		s.logger.Warn("cache: read failed, using the store", "err", err)
		return booking.Booking{}, false
	}
	if !found {
		return booking.Booking{}, false
	}
	var b booking.Booking
	if err := json.Unmarshal([]byte(value), &b); err != nil {
		s.logger.Warn("cache: decode booking", "err", err)
		return booking.Booking{}, false
	}
	return b, true
}

func (s *Store) set(ctx context.Context, key, value string) {
	if err := s.backend.Set(ctx, key, value, s.ttl); err != nil {
		s.errors.Add(1)
		s.logger.Warn("cache: write failed", "err", err)
	}
}
