// Command api starts the slotguard HTTP API and, when configured, the webhook
// dispatcher.
//
// Configuration, all optional, through environment variables:
//
//	PORT            HTTP port (default 8080)
//	DATABASE_URL    PostgreSQL; without it, data is kept in memory
//	REDIS_URL       Redis cache in front of the store
//	WEBHOOK_URL     endpoint receiving booking events
//	WEBHOOK_SECRET  key used to sign webhooks (required with WEBHOOK_URL)
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/Driane/slotguard/internal/booking"
	"github.com/Driane/slotguard/internal/httpapi"
	"github.com/Driane/slotguard/internal/outbox"
	"github.com/Driane/slotguard/internal/storage/cache"
	"github.com/Driane/slotguard/internal/storage/memory"
	"github.com/Driane/slotguard/internal/storage/postgres"
	"github.com/Driane/slotguard/internal/webhook"
)

// cacheTTL is how long cached bookings are kept. Retries come within seconds
// or minutes; a day leaves a wide margin.
const cacheTTL = 24 * time.Hour

// storage is what the API and the dispatcher need from a store.
type storage interface {
	booking.Store
	outbox.Store
}

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	if err := run(logger); err != nil {
		logger.Error("server stopped", "err", err)
		os.Exit(1)
	}
}

// run serves HTTP until the process receives Ctrl+C or SIGTERM, then lets
// in-flight requests and webhook deliveries finish before returning.
func run(logger *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	store, closeStore, err := openStore(ctx, logger)
	if err != nil {
		return err
	}
	// Deferred calls run in reverse order: cancel the background workers,
	// wait for them, and only then close the store they use.
	defer closeStore()
	var workers sync.WaitGroup
	defer workers.Wait()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	bookings, closeCache, err := withCache(ctx, store, logger)
	if err != nil {
		return err
	}
	defer closeCache()

	if err := startDispatcher(ctx, &workers, store, logger); err != nil {
		return err
	}

	addr := ":8080"
	if port := os.Getenv("PORT"); port != "" {
		addr = ":" + port
	}
	srv := &http.Server{
		Addr:              addr,
		Handler:           httpapi.NewRouter(booking.NewService(bookings), logger),
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
		shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancelShutdown()
		return srv.Shutdown(shutdownCtx)
	}
}

// openStore uses PostgreSQL when DATABASE_URL is set, and the in-memory store
// otherwise. The returned function releases the store's resources.
func openStore(ctx context.Context, logger *slog.Logger) (storage, func(), error) {
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

// withCache puts the Redis cache in front of store when REDIS_URL is set.
func withCache(ctx context.Context, store booking.Store, logger *slog.Logger) (booking.Store, func(), error) {
	url := os.Getenv("REDIS_URL")
	if url == "" {
		return store, func() {}, nil
	}

	opts, err := redis.ParseURL(url)
	if err != nil {
		return nil, nil, fmt.Errorf("parse REDIS_URL: %w", err)
	}
	client := redis.NewClient(opts)
	if err := client.Ping(ctx).Err(); err != nil {
		client.Close()
		return nil, nil, fmt.Errorf("reach Redis: %w", err)
	}
	logger.Info("using the Redis cache")
	return cache.New(store, cache.NewRedis(client), cacheTTL, logger), func() { client.Close() }, nil
}

// startDispatcher delivers booking events in the background when WEBHOOK_URL
// is set. Without it, events simply wait in the outbox.
func startDispatcher(ctx context.Context, workers *sync.WaitGroup, store outbox.Store, logger *slog.Logger) error {
	url := os.Getenv("WEBHOOK_URL")
	if url == "" {
		logger.Info("WEBHOOK_URL is not set: booking events stay in the outbox")
		return nil
	}

	d, err := webhook.NewDispatcher(store, webhook.Config{URL: url, Secret: []byte(os.Getenv("WEBHOOK_SECRET"))}, logger)
	if err != nil {
		return err
	}
	workers.Add(1)
	go func() {
		defer workers.Done()
		d.Run(ctx)
	}()
	logger.Info("delivering booking events", "url", url)
	return nil
}
