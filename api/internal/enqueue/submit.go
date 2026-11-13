package enqueue

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"github.com/davcs86/commsgun/api/internal/config"
	"github.com/davcs86/commsgun/api/internal/constants"
	"github.com/davcs86/commsgun/api/internal/model"
	"github.com/davcs86/commsgun/api/internal/store"
)

// Outcome's Channel and Recipients stay zero for an idempotent repeat; Held is always set.
type Outcome struct {
	Result     store.EnqueueResult
	Channel    model.Channel
	Recipients int
	Held       bool
}

// Failure is rendered by the caller; Code is the JSON "error" value and never carries request data.
type Failure struct {
	Status     int
	Code       string
	Field      string
	Window     string
	RetryAfter time.Duration
}

func unprocessable(code, field string) *Failure {
	return &Failure{Status: http.StatusUnprocessableEntity, Code: code, Field: field}
}

// Submit is the only path into the queue: idempotency, validation, channel check and limits.
func Submit(ctx context.Context, cfg *config.Config, st Enqueuer, now time.Time,
	app *config.App, key string, body []byte, sourceIP string) (Outcome, *Failure) {
	var out Outcome
	switch {
	case key == "":
		return out, unprocessable(errKeyRequired, headerKey)
	case len(key) > constants.IdempotencyKeyMaxLen || !keyPattern.MatchString(key):
		return out, unprocessable(errInvalidKey, headerKey)
	}

	req, hash, verr := parse(body)
	if verr != nil {
		return out, unprocessable(verr.Message, verr.Field)
	}
	out.Held = cfg.Providers[model.Channel(req.Channel)] == nil

	prev, res, found, err := st.LookupRequest(ctx, app.ID, key)
	if err != nil {
		return out, storeFailure(err)
	}
	if found {
		if prev != hash {
			return out, &Failure{Status: http.StatusConflict, Code: errIdemConflict}
		}
		out.Result = res
		return out, nil
	}

	in, verr := validateRequest(app, &req, hash, now)
	if verr != nil {
		return out, unprocessable(verr.Message, verr.Field)
	}
	out.Channel, out.Recipients = in.Channel, len(in.Recipients)
	cc, enabled := app.Channels[in.Channel]
	if !enabled {
		return out, &Failure{Status: http.StatusForbidden, Code: errNotAllowed}
	}

	in.IdempotencyKey = key
	in.SourceIP = sourceIP
	in.PerMinute, in.PerDay = cc.PerMinute, cc.PerDay
	if p := cfg.Providers[in.Channel]; p != nil {
		in.Provider = p.Name
	} else {
		in.Held = true
	}

	if out.Result, err = st.Enqueue(ctx, now, *in); err != nil {
		return out, storeFailure(err)
	}
	return out, nil
}

func storeFailure(err error) *Failure {
	var rl *store.RateLimitedError
	switch {
	case errors.Is(err, store.ErrIdempotencyConflict):
		return &Failure{Status: http.StatusConflict, Code: errIdemConflict}
	case errors.As(err, &rl):
		return &Failure{Status: http.StatusTooManyRequests, Code: errRateLimited, Window: rl.Window, RetryAfter: rl.RetryAfter}
	case errors.Is(err, store.ErrQueueFull), errors.Is(err, store.ErrBusy):
		return &Failure{Status: http.StatusServiceUnavailable, Code: errUnavailable, RetryAfter: constants.RetryAfterBusy}
	default:
		return &Failure{Status: http.StatusInternalServerError, Code: errInternal}
	}
}

// SourceIP prefers the first X-Forwarded-For address, which the reverse proxy sets.
func SourceIP(r *http.Request) string {
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

// WriteFailure renders f the same way on every listener.
func WriteFailure(w http.ResponseWriter, f *Failure) {
	body := map[string]string{"error": f.Code}
	switch f.Status {
	case http.StatusUnprocessableEntity:
		body["field"] = f.Field
	case http.StatusTooManyRequests:
		body["window"] = f.Window
		w.Header().Set("Retry-After", retryAfterSeconds(f.RetryAfter))
	case http.StatusServiceUnavailable:
		w.Header().Set("Retry-After", retryAfterSeconds(f.RetryAfter))
	}
	writeJSON(w, f.Status, body)
}
