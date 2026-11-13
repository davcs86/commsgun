package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/davcs86/commsgun/api/internal/clock"
	"github.com/davcs86/commsgun/api/internal/config"
	"github.com/davcs86/commsgun/api/internal/enqueue"
	"github.com/davcs86/commsgun/api/internal/model"
	"github.com/davcs86/commsgun/api/internal/store"
)

const (
	probePath  = "/admin/v1/probes"
	probeEmail = `{"channel":"email","to":"me@example.com","provider":"resend"}`
	probeSMS   = `{"channel":"sms","to":"+15551234567","provider":null}`
	opsSender  = "Ops <ops@example.com>"
)

type probeEnv struct {
	h   http.Handler
	st  *store.Store
	log *bytes.Buffer
}

func probeConfig(withSMS bool) *config.Config {
	snd := config.Sender{Raw: opsSender, Address: "ops@example.com"}
	chs := map[model.Channel]config.ChannelCfg{
		model.ChannelEmail: {PerMinute: 2, PerDay: 5, Senders: []config.Sender{snd}, DefaultSender: &snd},
	}
	if withSMS {
		chs[model.ChannelSMS] = config.ChannelCfg{PerMinute: 5, PerDay: 50}
	}
	return &config.Config{
		AdminAPIToken: token,
		Apps:          map[string]config.App{"ops": {ID: "ops", Key: strings.Repeat("k", 32), Channels: chs}},
		Providers:     map[model.Channel]*config.ProviderCfg{model.ChannelEmail: {Name: "resend", PerMinute: 60, PerDay: 100}},
	}
}

func probeSetup(t *testing.T, cfg *config.Config) probeEnv {
	t.Helper()
	st, err := store.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	buf := &bytes.Buffer{}
	return probeEnv{NewHandler(st, cfg, clock.NewFake(now), slog.New(slog.NewJSONHandler(buf, nil))), st, buf}
}

func (e probeEnv) send(method, key, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, probePath, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-Forwarded-For", "203.0.113.7")
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	return rec
}

type probeAccepted struct {
	RequestID  string   `json:"request_id"`
	MessageIDs []string `json:"message_ids"`
	Duplicate  bool     `json:"duplicate"`
	Held       bool     `json:"held"`
}

