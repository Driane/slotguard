package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// call sends one request to router and returns the recorded response.
func call(t *testing.T, router http.Handler, method, path, body string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func decode[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.NewDecoder(rec.Body).Decode(&v); err != nil {
		t.Fatalf("decoding response %q: %v", rec.Body.String(), err)
	}
	return v
}

const slotBody = `{"id":"slot-1","starts_at":"2026-12-01T10:00:00Z","ends_at":"2026-12-01T11:00:00Z"}`

func TestCreateAndGetSlot(t *testing.T) {
	router := newTestRouter()

	rec := call(t, router, http.MethodPost, "/slots", slotBody, nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST /slots status = %d, body %s", rec.Code, rec.Body)
	}

	rec = call(t, router, http.MethodGet, "/slots/slot-1", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /slots/slot-1 status = %d, body %s", rec.Code, rec.Body)
	}
	slot := decode[slotJSON](t, rec)
	if slot.ID != "slot-1" || !slot.Available {
		t.Fatalf("got %+v, want slot-1 available", slot)
	}
}

func TestBookingFlow(t *testing.T) {
	router := newTestRouter()
	if rec := call(t, router, http.MethodPost, "/slots", slotBody, nil); rec.Code != http.StatusCreated {
		t.Fatalf("creating the slot: status %d, body %s", rec.Code, rec.Body)
	}

	aliceKey := map[string]string{headerIdempotencyKey: "key-alice"}
	const aliceBody = `{"slot_id":"slot-1","customer_id":"alice"}`

	// First request books the slot.
	rec := call(t, router, http.MethodPost, "/bookings", aliceBody, aliceKey)
	if rec.Code != http.StatusCreated || rec.Header().Get(headerReplayed) != "" {
		t.Fatalf("first booking: status %d, replayed %q, body %s", rec.Code, rec.Header().Get(headerReplayed), rec.Body)
	}
	first := decode[bookingJSON](t, rec)

	// A retry with the same key returns the same booking.
	rec = call(t, router, http.MethodPost, "/bookings", aliceBody, aliceKey)
	if rec.Code != http.StatusCreated || rec.Header().Get(headerReplayed) != "true" {
		t.Fatalf("retry: status %d, replayed %q, body %s", rec.Code, rec.Header().Get(headerReplayed), rec.Body)
	}
	if retry := decode[bookingJSON](t, rec); retry.ID != first.ID {
		t.Fatalf("retry returned booking %s, want %s", retry.ID, first.ID)
	}

	// The slot is now unavailable.
	rec = call(t, router, http.MethodGet, "/slots/slot-1", "", nil)
	if slot := decode[slotJSON](t, rec); slot.Available {
		t.Fatal("slot still available after being booked")
	}

	tests := []struct {
		name       string
		body       string
		headers    map[string]string
		wantStatus int
		wantCode   string
	}{
		{
			name:       "another customer gets slot_taken",
			body:       `{"slot_id":"slot-1","customer_id":"bob"}`,
			headers:    map[string]string{headerIdempotencyKey: "key-bob"},
			wantStatus: http.StatusConflict,
			wantCode:   "slot_taken",
		},
		{
			name:       "reusing a key for another request is rejected",
			body:       `{"slot_id":"slot-1","customer_id":"bob"}`,
			headers:    aliceKey,
			wantStatus: http.StatusUnprocessableEntity,
			wantCode:   "idempotency_conflict",
		},
		{
			name:       "missing idempotency key",
			body:       `{"slot_id":"slot-1","customer_id":"carol"}`,
			wantStatus: http.StatusBadRequest,
			wantCode:   "invalid_request",
		},
		{
			name:       "unknown slot",
			body:       `{"slot_id":"nope","customer_id":"carol"}`,
			headers:    map[string]string{headerIdempotencyKey: "key-carol"},
			wantStatus: http.StatusNotFound,
			wantCode:   "slot_not_found",
		},
		{
			name:       "unknown JSON field",
			body:       `{"slot":"slot-1","customer_id":"carol"}`,
			headers:    map[string]string{headerIdempotencyKey: "key-carol-2"},
			wantStatus: http.StatusBadRequest,
			wantCode:   "invalid_request",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := call(t, router, http.MethodPost, "/bookings", tt.body, tt.headers)
			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d (body %s)", rec.Code, tt.wantStatus, rec.Body)
			}
			if got := decode[errorJSON](t, rec); got.Error != tt.wantCode {
				t.Fatalf("error code = %q, want %q", got.Error, tt.wantCode)
			}
		})
	}
}

func TestGetUnknownSlot(t *testing.T) {
	rec := call(t, newTestRouter(), http.MethodGet, "/slots/missing", "", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

func TestCreateSlotTwice(t *testing.T) {
	router := newTestRouter()
	call(t, router, http.MethodPost, "/slots", slotBody, nil)

	rec := call(t, router, http.MethodPost, "/slots", slotBody, nil)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusConflict)
	}
	if got := decode[errorJSON](t, rec); got.Error != "slot_exists" {
		t.Fatalf("error code = %q, want slot_exists", got.Error)
	}
}
