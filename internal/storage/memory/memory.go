// Package memory implements booking.Store and outbox.Store in memory. One
// mutex serialises every operation, which makes each booking, and the event
// written with it, atomic. It is the reference implementation the other
// stores are compared against.
package memory

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/Driane/slotguard/internal/booking"
	"github.com/Driane/slotguard/internal/outbox"
)

// Store is an in-memory booking.Store and outbox.Store, safe for concurrent use.
type Store struct {
	mu     sync.Mutex
	slots  map[string]booking.Slot
	bySlot map[string]booking.Booking // slot ID -> the booking that holds it
	byKey  map[string]booking.Booking // idempotency key -> booking
	events map[int64]*event
	nextID int64
}

// event is an outbox event with its delivery state.
type event struct {
	outbox.Event
	nextAttempt time.Time
	delivered   bool
	buried      bool
	lastError   string
}

// New returns an empty Store.
func New() *Store {
	return &Store{
		slots:  make(map[string]booking.Slot),
		bySlot: make(map[string]booking.Booking),
		byKey:  make(map[string]booking.Booking),
		events: make(map[int64]*event),
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

// Book implements booking.Store. Every check and every write happen under the
// same lock, so no other call can slip in between them.
func (s *Store) Book(_ context.Context, b booking.Booking) (booking.BookingResult, error) {
	payload, err := booking.EventPayload(b)
	if err != nil {
		return booking.BookingResult{}, err
	}

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
	s.nextID++
	s.events[s.nextID] = &event{
		Event:       outbox.Event{ID: s.nextID, Type: booking.EventBookingCreated, Payload: payload, CreatedAt: b.CreatedAt},
		nextAttempt: b.CreatedAt,
	}
	return booking.BookingResult{Booking: b}, nil
}

// Claim implements outbox.Store.
func (s *Store) Claim(_ context.Context, now time.Time, limit int, lease time.Duration) ([]outbox.Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var due []*event
	for _, e := range s.events {
		if !e.delivered && !e.buried && !e.nextAttempt.After(now) {
			due = append(due, e)
		}
	}
	sort.Slice(due, func(i, j int) bool { return due[i].ID < due[j].ID })
	if len(due) > limit {
		due = due[:limit]
	}

	claimed := make([]outbox.Event, 0, len(due))
	for _, e := range due {
		e.Attempts++
		e.nextAttempt = now.Add(lease)
		claimed = append(claimed, e.Event)
	}
	return claimed, nil
}

// MarkDelivered implements outbox.Store.
func (s *Store) MarkDelivered(_ context.Context, id int64, _ time.Time) error {
	return s.update(id, func(e *event) { e.delivered = true })
}

// Retry implements outbox.Store.
func (s *Store) Retry(_ context.Context, id int64, next time.Time, reason string) error {
	return s.update(id, func(e *event) {
		e.nextAttempt = next
		e.lastError = reason
	})
}

// Bury implements outbox.Store.
func (s *Store) Bury(_ context.Context, id int64, _ time.Time, reason string) error {
	return s.update(id, func(e *event) {
		e.buried = true
		e.lastError = reason
	})
}

func (s *Store) update(id int64, change func(*event)) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	e, ok := s.events[id]
	if !ok {
		return outbox.ErrNotFound
	}
	change(e)
	return nil
}
