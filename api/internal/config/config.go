// Package config parses and validates COMMSGUN_CONFIG and the process environment.
package config

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"net/mail"
	"regexp"
	"strings"
	"time"

	"github.com/davcs86/commsgun/api/internal/constants"
	"github.com/davcs86/commsgun/api/internal/model"
)

const (
	defaultDataDir = "/data"
	providerResend = "resend"
	providerFake   = "fake"
)

var appIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)

// unknownAppKey is compared against when the app is unknown, so both paths cost one comparison.
var unknownAppKey = []byte(strings.Repeat("x", constants.MinSecretLen))

type Sender struct {
	Raw     string
	Address string // lower-cased
}

type ChannelCfg struct {
	PerMinute, PerDay int
	Senders           []Sender
	DefaultSender     *Sender
}

type App struct {
	ID, Key        string
	Channels       map[model.Channel]ChannelCfg
	SMTPSubmission bool
	SendWindow     time.Duration
}

type ProviderCfg struct {
	Name              string
	PerMinute, PerDay int
}

// AuthMode is AUTH_MODE: which session kinds the API creates and accepts.
type AuthMode string

const (
	AuthLocal AuthMode = "local"
	AuthOIDC  AuthMode = "oidc"
	AuthBoth  AuthMode = "both"
)

func (m AuthMode) Allows(kind model.SessionKind) bool {
	switch m {
	case AuthLocal:
		return kind == model.KindLocal
	case AuthOIDC:
		return kind == model.KindOIDC
	case AuthBoth:
		return kind == model.KindLocal || kind == model.KindOIDC
	}
	return false
}

type Config struct {
	Apps                                 map[string]App
	Providers                            map[model.Channel]*ProviderCfg
	ResendAPIKey, AdminAPIToken, DataDir string
	AllowFake                            bool
	AuthMode                             AuthMode
}

type rawConfig struct {
	Apps      []rawApp                `json:"apps"`
	Providers map[string]*rawProvider `json:"providers"`
}

type rawApp struct {
	ID                string                `json:"id"`
	Key               string                `json:"key"`
	Channels          map[string]rawChannel `json:"channels"`
	SMTPSubmission    bool                  `json:"smtp_submission"`
	SendWindowSeconds *int                  `json:"send_window_seconds"`
}

type rawChannel struct {
	PerMinute     int      `json:"per_minute"`
	PerDay        int      `json:"per_day"`
	Senders       []string `json:"senders"`
	DefaultSender string   `json:"default_sender"`
}

type rawProvider struct {
	Name      string `json:"name"`
	PerMinute int    `json:"per_minute"`
	PerDay    int    `json:"per_day"`
}

// DataDir is DATA_DIR, or /data when unset.
func DataDir(getenv func(string) string) string {
	if d := getenv("DATA_DIR"); d != "" {
		return d
	}
	return defaultDataDir
}

// Load validates everything and never puts a key or token value in an error.
func Load(getenv func(string) string) (*Config, error) {
	var raw rawConfig
	dec := json.NewDecoder(strings.NewReader(getenv("COMMSGUN_CONFIG")))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&raw); err != nil {
		return nil, fmt.Errorf("COMMSGUN_CONFIG: invalid JSON: %w", err)
	}
	if dec.More() {
		return nil, fmt.Errorf("COMMSGUN_CONFIG: trailing data after JSON document")
	}

	c := &Config{
		Apps:          map[string]App{},
		Providers:     map[model.Channel]*ProviderCfg{},
		ResendAPIKey:  getenv("RESEND_API_KEY"),
		AdminAPIToken: getenv("ADMIN_API_TOKEN"),
		DataDir:       DataDir(getenv),
		AllowFake:     getenv("COMMSGUN_ALLOW_FAKE") == "1",
	}
	if len(c.AdminAPIToken) < constants.MinSecretLen {
		return nil, fmt.Errorf("ADMIN_API_TOKEN: must be at least %d characters", constants.MinSecretLen)
	}
	switch m := AuthMode(getenv("AUTH_MODE")); m {
	case "":
		c.AuthMode = AuthLocal
	case AuthLocal, AuthOIDC, AuthBoth:
		c.AuthMode = m
	default:
		return nil, fmt.Errorf("AUTH_MODE: must be %s, %s or %s", AuthLocal, AuthOIDC, AuthBoth)
	}
	if len(raw.Apps) == 0 {
		return nil, fmt.Errorf("COMMSGUN_CONFIG apps: at least one app is required")
	}
	for i, ra := range raw.Apps {
		app, err := buildApp(i, ra)
		if err != nil {
			return nil, err
		}
		if _, dup := c.Apps[app.ID]; dup {
			return nil, fmt.Errorf("COMMSGUN_CONFIG apps[%d].id: duplicate app id %q", i, app.ID)
		}
		c.Apps[app.ID] = app
	}
	if err := c.buildProviders(raw.Providers); err != nil {
		return nil, err
	}
	return c, nil
}

