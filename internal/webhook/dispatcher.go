package webhook

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/Driane/slotguard/internal/outbox"
)

// Envelope is the JSON body POSTed for every event. Data is the event payload,
// for example the booking for a booking.created event.
type Envelope struct {
	ID        int64           `json:"id"`
	Type      string          `json:"type"`
	CreatedAt time.Time       `json:"created_at"`
	Data      json.RawMessage `json:"data"`
}

// Config holds the dispatcher settings. Zero values get the defaults below.
type Config struct {
	URL          string        // endpoint receiving the webhooks (required)
	Secret       []byte        // HMAC key shared with the receiver (required)
	BatchSize    int           // events claimed per poll; default 20
	PollInterval time.Duration // pause between polls when idle; default 1s
	Timeout      time.Duration // per HTTP request; default 10s
	Lease        time.Duration // how long a claimed event stays hidden; default 1 min, must exceed Timeout
	MaxAttempts  int           // attempts before an event is buried; default 10
}

// Dispatcher delivers outbox events, at least once: a crash between a
// successful delivery and its recording leads to a second delivery, so
// receivers deduplicate on the Slotguard-Event-Id header.
type Dispatcher struct {
	store   outbox.Store
	cfg     Config
	client  *http.Client
	logger  *slog.Logger
	now     func() time.Time
	backoff func(attempt int) time.Duration
}

// NewDispatcher checks cfg, fills in the defaults and returns a Dispatcher.
func NewDispatcher(store outbox.Store, cfg Config, logger *slog.Logger) (*Dispatcher, error) {
	if cfg.URL == "" || len(cfg.Secret) == 0 {
		return nil, errors.New("webhook: URL and secret are required")
	}
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = 20
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = time.Second
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 10 * time.Second
	}
	if cfg.Lease <= 0 {
		cfg.Lease = time.Minute
	}
	if cfg.MaxAttempts <= 0 {
		cfg.MaxAttempts = 10
	}
	if cfg.Lease <= cfg.Timeout {
		// Otherwise another worker could claim an event that is still being
		// delivered, and send it twice.
		return nil, fmt.Errorf("webhook: lease (%s) must be longer than the request timeout (%s)", cfg.Lease, cfg.Timeout)
	}

	return &Dispatcher{
		store:   store,
		cfg:     cfg,
		client:  &http.Client{Timeout: cfg.Timeout},
		logger:  logger,
		now:     time.Now,
		backoff: Backoff,
	}, nil
}

// Run delivers events until ctx is cancelled. After a full batch it polls
// again at once, so a backlog drains quickly; otherwise it waits PollInterval.
func (d *Dispatcher) Run(ctx context.Context) {
	for {
		claimed, err := d.RunOnce(ctx)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			d.logger.Error("webhook dispatch failed", "err", err)
		}
		if claimed == d.cfg.BatchSize {
			continue
		}

		timer := time.NewTimer(d.cfg.PollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

// RunOnce claims one batch of due events, delivers them concurrently and
// records each outcome. It returns the number of events claimed.
func (d *Dispatcher) RunOnce(ctx context.Context) (int, error) {
	events, err := d.store.Claim(ctx, d.now(), d.cfg.BatchSize, d.cfg.Lease)
	if err != nil {
		return 0, err
	}

	var wg sync.WaitGroup
	for _, e := range events {
		wg.Add(1)
		go func() {
			defer wg.Done()
			d.handle(ctx, e)
		}()
	}
	wg.Wait()
	return len(events), nil
}

// handle delivers one event and records what happened: delivered, retried
// later, or buried after too many attempts.
func (d *Dispatcher) handle(ctx context.Context, e outbox.Event) {
	deliveryErr := d.deliver(ctx, e)

	// Record the outcome even if ctx was cancelled in the meantime: losing
	// the record of a successful delivery would cause a duplicate.
	recordCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()

	now := d.now()
	var err error
	switch {
	case deliveryErr == nil:
		err = d.store.MarkDelivered(recordCtx, e.ID, now)
	case e.Attempts >= d.cfg.MaxAttempts:
		d.logger.Error("webhook abandoned", "event", e.ID, "attempts", e.Attempts, "err", deliveryErr)
		err = d.store.Bury(recordCtx, e.ID, now, deliveryErr.Error())
	default:
		next := now.Add(d.backoff(e.Attempts))
		d.logger.Warn("webhook failed, will retry", "event", e.ID, "attempt", e.Attempts, "next", next, "err", deliveryErr)
		err = d.store.Retry(recordCtx, e.ID, next, deliveryErr.Error())
	}
	if err != nil {
		d.logger.Error("recording webhook outcome", "event", e.ID, "err", err)
	}
}

// deliver POSTs one signed event. Any status outside 2xx is a failure.
func (d *Dispatcher) deliver(ctx context.Context, e outbox.Event) error {
	body, err := json.Marshal(Envelope{ID: e.ID, Type: e.Type, CreatedAt: e.CreatedAt, Data: e.Payload})
	if err != nil {
		return fmt.Errorf("encode event: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.cfg.URL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(EventIDHeader, strconv.FormatInt(e.ID, 10))
	req.Header.Set(SignatureHeader, Sign(d.cfg.Secret, d.now(), body))

	resp, err := d.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	// Read a bounded amount of the body so the connection can be reused.
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("endpoint answered HTTP %d", resp.StatusCode)
	}
	return nil
}

// Backoff returns the delay before retrying after the given attempt:
// exponential (about 2s, 4s, 8s…) and capped at one hour. Half of the delay is
// random, so events that failed together do not all retry at the same moment.
func Backoff(attempt int) time.Duration {
	const base, ceiling = 2 * time.Second, time.Hour
	if attempt < 1 {
		attempt = 1
	}
	delay := ceiling
	if attempt < 12 {
		delay = min(base<<(attempt-1), ceiling)
	}
	return delay/2 + rand.N(delay/2+1)
}
