// Package admin serves the read-only, bearer-protected admin API under /admin/v1.
package admin

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/davcs86/commsgun/api/internal/clock"
	"github.com/davcs86/commsgun/api/internal/config"
	"github.com/davcs86/commsgun/api/internal/constants"
	"github.com/davcs86/commsgun/api/internal/model"
	"github.com/davcs86/commsgun/api/internal/store"
)

const (
	prefix     = "/admin/v1/"
	bearer     = "Bearer "
	errUnauth  = "unauthorized"
	errMissing = "not_found"
)

type handler struct {
	st    *store.Store
	token [sha256.Size]byte
	cfg   *config.Config
	clk   clock.Clock
	log   *slog.Logger
}

// NewHandler returns the admin API; it answers only /admin/v1/... and 404s everything else.
func NewHandler(st *store.Store, cfg *config.Config, clk clock.Clock, log *slog.Logger) http.Handler {
	return &handler{st: st, token: sha256.Sum256([]byte(cfg.AdminAPIToken)), cfg: cfg, clk: clk, log: log}
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	rest, ok := strings.CutPrefix(r.URL.Path, prefix)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": errMissing})
		return
	}
	if !h.authorized(r) {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": errUnauth})
		return
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method_not_allowed"})
		return
	}
	switch {
	case rest == "stats":
		h.stats(w, r)
	case rest == "messages":
		h.messages(w, r)
	case strings.HasPrefix(rest, "messages/") && !strings.Contains(rest[len("messages/"):], "/") && len(rest) > len("messages/"):
		h.message(w, r, rest[len("messages/"):])
	case rest == "facets":
		h.facets(w, r)
	case rest == "overview":
		h.overview(w, r)
	default:
		writeJSON(w, http.StatusNotFound, map[string]string{"error": errMissing})
	}
}

func (h *handler) authorized(r *http.Request) bool {
	got, ok := strings.CutPrefix(r.Header.Get("Authorization"), bearer)
	sum := sha256.Sum256([]byte(got))
	return subtle.ConstantTimeCompare(sum[:], h.token[:]) == 1 && ok
}

type paramError struct{ msg, field string }

func (e *paramError) Error() string { return e.msg }

func bad(msg, field string) error { return &paramError{msg, field} }

func (h *handler) filter(r *http.Request) (store.Filter, error) {
	q := r.URL.Query()
	var f store.Filter
	var err error
	f.To = h.clk.Now().UTC()
	if v := q.Get("to"); v != "" {
		if f.To, err = time.Parse(time.RFC3339, v); err != nil {
			return f, bad("to must be an RFC 3339 instant", "to")
		}
		f.To = f.To.UTC()
	}
	f.From = f.To.Add(-constants.AdminDefaultRange)
	if v := q.Get("from"); v != "" {
		if f.From, err = time.Parse(time.RFC3339, v); err != nil {
			return f, bad("from must be an RFC 3339 instant", "from")
		}
		f.From = f.From.UTC()
	}
	if !f.From.Before(f.To) {
		return f, bad("from must be before to", "from")
	}
	if f.To.Sub(f.From) > constants.AdminMaxRange {
		return f, bad("range must not exceed 31 days", "from")
	}
	f.Channel, f.App, f.State = q.Get("channel"), q.Get("app"), q.Get("state")
	f.ErrorType, f.Recipient, f.Sender = q.Get("error_type"), q.Get("recipient"), q.Get("sender")
	if c := model.Channel(f.Channel); f.Channel != "" && c != model.ChannelEmail && c != model.ChannelSMS {
		return f, bad("unknown channel", "channel")
	}
	if f.State != "" && !knownState(model.State(f.State)) {
		return f, bad("unknown state", "state")
	}
	for field, v := range map[string]string{"app": f.App, "error_type": f.ErrorType, "recipient": f.Recipient, "sender": f.Sender} {
		if len(v) > constants.AdminFilterMaxLen {
			return f, bad("filter text too long", field)
		}
	}
	return f, nil
}

func knownState(s model.State) bool {
	switch s {
	case model.StateQueued, model.StateSending, model.StateSent, model.StateDelivered,
		model.StateBounced, model.StateComplained, model.StateFailed, model.StateExpired:
		return true
	}
	return false
}

func (h *handler) stats(w http.ResponseWriter, r *http.Request) {
	f, err := h.filter(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out, err := h.st.Stats(r.Context(), f, h.clk.Now())
	h.respond(w, r, out, err)
}

func (h *handler) messages(w http.ResponseWriter, r *http.Request) {
	f, err := h.filter(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	items, next, err := h.st.ListMessages(r.Context(), f, r.URL.Query().Get("cursor"), constants.AdminPageSize)
	if errors.Is(err, store.ErrBadCursor) {
		err = bad("invalid cursor", "cursor")
	}
	var cursor *string
	if next != "" {
		cursor = &next
	}
	h.respond(w, r, map[string]any{"items": items, "next_cursor": cursor}, err)
}

func (h *handler) message(w http.ResponseWriter, r *http.Request, id string) {
	m, attempts, err := h.st.GetMessage(r.Context(), id)
	h.respond(w, r, map[string]any{"message": m, "attempts": attempts}, err)
}

func (h *handler) facets(w http.ResponseWriter, r *http.Request) {
	out, err := h.st.Facets(r.Context())
	h.respond(w, r, out, err)
}

func (h *handler) overview(w http.ResponseWriter, r *http.Request) {
	out, err := h.st.Overview(r.Context(), h.clk.Now(), h.cfg)
	h.respond(w, r, out, err)
}

func (h *handler) respond(w http.ResponseWriter, r *http.Request, v any, err error) {
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (h *handler) fail(w http.ResponseWriter, r *http.Request, err error) {
	var pe *paramError
	switch {
	case errors.As(err, &pe):
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": pe.msg, "field": pe.field})
	case errors.Is(err, store.ErrNotFound):
		writeJSON(w, http.StatusNotFound, map[string]string{"error": errMissing})
	case errors.Is(err, store.ErrBusy):
		w.Header().Set("Retry-After", strconv.Itoa(int(constants.RetryAfterBusy/time.Second)))
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "unavailable"})
	default:
		h.log.Error("admin query failed", "path", r.URL.Path, "err", err.Error())
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal"})
	}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
