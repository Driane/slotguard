// Package booking holds the reservation rules: slots, bookings and the
// guarantee that a slot is never booked twice, whatever the load.
package booking

import (
	"encoding/json"
	"errors"
	"time"
)

// EventBookingCreated is the type of the outbox event written with every new
// booking, in the same transaction.
const EventBookingCreated = "booking.created"

// Errors returned by the booking service and its stores. Callers compare
// them with errors.Is, because they are often wrapped with more context.
var (
	ErrInvalidRequest      = errors.New("invalid request")
	ErrSlotNotFound        = errors.New("slot not found")
	ErrSlotExists          = errors.New("slot already exists")
	ErrSlotTaken           = errors.New("slot already booked")
	ErrIdempotencyConflict = errors.New("idempotency key already used for a different request")
)

// Slot is a period of time that can be booked by exactly one customer.
type Slot struct {
	ID       string
	StartsAt time.Time
	EndsAt   time.Time
	// Available is computed by the store when the slot is read: it is false
	// once the slot has been booked.
	Available bool
}

// Booking records that a customer holds a slot.
type Booking struct {
	ID             string
	SlotID         string
	CustomerID     string
	IdempotencyKey string
	CreatedAt      time.Time
}

// BookingRequest is what a client asks for. The idempotency key identifies
// the request: sending the same request again must not book twice.
type BookingRequest struct {
	SlotID         string
	CustomerID     string
	IdempotencyKey string
}

// BookingResult is the outcome of a booking request.
type BookingResult struct {
	Booking Booking
	// Replayed is true when the request had already been processed: the
	// existing booking is returned and nothing new is created.
	Replayed bool
}

// SameRequestAs reports whether b and other were made by the same request,
// that is for the same slot and the same customer.
func (b Booking) SameRequestAs(other Booking) bool {
	return b.SlotID == other.SlotID && b.CustomerID == other.CustomerID
}

// EventPayload returns the JSON document carried by the booking.created
// event. It deliberately leaves out the idempotency key, which is a client
// secret, not something to broadcast.
func EventPayload(b Booking) ([]byte, error) {
	return json.Marshal(struct {
		ID         string    `json:"id"`
		SlotID     string    `json:"slot_id"`
		CustomerID string    `json:"customer_id"`
		CreatedAt  time.Time `json:"created_at"`
	}{b.ID, b.SlotID, b.CustomerID, b.CreatedAt})
}

// Replay returns the outcome of attempt when its idempotency key already
// produced existing: the existing booking if attempt is the same request,
// or ErrIdempotencyConflict if the key is being reused for another one.
func Replay(existing, attempt Booking) (BookingResult, error) {
	if !existing.SameRequestAs(attempt) {
		return BookingResult{}, ErrIdempotencyConflict
	}
	return BookingResult{Booking: existing, Replayed: true}, nil
}
