package booking

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// recordingStore is a minimal Store that remembers what the service sent,
// so these tests check the service rules without any real storage.
type recordingStore struct {
	slots    []Slot
	bookings []Booking
}

func (r *recordingStore) CreateSlot(_ context.Context, slot Slot) error {
	r.slots = append(r.slots, slot)
	return nil
}

func (r *recordingStore) GetSlot(_ context.Context, id string) (Slot, error) {
	for _, s := range r.slots {
		if s.ID == id {
			return s, nil
		}
	}
	return Slot{}, ErrSlotNotFound
}

func (r *recordingStore) Book(_ context.Context, b Booking) (BookingResult, error) {
	r.bookings = append(r.bookings, b)
	return BookingResult{Booking: b}, nil
}

func newTestService() (*Service, *recordingStore) {
	store := &recordingStore{}
	svc := NewService(store)
	svc.now = func() time.Time {
		return time.Date(2026, 11, 4, 9, 30, 0, 123456789, time.FixedZone("CET", 3600))
	}
	svc.newID = func() string { return "generated-id" }
	return svc, store
}

func TestServiceCreateSlot(t *testing.T) {
	start := time.Date(2026, 12, 1, 10, 0, 0, 0, time.UTC)

	tests := []struct {
		name    string
		slot    Slot
		wantErr error
		wantID  string
	}{
		{name: "valid slot keeps its id", slot: Slot{ID: "slot-1", StartsAt: start, EndsAt: start.Add(time.Hour)}, wantID: "slot-1"},
		{name: "missing id is generated", slot: Slot{StartsAt: start, EndsAt: start.Add(time.Hour)}, wantID: "generated-id"},
		{name: "missing start is rejected", slot: Slot{EndsAt: start}, wantErr: ErrInvalidRequest},
		{name: "end before start is rejected", slot: Slot{StartsAt: start, EndsAt: start.Add(-time.Minute)}, wantErr: ErrInvalidRequest},
		{name: "end equal to start is rejected", slot: Slot{StartsAt: start, EndsAt: start}, wantErr: ErrInvalidRequest},
		{name: "oversized id is rejected", slot: Slot{ID: strings.Repeat("x", 256), StartsAt: start, EndsAt: start.Add(time.Hour)}, wantErr: ErrInvalidRequest},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, store := newTestService()

			got, err := svc.CreateSlot(context.Background(), tt.slot)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			if tt.wantErr != nil {
				if len(store.slots) != 0 {
					t.Fatalf("an invalid slot reached the store: %+v", store.slots)
				}
				return
			}
			if got.ID != tt.wantID || !got.Available {
				t.Fatalf("got %+v, want id %q and available", got, tt.wantID)
			}
		})
	}
}

func TestServiceBookValidatesRequest(t *testing.T) {
	tests := []struct {
		name string
		req  BookingRequest
	}{
		{name: "missing slot", req: BookingRequest{CustomerID: "alice", IdempotencyKey: "k"}},
		{name: "missing customer", req: BookingRequest{SlotID: "s", IdempotencyKey: "k"}},
		{name: "missing idempotency key", req: BookingRequest{SlotID: "s", CustomerID: "alice"}},
		{name: "blank values only", req: BookingRequest{SlotID: " ", CustomerID: " ", IdempotencyKey: " "}},
		{name: "oversized key", req: BookingRequest{SlotID: "s", CustomerID: "alice", IdempotencyKey: strings.Repeat("k", 256)}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, store := newTestService()

			_, err := svc.Book(context.Background(), tt.req)
			if !errors.Is(err, ErrInvalidRequest) {
				t.Fatalf("err = %v, want %v", err, ErrInvalidRequest)
			}
			if len(store.bookings) != 0 {
				t.Fatalf("an invalid request reached the store: %+v", store.bookings)
			}
		})
	}
}

func TestServiceBookBuildsTheBooking(t *testing.T) {
	svc, store := newTestService()

	_, err := svc.Book(context.Background(), BookingRequest{SlotID: " slot-1 ", CustomerID: "alice", IdempotencyKey: "key-1"})
	if err != nil {
		t.Fatalf("Book: %v", err)
	}

	want := Booking{
		ID:             "generated-id",
		SlotID:         "slot-1",
		CustomerID:     "alice",
		IdempotencyKey: "key-1",
		// The service stores times in UTC, truncated to the microsecond.
		CreatedAt: time.Date(2026, 11, 4, 8, 30, 0, 123456000, time.UTC),
	}
	if len(store.bookings) != 1 || store.bookings[0] != want {
		t.Fatalf("store received %+v, want %+v", store.bookings, want)
	}
}

func TestNewIDIsRandomHex(t *testing.T) {
	a, b := NewID(), NewID()
	if len(a) != 32 || a == b {
		t.Fatalf("NewID returned %q and %q, want two different 32-character ids", a, b)
	}
}
