package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/davcs86/commsgun/api/internal/config"
	"github.com/davcs86/commsgun/api/internal/model"
)

var rowSeq int

type row struct {
	id, app, channel, recipient, sender, state, errType, hold string
	created                                                   time.Time
	sent, final                                               time.Time
}

func ms(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.UnixMilli()
}

func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func insertRow(t *testing.T, s *Store, r row) string {
	t.Helper()
	if r.id == "" {
		rowSeq++
		r.id = fmt.Sprintf("m-%d", rowSeq)
	}
	if r.app == "" {
		r.app = "app1"
	}
	if r.channel == "" {
		r.channel = "email"
	}
	if r.recipient == "" {
		r.recipient = "r@x.com"
	}
	if r.sender == "" {
		r.sender = "s@x.com"
	}
	if r.state == "" {
		r.state = "queued"
	}
	reqID := "req-" + r.id
	if _, err := s.w.Exec(`INSERT INTO requests(id, app, idempotency_key, body_hash, created_at) VALUES (?,?,?,?,?)`,
		reqID, r.app, r.id, []byte{1}, r.created.UnixMilli()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.w.Exec(`INSERT INTO messages(id, request_id, app, channel, recipient, sender, state, hold_reason, error_type,
		subject, body_text, body_bytes, created_at, updated_at, sent_at, final_at, expires_at)
		VALUES (?,?,?,?,?,?,?,?,?,'SECRET-SUBJECT','SECRET-BODY',10,?,?,?,?,?)`,
		r.id, reqID, r.app, r.channel, r.recipient, r.sender, r.state, nilIfEmpty(r.hold), nilIfEmpty(r.errType),
		r.created.UnixMilli(), r.created.UnixMilli(), ms(r.sent), ms(r.final), r.created.Add(24*time.Hour).UnixMilli()); err != nil {
		t.Fatal(err)
	}
	return r.id
}

func insertAttempt(t *testing.T, s *Store, id string, n int, at time.Time, outcome, errType string) {
	t.Helper()
	if _, err := s.w.Exec(`INSERT INTO attempts(message_id, n, started_at, duration_ms, outcome, error_type) VALUES (?,?,?,?,?,?)`,
		id, n, at.UnixMilli(), 5, outcome, nilIfEmpty(errType)); err != nil {
		t.Fatal(err)
	}
}

func lastHour() Filter { return Filter{From: t0.Add(-time.Hour), To: t0} }

func TestBucketFor(t *testing.T) {
	cases := []struct{ d, want time.Duration }{
		{time.Hour, time.Minute}, {time.Hour + time.Second, 5 * time.Minute}, {6 * time.Hour, 5 * time.Minute},
		{24 * time.Hour, 15 * time.Minute}, {25 * time.Hour, time.Hour}, {7 * 24 * time.Hour, time.Hour},
		{8 * 24 * time.Hour, 6 * time.Hour}, {14 * 24 * time.Hour, 6 * time.Hour}, {15 * 24 * time.Hour, 24 * time.Hour},
		{31 * 24 * time.Hour, 24 * time.Hour},
	}
	for _, c := range cases {
		if got := BucketFor(c.d); got != c.want {
			t.Errorf("BucketFor(%v) = %v, want %v", c.d, got, c.want)
		}
	}
}

func TestStatsCountsByEventTime(t *testing.T) {
	s := newStore(t)
	insertRow(t, s, row{id: "a", state: "bounced", errType: "bounced", created: t0.Add(-26 * time.Hour),
		sent: t0.Add(-26 * time.Hour), final: t0.Add(-10 * time.Minute)})
	st, err := s.Stats(context.Background(), lastHour(), t0)
	if err != nil {
		t.Fatal(err)
	}
	if st.Totals.Enqueued != 0 || st.Totals.Sent != 0 || st.Totals.Bounced != 1 {
		t.Fatalf("totals = %+v", st.Totals)
	}
	if len(st.ByErrorType) != 1 || st.ByErrorType[0] != (ErrorTypeCount{"bounced", 1}) {
		t.Fatalf("by_error_type = %+v", st.ByErrorType)
	}
}

func TestStatsRetriesAreAttemptsBeyondFirst(t *testing.T) {
	s := newStore(t)
	id := insertRow(t, s, row{id: "a", state: "sent", created: t0.Add(-30 * time.Minute), sent: t0.Add(-5 * time.Minute)})
	insertAttempt(t, s, id, 1, t0.Add(-20*time.Minute), "retryable", "timeout")
	insertAttempt(t, s, id, 2, t0.Add(-10*time.Minute), "retryable", "network")
	insertAttempt(t, s, id, 3, t0.Add(-5*time.Minute), "accepted", "")
	st, err := s.Stats(context.Background(), lastHour(), t0)
	if err != nil {
		t.Fatal(err)
	}
	if st.Totals.Retries != 2 {
		t.Fatalf("retries = %d, want 2", st.Totals.Retries)
	}
	got := map[string]int{}
	for _, e := range st.ByErrorType {
		got[e.ErrorType] = e.Count
	}
	if len(got) != 2 || got["timeout"] != 1 || got["network"] != 1 {
		t.Fatalf("by_error_type = %+v", st.ByErrorType)
	}
}

func TestStatsQueuedNowIgnoresRange(t *testing.T) {
	s := newStore(t)
	insertRow(t, s, row{id: "old", state: "queued", created: t0.Add(-48 * time.Hour)})
	insertRow(t, s, row{id: "snd", state: "sending", created: t0.Add(-5 * time.Minute), channel: "sms"})
	insertRow(t, s, row{id: "done", state: "delivered", created: t0.Add(-5 * time.Minute), final: t0.Add(-time.Minute)})
	st, err := s.Stats(context.Background(), lastHour(), t0)
	if err != nil {
		t.Fatal(err)
	}
	if st.Totals.QueuedNow != 2 {
		t.Fatalf("queued_now = %d, want 2", st.Totals.QueuedNow)
	}
	f := lastHour()
	f.Channel = "sms"
	if st, err = s.Stats(context.Background(), f, t0); err != nil || st.Totals.QueuedNow != 1 {
		t.Fatalf("sms queued_now = %d err %v", st.Totals.QueuedNow, err)
	}
}

func TestStatsSeriesIsDense(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	insertRow(t, s, row{id: "a", created: t0.Add(-30 * time.Minute)})
	cases := []struct {
		name     string
		to       time.Time
		span     time.Duration
		buckets  int
		bucket   int64
		wantFrom time.Time
		wantTo   time.Time
	}{
		{"1h unaligned", t0, time.Hour, 60, 60, time.Date(2026, 11, 13, 11, 1, 0, 0, time.UTC), time.Date(2026, 11, 13, 12, 1, 0, 0, time.UTC)},
		{"1h aligned", time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC), time.Hour, 60, 60, time.Date(2026, 11, 13, 11, 0, 0, 0, time.UTC), time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)},
		{"24h unaligned", t0, 24 * time.Hour, 96, 900, time.Date(2026, 11, 12, 12, 15, 0, 0, time.UTC), time.Date(2026, 11, 13, 12, 15, 0, 0, time.UTC)},
		{"30d unaligned", t0, 30 * 24 * time.Hour, 30, 86400, time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC), time.Date(2026, 11, 14, 0, 0, 0, 0, time.UTC)},
	}
	for _, c := range cases {
		st, err := s.Stats(ctx, Filter{From: c.to.Add(-c.span), To: c.to}, t0)
		if err != nil {
			t.Fatal(err)
		}
		if len(st.Series) != c.buckets || st.BucketSeconds != c.bucket || !st.From.Equal(c.wantFrom) || !st.To.Equal(c.wantTo) {
			t.Fatalf("%s: %d buckets, bucket_seconds %d, %v..%v", c.name, len(st.Series), st.BucketSeconds, st.From, st.To)
		}
		step := time.Duration(c.bucket) * time.Second
		for i, b := range st.Series {
			if !b.T.Equal(b.T.Truncate(step)) || b.T.Location() != time.UTC || (i > 0 && b.T.Sub(st.Series[i-1].T) != step) {
				t.Fatalf("%s: bucket %d misaligned: %v", c.name, i, b.T)
			}
		}
		if !st.Series[0].T.Equal(st.From) || !st.Series[len(st.Series)-1].T.Add(step).Equal(st.To) {
			t.Fatalf("%s: series does not span from..to", c.name)
		}
		if c.span <= 24*time.Hour && st.Totals.Enqueued != 1 {
			t.Fatalf("%s: totals %+v", c.name, st.Totals)
		}
	}
}

