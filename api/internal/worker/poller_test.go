package worker

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/davcs86/commsgun/api/internal/config"
	"github.com/davcs86/commsgun/api/internal/constants"
	"github.com/davcs86/commsgun/api/internal/model"
	"github.com/davcs86/commsgun/api/internal/provider"
	"github.com/davcs86/commsgun/api/internal/store"
)

type countingChecker struct {
	provider.StatusChecker
	calls int
	err   error
}

func (c *countingChecker) Status(ctx context.Context, id string) (provider.DeliveryStatus, error) {
	c.calls++
	if c.err != nil {
		return provider.Pending, c.err
	}
	return c.StatusChecker.Status(ctx, id)
}

func (e *env) poller(c provider.StatusChecker) *Poller {
	return NewPoller(e.st, map[CheckerKey]provider.StatusChecker{{model.ChannelEmail, "fake"}: c}, e.clk, e.logger())
}

// sentMessage enqueues and dispatches one message; it is sent at the fake clock's current time.
func (e *env) sentMessage(t *testing.T, key string) string {
	t.Helper()
	id := e.enqueue(t, key, time.Hour)
	e.tick(t, e.worker())
	if st, _ := e.state(t, id); st != "sent" {
		t.Fatalf("state = %s, want sent", st)
	}
	return id
}

func (e *env) pollRow(t *testing.T, id string) (state string, errType *string, finalAt, nextPoll *int64, polls int) {
	t.Helper()
	if err := e.db.QueryRow(`SELECT state, error_type, final_at, next_poll_at, poll_count FROM messages WHERE id=?`, id).
		Scan(&state, &errType, &finalAt, &nextPoll, &polls); err != nil {
		t.Fatal(err)
	}
	return
}

func (e *env) pollTick(t *testing.T, p *Poller) {
	t.Helper()
	if err := p.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestPollScheduledAtSend(t *testing.T) {
	e := newEnv(t, 10, 100)
	id := e.sentMessage(t, "k1")
	_, _, _, next, polls := e.pollRow(t, id)
	if next == nil || *next != t0.Add(constants.PollSchedule[0]).UnixMilli() || polls != 0 {
		t.Fatalf("next_poll_at = %v polls = %d, want sent+1m and 0", next, polls)
	}
}

func TestPollScheduleOffsets(t *testing.T) {
	e := newEnv(t, 10, 100)
	e.sentMessage(t, "k1")
	c := &countingChecker{StatusChecker: e.fake}
	p := e.poller(c)
	for k, off := range constants.PollSchedule {
		e.clk.Set(t0.Add(off - time.Millisecond))
		e.pollTick(t, p)
		if c.calls != k {
			t.Fatalf("poll %d happened before sent_at+%v", k+1, off)
		}
		e.clk.Set(t0.Add(off))
		e.pollTick(t, p)
		if c.calls != k+1 {
			t.Fatalf("calls = %d at sent_at+%v, want %d", c.calls, off, k+1)
		}
		e.pollTick(t, p)
		if c.calls != k+1 {
			t.Fatalf("polled twice at sent_at+%v", off)
		}
	}
}

func TestPollOutcomes(t *testing.T) {
	for _, tc := range []struct {
		name    string
		status  provider.DeliveryStatus
		state   string
		errType string
	}{
		{"delivered", provider.Delivered, "delivered", ""},
		{"bounced", provider.Bounced, "bounced", "bounced"},
		{"complained", provider.Complained, "complained", "complained"},
		{"rejected", provider.Rejected, "failed", "provider_rejected"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t, 10, 100)
			id := e.sentMessage(t, "k1")
			e.fake.SetStatus("fake-"+id, tc.status)
			e.clk.Advance(constants.PollSchedule[0])
			e.pollTick(t, e.poller(e.fake))
			state, et, final, next, polls := e.pollRow(t, id)
			if state != tc.state || polls != 1 || next != nil {
				t.Fatalf("state=%s polls=%d next=%v, want %s 1 NULL", state, polls, next, tc.state)
			}
			if final == nil || *final != e.clk.Now().UnixMilli() {
				t.Fatalf("final_at = %v, want now", final)
			}
			if (tc.errType == "") != (et == nil) || (et != nil && *et != tc.errType) {
				t.Fatalf("error_type = %v, want %q", et, tc.errType)
			}
		})
	}
}

func TestPollPendingToEnd(t *testing.T) {
	e := newEnv(t, 10, 100)
	id := e.sentMessage(t, "k1")
	c := &countingChecker{StatusChecker: e.fake}
	p := e.poller(c)
	e.clk.Advance(100 * time.Hour)
	for range 10 {
		e.pollTick(t, p)
	}
	state, _, final, next, polls := e.pollRow(t, id)
	if state != "sent" || next != nil || final != nil || polls != len(constants.PollSchedule) || c.calls != len(constants.PollSchedule) {
		t.Fatalf("state=%s next=%v final=%v polls=%d calls=%d, want sent NULL NULL %d %[5]d",
			state, next, final, polls, c.calls, len(constants.PollSchedule))
	}
}