func accepted(t *testing.T, rec *httptest.ResponseRecorder, code int) probeAccepted {
	t.Helper()
	var out probeAccepted
	if rec.Code != code {
		t.Fatalf("status %d, want %d: %s", rec.Code, code, rec.Body)
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || len(out.MessageIDs) != 1 {
		t.Fatalf("body %s: %v", rec.Body, err)
	}
	return out
}

func compact(t *testing.T, s string) string {
	t.Helper()
	var b bytes.Buffer
	if err := json.Compact(&b, []byte(s)); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

func TestProbeOptions(t *testing.T) {
	e := probeSetup(t, probeConfig(true))
	want := `{"app":"ops","available":true,"channels":[` +
		`{"channel":"email","enabled":true,"provider":"resend","sender":"Ops ` + "\\u003c" + `ops@example.com` + "\\u003e" + `"},` +
		`{"channel":"sms","enabled":true,"provider":null,"sender":null}]}`
	if rec := e.send(http.MethodGet, "", ""); rec.Code != 200 || compact(t, rec.Body.String()) != want {
		t.Errorf("options = %d %s", rec.Code, rec.Body)
	}
	e = probeSetup(t, probeConfig(false))
	if rec := e.send(http.MethodGet, "", ""); !strings.Contains(rec.Body.String(), `{"channel":"sms","enabled":false,"provider":null,"sender":null}`) {
		t.Errorf("sms not enabled = %s", rec.Body)
	}
	cfg := probeConfig(true)
	cfg.Apps = map[string]config.App{}
	e = probeSetup(t, cfg)
	if rec := e.send(http.MethodGet, "", ""); compact(t, rec.Body.String()) != `{"app":"ops","available":false,"channels":[]}` {
		t.Errorf("no ops app = %s", rec.Body)
	}
}

func TestProbeSendEmail(t *testing.T) {
	e := probeSetup(t, probeConfig(true))
	out := accepted(t, e.send(http.MethodPost, "k1", probeEmail), 202)
	if out.Held || out.Duplicate || out.RequestID == "" {
		t.Fatalf("body %+v", out)
	}
	m, _, err := e.st.GetMessage(context.Background(), out.MessageIDs[0])
	if err != nil {
		t.Fatal(err)
	}
	if m.App != "ops" || m.Channel != model.ChannelEmail || m.Recipient != "me@example.com" || m.Sender != opsSender ||
		m.Provider == nil || *m.Provider != "resend" || m.HoldReason != nil ||
		m.BodyBytes != len("commsgun probe")+len("Hello world") {
		t.Errorf("message %+v", m)
	}
}

func TestProbeSendSMSHeld(t *testing.T) {
	e := probeSetup(t, probeConfig(true))
	out := accepted(t, e.send(http.MethodPost, "k1", probeSMS), 202)
	m, _, err := e.st.GetMessage(context.Background(), out.MessageIDs[0])
	if err != nil {
		t.Fatal(err)
	}
	if !out.Held || m.HoldReason == nil || *m.HoldReason != "no_provider" || m.BodyBytes != len("Hello world") {
		t.Errorf("held=%v message %+v", out.Held, m)
	}
}

func TestProbeIdempotent(t *testing.T) {
	e := probeSetup(t, probeConfig(true))
	first := accepted(t, e.send(http.MethodPost, "k1", probeEmail), 202)
	again := accepted(t, e.send(http.MethodPost, "k1", probeEmail), 200)
	if !again.Duplicate || again.MessageIDs[0] != first.MessageIDs[0] || again.Held {
		t.Errorf("repeat %+v, first %+v", again, first)
	}
	items, _, err := e.st.ListMessages(context.Background(), store.Filter{From: now.Add(-3600e9), To: now.Add(3600e9)}, "", 50)
	if err != nil || len(items) != 1 {
		t.Errorf("messages = %d, %v", len(items), err)
	}
	other := strings.Replace(probeEmail, "me@", "you@", 1)
	if rec := e.send(http.MethodPost, "k1", other); rec.Code != 409 || !strings.Contains(rec.Body.String(), "idempotency_conflict") {
		t.Errorf("other body = %d %s", rec.Code, rec.Body)
	}
	held := accepted(t, e.send(http.MethodPost, "k2", probeSMS), 202)
	if rep := accepted(t, e.send(http.MethodPost, "k2", probeSMS), 200); !rep.Held || !held.Held {
		t.Errorf("held repeat %+v", rep)
	}
}

func TestProbeKeyIsNamespaced(t *testing.T) {
	e := probeSetup(t, probeConfig(true))
	accepted(t, e.send(http.MethodPost, "k1", probeEmail), 202)
	for key, want := range map[string]bool{"probe/k1": true, "k1": false, "probe:k1": false} {
		if _, _, found, err := e.st.LookupRequest(context.Background(), "ops", key); err != nil || found != want {
			t.Errorf("lookup %q: found=%v err=%v", key, found, err)
		}
	}
}

func TestProbeKeyCannotCollideWithAnOpsKey(t *testing.T) {
	cfg := probeConfig(true)
	email := cfg.Apps["ops"].Channels[model.ChannelEmail]
	email.PerMinute, email.PerDay = 10, 10
	cfg.Apps["ops"].Channels[model.ChannelEmail] = email
	e := probeSetup(t, cfg)
	app := cfg.Apps["ops"]
	other := []byte(`{"channel":"email","to":["x@example.com"],"subject":"s","text":"t"}`)
	for _, key := range []string{"k1", "probe:k1", "probe.k1"} {
		if _, fail := enqueue.Submit(context.Background(), cfg, e.st, now, &app, key, other, ""); fail != nil {
			t.Fatalf("ops enqueue %q: %+v", key, fail)
		}
	}
	accepted(t, e.send(http.MethodPost, "k1", probeEmail), 202)
}

func TestProbeKeyLengthFollowsEnqueue(t *testing.T) {
	e := probeSetup(t, probeConfig(true))
	accepted(t, e.send(http.MethodPost, strings.Repeat("k", 200), probeEmail), 202)
	if rec := e.send(http.MethodPost, strings.Repeat("k", 201), probeEmail); rec.Code != 422 {
		t.Errorf("201-character key = %d", rec.Code)
	}
}

func TestProbeRejects(t *testing.T) {
	noOps := probeConfig(true)
	noOps.Apps = map[string]config.App{}
	email := func(to string) string {
		b, _ := json.Marshal(map[string]any{"channel": "email", "to": to, "provider": "resend"})
		return string(b)
	}
	for _, tc := range []struct {
		name, key, body string
		cfg             *config.Config
		code            int
		want            string
	}{
		{"too large", "k", `{"channel":"email","to":"` + strings.Repeat("a", 5000) + `"}`, nil, 413, ""},
		{"not json", "k", `{`, nil, 422, `{"error":"invalid_json","field":""}`},
		{"trailing", "k", probeEmail + `{}`, nil, 422, `{"error":"invalid_json","field":""}`},
		{"unknown field", "k", `{"channel":"email","to":"a@b.io","provider":"resend","subject":"x"}`, nil, 422, `{"error":"unknown_field","field":"subject"}`},
		{"wrong type", "k", `{"channel":"email","to":["a@b.io"],"provider":"resend"}`, nil, 422, `{"error":"invalid_json","field":"to"}`},
		{"channel", "k", `{"channel":"fax","to":"a@b.io","provider":null}`, nil, 422, `{"error":"invalid_channel","field":"channel"}`},
		{"no ops app", "k", probeEmail, noOps, 409, `{"error":"probe_app_missing"}`},
		{"provider absent", "k", `{"channel":"email","to":"a@b.io"}`, nil, 422, `{"error":"provider_required","field":"provider"}`},
		{"provider type", "k", `{"channel":"email","to":"a@b.io","provider":7}`, nil, 422, `{"error":"invalid_json","field":"provider"}`},
		{"provider other", "k", `{"channel":"email","to":"a@b.io","provider":"fake"}`, nil, 409, `{"error":"provider_mismatch"}`},
		{"provider null for email", "k", `{"channel":"email","to":"a@b.io","provider":null}`, nil, 409, `{"error":"provider_mismatch"}`},
		{"provider named for sms", "k", `{"channel":"sms","to":"+15551234567","provider":"resend"}`, nil, 409, `{"error":"provider_mismatch"}`},
		{"no key", "", probeEmail, nil, 422, `{"error":"idempotency_key_required","field":"Idempotency-Key"}`},
		{"bad key", "a b", probeEmail, nil, 422, `{"error":"invalid_idempotency_key","field":"Idempotency-Key"}`},
		{"display name", "k", email("Me <me@example.com>"), nil, 422, `{"error":"invalid_recipient","field":"to"}`},
		{"crlf", "k", email("a@b.io\r\nBcc: x@y.io"), nil, 422, `{"error":"invalid_recipient","field":"to"}`},
		{"empty", "k", email(""), nil, 422, `{"error":"invalid_recipient","field":"to"}`},
		{"script", "k", email("<script>alert(1)</script>"), nil, 422, `{"error":"invalid_recipient","field":"to"}`},
		{"not e164", "k", `{"channel":"sms","to":"5551234567","provider":null}`, nil, 422, `{"error":"invalid_recipient","field":"to"}`},
		{"sms off", "k", probeSMS, probeConfig(false), 403, `{"error":"channel_not_allowed"}`},
	} {
		cfg := tc.cfg
		if cfg == nil {
			cfg = probeConfig(true)
		}
		e := probeSetup(t, cfg)
		rec := e.send(http.MethodPost, tc.key, tc.body)
		if rec.Code != tc.code || (tc.want != "" && compact(t, rec.Body.String()) != tc.want) {
			t.Errorf("%s: %d %s", tc.name, rec.Code, rec.Body)
		}
		if items, _, _ := e.st.ListMessages(context.Background(), store.Filter{From: now.Add(-3600e9), To: now.Add(3600e9)}, "", 50); len(items) != 0 {
			t.Errorf("%s: %d messages queued", tc.name, len(items))
		}
	}
}

func TestProbeRateLimited(t *testing.T) {
	e := probeSetup(t, probeConfig(true))
	accepted(t, e.send(http.MethodPost, "k1", probeEmail), 202)
	accepted(t, e.send(http.MethodPost, "k2", probeEmail), 202)
	rec := e.send(http.MethodPost, "k3", probeEmail)
	if rec.Code != 429 || rec.Header().Get("Retry-After") != "30" ||
		compact(t, rec.Body.String()) != `{"error":"rate_limited","window":"minute"}` {
		t.Errorf("third = %d retry-after=%q %s", rec.Code, rec.Header().Get("Retry-After"), rec.Body)
	}
}

func TestProbeLogHasNoDestination(t *testing.T) {
	e := probeSetup(t, probeConfig(true))
	out := accepted(t, e.send(http.MethodPost, "k1", probeEmail), 202)
	e.send(http.MethodPost, "k2", `{"channel":"email","to":"Me <me@example.com>","provider":"resend"}`)
	logged := e.log.String()
	if strings.Count(logged, `"msg":"probe"`) != 2 || !strings.Contains(logged, out.RequestID) ||
		!strings.Contains(logged, `"status":202`) || !strings.Contains(logged, `"status":422`) {
		t.Errorf("log lines: %s", logged)
	}
	for _, banned := range []string{"me@example.com", "Hello world", "commsgun probe", "203.0.113.7", token} {
		if strings.Contains(logged, banned) {
			t.Errorf("log contains %q: %s", banned, logged)
		}
	}
}

func TestProbeMethods(t *testing.T) {
	e := probeSetup(t, probeConfig(true))
	for _, m := range []string{http.MethodPut, http.MethodDelete, http.MethodPatch} {
		if rec := e.send(m, "k", probeEmail); rec.Code != 405 || rec.Header().Get("Allow") != "GET, POST" {
			t.Errorf("%s = %d allow=%q", m, rec.Code, rec.Header().Get("Allow"))
		}
	}
	for _, p := range []string{"/admin/v1/stats", "/admin/v1/messages", "/admin/v1/facets", "/admin/v1/overview"} {
		if rec := do(e.h, http.MethodPost, p, "Bearer "+token); rec.Code != 405 || rec.Header().Get("Allow") != "GET" {
			t.Errorf("POST %s = %d allow=%q", p, rec.Code, rec.Header().Get("Allow"))
		}
	}
	if rec := do(e.h, http.MethodPost, probePath, ""); rec.Code != 401 {
		t.Errorf("unauthenticated POST = %d", rec.Code)
	}
	if rec := do(e.h, http.MethodGet, probePath+"/x", "Bearer "+token); rec.Code != 404 {
		t.Errorf("subpath = %d", rec.Code)
	}
}
