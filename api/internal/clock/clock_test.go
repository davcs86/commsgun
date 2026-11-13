package clock

import (
	"testing"
	"time"
)

func TestFake(t *testing.T) {
	start := time.Date(2026, 11, 13, 12, 0, 0, 0, time.FixedZone("x", 3600))
	f := NewFake(start)
	if !f.Now().Equal(start) || f.Now().Location() != time.UTC {
		t.Fatalf("Now = %v, want %v in UTC", f.Now(), start)
	}
	f.Advance(90 * time.Second)
	if want := start.Add(90 * time.Second); !f.Now().Equal(want) {
		t.Fatalf("after Advance Now = %v, want %v", f.Now(), want)
	}
	set := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	f.Set(set)
	if !f.Now().Equal(set) {
		t.Fatalf("after Set Now = %v, want %v", f.Now(), set)
	}
}

func TestRealIsUTCAndCurrent(t *testing.T) {
	var c Clock = Real{}
	n := c.Now()
	if n.Location() != time.UTC {
		t.Fatalf("location = %v, want UTC", n.Location())
	}
	if d := time.Since(n); d < -time.Second || d > time.Minute {
		t.Fatalf("Real.Now off by %v", d)
	}
}