func TestStatsFilters(t *testing.T) {
	s := newStore(t)
	c := t0.Add(-30 * time.Minute)
	a := insertRow(t, s, row{id: "a", app: "app1", channel: "email", state: "failed", errType: "auth", created: c, final: c})
	insertRow(t, s, row{id: "b", app: "app2", channel: "sms", state: "delivered", created: c, sent: c, final: c})
	insertAttempt(t, s, a, 1, c, "permanent", "auth")
	ctx := context.Background()
	tot := func(f Filter) Totals {
		st, err := s.Stats(ctx, f, t0)
		if err != nil {
			t.Fatal(err)
		}
		var sum Totals
		for _, b := range st.Series {
			sum.Enqueued += b.Enqueued
			sum.Failed += b.Failed
			sum.Delivered += b.Delivered
		}
		if sum.Enqueued != st.Totals.Enqueued || sum.Failed != st.Totals.Failed {
			t.Fatalf("series sum %+v != totals %+v", sum, st.Totals)
		}
		return st.Totals
	}
	base := lastHour()
	if x := tot(base); x.Enqueued != 2 {
		t.Fatalf("base %+v", x)
	}
	f := base
	f.Channel = "sms"
	if x := tot(f); x.Enqueued != 1 || x.Delivered != 1 || x.Failed != 0 {
		t.Fatalf("channel %+v", x)
	}
	f = base
	f.App = "app1"
	if x := tot(f); x.Enqueued != 1 || x.Failed != 1 {
		t.Fatalf("app %+v", x)
	}
	f = base
	f.State = "delivered"
	if x := tot(f); x.Enqueued != 1 || x.Failed != 0 {
		t.Fatalf("state %+v", x)
	}
	f = base
	f.ErrorType = "auth"
	x := tot(f)
	if x.Enqueued != 1 || x.Failed != 1 {
		t.Fatalf("error_type %+v", x)
	}
	st, _ := s.Stats(ctx, f, t0)
	if len(st.ByApp) != 1 || st.ByApp[0] != (AppCount{"app1", 1}) || len(st.ByChannel) != 1 || st.ByChannel[0].Channel != "email" {
		t.Fatalf("by_app/by_channel %+v %+v", st.ByApp, st.ByChannel)
	}
}

