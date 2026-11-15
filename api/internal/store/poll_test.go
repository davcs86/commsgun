package store

import (
	"context"
	"testing"
	"time"

	"github.com/davcs86/commsgun/api/internal/provider"
)

func TestDuePollsReadsCommittedAttemptFromReadPool(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	id := enqueueOne(t, s, "k1", t0)
	claimOne(t, s, t0)
	firstPoll := t0.Add(time.Minute)
	if err := s.RecordAttempt(ctx, t0, id, provider.SendResult{Outcome: provider.Accepted, ProviderID: "re_1"}, t0, time.Minute, firstPoll); err != nil {
		t.Fatal(err)
	}
	if got, err := s.DuePolls(ctx, firstPoll.Add(-time.Millisecond), 10); err != nil || len(got) != 0 {
		t.Fatalf("before due: %+v %v", got, err)
	}
	got, err := s.DuePolls(ctx, firstPoll, 10)
	if err != nil || len(got) != 1 || got[0].ID != id || got[0].ProviderMessageID == nil || *got[0].ProviderMessageID != "re_1" {
		t.Fatalf("due: %+v %v", got, err)
	}
	if got, err := s.DuePolls(ctx, firstPoll, 0); err != nil || got != nil {
		t.Fatalf("limit 0: %+v %v", got, err)
	}
}
