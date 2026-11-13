package admin

import (
	"context"
	"crypto/sha256"
	"encoding/json"
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
	cfg := &config.Config{AdminAPIToken: token, Providers: map[model.Channel]*config.ProviderCfg{
		model.ChannelEmail: {Name: "resend", PerMinute: 60, PerDay: 100},
	}}
	return NewHandler(st, cfg, clock.NewFake(now), slog.New(slog.NewJSONHandler(io.Discard, nil))), st
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
	cases := []struct{ q, field string }{
		{"from=2026-11-13T12:00:00Z&to=2026-11-13T11:00:00Z", "from"},
		{"from=2026-11-13T12:00:00Z&to=2026-11-13T12:00:00Z", "from"},
		{"from=yesterday", "from"},
		{"to=nope", "to"},
		{"from=2026-10-01T00:00:00Z&to=2026-11-13T00:00:00Z", "from"},
		{"channel=fax", "channel"},
		{"state=lost", "state"},
		{"recipient=" + long, "recipient"},
		{"sender=" + long, "sender"},
		{"app=" + long, "app"},
		{"error_type=" + long, "error_type"},
	}
	for _, p := range []string{"/admin/v1/stats", "/admin/v1/messages"} {
		for _, c := range cases {
			rec := do(h, http.MethodGet, p+"?"+c.q, "Bearer "+token)
			var body map[string]string
			_ = json.Unmarshal(rec.Body.Bytes(), &body)
			if rec.Code != 400 || body["field"] != c.field || body["error"] == "" {
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
	if rec.Code != 400 || !strings.Contains(rec.Body.String(), `"field":"cursor"`) {
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
		ExpiresAt: now.Add(time.Hour), PerMinute: 10, PerDay: 10, Provider: "resend",
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

	one := get("/admin/v1/messages/" + id)
	if one["message"].(map[string]any)["id"] != id || len(one["attempts"].([]any)) != 0 {
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
