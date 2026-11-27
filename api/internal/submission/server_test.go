package submission

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"math/big"
	"net"
	"net/textproto"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/davcs86/commsgun/api/internal/clock"
	"github.com/davcs86/commsgun/api/internal/config"
	"github.com/davcs86/commsgun/api/internal/model"
)

const (
	srvName  = "smtp.commsgun.test"
	appKey   = "authelia-key-0123456789abcdef0123456789"
	webKey   = "web-key-0123456789abcdef0123456789abcdef"
	pm2Key   = "pm2-key-0123456789abcdef0123456789abcdef"
	pd1Key   = "pd1-key-0123456789abcdef0123456789abcdef"
	username = "attempted-user-unique-4410"
)

// testPKI is a CA and a server certificate for srvName, made with crypto/x509.
type testPKI struct {
	server tls.Certificate
	roots  *x509.CertPool
}

func newPKI(t *testing.T) testPKI {
	t.Helper()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "commsgun test CA"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	caDER, err := x509.CreateCertificate(rand.Reader, ca, ca, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatal(err)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	leaf := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: srvName},
		DNSNames: []string{srvName}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, leaf, caCert, &key.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(caCert)
	return testPKI{server: tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, roots: roots}
}

type lockedBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func srvConfig() *config.Config {
	snd := config.Sender{Raw: "Authelia <authelia@example.com>", Address: envFrom}
	email := func(pm, pd int) map[model.Channel]config.ChannelCfg {
		return map[model.Channel]config.ChannelCfg{
			model.ChannelEmail: {PerMinute: pm, PerDay: pd, Senders: []config.Sender{snd}, DefaultSender: &snd}}
	}
	apps := []config.App{
		{ID: "authelia", Key: appKey, SMTPSubmission: true, SendWindow: time.Hour, Channels: email(100, 1000)},
		{ID: "web", Key: webKey, SendWindow: time.Hour, Channels: email(100, 1000)},
		{ID: "pm2", Key: pm2Key, SMTPSubmission: true, SendWindow: time.Hour, Channels: email(2, 1000)},
		{ID: "pd1", Key: pd1Key, SMTPSubmission: true, SendWindow: time.Hour, Channels: email(100, 1)},
	}
	c := &config.Config{Apps: map[string]config.App{},
		Providers: map[model.Channel]*config.ProviderCfg{model.ChannelEmail: {Name: "fake", PerMinute: 100, PerDay: 1000}}}
	for _, a := range apps {
		c.Apps[a.ID] = a
	}
	return c
}

type harness struct {
	addr string
	pki  testPKI
	logs *lockedBuf
	st   realStore
}

func startServer(t *testing.T) *harness {
	t.Helper()
	h := &harness{pki: newPKI(t), logs: &lockedBuf{}, st: openStore(t)}
	log := slog.New(slog.NewJSONHandler(h.logs, nil))
	srv := NewServer(srvConfig(), h.st.st, clock.NewFake(t0), log, &tls.Config{Certificates: []tls.Certificate{h.pki.server}})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	h.addr = ln.Addr().String()
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { srv.Close() })
	return h
}

type client struct {
	t    *testing.T
	conn net.Conn
	tp   *textproto.Conn
}

func (h *harness) dial(t *testing.T) *client {
	t.Helper()
	conn, err := net.DialTimeout("tcp", h.addr, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))
	c := &client{t: t, conn: conn, tp: textproto.NewConn(conn)}
	t.Cleanup(func() { c.conn.Close() })
	c.expect(220, "")
	return c
}

func (c *client) read() (int, string) {
	c.t.Helper()
	code, msg, err := c.tp.ReadResponse(0)
	if err != nil {
		c.t.Fatalf("read reply: %v", err)
	}
	return code, msg
}

// cmd sends one line and returns the reply code and its text, enhanced code first.
func (c *client) cmd(format string, args ...any) (int, string) {
	c.t.Helper()
	if err := c.tp.PrintfLine(format, args...); err != nil {
		c.t.Fatal(err)
	}
	return c.read()
}

func (c *client) expect(code int, enhanced string) string {
	c.t.Helper()
	got, msg := c.read()
	if got != code || !strings.HasPrefix(msg, enhanced) {
		c.t.Fatalf("got %d %q, want %d %s", got, msg, code, enhanced)
	}
	return msg
}

