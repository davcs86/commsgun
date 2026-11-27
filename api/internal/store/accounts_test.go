package store

import (
	"context"
	"crypto/sha256"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/davcs86/commsgun/api/internal/constants"
	"github.com/davcs86/commsgun/api/internal/model"
)

const testHash = "$argon2id$v=19$m=64,t=1,p=1$c2FsdA$a2V5"

func tok(s string) [32]byte { return sha256.Sum256([]byte(s)) }

func addUser(t *testing.T, s *Store, name string) User {
	t.Helper()
	u, err := s.AddUser(context.Background(), t0, name, testHash)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func newSession(t *testing.T, s *Store, userID, token string) {
	t.Helper()
	if _, err := s.CreateSession(context.Background(), t0, userID, model.KindLocal, tok(token)); err != nil {
		t.Fatal(err)
	}
}

func TestValidUsername(t *testing.T) {
	cases := map[string]bool{
		"ab": false, "abc": true, strings.Repeat("a", 32): true, strings.Repeat("a", 33): false,
		"Admin": false, "a b": false, "a.b_c-d": true, "": false, "ü12": false,
	}
	for in, want := range cases {
		if got := ValidUsername(in); got != want {
			t.Errorf("ValidUsername(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestCreateFirstUserOnlyWhileEmpty(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	u, err := s.CreateFirstUser(ctx, t0, "admin", testHash)
	if err != nil {
		t.Fatal(err)
	}
	if u.ID == "" || u.Username != "admin" || u.Role != "admin" || u.Disabled {
		t.Fatalf("user = %+v", u)
	}
	if _, err := s.CreateFirstUser(ctx, t0, "other", testHash); !errors.Is(err, ErrUsersExist) {
		t.Fatalf("second call: err = %v, want ErrUsersExist", err)
	}
	if n, err := s.CountUsers(ctx); err != nil || n != 1 {
		t.Fatalf("CountUsers = %d, %v", n, err)
	}
	got, hash, err := s.UserByUsername(ctx, "admin")
	if err != nil || got != u || hash != testHash {
		t.Fatalf("UserByUsername = %+v, %q, %v", got, hash, err)
	}
	if _, _, err := s.UserByUsername(ctx, "nobody"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown: err = %v", err)
	}
}

func TestCreateFirstUserRace(t *testing.T) {
	s := newStore(t)
	const n = 8
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = s.CreateFirstUser(context.Background(), t0, "admin"+string(rune('a'+i)), testHash)
		}()
	}
	wg.Wait()
	ok, exist := 0, 0
	for _, err := range errs {
		switch {
		case err == nil:
			ok++
		case errors.Is(err, ErrUsersExist):
			exist++
		default:
			t.Fatalf("unexpected err %v", err)
		}
	}
	if ok != 1 || exist != n-1 {
		t.Fatalf("ok = %d, exist = %d", ok, exist)
	}
	if c := count(t, s, `SELECT COUNT(*) FROM users`); c != 1 {
		t.Fatalf("users = %d", c)
	}
}

func TestAddUserNamesArePermanent(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	addUser(t, s, "alice")
	if _, err := s.AddUser(ctx, t0, "alice", testHash); !errors.Is(err, ErrUserExists) {
		t.Fatalf("duplicate: err = %v, want ErrUserExists", err)
	}
	if err := s.SetDisabled(ctx, t0, "alice", true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddUser(ctx, t0, "alice", testHash); !errors.Is(err, ErrUserExists) {
		t.Fatalf("duplicate of a disabled user: err = %v, want ErrUserExists", err)
	}
	if u := addUser(t, s, "bob"); u.Role != "admin" {
		t.Fatalf("role = %q", u.Role)
	}
	if err := s.SetPassword(ctx, t0, "nobody", testHash); !errors.Is(err, ErrNotFound) {
		t.Fatalf("SetPassword unknown: err = %v", err)
	}
	if err := s.SetDisabled(ctx, t0, "nobody", true); !errors.Is(err, ErrNotFound) {
		t.Fatalf("SetDisabled unknown: err = %v", err)
	}
	if _, err := s.Unlink(ctx, t0, "nobody"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Unlink unknown: err = %v", err)
	}
}

func TestSessionLifetime(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	u := addUser(t, s, "alice")
	exp, err := s.CreateSession(ctx, t0, u.ID, model.KindOIDC, tok("a"))
	if err != nil {
		t.Fatal(err)
	}
	if !exp.Equal(t0.Add(constants.SessionTTL)) || constants.SessionTTL != 12*time.Hour {
		t.Fatalf("expires = %v", exp)
	}
	got, err := s.SessionByHash(ctx, exp.Add(-time.Millisecond), tok("a"))
	if err != nil {
		t.Fatal(err)
	}
	if got.User != u || got.Kind != model.KindOIDC || !got.ExpiresAt.Equal(exp) || got.ReauthAt != nil {
		t.Fatalf("session = %+v", got)
	}
	if _, err := s.SessionByHash(ctx, exp, tok("a")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("at expiry: err = %v", err)
	}
	if _, err := s.SessionByHash(ctx, t0, tok("missing")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing: err = %v", err)
	}
	if err := s.DeleteSession(ctx, tok("a")); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteSession(ctx, tok("a")); err != nil {
		t.Fatalf("absent: err = %v", err)
	}
	if _, err := s.SessionByHash(ctx, t0, tok("a")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after delete: err = %v", err)
	}
}

func TestEndingAccessDeletesSessions(t *testing.T) {
	ctx := context.Background()
	steps := map[string]func(s *Store) error{
		"SetPassword": func(s *Store) error { return s.SetPassword(ctx, t0, "alice", "$argon2id$new") },
		"SetDisabled": func(s *Store) error { return s.SetDisabled(ctx, t0, "alice", true) },
		"Unlink":      func(s *Store) error { _, err := s.Unlink(ctx, t0, "alice"); return err },
	}
	for name, step := range steps {
		t.Run(name, func(t *testing.T) {
			s := newStore(t)
			a, b := addUser(t, s, "alice"), addUser(t, s, "bob")
			newSession(t, s, a.ID, "a1")
			newSession(t, s, a.ID, "a2")
			newSession(t, s, b.ID, "b1")
			if err := step(s); err != nil {
				t.Fatal(err)
			}
			if n := count(t, s, `SELECT COUNT(*) FROM sessions WHERE user_id=?`, a.ID); n != 0 {
				t.Fatalf("alice sessions = %d", n)
			}
			if _, err := s.SessionByHash(ctx, t0, tok("b1")); err != nil {
				t.Fatalf("bob's session: %v", err)
			}
		})
	}
}

func TestDisabledUserSessionRefused(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	u := addUser(t, s, "alice")
	newSession(t, s, u.ID, "a")
	if _, err := s.w.Exec(`UPDATE users SET disabled=1 WHERE id=?`, u.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SessionByHash(ctx, t0, tok("a")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestLinkUniqueness(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	a, b := addUser(t, s, "alice"), addUser(t, s, "bob")
	const iss, iss2 = "https://idp.example", "https://other.example"
	if err := s.Link(ctx, t0, a.ID, iss, "sub-a"); err != nil {
		t.Fatal(err)
	}
	if err := s.Link(ctx, t0, b.ID, iss, "sub-a"); !errors.Is(err, ErrAlreadyLinked) {
		t.Fatalf("pair on another user: err = %v", err)
	}
	if err := s.Link(ctx, t0, a.ID, iss, "sub-other"); !errors.Is(err, ErrAlreadyLinked) {
		t.Fatalf("second identity for the issuer: err = %v", err)
	}
	if err := s.Link(ctx, t0, a.ID, iss, "sub-a"); !errors.Is(err, ErrAlreadyLinked) {
		t.Fatalf("same pair twice: err = %v", err)
	}
	if err := s.Link(ctx, t0, a.ID, iss2, "sub-a"); err != nil {
		t.Fatalf("another issuer: %v", err)
	}
	if err := s.Link(ctx, t0, "no-such-id", iss, "x"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown user: err = %v", err)
	}
	got, err := s.UserByIdentity(ctx, iss, "sub-a")
	if err != nil || got != a {
		t.Fatalf("UserByIdentity = %+v, %v", got, err)
	}
	if _, err := s.UserByIdentity(ctx, iss, "nobody"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unlinked: err = %v", err)
	}
	n, err := s.Unlink(ctx, t0, "alice")
	if err != nil || n != 2 {
		t.Fatalf("Unlink = %d, %v", n, err)
	}
	if _, err := s.UserByIdentity(ctx, iss, "sub-a"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after unlink: err = %v", err)
	}
}

func TestReauthFailures(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	u := addUser(t, s, "alice")
	newSession(t, s, u.ID, "a")
	if constants.ReauthMaxFailures != 5 {
		t.Fatalf("ReauthMaxFailures = %d", constants.ReauthMaxFailures)
	}
	for i := 1; i < constants.ReauthMaxFailures; i++ {
		if deleted, err := s.RecordReauthFailure(ctx, tok("a")); err != nil || deleted {
			t.Fatalf("failure %d: deleted = %v, err = %v", i, deleted, err)
		}
		if i == 2 {
			if err := s.MarkReauth(ctx, t0, tok("a")); err != nil {
				t.Fatal(err)
			}
		}
	}
	deleted, err := s.RecordReauthFailure(ctx, tok("a"))
	if err != nil || !deleted {
		t.Fatalf("5th failure: deleted = %v, err = %v", deleted, err)
	}
	if n := count(t, s, `SELECT COUNT(*) FROM sessions`); n != 0 {
		t.Fatalf("sessions = %d", n)
	}
	if _, err := s.RecordReauthFailure(ctx, tok("a")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("gone: err = %v", err)
	}
}

func TestMarkReauth(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	u := addUser(t, s, "alice")
	newSession(t, s, u.ID, "a")
	now := t0.Add(time.Hour)
	if err := s.MarkReauth(ctx, now, tok("a")); err != nil {
		t.Fatal(err)
	}
	got, err := s.SessionByHash(ctx, now, tok("a"))
	if err != nil || got.ReauthAt == nil || !got.ReauthAt.Equal(now) {
		t.Fatalf("session = %+v, %v", got, err)
	}
	if err := s.MarkReauth(ctx, now, tok("missing")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing: err = %v", err)
	}
	if err := s.MarkReauth(ctx, t0.Add(constants.SessionTTL), tok("a")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expired: err = %v", err)
	}
}