func TestListMessagesPaging(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	for i := 0; i < 120; i++ {
		insertRow(t, s, row{id: fmt.Sprintf("m%03d", i), created: t0.Add(-time.Duration(120-i) * time.Second)})
	}
	f := Filter{From: t0.Add(-time.Hour), To: t0.Add(time.Hour)}
	p1, c1, err := s.ListMessages(ctx, f, "", 50)
	if err != nil || len(p1) != 50 || c1 == "" {
		t.Fatalf("p1 %d %q %v", len(p1), c1, err)
	}
	if p1[0].ID != "m119" {
		t.Fatalf("not newest first: %s", p1[0].ID)
	}
	insertRow(t, s, row{id: "late", created: t0})
	p2, c2, err := s.ListMessages(ctx, f, c1, 50)
	if err != nil || len(p2) != 50 || c2 == "" {
		t.Fatalf("p2 %d %v", len(p2), err)
	}
	p3, c3, err := s.ListMessages(ctx, f, c2, 50)
	if err != nil || len(p3) != 20 || c3 != "" {
		t.Fatalf("p3 %d %q %v", len(p3), c3, err)
	}
	seen := map[string]bool{}
	for _, m := range append(append(p1, p2...), p3...) {
		if seen[m.ID] || m.ID == "late" {
			t.Fatalf("dup or late row %s", m.ID)
		}
		seen[m.ID] = true
	}
	if len(seen) != 120 {
		t.Fatalf("seen %d", len(seen))
	}
	if _, _, err := s.ListMessages(ctx, f, "!!notbase64", 50); err != ErrBadCursor {
		t.Fatalf("bad cursor err = %v", err)
	}
}

