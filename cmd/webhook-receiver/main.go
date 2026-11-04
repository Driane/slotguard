// Command webhook-receiver is a minimal endpoint for slotguard webhooks, useful
// to try the service locally. It shows the two things every receiver must do:
// check the signature, and ignore events it has already processed, because
// delivery is at least once.
//
//	WEBHOOK_SECRET  same key as the API (required)
//	PORT            HTTP port (default 9090)
package main

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/Driane/slotguard/internal/webhook"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	secret := []byte(os.Getenv("WEBHOOK_SECRET"))
	if len(secret) == 0 {
		logger.Error("WEBHOOK_SECRET is required")
		os.Exit(1)
	}
	addr := ":9090"
	if port := os.Getenv("PORT"); port != "" {
		addr = ":" + port
	}

	var (
		mu   sync.Mutex
		seen = map[string]bool{}
	)

	mux := http.NewServeMux()
	mux.HandleFunc("POST /webhooks", func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
		if err != nil {
			http.Error(w, "unreadable body", http.StatusBadRequest)
			return
		}
		if err := webhook.Verify(secret, r.Header.Get(webhook.SignatureHeader), body, time.Now(), 5*time.Minute); err != nil {
			logger.Warn("rejected webhook", "err", err)
			http.Error(w, "invalid signature", http.StatusUnauthorized)
			return
		}

		id := r.Header.Get(webhook.EventIDHeader)
		mu.Lock()
		duplicate := seen[id]
		seen[id] = true
		mu.Unlock()
		if duplicate {
			logger.Info("duplicate event ignored", "event", id)
			w.WriteHeader(http.StatusNoContent)
			return
		}

		var env webhook.Envelope
		if err := json.Unmarshal(body, &env); err != nil {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}
		logger.Info("event received", "event", id, "type", env.Type, "data", string(env.Data))
		w.WriteHeader(http.StatusNoContent)
	})

	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	logger.Info("webhook receiver listening", "addr", addr, "path", "/webhooks")
	if err := srv.ListenAndServe(); err != nil {
		logger.Error("receiver stopped", "err", err)
		os.Exit(1)
	}
}