func buildApp(i int, ra rawApp) (App, error) {
	p := fmt.Sprintf("COMMSGUN_CONFIG apps[%d]", i)
	if !appIDPattern.MatchString(ra.ID) {
		return App{}, fmt.Errorf("%s.id: must match %s", p, appIDPattern)
	}
	if len(ra.Key) < constants.MinSecretLen {
		return App{}, fmt.Errorf("%s.key: must be at least %d characters", p, constants.MinSecretLen)
	}
	app := App{ID: ra.ID, Key: ra.Key, Channels: map[model.Channel]ChannelCfg{},
		SMTPSubmission: ra.SMTPSubmission, SendWindow: constants.MaxSendWindow}
	if ra.SendWindowSeconds != nil {
		maxSec := int(constants.MaxSendWindow / time.Second)
		if sec := *ra.SendWindowSeconds; sec <= 0 || sec > maxSec {
			return App{}, fmt.Errorf("%s.send_window_seconds: must be 1 to %d", p, maxSec)
		}
		app.SendWindow = time.Duration(*ra.SendWindowSeconds) * time.Second
	}
	for name, rc := range ra.Channels {
		ch := model.Channel(name)
		cp := fmt.Sprintf("%s.channels.%s", p, name)
		if ch != model.ChannelEmail && ch != model.ChannelSMS {
			return App{}, fmt.Errorf("%s: unknown channel", cp)
		}
		cfg, err := buildChannel(cp, ch, rc)
		if err != nil {
			return App{}, err
		}
		app.Channels[ch] = cfg
	}
	return app, nil
}

func buildChannel(p string, ch model.Channel, rc rawChannel) (ChannelCfg, error) {
	if rc.PerMinute <= 0 {
		return ChannelCfg{}, fmt.Errorf("%s.per_minute: must be positive", p)
	}
	if rc.PerDay <= 0 {
		return ChannelCfg{}, fmt.Errorf("%s.per_day: must be positive", p)
	}
	cfg := ChannelCfg{PerMinute: rc.PerMinute, PerDay: rc.PerDay}
	if ch != model.ChannelEmail {
		if len(rc.Senders) > 0 || rc.DefaultSender != "" {
			return ChannelCfg{}, fmt.Errorf("%s.senders: only valid for email", p)
		}
		return cfg, nil
	}
	if len(rc.Senders) == 0 {
		return ChannelCfg{}, fmt.Errorf("%s.senders: at least one sender is required", p)
	}
	for j, raw := range rc.Senders {
		s, err := parseSender(raw)
		if err != nil {
			return ChannelCfg{}, fmt.Errorf("%s.senders[%d]: %w", p, j, err)
		}
		cfg.Senders = append(cfg.Senders, s)
	}
	if rc.DefaultSender != "" {
		d, err := parseSender(rc.DefaultSender)
		if err != nil {
			return ChannelCfg{}, fmt.Errorf("%s.default_sender: %w", p, err)
		}
		found := false
		for _, s := range cfg.Senders {
			found = found || s.Address == d.Address
		}
		if !found {
			return ChannelCfg{}, fmt.Errorf("%s.default_sender: not in senders", p)
		}
		cfg.DefaultSender = &d
	}
	return cfg, nil
}

func parseSender(raw string) (Sender, error) {
	if strings.ContainsAny(raw, "\r\n") {
		return Sender{}, fmt.Errorf("invalid address")
	}
	a, err := mail.ParseAddress(raw)
	if err != nil {
		return Sender{}, fmt.Errorf("invalid address")
	}
	return Sender{Raw: raw, Address: strings.ToLower(a.Address)}, nil
}

func (c *Config) buildProviders(raw map[string]*rawProvider) error {
	for name, rp := range raw {
		ch := model.Channel(name)
		p := "COMMSGUN_CONFIG providers." + name
		if ch != model.ChannelEmail && ch != model.ChannelSMS {
			return fmt.Errorf("%s: unknown channel", p)
		}
		if rp == nil {
			continue
		}
		switch {
		case rp.Name == providerResend && ch == model.ChannelEmail:
			if c.ResendAPIKey == "" {
				return fmt.Errorf("RESEND_API_KEY: required when %s.name is %q", p, providerResend)
			}
		case rp.Name == providerFake:
			if !c.AllowFake {
				return fmt.Errorf("COMMSGUN_ALLOW_FAKE: must be 1 when %s.name is %q", p, providerFake)
			}
		default:
			return fmt.Errorf("%s.name: unknown provider for this channel", p)
		}
		if rp.PerMinute <= 0 {
			return fmt.Errorf("%s.per_minute: must be positive", p)
		}
		if rp.PerDay <= 0 {
			return fmt.Errorf("%s.per_day: must be positive", p)
		}
		c.Providers[ch] = &ProviderCfg{Name: rp.Name, PerMinute: rp.PerMinute, PerDay: rp.PerDay}
	}
	return nil
}

// Authenticate costs one constant-time comparison of SHA-256 digests whether or not the app exists,
// so neither the key's length nor the app's existence shows in the timing.
func (c *Config) Authenticate(appID, bearer string) (*App, bool) {
	app, known := c.Apps[appID]
	want := unknownAppKey
	if known {
		want = []byte(app.Key)
	}
	got, exp := sha256.Sum256([]byte(bearer)), sha256.Sum256(want)
	match := subtle.ConstantTimeCompare(got[:], exp[:]) == 1
	if !known || !match {
		return nil, false
	}
	return &app, true
}

// AuthenticateSMTP is Authenticate, then accepts only an app with smtp_submission; it costs the same.
func (c *Config) AuthenticateSMTP(appID, key string) (*App, bool) {
	app, ok := c.Authenticate(appID, key)
	if !ok || !app.SMTPSubmission {
		return nil, false
	}
	return app, true
}
