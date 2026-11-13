package store

import (
	"context"
	"crypto/sha256"
	"testing"
)

func TestLookupRequest(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	created, err := s.Enqueue(ctx, t0, input("app1", "k1", "b", "a@x.com", "b@x.com"))
	if err != nil {
		t.Fatal(err)
	}
	hash, res, found, err := s.LookupRequest(ctx, "app1", "k1")
	if err != nil || !found {
		t.Fatalf("found=%v err=%v", found, err)
	}
	if hash != sha256.Sum256([]byte("b")) || !res.Duplicate || res.RequestID != created.RequestID ||
		len(res.MessageIDs) != 2 || res.MessageIDs[0] != created.MessageIDs[0] || res.MessageIDs[1] != created.MessageIDs[1] {
		t.Fatalf("got %+v hash %x", res, hash)
	}
}

func TestLookupRequestNotFoundAndOtherApp(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	if _, err := s.Enqueue(ctx, t0, input("app1", "k1", "b", "a@x.com")); err != nil {
		t.Fatal(err)
	}
	for _, c := range [][2]string{{"app1", "nope"}, {"app2", "k1"}} {
		if _, _, found, err := s.LookupRequest(ctx, c[0], c[1]); found || err != nil {
			t.Fatalf("%v: found=%v err=%v", c, found, err)
		}
	}
}
