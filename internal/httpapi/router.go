// Package httpapi exposes the slotguard HTTP endpoints.
package httpapi

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/Driane/slotguard/internal/booking"
	"github.com/Driane/slotguard/internal/metrics"
)

// BookingService is what the HTTP layer needs from the booking package.
// Depending on an interface keeps the handlers easy to test in isolation.
type BookingService interface {
	CreateSlot(ctx context.Context, slot booking.Slot) (booking.Slot, error)
	GetSlot(ctx context.Context, id string) (booking.Slot, error)
	Book(ctx context.Context, req booking.BookingRequest) (booking.BookingResult, error)
}

// NewRouter returns the HTTP handler with every route registered. m may be
// nil: the API then runs without metrics and without GET /metrics.
func NewRouter(svc BookingService, logger *slog.Logger, m *metrics.Metrics) http.Handler {
	h := &handlers{svc: svc, logger: logger, metrics: m}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", health)
	mux.HandleFunc("POST /slots", h.createSlot)
	mux.HandleFunc("GET /slots/{id}", h.getSlot)
	mux.HandleFunc("POST /bookings", h.createBooking)
	if m != nil {
		mux.Handle("GET /metrics", m.Handler())
	}
	return m.Middleware(mux)
}

func health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