func (c *client) want(code int, enhanced, format string, args ...any) string {
	c.t.Helper()
	got, msg := c.cmd(format, args...)
	if got != code || enhanced != "" && !strings.HasPrefix(msg, enhanced+" ") {
		c.t.Fatalf("%s: got %d %q, want %d %s", fmt.Sprintf(format, args...), got, msg, code, enhanced)
	}
	return msg
}

func (c *client) starttls(cfg *tls.Config) error {
	c.t.Helper()
	c.want(220, "2.0.0", "STARTTLS")
	tc := tls.Client(c.conn, cfg)
	if err := tc.Handshake(); err != nil {
		return err
	}
	c.conn, c.tp = tc, textproto.NewConn(tc)
	return nil
}

func (h *harness) clientTLS() *tls.Config {
	return &tls.Config{RootCAs: h.pki.roots, ServerName: srvName}
}

func plain(authz, user, pass string) string {
	return base64.StdEncoding.EncodeToString([]byte(authz + "\x00" + user + "\x00" + pass))
}

// tlsClient is connected, upgraded and greeted, not yet authenticated.
func (h *harness) tlsClient(t *testing.T) *client {
	t.Helper()
	c := h.dial(t)
	c.want(250, "", "EHLO client.test")
	if err := c.starttls(h.clientTLS()); err != nil {
		t.Fatal(err)
	}
	c.want(250, "", "EHLO client.test")
	return c
}

func (h *harness) authed(t *testing.T, app, key string) *client {
	t.Helper()
	c := h.tlsClient(t)
	c.want(235, "2.0.0", "AUTH PLAIN %s", plain("", app, key))
	return c
}

func message(id, subject string) string {
	return strings.Join([]string{"From: Authelia <authelia@example.com>", "To: " + rcpt, "Subject: " + subject,
		"Message-ID: " + id, "", "Your code is 123456"}, "\r\n")
}

// send runs one full transaction and returns the DATA reply.
func (c *client) send(from string, rcpts []string, data string) (int, string) {
	c.t.Helper()
	c.want(250, "2.0.0", "MAIL FROM:<%s>", from)
	for _, r := range rcpts {
		c.want(250, "2.0.0", "RCPT TO:<%s>", r)
	}
	c.want(354, "", "DATA")
	w := c.tp.DotWriter()
	if _, err := w.Write([]byte(data)); err != nil {
		c.t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		c.t.Fatal(err)
	}
	return c.read()
}

// EHLO advertises STARTTLS, not AUTH; MAIL before STARTTLS → 530 5.7.0; AUTH before STARTTLS → 523 5.7.10
func TestSTARTTLSRequired(t *testing.T) {
	h := startServer(t)
	c := h.dial(t)
	ehlo := c.want(250, "", "EHLO client.test")
	if !strings.Contains(ehlo, "STARTTLS") || strings.Contains(ehlo, "AUTH") {
		t.Fatalf("EHLO before TLS: %q", ehlo)
	}
	c.want(530, "5.7.0", "MAIL FROM:<%s>", envFrom)
	c.want(523, "5.7.10", "AUTH PLAIN %s", plain("", "authelia", appKey))
	if err := c.starttls(h.clientTLS()); err != nil {
		t.Fatal(err)
	}
	ehlo = c.want(250, "", "EHLO client.test")
	if strings.Contains(ehlo, "STARTTLS") || !strings.Contains(ehlo, "AUTH PLAIN") || strings.Contains(ehlo, "LOGIN") ||
		!strings.Contains(ehlo, "SIZE 262144") {
		t.Fatalf("EHLO after TLS: %q", ehlo)
	}
	c.want(504, "5.7.4", "AUTH LOGIN")
}

// wrong key, unknown app, non-smtp app, mismatched authzid → 535 5.7.8, identical text
func TestAuthOneReply(t *testing.T) {
	h := startServer(t)
	c := h.tlsClient(t)
	var texts []string
	for _, cred := range [][3]string{
		{"", "authelia", "wrong-" + appKey},
		{"", "nope", appKey},
		{"", "web", webKey},
		{"pm2", "authelia", appKey},
	} {
		texts = append(texts, c.want(535, "5.7.8", "AUTH PLAIN %s", plain(cred[0], cred[1], cred[2])))
	}
	for _, txt := range texts[1:] {
		if txt != texts[0] {
			t.Errorf("reply text %q differs from %q", txt, texts[0])
		}
	}
	c.want(235, "2.0.0", "AUTH PLAIN %s", plain("authelia", "authelia", appKey))
}

