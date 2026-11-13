// Package health serves the liveness and backlog probes.
package health

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/davcs86/commsgun/api/internal/clock"
	"github.com/davcs86/commsgun/api/internal/config"
	"github.com/davcs86/commsgun/api/internal/constants"
	"github.com/davcs86/commsgun/api/internal/model"
)

type Pinger interface {
	Ping(ctx context.Context) error
	Backlog(ctx context.Context, now time.Time, providers map[model.Channel]*config.ProviderCfg) (time.Time, bool, error)
}

func Handler(st Pinger, lastTick func() time.Time, clk clock.Clock, providers map[model.Channel]*config.ProviderCfg) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		if !getOnly(w, r) {
			return
		}
		switch {
		case st.Ping(r.Context()) != nil:
			write(w, http.StatusServiceUnavailable, map[string]any{"status": "unavailable", "reason": "db"})
		case clk.Now().Sub(lastTick()) > constants.WorkerFresh:
			write(w, http.StatusServiceUnavailable, map[string]any{"status": "unavailable", "reason": "worker"})
		default:
			write(w, http.StatusOK, map[string]any{"status": "ok"})
		}
	})
	mux.HandleFunc("/healthz/backlog", func(w http.ResponseWriter, r *http.Request) {
		if !getOnly(w, r) {
			return
		}
		now := clk.Now()
		oldest, ok, err := st.Backlog(r.Context(), now, providers)
		switch {
		case err != nil:
			write(w, http.StatusServiceUnavailable, map[string]any{"status": "unavailable", "reason": "db"})
		case ok && now.Sub(oldest) > constants.BacklogThreshold:
			write(w, http.StatusServiceUnavailable, map[string]any{"status": "backlog", "oldest_due_seconds": int64(now.Sub(oldest) / time.Second)})
		default:
			write(w, http.StatusOK, map[string]any{"status": "ok"})
		}
	})
	return mux
}

func getOnly(w http.ResponseWriter, r *http.Request) bool {
	if r.Method == http.MethodGet {
		return true
	}
	w.Header().Set("Allow", http.MethodGet)
	write(w, http.StatusMethodNotAllowed, map[string]any{"status": "method_not_allowed"})
	return false
}

func write(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
