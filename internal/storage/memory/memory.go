// Package memory implements booking.Store in memory. One mutex serialises
// every operation, which makes each booking atomic. It is the reference
// implementation the other stores are compared against.
package memory

import (
	"context"
	"sync"

	"github.com/Driane/slotguard/internal/booking"
)

// Store is an in-memory booking.Store, safe for concurrent use.
type Store struct {
	mu     sync.Mutex
	slots  map[string]booking.Slot
	bySlot map[string]booking.Booking // slot ID -> the booking that holds it
	byKey  map[string]booking.Booking // idempotency key -> booking
}

// New returns an empty Store.
func New() *Store {
	return &Store{
		slots:  make(map[string]booking.Slot),
		bySlot: make(map[string]booking.Booking),
		byKey:  make(map[string]booking.Booking),
	}
}

// CreateSlot implements booking.Store.
func (s *Store) CreateSlot(_ context.Context, slot booking.Slot) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.slots[slot.ID]; exists {
		return booking.ErrSlotExists
	}
	s.slots[slot.ID] = slot
	return nil
}

// GetSlot implements booking.Store.
func (s *Store) GetSlot(_ context.Context, id string) (booking.Slot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	slot, ok := s.slots[id]
	if !ok {
		return booking.Slot{}, booking.ErrSlotNotFound
	}
	_, booked := s.bySlot[id]
	slot.Available = !booked
	return slot, nil
}

// Book implements booking.Store. Every check and the write happen under the
// same lock, so no other call can slip in between them.
func (s *Store) Book(_ context.Context, b booking.Booking) (booking.BookingResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if existing, ok := s.byKey[b.IdempotencyKey]; ok {
		return booking.Replay(existing, b)
	}
	if _, ok := s.slots[b.SlotID]; !ok {
		return booking.BookingResult{}, booking.ErrSlotNotFound
	}
	if _, taken := s.bySlot[b.SlotID]; taken {
		return booking.BookingResult{}, booking.ErrSlotTaken
	}

	s.bySlot[b.SlotID] = b
	s.byKey[b.IdempotencyKey] = b
	return booking.BookingResult{Booking: b}, nil
}
