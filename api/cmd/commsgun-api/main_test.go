package main

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/textproto"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/davcs86/commsgun/api/internal/config"
	"github.com/davcs86/commsgun/api/internal/constants"
	"github.com/davcs86/commsgun/api/internal/model"
	"github.com/davcs86/commsgun/api/internal/provider"
	"github.com/davcs86/commsgun/api/internal/store"
	"github.com/davcs86/commsgun/api/internal/worker"
)

const (
	appKey     = "app-key-0123456789abcdef0123456789abcdef"
	adminToken = "admin-token-0123456789abcdef0123456789abcdef"
	sender     = "Alerts <alerts-unique@sender.example>"
	subject    = "subject-unique-7731"
	textBody   = "text-body-unique-5519"
	rcptA      = "alice-unique@rcpt.example"
	rcptB      = "bob-unique@rcpt.example"
	failedText = "provider-said-unique-9042"
	adminPass  = "admin-password-unique-8823"
)

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

func configJSON() string {
	return `{"apps":[{"id":"app1","key":"` + appKey + `","channels":{"email":{"per_minute":100,"per_day":1000,` +
		`"senders":["` + sender + `"],"default_sender":"` + sender + `"}}}],` +
		`"providers":{"email":{"name":"fake","per_minute":100,"per_day":1000},"sms":null}}`
}

func envFor(dir string, extra map[string]string) func(string) string {
	m := map[string]string{
		"COMMSGUN_CONFIG": configJSON(), "COMMSGUN_ALLOW_FAKE": "1",
		"ADMIN_API_TOKEN": adminToken, "DATA_DIR": dir,
		"ENQUEUE_ADDR": "127.0.0.1:0", "ADMIN_ADDR": "127.0.0.1:0",
	}
	for k, v := range extra {
		m[k] = v
	}
	return func(k string) string { return m[k] }
}

type proc struct {
	out        *lockedBuf
	enq, adm   string
	cancel     context.CancelFunc
	done       chan error
	dir        string
	stoppedErr error
	session    string
}

func start(t *testing.T, wrap func(provider.Provider) provider.Provider) *proc {
	t.Helper()
	return startEnv(t, wrap, nil)
}

func startEnv(t *testing.T, wrap func(provider.Provider) provider.Provider, extra map[string]string) *proc {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	p := &proc{out: &lockedBuf{}, cancel: cancel, done: make(chan error, 1), dir: t.TempDir()}
	go func() { p.done <- runWith(ctx, envFor(p.dir, extra), p.out, wrap) }()
	t.Cleanup(func() { p.stop(t) })
	waitFor(t, 5*time.Second, "listeners", func() bool {
		p.enq, p.adm = p.addr("enqueue"), p.addr("admin")
		return p.enq != "" && p.adm != ""
	})
	return p
}

func (p *proc) addr(listener string) string {
	for _, line := range strings.Split(p.out.String(), "\n") {
		var rec struct{ Msg, Listener, Addr string }
		if json.Unmarshal([]byte(line), &rec) == nil && rec.Msg == "listening" && rec.Listener == listener {
			return rec.Addr
		}
	}
	return ""
}

func (p *proc) stop(t *testing.T) {
	t.Helper()
	p.cancel()
	if p.done == nil {
		return
	}
	select {
	case p.stoppedErr = <-p.done:
		p.done = nil
	case <-time.After(constants.LeaseDuration):
		t.Fatal("run did not return after cancel")
	}
}

