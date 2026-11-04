// Package outbox holds the events waiting to be delivered to the webhook
// endpoint, and the way workers claim them.
//
// Events are written in the same transaction as the change they describe (the
// transactional outbox pattern): a booking can never exist without its event,
// and an event can never describe a booking that was rolled back.
package outbox

import (
	"context"
	"errors"
	"time"
)

// ErrNotFound is returned when an event ID does not exist.
var ErrNotFound = errors.New("outbox event not found")

// Event is a fact waiting to be delivered.
type Event struct {
	ID        int64
	Type      string
	Payload   []byte // JSON document
	CreatedAt time.Time
	// Attempts counts delivery attempts, including the one in progress once
	// the event has been claimed.
	Attempts int
}

// Store gives delivery workers access to pending events. Implementations must
// be safe for concurrent use by several workers, in several processes.
type Store interface {
	// Claim returns up to limit events that are due at now, oldest first,
	// and hides them from other workers until now+lease. A worker that dies
	// mid-delivery therefore does not lose events: they reappear after the
	// lease.
	Claim(ctx context.Context, now time.Time, limit int, lease time.Duration) ([]Event, error)

	// MarkDelivered records a successful delivery: the event is never
	// claimed again.
	MarkDelivered(ctx context.Context, id int64, at time.Time) error

	// Retry schedules the next delivery attempt of a failed event.
	Retry(ctx context.Context, id int64, next time.Time, reason string) error

	// Bury gives up on an event that failed too many times. It is kept for
	// inspection but never claimed again.
	Bury(ctx context.Context, id int64, at time.Time, reason string) error
}
