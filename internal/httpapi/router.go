// Package httpapi exposes the slotguard HTTP endpoints.
package httpapi

import (
	"encoding/json"
	"net/http"
)

// NewRouter returns the HTTP handler with every route registered.
func NewRouter() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", health)
	return mux
}

func health(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}
