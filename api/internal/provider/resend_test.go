package provider

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/davcs86/commsgun/api/internal/constants"
	"github.com/davcs86/commsgun/api/internal/model"
)

var msg = model.Message{ID: "msg-1", Recipient: "bob@example.com", Sender: "Me <me@ex.org>", Channel: model.ChannelEmail}

func serve(t *testing.T, status int, body string, hdr map[string]string) (*Resend, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for k, v := range hdr {
			w.Header().Set(k, v)
		}
		w.WriteHeader(status)
		io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return NewResend("key", srv.Client(), srv.URL), srv
}

func errBody(status int, name, message string) string {
	b, _ := json.Marshal(map[string]any{"statusCode": status, "name": name, "message": message})
	return string(b)
}

func TestResendRequestShapeAndAccepted(t *testing.T) {
	var got map[string]any
	var hdr http.Header
	var path, method string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hdr, path, method = r.Header, r.URL.Path, r.Method
		json.NewDecoder(r.Body).Decode(&got)
		io.WriteString(w, `{"id":"x"}`)
	}))
	defer srv.Close()
	r := NewResend("secret", srv.Client(), srv.URL)
	res := r.Send(context.Background(), msg, Body{Subject: "hi", Text: "t", ReplyTo: "r@ex.org"})
	if res.Outcome != Accepted || res.ProviderID != "x" {
		t.Fatalf("%+v", res)
	}
	if method != "POST" || path != "/emails" {
		t.Fatalf("%s %s", method, path)
	}
	if hdr.Get("Authorization") != "Bearer secret" || hdr.Get("Idempotency-Key") != "msg-1" {
		t.Fatalf("headers %v", hdr)
	}
	if got["from"] != msg.Sender || got["subject"] != "hi" || got["text"] != "t" || got["reply_to"] != "r@ex.org" {
		t.Fatalf("body %v", got)
	}
	to, _ := got["to"].([]any)
	if len(to) != 1 || to[0] != "bob@example.com" {
		t.Fatalf("to %v", got["to"])
	}
	if _, ok := got["html"]; ok {
		t.Fatal("empty html must be omitted")
	}
}

func TestResendOmitsEmptyTextAndReplyTo(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&got)
		io.WriteString(w, `{"id":"x"}`)
	}))
	defer srv.Close()
	NewResend("k", srv.Client(), srv.URL).Send(context.Background(), msg, Body{Subject: "s", HTML: "<p>h</p>"})
	if _, ok := got["text"]; ok {
		t.Fatal("text present")
	}
	if _, ok := got["reply_to"]; ok {
		t.Fatal("reply_to present")
	}
	if got["html"] != "<p>h</p>" {
		t.Fatalf("%v", got)
	}
}

func TestResendThrottled(t *testing.T) {
	r, _ := serve(t, 429, errBody(429, "rate_limit_exceeded", "slow"), map[string]string{"retry-after": "3"})
	res := r.Send(context.Background(), msg, Body{})
	if res.Outcome != Throttled || res.PauseFor != 3*time.Second || res.PauseReason != "rate_limit" || res.Status != 429 || res.ProviderError != "rate_limit_exceeded" {
		t.Fatalf("%+v", res)
	}
	for _, h := range []map[string]string{nil, {"retry-after": "soon"}} {
		r, _ = serve(t, 429, errBody(429, "rate_limit_exceeded", "slow"), h)
		if res := r.Send(context.Background(), msg, Body{}); res.PauseFor != time.Second {
			t.Fatalf("fallback pause %+v", res)
		}
	}
	r, _ = serve(t, 429, errBody(429, "daily_quota_exceeded", "x"), nil)
	res = r.Send(context.Background(), msg, Body{})
	if res.Outcome != Throttled || !res.PauseUntilUTCMidnight || res.PauseReason != "daily_quota" {
		t.Fatalf("%+v", res)
	}
	r, _ = serve(t, 429, errBody(429, "monthly_quota_exceeded", "x"), nil)
	res = r.Send(context.Background(), msg, Body{})
	if res.Outcome != Throttled || res.PauseFor != constants.MonthlyQuotaPause || res.PauseReason != "monthly_quota" {
		t.Fatalf("%+v", res)
	}
}

func TestResendClassification(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		errName string
		message string
		want    Outcome
		et      model.ErrorType
	}{
		{"500", 500, "internal_server_error", "x", Retryable, model.ErrProviderUnavailable},
		{"503", 503, "", "", Retryable, model.ErrProviderUnavailable},
		{"408", 408, "", "", Retryable, model.ErrProviderUnavailable},
		{"409 concurrent", 409, "concurrent_idempotent_requests", "x", Retryable, model.ErrProviderUnavailable},
		{"401", 401, "missing_api_key", "x", Permanent, model.ErrAuth},
		{"403 key", 403, "restricted_api_key", "x", Permanent, model.ErrAuth},
		{"403 invalid key", 403, "invalid_api_key", "x", Permanent, model.ErrAuth},
		{"403 domain", 403, "validation_error", "The example.org domain is not verified.", Permanent, model.ErrSenderNotVerified},
		{"403 other", 403, "forbidden", "x", Permanent, model.ErrUnknown},
		{"422", 422, "validation_error", "bad subject", Permanent, model.ErrProviderRejected},
		{"400", 400, "validation_error", "bad", Permanent, model.ErrProviderRejected},
		{"422 to", 422, "validation_error", "Invalid `to` field.", Permanent, model.ErrInvalidRecipient},
		{"409 invalid", 409, "invalid_idempotent_request", "x", Permanent, model.ErrProviderRejected},
		{"404", 404, "not_found", "x", Permanent, model.ErrUnknown},
		{"418", 418, "", "", Permanent, model.ErrUnknown},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r, _ := serve(t, c.status, errBody(c.status, c.errName, c.message), nil)
			res := r.Send(context.Background(), msg, Body{})
			if res.Outcome != c.want || res.ErrorType != c.et || res.Status != c.status || res.ProviderError != c.errName {
				t.Fatalf("%+v", res)
			}
		})
	}
}

