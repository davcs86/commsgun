package submission

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/mail"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/davcs86/commsgun/api/internal/config"
	"github.com/davcs86/commsgun/api/internal/enqueue"
	"github.com/davcs86/commsgun/api/internal/model"
	"github.com/davcs86/commsgun/api/internal/store"
)

const (
	envFrom = "authelia@example.com"
	rcpt    = "user@example.org"
	msgID   = "<reset-1@auth.example.com>"
)

var t0 = time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)

func testApp() *config.App {
	snd := config.Sender{Raw: "Authelia <authelia@example.com>", Address: envFrom}
	return &config.App{ID: "authelia", Key: strings.Repeat("k", 32), SMTPSubmission: true, SendWindow: time.Hour,
		Channels: map[model.Channel]config.ChannelCfg{
			model.ChannelEmail: {PerMinute: 100, PerDay: 1000, Senders: []config.Sender{snd}, DefaultSender: &snd},
		}}
}

func testConfig() *config.Config {
	a := testApp()
	return &config.Config{
		Apps:      map[string]config.App{a.ID: *a},
		Providers: map[model.Channel]*config.ProviderCfg{model.ChannelEmail: {Name: "fake", PerMinute: 10, PerDay: 100}},
	}
}

// msg joins header lines and a body with CRLF.
func msg(body string, headers ...string) []byte {
	return []byte(strings.Join(headers, "\r\n") + "\r\n\r\n" + body)
}

func base(extra ...string) []string {
	return append([]string{"From: Authelia <authelia@example.com>", "To: " + rcpt, "Subject: Reset your password",
		"Message-ID: " + msgID, "Date: Fri, 13 Nov 2026 12:00:00 +0000"}, extra...)
}

type built struct {
	Channel string   `json:"channel"`
	To      []string `json:"to"`
	From    string   `json:"from"`
	Subject string   `json:"subject"`
	Text    *string  `json:"text"`
	HTML    *string  `json:"html"`
	ReplyTo *string  `json:"reply_to"`
}

