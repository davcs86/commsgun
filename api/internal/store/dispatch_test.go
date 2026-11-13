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
	got, err := s.ClaimDue(context.Background(), now, 1, model.ChannelEmail, "resend")
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

	got, err := s.ClaimDue(ctx, t0, 10, model.ChannelEmail, "resend")
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
	if _, err := s.w.Exec(`UPDATE messages SET provider='twilio'`); err != nil {
		t.Fatal(err)
	}
	got, err := s.ClaimDue(context.Background(), t0, 10, model.ChannelSMS, "twilio")
	if err != nil || len(got) != 0 {
		t.Fatalf("claimed %d err %v, want none", len(got), err)
	}
	if due, err := s.HasDue(context.Background(), t0, model.ChannelSMS, "twilio"); err != nil || due {
		t.Fatalf("HasDue = %v (%v) for a held row", due, err)
	}
}

func TestLeaseBlocksSecondClaim(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	id := enqueueOne(t, s, "k1", t0)
	claimOne(t, s, t0)
	got, err := s.ClaimDue(ctx, t0.Add(constants.LeaseDuration-time.Millisecond), 10, model.ChannelEmail, "resend")
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
		ok, _, _, err := s.ProviderReserve(ctx, t0, model.ChannelEmail, "resend", 2, 3)
		if err != nil || !ok {
			t.Fatalf("reserve %d: ok=%v err=%v", i, ok, err)
		}
	}
	ok, until, reason, err := s.ProviderReserve(ctx, t0, model.ChannelEmail, "resend", 2, 3)
	if err != nil || ok || reason != "minute" || !until.Equal(t0.Truncate(time.Minute).Add(time.Minute)) {
		t.Fatalf("minute full: ok=%v until=%v reason=%q err=%v", ok, until, reason, err)
	}
	next := t0.Truncate(time.Minute).Add(time.Minute)
	if ok, _, _, err := s.ProviderReserve(ctx, next, model.ChannelEmail, "resend", 2, 3); err != nil || !ok {
		t.Fatalf("next minute: ok=%v err=%v", ok, err)
	}
	midnight := time.Date(2026, 11, 14, 0, 0, 0, 0, time.UTC)
	ok, until, reason, err = s.ProviderReserve(ctx, next, model.ChannelEmail, "resend", 2, 3)
	if err != nil || ok || reason != "day" || !until.Equal(midnight) {
		t.Fatalf("day full: ok=%v until=%v reason=%q err=%v", ok, until, reason, err)
	}
	if n := count(t, s, `SELECT count FROM rate_counters WHERE scope='provider:email:resend' AND window_kind='day'`); n != 3 {
		t.Fatalf("day counter = %d, want 3", n)
	}
	if err := s.ProviderRelease(ctx, next, model.ChannelEmail, "resend"); err != nil {
		t.Fatal(err)
	}
	if ok, _, _, err := s.ProviderReserve(ctx, next, model.ChannelEmail, "resend", 2, 3); err != nil || !ok {
		t.Fatalf("after release: ok=%v err=%v", ok, err)
	}
}

func TestProviderReservePause(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	until := t0.Add(time.Hour)
	if err := s.PauseProvider(ctx, model.ChannelEmail, "resend", until, "daily_quota"); err != nil {
		t.Fatal(err)
	}
	ok, got, reason, err := s.ProviderReserve(ctx, t0, model.ChannelEmail, "resend", 10, 10)
	if err != nil || ok || reason != "pause:daily_quota" || !got.Equal(until) {
		t.Fatalf("paused: ok=%v until=%v reason=%q err=%v", ok, got, reason, err)
	}
	if n := count(t, s, `SELECT COUNT(*) FROM rate_counters`); n != 0 {
		t.Fatalf("counters written while paused: %d", n)
	}
	if ok, _, _, err := s.ProviderReserve(ctx, until, model.ChannelEmail, "resend", 10, 10); err != nil || !ok {
		t.Fatalf("pause over: ok=%v err=%v", ok, err)
	}
}

