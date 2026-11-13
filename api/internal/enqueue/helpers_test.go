package enqueue

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/davcs86/commsgun/api/internal/clock"
	"github.com/davcs86/commsgun/api/internal/config"
	"github.com/davcs86/commsgun/api/internal/model"
	"github.com/davcs86/commsgun/api/internal/store"
)

const (
	testKey   = "k-0123456789abcdef0123456789abcdef"
	testApp   = "app1"
	testBody  = `{"channel":"email","to":["a@example.com"],"subject":"hi","text":"hello"}`
	testSMS   = `{"channel":"sms","to":["+15551234567"],"body":"hello"}`
	testSend  = "Alerts <alerts@example.com>"
	testStart = "2026-11-13T12:00:00Z"
)

var testNow = func() time.Time { t, _ := time.Parse(time.RFC3339, testStart); return t }()

func testConfig() *config.Config {
	snd := config.Sender{Raw: testSend, Address: "alerts@example.com"}
	email := config.ChannelCfg{PerMinute: 100, PerDay: 1000, Senders: []config.Sender{snd}, DefaultSender: &snd}
	sms := config.ChannelCfg{PerMinute: 100, PerDay: 1000}
	mk := func(id string, ch map[model.Channel]config.ChannelCfg) config.App {
		return config.App{ID: id, Key: testKey, Channels: ch}
	}
	noDefault := email
	noDefault.DefaultSender = nil
	tiny := email
	tiny.PerMinute = 2
	return &config.Config{
		Apps: map[string]config.App{
			testApp:     mk(testApp, map[model.Channel]config.ChannelCfg{model.ChannelEmail: email, model.ChannelSMS: sms}),
			"emailonly": mk("emailonly", map[model.Channel]config.ChannelCfg{model.ChannelEmail: email}),
			"nodefault": mk("nodefault", map[model.Channel]config.ChannelCfg{model.ChannelEmail: noDefault}),
			"tiny":      mk("tiny", map[model.Channel]config.ChannelCfg{model.ChannelEmail: tiny}),
		},
		Providers: map[model.Channel]*config.ProviderCfg{model.ChannelEmail: {Name: "fake", PerMinute: 10, PerDay: 100}},
	}
}

type fakeStore struct {
	mu    sync.Mutex
	calls []store.EnqueueInput
	res   store.EnqueueResult
	err   error
	look  *lookup
	lerr  error
	enter chan struct{} // when set, Enqueue signals here then waits for release
	hold  chan struct{}
}

func (f *fakeStore) Enqueue(_ context.Context, _ time.Time, in store.EnqueueInput) (store.EnqueueResult, error) {
	f.mu.Lock()
	f.calls = append(f.calls, in)
	res, err := f.res, f.err
	f.mu.Unlock()
	if f.enter != nil {
		f.enter <- struct{}{}
		<-f.hold
	}
	return res, err
}

type lookup struct {
	hash [32]byte
	res  store.EnqueueResult
}

func (f *fakeStore) LookupRequest(context.Context, string, string) ([32]byte, store.EnqueueResult, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.lerr != nil {
		return [32]byte{}, store.EnqueueResult{}, false, f.lerr
	}
	if f.look == nil {
		return [32]byte{}, store.EnqueueResult{}, false, nil
	}
	r := f.look.res
	r.Duplicate = true
	return f.look.hash, r, true, nil
}

// remember makes the double answer lookups as if body had been accepted earlier.
func (f *fakeStore) remember(t *testing.T, body string) {
	t.Helper()
	_, h, verr := parse([]byte(body))
	if verr != nil {
		t.Fatal(verr)
	}
	f.look = &lookup{hash: h, res: f.res}
}

func (f *fakeStore) last(t *testing.T) store.EnqueueInput {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) == 0 {
		t.Fatal("store was not called")
	}
	return f.calls[len(f.calls)-1]
}

func (f *fakeStore) called() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

type reqOpt func(*http.Request)

func header(k, v string) reqOpt { return func(r *http.Request) { r.Header.Set(k, v) } }
func noHeader(k string) reqOpt  { return func(r *http.Request) { r.Header.Del(k) } }

func newReq(body string, opts ...reqOpt) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
	r.RemoteAddr = "192.0.2.9:4000"
	r.Header.Set(headerApp, testApp)
	r.Header.Set("Authorization", "Bearer "+testKey)
	r.Header.Set(headerKey, "key-1")
	r.Header.Set("Content-Type", "application/json")
	for _, o := range opts {
		o(r)
	}
	return r
}

func newHandler(cfg *config.Config, st Enqueuer) http.Handler {
	return NewHandler(cfg, st, clock.NewFake(testNow), newLogger(io.Discard))
}

func do(h http.Handler, r *http.Request) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func okStore() *fakeStore {
	return &fakeStore{res: store.EnqueueResult{RequestID: "req-1", MessageIDs: []string{"m-1"}}}
}

func fakeClock() clock.Clock { return clock.NewFake(testNow) }

func newLogger(w io.Writer) *slog.Logger { return slog.New(slog.NewJSONHandler(w, nil)) }
