package worker

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/davcs86/commsgun/api/internal/clock"
	"github.com/davcs86/commsgun/api/internal/config"
	"github.com/davcs86/commsgun/api/internal/constants"
	"github.com/davcs86/commsgun/api/internal/health"
	"github.com/davcs86/commsgun/api/internal/model"
	"github.com/davcs86/commsgun/api/internal/provider"
	"github.com/davcs86/commsgun/api/internal/store"
)

var t0 = time.Date(2026, 11, 13, 12, 0, 30, 0, time.UTC)

const rcpt = "someone@example.org"

func half() float64 { return 0.5 }

type env struct {
	st   *store.Store
	db   *sql.DB
	clk  *clock.Fake
	fake *provider.Fake
	cfg  *config.Config
	logs *bytes.Buffer
}

func newEnv(t *testing.T, perMinute, perDay int) *env {
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
	cfg := &config.Config{Providers: map[model.Channel]*config.ProviderCfg{
		model.ChannelEmail: {Name: "fake", PerMinute: perMinute, PerDay: perDay},
	}}
	return &env{st: st, db: db, clk: clock.NewFake(t0), fake: provider.NewFake(), cfg: cfg, logs: &bytes.Buffer{}}
}

func (e *env) worker() *Worker {
	return e.workerWith(map[model.Channel]provider.Provider{model.ChannelEmail: e.fake})
}

func (e *env) workerWith(providers map[model.Channel]provider.Provider) *Worker {
	w, err := New(e.st, providers, e.cfg, e.clk, e.logger(), half)
	if err != nil {
		panic(err)
	}
	return w
}

func (e *env) logger() *slog.Logger { return slog.New(slog.NewJSONHandler(e.logs, nil)) }

func (e *env) enqueue(t *testing.T, key string, ttl time.Duration) string {
	t.Helper()
	return e.enqueueVia(t, key, ttl, "fake")
}

func (e *env) enqueueVia(t *testing.T, key string, ttl time.Duration, prov string) string {
	t.Helper()
	res, err := e.st.Enqueue(context.Background(), e.clk.Now(), store.EnqueueInput{
		App: "a", Channel: model.ChannelEmail, IdempotencyKey: key, BodyHash: sha256.Sum256([]byte(key)),
		Recipients: []string{rcpt}, Sender: "S <s@example.com>", Subject: "code 123", Text: "hello",
		ExpiresAt: e.clk.Now().Add(ttl), PerMinute: 100, PerDay: 1000, Provider: prov,
	})
	if err != nil {
		t.Fatal(err)
	}
	return res.MessageIDs[0]
}

