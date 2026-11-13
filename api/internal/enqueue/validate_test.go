package enqueue

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/davcs86/commsgun/api/internal/config"
	"github.com/davcs86/commsgun/api/internal/model"
)

func appNamed(t *testing.T, id string) *config.App {
	t.Helper()
	a := testConfig().Apps[id]
	return &a
}

func TestValidateRejects(t *testing.T) {
	long := strings.Repeat("a", 999)
	eleven := `"a1@e.co","a2@e.co","a3@e.co","a4@e.co","a5@e.co","a6@e.co","a7@e.co","a8@e.co","a9@e.co","a10@e.co","a11@e.co"`
	email := func(extra string) string {
		return fmt.Sprintf(`{"channel":"email","to":["a@example.com"],"subject":"s","text":"t"%s}`, extra)
	}
	cases := []struct{ name, body, msg, field string }{
		{"bad json", `{`, "invalid_json", ""},
		{"trailing data", testBody + `{}`, "invalid_json", ""},
		{"to not array", `{"channel":"email","to":"a@example.com"}`, "invalid_json", "to"},
		{"unknown channel", `{"channel":"fax","to":["a@example.com"]}`, "invalid_channel", "channel"},
		{"to empty", `{"channel":"email","to":[],"subject":"s","text":"t"}`, "invalid_recipient_count", "to"},
		{"to eleven", `{"channel":"email","to":[` + eleven + `],"subject":"s","text":"t"}`, "invalid_recipient_count", "to"},
		{"display name in to", `{"channel":"email","to":["Name <a@b.c>"],"subject":"s","text":"t"}`, "invalid_recipient", "to"},
		{"crlf in to", `{"channel":"email","to":["a@b.c\r\nBcc: x@y.z"],"subject":"s","text":"t"}`, "invalid_recipient", "to"},
		{"sms not e164", `{"channel":"sms","to":["5551234567"],"body":"x"}`, "invalid_recipient", "to"},
		{"subject missing", `{"channel":"email","to":["a@example.com"],"text":"t"}`, "invalid_subject", "subject"},
		{"subject newline", `{"channel":"email","to":["a@example.com"],"subject":"a\nb","text":"t"}`, "invalid_subject", "subject"},
		{"subject 999", `{"channel":"email","to":["a@example.com"],"subject":"` + long + `","text":"t"}`, "invalid_subject", "subject"},
		{"no text or html", `{"channel":"email","to":["a@example.com"],"subject":"s"}`, "body_required", "text"},
		{"from not allowed", email(`,"from":"Eve <eve@example.com>"`), "sender_not_allowed", "from"},
		{"from garbage", email(`,"from":"nope"`), "sender_not_allowed", "from"},
		{"reply_to list", email(`,"reply_to":"a@b.c, d@e.f"`), "invalid_reply_to", "reply_to"},
		{"reply_to crlf", email(`,"reply_to":"a@b.c\r\nX: y"`), "invalid_reply_to", "reply_to"},
		{"sms body empty", `{"channel":"sms","to":["+15551234567"],"body":""}`, "invalid_body", "body"},
		{"sms body 1601", `{"channel":"sms","to":["+15551234567"],"body":"` + strings.Repeat("x", 1601) + `"}`, "invalid_body", "body"},
		{"expires past", email(`,"expires_at":"2026-11-13T11:59:00Z"`), "invalid_expires_at", "expires_at"},
		{"expires now", email(`,"expires_at":"2026-11-13T12:00:00Z"`), "invalid_expires_at", "expires_at"},
		{"expires 24h+1s", email(`,"expires_at":"2026-11-14T12:00:01Z"`), "invalid_expires_at", "expires_at"},
		{"expires garbage", email(`,"expires_at":"tomorrow"`), "invalid_expires_at", "expires_at"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in, verr := Validate(appNamed(t, testApp), []byte(c.body), testNow)
			if verr == nil || in != nil {
				t.Fatalf("want rejection, got in=%v verr=%v", in, verr)
			}
			if verr.Message != c.msg || verr.Field != c.field {
				t.Fatalf("got %+v, want message=%s field=%s", verr, c.msg, c.field)
			}
		})
	}
}

func TestValidateNoDefaultSender(t *testing.T) {
	_, verr := Validate(appNamed(t, "nodefault"), []byte(testBody), testNow)
	if verr == nil || verr.Message != "sender_required" || verr.Field != "from" {
		t.Fatalf("got %+v", verr)
	}
}

func TestValidateRecipientsAboveLimit(t *testing.T) {
	body := `{"channel":"email","to":["a@e.co","b@e.co","c@e.co"],"subject":"s","text":"t"}`
	_, verr := Validate(appNamed(t, "tiny"), []byte(body), testNow)
	if verr == nil || verr.Message != "too_many_recipients_for_limit" || verr.Field != "to" {
		t.Fatalf("got %+v", verr)
	}
}

func TestValidateDefaults(t *testing.T) {
	in, verr := Validate(appNamed(t, testApp), []byte(testBody), testNow)
	if verr != nil {
		t.Fatal(verr)
	}
	if want := testNow.Add(24 * time.Hour); !in.ExpiresAt.Equal(want) {
		t.Fatalf("expires_at = %v, want %v", in.ExpiresAt, want)
	}
	if in.Sender != testSend || in.App != testApp || in.Channel != model.ChannelEmail {
		t.Fatalf("got %+v", in)
	}
}

func TestValidateExpiresBoundaryAccepted(t *testing.T) {
	body := `{"channel":"email","to":["a@example.com"],"subject":"s","text":"t","expires_at":"2026-11-14T12:00:00Z"}`
	in, verr := Validate(appNamed(t, testApp), []byte(body), testNow)
	if verr != nil || !in.ExpiresAt.Equal(testNow.Add(24*time.Hour)) {
		t.Fatalf("got %v %v", in, verr)
	}
}

func TestValidateBodyHashIgnoresKeyOrderAndWhitespace(t *testing.T) {
	a := `{"channel":"email","to":["a@example.com"],"subject":"hi","text":"hello"}`
	b := "{ \"text\": \"hello\",\n \"subject\":\"hi\", \"to\":[\"a@example.com\"], \"channel\":\"email\" }"
	c := `{"channel":"email","to":["a@example.com"],"subject":"hi","text":"other"}`
	ia, _ := Validate(appNamed(t, testApp), []byte(a), testNow)
	ib, _ := Validate(appNamed(t, testApp), []byte(b), testNow)
	ic, _ := Validate(appNamed(t, testApp), []byte(c), testNow)
	if ia.BodyHash != ib.BodyHash {
		t.Fatal("same request hashed differently")
	}
	if ia.BodyHash == ic.BodyHash {
		t.Fatal("different requests hashed equally")
	}
}

func TestValidateSMS(t *testing.T) {
	in, verr := Validate(appNamed(t, testApp), []byte(testSMS), testNow)
	if verr != nil {
		t.Fatal(verr)
	}
	if in.SMSBody != "hello" || in.Channel != model.ChannelSMS || in.Sender != "" || in.Recipients[0] != "+15551234567" {
		t.Fatalf("got %+v", in)
	}
}