func build(t *testing.T, raw []byte) built {
	t.Helper()
	body, key, rej := BuildRequest(raw, envFrom, []string{rcpt}, testApp())
	if rej != nil {
		t.Fatalf("refused: %+v", *rej)
	}
	if want := sha256.Sum256([]byte(msgID)); key != hex.EncodeToString(want[:]) {
		t.Fatalf("key %q", key)
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	var b built
	if err := dec.Decode(&b); err != nil {
		t.Fatalf("body %s: %v", body, err)
	}
	return b
}

func refused(t *testing.T, name string, raw []byte, want Reply) {
	t.Helper()
	body, key, rej := BuildRequest(raw, envFrom, []string{rcpt}, testApp())
	if rej == nil || rej.Code != want.Code || rej.Enhanced != want.Enhanced {
		t.Errorf("%s: got %+v, want %d %v", name, rej, want.Code, want.Enhanced)
		return
	}
	if body != nil || key != "" {
		t.Errorf("%s: refusal returned body %q key %q", name, body, key)
	}
}

func str(p *string) string {
	if p == nil {
		return "<nil>"
	}
	return *p
}

// text/plain; multipart/alternative text+html → both fields
func TestBuildPlainAndAlternative(t *testing.T) {
	b := build(t, msg("Your link: https://x/1\r\n", base("Content-Type: text/plain; charset=utf-8")...))
	if b.Channel != "email" || len(b.To) != 1 || b.To[0] != rcpt || b.Subject != "Reset your password" ||
		str(b.Text) != "Your link: https://x/1\r\n" || b.HTML != nil || b.ReplyTo != nil {
		t.Fatalf("plain: %+v text %q", b, str(b.Text))
	}
	if a, err := mail.ParseAddress(b.From); err != nil || a.Address != envFrom || a.Name != "Authelia" {
		t.Fatalf("from %q", b.From)
	}

	alt := "--b1\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nplain\r\n" +
		"--b1\r\nContent-Type: text/html; charset=utf-8\r\n\r\n<p>html</p>\r\n--b1--\r\n"
	b = build(t, msg(alt, base("MIME-Version: 1.0", `Content-Type: multipart/alternative; boundary="b1"`)...))
	if str(b.Text) != "plain" || str(b.HTML) != "<p>html</p>" {
		t.Fatalf("alternative: text %q html %q", str(b.Text), str(b.HTML))
	}
	for h, want := range map[string]string{
		"Reply-To: ops@example.com":            "ops@example.com",
		"Reply-To: <ops@example.com>":          "ops@example.com",
		"Reply-To: Ops Desk <ops@example.com>": `"Ops Desk" <ops@example.com>`,
	} {
		if got := str(build(t, msg("x", base(h)...)).ReplyTo); got != want {
			t.Errorf("%s: reply_to %q, want %q", h, got, want)
		}
	}
	b = build(t, msg("<p>only</p>", base("Content-Type: text/html; charset=us-ascii")...))
	if b.Text != nil || str(b.HTML) != "<p>only</p>" {
		t.Fatalf("html only: %+v", b)
	}
}

// 7bit, 8bit, quoted-printable, base64 decoded; absent = 7bit
func TestTransferEncodings(t *testing.T) {
	ct := "Content-Type: text/plain; charset=utf-8"
	b64 := base64.StdEncoding.EncodeToString([]byte("héllo wörld, a longer line to wrap"))
	for name, c := range map[string]struct{ cte, body, want string }{
		"absent":           {"", "hello", "hello"},
		"7bit":             {"7bit", "hello", "hello"},
		"8bit":             {"8BIT", "héllo", "héllo"},
		"quoted-printable": {"quoted-printable", "h=C3=A9llo =\r\nw=C3=B6rld", "héllo wörld"},
		"base64":           {"Base64", b64[:20] + "\r\n" + b64[20:] + "\r\n", "héllo wörld, a longer line to wrap"},
	} {
		h := base(ct)
		if c.cte != "" {
			h = append(h, "Content-Transfer-Encoding: "+c.cte)
		}
		if got := str(build(t, msg(c.body, h...)).Text); got != c.want {
			t.Errorf("%s: text %q, want %q", name, got, c.want)
		}
	}
	refused(t, "binary", msg("hello", base(ct, "Content-Transfer-Encoding: binary")...), replyContent)
	refused(t, "bad base64", msg("!!!", base(ct, "Content-Transfer-Encoding: base64")...), replyContent)
}

// utf-8 ok; absent = us-ascii, a non-ASCII byte under it → 554 5.6.0; iso-8859-1 → 554 5.6.0
func TestCharsets(t *testing.T) {
	if got := str(build(t, msg("héllo", base("Content-Type: text/plain; charset=UTF-8")...)).Text); got != "héllo" {
		t.Errorf("utf-8: %q", got)
	}
	if got := str(build(t, msg("hello", base()...)).Text); got != "hello" {
		t.Errorf("absent, ASCII: %q", got)
	}
	if got := str(build(t, msg("hello", base("Content-Type: text/plain; charset=us-ascii")...)).Text); got != "hello" {
		t.Errorf("us-ascii: %q", got)
	}
	refused(t, "absent, non-ASCII", msg("héllo", base("Content-Type: text/plain")...), replyContent)
	refused(t, "no header, non-ASCII", msg("héllo", base()...), replyContent)
	refused(t, "iso-8859-1", msg("h\xe9llo", base("Content-Type: text/plain; charset=iso-8859-1")...), replyContent)
	refused(t, "invalid utf-8", msg("h\xe9llo", base("Content-Type: text/plain; charset=utf-8")...), replyContent)
}

// attachment, nested multipart, two text/plain, other type, no Message-ID, two Reply-To addresses,
// encoded CR/LF in Subject → 554 5.6.0
func TestRefusedContent(t *testing.T) {
	mp := func(parts ...string) []byte {
		body := ""
		for _, p := range parts {
			body += "--b1\r\n" + p + "\r\n"
		}
		return msg(body+"--b1--\r\n", base(`Content-Type: multipart/alternative; boundary="b1"`)...)
	}
	plain := "Content-Type: text/plain; charset=utf-8\r\n\r\nplain"
	noID := []string{"From: Authelia <authelia@example.com>", "Subject: s"}
	for name, raw := range map[string][]byte{
		"attachment part": mp(plain, "Content-Type: text/plain\r\nContent-Disposition: attachment; filename=a.txt\r\n\r\nx"),
		"attachment top":  msg("x", base("Content-Type: text/plain", "Content-Disposition: attachment")...),
		"nested multipart": mp(plain, "Content-Type: multipart/alternative; boundary=b2\r\n\r\n"+
			"--b2\r\nContent-Type: text/html\r\n\r\n<p>x</p>\r\n--b2--"),
		"two text/plain":   mp(plain, plain),
		"two text/html":    mp("Content-Type: text/html\r\n\r\n<p>1</p>", "Content-Type: text/html\r\n\r\n<p>2</p>"),
		"other part type":  mp(plain, "Content-Type: image/png\r\nContent-Transfer-Encoding: base64\r\n\r\niVBORw0KGgo="),
		"other top type":   msg("x", base("Content-Type: application/pdf")...),
		"multipart/mixed":  msg("--b1\r\n"+plain+"\r\n--b1--\r\n", base(`Content-Type: multipart/mixed; boundary="b1"`)...),
		"no Message-ID":    msg("x", noID...),
		"empty Message-ID": msg("x", append(noID, "Message-ID:   ")...),
		"two Message-IDs":  msg("x", base("Message-ID: <other@x>")...),
		"two Reply-To":     msg("x", base("Reply-To: a@example.com, b@example.com")...),
		"bad Reply-To":     msg("x", base("Reply-To: not an address")...),
		"encoded CR/LF":    msg("x", append(base()[:2], "Subject: =?utf-8?q?a=0D=0ABcc:_x@y?=", "Message-ID: "+msgID)...),
		"encoded LF":       msg("x", append(base()[:2], "Subject: =?utf-8?b?YQpi?=", "Message-ID: "+msgID)...),
		"not a message":    []byte("no header block"),
	} {
		refused(t, name, raw, replyContent)
	}
}

// other address → 550 5.7.1; same address other case or display name → ok, display name kept
func TestHeaderFromMustMatchEnvelope(t *testing.T) {
	rest := base()[1:]
	for name, from := range map[string]string{
		"other address":  "From: Authelia <attacker@example.com>",
		"two addresses":  "From: authelia@example.com, other@example.com",
		"unparseable":    "From: not an address",
		"display trick":  `From: "authelia@example.com" <attacker@example.com>`,
		"sub-domain":     "From: authelia@mail.example.com",
		"missing header": "X-Note: none",
	} {
		refused(t, name, msg("x", append([]string{from}, rest...)...), replyNotAllowed)
	}
	if _, _, rej := BuildRequest(msg("x", base()...), "someone@example.com", []string{rcpt}, testApp()); rej == nil || *rej != replyNotAllowed {
		t.Errorf("envelope not an allowed sender: %+v", rej)
	}
	for name, c := range map[string]struct{ from, wantName, wantFrom string }{
		"other case":   {"From: Authelia Bot <AUTHELIA@Example.COM>", "Authelia Bot", `"Authelia Bot" <AUTHELIA@Example.COM>`},
		"no name":      {"From: authelia@example.com", "", "authelia@example.com"},
		"bare, angled": {"From: <authelia@example.com>", "", "authelia@example.com"},
		"encoded name": {"From: =?utf-8?q?J=C3=BCrgen?= <authelia@example.com>", "Jürgen", "=?utf-8?q?J=C3=BCrgen?= <authelia@example.com>"},
	} {
		b := build(t, msg("x", append([]string{c.from}, rest...)...))
		if b.From != c.wantFrom {
			t.Errorf("%s: from %q, want %q", name, b.From, c.wantFrom)
		}
		a, err := mail.ParseAddress(b.From)
		if err != nil || !strings.EqualFold(a.Address, envFrom) || a.Name != c.wantName {
			t.Errorf("%s: from %q (%v)", name, b.From, err)
		}
	}
}

// To, Cc ignored; Bcc never forwarded
func TestRecipientsFromEnvelopeOnly(t *testing.T) {
	raw := msg("x", base("Cc: cc-person@example.net", "Bcc: hidden-person@example.net")...)
	rcpts := []string{"first@example.org", "second@example.org"}
	body, _, rej := BuildRequest(raw, envFrom, rcpts, testApp())
	if rej != nil {
		t.Fatal(*rej)
	}
	var b built
	if err := json.Unmarshal(body, &b); err != nil {
		t.Fatal(err)
	}
	if len(b.To) != 2 || b.To[0] != rcpts[0] || b.To[1] != rcpts[1] {
		t.Fatalf("to %v", b.To)
	}
	for _, bad := range []string{rcpt, "cc-person", "hidden-person"} {
		if bytes.Contains(body, []byte(bad)) {
			t.Errorf("request carries %q: %s", bad, body)
		}
	}
}

func TestSubjectRFC2047(t *testing.T) {
	head := base()[:1]
	tail := base()[3:]
	for raw, want := range map[string]string{
		"Subject: =?UTF-8?B?UmVzZXQgeW91ciBwYXNzd29yZCDinJM=?=":   "Reset your password ✓",
		"Subject: =?utf-8?q?R=C3=A9initialiser?= le mot de passe": "Réinitialiser le mot de passe",
		"Subject: =?utf-8?q?part_one?=\r\n =?utf-8?q?_part_two?=": "part one part two",
		"Subject: plain words":             "plain words",
		"Subject: =?us-ascii?q?ascii?=":    "ascii",
		"Subject: =?iso-8859-1?q?caf=E9?=": "café",
	} {
		h := append(append(append([]string{}, head...), raw), tail...)
		if got := build(t, msg("x", h...)).Subject; got != want {
			t.Errorf("%q: subject %q, want %q", raw, got, want)
		}
	}
	koi8 := append(append(append([]string{}, head...), "Subject: =?koi8-r?b?8NLJ18XU?="), tail...)
	refused(t, "koi8-r subject", msg("x", koi8...), replyContent)
}

// Each enqueue outcome maps to one SMTP reply; anything unlisted (internal, decode errors,
// too_many_recipients_for_limit) is 451 4.3.0.
func TestReplyFor(t *testing.T) {
	type r = struct {
		code int
		enh  [3]int
	}
	cases := []struct {
		f    *enqueue.Failure
		want r
	}{
		{nil, r{250, [3]int{2, 0, 0}}},
		{&enqueue.Failure{Status: 403, Code: "channel_not_allowed"}, r{550, [3]int{5, 7, 1}}},
		{&enqueue.Failure{Status: 422, Code: "sender_not_allowed", Field: "from"}, r{550, [3]int{5, 7, 1}}},
		{&enqueue.Failure{Status: 422, Code: "invalid_subject", Field: "subject"}, r{554, [3]int{5, 6, 0}}},
		{&enqueue.Failure{Status: 422, Code: "body_required", Field: "text"}, r{554, [3]int{5, 6, 0}}},
		{&enqueue.Failure{Status: 422, Code: "invalid_reply_to", Field: "reply_to"}, r{554, [3]int{5, 6, 0}}},
		{&enqueue.Failure{Status: 409, Code: "idempotency_conflict"}, r{554, [3]int{5, 6, 0}}},
		{&enqueue.Failure{Status: 429, Code: "rate_limited", Window: "minute", RetryAfter: time.Second}, r{451, [3]int{4, 7, 1}}},
		{&enqueue.Failure{Status: 503, Code: "unavailable", RetryAfter: time.Second}, r{451, [3]int{4, 3, 2}}},
		{&enqueue.Failure{Status: 422, Code: "invalid_json"}, r{451, [3]int{4, 3, 0}}},
		{&enqueue.Failure{Status: 422, Code: "unknown_field", Field: "cc"}, r{451, [3]int{4, 3, 0}}},
		{&enqueue.Failure{Status: 500, Code: "internal"}, r{451, [3]int{4, 3, 0}}},
		{&enqueue.Failure{Status: 422, Code: "too_many_recipients_for_limit", Field: "to"}, r{451, [3]int{4, 3, 0}}},
		{&enqueue.Failure{Status: 422, Code: "invalid_recipient", Field: "to"}, r{451, [3]int{4, 3, 0}}},
		{&enqueue.Failure{Status: 418, Code: "never_seen"}, r{451, [3]int{4, 3, 0}}},
	}
	for _, c := range cases {
		got := ReplyFor(c.f)
		if got.Code != c.want.code || got.Enhanced != c.want.enh || got.Text == "" {
			t.Errorf("%+v: got %+v, want %v", c.f, got, c.want)
		}
	}
}

type realStore struct {
	st *store.Store
	db *sql.DB
}

func openStore(t *testing.T) realStore {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	db, err := sql.Open("sqlite", "file:"+filepath.Join(dir, "commsgun.db")+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return realStore{st, db}
}

func (s realStore) count(t *testing.T, q string, args ...any) int {
	t.Helper()
	var n int
	if err := s.db.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func (s realStore) submit(t *testing.T, now time.Time, raw []byte) (enqueue.Outcome, Reply) {
	t.Helper()
	app := testApp()
	body, key, rej := BuildRequest(raw, envFrom, []string{rcpt}, app)
	if rej != nil {
		t.Fatalf("refused: %+v", *rej)
	}
	out, f := enqueue.SubmitWindowed(context.Background(), testConfig(), s.st, now, app, Namespace, key, body, "10.0.0.2", app.SendWindow)
	return out, ReplyFor(f)
}

// real store: window 3600s, provider paused; at +1h ExpireDue → expired, bodies NULL (anti-goal)
func TestSendWindowExpiresBodies(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	if err := s.st.PauseProvider(ctx, model.ChannelEmail, "fake", t0.Add(2*time.Hour), "test"); err != nil {
		t.Fatal(err)
	}
	out, rep := s.submit(t, t0, msg("Your code is 123456", base()...))
	if rep.Code != 250 || len(out.Result.MessageIDs) != 1 {
		t.Fatalf("reply %+v out %+v", rep, out)
	}
	id := out.Result.MessageIDs[0]
	if n := s.count(t, `SELECT COUNT(*) FROM messages WHERE id=? AND expires_at=? AND subject='Reset your password'`,
		id, t0.Add(time.Hour).UnixMilli()); n != 1 {
		t.Fatal("expires_at is not now + send window")
	}
	if n, err := s.st.ExpireDue(ctx, t0.Add(time.Hour-time.Millisecond)); err != nil || n != 0 {
		t.Fatalf("before the window: expired %d (%v)", n, err)
	}
	if n, err := s.st.ExpireDue(ctx, t0.Add(time.Hour)); err != nil || n != 1 {
		t.Fatalf("at the window: expired %d (%v)", n, err)
	}
	if n := s.count(t, `SELECT COUNT(*) FROM messages WHERE id=? AND state='expired' AND subject IS NULL
		AND body_text IS NULL AND body_html IS NULL AND reply_to IS NULL`, id); n != 1 {
		t.Fatal("bodies kept after expiry")
	}
}

// Review Focus 5: same message at t and t+10m, Message-ID case/space varied in the header line only → second is 250, one message
func TestDuplicateMessageID(t *testing.T) {
	s := openStore(t)
	first, rep := s.submit(t, t0, msg("link", base()...))
	if rep.Code != 250 || first.Result.Duplicate {
		t.Fatalf("first: %+v %+v", rep, first)
	}
	resend := base()
	resend[3] = "message-ID:   \t" + msgID + "  "
	again, rep := s.submit(t, t0.Add(10*time.Minute), msg("link", resend...))
	if rep.Code != 250 || rep.Enhanced != [3]int{2, 0, 0} || !again.Result.Duplicate ||
		again.Result.RequestID != first.Result.RequestID {
		t.Fatalf("resend: %+v %+v", rep, again)
	}
	resend[3] = "MESSAGE-ID:\r\n " + msgID
	if _, rep = s.submit(t, t0.Add(20*time.Minute), msg("link", resend...)); rep.Code != 250 {
		t.Fatalf("folded resend: %+v", rep)
	}
	if n := s.count(t, `SELECT COUNT(*) FROM messages`); n != 1 {
		t.Fatalf("%d messages", n)
	}
	if n := s.count(t, `SELECT COUNT(*) FROM requests WHERE idempotency_key LIKE 'smtp/%'`); n != 1 {
		t.Fatalf("%d smtp/ requests", n)
	}
	if _, rep = s.submit(t, t0.Add(30*time.Minute), msg("other content", base()...)); rep != replyContent {
		t.Fatalf("same Message-ID, other content: %+v", rep)
	}
}
