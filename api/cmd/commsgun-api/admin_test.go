package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/davcs86/commsgun/api/internal/clock"
	"github.com/davcs86/commsgun/api/internal/model"
	"github.com/davcs86/commsgun/api/internal/password"
	"github.com/davcs86/commsgun/api/internal/store"
)

const (
	cliPassword  = "cli-password-unique-4410"
	cliPassword2 = "cli-password-unique-8823"
	cliUser      = "ops.admin"
	cliIssuer    = "https://issuer.example"
)

var cliT0 = time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)

type cliResult struct {
	code           int
	stdout, stderr string
}

func cliHasher() *password.Hasher {
	return password.NewHasher(password.Params{MemoryKiB: 64, Time: 1, Threads: 1}, 1)
}

func dataDirEnv(dir string) func(string) string {
	return func(k string) string {
		if k == "DATA_DIR" {
			return dir
		}
		return ""
	}
}

func cli(t *testing.T, dir, stdin string, args ...string) cliResult {
	t.Helper()
	var out, errb bytes.Buffer
	code := runAdmin(context.Background(), args, dataDirEnv(dir), strings.NewReader(stdin), &out, &errb,
		clock.NewFake(cliT0), cliHasher())
	return cliResult{code, out.String(), errb.String()}
}

func mustCLI(t *testing.T, dir, stdin string, args ...string) {
	t.Helper()
	if r := cli(t, dir, stdin, args...); r.code != 0 {
		t.Fatalf("%v: exit %d, stderr %q", args, r.code, r.stderr)
	}
}

func openStore(t *testing.T, dir string) *store.Store {
	t.Helper()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func cliSession(t *testing.T, st *store.Store, username, token string) [32]byte {
	t.Helper()
	u, _, err := st.UserByUsername(context.Background(), username)
	if err != nil {
		t.Fatal(err)
	}
	h := sha256.Sum256([]byte(token))
	if _, err := st.CreateSession(context.Background(), cliT0, u.ID, model.KindLocal, h); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SessionByHash(context.Background(), cliT0, h); err != nil {
		t.Fatalf("fresh session refused: %v", err)
	}
	return h
}

func sessionGone(t *testing.T, st *store.Store, h [32]byte) {
	t.Helper()
	if _, err := st.SessionByHash(context.Background(), cliT0, h); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("SessionByHash = %v, want ErrNotFound", err)
	}
}

func verifies(t *testing.T, st *store.Store, username, pw string) bool {
	t.Helper()
	_, hash, err := st.UserByUsername(context.Background(), username)
	if err != nil {
		t.Fatal(err)
	}
	ok, err := cliHasher().Verify(context.Background(), pw, hash)
	if err != nil {
		t.Fatal(err)
	}
	return ok
}

// empty store → 0, user role admin; again → 1 "users already exist"
func TestAdminCreate(t *testing.T) {
	dir := t.TempDir()
	mustCLI(t, dir, cliPassword+"\n", "create", cliUser)
	r := cli(t, dir, cliPassword+"\n", "create", "second")
	if r.code != 1 || !strings.Contains(r.stderr, "users already exist") {
		t.Fatalf("second create: exit %d, stderr %q; want 1 and %q", r.code, r.stderr, "users already exist")
	}
	st := openStore(t, dir)
	u, _, err := st.UserByUsername(context.Background(), cliUser)
	if err != nil || u.Role != "admin" || u.Disabled {
		t.Fatalf("user = %+v, %v; want an enabled admin", u, err)
	}
	if n, _ := st.CountUsers(context.Background()); n != 1 {
		t.Fatalf("users = %d, want 1", n)
	}
	if !verifies(t, st, cliUser, cliPassword) {
		t.Fatal("created password does not verify")
	}
}

