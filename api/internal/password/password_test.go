package password

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"
)

var tiny = Params{MemoryKiB: 64, Time: 1, Threads: 1}

const pw = "correct horse battery staple"

func mustHash(t *testing.T, h *Hasher, p string) string {
	t.Helper()
	phc, err := h.Hash(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	return phc
}

func TestHashIsPHC(t *testing.T) {
	h := NewHasher(Default(), 1)
	a, b := mustHash(t, h, pw), mustHash(t, h, pw)
	const prefix = "$argon2id$v=19$m=19456,t=2,p=1$"
	if !strings.HasPrefix(a, prefix) {
		t.Fatalf("hash %q lacks prefix %q", a, prefix)
	}
	parts := strings.Split(strings.TrimPrefix(a, prefix), "$")
	if len(parts) != 2 {
		t.Fatalf("want salt$key after the prefix, got %d parts", len(parts))
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[0])
	if err != nil || len(salt) != 16 {
		t.Fatalf("salt: %d bytes, err %v", len(salt), err)
	}
	key, err := base64.RawStdEncoding.DecodeString(parts[1])
	if err != nil || len(key) != 32 {
		t.Fatalf("key: %d bytes, err %v", len(key), err)
	}
	if a == b {
		t.Fatal("two hashes of one password are equal")
	}
}

func TestVerify(t *testing.T) {
	h := NewHasher(tiny, 1)
	phc := mustHash(t, h, pw)
	cases := []struct {
		name, pw string
		want     bool
	}{
		{"right", pw, true},
		{"wrong", "incorrect horse battery staple", false},
		{"prefix", pw[:len(pw)-1], false},
		{"longer", pw + "x", false},
	}
	for _, c := range cases {
		ok, err := h.Verify(context.Background(), c.pw, phc)
		if err != nil || ok != c.want {
			t.Errorf("%s: got (%v, %v), want (%v, nil)", c.name, ok, err, c.want)
		}
	}
}

func TestVerifyReadsParamsFromHash(t *testing.T) {
	phc := mustHash(t, NewHasher(Params{MemoryKiB: 64, Time: 1, Threads: 1}, 1), pw)
	if !strings.HasPrefix(phc, "$argon2id$v=19$m=64,t=1,p=1$") {
		t.Fatalf("hash %q not made with the tiny params", phc)
	}
	ok, err := NewHasher(Default(), 1).Verify(context.Background(), pw, phc)
	if err != nil || !ok {
		t.Fatalf("got (%v, %v), want (true, nil)", ok, err)
	}
	higher := mustHash(t, NewHasher(Params{MemoryKiB: 128, Time: 2, Threads: 2}, 1), pw)
	if ok, err := NewHasher(tiny, 1).Verify(context.Background(), pw, higher); err != nil || !ok {
		t.Fatalf("params above the Hasher's: got (%v, %v), want (true, nil)", ok, err)
	}
}

func TestVerifyMalformed(t *testing.T) {
	h := NewHasher(tiny, 1)
	phc := mustHash(t, h, pw)
	f := strings.Split(phc, "$") // "", argon2id, v=19, params, salt, key
	join := func(parts ...string) string { return strings.Join(parts, "$") }
	cases := map[string]string{
		"empty":         "",
		"argon2i":       join("", "argon2i", f[2], f[3], f[4], f[5]),
		"bad version":   join("", "argon2id", "v=16", f[3], f[4], f[5]),
		"bad salt b64":  join("", "argon2id", f[2], f[3], "!!!!", f[5]),
		"bad key b64":   join("", "argon2id", f[2], f[3], f[4], f[5]+"="),
		"m=0":           join("", "argon2id", f[2], "m=0,t=1,p=1", f[4], f[5]),
		"t=0":           join("", "argon2id", f[2], "m=64,t=0,p=1", f[4], f[5]),
		"p=0":           join("", "argon2id", f[2], "m=64,t=1,p=0", f[4], f[5]),
		"m over 32 bit": join("", "argon2id", f[2], "m=4294967296,t=1,p=1", f[4], f[5]),
		"p over 8 bit":  join("", "argon2id", f[2], "m=64,t=1,p=256", f[4], f[5]),
		"m not a num":   join("", "argon2id", f[2], "m=x,t=1,p=1", f[4], f[5]),
		"params order":  join("", "argon2id", f[2], "t=1,m=64,p=1", f[4], f[5]),
		"missing param": join("", "argon2id", f[2], "m=64,t=1", f[4], f[5]),
		"missing field": join("", "argon2id", f[2], f[3], f[5]),
		"extra field":   phc + "$x",
		"short salt":    join("", "argon2id", f[2], f[3], f[4][:8], f[5]),
		"short key":     join("", "argon2id", f[2], f[3], f[4], f[5][:8]),
	}
	for name, in := range cases {
		ok, err := h.Verify(context.Background(), pw, in)
		if ok || err == nil {
			t.Errorf("%s: got (%v, %v), want (false, error)", name, ok, err)
		}
		if err != nil && strings.Contains(err.Error(), pw) {
			t.Errorf("%s: error carries the password", name)
		}
	}
}

func TestValid(t *testing.T) {
	cases := []struct {
		name, pw string
		want     bool
	}{
		{"15 code points", strings.Repeat("a", 15), false},
		{"16 code points", strings.Repeat("a", 16), true},
		{"15 emoji (60 bytes)", strings.Repeat("😀", 15), false},
		{"16 emoji", strings.Repeat("😀", 16), true},
		{"1024 bytes", strings.Repeat("a", 1024), true},
		{"1025 bytes", strings.Repeat("a", 1025), false},
	}
	for _, c := range cases {
		if got := Valid(c.pw); got != c.want {
			t.Errorf("%s: Valid = %v, want %v", c.name, got, c.want)
		}
	}
}

func cancelAfter(d time.Duration) context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(d, cancel)
	return ctx
}

func TestOneHashAtATime(t *testing.T) {
	h := NewHasher(tiny, 1)
	phc := mustHash(t, h, pw)
	h.slot <- struct{}{}
	start := time.Now()
	ok, err := h.Verify(cancelAfter(50*time.Millisecond), pw, phc)
	if ok || !errors.Is(err, context.Canceled) {
		t.Fatalf("Verify: got (%v, %v), want (false, context.Canceled)", ok, err)
	}
	if time.Since(start) < 50*time.Millisecond {
		t.Fatal("Verify returned before its context ended")
	}
	if _, err := h.Hash(cancelAfter(50*time.Millisecond), pw); !errors.Is(err, context.Canceled) {
		t.Fatalf("Hash while held: got %v, want context.Canceled", err)
	}
	<-h.slot
	if _, err := h.Hash(context.Background(), pw); err != nil {
		t.Fatalf("Hash after release: %v", err)
	}
}

func TestVerifyDummy(t *testing.T) {
	h := NewHasher(tiny, 1)
	for _, p := range []string{"", pw, strings.Repeat("😀", 256)} {
		if err := h.VerifyDummy(context.Background(), p); err != nil {
			t.Errorf("VerifyDummy(%d bytes) = %v", len(p), err)
		}
	}
	h.slot <- struct{}{}
	defer func() { <-h.slot }()
	start := time.Now()
	if err := h.VerifyDummy(cancelAfter(50*time.Millisecond), pw); !errors.Is(err, context.Canceled) {
		t.Fatalf("VerifyDummy while held: got %v, want context.Canceled", err)
	}
	if time.Since(start) < 50*time.Millisecond {
		t.Fatal("VerifyDummy returned before its context ended")
	}
}
