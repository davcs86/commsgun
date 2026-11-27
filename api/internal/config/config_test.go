package config

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/davcs86/commsgun/api/internal/constants"
	"github.com/davcs86/commsgun/api/internal/model"
)

const (
	key1  = "k1-0123456789abcdef0123456789abcdef"
	key2  = "k2-0123456789abcdef0123456789abcdef"
	admin = "admin-0123456789abcdef0123456789ab"
)

type doc = map[string]any

func validDoc() doc {
	return doc{
		"apps": []any{doc{
			"id": "a", "key": key1,
			"channels": doc{
				"email": doc{"per_minute": 10, "per_day": 80,
					"senders":        []any{"Alerts <Alerts@Example.com>"},
					"default_sender": "Alerts <Alerts@Example.com>"},
				"sms": doc{"per_minute": 5, "per_day": 50},
			}}},
		"providers": doc{
			"email": doc{"name": "resend", "per_minute": 60, "per_day": 100},
			"sms":   nil,
		},
	}
}

func app0(d doc) doc  { return d["apps"].([]any)[0].(doc) }
func email(d doc) doc { return app0(d)["channels"].(doc)["email"].(doc) }
func prov(d doc, ch string) doc {
	return d["providers"].(doc)[ch].(doc)
}

type env map[string]string

func baseEnv() env {
	return env{"RESEND_API_KEY": "re_secret_value_123", "ADMIN_API_TOKEN": admin}
}

func load(t *testing.T, d any, e env) (*Config, error) {
	t.Helper()
	b, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	e2 := env{}
	for k, v := range e {
		e2[k] = v
	}
	e2["COMMSGUN_CONFIG"] = string(b)
	return Load(func(k string) string { return e2[k] })
}

func TestLoadRejects(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(d doc, e env)
		raw    string // when set, used instead of the document
		field  string // text the error must contain
	}{
		{"duplicate app id", func(d doc, e env) {
			second := doc{"id": "a", "key": key2, "channels": doc{"sms": doc{"per_minute": 1, "per_day": 1}}}
			d["apps"] = append(d["apps"].([]any), second)
		}, "", "apps[1].id"},
		{"app id bad pattern", func(d doc, e env) { app0(d)["id"] = "Bad_ID" }, "", "apps[0].id"},
		{"app id empty", func(d doc, e env) { app0(d)["id"] = "" }, "", "apps[0].id"},
		{"app id too long", func(d doc, e env) { app0(d)["id"] = strings.Repeat("a", 33) }, "", "apps[0].id"},
		{"key of 31 chars", func(d doc, e env) { app0(d)["key"] = key1[:31] }, "", "apps[0].key"},
		{"default sender not in senders", func(d doc, e env) {
			email(d)["default_sender"] = "Other <other@example.com>"
		}, "", "default_sender"},
		{"email enabled with no senders", func(d doc, e env) {
			email(d)["senders"] = []any{}
			delete(email(d), "default_sender")
		}, "", "senders"},
		{"sender not an address", func(d doc, e env) {
			email(d)["senders"] = []any{"not an address"}
			delete(email(d), "default_sender")
		}, "", "senders[0]"},
		{"zero per_day", func(d doc, e env) { email(d)["per_day"] = 0 }, "", "per_day"},
		{"negative per_minute", func(d doc, e env) { email(d)["per_minute"] = -1 }, "", "per_minute"},
		{"unknown app channel", func(d doc, e env) {
			app0(d)["channels"].(doc)["fax"] = doc{"per_minute": 1, "per_day": 1}
		}, "", "channels.fax"},
		{"unknown provider name", func(d doc, e env) { prov(d, "email")["name"] = "smtp" }, "", "providers.email.name"},
		{"resend for sms", func(d doc, e env) {
			d["providers"].(doc)["sms"] = doc{"name": "resend", "per_minute": 1, "per_day": 1}
		}, "", "providers.sms.name"},
		{"provider limit zero", func(d doc, e env) { prov(d, "email")["per_minute"] = 0 }, "", "providers.email.per_minute"},
		{"unknown provider channel", func(d doc, e env) {
			d["providers"].(doc)["fax"] = doc{"name": "fake", "per_minute": 1, "per_day": 1}
		}, "", "providers.fax"},
		{"resend without RESEND_API_KEY", func(d doc, e env) { delete(e, "RESEND_API_KEY") }, "", "RESEND_API_KEY"},
		{"fake without allow flag", func(d doc, e env) { prov(d, "email")["name"] = "fake" }, "", "COMMSGUN_ALLOW_FAKE"},
		{"fake with flag not 1", func(d doc, e env) {
			prov(d, "email")["name"] = "fake"
			e["COMMSGUN_ALLOW_FAKE"] = "true"
		}, "", "COMMSGUN_ALLOW_FAKE"},
		{"ADMIN_API_TOKEN of 31 chars", func(d doc, e env) { e["ADMIN_API_TOKEN"] = admin[:31] }, "", "ADMIN_API_TOKEN"},
		{"ADMIN_API_TOKEN missing", func(d doc, e env) { delete(e, "ADMIN_API_TOKEN") }, "", "ADMIN_API_TOKEN"},
		{"no apps", func(d doc, e env) { d["apps"] = []any{} }, "", "apps"},
		{"invalid JSON", nil, `{"apps": [`, "COMMSGUN_CONFIG"},
		{"empty config", nil, ``, "COMMSGUN_CONFIG"},
		{"unknown field", func(d doc, e env) { app0(d)["secret_extra"] = 1 }, "", "COMMSGUN_CONFIG"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := baseEnv()
			var err error
			if tc.mutate == nil {
				e["COMMSGUN_CONFIG"] = tc.raw
				_, err = Load(func(k string) string { return e[k] })
			} else {
				d := validDoc()
				tc.mutate(d, e)
				_, err = load(t, d, e)
			}
			if err == nil {
				t.Fatal("Load succeeded, want error")
			}
			msg := err.Error()
			if !strings.Contains(msg, tc.field) {
				t.Errorf("error %q does not name %q", msg, tc.field)
			}
			for _, secret := range []string{key1, key2, admin, "re_secret_value_123", key1[:31], admin[:31]} {
				if strings.Contains(msg, secret) {
					t.Errorf("error leaks a secret value: %q", msg)
				}
			}
		})
	}
}