// after create → 0; same name → 1
func TestAdminAdd(t *testing.T) {
	dir := t.TempDir()
	mustCLI(t, dir, cliPassword+"\n", "create", cliUser)
	mustCLI(t, dir, cliPassword2+"\n", "add", "second")
	if r := cli(t, dir, cliPassword2+"\n", "add", "second"); r.code != 1 {
		t.Fatalf("duplicate add: exit %d, want 1", r.code)
	}
	mustCLI(t, dir, "", "disable", "second")
	if r := cli(t, dir, cliPassword2+"\n", "add", "second"); r.code != 1 {
		t.Fatalf("add of a disabled name: exit %d, want 1", r.code)
	}
	st := openStore(t, dir)
	if n, _ := st.CountUsers(context.Background()); n != 2 {
		t.Fatalf("users = %d, want 2", n)
	}
	if !verifies(t, st, "second", cliPassword2) {
		t.Fatal("added password does not verify")
	}
}

// new hash verifies; the user's sessions are gone
func TestAdminResetPassword(t *testing.T) {
	dir := t.TempDir()
	mustCLI(t, dir, cliPassword+"\n", "create", cliUser)
	st := openStore(t, dir)
	h := cliSession(t, st, cliUser, "tok-reset")
	mustCLI(t, dir, cliPassword2+"\r\n", "reset-password", cliUser)
	sessionGone(t, st, h)
	if !verifies(t, st, cliUser, cliPassword2) || verifies(t, st, cliUser, cliPassword) {
		t.Fatal("reset: new password must verify and the old must not")
	}
	mustCLI(t, dir, cliPassword2+"\n\n", "reset-password", cliUser)
	if !verifies(t, st, cliUser, cliPassword2+"\n") {
		t.Fatal("only one trailing newline may be stripped")
	}
	if r := cli(t, dir, cliPassword2+"\n", "reset-password", "nobody"); r.code != 1 {
		t.Fatalf("reset of an unknown user: exit %d, want 1", r.code)
	}
}

// disable → sessions gone, SessionByHash refused; enable → login possible again
func TestAdminDisableEnable(t *testing.T) {
	dir := t.TempDir()
	mustCLI(t, dir, cliPassword+"\n", "create", cliUser)
	st := openStore(t, dir)
	h := cliSession(t, st, cliUser, "tok-disable")
	mustCLI(t, dir, "", "disable", cliUser)
	sessionGone(t, st, h)
	u, _, _ := st.UserByUsername(context.Background(), cliUser)
	if !u.Disabled {
		t.Fatal("user not disabled")
	}
	h2 := sha256.Sum256([]byte("tok-while-disabled"))
	if _, err := st.CreateSession(context.Background(), cliT0, u.ID, model.KindLocal, h2); err != nil {
		t.Fatal(err)
	}
	sessionGone(t, st, h2)
	mustCLI(t, dir, "", "enable", cliUser)
	u, _, _ = st.UserByUsername(context.Background(), cliUser)
	if u.Disabled || !verifies(t, st, cliUser, cliPassword) {
		t.Fatal("enabled user cannot log in")
	}
	cliSession(t, st, cliUser, "tok-after-enable")
	if r := cli(t, dir, "", "disable", "nobody"); r.code != 1 {
		t.Fatalf("disable of an unknown user: exit %d, want 1", r.code)
	}
}

// identities and sessions gone
func TestAdminUnlink(t *testing.T) {
	dir := t.TempDir()
	mustCLI(t, dir, cliPassword+"\n", "create", cliUser)
	st := openStore(t, dir)
	u, _, _ := st.UserByUsername(context.Background(), cliUser)
	if err := st.Link(context.Background(), cliT0, u.ID, cliIssuer, "sub-1"); err != nil {
		t.Fatal(err)
	}
	h := cliSession(t, st, cliUser, "tok-unlink")
	mustCLI(t, dir, "", "unlink", cliUser)
	sessionGone(t, st, h)
	if _, err := st.UserByIdentity(context.Background(), cliIssuer, "sub-1"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("UserByIdentity = %v, want ErrNotFound", err)
	}
	if r := cli(t, dir, "", "unlink", "nobody"); r.code != 1 {
		t.Fatalf("unlink of an unknown user: exit %d, want 1", r.code)
	}
}