func waitFor(t *testing.T, d time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func do(t *testing.T, method, url string, hdr map[string]string, body string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func enqueueBody() string {
	return `{"channel":"email","to":["` + rcptA + `","` + rcptB + `"],"subject":"` + subject + `","text":"` + textBody + `"}`
}

func enqueueHeaders(idem string) map[string]string {
	return map[string]string{
		"X-Commsgun-App": "app1", "Authorization": "Bearer " + appKey,
		"Idempotency-Key": idem, "Content-Type": "application/json",
	}
}

// login creates a user and a session through the admin API once (import, then sessions/local).
func (p *proc) login(t *testing.T) string {
	t.Helper()
	if p.session != "" {
		return p.session
	}
	bearer := map[string]string{"Authorization": "Bearer " + adminToken}
	creds := `{"username":"admin","password":"` + adminPass + `"}`
	if code, body := do(t, http.MethodPost, "http://"+p.adm+"/admin/v1/bootstrap/import", bearer, creds); code != http.StatusOK {
		t.Fatalf("import: %d %s", code, body)
	}
	code, body := do(t, http.MethodPost, "http://"+p.adm+"/admin/v1/sessions/local", bearer, creds)
	var ses struct{ Token string }
	if code != http.StatusCreated || json.Unmarshal([]byte(body), &ses) != nil || ses.Token == "" {
		t.Fatalf("sessions/local: %d %s", code, body)
	}
	p.session = ses.Token
	return p.session
}

func (p *proc) adminMessages(t *testing.T) []model.Message {
	t.Helper()
	hdr := map[string]string{"Authorization": "Bearer " + adminToken, "Commsgun-Session": p.login(t)}
	code, body := do(t, http.MethodGet, "http://"+p.adm+"/admin/v1/messages", hdr, "")
	if code != http.StatusOK {
		t.Fatalf("admin messages: %d %s", code, body)
	}
	var out struct{ Items []model.Message }
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatal(err)
	}
	return out.Items
}

func waitHealthy(t *testing.T, addr string) {
	t.Helper()
	waitFor(t, 5*time.Second, "healthz on "+addr, func() bool {
		code, _ := do(t, http.MethodGet, "http://"+addr+"/healthz", nil, "")
		return code == http.StatusOK
	})
}

func TestEndToEndWithFakeProvider(t *testing.T) {
	p := start(t, nil)
	waitHealthy(t, p.enq)
	waitHealthy(t, p.adm)

	code, body := do(t, http.MethodPost, "http://"+p.enq+"/v1/messages", enqueueHeaders("k1"), enqueueBody())
	if code != http.StatusAccepted {
		t.Fatalf("enqueue: %d %s", code, body)
	}
	waitFor(t, 5*time.Second, "both messages sent", func() bool {
		items := p.adminMessages(t)
		n := 0
		for _, m := range items {
			if m.State == model.StateSent {
				n++
			}
		}
		return len(items) == 2 && n == 2
	})

	for _, tc := range []struct {
		name, method, url string
		want              int
	}{
		{"admin path on enqueue listener", http.MethodGet, "http://" + p.enq + "/admin/v1/messages", http.StatusNotFound},
		{"enqueue path on admin listener", http.MethodPost, "http://" + p.adm + "/v1/messages", http.StatusNotFound},
		{"backlog on enqueue listener", http.MethodGet, "http://" + p.enq + "/healthz/backlog", http.StatusOK},
		{"backlog on admin listener", http.MethodGet, "http://" + p.adm + "/healthz/backlog", http.StatusNotFound},
		{"admin API without token", http.MethodGet, "http://" + p.adm + "/admin/v1/messages", http.StatusUnauthorized},
		{"unknown path", http.MethodGet, "http://" + p.enq + "/nope", http.StatusNotFound},
	} {
		if code, body := do(t, tc.method, tc.url, nil, ""); code != tc.want {
			t.Errorf("%s: got %d (%s), want %d", tc.name, code, body, tc.want)
		} else if tc.want == http.StatusNotFound && !strings.Contains(body, "not_found") {
			t.Errorf("%s: 404 body is not JSON error: %q", tc.name, body)
		}
	}
}

func TestLogsCarryNoSecretsOrAddresses(t *testing.T) {
	p := start(t, func(pr provider.Provider) provider.Provider {
		pr.(*provider.Fake).Script(provider.SendResult{
			Outcome: provider.Permanent, ErrorType: model.ErrProviderRejected,
			Status: 422, ProviderError: failedText, Message: failedText + " " + rcptA,
		})
		return pr
	})
	waitHealthy(t, p.enq)

	if code, _ := do(t, http.MethodPost, "http://"+p.enq+"/v1/messages", enqueueHeaders("k1"), enqueueBody()); code != http.StatusAccepted {
		t.Fatalf("enqueue: %d", code)
	}
	bad := enqueueHeaders("k2")
	bad["Authorization"] = "Bearer wrong-" + appKey
	if code, _ := do(t, http.MethodPost, "http://"+p.enq+"/v1/messages", bad, enqueueBody()); code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", code)
	}
	if code, _ := do(t, http.MethodPost, "http://"+p.enq+"/v1/messages", enqueueHeaders("k3"), `{"channel":"email","to":["`+rcptA+`"],"subject":"`+subject+`"}`); code != http.StatusUnprocessableEntity {
		t.Fatalf("want 422, got %d", code)
	}
	waitFor(t, 5*time.Second, "one failed and one sent", func() bool {
		var failed, sent int
		for _, m := range p.adminMessages(t) {
			switch m.State {
			case model.StateFailed:
				failed++
			case model.StateSent:
				sent++
			}
		}
		return failed == 1 && sent == 1
	})
	p.stop(t)

	logs := p.out.String()
	if !strings.Contains(logs, "send attempt") {
		t.Fatalf("expected worker log lines, got:\n%s", logs)
	}
	for name, secret := range map[string]string{
		"app key": appKey, "admin token": adminToken, "recipient A": rcptA, "recipient B": rcptB,
		"sender": "alerts-unique@sender.example", "subject": subject, "text body": textBody, "provider text": failedText,
		"admin password": adminPass, "session token": p.session,
	} {
		if strings.Contains(logs, secret) {
			t.Errorf("log contains %s", name)
		}
	}
}

