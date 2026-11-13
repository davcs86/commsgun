package store

import (
	"context"
	"testing"
	"time"

	"github.com/davcs86/commsgun/api/internal/config"
	"github.com/davcs86/commsgun/api/internal/constants"
	"github.com/davcs86/commsgun/api/internal/model"
)

func addAttempt(t *testing.T, s *Store, id string) {
	t.Helper()
	if _, err := s.w.Exec(`INSERT INTO attempts(message_id, n, started_at, duration_ms, outcome) VALUES (?,1,0,1,'retryable')`, id); err != nil {
		t.Fatal(err)
	}
}

func TestSweepDeletesAfter30Days(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	now := t0
	old := enqueueOne(t, s, "old", now.Add(-constants.Retention-time.Second))
	kept := enqueueOne(t, s, "kept", now.Add(-constants.Retention+time.Hour))
	addAttempt(t, s, old)
	addAttempt(t, s, kept)

	res, err := s.Sweep(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if res.Messages != 1 || res.Requests != 1 {
		t.Fatalf("res = %+v", res)
	}
	if n := count(t, s, `SELECT COUNT(*) FROM messages WHERE id=?`, old); n != 0 {
		t.Fatal("old message kept")
	}
	if n := count(t, s, `SELECT COUNT(*) FROM attempts WHERE message_id=?`, old); n != 0 {
		t.Fatal("old attempts kept")
	}
	if n := count(t, s, `SELECT COUNT(*) FROM requests`); n != 1 {
		t.Fatalf("requests = %d, want 1", n)
	}
	if n := count(t, s, `SELECT COUNT(*) FROM messages WHERE id=?`, kept); n != 1 {
		t.Fatal("recent message deleted")
	}
	if n := count(t, s, `SELECT COUNT(*) FROM attempts WHERE message_id=?`, kept); n != 1 {
		t.Fatal("recent attempts deleted")
	}
}

func TestSweepExpiresUnfinishedFirst(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	created := t0.Add(-10 * 24 * time.Hour)
	id := enqueueOne(t, s, "k", created)

	res, err := s.Sweep(ctx, t0)
	if err != nil {
		t.Fatal(err)
	}
	if res.Expired != 1 || res.Messages != 0 {
		t.Fatalf("res = %+v", res)
	}
	var state string
	var final *int64
	if err := s.w.QueryRow(`SELECT state, final_at FROM messages WHERE id=?`, id).Scan(&state, &final); err != nil {
		t.Fatal(err)
	}
	if state != string(model.StateExpired) || final == nil {
		t.Fatalf("state=%s final=%v", state, final)
	}
}

func TestSweepDropsStaleCountersAndPauses(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	ins := func(kind string, start time.Time) {
		if _, err := s.w.Exec(`INSERT INTO rate_counters VALUES ('x',?,?,1)`, kind, start.UnixMilli()); err != nil {
			t.Fatal(err)
		}
	}
	ins("minute", t0.Add(-constants.MinuteCounterMaxAge-time.Minute))
	ins("minute", t0.Add(-constants.MinuteCounterMaxAge+time.Minute))
	ins("day", t0.Add(-constants.DayCounterMaxAge-time.Minute))
	ins("day", t0.Add(-constants.DayCounterMaxAge+time.Minute))
	if err := s.PauseProvider(ctx, model.ChannelEmail, "resend", t0.Add(-time.Second), "quota"); err != nil {
		t.Fatal(err)
	}
	if err := s.PauseProvider(ctx, model.ChannelSMS, "twilio", t0.Add(time.Hour), "quota"); err != nil {
		t.Fatal(err)
	}

	res, err := s.Sweep(ctx, t0)
	if err != nil {
		t.Fatal(err)
	}
	if res.Counters != 2 {
		t.Fatalf("Counters = %d, want 2", res.Counters)
	}
	if n := count(t, s, `SELECT COUNT(*) FROM rate_counters`); n != 2 {
		t.Fatalf("counters left = %d", n)
	}
	if n := count(t, s, `SELECT COUNT(*) FROM provider_pauses`); n != 1 {
		t.Fatalf("pauses left = %d", n)
	}
}

func TestBacklog(t *testing.T) {
	ctx := context.Background()
	providers := map[model.Channel]*config.ProviderCfg{model.ChannelEmail: {Name: "resend", PerMinute: 100, PerDay: 1000}}

	t.Run("oldest due dispatchable", func(t *testing.T) {
		s := newStore(t)
		first := t0.Add(-20 * time.Minute)
		enqueueOne(t, s, "a", first)
		enqueueOne(t, s, "b", t0.Add(-5*time.Minute))
		got, ok, err := s.Backlog(ctx, t0, providers)
		if err != nil || !ok || !got.Equal(first) {
			t.Fatalf("got %v %v %v, want %v", got, ok, err, first)
		}
	})
	t.Run("empty", func(t *testing.T) {
		s := newStore(t)
		if _, ok, err := s.Backlog(ctx, t0, providers); err != nil || ok {
			t.Fatalf("ok=%v err=%v", ok, err)
		}
	})
	t.Run("not yet due", func(t *testing.T) {
		s := newStore(t)
		enqueueOne(t, s, "a", t0.Add(time.Minute))
		if _, ok, _ := s.Backlog(ctx, t0, providers); ok {
			t.Fatal("future row counted")
		}
	})
	t.Run("paused", func(t *testing.T) {
		s := newStore(t)
		enqueueOne(t, s, "a", t0.Add(-time.Hour))
		if err := s.PauseProvider(ctx, model.ChannelEmail, "resend", t0.Add(time.Hour), "quota"); err != nil {
			t.Fatal(err)
		}
		if _, ok, _ := s.Backlog(ctx, t0, providers); ok {
			t.Fatal("paused provider counted")
		}
		if _, ok, _ := s.Backlog(ctx, t0.Add(2*time.Hour), providers); !ok {
			t.Fatal("ended pause still hides backlog")
		}
	})
	t.Run("held no_provider", func(t *testing.T) {
		s := newStore(t)
		in := input("app1", "h", "h", "r@x.com")
		in.Held = true
		if _, err := s.Enqueue(ctx, t0.Add(-time.Hour), in); err != nil {
			t.Fatal(err)
		}
		if _, ok, _ := s.Backlog(ctx, t0, providers); ok {
			t.Fatal("held row counted")
		}
	})
	t.Run("window full", func(t *testing.T) {
		s := newStore(t)
		enqueueOne(t, s, "a", t0.Add(-time.Hour))
		tight := map[model.Channel]*config.ProviderCfg{model.ChannelEmail: {Name: "resend", PerMinute: 1, PerDay: 1000}}
		if ok, _, _, err := s.ProviderReserve(ctx, t0, model.ChannelEmail, "resend", 1, 1000); err != nil || !ok {
			t.Fatalf("reserve: %v %v", ok, err)
		}
		if _, ok, _ := s.Backlog(ctx, t0, tight); ok {
			t.Fatal("full minute window counted")
		}
		if _, ok, _ := s.Backlog(ctx, t0.Add(time.Minute), tight); !ok {
			t.Fatal("new window should have capacity")
		}
	})
	t.Run("unconfigured channel", func(t *testing.T) {
		s := newStore(t)
		enqueueOne(t, s, "a", t0.Add(-time.Hour))
		if _, ok, _ := s.Backlog(ctx, t0, nil); ok {
			t.Fatal("row without provider counted")
		}
	})
}

func TestPing(t *testing.T) {
	s := newStore(t)
	if err := s.Ping(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestSweepSentDeletedUnfinishedKept(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	oldAt := t0.Add(-constants.Retention - 24*time.Hour)
	sent := enqueueOne(t, s, "sent", oldAt)
	sending := enqueueOne(t, s, "sending", oldAt)
	addAttempt(t, s, sent)
	addAttempt(t, s, sending)
	if _, err := s.w.Exec(`UPDATE messages SET state=?, final_at=? WHERE id=?`, model.StateSent, oldAt.UnixMilli(), sent); err != nil {
		t.Fatal(err)
	}
	if _, err := s.w.Exec(`UPDATE messages SET state=?, locked_until=?, expires_at=? WHERE id=?`,
		model.StateSending, t0.Add(time.Minute).UnixMilli(), t0.Add(time.Hour).UnixMilli(), sending); err != nil {
		t.Fatal(err)
	}

	res, err := s.Sweep(ctx, t0)
	if err != nil {
		t.Fatal(err)
	}
	if res.Messages != 1 || res.Requests != 1 {
		t.Fatalf("res = %+v", res)
	}
	if n := count(t, s, `SELECT COUNT(*) FROM messages WHERE id=?`, sent); n != 0 {
		t.Fatal("old sent row kept")
	}
	if n := count(t, s, `SELECT COUNT(*) FROM attempts WHERE message_id=?`, sent); n != 0 {
		t.Fatal("old sent attempts kept")
	}
	if n := count(t, s, `SELECT COUNT(*) FROM messages WHERE id=?`, sending); n != 1 {
		t.Fatal("live sending row deleted")
	}
	if n := count(t, s, `SELECT COUNT(*) FROM attempts WHERE message_id=?`, sending); n != 1 {
		t.Fatal("live sending attempts deleted")
	}
	if n := count(t, s, `SELECT COUNT(*) FROM requests`); n != 1 {
		t.Fatalf("request of kept message deleted, requests = %d", n)
	}
}
