// Command api starts the slotguard HTTP API.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Driane/slotguard/internal/booking"
	"github.com/Driane/slotguard/internal/httpapi"
	"github.com/Driane/slotguard/internal/storage/memory"
	"github.com/Driane/slotguard/internal/storage/postgres"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	if err := run(logger); err != nil {
		logger.Error("server stopped", "err", err)
		os.Exit(1)
	}
}

// run serves HTTP until the process receives Ctrl+C or SIGTERM, then lets
// in-flight requests finish before returning.
func run(logger *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	addr := ":8080"
	if port := os.Getenv("PORT"); port != "" {
		addr = ":" + port
	}

	store, closeStore, err := openStore(ctx, logger)
	if err != nil {
		return err
	}
	defer closeStore()

	svc := booking.NewService(store)
	srv := &http.Server{
		Addr:              addr,
		Handler:           httpapi.NewRouter(svc, logger),
		ReadHeaderTimeout: 5 * time.Second,
	}

	serveErr := make(chan error, 1)
	go func() {
		logger.Info("slotguard API listening", "addr", addr)
		serveErr <- srv.ListenAndServe()
	}()

	select {
	case err := <-serveErr:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		logger.Info("shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	}
}

// openStore uses PostgreSQL when DATABASE_URL is set, and the in-memory store
// otherwise. The returned function releases the store's resources.
func openStore(ctx context.Context, logger *slog.Logger) (booking.Store, func(), error) {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		logger.Info("DATABASE_URL is not set: using the in-memory store")
		return memory.New(), func() {}, nil
	}

	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, nil, fmt.Errorf("connect to PostgreSQL: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, nil, fmt.Errorf("reach PostgreSQL: %w", err)
	}
	if err := postgres.Migrate(ctx, pool); err != nil {
		pool.Close()
		return nil, nil, fmt.Errorf("migrate: %w", err)
	}
	logger.Info("using the PostgreSQL store")
	return postgres.New(pool), pool.Close, nil
}
