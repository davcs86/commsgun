package worker

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

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
	return NewPoller(e.st, map[string]provider.StatusChecker{"fake": c}, e.clk, e.logger())
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
	id := e.enqueue(t, "k1", time.Hour)
	rec := &recordingProvider{Provider: e.fake}
	e.tick(t, e.workerWith(map[model.Channel]provider.Provider{model.ChannelEmail: rec}))
	if st, _ := e.state(t, id); st != "sent" {
		t.Fatalf("state = %s, want sent", st)
	}
	if _, _, _, next, _ := e.pollRow(t, id); next != nil {
		t.Fatalf("next_poll_at = %d, want NULL for a provider without StatusChecker", *next)
	}
}

func TestPollClearsRowWithoutChecker(t *testing.T) {
	e := newEnv(t, 10, 100)
	id := e.sentMessage(t, "k1")
	e.clk.Advance(constants.PollSchedule[0])
	e.pollTick(t, NewPoller(e.st, map[string]provider.StatusChecker{}, e.clk, e.logger()))
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