func TestRefusesToStartOnBadConfig(t *testing.T) {
	out := &lockedBuf{}
	env := func(k string) string {
		if k == "ADMIN_API_TOKEN" {
			return adminToken
		}
		return map[string]string{"COMMSGUN_CONFIG": "{}", "DATA_DIR": t.TempDir()}[k]
	}
	err := run(context.Background(), env, out)
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "apps") || strings.Contains(err.Error(), adminToken) {
		t.Errorf("error should name the problem and hold no secret: %v", err)
	}
	if strings.Contains(out.String(), "listening") {
		t.Errorf("bound a port despite bad config:\n%s", out.String())
	}
}

type slowProvider struct {
	provider.Provider
	entered chan struct{}
	once    sync.Once
}

func (s *slowProvider) Send(ctx context.Context, m model.Message, b provider.Body) provider.SendResult {
	s.once.Do(func() { close(s.entered) })
	<-ctx.Done()
	return provider.SendResult{Outcome: provider.Retryable, ErrorType: model.ErrTimeout, Message: "interrupted"}
}

func TestGracefulShutdown(t *testing.T) {
	slow := &slowProvider{entered: make(chan struct{})}
	p := start(t, func(pr provider.Provider) provider.Provider { slow.Provider = pr; return slow })
	waitHealthy(t, p.enq)
	if code, _ := do(t, http.MethodPost, "http://"+p.enq+"/v1/messages", enqueueHeaders("k1"), enqueueBody()); code != http.StatusAccepted {
		t.Fatalf("enqueue: %d", code)
	}
	select {
	case <-slow.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("send never started")
	}

	begin := time.Now()
	p.stop(t)
	if p.stoppedErr != nil {
		t.Fatalf("run returned %v on clean shutdown", p.stoppedErr)
	}
	if d := time.Since(begin); d > constants.LeaseDuration {
		t.Fatalf("shutdown took %v", d)
	}

	st, err := store.Open(p.dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	items, _, err := st.ListMessages(context.Background(), store.Filter{From: time.Now().Add(-time.Hour), To: time.Now().Add(time.Hour)}, "", 10)
	if err != nil || len(items) != 2 {
		t.Fatalf("items=%d err=%v", len(items), err)
	}
	for _, m := range items {
		if m.State != model.StateSent && m.State != model.StateQueued {
			t.Errorf("message %s in state %s after shutdown, want sent or queued", m.ID, m.State)
		}
	}
}

func TestBuildProvidersOneCheckerPerChannel(t *testing.T) {
	cfg := &config.Config{Providers: map[model.Channel]*config.ProviderCfg{
		model.ChannelEmail: {Name: "fake", PerMinute: 1, PerDay: 1},
		model.ChannelSMS:   {Name: "fake", PerMinute: 1, PerDay: 1},
	}}
	providers, checkers, err := buildProviders(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(checkers) != len(providers) {
		t.Fatalf("%d checkers for %d channels sharing a provider name", len(checkers), len(providers))
	}
	for ch, p := range providers {
		if c := checkers[worker.CheckerKey{Channel: ch, Provider: "fake"}]; c != p.(provider.StatusChecker) {
			t.Fatalf("%s checker is not that channel's provider instance", ch)
		}
	}
}

// ENQUEUE_ADDR/ADMIN_ADDR 127.0.0.1:0 honoured; no smtp listener by default
func TestAddressesFromEnv(t *testing.T) {
	p := start(t, nil)
	for _, a := range []string{p.enq, p.adm} {
		if host, port, err := net.SplitHostPort(a); err != nil || host != "127.0.0.1" || port == "0" || port == "9200" || port == "9201" {
			t.Errorf("listener on %q, want 127.0.0.1 and an ephemeral port", a)
		}
	}
	waitHealthy(t, p.enq)
	if a := p.addr("smtp"); a != "" {
		t.Errorf("smtp listener on %q without SMTP_ADDR", a)
	}
}

// writeCert writes a self-signed certificate and its key as PEM files under dir.
func writeCert(t *testing.T, dir string) (certFile, keyFile string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "smtp.test"},
		DNSNames: []string{"smtp.test"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	kder, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	certFile, keyFile = filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	for f, b := range map[string]*pem.Block{certFile: {Type: "CERTIFICATE", Bytes: der}, keyFile: {Type: "EC PRIVATE KEY", Bytes: kder}} {
		if err := os.WriteFile(f, pem.EncodeToMemory(b), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return certFile, keyFile
}

// SMTP_ADDR + test cert files → "listening" smtp; unreadable cert → run() error before any bind
func TestSMTPOnWithCertificate(t *testing.T) {
	cert, key := writeCert(t, t.TempDir())
	p := startEnv(t, nil, map[string]string{"SMTP_ADDR": "127.0.0.1:0", "SMTP_TLS_CERT_FILE": cert, "SMTP_TLS_KEY_FILE": key})
	var smtpAddr string
	waitFor(t, 5*time.Second, "smtp listener", func() bool { smtpAddr = p.addr("smtp"); return smtpAddr != "" })
	conn, err := net.DialTimeout("tcp", smtpAddr, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	tp := textproto.NewConn(conn)
	if _, _, err := tp.ReadResponse(220); err != nil {
		t.Fatalf("greeting: %v", err)
	}
	if err := tp.PrintfLine("EHLO client.test"); err != nil {
		t.Fatal(err)
	}
	if _, msg, err := tp.ReadResponse(250); err != nil || !strings.Contains(msg, "STARTTLS") {
		t.Fatalf("EHLO: %q %v", msg, err)
	}
	if err := tp.PrintfLine("QUIT"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := tp.ReadResponse(221); err != nil {
		t.Fatalf("QUIT: %v", err)
	}
	p.stop(t)
	if p.stoppedErr != nil {
		t.Fatalf("run returned %v on clean shutdown", p.stoppedErr)
	}

	out := &lockedBuf{}
	missing := filepath.Join(t.TempDir(), "absent.pem")
	err = run(context.Background(), envFor(t.TempDir(), map[string]string{
		"SMTP_ADDR": "127.0.0.1:0", "SMTP_TLS_CERT_FILE": missing, "SMTP_TLS_KEY_FILE": key}), out)
	if err == nil || !strings.Contains(err.Error(), "SMTP_TLS") {
		t.Fatalf("unreadable cert: err %v", err)
	}
	if strings.Contains(out.String(), "listening") {
		t.Errorf("bound a port despite an unreadable certificate:\n%s", out.String())
	}
}
