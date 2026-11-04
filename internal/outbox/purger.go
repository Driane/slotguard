package outbox

import (
	"context"
	"log/slog"
	"time"
)

// RunPurger deletes the events delivered more than retention ago, once at
// start and then every interval, until ctx is cancelled. Without it, the
// outbox would keep every event ever delivered.
func RunPurger(ctx context.Context, store Store, interval, retention time.Duration, logger *slog.Logger) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		deleted, err := store.Purge(ctx, time.Now().Add(-retention))
		switch {
		case ctx.Err() != nil:
			return
		case err != nil:
			logger.Error("outbox purge failed", "err", err)
		case deleted > 0:
			logger.Info("outbox purged", "deleted", deleted)
		}

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
