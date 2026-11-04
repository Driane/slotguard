// Package metrics exposes slotguard's metrics in the Prometheus format, on
// GET /metrics. A nil *Metrics is valid and records nothing, so components
// work the same with or without metrics, in production and in tests.
package metrics

import (
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Metrics holds the service's own registry. Using a dedicated registry
// rather than the global one keeps tests independent from each other.
type Metrics struct {
	registry *prometheus.Registry
	requests *prometheus.CounterVec
	duration *prometheus.HistogramVec
	bookings *prometheus.CounterVec
}

// New returns Metrics with the HTTP and booking metrics registered, plus the
// standard Go runtime and process metrics.
func New() *Metrics {
	m := &Metrics{
		registry: prometheus.NewRegistry(),
		requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "slotguard_http_requests_total",
			Help: "HTTP requests, by route and status code.",
		}, []string{"route", "status"}),
		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "slotguard_http_request_duration_seconds",
			Help:    "Time to answer HTTP requests, by route.",
			Buckets: []float64{.0005, .001, .0025, .005, .01, .025, .05, .1, .25, .5, 1},
		}, []string{"route"}),
		bookings: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "slotguard_bookings_total",
			Help: "Booking attempts, by result: created, replayed, or the error code.",
		}, []string{"result"}),
	}
	m.registry.MustRegister(
		m.requests, m.duration, m.bookings,
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)
	return m
}

// Handler serves the metrics in the Prometheus text format.
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{})
}

// Middleware counts and times every request handled by next, labelled with
// the matched route pattern (for example "POST /bookings"), never the raw
// path: a path like /slots/abc123 would create one series per slot.
func (m *Metrics) Middleware(next http.Handler) http.Handler {
	if m == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)

		route := r.Pattern // set by http.ServeMux once it has matched a route
		if route == "" {
			route = "unmatched"
		}
		m.requests.WithLabelValues(route, strconv.Itoa(rec.status)).Inc()
		m.duration.WithLabelValues(route).Observe(time.Since(start).Seconds())
	})
}

// Booking records the result of one booking attempt.
func (m *Metrics) Booking(result string) {
	if m == nil {
		return
	}
	m.bookings.WithLabelValues(result).Inc()
}

// CounterFunc exposes a counter read from f at every scrape, for components
// that keep their own counters.
func (m *Metrics) CounterFunc(name, help string, f func() float64) {
	m.registry.MustRegister(prometheus.NewCounterFunc(prometheus.CounterOpts{Name: name, Help: help}, f))
}

// GaugeFunc exposes a value read from f at every scrape.
func (m *Metrics) GaugeFunc(name, help string, f func() float64) {
	m.registry.MustRegister(prometheus.NewGaugeFunc(prometheus.GaugeOpts{Name: name, Help: help}, f))
}

// statusRecorder remembers the status code written by a handler. Embedding
// http.ResponseWriter keeps every other method unchanged.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}
