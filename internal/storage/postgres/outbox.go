package postgres

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Driane/slotguard/internal/outbox"
)

// claimSQL picks due events and pushes their next attempt to the end of the
// lease, in one statement. FOR UPDATE SKIP LOCKED makes concurrent workers
// skip the rows another worker is claiming instead of waiting for them, so
// several workers, in several processes, never claim the same event.
const claimSQL = `
UPDATE outbox_events
   SET attempts = attempts + 1,
       next_attempt_at = $2
 WHERE id IN (
        SELECT id
          FROM outbox_events
         WHERE delivered_at IS NULL
           AND dead_at IS NULL
           AND next_attempt_at <= $1
         ORDER BY id
         LIMIT $3
           FOR UPDATE SKIP LOCKED)
RETURNING id, event_type, payload, created_at, attempts`

// Claim implements outbox.Store.
func (s *Store) Claim(ctx context.Context, now time.Time, limit int, lease time.Duration) ([]outbox.Event, error) {
	rows, err := s.pool.Query(ctx, claimSQL, now, now.Add(lease), limit)
	if err != nil {
		return nil, fmt.Errorf("claim events: %w", err)
	}
	events, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (outbox.Event, error) {
		var e outbox.Event
		err := row.Scan(&e.ID, &e.Type, &e.Payload, &e.CreatedAt, &e.Attempts)
		e.CreatedAt = e.CreatedAt.UTC()
		return e, err
	})
	if err != nil {
		return nil, fmt.Errorf("read claimed events: %w", err)
	}

	// RETURNING does not keep the ORDER BY of the subquery.
	sort.Slice(events, func(i, j int) bool { return events[i].ID < events[j].ID })
	return events, nil
}

// MarkDelivered implements outbox.Store.
func (s *Store) MarkDelivered(ctx context.Context, id int64, at time.Time) error {
	return s.updateEvent(ctx, `UPDATE outbox_events SET delivered_at = $2 WHERE id = $1`, id, at)
}

// Retry implements outbox.Store.
func (s *Store) Retry(ctx context.Context, id int64, next time.Time, reason string) error {
	return s.updateEvent(ctx, `UPDATE outbox_events SET next_attempt_at = $2, last_error = $3 WHERE id = $1`, id, next, reason)
}

// Bury implements outbox.Store.
func (s *Store) Bury(ctx context.Context, id int64, at time.Time, reason string) error {
	return s.updateEvent(ctx, `UPDATE outbox_events SET dead_at = $2, last_error = $3 WHERE id = $1`, id, at, reason)
}

func (s *Store) updateEvent(ctx context.Context, sql string, args ...any) error {
	tag, err := s.pool.Exec(ctx, sql, args...)
	if err != nil {
		return fmt.Errorf("update outbox event: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return outbox.ErrNotFound
	}
	return nil
}