func TestRecordAttemptAcceptedDeletesBodies(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	id := enqueueOne(t, s, "k1", t0)
	claimOne(t, s, t0)
	now := t0.Add(250 * time.Millisecond)
	if err := s.RecordAttempt(ctx, now, id, provider.SendResult{Outcome: provider.Accepted, ProviderID: "re_1"}, t0, time.Minute, time.Time{}); err != nil {
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
	if err := s.RecordAttempt(ctx, t0, id, r, t0, 30*time.Second, time.Time{}); err != nil {
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
	if err := s.RecordAttempt(ctx, t0, id, r, t0, time.Minute, time.Time{}); err != nil {
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
	if err := s.RecordAttempt(ctx, t0, id, r, t0, time.Minute, time.Time{}); err != nil {
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
	err := s.RecordAttempt(context.Background(), t0, id, provider.SendResult{Outcome: provider.Accepted, ProviderID: "re_1"}, t0, time.Minute, time.Time{})
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
	if err := s.Unclaim(context.Background(), t0.Add(time.Second), id); err != nil {
		t.Fatal(err)
	}
	if n := count(t, s, `SELECT COUNT(*) FROM messages WHERE id=? AND state='queued' AND attempts=0 AND locked_until IS NULL AND updated_at=?`, id, t0.Add(time.Second).UnixMilli()); n != 1 {
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

func TestRecordAttemptEmptyErrorTypeIsUnknown(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	retry, perm := enqueueOne(t, s, "k1", t0), enqueueOne(t, s, "k2", t0)
	if _, err := s.ClaimDue(ctx, t0, 2, model.ChannelEmail, "resend"); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordAttempt(ctx, t0, retry, provider.SendResult{Outcome: provider.Retryable}, t0, time.Minute, time.Time{}); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordAttempt(ctx, t0, perm, provider.SendResult{Outcome: provider.Permanent}, t0, time.Minute, time.Time{}); err != nil {
		t.Fatal(err)
	}
	if n := count(t, s, `SELECT COUNT(*) FROM attempts WHERE error_type='unknown'`); n != 2 {
		t.Fatalf("attempts with unknown = %d, want 2", n)
	}
	if n := count(t, s, `SELECT COUNT(*) FROM messages WHERE id=? AND state='failed' AND error_type='unknown'`, perm); n != 1 {
		t.Fatal("permanent failure without unknown error_type")
	}
}

func TestExpireDueDeadLeaseOfRemovedProvider(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	id := enqueueOne(t, s, "k1", t0)
	claimOne(t, s, t0)
	if got, err := s.ClaimDue(ctx, t0.Add(2*time.Hour), 10, model.ChannelEmail, "other"); err != nil || len(got) != 0 {
		t.Fatalf("claimed %d via another provider, err %v", len(got), err)
	}
	live := t0.Add(constants.LeaseDuration - time.Millisecond)
	if _, err := s.w.Exec(`UPDATE messages SET expires_at=? WHERE id=?`, t0.UnixMilli(), id); err != nil {
		t.Fatal(err)
	}
	if n, err := s.ExpireDue(ctx, live); err != nil || n != 0 {
		t.Fatalf("expired %d with a live lease, err %v", n, err)
	}
	now := t0.Add(2 * time.Hour)
	if n, err := s.ExpireDue(ctx, now); err != nil || n != 1 {
		t.Fatalf("expired %d, err %v, want 1", n, err)
	}
	if c := count(t, s, `SELECT COUNT(*) FROM messages WHERE id=? AND state='expired' AND error_type='expired' AND final_at=?
		AND locked_until IS NULL AND subject IS NULL AND body_text IS NULL AND body_bytes>0`, id, now.UnixMilli()); c != 1 {
		t.Fatal("dead-lease row not expired")
	}
}

func TestChannelsDoNotMix(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	mail := input("app1", "k1", "k1", "r@x.com")
	mail.Provider = "fake"
	sms := input("app1", "k2", "k2", "+15550001")
	sms.Channel, sms.Provider = model.ChannelSMS, "fake"
	for _, in := range []EnqueueInput{mail, sms} {
		if _, err := s.Enqueue(ctx, t0, in); err != nil {
			t.Fatal(err)
		}
	}
	for _, ch := range []model.Channel{model.ChannelEmail, model.ChannelSMS} {
		got, err := s.ClaimDue(ctx, t0, 10, ch, "fake")
		if err != nil || len(got) != 1 || got[0].Channel != ch {
			t.Fatalf("%s pass claimed %+v err %v", ch, got, err)
		}
	}
	if ok, _, _, err := s.ProviderReserve(ctx, t0, model.ChannelEmail, "fake", 1, 1); err != nil || !ok {
		t.Fatalf("email reserve ok=%v err=%v", ok, err)
	}
	if ok, _, _, err := s.ProviderReserve(ctx, t0, model.ChannelSMS, "fake", 1, 1); err != nil || !ok {
		t.Fatalf("sms reserve blocked by email counters: ok=%v err=%v", ok, err)
	}
	if err := s.PauseProvider(ctx, model.ChannelEmail, "fake", t0.Add(time.Hour), "rate_limit"); err != nil {
		t.Fatal(err)
	}
	if _, _, reason, err := s.ProviderReserve(ctx, t0.Add(2*time.Minute), model.ChannelSMS, "fake", 1, 10); err != nil || reason != "" {
		t.Fatalf("sms paused by email pause: reason=%q err=%v", reason, err)
	}
	if n := count(t, s, `SELECT COUNT(*) FROM rate_counters WHERE scope IN ('provider:email:fake','provider:sms:fake') AND window_kind='day'`); n != 2 {
		t.Fatalf("separate day counters = %d, want 2", n)
	}
}

func TestRecordAttemptSchedulesFirstPollAtomically(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	id := enqueueOne(t, s, "k1", t0)
	claimOne(t, s, t0)
	poll := t0.Add(time.Minute)
	if err := s.RecordAttempt(ctx, t0, id, provider.SendResult{Outcome: provider.Accepted, ProviderID: "re_1"}, t0, time.Minute, poll); err != nil {
		t.Fatal(err)
	}
	if n := count(t, s, `SELECT COUNT(*) FROM messages WHERE id=? AND state='sent' AND next_poll_at=?`, id, poll.UnixMilli()); n != 1 {
		t.Fatal("sent row lacks next_poll_at")
	}
}

func TestRecordAttemptNonAcceptedNeverSchedulesPoll(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	retry := enqueueOne(t, s, "k1", t0)
	perm := enqueueOne(t, s, "k2", t0)
	claimOne(t, s, t0)
	claimOne(t, s, t0)
	poll := t0.Add(time.Minute)
	if err := s.RecordAttempt(ctx, t0, retry, provider.SendResult{Outcome: provider.Retryable}, t0, time.Minute, poll); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordAttempt(ctx, t0, perm, provider.SendResult{Outcome: provider.Permanent}, t0, time.Minute, poll); err != nil {
		t.Fatal(err)
	}
	if n := count(t, s, `SELECT COUNT(*) FROM messages WHERE next_poll_at IS NOT NULL`); n != 0 {
		t.Fatalf("%d rows scheduled for polling", n)
	}
}

func TestExpireDueAtIdempotencySafetyMargin(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	in := input("app1", "k1", "b", "r@x.com")
	in.ExpiresAt = t0.Add(constants.MaxSendWindow)
	res, err := s.Enqueue(ctx, t0, in)
	if err != nil {
		t.Fatal(err)
	}
	cutoff := t0.Add(constants.MaxSendWindow - constants.IdempotencySafetyMargin)
	if n, err := s.ExpireDue(ctx, cutoff.Add(-time.Millisecond)); err != nil || n != 0 {
		t.Fatalf("expired %d (%v) before the margin", n, err)
	}
	if n, err := s.ExpireDue(ctx, cutoff); err != nil || n != 1 {
		t.Fatalf("expired %d (%v) at the margin, want 1", n, err)
	}
	if n := count(t, s, `SELECT COUNT(*) FROM messages WHERE id=? AND state='expired' AND error_type='expired' AND subject IS NULL`, res.MessageIDs[0]); n != 1 {
		t.Fatal("message not expired at the margin")
	}
}
