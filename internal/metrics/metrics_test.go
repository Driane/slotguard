package metrics_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Driane/slotguard/internal/metrics"
)

func TestMiddlewareAndHandler(t *testing.T) {
	m := metrics.New()
	m.CounterFunc("slotguard_test_things_total", "Things.", func() float64 { return 7 })
	m.Booking("created")
	m.Booking("slot_taken")

	mux := http.NewServeMux()
	mux.HandleFunc("GET /slots/{id}", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	mux.Handle("GET /metrics", m.Handler())
	handler := m.Middleware(mux)

	for _, path := range []string{"/slots/a", "/slots/b", "/nowhere"} {
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, path, nil))
	}

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body, _ := io.ReadAll(rec.Body)
	out := string(body)

	for _, want := range []string{
		// Two different slots, one series: the route pattern, not the path.
		`slotguard_http_requests_total{route="GET /slots/{id}",status="404"} 2`,
		`slotguard_http_requests_total{route="unmatched",status="404"} 1`,
		`slotguard_bookings_total{result="created"} 1`,
		`slotguard_bookings_total{result="slot_taken"} 1`,
		`slotguard_test_things_total 7`,
		`slotguard_http_request_duration_seconds_count{route="GET /slots/{id}"} 2`,
		`go_goroutines`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("metrics output does not contain %q", want)
		}
	}
}

func TestNilMetricsIsANoOp(t *testing.T) {
	var m *metrics.Metrics
	m.Booking("created") // must not panic

	called := false
	next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true })
	m.Middleware(next).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	if !called {
		t.Fatal("the nil middleware did not call the next handler")
	}
}