func TestPollErrorKeepsSchedule(t *testing.T) {
	e := newEnv(t, 10, 100)
	id := e.sentMessage(t, "k1")
	c := &countingChecker{StatusChecker: e.fake, err: errors.New("boom")}
	p := e.poller(c)
	e.clk.Advance(constants.PollSchedule[0])
	e.pollTick(t, p)
	state, _, _, next, polls := e.pollRow(t, id)
	if state != "sent" || polls != 1 || next == nil || *next != t0.Add(constants.PollSchedule[1]).UnixMilli() {
		t.Fatalf("state=%s polls=%d next=%v, want sent 1 sent_at+5m", state, polls, next)
	}
}

func TestSMSIsNeverPolled(t *testing.T) {
	e := newEnv(t, 10, 100)
	e.cfg.Providers[model.ChannelSMS] = &config.ProviderCfg{Name: "fake", PerMinute: 10, PerDay: 100}
	res, err := e.st.Enqueue(context.Background(), e.clk.Now(), store.EnqueueInput{
		App: "a", Channel: model.ChannelSMS, IdempotencyKey: "sms", BodyHash: sha256.Sum256([]byte("sms")),
		Recipients: []string{"+15550100"}, Sender: "S", SMSBody: "code 123",
		ExpiresAt: e.clk.Now().Add(time.Hour), PerMinute: 100, PerDay: 1000, Provider: "fake",
	})
	if err != nil {
		t.Fatal(err)
	}
	id := res.MessageIDs[0]
	rec := &recordingProvider{Provider: e.fake}
	e.tick(t, e.workerWith(map[model.Channel]provider.Provider{model.ChannelSMS: rec}))
	if st, _ := e.state(t, id); st != "sent" {
		t.Fatalf("state = %s, want sent", st)
	}
	if _, _, _, next, _ := e.pollRow(t, id); next != nil {
		t.Fatalf("next_poll_at = %d, want NULL for an SMS", *next)
	}
}

func TestFailedSendsAreNotScheduled(t *testing.T) {
	for _, r := range []provider.SendResult{
		{Outcome: provider.Retryable, ErrorType: model.ErrNetwork},
		{Outcome: provider.Permanent, ErrorType: model.ErrProviderRejected},
	} {
		e := newEnv(t, 10, 100)
		id := e.enqueue(t, "k1", time.Hour)
		e.fake.Script(r)
		e.tick(t, e.worker())
		if _, _, _, next, _ := e.pollRow(t, id); next != nil {
			t.Fatalf("outcome %d: next_poll_at = %d, want NULL", r.Outcome, *next)
		}
	}
}

func TestEmailWithoutStatusCheckerIsNotScheduled(t *testing.T) {
	e := newEnv(t, 10, 100)
	id := e.enqueue(t, "k1", time.Hour)
	rec := &recordingProvider{Provider: e.fake}
	e.tick(t, e.workerWith(map[model.Channel]provider.Provider{model.ChannelEmail: rec}))
	if _, _, _, next, _ := e.pollRow(t, id); next != nil {
		t.Fatalf("next_poll_at = %d, want NULL", *next)
	}
}

func TestUnknownStatusAdvancesSchedule(t *testing.T) {
	e := newEnv(t, 10, 100)
	id := e.sentMessage(t, "k1")
	if err := e.st.RecordPoll(context.Background(), e.clk.Now(), id, provider.DeliveryStatus(99)); err != nil {
		t.Fatal(err)
	}
	state, _, _, next, polls := e.pollRow(t, id)
	if state != "sent" || polls != 1 || next == nil || *next != t0.Add(constants.PollSchedule[1]).UnixMilli() {
		t.Fatalf("state=%s polls=%d next=%v, want sent 1 sent_at+5m", state, polls, next)
	}
}

type failFirstPoll struct {
	pollStore
	failed bool
}

func (f *failFirstPoll) RecordPoll(ctx context.Context, now time.Time, id string, s provider.DeliveryStatus) error {
	if !f.failed {
		f.failed = true
		return errors.New("injected")
	}
	return f.pollStore.RecordPoll(ctx, now, id, s)
}

func TestPollStoreErrorDoesNotAbortBatch(t *testing.T) {
	e := newEnv(t, 100, 1000)
	var ids []string
	for i := range constants.PollBatch {
		ids = append(ids, e.enqueue(t, "k"+string(rune('a'+i)), time.Hour))
	}
	e.tick(t, e.worker())
	for _, id := range ids {
		e.fake.SetStatus("fake-"+id, provider.Delivered)
	}
	e.clk.Advance(constants.PollSchedule[0])
	p := e.poller(e.fake)
	p.st = &failFirstPoll{pollStore: p.st}
	if err := p.Tick(context.Background()); err == nil {
		t.Fatal("Tick hid the store error")
	}
	if n := e.count(t, `SELECT COUNT(*) FROM messages WHERE state='delivered'`); n != constants.PollBatch-1 {
		t.Fatalf("delivered = %d, want %d", n, constants.PollBatch-1)
	}
}