// after STARTTLS, MAIL before AUTH → 530 5.7.0
func TestMailBeforeAuth(t *testing.T) {
	h := startServer(t)
	c := h.tlsClient(t)
	c.want(530, "5.7.0", "MAIL FROM:<%s>", envFrom)
}

// MAIL FROM not allowed → 550 5.7.1; malformed RCPT → 553 5.1.3; 11th RCPT → 452 4.5.3;
// app per_minute 2: 3rd RCPT → 452 4.5.3; app per_day 1: 2nd RCPT → 452 4.5.3; DATA over SIZE → 552 5.3.4
func TestEnvelopeReplies(t *testing.T) {
	h := startServer(t)
	c := h.authed(t, "authelia", appKey)
	c.want(550, "5.7.1", "MAIL FROM:<other@example.com>")
	c.want(250, "2.0.0", "MAIL FROM:<%s>", envFrom)
	c.want(553, "5.1.3", "RCPT TO:<postmaster>")
	for i := range 10 {
		c.want(250, "2.0.0", "RCPT TO:<u%d@example.org>", i)
	}
	c.want(452, "4.5.3", "RCPT TO:<u10@example.org>")
	c.want(250, "2.0.0", "RSET")

	pm := h.authed(t, "pm2", pm2Key)
	pm.want(250, "2.0.0", "MAIL FROM:<%s>", envFrom)
	pm.want(250, "2.0.0", "RCPT TO:<a@example.org>")
	pm.want(250, "2.0.0", "RCPT TO:<b@example.org>")
	pm.want(452, "4.5.3", "RCPT TO:<c@example.org>")

	pd := h.authed(t, "pd1", pd1Key)
	pd.want(250, "2.0.0", "MAIL FROM:<%s>", envFrom)
	pd.want(250, "2.0.0", "RCPT TO:<a@example.org>")
	pd.want(452, "4.5.3", "RCPT TO:<b@example.org>")

	c.want(250, "2.0.0", "MAIL FROM:<%s>", envFrom)
	c.want(250, "2.0.0", "RCPT TO:<%s>", rcpt)
	c.want(354, "", "DATA")
	w := c.tp.DotWriter()
	big := message("<big@auth.example.com>", "Big") + strings.Repeat("\r\n"+strings.Repeat("x", 98), 2700)
	if _, err := w.Write([]byte(big)); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	c.expect(552, "5.3.4")
	if n := h.st.count(t, `SELECT COUNT(*) FROM messages`); n != 0 {
		t.Fatalf("%d messages stored", n)
	}
}

// a DATA line over go-smtp's 2000-character limit → 554 5.6.0, nothing stored
func TestDataLongLineRefused(t *testing.T) {
	h := startServer(t)
	c := h.authed(t, "authelia", appKey)
	code, msg := c.send(envFrom, []string{rcpt}, message("<long@auth.example.com>", "Long")+strings.Repeat("x", 2100))
	if code != 554 || !strings.HasPrefix(msg, "5.6.0 ") {
		t.Fatalf("DATA: %d %q, want 554 5.6.0", code, msg)
	}
	if n := h.st.count(t, `SELECT COUNT(*) FROM messages`); n != 0 {
		t.Fatalf("%d messages stored", n)
	}
}

// real store: 250 2.0.0, one message per RCPT, request key "smtp/<hex>", expires_at = now+window
func TestSubmitQueues(t *testing.T) {
	h := startServer(t)
	c := h.authed(t, "authelia", appKey)
	if code, msg := c.send(envFrom, []string{rcpt, "second@example.org"}, message(msgID, "Reset")); code != 250 ||
		!strings.HasPrefix(msg, "2.0.0 ") {
		t.Fatalf("DATA: %d %q", code, msg)
	}
	if n := h.st.count(t, `SELECT COUNT(*) FROM messages WHERE app='authelia' AND expires_at=? AND source_ip='127.0.0.1'`,
		t0.Add(time.Hour).UnixMilli()); n != 2 {
		t.Fatalf("%d messages with expires_at = now + window", n)
	}
	_, hexKey, _ := BuildRequest([]byte(message(msgID, "Reset")), envFrom, []string{rcpt}, testApp())
	if n := h.st.count(t, `SELECT COUNT(*) FROM requests WHERE app='authelia' AND idempotency_key=?`, "smtp/"+hexKey); n != 1 {
		t.Fatalf("%d requests under smtp/<hex>", n)
	}
	if code, _ := c.send(envFrom, []string{rcpt, "second@example.org"}, message(msgID, "Reset")); code != 250 {
		t.Fatalf("resend: %d", code)
	}
	if n := h.st.count(t, `SELECT COUNT(*) FROM messages`); n != 2 {
		t.Fatalf("resend made %d messages", n)
	}
}