// 15-char password → 1; bad username → 1; unknown verb, missing or extra arg → 2
func TestAdminRefusesBadInput(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		name, stdin string
		args        []string
		want        int
	}{
		{"short password", strings.Repeat("p", 15) + "\n", []string{"create", cliUser}, 1},
		{"empty password", "", []string{"create", cliUser}, 1},
		{"long password", strings.Repeat("p", 1025) + "\n", []string{"create", cliUser}, 1},
		{"bad username", cliPassword + "\n", []string{"create", "Admin"}, 1},
		{"short username", cliPassword + "\n", []string{"add", "ab"}, 1},
		{"bad username on disable", "", []string{"disable", "a b"}, 1},
		{"no verb", "", nil, 2},
		{"unknown verb", "", []string{"delete", cliUser}, 2},
		{"missing arg", cliPassword + "\n", []string{"create"}, 2},
		{"extra arg", cliPassword + "\n", []string{"create", cliUser, "x"}, 2},
		{"extra arg on enable", "", []string{"enable", cliUser, "x"}, 2},
	}
	for _, c := range cases {
		if r := cli(t, dir, c.stdin, c.args...); r.code != c.want {
			t.Errorf("%s: exit %d, want %d (stderr %q)", c.name, r.code, c.want, r.stderr)
		}
	}
	st := openStore(t, dir)
	if n, _ := st.CountUsers(context.Background()); n != 0 {
		t.Fatalf("users = %d after refusals, want 0", n)
	}
}

// getenv knows only DATA_DIR → 0
func TestAdminNeedsOnlyDataDir(t *testing.T) {
	dir := t.TempDir()
	var asked []string
	getenv := func(k string) string {
		asked = append(asked, k)
		if k == "DATA_DIR" {
			return dir
		}
		return ""
	}
	var out, errb bytes.Buffer
	code := runAdmin(context.Background(), []string{"create", cliUser}, getenv, strings.NewReader(cliPassword+"\n"),
		&out, &errb, clock.NewFake(cliT0), cliHasher())
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, errb.String())
	}
	for _, k := range asked {
		if k != "DATA_DIR" {
			t.Errorf("read env %q; only DATA_DIR is allowed", k)
		}
	}
	if n, _ := openStore(t, dir).CountUsers(context.Background()); n != 1 {
		t.Fatalf("users = %d, want 1", n)
	}
}

// run() serving on the same DATA_DIR; disable → 0 and the running admin API refuses that session
func TestAdminWhileServerRuns(t *testing.T) {
	p := start(t, nil)
	waitHealthy(t, p.adm)
	mustCLI(t, p.dir, cliPassword+"\n", "create", cliUser)
	st := openStore(t, p.dir)
	h := cliSession(t, st, cliUser, "tok-live")
	mustCLI(t, p.dir, "", "disable", cliUser)
	sessionGone(t, st, h)
	// Task 5/7 adds GET /admin/v1/session; until then the live server must stay healthy beside the CLI.
	if code, _ := do(t, http.MethodGet, "http://"+p.adm+"/healthz", nil, ""); code != http.StatusOK {
		t.Fatalf("admin healthz = %d after the CLI, want 200", code)
	}
	if strings.Contains(p.out.String(), `"level":"ERROR"`) {
		t.Fatalf("server logged an error beside the CLI: %s", p.out.String())
	}
}

func TestAdminNeverPrintsPassword(t *testing.T) {
	dir := t.TempDir()
	runs := []struct {
		stdin string
		args  []string
	}{
		{cliPassword + "\n", []string{"create", cliUser}},
		{cliPassword + "\n", []string{"create", "second"}},
		{cliPassword2 + "\n", []string{"add", "second"}},
		{cliPassword2 + "\n", []string{"add", "second"}},
		{cliPassword + "\r\n", []string{"reset-password", cliUser}},
		{cliPassword + "\n", []string{"reset-password", "nobody"}},
		{cliPassword + "\n", []string{"reset-password"}},
		{cliPassword[:15] + "\n", []string{"add", "third"}},
		{"", []string{"disable", cliUser}},
		{"", []string{"enable", cliUser}},
		{"", []string{"unlink", cliUser}},
	}
	for _, r := range runs {
		res := cli(t, dir, r.stdin, r.args...)
		for _, pw := range []string{cliPassword, cliPassword2, cliPassword[:15]} {
			if strings.Contains(res.stdout+res.stderr, pw) {
				t.Errorf("%v printed the password: stdout %q stderr %q", r.args, res.stdout, res.stderr)
			}
		}
	}
}
