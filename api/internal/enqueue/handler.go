package enqueue

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"math"
	"net"
	"net/http"
	"net/netip"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/davcs86/commsgun/api/internal/clock"
	"github.com/davcs86/commsgun/api/internal/config"
	"github.com/davcs86/commsgun/api/internal/constants"
	"github.com/davcs86/commsgun/api/internal/store"
)

const (
	headerApp         = "X-Commsgun-App"
	headerKey         = "Idempotency-Key"
	bearerPrefix      = "bearer "
	contentTypeJSON   = "application/json"
	errUnauthorized   = "unauthorized"
	errUnavailable    = "unavailable"
	errNotAllowed     = "channel_not_allowed"
	errIdemConflict   = "idempotency_conflict"
	errRateLimited    = "rate_limited"
	errInternal       = "internal"
	errInvalidKey     = "invalid_idempotency_key"
	errKeyRequired    = "idempotency_key_required"
	errUnreadableBody = "invalid_json"
)

var keyPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]+$`)

// Enqueuer is the one store method the handler needs.
type Enqueuer interface {
	Enqueue(ctx context.Context, now time.Time, in store.EnqueueInput) (store.EnqueueResult, error)
	LookupRequest(ctx context.Context, app, idempotencyKey string) (hash [32]byte, res store.EnqueueResult, found bool, err error)
}

type handler struct {
	cfg  *config.Config
	st   Enqueuer
	clk  clock.Clock
	log  *slog.Logger
	gate chan struct{}
}

// NewHandler serves exactly POST /v1/messages.
func NewHandler(cfg *config.Config, st Enqueuer, clk clock.Clock, log *slog.Logger) http.Handler {
	return &handler{cfg: cfg, st: st, clk: clk, log: log, gate: make(chan struct{}, constants.EnqueueMaxConcurrent)}
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

// logInfo holds the only request facts that may reach the log.
type logInfo struct {
	app, channel, requestID string
	recipients              int
}

func (h *handler) ServeHTTP(rw http.ResponseWriter, r *http.Request) {
	start := h.clk.Now()
	w := &statusWriter{ResponseWriter: rw, status: http.StatusOK}
	var info logInfo
	select {
	case h.gate <- struct{}{}:
		defer func() { <-h.gate }()
		h.serve(w, r, start, &info)
	default:
		unavailable(w)
	}
	h.log.Info("enqueue", "status", w.status, "app", info.app, "channel", info.channel,
		"recipients", info.recipients, "request_id", info.requestID,
		"duration_ms", h.clk.Now().Sub(start).Milliseconds())
}

func (h *handler) serve(w *statusWriter, r *http.Request, now time.Time, info *logInfo) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, constants.MaxBodyBytes+1))
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": errUnreadableBody})
		return
	}
	if len(body) > constants.MaxBodyBytes {
		w.WriteHeader(http.StatusRequestEntityTooLarge)
		return
	}

	appID := r.Header.Get(headerApp)
	app, ok := h.cfg.Authenticate(appID, bearer(r))
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": errUnauthorized})
		return
	}
	info.app = app.ID

	key := r.Header.Get(headerKey)
	switch {
	case key == "":
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": errKeyRequired, "field": headerKey})
		return
	case len(key) > constants.IdempotencyKeyMaxLen || !keyPattern.MatchString(key):
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": errInvalidKey, "field": headerKey})
		return
	}

	req, hash, verr := parse(body)
	if verr != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": verr.Message, "field": verr.Field})
		return
	}

	prev, res, found, err := h.st.LookupRequest(r.Context(), app.ID, key)
	if err != nil {
		h.storeError(w, err)
		return
	}
	if found {
		if prev != hash {
			writeJSON(w, http.StatusConflict, map[string]string{"error": errIdemConflict})
			return
		}
		info.requestID = res.RequestID
		writeAccepted(w, res)
		return
	}

	in, verr := validateRequest(app, &req, hash, now)
	if verr != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": verr.Message, "field": verr.Field})
		return
	}
	info.channel, info.recipients = string(in.Channel), len(in.Recipients)
	cc, enabled := app.Channels[in.Channel]
	if !enabled {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": errNotAllowed})
		return
	}

	in.IdempotencyKey = key
	in.SourceIP = sourceIP(r)
	in.PerMinute, in.PerDay = cc.PerMinute, cc.PerDay
	if p := h.cfg.Providers[in.Channel]; p != nil {
		in.Provider = p.Name
	} else {
		in.Held = true
	}

	res, err = h.st.Enqueue(r.Context(), now, *in)
	if err != nil {
		h.storeError(w, err)
		return
	}
	info.requestID = res.RequestID
	writeAccepted(w, res)
}

func writeAccepted(w http.ResponseWriter, res store.EnqueueResult) {
	status := http.StatusAccepted
	if res.Duplicate {
		status = http.StatusOK
	}
	ids := res.MessageIDs
	if ids == nil {
		ids = []string{}
	}
	writeJSON(w, status, struct {
		RequestID  string   `json:"request_id"`
		MessageIDs []string `json:"message_ids"`
		Duplicate  bool     `json:"duplicate"`
	}{res.RequestID, ids, res.Duplicate})
}

func (h *handler) storeError(w http.ResponseWriter, err error) {
	var rl *store.RateLimitedError
	switch {
	case errors.Is(err, store.ErrIdempotencyConflict):
		writeJSON(w, http.StatusConflict, map[string]string{"error": errIdemConflict})
	case errors.As(err, &rl):
		w.Header().Set("Retry-After", retryAfterSeconds(rl.RetryAfter))
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": errRateLimited, "window": rl.Window})
	case errors.Is(err, store.ErrQueueFull), errors.Is(err, store.ErrBusy):
		unavailable(w)
	default:
		h.log.Error("enqueue store failure")
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": errInternal})
	}
}

// retryAfterSeconds rounds up and never advertises less than RetryAfterBusy.
func retryAfterSeconds(d time.Duration) string {
	d = max(d, constants.RetryAfterBusy)
	return strconv.Itoa(int(math.Ceil(d.Seconds())))
}

func unavailable(w http.ResponseWriter) {
	w.Header().Set("Retry-After", retryAfterSeconds(constants.RetryAfterBusy))
	writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": errUnavailable})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", contentTypeJSON)
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func bearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if len(h) >= len(bearerPrefix) && strings.EqualFold(h[:len(bearerPrefix)], bearerPrefix) {
		return h[len(bearerPrefix):]
	}
	return ""
}

func sourceIP(r *http.Request) string {
	first, _, _ := strings.Cut(r.Header.Get("X-Forwarded-For"), ",")
	if a, err := netip.ParseAddr(strings.TrimSpace(first)); err == nil {
		return a.String()
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
