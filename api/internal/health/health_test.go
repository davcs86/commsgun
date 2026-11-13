package health

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/davcs86/commsgun/api/internal/clock"
	"github.com/davcs86/commsgun/api/internal/config"
	"github.com/davcs86/commsgun/api/internal/constants"
	"github.com/davcs86/commsgun/api/internal/model"
)

var now = time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)

type fakeStore struct {
	pingErr, backlogErr error
	oldest              time.Time
	ok                  bool
}

func (f *fakeStore) Ping(context.Context) error { return f.pingErr }
func (f *fakeStore) Backlog(context.Context, time.Time, map[model.Channel]*config.ProviderCfg) (time.Time, bool, error) {
	return f.oldest, f.ok, f.backlogErr
}

func get(t *testing.T, st *fakeStore, tick time.Time, method, path string) (int, string, string) {
	t.Helper()
	h := Handler(st, func() time.Time { return tick }, clock.NewFake(now), nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
	return rec.Code, strings.TrimSpace(rec.Body.String()), rec.Header().Get("Content-Type")
}

func TestHealthz(t *testing.T) {
	cases := []struct {
		name string
		st   *fakeStore
		tick time.Time
		code int
		body string
	}{
		{"ok", &fakeStore{}, now.Add(-time.Second), 200, `{"status":"ok"}`},
		{"db down", &fakeStore{pingErr: errors.New("x")}, now, 503, `{"reason":"db","status":"unavailable"}`},
		{"stale worker", &fakeStore{}, now.Add(-constants.WorkerFresh - time.Second), 503, `{"reason":"worker","status":"unavailable"}`},
		{"worker at limit", &fakeStore{}, now.Add(-constants.WorkerFresh), 200, `{"status":"ok"}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			code, body, ct := get(t, c.st, c.tick, "GET", "/healthz")
			if code != c.code || body != c.body || ct != "application/json" {
				t.Fatalf("got %d %s %s", code, body, ct)
			}
		})
	}
}

func TestBacklog(t *testing.T) {
	cases := []struct {
		name string
		st   *fakeStore
		code int
		body string
	}{
		{"14m", &fakeStore{ok: true, oldest: now.Add(-14 * time.Minute)}, 200, `{"status":"ok"}`},
		{"exactly threshold", &fakeStore{ok: true, oldest: now.Add(-constants.BacklogThreshold)}, 200, `{"status":"ok"}`},
		{"16m", &fakeStore{ok: true, oldest: now.Add(-16 * time.Minute)}, 503, `{"oldest_due_seconds":960,"status":"backlog"}`},
		{"none dispatchable", &fakeStore{}, 200, `{"status":"ok"}`},
		{"db error", &fakeStore{backlogErr: errors.New("x")}, 503, `{"reason":"db","status":"unavailable"}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			code, body, _ := get(t, c.st, now, "GET", "/healthz/backlog")
			if code != c.code || body != c.body {
				t.Fatalf("got %d %s", code, body)
			}
		})
	}
}

func TestNonGetRejected(t *testing.T) {
	for _, p := range []string{"/healthz", "/healthz/backlog"} {
		if code, _, _ := get(t, &fakeStore{}, now, http.MethodPost, p); code != http.StatusMethodNotAllowed {
			t.Fatalf("%s POST = %d", p, code)
		}
	}
}
