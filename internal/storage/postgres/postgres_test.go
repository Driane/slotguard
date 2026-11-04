package postgres_test

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Driane/slotguard/internal/booking"
	"github.com/Driane/slotguard/internal/booking/bookingtest"
	"github.com/Driane/slotguard/internal/outbox/outboxtest"
	"github.com/Driane/slotguard/internal/storage/postgres"
)

// These integration tests need a dedicated PostgreSQL database. Locally:
// docker compose up -d, then set SLOTGUARD_TEST_DATABASE_URL (see the
// README). The CI provides one.
func newPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("SLOTGUARD_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("SLOTGUARD_TEST_DATABASE_URL is not set: skipping PostgreSQL integration tests")
	}

	pool, err := pgxpool.New(context.Background(), url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)

	if err := postgres.Migrate(context.Background(), pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return pool
}

func TestStore(t *testing.T) {
	pool := newPool(t)
	// Every test uses random slot IDs and keys, so they can share one database.
	bookingtest.RunStoreTests(t, func(*testing.T) booking.Store {
		return postgres.New(pool)
	})
}

func TestOutbox(t *testing.T) {
	pool := newPool(t)
	// The outbox tests count events, so each one starts from an empty outbox.
	outboxtest.RunStoreTests(t, func(t *testing.T) outboxtest.Store {
		if _, err := pool.Exec(context.Background(), `TRUNCATE outbox_events`); err != nil {
			t.Fatalf("empty the outbox: %v", err)
		}
		return postgres.New(pool)
	})
}

func TestMigrateIsIdempotent(t *testing.T) {
	pool := newPool(t) // migrates once
	if err := postgres.Migrate(context.Background(), pool); err != nil {
		t.Fatalf("second Migrate: %v", err)
	}
}
