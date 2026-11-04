package booking

import (
	"errors"
	"testing"
)

func TestReplay(t *testing.T) {
	existing := Booking{ID: "b1", SlotID: "slot-1", CustomerID: "alice", IdempotencyKey: "key-1"}

	tests := []struct {
		name    string
		attempt Booking
		wantErr error
	}{
		{
			name:    "same request returns the existing booking",
			attempt: Booking{ID: "b2", SlotID: "slot-1", CustomerID: "alice", IdempotencyKey: "key-1"},
		},
		{
			name:    "same key for another slot is a conflict",
			attempt: Booking{ID: "b2", SlotID: "slot-2", CustomerID: "alice", IdempotencyKey: "key-1"},
			wantErr: ErrIdempotencyConflict,
		},
		{
			name:    "same key for another customer is a conflict",
			attempt: Booking{ID: "b2", SlotID: "slot-1", CustomerID: "bob", IdempotencyKey: "key-1"},
			wantErr: ErrIdempotencyConflict,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Replay(existing, tt.attempt)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			if tt.wantErr != nil {
				return
			}
			if !got.Replayed || got.Booking.ID != existing.ID {
				t.Fatalf("got %+v, want the existing booking replayed", got)
			}
		})
	}
}