func TestLoadValid(t *testing.T) {
	c, err := load(t, validDoc(), baseEnv())
	if err != nil {
		t.Fatal(err)
	}
	if c.DataDir != "/data" {
		t.Errorf("DataDir = %q, want /data", c.DataDir)
	}
	if c.Providers[model.ChannelSMS] != nil {
		t.Errorf("sms provider = %+v, want nil", c.Providers[model.ChannelSMS])
	}
	if p := c.Providers[model.ChannelEmail]; p == nil || p.Name != "resend" || p.PerMinute != 60 || p.PerDay != 100 {
		t.Errorf("email provider = %+v", p)
	}
	if c.ResendAPIKey != "re_secret_value_123" || c.AdminAPIToken != admin || c.AllowFake {
		t.Errorf("env fields wrong: %+v", c)
	}
	a := c.Apps["a"]
	em := a.Channels[model.ChannelEmail]
	if em.PerMinute != 10 || em.PerDay != 80 || len(em.Senders) != 1 {
		t.Fatalf("email channel = %+v", em)
	}
	s := em.Senders[0]
	if s.Raw != "Alerts <Alerts@Example.com>" || s.Address != "alerts@example.com" {
		t.Errorf("sender = %+v", s)
	}
	if em.DefaultSender == nil || em.DefaultSender.Address != "alerts@example.com" {
		t.Errorf("default sender = %+v", em.DefaultSender)
	}
	if sms := a.Channels[model.ChannelSMS]; sms.PerMinute != 5 || sms.PerDay != 50 {
		t.Errorf("sms channel = %+v", sms)
	}
}

func TestLoadDataDirAndFake(t *testing.T) {
	d := validDoc()
	prov(d, "email")["name"] = "fake"
	d["providers"].(doc)["sms"] = doc{"name": "fake", "per_minute": 1, "per_day": 1}
	e := env{"ADMIN_API_TOKEN": admin, "COMMSGUN_ALLOW_FAKE": "1", "DATA_DIR": "/tmp/x"}
	c, err := load(t, d, e)
	if err != nil {
		t.Fatal(err)
	}
	if !c.AllowFake || c.DataDir != "/tmp/x" || c.Providers[model.ChannelSMS].Name != "fake" {
		t.Errorf("config = %+v", c)
	}
}

func TestAuthenticate(t *testing.T) {
	d := validDoc()
	d["apps"] = append(d["apps"].([]any), doc{"id": "b", "key": key2,
		"channels": doc{"sms": doc{"per_minute": 1, "per_day": 1}}})
	c, err := load(t, d, baseEnv())
	if err != nil {
		t.Fatal(err)
	}
	if a, ok := c.Authenticate("a", key1); !ok || a == nil || a.ID != "a" {
		t.Errorf("right key: got %v %v", a, ok)
	}
	if a, ok := c.Authenticate("b", key2); !ok || a.ID != "b" {
		t.Errorf("app b: got %v %v", a, ok)
	}
	for name, tc := range map[string][2]string{
		"wrong key":         {"a", key2},
		"other apps key":    {"b", key1},
		"unknown app":       {"zzz", key1},
		"empty bearer":      {"a", ""},
		"empty app":         {"", key1},
		"prefix of key":     {"a", key1[:31]},
		"key with suffix":   {"a", key1 + "x"},
		"unknown and empty": {"zzz", ""},
	} {
		if a, ok := c.Authenticate(tc[0], tc[1]); ok || a != nil {
			t.Errorf("%s: authenticated (%v, %v)", name, a, ok)
		}
	}
}

