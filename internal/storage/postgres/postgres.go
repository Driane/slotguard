// Package postgres implements booking.Store on PostgreSQL. The database, not
// the application, guarantees that a slot is booked at most once: two UNIQUE
// constraints reject any second booking, however many requests race.
package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Driane/slotguard/internal/booking"
)

// PostgreSQL error codes this store reacts to.
const (
	codeUniqueViolation     = "23505"
	codeForeignKeyViolation = "23503"
)

// Store is a booking.Store backed by a PostgreSQL connection pool.
type Store struct {
	pool *pgxpool.Pool
}

// New returns a Store using pool. The schema must be up to date: see Migrate.
func New(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

// CreateSlot implements booking.Store.
func (s *Store) CreateSlot(ctx context.Context, slot booking.Slot) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO slots (id, starts_at, ends_at) VALUES ($1, $2, $3)`,
		slot.ID, slot.StartsAt, slot.EndsAt)
	if isCode(err, codeUniqueViolation) {
		return booking.ErrSlotExists
	}
	if err != nil {
		return fmt.Errorf("insert slot: %w", err)
	}
	return nil
}

// GetSlot implements booking.Store.
func (s *Store) GetSlot(ctx context.Context, id string) (booking.Slot, error) {
	var slot booking.Slot
	err := s.pool.QueryRow(ctx,
		`SELECT s.id, s.starts_at, s.ends_at,
		        NOT EXISTS (SELECT 1 FROM bookings b WHERE b.slot_id = s.id)
		   FROM slots s
		  WHERE s.id = $1`, id,
	).Scan(&slot.ID, &slot.StartsAt, &slot.EndsAt, &slot.Available)
	if errors.Is(err, pgx.ErrNoRows) {
		return booking.Slot{}, booking.ErrSlotNotFound
	}
	if err != nil {
		return booking.Slot{}, fmt.Errorf("select slot: %w", err)
	}
	slot.StartsAt = slot.StartsAt.UTC()
	slot.EndsAt = slot.EndsAt.UTC()
	return slot, nil
}

// Book implements booking.Store. It simply tries the INSERT and lets the
// constraints decide: there is no "check, then write" window for another
// request to slip into.
func (s *Store) Book(ctx context.Context, b booking.Booking) (booking.BookingResult, error) {
	// Fast path for retries: the key may already have produced a booking.
	existing, err := s.bookingByKey(ctx, b.IdempotencyKey)
	switch {
	case err == nil:
		return booking.Replay(existing, b)
	case !errors.Is(err, pgx.ErrNoRows):
		return booking.BookingResult{}, err
	}

	_, err = s.pool.Exec(ctx,
		`INSERT INTO bookings (id, slot_id, customer_id, idempotency_key, created_at)
		 VALUES ($1, $2, $3, $4, $5)`,
		b.ID, b.SlotID, b.CustomerID, b.IdempotencyKey, b.CreatedAt)
	switch {
	case err == nil:
		return booking.BookingResult{Booking: b}, nil
	case isCode(err, codeForeignKeyViolation):
		return booking.BookingResult{}, booking.ErrSlotNotFound
	case isCode(err, codeUniqueViolation):
		return s.resolveUniqueViolation(ctx, b)
	default:
		return booking.BookingResult{}, fmt.Errorf("insert booking: %w", err)
	}
}

// resolveUniqueViolation explains why the INSERT of b was rejected. When two
// requests with the same key race, PostgreSQL may report either constraint,
// so the key is checked first: if it already produced a booking, this is a
// retry. Otherwise someone else holds the slot.
func (s *Store) resolveUniqueViolation(ctx context.Context, b booking.Booking) (booking.BookingResult, error) {
	existing, err := s.bookingByKey(ctx, b.IdempotencyKey)
	switch {
	case err == nil:
		return booking.Replay(existing, b)
	case errors.Is(err, pgx.ErrNoRows):
		return booking.BookingResult{}, booking.ErrSlotTaken
	default:
		return booking.BookingResult{}, err
	}
}

func (s *Store) bookingByKey(ctx context.Context, key string) (booking.Booking, error) {
	var b booking.Booking
	err := s.pool.QueryRow(ctx,
		`SELECT id, slot_id, customer_id, idempotency_key, created_at
		   FROM bookings
		  WHERE idempotency_key = $1`, key,
	).Scan(&b.ID, &b.SlotID, &b.CustomerID, &b.IdempotencyKey, &b.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return booking.Booking{}, err
	}
	if err != nil {
		return booking.Booking{}, fmt.Errorf("select booking by key: %w", err)
	}
	b.CreatedAt = b.CreatedAt.UTC()
	return b, nil
}

// isCode reports whether err is a PostgreSQL error with the given code.
func isCode(err error, code string) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == code
}
