package model

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestStateFinal(t *testing.T) {
	want := map[State]bool{
		StateQueued: false, StateSending: false, StateSent: false,
		StateDelivered: true, StateBounced: true, StateComplained: true,
		StateFailed: true, StateExpired: true,
	}
	for s, final := range want {
		if s.Final() != final {
			t.Errorf("%s.Final() = %v, want %v", s, !final, final)
		}
	}
}

func TestMessageJSON(t *testing.T) {
	m := Message{ID: "m1", CreatedAt: time.Date(2026, 11, 13, 1, 2, 3, 0, time.UTC)}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, frag := range []string{`"created_at":"2026-11-13T01:02:03Z"`, `"sent_at":null`, `"error_type":null`} {
		if !strings.Contains(s, frag) {
			t.Errorf("%s missing %s", s, frag)
		}
	}
}