func TestResendNonJSONErrorBody(t *testing.T) {
	r, _ := serve(t, 502, "<html>bad gateway</html>", nil)
	res := r.Send(context.Background(), msg, Body{})
	if res.Outcome != Retryable || res.ErrorType != model.ErrProviderUnavailable || res.Status != 502 {
		t.Fatalf("%+v", res)
	}
}

func TestResend2xxWithoutIDIsRetryable(t *testing.T) {
	for _, body := range []string{"{}", "not json"} {
		r, _ := serve(t, 200, body, nil)
		res := r.Send(context.Background(), msg, Body{})
		if res.Outcome != Retryable || res.ErrorType != model.ErrProviderUnavailable || res.ProviderID != "" {
			t.Fatalf("%q: %+v", body, res)
		}
	}
}

func TestResendClientTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
	}))
	defer srv.Close()
	res := NewResend("k", &http.Client{Timeout: 50 * time.Millisecond}, srv.URL).Send(context.Background(), msg, Body{})
	if res.Outcome != Retryable || res.ErrorType != model.ErrTimeout {
		t.Fatalf("client timeout: %+v", res)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	res = NewResend("k", srv.Client(), srv.URL).Send(ctx, msg, Body{})
	if res.Outcome != Retryable || res.ErrorType != model.ErrTimeout {
		t.Fatalf("ctx deadline: %+v", res)
	}
}

func TestResendConnectionRefusedIsNetwork(t *testing.T) {
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := l.Addr().String()
	l.Close()
	res := NewResend("k", &http.Client{}, "http://"+addr).Send(context.Background(), msg, Body{})
	if res.Outcome != Retryable || res.ErrorType != model.ErrNetwork {
		t.Fatalf("%+v", res)
	}
}

func TestResendConnectionDropAfterWriteIsRetryable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		c, _, _ := w.(http.Hijacker).Hijack()
		c.Close()
	}))
	defer srv.Close()
	res := NewResend("k", srv.Client(), srv.URL).Send(context.Background(), msg, Body{})
	if res.Outcome != Retryable || res.ErrorType != model.ErrNetwork {
		t.Fatalf("%+v", res)
	}
}

func TestResendMessageRedactsAddressesAndTruncates(t *testing.T) {
	r, _ := serve(t, 422, errBody(422, "validation_error", "Invalid to bob@example.com and also cc.x+y@sub.mail.org here"), nil)
	res := r.Send(context.Background(), msg, Body{})
	if strings.Contains(res.Message, "@") || !strings.Contains(res.Message, "[redacted]") {
		t.Fatalf("message %q", res.Message)
	}
	r, _ = serve(t, 422, errBody(422, "validation_error", strings.Repeat("x", 1000)), nil)
	res = r.Send(context.Background(), msg, Body{})
	if len(res.Message) != constants.AttemptMessageMaxLen {
		t.Fatalf("len %d", len(res.Message))
	}
}

func TestResendResponseBodyIsCapped(t *testing.T) {
	big := `{"id":"` + strings.Repeat("a", constants.ProviderResponseMaxBytes) + `"}`
	r, _ := serve(t, 200, big, nil)
	res := r.Send(context.Background(), msg, Body{})
	if res.Outcome != Retryable {
		t.Fatalf("oversized body must not parse: %+v", res)
	}
}

func TestResendNameAndDefaultBaseURL(t *testing.T) {
	r := NewResend("k", nil, "")
	if r.Name() != "resend" || r.baseURL != "https://api.resend.com" || r.hc == nil {
		t.Fatalf("%+v", r)
	}
}

func TestResendStatusMapping(t *testing.T) {
	cases := map[string]DeliveryStatus{
		"delivered": Delivered, "bounced": Bounced, "complained": Complained,
		"failed": Rejected, "suppressed": Rejected, "canceled": Rejected,
		"sent": Pending, "queued": Pending, "delivery_delayed": Pending, "scheduled": Pending,
		"opened": Pending, "clicked": Pending, "something_new": Pending, "": Pending,
	}
	for ev, want := range cases {
		var path string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			path = r.URL.Path
			if r.Header.Get("Authorization") != "Bearer k" || r.Method != "GET" {
				w.WriteHeader(500)
			}
			json.NewEncoder(w).Encode(map[string]string{"id": "pid", "last_event": ev})
		}))
		got, err := NewResend("k", srv.Client(), srv.URL).Status(context.Background(), "pid")
		srv.Close()
		if err != nil || got != want || path != "/emails/pid" {
			t.Errorf("%q: got %v err %v path %s", ev, got, err, path)
		}
	}
}

func TestResendStatusErrors(t *testing.T) {
	r, _ := serve(t, 403, errBody(403, "restricted_api_key", "bob@example.com"), nil)
	_, err := r.Status(context.Background(), "pid")
	if err == nil || strings.Contains(err.Error(), "@") {
		t.Fatalf("err %v", err)
	}
	r, _ = serve(t, 200, "not json", nil)
	if _, err := r.Status(context.Background(), "pid"); err == nil {
		t.Fatal("want error on bad json")
	}
}
