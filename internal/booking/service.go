package booking

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

// maxKeyLength bounds client-supplied identifiers so a single request cannot
// store an arbitrarily large value.
const maxKeyLength = 255

// Service validates requests and applies the booking rules on top of a Store.
type Service struct {
	store Store
	now   func() time.Time
	newID func() string
}

// NewService returns a Service backed by store.
func NewService(store Store) *Service {
	return &Service{store: store, now: time.Now, newID: NewID}
}

// CreateSlot validates and stores a new slot. A missing ID is generated.
func (s *Service) CreateSlot(ctx context.Context, slot Slot) (Slot, error) {
	slot.ID = strings.TrimSpace(slot.ID)
	if slot.ID == "" {
		slot.ID = s.newID()
	}
	if len(slot.ID) > maxKeyLength {
		return Slot{}, fmt.Errorf("%w: id is longer than %d characters", ErrInvalidRequest, maxKeyLength)
	}
	if slot.StartsAt.IsZero() || !slot.EndsAt.After(slot.StartsAt) {
		return Slot{}, fmt.Errorf("%w: a slot needs a start and must end after it starts", ErrInvalidRequest)
	}

	// PostgreSQL keeps microseconds and no time zone: normalise now so that
	// every store returns exactly what it was given.
	slot.StartsAt = normalise(slot.StartsAt)
	slot.EndsAt = normalise(slot.EndsAt)
	slot.Available = true

	if err := s.store.CreateSlot(ctx, slot); err != nil {
		return Slot{}, err
	}
	return slot, nil
}

// GetSlot returns a slot and whether it can still be booked.
func (s *Service) GetSlot(ctx context.Context, id string) (Slot, error) {
	return s.store.GetSlot(ctx, strings.TrimSpace(id))
}

// Book books a slot for a customer. Retrying with the same idempotency key
// returns the original booking instead of creating a second one.
func (s *Service) Book(ctx context.Context, req BookingRequest) (BookingResult, error) {
	req.SlotID = strings.TrimSpace(req.SlotID)
	req.CustomerID = strings.TrimSpace(req.CustomerID)
	req.IdempotencyKey = strings.TrimSpace(req.IdempotencyKey)

	switch {
	case req.SlotID == "" || req.CustomerID == "" || req.IdempotencyKey == "":
		return BookingResult{}, fmt.Errorf("%w: slot id, customer id and idempotency key are required", ErrInvalidRequest)
	case len(req.SlotID) > maxKeyLength || len(req.CustomerID) > maxKeyLength || len(req.IdempotencyKey) > maxKeyLength:
		return BookingResult{}, fmt.Errorf("%w: identifiers are limited to %d characters", ErrInvalidRequest, maxKeyLength)
	}

	return s.store.Book(ctx, Booking{
		ID:             s.newID(),
		SlotID:         req.SlotID,
		CustomerID:     req.CustomerID,
		IdempotencyKey: req.IdempotencyKey,
		CreatedAt:      normalise(s.now()),
	})
}

// NewID returns a random 128-bit identifier encoded as 32 hex characters.
func NewID() string {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		panic(fmt.Sprintf("booking: reading random bytes: %v", err))
	}
	return hex.EncodeToString(buf[:])
}

func normalise(t time.Time) time.Time {
	return t.UTC().Truncate(time.Microsecond)
}
