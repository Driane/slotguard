package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/Driane/slotguard/internal/booking"
	"github.com/Driane/slotguard/internal/metrics"
)

// maxBodyBytes caps request bodies: every valid request is a few hundred bytes.
const maxBodyBytes = 1 << 20

const (
	headerIdempotencyKey = "Idempotency-Key"
	headerReplayed       = "Idempotent-Replayed"
)

type handlers struct {
	svc     BookingService
	logger  *slog.Logger
	metrics *metrics.Metrics // may be nil
}

type slotJSON struct {
	ID        string    `json:"id"`
	StartsAt  time.Time `json:"starts_at"`
	EndsAt    time.Time `json:"ends_at"`
	Available bool      `json:"available"`
}

type createSlotRequest struct {
	ID       string    `json:"id"`
	StartsAt time.Time `json:"starts_at"`
	EndsAt   time.Time `json:"ends_at"`
}

type bookingJSON struct {
	ID         string    `json:"id"`
	SlotID     string    `json:"slot_id"`
	CustomerID string    `json:"customer_id"`
	CreatedAt  time.Time `json:"created_at"`
}

type createBookingRequest struct {
	SlotID     string `json:"slot_id"`
	CustomerID string `json:"customer_id"`
}

type errorJSON struct {
	Error   string `json:"error"`
	Message string `json:"message"`
}

func (h *handlers) createSlot(w http.ResponseWriter, r *http.Request) {
	var req createSlotRequest
	if err := decodeJSON(w, r, &req); err != nil {
		h.writeError(w, r, err)
		return
	}

	slot, err := h.svc.CreateSlot(r.Context(), booking.Slot{ID: req.ID, StartsAt: req.StartsAt, EndsAt: req.EndsAt})
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, toSlotJSON(slot))
}

func (h *handlers) getSlot(w http.ResponseWriter, r *http.Request) {
	slot, err := h.svc.GetSlot(r.Context(), r.PathValue("id"))
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toSlotJSON(slot))
}

// createBooking books a slot. The Idempotency-Key header is mandatory: a
// client that retries after a timeout sends the same key and receives the
// original booking, flagged with the Idempotent-Replayed header.
func (h *handlers) createBooking(w http.ResponseWriter, r *http.Request) {
	var req createBookingRequest
	if err := decodeJSON(w, r, &req); err != nil {
		h.writeError(w, r, err)
		return
	}

	res, err := h.svc.Book(r.Context(), booking.BookingRequest{
		SlotID:         req.SlotID,
		CustomerID:     req.CustomerID,
		IdempotencyKey: r.Header.Get(headerIdempotencyKey),
	})
	if err != nil {
		_, code := classify(err)
		h.metrics.Booking(code)
		h.writeError(w, r, err)
		return
	}

	if res.Replayed {
		h.metrics.Booking("replayed")
		w.Header().Set(headerReplayed, "true")
	} else {
		h.metrics.Booking("created")
	}
	writeJSON(w, http.StatusCreated, bookingJSON{
		ID:         res.Booking.ID,
		SlotID:     res.Booking.SlotID,
		CustomerID: res.Booking.CustomerID,
		CreatedAt:  res.Booking.CreatedAt,
	})
}

// decodeJSON reads a size-limited JSON body and rejects unknown fields, so a
// typo in a field name fails loudly instead of being silently ignored.
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return fmt.Errorf("%w: malformed JSON body: %v", booking.ErrInvalidRequest, err)
	}
	return nil
}

// classify maps a domain error to an HTTP status and a stable error code that
// clients can rely on.
func classify(err error) (status int, code string) {
	switch {
	case errors.Is(err, booking.ErrInvalidRequest):
		return http.StatusBadRequest, "invalid_request"
	case errors.Is(err, booking.ErrSlotNotFound):
		return http.StatusNotFound, "slot_not_found"
	case errors.Is(err, booking.ErrSlotExists):
		return http.StatusConflict, "slot_exists"
	case errors.Is(err, booking.ErrSlotTaken):
		return http.StatusConflict, "slot_taken"
	case errors.Is(err, booking.ErrIdempotencyConflict):
		return http.StatusUnprocessableEntity, "idempotency_conflict"
	default:
		return http.StatusInternalServerError, "internal_error"
	}
}

// writeError answers with the status and code chosen by classify.
func (h *handlers) writeError(w http.ResponseWriter, r *http.Request, err error) {
	status, code := classify(err)
	message := err.Error()
	if status == http.StatusInternalServerError {
		// Internal details stay in the logs, not in the response.
		h.logger.Error("request failed", "method", r.Method, "path", r.URL.Path, "err", err)
		message = "internal error"
	}
	writeJSON(w, status, errorJSON{Error: code, Message: message})
}

func toSlotJSON(s booking.Slot) slotJSON {
	return slotJSON{ID: s.ID, StartsAt: s.StartsAt, EndsAt: s.EndsAt, Available: s.Available}
}
