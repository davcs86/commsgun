package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/davcs86/commsgun/api/internal/constants"
	"github.com/davcs86/commsgun/api/internal/model"
)

var t0 = time.Date(2026, 11, 13, 12, 0, 30, 0, time.UTC)

func newStore(t *testing.T) *Store {
	t.Helper()
	s, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func input(app, key, body string, rcpt ...string) EnqueueInput {
	return EnqueueInput{
		App: app, Channel: model.ChannelEmail, IdempotencyKey: key,
		BodyHash: sha256.Sum256([]byte(body)), Recipients: rcpt,
		Sender: "a@example.com", Subject: "hi", Text: "hello",
		ExpiresAt: t0.Add(time.Hour), SourceIP: "10.0.0.1",
		PerMinute: 100, PerDay: 1000, Provider: "resend",
	}
}

func count(t *testing.T, s *Store, q string, args ...any) int {
	t.Helper()
	var n int
	if err := s.w.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestEnqueueCreatesOneMessagePerRecipient(t *testing.T) {
	s := newStore(t)
	res, err := s.Enqueue(context.Background(), t0, input("app1", "k1", "b", "a@x.com", "b@x.com", "c@x.com"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Duplicate || res.RequestID == "" || len(res.MessageIDs) != 3 {
		t.Fatalf("result = %+v", res)
	}
	if n := count(t, s, `SELECT COUNT(*) FROM messages WHERE state='queued' AND attempts=0 AND next_attempt_at=? AND provider='resend' AND hold_reason IS NULL AND request_id=?`, t0.UnixMilli(), res.RequestID); n != 3 {
		t.Fatalf("matching rows = %d, want 3", n)
	}
	var recipient string
	var bodyBytes int
	if err := s.w.QueryRow(`SELECT recipient, body_bytes FROM messages WHERE id=?`, res.MessageIDs[1]).Scan(&recipient, &bodyBytes); err != nil {
		t.Fatal(err)
	}
	if recipient != "b@x.com" || bodyBytes != len("hi")+len("hello") {
		t.Fatalf("recipient=%q body_bytes=%d", recipient, bodyBytes)
	}
}

func TestEnqueueIdempotentRepeat(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	first, err := s.Enqueue(ctx, t0, input("app1", "k1", "b", "a@x.com", "b@x.com"))
	if err != nil {
		t.Fatal(err)
	}
	in := input("app1", "k1", "b", "a@x.com", "b@x.com")
	in.PerMinute, in.PerDay = 2, 2
	again, err := s.Enqueue(ctx, t0, in)
	if err != nil {
		t.Fatal(err)
	}
	if !again.Duplicate || again.RequestID != first.RequestID || len(again.MessageIDs) != 2 ||
		again.MessageIDs[0] != first.MessageIDs[0] || again.MessageIDs[1] != first.MessageIDs[1] {
		t.Fatalf("first=%+v again=%+v", first, again)
	}
	if n := count(t, s, `SELECT SUM(count) FROM rate_counters WHERE window_kind='minute'`); n != 2 {
		t.Fatalf("minute counter = %d, want 2", n)
	}
	if n := count(t, s, `SELECT COUNT(*) FROM messages`); n != 2 {
		t.Fatalf("messages = %d, want 2", n)
	}
}

func TestEnqueueIdempotencyConflict(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	if _, err := s.Enqueue(ctx, t0, input("app1", "k1", "one", "a@x.com")); err != nil {
		t.Fatal(err)
	}
	_, err := s.Enqueue(ctx, t0, input("app1", "k1", "two", "a@x.com"))
	if !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("err = %v", err)
	}
}

func TestEnqueueKeyScopedToApp(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	a, err := s.Enqueue(ctx, t0, input("app1", "k1", "b", "a@x.com"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.Enqueue(ctx, t0, input("app2", "k1", "b", "a@x.com"))
	if err != nil {
		t.Fatal(err)
	}
	if b.Duplicate || b.RequestID == a.RequestID {
		t.Fatalf("a=%+v b=%+v", a, b)
	}
}

func TestEnqueueMinuteLimit(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	for i, k := range []string{"k1", "k2"} {
		in := input("app1", k, k, "a@x.com")
		in.PerMinute = 2
		if _, err := s.Enqueue(ctx, t0, in); err != nil {
			t.Fatalf("enqueue %d: %v", i, err)
		}
	}
	in := input("app1", "k3", "k3", "a@x.com")
	in.PerMinute = 2
	_, err := s.Enqueue(ctx, t0, in)
	var rl *RateLimitedError
	if !errors.As(err, &rl) {
		t.Fatalf("err = %v", err)
	}
	if rl.Window != "minute" || rl.RetryAfter != 30*time.Second {
		t.Fatalf("rl = %+v", rl)
	}
}

func TestEnqueueDayLimitAtWindowEdge(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	late := time.Date(2026, 11, 13, 23, 59, 59, 0, time.UTC)
	for _, k := range []string{"k1", "k2"} {
		in := input("app1", k, k, "a@x.com")
		in.PerDay = 2
		if _, err := s.Enqueue(ctx, late, in); err != nil {
			t.Fatal(err)
		}
	}
	in := input("app1", "k3", "k3", "a@x.com")
	in.PerDay = 2
	_, err := s.Enqueue(ctx, late, in)
	var rl *RateLimitedError
	if !errors.As(err, &rl) || rl.Window != "day" || rl.RetryAfter != time.Second {
		t.Fatalf("err = %v (%+v)", err, rl)
	}
	if _, err := s.Enqueue(ctx, late.Add(time.Second), in); err != nil {
		t.Fatalf("after midnight: %v", err)
	}
}

func TestEnqueueLimitIsAllOrNothing(t *testing.T) {
	s := newStore(t)
	in := input("app1", "k1", "b", "a@x.com", "b@x.com", "c@x.com")
	in.PerMinute = 2
	_, err := s.Enqueue(context.Background(), t0, in)
	var rl *RateLimitedError
	if !errors.As(err, &rl) {
		t.Fatalf("err = %v", err)
	}
	for _, tbl := range []string{"messages", "requests", "rate_counters"} {
		if n := count(t, s, `SELECT COUNT(*) FROM `+tbl); n != 0 {
			t.Fatalf("%s rows = %d, want 0", tbl, n)
		}
	}
}

func TestEnqueueQueueFull(t *testing.T) {
	s := newStore(t)
	if _, err := s.w.Exec(`INSERT INTO requests(id, app, idempotency_key, body_hash, created_at) VALUES ('r','a','k',x'00',0)`); err != nil {
		t.Fatal(err)
	}
	_, err := s.w.Exec(`WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i<?)
		INSERT INTO messages(id, request_id, app, channel, recipient, sender, state, created_at, updated_at, expires_at, next_attempt_at)
		SELECT 'm'||i, 'r', 'a', 'email', 'x@x.com', 's', CASE WHEN i%2=0 THEN 'queued' ELSE 'sending' END, 0, 0, 0, 0 FROM n`, constants.MaxQueueDepth)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Enqueue(context.Background(), t0, input("app1", "k1", "b", "a@x.com"))
	if !errors.Is(err, ErrQueueFull) {
		t.Fatalf("err = %v", err)
	}
	if _, err := s.w.Exec(`UPDATE messages SET state='sent' WHERE id='m1'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Enqueue(context.Background(), t0, input("app1", "k1", "b", "a@x.com")); err != nil {
		t.Fatalf("after finishing one: %v", err)
	}
}

func TestEnqueueHeld(t *testing.T) {
	s := newStore(t)
	in := input("app1", "k1", "b", "+15551234567")
	in.Channel, in.Held, in.Provider = model.ChannelSMS, true, ""
	in.SMSBody, in.Subject, in.Text = "code 1234", "", ""
	res, err := s.Enqueue(context.Background(), t0, in)
	if err != nil {
		t.Fatal(err)
	}
	var reason string
	var prov *string
	var state string
	if err := s.w.QueryRow(`SELECT hold_reason, provider, state FROM messages WHERE id=?`, res.MessageIDs[0]).Scan(&reason, &prov, &state); err != nil {
		t.Fatal(err)
	}
	if reason != "no_provider" || prov != nil || state != "queued" {
		t.Fatalf("reason=%q provider=%v state=%q", reason, prov, state)
	}
}

func TestCountersSurviveReopen(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	in := input("app1", "k1", "b", "a@x.com", "b@x.com")
	in.PerMinute = 2
	if _, err := s.Enqueue(context.Background(), t0, in); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	in2 := input("app1", "k2", "c", "c@x.com")
	in2.PerMinute = 2
	_, err = s.Enqueue(context.Background(), t0, in2)
	var rl *RateLimitedError
	if !errors.As(err, &rl) {
		t.Fatalf("err = %v", err)
	}
}

func TestOpenPragmas(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for pragma, want := range map[string]int{"auto_vacuum": 2, "foreign_keys": 1, "busy_timeout": int(constants.SQLiteBusyTimeout.Milliseconds())} {
		var got int
		if err := s.w.QueryRow("PRAGMA " + pragma).Scan(&got); err != nil || got != want {
			t.Fatalf("%s = %d (%v), want %d", pragma, got, err, want)
		}
	}
	var mode string
	if err := s.w.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil || mode != "wal" {
		t.Fatalf("journal_mode = %q (%v)", mode, err)
	}
	if _, err := s.r.Exec(`DELETE FROM messages`); err == nil {
		t.Fatal("read pool accepted a write")
	}
}

func TestTranslateBusy(t *testing.T) {
	if err := translate(errors.New("database is locked (5) (SQLITE_BUSY)")); !errors.Is(err, ErrBusy) {
		t.Fatalf("err = %v", err)
	}
	other := errors.New("boom")
	if err := translate(other); err != other {
		t.Fatalf("err = %v", err)
	}
}

func TestSchemaCreationIsAtomic(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "x.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	bad := `CREATE TABLE schema_version (version INTEGER NOT NULL);
INSERT INTO schema_version(version) VALUES (1);
CREATE TABLE a (x);
INSERT INTO missing_table VALUES (1);`
	if err := initSchema(db, bad); err == nil {
		t.Fatal("expected failure")
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("objects left behind: %d (%v)", n, err)
	}
	if err := initSchema(db, schemaSQL); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name IN ('messages','attempts','requests','rate_counters','provider_pauses')`).Scan(&n); err != nil || n != 5 {
		t.Fatalf("tables = %d (%v)", n, err)
	}
}