func (e *env) tick(t *testing.T, w *Worker) {
	t.Helper()
	if err := w.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func (e *env) count(t *testing.T, q string, args ...any) int {
	t.Helper()
	var n int
	if err := e.db.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func (e *env) state(t *testing.T, id string) (state string, attempts int) {
	t.Helper()
	if err := e.db.QueryRow(`SELECT state, attempts FROM messages WHERE id=?`, id).Scan(&state, &attempts); err != nil {
		t.Fatal(err)
	}
	return state, attempts
}

func (e *env) bodiesGone(t *testing.T, id string) bool {
	t.Helper()
	return e.count(t, `SELECT COUNT(*) FROM messages WHERE id=? AND subject IS NULL AND body_text IS NULL AND body_html IS NULL
		AND reply_to IS NULL AND sms_body IS NULL AND body_bytes=?`, id, len("code 123")+len("hello")) == 1
}

func nextMidnight(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day()+1, 0, 0, 0, 0, time.UTC)
}

func TestBackoff(t *testing.T) {
	for _, c := range []struct {
		n    int
		want time.Duration
	}{{1, 30 * time.Second}, {2, 60 * time.Second}, {7, 1920 * time.Second}, {8, 3600 * time.Second}} {
		if got := Backoff(c.n, half); got != c.want {
			t.Errorf("Backoff(%d) = %v, want %v", c.n, got, c.want)
		}
	}
	if got := Backoff(1, func() float64 { return 0 }); got != 24*time.Second {
		t.Errorf("rnd 0: %v, want 24s", got)
	}
	if got := Backoff(1, func() float64 { return 1 }); got != 36*time.Second {
		t.Errorf("rnd 1: %v, want 36s", got)
	}
	if got := Backoff(8, func() float64 { return 1 }); got != 4320*time.Second {
		t.Errorf("rnd 1 at cap: %v, want 4320s", got)
	}
}

func TestSendSuccess(t *testing.T) {
	e := newEnv(t, 10, 100)
	id := e.enqueue(t, "k1", time.Hour)
	w := e.worker()
	e.tick(t, w)
	if n := e.count(t, `SELECT COUNT(*) FROM messages WHERE id=? AND state='sent' AND sent_at=? AND provider_message_id=? AND attempts=1 AND locked_until IS NULL`,
		id, t0.UnixMilli(), "fake-"+id); n != 1 {
		t.Fatalf("sent row not as expected")
	}
	if !e.bodiesGone(t, id) {
		t.Fatal("bodies not deleted or body_bytes lost")
	}
	if n := e.count(t, `SELECT COUNT(*) FROM attempts WHERE message_id=? AND n=1 AND outcome='accepted'`, id); n != 1 {
		t.Fatal("attempt row missing")
	}
	if !w.LastTick().Equal(t0) {
		t.Fatalf("LastTick = %v", w.LastTick())
	}
	if s := e.logs.String(); !strings.Contains(s, id) || strings.Contains(s, rcpt) || strings.Contains(s, "code 123") || strings.Contains(s, "s@example.com") {
		t.Fatalf("log must carry the id and no address or subject: %s", s)
	}
}

func TestRetryableThenSuccess(t *testing.T) {
	e := newEnv(t, 10, 100)
	id := e.enqueue(t, "k1", time.Hour)
	e.fake.Script(provider.SendResult{Outcome: provider.Retryable, ErrorType: model.ErrTimeout})
	w := e.worker()
	e.tick(t, w)
	if n := e.count(t, `SELECT COUNT(*) FROM messages WHERE id=? AND state='queued' AND attempts=1 AND next_attempt_at=? AND subject='code 123'`,
		id, t0.Add(30*time.Second).UnixMilli()); n != 1 {
		t.Fatal("requeued row not as expected")
	}
	e.clk.Advance(29 * time.Second)
	e.tick(t, w)
	if st, _ := e.state(t, id); st != "queued" {
		t.Fatalf("sent before backoff elapsed: %s", st)
	}
	e.clk.Advance(time.Second)
	e.tick(t, w)
	if st, n := e.state(t, id); st != "sent" || n != 2 {
		t.Fatalf("state=%s attempts=%d, want sent 2", st, n)
	}
	if n := e.count(t, `SELECT COUNT(*) FROM attempts WHERE message_id=?`, id); n != 2 {
		t.Fatalf("attempt rows = %d, want 2", n)
	}
	if n := e.count(t, `SELECT COUNT(*) FROM attempts WHERE message_id=? AND n=1 AND outcome='retryable' AND error_type='timeout'`, id); n != 1 {
		t.Fatal("first attempt not recorded as retryable")
	}
}

func TestAttemptsExhausted(t *testing.T) {
	e := newEnv(t, 100, 1000)
	id := e.enqueue(t, "k1", 24*time.Hour)
	for i := 0; i < constants.MaxAttempts; i++ {
		e.fake.Script(provider.SendResult{Outcome: provider.Retryable, ErrorType: model.ErrProviderUnavailable, Status: 503})
	}
	e.fake.Script(provider.SendResult{Outcome: provider.Accepted})
	w := e.worker()
	for i := 0; i < constants.MaxAttempts+2; i++ {
		e.tick(t, w)
		e.clk.Advance(2 * constants.BackoffMax)
	}
	if n := e.count(t, `SELECT COUNT(*) FROM messages WHERE id=? AND state='failed' AND attempts=? AND error_type='provider_unavailable' AND final_at IS NOT NULL`,
		id, constants.MaxAttempts); n != 1 {
		t.Fatal("failed row not as expected")
	}
	if !e.bodiesGone(t, id) {
		t.Fatal("bodies not deleted")
	}
	if n := e.count(t, `SELECT COUNT(*) FROM attempts WHERE message_id=?`, id); n != constants.MaxAttempts {
		t.Fatalf("attempts = %d, want %d", n, constants.MaxAttempts)
	}
	if len(e.fake.Sent()) != 0 {
		t.Fatal("a 9th send was made")
	}
}

func TestPermanentFails(t *testing.T) {
	e := newEnv(t, 10, 100)
	id := e.enqueue(t, "k1", time.Hour)
	e.fake.Script(provider.SendResult{Outcome: provider.Permanent, ErrorType: model.ErrInvalidRecipient, Status: 422})
	e.tick(t, e.worker())
	if n := e.count(t, `SELECT COUNT(*) FROM messages WHERE id=? AND state='failed' AND attempts=1 AND error_type='invalid_recipient' AND final_at=?`,
		id, t0.UnixMilli()); n != 1 {
		t.Fatal("failed row not as expected")
	}
	if !e.bodiesGone(t, id) {
		t.Fatal("bodies not deleted")
	}
}

func TestProviderDailyLimitPauses(t *testing.T) {
	e := newEnv(t, 10, 2)
	ids := []string{e.enqueue(t, "k1", 24*time.Hour), e.enqueue(t, "k2", 24*time.Hour), e.enqueue(t, "k3", 24*time.Hour)}
	w := e.worker()
	e.tick(t, w)
	e.clk.Advance(time.Hour)
	e.tick(t, w)
	for _, id := range ids[:2] {
		if st, _ := e.state(t, id); st != "sent" {
			t.Fatalf("%s state %s, want sent", id, st)
		}
	}
	if st, n := e.state(t, ids[2]); st != "queued" || n != 0 {
		t.Fatalf("third: state=%s attempts=%d, want queued 0", st, n)
	}
	e.clk.Set(nextMidnight(t0).Add(-time.Millisecond))
	e.tick(t, w)
	if st, _ := e.state(t, ids[2]); st != "queued" {
		t.Fatal("sent before UTC midnight")
	}
	e.clk.Set(nextMidnight(t0))
	e.tick(t, w)
	if st, n := e.state(t, ids[2]); st != "sent" || n != 1 {
		t.Fatalf("after midnight: state=%s attempts=%d, want sent 1", st, n)
	}
}

func TestProviderMinuteLimit(t *testing.T) {
	e := newEnv(t, 1, 100)
	a, b := e.enqueue(t, "k1", time.Hour), e.enqueue(t, "k2", time.Hour)
	w := e.worker()
	e.tick(t, w)
	e.clk.Set(t0.Truncate(time.Minute).Add(time.Minute - time.Millisecond))
	e.tick(t, w)
	if st, _ := e.state(t, a); st != "sent" {
		t.Fatalf("first state %s", st)
	}
	if st, n := e.state(t, b); st != "queued" || n != 0 {
		t.Fatalf("second: state=%s attempts=%d, want queued 0", st, n)
	}
	e.clk.Set(t0.Truncate(time.Minute).Add(time.Minute))
	e.tick(t, w)
	if st, _ := e.state(t, b); st != "sent" {
		t.Fatalf("second after next minute: %s", st)
	}
}

func TestThrottled429PausesWithoutAttempt(t *testing.T) {
	e := newEnv(t, 10, 100)
	id := e.enqueue(t, "k1", 24*time.Hour)
	e.fake.Script(provider.SendResult{Outcome: provider.Throttled, Status: 429, PauseUntilUTCMidnight: true, PauseReason: provider.PauseDailyQuota})
	w := e.worker()
	e.tick(t, w)
	if n := e.count(t, `SELECT COUNT(*) FROM provider_pauses WHERE provider='provider:email:fake' AND until=? AND reason='daily_quota'`, nextMidnight(t0).UnixMilli()); n != 1 {
		t.Fatal("provider not paused until UTC midnight")
	}
	if n := e.count(t, `SELECT COUNT(*) FROM messages WHERE id=? AND state='queued' AND attempts=0 AND locked_until IS NULL`, id); n != 1 {
		t.Fatal("message not back in queue with attempts 0")
	}
	if n := e.count(t, `SELECT COUNT(*) FROM attempts`); n != 0 {
		t.Fatal("an attempt was recorded for a throttle")
	}
	if n := e.count(t, `SELECT COALESCE(SUM(count),0) FROM rate_counters WHERE scope='provider:email:fake'`); n != 0 {
		t.Fatalf("reservation not released: %d", n)
	}
	e.clk.Advance(time.Hour)
	e.tick(t, w)
	if st, _ := e.state(t, id); st != "queued" {
		t.Fatal("sent while paused")
	}
	e.clk.Set(nextMidnight(t0))
	e.tick(t, w)
	if st, n := e.state(t, id); st != "sent" || n != 1 {
		t.Fatalf("after pause: state=%s attempts=%d", st, n)
	}
}

func TestThrottledPauseFor(t *testing.T) {
	e := newEnv(t, 10, 100)
	e.enqueue(t, "k1", time.Hour)
	e.fake.Script(provider.SendResult{Outcome: provider.Throttled, Status: 429, PauseFor: 7 * time.Second, PauseReason: provider.PauseRateLimit})
	e.tick(t, e.worker())
	if n := e.count(t, `SELECT COUNT(*) FROM provider_pauses WHERE provider='provider:email:fake' AND until=? AND reason='rate_limit'`, t0.Add(7*time.Second).UnixMilli()); n != 1 {
		t.Fatal("provider not paused for PauseFor")
	}
}

func TestExpiry(t *testing.T) {
	e := newEnv(t, 10, 100)
	id := e.enqueue(t, "k1", time.Hour)
	if err := e.st.PauseProvider(context.Background(), model.ChannelEmail, "fake", nextMidnight(t0), provider.PauseDailyQuota); err != nil {
		t.Fatal(err)
	}
	w := e.worker()
	e.tick(t, w)
	e.clk.Advance(time.Hour)
	e.tick(t, w)
	if n := e.count(t, `SELECT COUNT(*) FROM messages WHERE id=? AND state='expired' AND error_type='expired' AND final_at=?`, id, t0.Add(time.Hour).UnixMilli()); n != 1 {
		t.Fatal("message not expired")
	}
	if !e.bodiesGone(t, id) {
		t.Fatal("bodies not deleted")
	}
	e.clk.Set(nextMidnight(t0))
	e.tick(t, w)
	if len(e.fake.Sent()) != 0 {
		t.Fatal("expired message was sent")
	}
}

type recordingProvider struct {
	provider.Provider
	mu  sync.Mutex
	ids []string
}

func (r *recordingProvider) Send(ctx context.Context, m model.Message, b provider.Body) provider.SendResult {
	r.mu.Lock()
	r.ids = append(r.ids, m.ID)
	r.mu.Unlock()
	return r.Provider.Send(ctx, m, b)
}

func TestNoSendAfterDeadline(t *testing.T) {
	e := newEnv(t, 10, 100)
	id := e.enqueue(t, "k1", time.Minute)
	if _, err := e.st.ClaimDue(context.Background(), t0, 1, model.ChannelEmail, "fake"); err != nil {
		t.Fatal(err)
	}
	e.clk.Advance(constants.LeaseDuration)
	rec := &recordingProvider{Provider: e.fake}
	w := e.workerWith(map[model.Channel]provider.Provider{model.ChannelEmail: rec})
	e.tick(t, w)
	if len(rec.ids) != 0 {
		t.Fatal("message past its deadline was handed to the provider")
	}
	if n := e.count(t, `SELECT COUNT(*) FROM messages WHERE id=? AND state='expired' AND error_type='expired' AND locked_until IS NULL`, id); n != 1 {
		t.Fatal("claimed message not expired")
	}
	if !e.bodiesGone(t, id) {
		t.Fatal("bodies not deleted")
	}
	if n := e.count(t, `SELECT COALESCE(SUM(count),0) FROM rate_counters WHERE scope='provider:email:fake'`); n != 0 {
		t.Fatalf("reservation kept for an unsent message: %d", n)
	}
}

func TestHeldSMSNotClaimed(t *testing.T) {
	e := newEnv(t, 10, 100)
	res, err := e.st.Enqueue(context.Background(), t0, store.EnqueueInput{
		App: "a", Channel: model.ChannelSMS, IdempotencyKey: "k1", BodyHash: sha256.Sum256([]byte("k1")),
		Recipients: []string{"+15550001"}, SMSBody: "code 123", ExpiresAt: t0.Add(time.Hour),
		PerMinute: 10, PerDay: 10, Held: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	id := res.MessageIDs[0]
	w := e.worker()
	e.tick(t, w)
	if n := e.count(t, `SELECT COUNT(*) FROM messages WHERE id=? AND state='queued' AND hold_reason='no_provider' AND provider IS NULL AND attempts=0`, id); n != 1 {
		t.Fatal("held sms changed")
	}
	e.clk.Advance(time.Hour)
	e.tick(t, w)
	if n := e.count(t, `SELECT COUNT(*) FROM messages WHERE id=? AND state='expired' AND error_type='expired' AND sms_body IS NULL`, id); n != 1 {
		t.Fatal("held sms not expired at its deadline")
	}
	if len(e.fake.Sent()) != 0 {
		t.Fatal("held sms was sent")
	}
}

func TestHeldSMSReleasedWhenProviderConfigured(t *testing.T) {
	e := newEnv(t, 10, 100)
	res, err := e.st.Enqueue(context.Background(), t0, store.EnqueueInput{
		App: "a", Channel: model.ChannelSMS, IdempotencyKey: "k1", BodyHash: sha256.Sum256([]byte("k1")),
		Recipients: []string{"+15550001"}, SMSBody: "code 123", ExpiresAt: t0.Add(time.Hour),
		PerMinute: 10, PerDay: 10, Held: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	sms := provider.NewFake()
	e.cfg.Providers[model.ChannelSMS] = &config.ProviderCfg{Name: "fake", PerMinute: 10, PerDay: 10}
	w := e.workerWith(map[model.Channel]provider.Provider{model.ChannelSMS: sms})
	e.tick(t, w)
	if st, _ := e.state(t, res.MessageIDs[0]); st != "sent" || len(sms.Sent()) != 1 {
		t.Fatalf("released sms: state=%s sends=%d", st, len(sms.Sent()))
	}
}

type failOnceStore struct {
	*store.Store
	failed bool
}

func (f *failOnceStore) RecordAttempt(ctx context.Context, now time.Time, id string, r provider.SendResult, started time.Time, backoff time.Duration, firstPoll time.Time) error {
	if !f.failed {
		f.failed = true
		return errors.New("injected crash")
	}
	return f.Store.RecordAttempt(ctx, now, id, r, started, backoff, firstPoll)
}

func TestCrashBetweenSendAndRecord(t *testing.T) {
	e := newEnv(t, 10, 100)
	id := e.enqueue(t, "k1", time.Hour)
	rec := &recordingProvider{Provider: e.fake}
	providers := map[model.Channel]provider.Provider{model.ChannelEmail: rec}
	dying, err := newWorker(&failOnceStore{Store: e.st}, providers, e.cfg, e.clk, e.logger(), half)
	if err != nil {
		t.Fatal(err)
	}
	if err := dying.Tick(context.Background()); err == nil {
		t.Fatal("Tick hid the RecordAttempt failure")
	}
	if st, n := e.state(t, id); st != "sending" || n != 0 {
		t.Fatalf("after crash: state=%s attempts=%d, want sending 0", st, n)
	}
	if !strings.Contains(e.logs.String(), id) {
		t.Fatal("crash not logged with the message id")
	}

	e.clk.Advance(constants.LeaseDuration - time.Millisecond)
	e.tick(t, e.workerWith(providers))
	if len(rec.ids) != 1 {
		t.Fatalf("resent while the lease was live: %d calls", len(rec.ids))
	}

	e.clk.Advance(time.Millisecond)
	e.tick(t, e.workerWith(providers))
	if len(rec.ids) != 2 || rec.ids[0] != id || rec.ids[1] != id {
		t.Fatalf("send calls = %v, want the same id twice", rec.ids)
	}
	if len(e.fake.Sent()) != 1 {
		t.Fatalf("deliveries = %d, want 1", len(e.fake.Sent()))
	}
	if n := e.count(t, `SELECT COUNT(*) FROM messages WHERE id=? AND state='sent' AND attempts=1 AND provider_message_id=?`, id, "fake-"+id); n != 1 {
		t.Fatal("reclaimed message not recorded as sent once")
	}
}

func TestShutdownUnclaimsWithoutAttempt(t *testing.T) {
	e := newEnv(t, 10, 100)
	id := e.enqueue(t, "k1", time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	p := &cancellingProvider{cancel: cancel, res: provider.SendResult{Outcome: provider.Retryable, ErrorType: model.ErrTimeout}}
	w := e.workerWith(map[model.Channel]provider.Provider{model.ChannelEmail: p})
	if err := w.Tick(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Tick error = %v, want context.Canceled", err)
	}
	if p.calls != 1 {
		t.Fatalf("send calls = %d, want 1", p.calls)
	}
	if n := e.count(t, `SELECT COUNT(*) FROM messages WHERE id=? AND state='queued' AND attempts=0 AND locked_until IS NULL`, id); n != 1 {
		t.Fatal("message not unclaimed on shutdown")
	}
	if n := e.count(t, `SELECT COUNT(*) FROM attempts`); n != 0 {
		t.Fatal("attempt recorded on shutdown")
	}
	if n := e.count(t, `SELECT COALESCE(SUM(count),0) FROM rate_counters WHERE scope='provider:email:fake'`); n != 0 {
		t.Fatalf("reservation leaked on shutdown: %d", n)
	}
}

type cancellingProvider struct {
	cancel func()
	res    provider.SendResult
	calls  int
}

func (c *cancellingProvider) Name() string { return "fake" }

func (c *cancellingProvider) Send(ctx context.Context, _ model.Message, _ provider.Body) provider.SendResult {
	c.calls++
	c.cancel()
	<-ctx.Done()
	return c.res
}

func TestShutdownStillRecordsAccepted(t *testing.T) {
	e := newEnv(t, 10, 100)
	id := e.enqueue(t, "k1", time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	p := &cancellingProvider{cancel: cancel, res: provider.SendResult{Outcome: provider.Accepted, ProviderID: "p-1"}}
	w := e.workerWith(map[model.Channel]provider.Provider{model.ChannelEmail: p})
	if err := w.Tick(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Tick error = %v, want context.Canceled", err)
	}
	if p.calls != 1 {
		t.Fatalf("send calls = %d, want 1", p.calls)
	}
	if n := e.count(t, `SELECT COUNT(*) FROM messages WHERE id=? AND state='sent' AND attempts=1 AND provider_message_id='p-1' AND subject IS NULL`, id); n != 1 {
		t.Fatal("accepted send not recorded on shutdown")
	}
	if n := e.count(t, `SELECT COUNT(*) FROM attempts WHERE message_id=? AND outcome='accepted'`, id); n != 1 {
		t.Fatal("accepted attempt row missing")
	}
}

func TestThrottleCleanupSurvivesShutdown(t *testing.T) {
	e := newEnv(t, 10, 100)
	id := e.enqueue(t, "k1", time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	p := &cancellingProvider{cancel: cancel, res: provider.SendResult{Outcome: provider.Throttled, PauseFor: time.Minute, PauseReason: provider.PauseRateLimit}}
	w := e.workerWith(map[model.Channel]provider.Provider{model.ChannelEmail: p})
	if err := w.Tick(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Tick error = %v, want context.Canceled", err)
	}
	if n := e.count(t, `SELECT COUNT(*) FROM messages WHERE id=? AND state='queued' AND attempts=0 AND locked_until IS NULL`, id); n != 1 {
		t.Fatal("message not unclaimed")
	}
	if n := e.count(t, `SELECT COALESCE(SUM(count),0) FROM rate_counters WHERE scope='provider:email:fake'`); n != 0 {
		t.Fatalf("reservation leaked on shutdown: %d", n)
	}
}

func TestNewRejectsMismatchedProviders(t *testing.T) {
	e := newEnv(t, 10, 100)
	if _, err := New(e.st, map[model.Channel]provider.Provider{model.ChannelSMS: e.fake}, e.cfg, e.clk, e.logger(), half); err == nil {
		t.Error("channel without a config entry accepted")
	}
	e.cfg.Providers[model.ChannelEmail].Name = "resend"
	if _, err := New(e.st, map[model.Channel]provider.Provider{model.ChannelEmail: e.fake}, e.cfg, e.clk, e.logger(), half); err == nil {
		t.Error("provider name differing from config accepted")
	}
}

func TestSameProviderNameOnTwoChannels(t *testing.T) {
	e := newEnv(t, 10, 100)
	e.cfg.Providers[model.ChannelSMS] = &config.ProviderCfg{Name: "fake", PerMinute: 10, PerDay: 100}
	mail := e.enqueue(t, "k1", time.Hour)
	res, err := e.st.Enqueue(context.Background(), t0, store.EnqueueInput{
		App: "a", Channel: model.ChannelSMS, IdempotencyKey: "k2", BodyHash: sha256.Sum256([]byte("k2")),
		Recipients: []string{"+15550001"}, SMSBody: "code 123", ExpiresAt: t0.Add(time.Hour),
		PerMinute: 10, PerDay: 10, Provider: "fake",
	})
	if err != nil {
		t.Fatal(err)
	}
	sms := provider.NewFake()
	e.tick(t, e.workerWith(map[model.Channel]provider.Provider{model.ChannelEmail: e.fake, model.ChannelSMS: sms}))
	if got := e.fake.Sent(); len(got) != 1 || got[0].ID != mail {
		t.Fatalf("email provider sent %+v", got)
	}
	if got := sms.Sent(); len(got) != 1 || got[0].ID != res.MessageIDs[0] {
		t.Fatalf("sms provider sent %+v", got)
	}
	for _, scope := range []string{"provider:email:fake", "provider:sms:fake"} {
		if n := e.count(t, `SELECT count FROM rate_counters WHERE scope=? AND window_kind='day'`, scope); n != 1 {
			t.Fatalf("%s day counter = %d, want 1", scope, n)
		}
	}
}

func TestQueuedMessageSurvivesConfigRemoval(t *testing.T) {
	e := newEnv(t, 10, 100)
	id := e.enqueue(t, "k1", time.Hour)
	e.cfg.Apps = map[string]config.App{"b": {ID: "b"}}
	e.tick(t, e.worker())
	sent := e.fake.Sent()
	if len(sent) != 1 || sent[0].ID != id || sent[0].Sender != "S <s@example.com>" || sent[0].App != "a" {
		t.Fatalf("sent = %+v", sent)
	}
}

// slowProvider advances the fake clock by SendTimeout per send and probes /healthz while the send is in flight.
type slowProvider struct {
	*provider.Fake
	clk    *clock.Fake
	health http.Handler
	codes  []int
}

func (s *slowProvider) Send(ctx context.Context, m model.Message, b provider.Body) provider.SendResult {
	s.clk.Advance(constants.SendTimeout)
	rec := httptest.NewRecorder()
	s.health.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	s.codes = append(s.codes, rec.Code)
	return s.Fake.Send(ctx, m, b)
}

func TestHealthStaysOkWhileSlowSendsProgress(t *testing.T) {
	e := newEnv(t, 100, 1000)
	sp := &slowProvider{Fake: e.fake, clk: e.clk}
	w := e.workerWith(map[model.Channel]provider.Provider{model.ChannelEmail: sp})
	sp.health = health.Handler(e.st, w.LastTick, e.clk, e.cfg.Providers)
	e.tick(t, w)
	for i := range constants.DispatchBatch {
		e.enqueue(t, fmt.Sprintf("k%d", i), time.Hour)
	}
	e.tick(t, w)
	if time.Duration(len(sp.codes))*constants.SendTimeout <= constants.WorkerFresh {
		t.Fatalf("tick too short to exercise freshness: %d sends", len(sp.codes))
	}
	for i, c := range sp.codes {
		if c != http.StatusOK {
			t.Fatalf("healthz during send %d = %d, want 200 (codes %v)", i+1, c, sp.codes)
		}
	}
	e.clk.Advance(constants.WorkerFresh + time.Millisecond)
	rec := httptest.NewRecorder()
	sp.health.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("healthz after the loop stopped = %d, want 503", rec.Code)
	}
}

func TestSendStopsAtIdempotencySafetyMargin(t *testing.T) {
	cutoff := t0.Add(constants.MaxSendWindow - constants.IdempotencySafetyMargin)
	for _, c := range []struct {
		name  string
		at    time.Time
		state string
		calls int
	}{
		{"1ms before", cutoff.Add(-time.Millisecond), "sent", 1},
		{"at", cutoff, "expired", 0},
		{"after", cutoff.Add(time.Minute), "expired", 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t, 10, 100)
			id := e.enqueue(t, "k1", constants.MaxSendWindow)
			rec := &recordingProvider{Provider: e.fake}
			e.clk.Set(c.at)
			e.tick(t, e.workerWith(map[model.Channel]provider.Provider{model.ChannelEmail: rec}))
			if st, _ := e.state(t, id); st != c.state || len(rec.ids) != c.calls {
				t.Fatalf("state=%s calls=%d, want %s %d", st, len(rec.ids), c.state, c.calls)
			}
		})
	}
}

type noExpireStore struct{ *store.Store }

func (noExpireStore) ExpireDue(context.Context, time.Time) (int, error) { return 0, nil }

func TestClaimedMessageExpiredAtIdempotencySafetyMargin(t *testing.T) {
	e := newEnv(t, 10, 100)
	id := e.enqueue(t, "k1", constants.MaxSendWindow)
	e.clk.Set(t0.Add(constants.MaxSendWindow - constants.IdempotencySafetyMargin))
	rec := &recordingProvider{Provider: e.fake}
	w, err := newWorker(noExpireStore{e.st}, map[model.Channel]provider.Provider{model.ChannelEmail: rec}, e.cfg, e.clk, e.logger(), half)
	if err != nil {
		t.Fatal(err)
	}
	e.tick(t, w)
	if len(rec.ids) != 0 {
		t.Fatal("message inside the safety margin was handed to the provider")
	}
	if n := e.count(t, `SELECT COUNT(*) FROM messages WHERE id=? AND state='expired' AND error_type='expired' AND subject IS NULL`, id); n != 1 {
		t.Fatal("claimed message not expired")
	}
	if n := e.count(t, `SELECT COALESCE(SUM(count),0) FROM rate_counters WHERE scope='provider:email:fake'`); n != 0 {
		t.Fatalf("reservation kept for an unsent message: %d", n)
	}
}

func TestProviderAuthFailurePausesWithoutFailing(t *testing.T) {
	e := newEnv(t, 10, 100)
	e.cfg.Providers[model.ChannelEmail].Name = "resend"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"statusCode":401,"name":"missing_api_key","message":"Missing API key"}`)
	}))
	t.Cleanup(srv.Close)
	ids := []string{e.enqueueVia(t, "k1", time.Hour, "resend"), e.enqueueVia(t, "k2", time.Hour, "resend")}
	w := e.workerWith(map[model.Channel]provider.Provider{model.ChannelEmail: provider.NewResend("k", srv.Client(), srv.URL)})
	e.tick(t, w)
	if n := e.count(t, `SELECT COUNT(*) FROM provider_pauses WHERE provider='provider:email:resend' AND until=? AND reason='auth'`,
		t0.Add(constants.AuthPause).UnixMilli()); n != 1 {
		t.Fatal("provider not paused for AuthPause with reason auth")
	}
	for _, id := range ids {
		if st, n := e.state(t, id); st != "queued" || n != 0 {
			t.Fatalf("%s: state=%s attempts=%d, want queued 0", id, st, n)
		}
	}
	if n := e.count(t, `SELECT COUNT(*) FROM attempts`); n != 0 {
		t.Fatalf("attempts recorded for an auth failure: %d", n)
	}
}

func TestIdleTickWritesNothing(t *testing.T) {
	e := newEnv(t, 10, 100)
	ctx := context.Background()
	conn, err := e.db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	version := func() int {
		var v int
		if err := conn.QueryRowContext(ctx, `PRAGMA data_version`).Scan(&v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	w := e.worker()
	before := version()
	e.tick(t, w)
	if after := version(); after != before {
		t.Fatalf("empty tick committed a write: data_version %d -> %d", before, after)
	}
	if n := e.count(t, `SELECT COUNT(*) FROM rate_counters`); n != 0 {
		t.Fatalf("empty tick touched rate_counters: %d rows", n)
	}

	e.enqueue(t, "k1", time.Hour)
	e.fake.Script(provider.SendResult{Outcome: provider.Retryable, ErrorType: model.ErrTimeout})
	e.tick(t, w)
	e.clk.Advance(time.Second)
	before = version()
	e.tick(t, w)
	if after := version(); after != before {
		t.Fatalf("tick with nothing due committed a write: data_version %d -> %d", before, after)
	}
}
