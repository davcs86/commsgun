package admin

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/davcs86/commsgun/api/internal/clock"
	"github.com/davcs86/commsgun/api/internal/config"
	"github.com/davcs86/commsgun/api/internal/model"
	"github.com/davcs86/commsgun/api/internal/store"
)

const token = "0123456789abcdef0123456789abcdef-token"

var now = time.Date(2026, 11, 13, 12, 0, 30, 0, time.UTC)

func setup(t *testing.T) (http.Handler, *store.Store) {
	t.Helper()
	st, err := store.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	cfg := &config.Config{AdminAPIToken: token, AuthMode: config.AuthLocal, Providers: map[model.Channel]*config.ProviderCfg{
		model.ChannelEmail: {Name: "resend", PerMinute: 60, PerDay: 100},
	}}
	return withSession(t, NewHandler(st, cfg, clock.NewFake(now), slog.New(slog.NewJSONHandler(io.Discard, nil)), &fakeHasher{})), st
}

// sessionHandler sends its session on every request that carries none.
type sessionHandler struct {
	h     http.Handler
	token string
}

func (s sessionHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get(HeaderSession) == "" {
		r.Header.Set(HeaderSession, s.token)
	}
	s.h.ServeHTTP(w, r)
}

// withSession creates a user and a session through the API (import, then sessions/local) and wraps h with it.
func withSession(t *testing.T, h http.Handler) http.Handler {
	t.Helper()
	post := func(path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"username":"admin","password":"`+acctPass+`"}`))
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	if rec := post("/admin/v1/bootstrap/import"); rec.Code != http.StatusOK {
		t.Fatalf("import: %d %s", rec.Code, rec.Body)
	}
	rec := post("/admin/v1/sessions/local")
	if rec.Code != http.StatusCreated {
		t.Fatalf("sessions/local: %d %s", rec.Code, rec.Body)
	}
	return sessionHandler{h, sessionToken(t, rec)}
}

func do(h http.Handler, method, path, auth string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

var routes = []string{
	"/admin/v1/stats", "/admin/v1/messages", "/admin/v1/messages/abc", "/admin/v1/facets", "/admin/v1/overview", "/admin/v1/nope",
}

func TestAdminAuth(t *testing.T) {
	h, _ := setup(t)
	for _, p := range routes {
		for _, auth := range []string{"", "Bearer wrong", "Bearer " + token + "x", token, "Basic " + token} {
			rec := do(h, http.MethodGet, p, auth)
			if rec.Code != 401 || strings.TrimSpace(rec.Body.String()) != `{"error":"unauthorized"}` {
				t.Errorf("%s auth=%q: %d %s", p, auth, rec.Code, rec.Body)
			}
		}
		if rec := do(h, http.MethodPost, p, ""); rec.Code != 401 {
			t.Errorf("%s unauthenticated POST = %d", p, rec.Code)
		}
	}
	if rec := do(h, http.MethodGet, "/admin/v1/facets", "Bearer "+token); rec.Code != 200 {
		t.Fatalf("valid token = %d %s", rec.Code, rec.Body)
	}
}

func TestAdminOnlyServesAdminPaths(t *testing.T) {
	h, _ := setup(t)
	for _, p := range []string{"/", "/healthz", "/v1/messages"} {
		rec := do(h, http.MethodGet, p, "")
		if rec.Code != 404 || rec.Header().Get("Content-Type") != "application/json" {
			t.Errorf("%s = %d %s", p, rec.Code, rec.Body)
		}
	}
	if rec := do(h, http.MethodGet, "/admin/v1/nope", "Bearer "+token); rec.Code != 404 {
		t.Errorf("unknown admin route = %d", rec.Code)
	}
}

func TestAdminRejectsNonGET(t *testing.T) {
	h, _ := setup(t)
	for _, m := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch} {
		rec := do(h, m, "/admin/v1/stats", "Bearer "+token)
		if rec.Code != 405 || rec.Header().Get("Allow") != "GET" {
			t.Errorf("%s = %d allow=%q", m, rec.Code, rec.Header().Get("Allow"))
		}
	}
}

func TestAdminBadParams(t *testing.T) {
	h, _ := setup(t)
	long := strings.Repeat("a", 201)
	cases := []struct{ q, code, field string }{
		{"from=2026-11-13T12:00:00Z&to=2026-11-13T11:00:00Z", "invalid_range", "from"},
		{"from=2026-11-13T12:00:00Z&to=2026-11-13T12:00:00Z", "invalid_range", "from"},
		{"from=yesterday", "invalid_instant", "from"},
		{"to=nope", "invalid_instant", "to"},
		{"from=2026-10-01T00:00:00Z&to=2026-11-13T00:00:00Z", "range_too_long", "from"},
		{"channel=fax", "invalid_channel", "channel"},
		{"state=lost", "invalid_state", "state"},
		{"recipient=" + long, "filter_too_long", "recipient"},
		{"sender=" + long, "filter_too_long", "sender"},
		{"app=" + long, "filter_too_long", "app"},
		{"error_type=" + long, "filter_too_long", "error_type"},
	}
	for _, p := range []string{"/admin/v1/stats", "/admin/v1/messages"} {
		for _, c := range cases {
			rec := do(h, http.MethodGet, p+"?"+c.q, "Bearer "+token)
			var body map[string]string
			_ = json.Unmarshal(rec.Body.Bytes(), &body)
			if rec.Code != 400 || body["field"] != c.field || body["error"] != c.code || len(body) != 2 {
				t.Errorf("%s?%.60s = %d %s", p, c.q, rec.Code, rec.Body)
			}
		}
	}
	both := "/admin/v1/stats?sender=" + long + "&recipient=" + long + "&app=" + long
	for i := 0; i < 20; i++ {
		if rec := do(h, http.MethodGet, both, "Bearer "+token); !strings.Contains(rec.Body.String(), `"field":"app"`) {
			t.Fatalf("field not deterministic: %s", rec.Body)
		}
	}
	rec := do(h, http.MethodGet, "/admin/v1/messages?cursor=@@", "Bearer "+token)
	if rec.Code != 400 || strings.TrimSpace(rec.Body.String()) != `{"error":"invalid_cursor","field":"cursor"}` {
		t.Errorf("bad cursor = %d %s", rec.Code, rec.Body)
	}
	ok := do(h, http.MethodGet, "/admin/v1/stats?from=2026-10-13T12:00:00Z&to=2026-11-13T12:00:00Z", "Bearer "+token)
	if ok.Code != 200 {
		t.Errorf("exactly 31 days = %d %s", ok.Code, ok.Body)
	}
}

func seed(t *testing.T, st *store.Store) {
	t.Helper()
	in := store.EnqueueInput{
		App: "app1", Channel: model.ChannelEmail, IdempotencyKey: "k", BodyHash: sha256.Sum256([]byte("b")),
		Recipients: []string{"a@x.com"}, Sender: "s@x.com", Subject: "SECRET-SUBJECT", Text: "SECRET-BODY",
		ExpiresAt: now.Add(time.Hour), PerMinute: 10, PerDay: 10, Provider: "resend", SourceIP: "192.0.2.9",
	}
	if _, err := st.Enqueue(context.Background(), now.Add(-time.Minute), in); err != nil {
		t.Fatal(err)
	}
}

func TestAdminEndpointsShape(t *testing.T) {
	h, st := setup(t)
	seed(t, st)
	get := func(p string) map[string]any {
		rec := do(h, http.MethodGet, p, "Bearer "+token)
		if rec.Code != 200 || rec.Header().Get("Content-Type") != "application/json" {
			t.Fatalf("%s = %d %s", p, rec.Code, rec.Body)
		}
		if strings.Contains(rec.Body.String(), "SECRET") {
			t.Fatalf("%s leaks body: %s", p, rec.Body)
		}
		var out map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}

	stats := get("/admin/v1/stats")
	for _, k := range []string{"from", "to", "bucket_seconds", "totals", "series", "by_error_type", "by_app", "by_channel"} {
		if _, ok := stats[k]; !ok {
			t.Errorf("stats missing %q", k)
		}
	}
	if stats["to"] != "2026-11-13T12:01:00Z" || stats["from"] != "2026-11-13T11:01:00Z" {
		t.Errorf("default range from=%v to=%v", stats["from"], stats["to"])
	}
	if stats["totals"].(map[string]any)["enqueued"].(float64) != 1 {
		t.Errorf("totals %v", stats["totals"])
	}

	list := get("/admin/v1/messages")
	items := list["items"].([]any)
	if len(items) != 1 || list["next_cursor"] != nil {
		t.Fatalf("list %v", list)
	}
	id := items[0].(map[string]any)["id"].(string)
	if ip := items[0].(map[string]any)["source_ip"]; ip != "192.0.2.9" {
		t.Fatalf("list source_ip = %v", ip)
	}

	one := get("/admin/v1/messages/" + id)
	if m := one["message"].(map[string]any); m["id"] != id || m["source_ip"] != "192.0.2.9" || len(one["attempts"].([]any)) != 0 {
		t.Fatalf("detail %v", one)
	}
	if rec := do(h, http.MethodGet, "/admin/v1/messages/unknown", "Bearer "+token); rec.Code != 404 || !strings.Contains(rec.Body.String(), `"error"`) {
		t.Fatalf("unknown id = %d %s", rec.Code, rec.Body)
	}

	if f := get("/admin/v1/facets"); len(f["apps"].([]any)) != 1 || len(f["channels"].([]any)) != 2 {
		t.Fatalf("facets %v", f)
	}
	ov := get("/admin/v1/overview")
	if ov["queue_depth"].(float64) != 1 || len(ov["providers"].([]any)) != 2 {
		t.Fatalf("overview %v", ov)
	}
}

func TestAdminFailBusyAndInternal(t *testing.T) {
	var logs strings.Builder
	h := &handler{log: slog.New(slog.NewJSONHandler(&logs, nil))}
	req := httptest.NewRequest(http.MethodGet, "/admin/v1/stats", nil)

	rec := httptest.NewRecorder()
	h.fail(rec, req, fmt.Errorf("wrapped: %w", store.ErrBusy))
	if rec.Code != 503 || rec.Header().Get("Retry-After") != "1" || strings.TrimSpace(rec.Body.String()) != `{"error":"unavailable"}` {
		t.Fatalf("busy = %d retry-after=%q %s", rec.Code, rec.Header().Get("Retry-After"), rec.Body)
	}

	rec = httptest.NewRecorder()
	h.fail(rec, req, errors.New("boom"))
	var line map[string]any
	if err := json.Unmarshal([]byte(logs.String()), &line); err != nil {
		t.Fatalf("log %q: %v", logs.String(), err)
	}
	if rec.Code != 500 || line["error"] != "boom" || line["err"] != nil {
		t.Fatalf("internal = %d log=%v", rec.Code, line)
	}
}