func TestListMessagesNonPositiveLimit(t *testing.T) {
	s := newStore(t)
	insertRow(t, s, row{id: "a", created: t0})
	for _, l := range []int{0, -3} {
		items, _, err := s.ListMessages(context.Background(), Filter{From: t0.Add(-time.Hour), To: t0.Add(time.Hour)}, "", l)
		if err != nil || len(items) != 1 {
			t.Fatalf("limit %d: %d %v", l, len(items), err)
		}
	}
}

func TestListMessagesSameMillisecondTieBreak(t *testing.T) {
	s := newStore(t)
	for i := 0; i < 5; i++ {
		insertRow(t, s, row{id: fmt.Sprintf("t%d", i), created: t0})
	}
	f := Filter{From: t0.Add(-time.Hour), To: t0.Add(time.Hour)}
	var got []string
	cur := ""
	for {
		items, next, err := s.ListMessages(context.Background(), f, cur, 2)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range items {
			got = append(got, m.ID)
		}
		if next == "" {
			break
		}
		cur = next
	}
	if strings.Join(got, ",") != "t4,t3,t2,t1,t0" {
		t.Fatalf("order %v", got)
	}
}

func TestListNeverReturnsBodies(t *testing.T) {
	s := newStore(t)
	insertRow(t, s, row{id: "a", created: t0.Add(-time.Minute)})
	items, _, err := s.ListMessages(context.Background(), lastHour(), "", 50)
	if err != nil || len(items) != 1 {
		t.Fatal(err, len(items))
	}
	b, _ := json.Marshal(items)
	for _, bad := range []string{"subject", "text", "html", "\"body\"", "SECRET"} {
		if strings.Contains(string(b), bad) {
			t.Fatalf("response contains %q: %s", bad, b)
		}
	}
	m, _, err := s.GetMessage(context.Background(), "a")
	if err != nil {
		t.Fatal(err)
	}
	b, _ = json.Marshal(m)
	if strings.Contains(string(b), "SECRET") {
		t.Fatalf("message leaks body: %s", b)
	}
}

func TestGetMessage(t *testing.T) {
	s := newStore(t)
	id := insertRow(t, s, row{id: "a", created: t0})
	insertAttempt(t, s, id, 2, t0.Add(time.Second), "accepted", "")
	insertAttempt(t, s, id, 1, t0, "retryable", "timeout")
	m, at, err := s.GetMessage(context.Background(), id)
	if err != nil || m.ID != id || len(at) != 2 || at[0].N != 1 || at[1].N != 2 {
		t.Fatalf("%+v %+v %v", m, at, err)
	}
	if at[0].ErrorType == nil || *at[0].ErrorType != model.ErrTimeout || at[1].ErrorType != nil {
		t.Fatalf("attempt error types %+v", at)
	}
	if _, _, err := s.GetMessage(context.Background(), "nope"); err != ErrNotFound {
		t.Fatalf("err = %v", err)
	}
}

