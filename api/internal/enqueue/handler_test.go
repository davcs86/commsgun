package enqueue

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/davcs86/commsgun/api/internal/constants"
	"github.com/davcs86/commsgun/api/internal/model"
	"github.com/davcs86/commsgun/api/internal/store"
)

func jsonBody(t *testing.T, b *bytes.Buffer) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(b.Bytes(), &m); err != nil {
		t.Fatalf("body %q is not JSON: %v", b.String(), err)
	}
	return m
}

func TestAccepted(t *testing.T) {
	st := okStore()
	w := do(newHandler(testConfig(), st), newReq(testBody))
	if w.Code != 202 {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("content type %q", ct)
	}
	m := jsonBody(t, w.Body)
	ids, _ := m["message_ids"].([]any)
	if m["request_id"] != "req-1" || m["duplicate"] != false || len(ids) != 1 || ids[0] != "m-1" {
		t.Fatalf("body %v", m)
	}
}

func TestDuplicateIs200(t *testing.T) {
	st := okStore()
	st.res.Duplicate = true
	w := do(newHandler(testConfig(), st), newReq(testBody))
	if w.Code != 200 || jsonBody(t, w.Body)["duplicate"] != true {
		t.Fatalf("status %d body %s", w.Code, w.Body)
	}
}

func TestStoreInputIsFilled(t *testing.T) {
	st := okStore()
	h := newHandler(testConfig(), st)
	do(h, newReq(testBody, header("X-Forwarded-For", "203.0.113.7, 10.0.0.1")))
	in := st.last(t)
	if in.IdempotencyKey != "key-1" || in.SourceIP != "203.0.113.7" || in.PerMinute != 100 || in.PerDay != 1000 ||
		in.Provider != "fake" || in.Held || in.Sender != testSend || in.App != testApp {
		t.Fatalf("got %+v", in)
	}
	if want := testNow.Add(constants.MaxSendWindow); !in.ExpiresAt.Equal(want) {
		t.Fatalf("expires_at %v", in.ExpiresAt)
	}
	do(h, newReq(testBody))
	if ip := st.last(t).SourceIP; ip != "192.0.2.9" {
		t.Fatalf("remote host fallback gave %q", ip)
	}
}

func TestJunkForwardedForFallsBackToRemote(t *testing.T) {
	st := okStore()
	do(newHandler(testConfig(), st), newReq(testBody, header("X-Forwarded-For", "not-an-ip")))
	if ip := st.last(t).SourceIP; ip != "192.0.2.9" {
		t.Fatalf("got %q", ip)
	}
}

func TestHeldWhenNoProvider(t *testing.T) {
	st := okStore()
	do(newHandler(testConfig(), st), newReq(testSMS))
	if in := st.last(t); !in.Held || in.Provider != "" || in.Channel != model.ChannelSMS {
		t.Fatalf("got %+v", in)
	}
}

func TestUnauthorizedIsIdenticalForEveryCause(t *testing.T) {
	cases := map[string][]reqOpt{
		"no app header":    {noHeader(headerApp)},
		"unknown app":      {header(headerApp, "ghost")},
		"no authorization": {noHeader("Authorization")},
		"wrong key":        {header("Authorization", "Bearer "+strings.Repeat("z", 40))},
		"not bearer":       {header("Authorization", "Basic "+testKey)},
	}
	var wantBody string
	var wantHdr http.Header
	for name, opts := range cases {
		st := okStore()
		w := do(newHandler(testConfig(), st), newReq(testBody, opts...))
		if w.Code != 401 || w.Body.String() != `{"error":"unauthorized"}`+"\n" {
			t.Fatalf("%s: %d %q", name, w.Code, w.Body)
		}
		if wantHdr == nil {
			wantBody, wantHdr = w.Body.String(), w.Header()
		}
		if w.Body.String() != wantBody || fmt.Sprint(w.Header()) != fmt.Sprint(wantHdr) {
			t.Fatalf("%s: response differs from the others", name)
		}
		if st.called() != 0 {
			t.Fatalf("%s: store reached", name)
		}
	}
}