// 9th concurrent session → 451 4.3.2; MAIL of message 11 on one connection → 451 4.3.2
func TestSessionCaps(t *testing.T) {
	h := startServer(t)
	var open []*client
	for range 8 {
		c := h.dial(t)
		c.want(250, "", "EHLO client.test")
		open = append(open, c)
	}
	ninth := h.dial(t)
	ninth.want(451, "4.3.2", "EHLO client.test")
	open[0].want(221, "2.0.0", "QUIT")
	deadline := time.Now().Add(5 * time.Second)
	for {
		code, msg := ninth.cmd("EHLO client.test")
		if code == 250 {
			break
		}
		if code != 451 || !strings.HasPrefix(msg, "4.3.2 ") || time.Now().After(deadline) {
			t.Fatalf("after a slot freed: %d %q", code, msg)
		}
		time.Sleep(20 * time.Millisecond)
	}
	for _, c := range open[1:] {
		c.want(221, "2.0.0", "QUIT")
	}

	c := h.authed(t, "authelia", appKey)
	for i := range 10 {
		if code, msg := c.send(envFrom, []string{rcpt}, message(fmt.Sprintf("<m%d@auth.example.com>", i), "Reset")); code != 250 {
			t.Fatalf("message %d: %d %q", i+1, code, msg)
		}
	}
	c.want(451, "4.3.2", "MAIL FROM:<%s>", envFrom)
}

func TestTLS11Refused(t *testing.T) {
	h := startServer(t)
	c := h.dial(t)
	c.want(250, "", "EHLO client.test")
	cfg := h.clientTLS()
	cfg.MinVersion, cfg.MaxVersion = tls.VersionTLS10, tls.VersionTLS11
	if err := c.starttls(cfg); err == nil {
		t.Fatal("TLS 1.1 handshake succeeded")
	}
}

// no address, subject, Message-ID, key or attempted username
func TestSMTPLogsCarryNoSecrets(t *testing.T) {
	h := startServer(t)
	bad := h.tlsClient(t)
	bad.want(535, "5.7.8", "AUTH PLAIN %s", plain("", username, appKey))
	bad.want(221, "2.0.0", "QUIT")

	const subject, id = "subject-unique-3391", "<message-id-unique-2207@auth.example.com>"
	c := h.authed(t, "authelia", appKey)
	if code, _ := c.send(envFrom, []string{rcpt}, message(id, subject)); code != 250 {
		t.Fatalf("DATA: %d", code)
	}
	c.want(221, "2.0.0", "QUIT")

	var lines []map[string]any
	deadline := time.Now().Add(5 * time.Second)
	for len(lines) < 4 && time.Now().Before(deadline) {
		lines = lines[:0]
		for _, l := range strings.Split(strings.TrimSpace(h.logs.String()), "\n") {
			var rec map[string]any
			if json.Unmarshal([]byte(l), &rec) == nil && rec["msg"] == "smtp session" {
				lines = append(lines, rec)
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(lines) != 4 {
		t.Fatalf("want 4 session lines (2 starttls, 2 tls), got:\n%s", h.logs.String())
	}
	var failed, queued map[string]any
	for _, l := range lines {
		switch l["outcome"] {
		case "auth_failed":
			failed = l
		case "authenticated":
			queued = l
		}
	}
	if failed == nil || failed["app"] != nil || failed["last_reply"] != float64(535) {
		t.Errorf("failed-auth line: %v", failed)
	}
	if queued == nil || queued["app"] != "authelia" || queued["queued"] != float64(1) || queued["recipients"] != float64(1) {
		t.Errorf("queued line: %v", queued)
	}
	logs := h.logs.String()
	for name, secret := range map[string]string{
		"recipient": rcpt, "sender": envFrom, "subject": subject, "message-id": "message-id-unique-2207",
		"key": appKey, "username": username, "peer": "127.0.0.1",
	} {
		if strings.Contains(logs, secret) {
			t.Errorf("log contains %s:\n%s", name, logs)
		}
	}
}