func TestFilterTextIsLiteral(t *testing.T) {
	s := newStore(t)
	for i, r := range []string{"a_b@x.io", "aXb@x.io", "100%@x.io", "José@É.io"} {
		insertRow(t, s, row{id: fmt.Sprintf("r%d", i), recipient: r, sender: r, created: t0.Add(-time.Minute)})
	}
	ids := func(f Filter) string {
		items, _, err := s.ListMessages(context.Background(), f, "", 50)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, m := range items {
			out = append(out, m.ID)
		}
		return strings.Join(out, ",")
	}
	for _, field := range []string{"recipient", "sender"} {
		with := func(v string) Filter {
			f := lastHour()
			if field == "recipient" {
				f.Recipient = v
			} else {
				f.Sender = v
			}
			return f
		}
		if got := ids(with("a_b")); got != "r0" {
			t.Errorf("%s a_b = %q", field, got)
		}
		if got := ids(with("%")); got != "r2" {
			t.Errorf("%s %% = %q", field, got)
		}
		if got := ids(with("' OR 1=1 --")); got != "" {
			t.Errorf("%s injection = %q", field, got)
		}
		if got := ids(with("A_B@X")); got != "r0" {
			t.Errorf("%s case-insensitive = %q", field, got)
		}
		if got := ids(with("É.IO")); got != "r3" {
			t.Errorf("%s non-ascii = %q", field, got)
		}
		if got := ids(with("é.io")); got != "" {
			t.Errorf("%s non-ascii other case = %q", field, got)
		}
		if got := ids(with(`\`)); got != "" {
			t.Errorf("%s backslash = %q", field, got)
		}
	}
}

func TestFacets(t *testing.T) {
	s := newStore(t)
	id := insertRow(t, s, row{id: "a", app: "b-app", sender: "z@x.com", errType: "bounced", created: t0})
	insertRow(t, s, row{id: "b", app: "a-app", sender: "a@x.com", created: t0})
	if _, err := s.w.Exec(`UPDATE messages SET sender='', channel='sms' WHERE id=?`, insertRow(t, s, row{id: "c", app: "a-app", created: t0})); err != nil {
		t.Fatal(err)
	}
	insertAttempt(t, s, id, 1, t0, "retryable", "timeout")
	f, err := s.Facets(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	j, _ := json.Marshal(f)
	want := `{"apps":["a-app","b-app"],"senders":["a@x.com","z@x.com"],"error_types":["bounced","timeout"],"channels":["email","sms"],"states":["queued","sending","sent","delivered","bounced","complained","failed","expired"]}`
	if string(j) != want {
		t.Fatalf("facets\n got %s\nwant %s", j, want)
	}
	empty, _ := newStoreFacets(t)
	if string(empty) != `{"apps":[],"senders":[],"error_types":[],"channels":["email","sms"],"states":["queued","sending","sent","delivered","bounced","complained","failed","expired"]}` {
		t.Fatalf("empty facets %s", empty)
	}
}

func newStoreFacets(t *testing.T) ([]byte, error) {
	f, err := newStore(t).Facets(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return json.Marshal(f)
}

func TestOverview(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	cfg := &config.Config{Providers: map[model.Channel]*config.ProviderCfg{
		model.ChannelEmail: {Name: "resend", PerMinute: 60, PerDay: 100},
	}}
	if _, err := s.Enqueue(ctx, t0, input("app1", "k1", "b", "a@x.com", "b@x.com")); err != nil {
		t.Fatal(err)
	}
	if ok, _, _, err := s.ProviderReserve(ctx, t0, model.ChannelEmail, "resend", 60, 100); err != nil || !ok {
		t.Fatal(ok, err)
	}
	if err := s.PauseProvider(ctx, model.ChannelEmail, "resend", t0.Add(time.Hour), "monthly_quota"); err != nil {
		t.Fatal(err)
	}
	insertRow(t, s, row{id: "h1", channel: "sms", hold: "no_provider", created: t0.Add(-2 * time.Hour)})
	insertRow(t, s, row{id: "h2", channel: "sms", hold: "no_provider", created: t0.Add(-time.Hour)})
	insertRow(t, s, row{id: "done", state: "delivered", created: t0.Add(-5 * time.Hour), final: t0})
	insertRow(t, s, row{id: "q", created: t0.Add(-30 * time.Minute)})

	ov, err := s.Overview(ctx, t0, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if ov.QueueDepth != 5 {
		t.Fatalf("queue_depth = %d", ov.QueueDepth)
	}
	if ov.OldestWaitingAt == nil || !ov.OldestWaitingAt.Equal(t0.Add(-30*time.Minute)) {
		t.Fatalf("oldest = %v", ov.OldestWaitingAt)
	}
	if len(ov.Providers) != 2 || ov.Providers[0].Channel != "email" || ov.Providers[1].Channel != "sms" {
		t.Fatalf("providers %+v", ov.Providers)
	}
	e, sm := ov.Providers[0], ov.Providers[1]
	if e.Name == nil || *e.Name != "resend" || e.Minute.Used != 1 || e.Minute.Limit != 60 || e.Day.Used != 1 || e.Day.Limit != 100 {
		t.Fatalf("email %+v", e)
	}
	if !e.Day.ResetsAt.Equal(time.Date(2026, 11, 14, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("resets_at %v", e.Day.ResetsAt)
	}
	if e.PausedUntil == nil || !e.PausedUntil.Equal(t0.Add(time.Hour)) || e.PauseReason == nil || *e.PauseReason != "monthly_quota" {
		t.Fatalf("pause %+v", e)
	}
	if e.Held != 0 || sm.Held != 2 {
		t.Fatalf("held email %d sms %d", e.Held, sm.Held)
	}
	if sm.Name != nil || sm.Minute.Limit != 0 || sm.Day.Limit != 0 || sm.Day.Used != 0 || sm.PausedUntil != nil || sm.PauseReason != nil {
		t.Fatalf("sms %+v", sm)
	}
	j, _ := json.Marshal(sm)
	if !strings.Contains(string(j), `"name":null`) || !strings.Contains(string(j), `"paused_until":null`) {
		t.Fatalf("sms json %s", j)
	}

	ov, err = s.Overview(ctx, t0.Add(2*time.Hour), cfg)
	if err != nil || ov.Providers[0].PausedUntil != nil || ov.Providers[0].Minute.Used != 0 {
		t.Fatalf("after pause: %+v %v", ov.Providers[0], err)
	}

	empty, err := newStore(t).Overview(ctx, t0, cfg)
	if err != nil || empty.QueueDepth != 0 || empty.OldestWaitingAt != nil {
		t.Fatalf("empty %+v %v", empty, err)
	}
}

func TestListMessagesErrorTypeMatchesAttempts(t *testing.T) {
	s := newStore(t)
	retried := insertRow(t, s, row{id: "retried", state: "sent", created: t0.Add(-30 * time.Minute), sent: t0.Add(-20 * time.Minute)})
	insertAttempt(t, s, retried, 1, t0.Add(-25*time.Minute), "retryable", "timeout")
	insertAttempt(t, s, retried, 2, t0.Add(-20*time.Minute), "accepted", "")
	insertRow(t, s, row{id: "final", state: "failed", errType: "timeout", created: t0.Add(-10 * time.Minute), final: t0.Add(-5 * time.Minute)})
	insertRow(t, s, row{id: "clean", state: "sent", created: t0.Add(-10 * time.Minute)})
	f := lastHour()
	f.ErrorType = "timeout"
	items, _, err := s.ListMessages(context.Background(), f, "", 50)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, m := range items {
		got = append(got, m.ID)
	}
	if strings.Join(got, ",") != "final,retried" {
		t.Fatalf("error_type=timeout listed %v, want [final retried]", got)
	}
}

func TestStatsCountsPollRejectedOnce(t *testing.T) {
	s := newStore(t)
	polled := insertRow(t, s, row{id: "polled", state: "failed", errType: "provider_rejected", created: t0.Add(-40 * time.Minute),
		sent: t0.Add(-40 * time.Minute), final: t0.Add(-10 * time.Minute)})
	insertAttempt(t, s, polled, 1, t0.Add(-40*time.Minute), "accepted", "")
	sendRejected := insertRow(t, s, row{id: "send", state: "failed", errType: "provider_rejected", created: t0.Add(-30 * time.Minute),
		final: t0.Add(-30 * time.Minute)})
	insertAttempt(t, s, sendRejected, 1, t0.Add(-30*time.Minute), "permanent", "provider_rejected")
	st, err := s.Stats(context.Background(), lastHour(), t0)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.ByErrorType) != 1 || st.ByErrorType[0] != (ErrorTypeCount{"provider_rejected", 2}) {
		t.Fatalf("by_error_type = %+v, want provider_rejected 2", st.ByErrorType)
	}
}