func TestChannelNotAllowed(t *testing.T) {
	st := okStore()
	w := do(newHandler(testConfig(), st), newReq(testSMS, header(headerApp, "emailonly")))
	if w.Code != 403 || jsonBody(t, w.Body)["error"] != "channel_not_allowed" || st.called() != 0 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
}

func TestIdempotencyConflict(t *testing.T) {
	st := okStore()
	st.err = store.ErrIdempotencyConflict
	w := do(newHandler(testConfig(), st), newReq(testBody))
	if w.Code != 409 || jsonBody(t, w.Body)["error"] != "idempotency_conflict" {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
}

func TestBodySizeBoundary(t *testing.T) {
	pad := func(n int) string {
		base := `{"channel":"email","to":["a@example.com"],"subject":"s","text":"`
		return base + strings.Repeat("x", n-len(base)-2) + `"}`
	}
	st := okStore()
	h := newHandler(testConfig(), st)
	w := do(h, newReq(pad(constants.MaxBodyBytes+1)))
	if w.Code != 413 || w.Body.Len() != 0 {
		t.Fatalf("over: %d %q", w.Code, w.Body)
	}
	if w := do(h, newReq(pad(constants.MaxBodyBytes))); w.Code != 202 {
		t.Fatalf("exact: %d %s", w.Code, w.Body)
	}
}

func TestOversizeBodyBeats401(t *testing.T) {
	big := strings.Repeat("x", constants.MaxBodyBytes+1)
	w := do(newHandler(testConfig(), okStore()), newReq(big, noHeader("Authorization")))
	if w.Code != 413 {
		t.Fatalf("got %d", w.Code)
	}
}

func TestIdempotencyKeyValidation(t *testing.T) {
	cases := map[string]string{
		"missing":   "",
		"201 chars": strings.Repeat("k", 201),
		"space":     "has space",
		"slash":     "a/b",
	}
	for name, key := range cases {
		opts := []reqOpt{header(headerKey, key)}
		if key == "" {
			opts = []reqOpt{noHeader(headerKey)}
		}
		w := do(newHandler(testConfig(), okStore()), newReq(testBody, opts...))
		m := jsonBody(t, w.Body)
		if w.Code != 422 || m["field"] != "Idempotency-Key" {
			t.Fatalf("%s: %d %s", name, w.Code, w.Body)
		}
	}
	for _, key := range []string{strings.Repeat("k", 200), "a.b_c:d-E9"} {
		if w := do(newHandler(testConfig(), okStore()), newReq(testBody, header(headerKey, key))); w.Code != 202 {
			t.Fatalf("key %q: %d", key, w.Code)
		}
	}
}

func TestValidationIs422WithField(t *testing.T) {
	w := do(newHandler(testConfig(), okStore()), newReq(`{"channel":"email","to":["a@example.com"],"text":"t"}`))
	m := jsonBody(t, w.Body)
	if w.Code != 422 || m["error"] != "invalid_subject" || m["field"] != "subject" {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
}

func TestRateLimited(t *testing.T) {
	for _, tc := range []struct {
		window string
		after  time.Duration
		header string
	}{{"minute", 1500 * time.Millisecond, "2"}, {"day", 3 * time.Hour, "10800"}, {"minute", 10 * time.Millisecond, "1"}} {
		st := okStore()
		st.err = &store.RateLimitedError{Window: tc.window, RetryAfter: tc.after}
		w := do(newHandler(testConfig(), st), newReq(testBody))
		m := jsonBody(t, w.Body)
		if w.Code != 429 || w.Header().Get("Retry-After") != tc.header || m["error"] != "rate_limited" || m["window"] != tc.window {
			t.Fatalf("%+v: %d %v %v", tc, w.Code, w.Header(), m)
		}
	}
}

func TestQueueFullIs503(t *testing.T) {
	st := okStore()
	st.err = store.ErrQueueFull
	assertUnavailable(t, do(newHandler(testConfig(), st), newReq(testBody)))
}

func TestStoreBusyIs503(t *testing.T) {
	st := okStore()
	st.err = fmt.Errorf("wrapped: %w", store.ErrBusy)
	assertUnavailable(t, do(newHandler(testConfig(), st), newReq(testBody)))
}

func TestUnexpectedStoreErrorIs500(t *testing.T) {
	st := okStore()
	st.err = errors.New("boom")
	w := do(newHandler(testConfig(), st), newReq(testBody))
	if w.Code != 500 || jsonBody(t, w.Body)["error"] != "internal" {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
}

func assertUnavailable(t *testing.T, w *httptest.ResponseRecorder) {
	t.Helper()
	if w.Code != 503 || w.Header().Get("Retry-After") != "1" || w.Body.String() != `{"error":"unavailable"}`+"\n" ||
		w.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("%d %v %q", w.Code, w.Header(), w.Body)
	}
}

func TestInFlightGate(t *testing.T) {
	st := okStore()
	st.enter = make(chan struct{}, constants.EnqueueMaxConcurrent)
	st.hold = make(chan struct{})
	h := newHandler(testConfig(), st)
	var wg sync.WaitGroup
	codes := make(chan int, constants.EnqueueMaxConcurrent)
	for i := 0; i < constants.EnqueueMaxConcurrent; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes <- do(h, newReq(testBody)).Code
		}()
	}
	for i := 0; i < constants.EnqueueMaxConcurrent; i++ {
		select {
		case <-st.enter:
		case <-time.After(5 * time.Second):
			t.Fatalf("only %d requests reached the store", i)
		}
	}
	w := do(h, newReq(testBody))
	close(st.hold)
	wg.Wait()
	close(codes)
	if w.Code != 503 || w.Header().Get("Retry-After") != "1" || w.Body.String() != `{"error":"unavailable"}`+"\n" {
		t.Fatalf("33rd: %d %v %q", w.Code, w.Header(), w.Body)
	}
	for c := range codes {
		if c != 202 {
			t.Fatalf("held request finished with %d", c)
		}
	}
	if w := do(h, newReq(testBody)); w.Code != 202 {
		t.Fatalf("gate did not release: %d", w.Code)
	}
}