// unset → local; "oidc","both" accepted; "OIDC", "sso" → Load error naming AUTH_MODE
func TestAuthModeConfig(t *testing.T) {
	for _, c := range []struct {
		env  string
		want AuthMode
	}{{"", AuthLocal}, {"local", AuthLocal}, {"oidc", AuthOIDC}, {"both", AuthBoth}} {
		e := baseEnv()
		if c.env != "" {
			e["AUTH_MODE"] = c.env
		}
		cfg, err := load(t, validDoc(), e)
		if err != nil {
			t.Errorf("AUTH_MODE=%q: %v", c.env, err)
		} else if cfg.AuthMode != c.want {
			t.Errorf("AUTH_MODE=%q: mode %q, want %q", c.env, cfg.AuthMode, c.want)
		}
	}
	for _, v := range []string{"OIDC", "sso", " local"} {
		e := baseEnv()
		e["AUTH_MODE"] = v
		if _, err := load(t, validDoc(), e); err == nil || !strings.Contains(err.Error(), "AUTH_MODE") {
			t.Errorf("AUTH_MODE=%q: err %v, want one naming AUTH_MODE", v, err)
		}
	}
	allows := map[AuthMode][2]bool{AuthLocal: {true, false}, AuthOIDC: {false, true}, AuthBoth: {true, true}}
	for m, want := range allows {
		if m.Allows(model.KindLocal) != want[0] || m.Allows(model.KindOIDC) != want[1] {
			t.Errorf("%s.Allows: local %v oidc %v, want %v", m, m.Allows(model.KindLocal), m.Allows(model.KindOIDC), want)
		}
	}
}

// smtp_submission default false; send_window_seconds default 86400; 0, 86401 → Load error
func TestSMTPAppConfig(t *testing.T) {
	c, err := load(t, validDoc(), baseEnv())
	if err != nil {
		t.Fatal(err)
	}
	if a := c.Apps["a"]; a.SMTPSubmission || a.SendWindow != constants.MaxSendWindow {
		t.Errorf("defaults: smtp %v window %v", a.SMTPSubmission, a.SendWindow)
	}
	d := validDoc()
	app0(d)["smtp_submission"] = true
	app0(d)["send_window_seconds"] = 3600
	if c, err = load(t, d, baseEnv()); err != nil {
		t.Fatal(err)
	}
	if a := c.Apps["a"]; !a.SMTPSubmission || a.SendWindow != time.Hour {
		t.Errorf("set: smtp %v window %v", a.SMTPSubmission, a.SendWindow)
	}
	app0(d)["send_window_seconds"] = 86400
	if c, err = load(t, d, baseEnv()); err != nil || c.Apps["a"].SendWindow != constants.MaxSendWindow {
		t.Errorf("86400: %v", err)
	}
	for _, v := range []any{0, -1, 86401, 1 << 62} {
		app0(d)["send_window_seconds"] = v
		if _, err := load(t, d, baseEnv()); err == nil || !strings.Contains(err.Error(), "send_window_seconds") {
			t.Errorf("send_window_seconds=%v: err %v", v, err)
		}
	}
}

// smtp app + key → ok; non-smtp app with its key → false; unknown app → false
func TestAuthenticateSMTP(t *testing.T) {
	d := validDoc()
	d["apps"] = append(d["apps"].([]any), doc{"id": "b", "key": key2, "smtp_submission": true,
		"channels": doc{"sms": doc{"per_minute": 1, "per_day": 1}}})
	c, err := load(t, d, baseEnv())
	if err != nil {
		t.Fatal(err)
	}
	if a, ok := c.AuthenticateSMTP("b", key2); !ok || a == nil || a.ID != "b" {
		t.Errorf("smtp app: got %v %v", a, ok)
	}
	for name, tc := range map[string][2]string{
		"non-smtp app": {"a", key1},
		"wrong key":    {"b", key1},
		"unknown app":  {"zzz", key2},
	} {
		if a, ok := c.AuthenticateSMTP(tc[0], tc[1]); ok || a != nil {
			t.Errorf("%s: authenticated (%v, %v)", name, a, ok)
		}
	}
}
