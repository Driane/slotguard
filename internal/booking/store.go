package booking

import "context"

// Store persists slots and bookings.
//
// Implementations must make Book atomic: whatever the number of concurrent
// calls, a slot is booked at most once and an idempotency key creates at most
// one booking. The bookingtest package checks this contract.
type Store interface {
	// CreateSlot stores a new slot. It returns ErrSlotExists if the ID is
	// already used.
	CreateSlot(ctx context.Context, slot Slot) error

	// GetSlot returns a slot with its current availability, or
	// ErrSlotNotFound.
	GetSlot(ctx context.Context, id string) (Slot, error)

	// Book stores b if its slot exists and is still free, together with its
	// EventBookingCreated outbox event when the store has an outbox. If a
	// booking with the same idempotency key already exists, Book returns the
	// result of Replay instead of creating anything. Errors: ErrSlotNotFound,
	// ErrSlotTaken, ErrIdempotencyConflict.
	Book(ctx context.Context, b Booking) (BookingResult, error)
}