func TestOnlyPostAllowed(t *testing.T) {
	r := newReq("")
	r.Method = http.MethodGet
	w := do(newHandler(testConfig(), okStore()), r)
	if w.Code != 405 || w.Header().Get("Allow") != "POST" {
		t.Fatalf("%d %v", w.Code, w.Header())
	}
}

func TestAwkwardAddresses(t *testing.T) {
	st := okStore()
	h := newHandler(testConfig(), st)
	body := `{"channel":"email","to":["A@Example.com","a@example.com"],"subject":"s","text":"t"}`
	if w := do(h, newReq(body)); w.Code != 202 {
		t.Fatalf("dedupe: %d %s", w.Code, w.Body)
	}
	if rc := st.last(t).Recipients; len(rc) != 1 || rc[0] != "A@Example.com" {
		t.Fatalf("recipients %v", rc)
	}
	body = `{"channel":"email","to":["user+tag@example.com","user@xn--bcher-kva.example"],"subject":"s","text":"t"}`
	if w := do(h, newReq(body)); w.Code != 202 {
		t.Fatalf("awkward: %d %s", w.Code, w.Body)
	}
	body = `{"channel":"email","to":["a@example.com"],"from":"Other Name <alerts@example.com>","subject":"s","text":"t"}`
	if w := do(h, newReq(body)); w.Code != 202 {
		t.Fatalf("sender: %d %s", w.Code, w.Body)
	}
	if s := st.last(t).Sender; s != "Other Name <alerts@example.com>" {
		t.Fatalf("stored sender %q", s)
	}
}

func TestLogCarriesNoSecrets(t *testing.T) {
	var buf bytes.Buffer
	h := NewHandler(testConfig(), okStore(), fakeClock(), newLogger(&buf))
	do(h, newReq(`{"channel":"email","to":["secret-rcpt@example.com"],"subject":"secret-subject","text":"secret-body"}`))
	out := buf.String()
	for _, bad := range []string{testKey, "secret-rcpt", "secret-subject", "secret-body"} {
		if strings.Contains(out, bad) {
			t.Fatalf("log leaks %q: %s", bad, out)
		}
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &m); err != nil {
		t.Fatalf("not one JSON line: %q", out)
	}
	if m["status"] != float64(202) || m["app"] != testApp || m["channel"] != "email" || m["recipients"] != float64(1) || m["request_id"] != "req-1" {
		t.Fatalf("log %v", m)
	}
}