func TestPollClearsRowWithoutChecker(t *testing.T) {
	e := newEnv(t, 10, 100)
	id := e.sentMessage(t, "k1")
	e.clk.Advance(constants.PollSchedule[0])
	e.pollTick(t, NewPoller(e.st, map[CheckerKey]provider.StatusChecker{}, e.clk, e.logger()))
	state, _, _, next, polls := e.pollRow(t, id)
	if state != "sent" || next != nil || polls != 0 {
		t.Fatalf("state=%s next=%v polls=%d, want sent NULL 0", state, next, polls)
	}
}

func TestPollBatchCap(t *testing.T) {
	e := newEnv(t, 100, 1000)
	var ids []string
	for i := range constants.PollBatch + 2 {
		ids = append(ids, e.enqueue(t, "k"+string(rune('a'+i)), time.Hour))
	}
	w := e.worker()
	for range 3 {
		e.tick(t, w)
	}
	c := &countingChecker{StatusChecker: e.fake}
	e.clk.Advance(constants.PollSchedule[0])
	e.pollTick(t, e.poller(c))
	if c.calls != constants.PollBatch {
		t.Fatalf("calls = %d, want %d", c.calls, constants.PollBatch)
	}
	e.pollTick(t, e.poller(c))
	if c.calls != len(ids) {
		t.Fatalf("calls = %d after second tick, want %d", c.calls, len(ids))
	}
}

func TestRecordPollOnlyFromSent(t *testing.T) {
	e := newEnv(t, 10, 100)
	id := e.enqueue(t, "k1", time.Hour)
	ctx := context.Background()
	if err := e.st.RecordPoll(ctx, e.clk.Now(), id, provider.Delivered); !errors.Is(err, store.ErrNotSent) {
		t.Fatalf("RecordPoll on queued: %v, want ErrNotSent", err)
	}
	if err := e.st.RecordPollError(ctx, e.clk.Now(), id); !errors.Is(err, store.ErrNotSent) {
		t.Fatalf("RecordPollError on queued: %v, want ErrNotSent", err)
	}
	if st, _ := e.state(t, id); st != "queued" {
		t.Fatalf("state = %s, want unchanged queued", st)
	}

	sent := e.sentMessage(t, "k2")
	if err := e.st.RecordPoll(ctx, e.clk.Now(), sent, provider.Delivered); err != nil {
		t.Fatal(err)
	}
	if err := e.st.RecordPoll(ctx, e.clk.Now(), sent, provider.Bounced); !errors.Is(err, store.ErrNotSent) {
		t.Fatalf("second RecordPoll: %v, want ErrNotSent", err)
	}
	if st, _, _, _, _ := e.pollRow(t, sent); st != "delivered" {
		t.Fatalf("state = %s, want delivered kept", st)
	}
}

func TestPollLogsIDsOnly(t *testing.T) {
	e := newEnv(t, 10, 100)
	id := e.sentMessage(t, "k1")
	e.fake.SetStatus("fake-"+id, provider.Delivered)
	e.clk.Advance(constants.PollSchedule[0])
	e.pollTick(t, e.poller(e.fake))
	if out := e.logs.String(); !strings.Contains(out, id) || strings.Contains(out, rcpt) {
		t.Fatalf("log should carry the id and no address: %s", out)
	}
}

func TestPollFailureLogsStatusWithoutAddress(t *testing.T) {
	for _, c := range []struct{ errName, want string }{
		{"restricted_api_key", `"error":"status http 401 restricted_api_key"`},
		{"leak-" + rcpt, `"error":"status http 401"`},
	} {
		t.Run(c.errName, func(t *testing.T) {
			e := newEnv(t, 10, 100)
			e.cfg.Providers[model.ChannelEmail].Name = "resend"
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPost {
					fmt.Fprint(w, `{"id":"pid-1"}`)
					return
				}
				w.WriteHeader(http.StatusUnauthorized)
				fmt.Fprintf(w, `{"statusCode":401,"name":%q,"message":"key cannot read %s"}`, c.errName, rcpt)
			}))
			t.Cleanup(srv.Close)
			rs := provider.NewResend("k", srv.Client(), srv.URL)
			id := e.enqueueVia(t, "k1", time.Hour, "resend")
			e.tick(t, e.workerWith(map[model.Channel]provider.Provider{model.ChannelEmail: rs}))
			e.clk.Advance(constants.PollSchedule[0])
			e.logs.Reset()
			e.pollTick(t, NewPoller(e.st, map[CheckerKey]provider.StatusChecker{{model.ChannelEmail, "resend"}: rs}, e.clk, e.logger()))
			logs := e.logs.String()
			if !strings.Contains(logs, "status poll failed") || !strings.Contains(logs, id) || !strings.Contains(logs, c.want) {
				t.Fatalf("poll failure log lacks id or %s: %s", c.want, logs)
			}
			if strings.Contains(logs, "@") {
				t.Fatalf("poll failure log carries an address: %s", logs)
			}
		})
	}
}
