package enqueue

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/davcs86/commsgun/api/internal/config"
	"github.com/davcs86/commsgun/api/internal/model"
	"github.com/davcs86/commsgun/api/internal/store"
)

func submit(cfg *config.Config, st Enqueuer, appID, key, body string) (Outcome, *Failure) {
	app := cfg.Apps[appID]
	return Submit(context.Background(), cfg, st, testNow, &app, key, []byte(body), "192.0.2.9")
}

func TestSubmitQueues(t *testing.T) {
	for _, tc := range []struct {
		body string
		ch   model.Channel
		held bool
	}{{testBody, model.ChannelEmail, false}, {testSMS, model.ChannelSMS, true}} {
		st := okStore()
		out, fail := submit(testConfig(), st, testApp, "key-1", tc.body)
		if fail != nil {
			t.Fatalf("%s: failure %+v", tc.ch, fail)
		}
		if out.Channel != tc.ch || out.Recipients != 1 || out.Held != tc.held || out.Result.RequestID != "req-1" {
			t.Errorf("%s: outcome %+v", tc.ch, out)
		}
		if in := st.last(t); in.IdempotencyKey != "key-1" || in.SourceIP != "192.0.2.9" || in.Held != tc.held {
			t.Errorf("%s: store input %+v", tc.ch, in)
		}
	}
}

func TestSubmitRepeatReportsHeld(t *testing.T) {
	st := okStore()
	st.remember(t, testSMS)
	out, fail := submit(testConfig(), st, testApp, "key-1", testSMS)
	if fail != nil || !out.Result.Duplicate || !out.Held || st.called() != 0 {
		t.Fatalf("outcome %+v failure %+v calls %d", out, fail, st.called())
	}
}

func TestSubmitFailures(t *testing.T) {
	conflict := okStore()
	conflict.remember(t, testSMS)
	for _, tc := range []struct {
		name, app, key, body string
		st                   *fakeStore
		want                 Failure
	}{
		{"no key", testApp, "", testBody, okStore(), Failure{Status: 422, Code: "idempotency_key_required", Field: "Idempotency-Key"}},
		{"bad key", testApp, "a b", testBody, okStore(), Failure{Status: 422, Code: "invalid_idempotency_key", Field: "Idempotency-Key"}},
		{"bad json", testApp, "k", "{", okStore(), Failure{Status: 422, Code: "invalid_json"}},
		{"channel off", "emailonly", "k", testSMS, okStore(), Failure{Status: 403, Code: "channel_not_allowed"}},
		{"rate limited", testApp, "k", testBody, &fakeStore{err: &store.RateLimitedError{Window: "day", RetryAfter: 90 * time.Second}},
			Failure{Status: 429, Code: "rate_limited", Window: "day", RetryAfter: 90 * time.Second}},
		{"queue full", testApp, "k", testBody, &fakeStore{err: store.ErrQueueFull}, Failure{Status: 503, Code: "unavailable", RetryAfter: time.Second}},
		{"other body", testApp, "k", testBody, conflict, Failure{Status: 409, Code: "idempotency_conflict"}},
		{"store broken", testApp, "k", testBody, &fakeStore{err: errors.New("boom")}, Failure{Status: 500, Code: "internal"}},
	} {
		_, fail := submit(testConfig(), tc.st, tc.app, tc.key, tc.body)
		if fail == nil || *fail != tc.want {
			t.Errorf("%s: got %+v, want %+v", tc.name, fail, tc.want)
		}
	}
}
