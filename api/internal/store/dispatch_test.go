package store

import (
	"context"
	"testing"
	"time"

	"github.com/davcs86/commsgun/api/internal/constants"
	"github.com/davcs86/commsgun/api/internal/model"
	"github.com/davcs86/commsgun/api/internal/provider"
)

func enqueueOne(t *testing.T, s *Store, key string, at time.Time) string {
	t.Helper()
	in := input("app1", key, key, "r@x.com")
	in.ExpiresAt = at.Add(time.Hour)
	res, err := s.Enqueue(context.Background(), at, in)
	if err != nil {
		t.Fatal(err)
	}
	return res.MessageIDs[0]
}

func claimOne(t *testing.T, s *Store, now time.Time) ClaimedMessage {
	t.Helper()
	got, err := s.ClaimDue(context.Background(), now, 1, []string{"resend"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("claimed %d, want 1", len(got))
	}
	return got[0]
}

func TestClaimDueOrderFiltersAndBody(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	older := enqueueOne(t, s, "k1", t0.Add(-time.Minute))
	newer := enqueueOne(t, s, "k2", t0)
	enqueueOne(t, s, "k3", t0.Add(time.Minute))
	other := input("app1", "k4", "k4", "o@x.com")
	other.Provider = "other"
	if _, err := s.Enqueue(ctx, t0, other); err != nil {
		t.Fatal(err)
	}

	got, err := s.ClaimDue(ctx, t0, 10, []string{"resend"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ID != older || got[1].ID != newer {
		t.Fatalf("claimed %+v, want [%s %s]", got, older, newer)
	}
	if got[0].State != model.StateSending || got[0].Sender != "a@example.com" || got[0].Recipient != "r@x.com" {
		t.Fatalf("message = %+v", got[0].Message)
	}
	if got[0].Body != (provider.Body{Subject: "hi", Text: "hello"}) {
		t.Fatalf("body = %+v", got[0].Body)
	}
	if n := count(t, s, `SELECT COUNT(*) FROM messages WHERE state='sending' AND locked_until=?`, t0.Add(constants.LeaseDuration).UnixMilli()); n != 2 {
		t.Fatalf("leased rows = %d, want 2", n)
	}
}

func TestClaimDueSkipsHeld(t *testing.T) {
	s := newStore(t)
	in := input("app1", "k1", "b", "+15550001")
	in.Channel, in.Held, in.Provider = model.ChannelSMS, true, ""
	if _, err := s.Enqueue(context.Background(), t0, in); err != nil {
		t.Fatal(err)
	}
	got, err := s.ClaimDue(context.Background(), t0, 10, []string{"resend", "fake"})
	if err != nil || len(got) != 0 {
		t.Fatalf("claimed %d err %v, want none", len(got), err)
	}
}

func TestLeaseBlocksSecondClaim(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	id := enqueueOne(t, s, "k1", t0)
	claimOne(t, s, t0)
	got, err := s.ClaimDue(ctx, t0.Add(constants.LeaseDuration-time.Millisecond), 10, []string{"resend"})
	if err != nil || len(got) != 0 {
		t.Fatalf("second claim got %d err %v, want none", len(got), err)
	}
	again := claimOne(t, s, t0.Add(constants.LeaseDuration))
	if again.ID != id {
		t.Fatalf("reclaimed %s, want %s", again.ID, id)
	}
}

func TestProviderReserveWindows(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		ok, _, _, err := s.ProviderReserve(ctx, t0, "resend", 2, 3)
		if err != nil || !ok {
			t.Fatalf("reserve %d: ok=%v err=%v", i, ok, err)
		}
	}
	ok, until, reason, err := s.ProviderReserve(ctx, t0, "resend", 2, 3)
	if err != nil || ok || reason != "minute" || !until.Equal(t0.Truncate(time.Minute).Add(time.Minute)) {
		t.Fatalf("minute full: ok=%v until=%v reason=%q err=%v", ok, until, reason, err)
	}
	next := t0.Truncate(time.Minute).Add(time.Minute)
	if ok, _, _, err := s.ProviderReserve(ctx, next, "resend", 2, 3); err != nil || !ok {
		t.Fatalf("next minute: ok=%v err=%v", ok, err)
	}
	midnight := time.Date(2026, 11, 14, 0, 0, 0, 0, time.UTC)
	ok, until, reason, err = s.ProviderReserve(ctx, next, "resend", 2, 3)
	if err != nil || ok || reason != "day" || !until.Equal(midnight) {
		t.Fatalf("day full: ok=%v until=%v reason=%q err=%v", ok, until, reason, err)
	}
	if n := count(t, s, `SELECT count FROM rate_counters WHERE scope='provider:resend' AND window_kind='day'`); n != 3 {
		t.Fatalf("day counter = %d, want 3", n)
	}
	if err := s.ProviderRelease(ctx, next, "resend"); err != nil {
		t.Fatal(err)
	}
	if ok, _, _, err := s.ProviderReserve(ctx, next, "resend", 2, 3); err != nil || !ok {
		t.Fatalf("after release: ok=%v err=%v", ok, err)
	}
}

func TestProviderReservePause(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	until := t0.Add(time.Hour)
	if err := s.PauseProvider(ctx, "resend", until, "daily_quota"); err != nil {
		t.Fatal(err)
	}
	ok, got, reason, err := s.ProviderReserve(ctx, t0, "resend", 10, 10)
	if err != nil || ok || reason != "pause:daily_quota" || !got.Equal(until) {
		t.Fatalf("paused: ok=%v until=%v reason=%q err=%v", ok, got, reason, err)
	}
	if n := count(t, s, `SELECT COUNT(*) FROM rate_counters`); n != 0 {
		t.Fatalf("counters written while paused: %d", n)
	}
	if ok, _, _, err := s.ProviderReserve(ctx, until, "resend", 10, 10); err != nil || !ok {
		t.Fatalf("pause over: ok=%v err=%v", ok, err)
	}
}

func TestRecordAttemptAcceptedDeletesBodies(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	id := enqueueOne(t, s, "k1", t0)
	claimOne(t, s, t0)
	now := t0.Add(250 * time.Millisecond)
	if err := s.RecordAttempt(ctx, now, id, provider.SendResult{Outcome: provider.Accepted, ProviderID: "re_1"}, t0, time.Minute); err != nil {
		t.Fatal(err)
	}
	if n := count(t, s, `SELECT COUNT(*) FROM messages WHERE id=? AND state='sent' AND sent_at=? AND provider_message_id='re_1'
		AND attempts=1 AND locked_until IS NULL AND next_poll_at IS NULL AND body_bytes=?
		AND subject IS NULL AND body_text IS NULL AND body_html IS NULL AND reply_to IS NULL AND sms_body IS NULL`,
		id, now.UnixMilli(), len("hi")+len("hello")); n != 1 {
		t.Fatalf("sent row not as expected")
	}
	if n := count(t, s, `SELECT COUNT(*) FROM attempts WHERE message_id=? AND n=1 AND outcome='accepted' AND started_at=? AND duration_ms=250`, id, t0.UnixMilli()); n != 1 {
		t.Fatalf("attempt row not as expected")
	}
}

func TestRecordAttemptRetryableRequeues(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	id := enqueueOne(t, s, "k1", t0)
	claimOne(t, s, t0)
	r := provider.SendResult{Outcome: provider.Retryable, ErrorType: model.ErrTimeout, Status: 503, ProviderError: "unavailable", Message: "try later"}
	if err := s.RecordAttempt(ctx, t0, id, r, t0, 30*time.Second); err != nil {
		t.Fatal(err)
	}
	if n := count(t, s, `SELECT COUNT(*) FROM messages WHERE id=? AND state='queued' AND attempts=1 AND next_attempt_at=? AND locked_until IS NULL AND subject='hi'`,
		id, t0.Add(30*time.Second).UnixMilli()); n != 1 {
		t.Fatalf("requeued row not as expected")
	}
	if n := count(t, s, `SELECT COUNT(*) FROM attempts WHERE message_id=? AND n=1 AND outcome='retryable' AND error_type='timeout'
		AND provider_status=503 AND provider_error='unavailable' AND message='try later'`, id); n != 1 {
		t.Fatalf("attempt row not as expected")
	}
}

func TestRecordAttemptRetryableExhaustedFails(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	id := enqueueOne(t, s, "k1", t0)
	if _, err := s.w.Exec(`UPDATE messages SET attempts=? WHERE id=?`, constants.MaxAttempts-1, id); err != nil {
		t.Fatal(err)
	}
	claimOne(t, s, t0)
	r := provider.SendResult{Outcome: provider.Retryable, ErrorType: model.ErrNetwork}
	if err := s.RecordAttempt(ctx, t0, id, r, t0, time.Minute); err != nil {
		t.Fatal(err)
	}
	if n := count(t, s, `SELECT COUNT(*) FROM messages WHERE id=? AND state='failed' AND attempts=? AND error_type='network' AND final_at=? AND subject IS NULL AND body_text IS NULL`,
		id, constants.MaxAttempts, t0.UnixMilli()); n != 1 {
		t.Fatalf("exhausted row not as expected")
	}
}

func TestRecordAttemptPermanentFails(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	id := enqueueOne(t, s, "k1", t0)
	claimOne(t, s, t0)
	r := provider.SendResult{Outcome: provider.Permanent, ErrorType: model.ErrInvalidRecipient, Status: 422}
	if err := s.RecordAttempt(ctx, t0, id, r, t0, time.Minute); err != nil {
		t.Fatal(err)
	}
	if n := count(t, s, `SELECT COUNT(*) FROM messages WHERE id=? AND state='failed' AND attempts=1 AND error_type='invalid_recipient' AND final_at=? AND subject IS NULL`,
		id, t0.UnixMilli()); n != 1 {
		t.Fatalf("failed row not as expected")
	}
	if n := count(t, s, `SELECT COUNT(*) FROM attempts WHERE message_id=? AND outcome='permanent'`, id); n != 1 {
		t.Fatalf("attempt row missing")
	}
}

func TestRecordAttemptRequiresSending(t *testing.T) {
	s := newStore(t)
	id := enqueueOne(t, s, "k1", t0)
	err := s.RecordAttempt(context.Background(), t0, id, provider.SendResult{Outcome: provider.Accepted, ProviderID: "re_1"}, t0, time.Minute)
	if err == nil {
		t.Fatal("RecordAttempt on a queued row succeeded")
	}
	if n := count(t, s, `SELECT COUNT(*) FROM messages WHERE id=? AND state='queued' AND attempts=0 AND subject='hi'`, id); n != 1 {
		t.Fatalf("row changed")
	}
	if n := count(t, s, `SELECT COUNT(*) FROM attempts`); n != 0 {
		t.Fatalf("attempt written")
	}
}

func TestUnclaim(t *testing.T) {
	s := newStore(t)
	id := enqueueOne(t, s, "k1", t0)
	claimOne(t, s, t0)
	if err := s.Unclaim(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if n := count(t, s, `SELECT COUNT(*) FROM messages WHERE id=? AND state='queued' AND attempts=0 AND locked_until IS NULL`, id); n != 1 {
		t.Fatalf("unclaimed row not as expected")
	}
	claimOne(t, s, t0)
}

func TestExpireDue(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	id := enqueueOne(t, s, "k1", t0)
	enqueueOne(t, s, "k2", t0.Add(time.Second))
	n, err := s.ExpireDue(ctx, t0.Add(time.Hour))
	if err != nil || n != 1 {
		t.Fatalf("expired %d err %v, want 1", n, err)
	}
	if c := count(t, s, `SELECT COUNT(*) FROM messages WHERE id=? AND state='expired' AND error_type='expired' AND final_at=? AND subject IS NULL AND body_text IS NULL AND body_bytes>0`,
		id, t0.Add(time.Hour).UnixMilli()); c != 1 {
		t.Fatalf("expired row not as expected")
	}
}

func TestExpireClaimed(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	id := enqueueOne(t, s, "k1", t0)
	if err := s.ExpireClaimed(ctx, t0, id); err == nil {
		t.Fatal("ExpireClaimed on a queued row succeeded")
	}
	claimOne(t, s, t0)
	if err := s.ExpireClaimed(ctx, t0.Add(time.Hour), id); err != nil {
		t.Fatal(err)
	}
	if c := count(t, s, `SELECT COUNT(*) FROM messages WHERE id=? AND state='expired' AND error_type='expired' AND locked_until IS NULL AND subject IS NULL`, id); c != 1 {
		t.Fatalf("expired row not as expected")
	}
}

func TestReleaseHeld(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	in := input("app1", "k1", "b", "+15550001", "+15550002")
	in.Channel, in.Held, in.Provider = model.ChannelSMS, true, ""
	if _, err := s.Enqueue(ctx, t0, in); err != nil {
		t.Fatal(err)
	}
	enqueueOne(t, s, "k2", t0)
	n, err := s.ReleaseHeld(ctx, model.ChannelSMS, "fake")
	if err != nil || n != 2 {
		t.Fatalf("released %d err %v, want 2", n, err)
	}
	if c := count(t, s, `SELECT COUNT(*) FROM messages WHERE channel='sms' AND provider='fake' AND hold_reason IS NULL AND state='queued'`); c != 2 {
		t.Fatalf("released rows = %d", c)
	}
	if c := count(t, s, `SELECT COUNT(*) FROM messages WHERE channel='email' AND provider='resend'`); c != 1 {
		t.Fatalf("email row touched")
	}
}
