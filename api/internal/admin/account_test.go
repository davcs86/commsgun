package admin

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/davcs86/commsgun/api/internal/clock"
	"github.com/davcs86/commsgun/api/internal/config"
	"github.com/davcs86/commsgun/api/internal/constants"
	"github.com/davcs86/commsgun/api/internal/store"
)

const (
	acctUser  = "ops.admin"
	acctUser2 = "second.admin"
	acctPass  = "account-password-unique-3307"
	acctIss   = "https://issuer-unique.example"
	acctSub   = "subject-unique-6612"

	pathAuth     = "/admin/v1/auth"
	pathLocal    = "/admin/v1/sessions/local"
	pathOIDC     = "/admin/v1/sessions/oidc"
	pathImport   = "/admin/v1/bootstrap/import"
	pathSession  = "/admin/v1/session"
	pathReauth   = "/admin/v1/sessions/reauth"
	pathIdentity = "/admin/v1/identities"

	wantCreds   = `{"error":"invalid_credentials"}`
	wantSession = `{"error":"session"}`
	wantMode    = `{"error":"mode"}`
)

var tokenPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)

// fakeHasher stores "fake$"+pw and counts every call; with ended set, every call answers as if its ctx ended.
type fakeHasher struct {
	hash, verify, dummy atomic.Int32
	ended               atomic.Bool
}

func (f *fakeHasher) err(ctx context.Context) error {
	if f.ended.Load() {
		return context.DeadlineExceeded
	}
	return ctx.Err()
}

func (f *fakeHasher) Hash(ctx context.Context, pw string) (string, error) {
	f.hash.Add(1)
	if err := f.err(ctx); err != nil {
		return "", err
	}
	return "fake$" + pw, nil
}

func (f *fakeHasher) Verify(ctx context.Context, pw, phc string) (bool, error) {
	f.verify.Add(1)
	if err := f.err(ctx); err != nil {
		return false, err
	}
	return phc == "fake$"+pw, nil
}

func (f *fakeHasher) VerifyDummy(ctx context.Context, _ string) error {
	f.dummy.Add(1)
	return f.err(ctx)
}

func (f *fakeHasher) calls() [3]int32 {
	return [3]int32{f.hash.Load(), f.verify.Load(), f.dummy.Load()}
}

type acctEnv struct {
	h   http.Handler
	st  *store.Store
	cfg *config.Config
	clk *clock.Fake
	hs  *fakeHasher
	log *bytes.Buffer
}

func accountSetup(t *testing.T, mode config.AuthMode) acctEnv {
	t.Helper()
	st, err := store.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	cfg := &config.Config{AdminAPIToken: token, AuthMode: mode}
	e := acctEnv{st: st, cfg: cfg, clk: clock.NewFake(now), hs: &fakeHasher{}, log: &bytes.Buffer{}}
	e.h = NewHandler(st, cfg, e.clk, slog.New(slog.NewJSONHandler(e.log, nil)), e.hs)
	return e
}

func (e acctEnv) request(ctx context.Context, method, path, session, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequestWithContext(ctx, method, path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	if session != "" {
		req.Header.Set(HeaderSession, session)
	}
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	return rec
}

func (e acctEnv) call(method, path, session, body string) *httptest.ResponseRecorder {
	return e.request(context.Background(), method, path, session, body)
}

func jsonBody(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func creds(t *testing.T, username, pw string) string {
	return jsonBody(t, map[string]string{"username": username, "password": pw})
}

func identity(t *testing.T, iss, sub string) string {
	return jsonBody(t, map[string]string{"iss": iss, "sub": sub})
}

func reauthBody(t *testing.T, pw string) string {
	return jsonBody(t, map[string]string{"password": pw})
}

func expect(t *testing.T, what string, rec *httptest.ResponseRecorder, code int, body string) {
	t.Helper()
	if rec.Code != code || strings.TrimSpace(rec.Body.String()) != body {
		t.Errorf("%s: %d %q, want %d %q", what, rec.Code, rec.Body.String(), code, body)
	}
}

func addUser(t *testing.T, e acctEnv, username string) store.User {
	t.Helper()
	u, err := e.st.AddUser(context.Background(), now, username, "fake$"+acctPass)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func login(t *testing.T, e acctEnv, username string) string {
	t.Helper()
	rec := e.call(http.MethodPost, pathLocal, "", creds(t, username, acctPass))
	if rec.Code != http.StatusCreated {
		t.Fatalf("login %s: %d %s", username, rec.Code, rec.Body)
	}
	return sessionToken(t, rec)
}

func loginOIDC(t *testing.T, e acctEnv, iss, sub string) string {
	t.Helper()
	rec := e.call(http.MethodPost, pathOIDC, "", identity(t, iss, sub))
	if rec.Code != http.StatusCreated {
		t.Fatalf("oidc login: %d %s", rec.Code, rec.Body)
	}
	return sessionToken(t, rec)
}

func sessionToken(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var s struct{ Token string }
	if err := json.Unmarshal(rec.Body.Bytes(), &s); err != nil {
		t.Fatal(err)
	}
	return s.Token
}

func link(t *testing.T, e acctEnv, u store.User, iss, sub string) {
	t.Helper()
	if err := e.st.Link(context.Background(), now, u.ID, iss, sub); err != nil {
		t.Fatal(err)
	}
}

func keys(t *testing.T, raw []byte) (map[string]json.RawMessage, string) {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("%s: %v", raw, err)
	}
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return m, strings.Join(ks, ",")
}

// checkSession asserts the 201 Session shape for user u and returns the token.
func checkSession(t *testing.T, rec *httptest.ResponseRecorder, u store.User, at time.Time) string {
	t.Helper()
	if rec.Code != http.StatusCreated {
		t.Fatalf("session: %d %s", rec.Code, rec.Body)
	}
	m, ks := keys(t, rec.Body.Bytes())
	if ks != "expires_at,token,user" {
		t.Fatalf("session keys = %s", ks)
	}
	checkUser(t, m["user"], u)
	var tok string
	var exp time.Time
	if json.Unmarshal(m["token"], &tok) != nil || !tokenPattern.MatchString(tok) {
		t.Errorf("token %s does not match %s", m["token"], tokenPattern)
	}
	if json.Unmarshal(m["expires_at"], &exp) != nil || !exp.Equal(at.Add(constants.SessionTTL)) || exp.Location() != time.UTC {
		t.Errorf("expires_at = %s, want %s", m["expires_at"], at.Add(constants.SessionTTL))
	}
	return tok
}

func checkUser(t *testing.T, raw json.RawMessage, u store.User) {
	t.Helper()
	m, ks := keys(t, raw)
	if ks != "id,role,username" {
		t.Fatalf("user keys = %s", ks)
	}
	var id, name, role string
	_ = json.Unmarshal(m["id"], &id)
	_ = json.Unmarshal(m["username"], &name)
	_ = json.Unmarshal(m["role"], &role)
	if id != u.ID || name != u.Username || role != "admin" {
		t.Errorf("user = %s, want %+v", raw, u)
	}
}

// each mode → 200 {"mode":…}; no session needed; wrong bearer → 401 unauthorized
func TestAuthRoute(t *testing.T) {
	for _, m := range []config.AuthMode{config.AuthLocal, config.AuthOIDC, config.AuthBoth} {
		e := accountSetup(t, m)
		expect(t, string(m), e.call(http.MethodGet, pathAuth, "", ""), 200, `{"mode":"`+string(m)+`"}`)
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, pathAuth, nil)
		req.Header.Set("Authorization", "Bearer wrong")
		e.h.ServeHTTP(rec, req)
		expect(t, "wrong bearer", rec, 401, `{"error":"unauthorized"}`)
	}
}

func TestSessionsLocal(t *testing.T) {
	e := accountSetup(t, config.AuthLocal)
	u := addUser(t, e, acctUser)
	tok := checkSession(t, e.call(http.MethodPost, pathLocal, "", creds(t, acctUser, acctPass)), u, now)
	if _, err := e.st.SessionByHash(context.Background(), now, sha256.Sum256([]byte(tok))); err != nil {
		t.Fatalf("stored session: %v", err)
	}

	gone := addUser(t, e, "gone.user")
	if err := e.st.SetDisabled(context.Background(), now, gone.Username, true); err != nil {
		t.Fatal(err)
	}
	var bodies []string
	for _, c := range []struct {
		name, user, pw string
		dummy          bool
	}{
		{"wrong password", acctUser, acctPass + "x", false}, {"unknown user", "nobody", acctPass, true},
		{"disabled user", gone.Username, acctPass, false},
	} {
		before := e.hs.calls()
		rec := e.call(http.MethodPost, pathLocal, "", creds(t, c.user, c.pw))
		expect(t, c.name, rec, 401, wantCreds)
		bodies = append(bodies, rec.Body.String())
		want := [3]int32{before[0], before[1] + 1, before[2]}
		if c.dummy {
			want = [3]int32{before[0], before[1], before[2] + 1}
		}
		if got := e.hs.calls(); got != want {
			t.Errorf("%s: hash, verify, dummy = %v, want %v", c.name, got, want)
		}
	}
	if bodies[0] != bodies[1] || bodies[1] != bodies[2] {
		t.Errorf("refusals differ: %q", bodies)
	}

	e.hs.ended.Store(true)
	for _, user := range []string{acctUser, "nobody"} {
		rec := e.call(http.MethodPost, pathLocal, "", creds(t, user, acctPass))
		expect(t, "hash context ended", rec, 503, `{"error":"unavailable"}`)
		if rec.Header().Get("Retry-After") == "" {
			t.Error("503 without Retry-After")
		}
	}
	e.hs.ended.Store(false)

	e.cfg.AuthMode = config.AuthOIDC
	before := e.hs.calls()
	expect(t, "mode oidc", e.call(http.MethodPost, pathLocal, "", creds(t, acctUser, acctPass)), 403, wantMode)
	if e.hs.calls() != before {
		t.Errorf("mode refusal ran the hasher: %v → %v", before, e.hs.calls())
	}
}

// VerifyDummy 1, Verify 0
func TestUnknownUsernameUsesDummyHash(t *testing.T) {
	e := accountSetup(t, config.AuthLocal)
	addUser(t, e, acctUser)
	expect(t, "unknown", e.call(http.MethodPost, pathLocal, "", creds(t, "nobody", acctPass)), 401, wantCreds)
	if c := e.hs.calls(); c != [3]int32{0, 0, 1} {
		t.Errorf("hash, verify, dummy = %v, want [0 0 1]", c)
	}
}

func TestSessionsOIDC(t *testing.T) {
	e := accountSetup(t, config.AuthOIDC)
	u := addUser(t, e, acctUser)
	link(t, e, u, acctIss, acctSub)
	checkSession(t, e.call(http.MethodPost, pathOIDC, "", identity(t, acctIss, acctSub)), u, now)

	gone := addUser(t, e, "gone.user")
	link(t, e, gone, acctIss, "gone-sub")
	if err := e.st.SetDisabled(context.Background(), now, gone.Username, true); err != nil {
		t.Fatal(err)
	}
	expect(t, "unlinked", e.call(http.MethodPost, pathOIDC, "", identity(t, acctIss, "other")), 403, `{"error":"not_linked"}`)
	expect(t, "disabled", e.call(http.MethodPost, pathOIDC, "", identity(t, acctIss, "gone-sub")), 403, `{"error":"not_linked"}`)
	expect(t, "sub 255 bytes", e.call(http.MethodPost, pathOIDC, "", identity(t, acctIss, strings.Repeat("s", 255))), 403, `{"error":"not_linked"}`)

	for _, c := range []struct{ iss, sub, field string }{
		{"http://x", acctSub, "iss"}, {"", acctSub, "iss"}, {"not a url", acctSub, "iss"}, {"https://", acctSub, "iss"},
		{acctIss, "", "sub"}, {acctIss, strings.Repeat("s", 256), "sub"},
	} {
		expect(t, c.iss+"/"+c.sub, e.call(http.MethodPost, pathOIDC, "", identity(t, c.iss, c.sub)),
			422, `{"error":"invalid_identity","field":"`+c.field+`"}`)
	}

	e.cfg.AuthMode = config.AuthLocal
	expect(t, "mode local", e.call(http.MethodPost, pathOIDC, "", identity(t, acctIss, acctSub)), 403, wantMode)
	if c := e.hs.calls(); c != [3]int32{} {
		t.Errorf("sessions/oidc ran the hasher: %v", c)
	}
}

func TestBootstrapImport(t *testing.T) {
	bad := []string{strings.Repeat("p", 15), strings.Repeat("😀", 15), strings.Repeat("p", 1025)}
	wantBad := `{"error":"invalid_password","field":"password"}`

	e := accountSetup(t, config.AuthLocal)
	for _, pw := range bad {
		expect(t, "empty store, bad password", e.call(http.MethodPost, pathImport, "", creds(t, "admin", pw)), 422, wantBad)
	}
	expect(t, "bad username", e.call(http.MethodPost, pathImport, "", creds(t, "Admin", acctPass)), 422,
		`{"error":"invalid_username","field":"username"}`)
	e.hs.ended.Store(true)
	expect(t, "hash context ended", e.call(http.MethodPost, pathImport, "", creds(t, "admin", acctPass)), 503, `{"error":"unavailable"}`)
	e.hs.ended.Store(false)
	expect(t, "first", e.call(http.MethodPost, pathImport, "", creds(t, "admin", acctPass)), 200, `{"result":"imported"}`)
	if c := e.hs.calls(); c[0] != 2 {
		t.Fatalf("Hash calls = %d, want 2 (one ended, one imported)", c[0])
	}
	u, hash, err := e.st.UserByUsername(context.Background(), "admin")
	if err != nil || hash != "fake$"+acctPass || u.Role != "admin" || u.Disabled {
		t.Fatalf("imported user = %+v %q %v", u, hash, err)
	}
	expect(t, "again", e.call(http.MethodPost, pathImport, "", creds(t, "admin", acctPass)), 200, `{"result":"skipped"}`)
	for _, pw := range bad {
		expect(t, "users exist, bad password", e.call(http.MethodPost, pathImport, "", creds(t, "admin", pw)), 422, wantBad)
	}
	if c := e.hs.calls(); c[0] != 2 {
		t.Errorf("Hash calls = %d after skipped and refusals, want still 2", c[0])
	}
	if n, _ := e.st.CountUsers(context.Background()); n != 1 {
		t.Errorf("users = %d, want 1", n)
	}
}

// 8 concurrent → one imported, seven skipped, one row
func TestBootstrapImportRace(t *testing.T) {
	e := accountSetup(t, config.AuthLocal)
	var wg sync.WaitGroup
	results := make(chan string, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rec := e.call(http.MethodPost, pathImport, "", creds(t, "admin", acctPass))
			results <- strings.TrimSpace(rec.Body.String())
		}()
	}
	wg.Wait()
	close(results)
	got := map[string]int{}
	for r := range results {
		got[r]++
	}
	if got[`{"result":"imported"}`] != 1 || got[`{"result":"skipped"}`] != 7 {
		t.Errorf("results = %v", got)
	}
	if n, _ := e.st.CountUsers(context.Background()); n != 1 {
		t.Errorf("users = %d, want 1", n)
	}
}

func TestSessionGetDelete(t *testing.T) {
	e := accountSetup(t, config.AuthLocal)
	u := addUser(t, e, acctUser)
	tok := login(t, e, acctUser)
	rec := e.call(http.MethodGet, pathSession, tok, "")
	if rec.Code != 200 {
		t.Fatalf("GET session: %d %s", rec.Code, rec.Body)
	}
	m, ks := keys(t, rec.Body.Bytes())
	if ks != "expires_at,kind,user" || string(m["kind"]) != `"local"` {
		t.Fatalf("GET session = %s", rec.Body)
	}
	checkUser(t, m["user"], u)
	var exp time.Time
	if json.Unmarshal(m["expires_at"], &exp) != nil || !exp.Equal(now.Add(constants.SessionTTL)) {
		t.Errorf("expires_at = %s", m["expires_at"])
	}

	expect(t, "missing", e.call(http.MethodGet, pathSession, "", ""), 401, wantSession)
	expect(t, "unknown", e.call(http.MethodGet, pathSession, tok+"x", ""), 401, wantSession)
	expect(t, "query", e.call(http.MethodGet, pathSession+"?commsgun-session="+tok, "", ""), 401, wantSession)
	e.clk.Advance(constants.SessionTTL)
	expect(t, "expired", e.call(http.MethodGet, pathSession, tok, ""), 401, wantSession)
	e.clk.Set(now)

	expect(t, "DELETE valid", e.call(http.MethodDelete, pathSession, tok, ""), 204, "")
	expect(t, "GET after DELETE", e.call(http.MethodGet, pathSession, tok, ""), 401, wantSession)
	expect(t, "DELETE missing", e.call(http.MethodDelete, pathSession, "", ""), 204, "")
	expect(t, "DELETE unknown", e.call(http.MethodDelete, pathSession, "unknown", ""), 204, "")
}

// local session + mode oidc → 401 session and row deleted; oidc session + mode local → same
func TestModeSwitchEndsRefusedSessions(t *testing.T) {
	e := accountSetup(t, config.AuthBoth)
	u := addUser(t, e, acctUser)
	link(t, e, u, acctIss, acctSub)
	local, oidc := login(t, e, acctUser), loginOIDC(t, e, acctIss, acctSub)

	e.cfg.AuthMode = config.AuthOIDC
	expect(t, "local in oidc", e.call(http.MethodGet, pathSession, local, ""), 401, wantSession)
	if rec := e.call(http.MethodGet, pathSession, oidc, ""); rec.Code != 200 {
		t.Errorf("oidc in oidc: %d %s", rec.Code, rec.Body)
	}
	e.cfg.AuthMode = config.AuthLocal
	expect(t, "oidc in local", e.call(http.MethodGet, pathSession, oidc, ""), 401, wantSession)

	e.cfg.AuthMode = config.AuthBoth
	for name, tok := range map[string]string{"local": local, "oidc": oidc} {
		expect(t, name+" after switching back", e.call(http.MethodGet, pathSession, tok, ""), 401, wantSession)
		if _, err := e.st.SessionByHash(context.Background(), now, sha256.Sum256([]byte(tok))); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("%s row: %v, want deleted", name, err)
		}
	}
}

func TestReauth(t *testing.T) {
	e := accountSetup(t, config.AuthBoth)
	u := addUser(t, e, acctUser)
	link(t, e, u, acctIss, acctSub)
	tok := login(t, e, acctUser)

	e.clk.Advance(time.Minute)
	expect(t, "right", e.call(http.MethodPost, pathReauth, tok, reauthBody(t, acctPass)), 204, "")
	ses, err := e.st.SessionByHash(context.Background(), e.clk.Now(), sha256.Sum256([]byte(tok)))
	if err != nil || ses.ReauthAt == nil || !ses.ReauthAt.Equal(e.clk.Now()) {
		t.Fatalf("reauth_at = %v, %v; want %s", ses.ReauthAt, err, e.clk.Now())
	}
	for i := 1; i < constants.ReauthMaxFailures; i++ {
		expect(t, "wrong", e.call(http.MethodPost, pathReauth, tok, reauthBody(t, "wrong-password-0000")), 401, wantCreds)
	}
	if rec := e.call(http.MethodGet, pathSession, tok, ""); rec.Code != 200 {
		t.Fatalf("session after %d failures: %d", constants.ReauthMaxFailures-1, rec.Code)
	}
	expect(t, "5th wrong", e.call(http.MethodPost, pathReauth, tok, reauthBody(t, "wrong-password-0000")), 401, wantCreds)
	expect(t, "session gone", e.call(http.MethodGet, pathSession, tok, ""), 401, wantSession)
	expect(t, "reauth on gone session", e.call(http.MethodPost, pathReauth, tok, reauthBody(t, acctPass)), 401, wantSession)
	expect(t, "no session", e.call(http.MethodPost, pathReauth, "", reauthBody(t, acctPass)), 401, wantSession)

	oidc := loginOIDC(t, e, acctIss, acctSub)
	expect(t, "oidc session in both", e.call(http.MethodPost, pathReauth, oidc, reauthBody(t, acctPass)), 403, `{"error":"session_kind"}`)
	e.cfg.AuthMode = config.AuthOIDC
	expect(t, "mode oidc", e.call(http.MethodPost, pathReauth, oidc, `{`), 403, wantMode)
	e.cfg.AuthMode = config.AuthLocal
	local := login(t, e, acctUser)
	before := e.hs.calls()
	expect(t, "mode local", e.call(http.MethodPost, pathReauth, local, reauthBody(t, acctPass)), 403, wantMode)
	if e.hs.calls() != before {
		t.Errorf("mode refusal ran the hasher")
	}
}

func TestIdentities(t *testing.T) {
	e := accountSetup(t, config.AuthBoth)
	a := addUser(t, e, acctUser)
	addUser(t, e, acctUser2)
	tok := login(t, e, acctUser)
	body := identity(t, acctIss, acctSub)
	reauth := func(tok string) {
		t.Helper()
		expect(t, "reauth", e.call(http.MethodPost, pathReauth, tok, reauthBody(t, acctPass)), 204, "")
	}

	expect(t, "no reauth", e.call(http.MethodPost, pathIdentity, tok, body), 403, `{"error":"reauth_required"}`)
	reauth(tok)
	e.clk.Advance(constants.ReauthWindow + time.Second)
	expect(t, "reauth+5m+1s", e.call(http.MethodPost, pathIdentity, tok, body), 403, `{"error":"reauth_required"}`)
	reauth(tok)
	expect(t, "invalid iss", e.call(http.MethodPost, pathIdentity, tok, identity(t, "http://x", acctSub)), 422,
		`{"error":"invalid_identity","field":"iss"}`)
	e.clk.Advance(constants.ReauthWindow)
	expect(t, "reauth+5m", e.call(http.MethodPost, pathIdentity, tok, body), 204, "")

	reauth(tok)
	expect(t, "issuer already linked", e.call(http.MethodPost, pathIdentity, tok, identity(t, acctIss, "other-sub")), 409, `{"error":"already_linked"}`)
	tok2 := login(t, e, acctUser2)
	reauth(tok2)
	expect(t, "pair taken", e.call(http.MethodPost, pathIdentity, tok2, body), 409, `{"error":"already_linked"}`)

	oidc := loginOIDC(t, e, acctIss, acctSub)
	expect(t, "oidc session", e.call(http.MethodPost, pathIdentity, oidc, body), 403, `{"error":"session_kind"}`)
	expect(t, "no session", e.call(http.MethodPost, pathIdentity, "", body), 401, wantSession)
	e.cfg.AuthMode = config.AuthLocal
	expect(t, "mode local", e.call(http.MethodPost, pathIdentity, tok2, body), 403, wantMode)

	e.cfg.AuthMode = config.AuthOIDC
	checkSession(t, e.call(http.MethodPost, pathOIDC, "", body), a, e.clk.Now())
}

func TestAccountBodiesStrict(t *testing.T) {
	e := accountSetup(t, config.AuthBoth)
	addUser(t, e, acctUser)
	tok := login(t, e, acctUser)
	for _, r := range []struct{ path, session, field string }{
		{pathLocal, "", "username"}, {pathOIDC, "", "iss"}, {pathImport, "", "username"},
		{pathReauth, tok, "password"}, {pathIdentity, tok, "iss"},
	} {
		expect(t, r.path+" malformed", e.call(http.MethodPost, r.path, r.session, `{`), 422, `{"error":"invalid_json","field":""}`)
		expect(t, r.path+" trailing", e.call(http.MethodPost, r.path, r.session, `{}{}`), 422, `{"error":"invalid_json","field":""}`)
		expect(t, r.path+" wrong type", e.call(http.MethodPost, r.path, r.session, `{"`+r.field+`":1}`), 422,
			`{"error":"invalid_json","field":"`+r.field+`"}`)
		expect(t, r.path+" unknown", e.call(http.MethodPost, r.path, r.session, `{"`+r.field+`":"a","x":1}`), 422,
			`{"error":"unknown_field","field":"x"}`)
		pad := func(n int) string { return "{" + strings.Repeat(" ", n-2) + "}" }
		expect(t, r.path+" 4097 bytes", e.call(http.MethodPost, r.path, r.session, pad(constants.AccountMaxBodyBytes+1)), 413, "")
		if rec := e.call(http.MethodPost, r.path, r.session, pad(constants.AccountMaxBodyBytes)); rec.Code == 413 || rec.Code == 422 && strings.Contains(rec.Body.String(), "invalid_json") {
			t.Errorf("%s 4096 bytes: %d %s", r.path, rec.Code, rec.Body)
		}
	}
	if constants.AccountMaxBodyBytes != 4096 {
		t.Errorf("AccountMaxBodyBytes = %d, want ProbeMaxBodyBytes (4096)", constants.AccountMaxBodyBytes)
	}
}

// wrong method on each route → 405 with Allow
func TestAccountMethods(t *testing.T) {
	e := accountSetup(t, config.AuthBoth)
	for path, allow := range map[string]string{
		pathAuth: "GET", pathLocal: "POST", pathOIDC: "POST", pathImport: "POST",
		pathSession: "GET, DELETE", pathReauth: "POST", pathIdentity: "POST",
	} {
		for _, m := range []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch} {
			if strings.Contains(allow, m) {
				continue
			}
			rec := e.call(m, path, "", "")
			if rec.Code != 405 || rec.Header().Get("Allow") != allow ||
				strings.TrimSpace(rec.Body.String()) != `{"error":"method_not_allowed"}` {
				t.Errorf("%s %s = %d allow=%q %s", m, path, rec.Code, rec.Header().Get("Allow"), rec.Body)
			}
		}
	}
}

// no password, token, username, iss or sub in the captured log
func TestAccountLogsCarryNoSecrets(t *testing.T) {
	e := accountSetup(t, config.AuthBoth)
	expect(t, "import", e.call(http.MethodPost, pathImport, "", creds(t, acctUser, acctPass)), 200, `{"result":"imported"}`)
	tok := login(t, e, acctUser)
	e.call(http.MethodPost, pathLocal, "", creds(t, acctUser, acctPass+"x"))
	e.call(http.MethodPost, pathLocal, "", creds(t, "nobody.user", acctPass))
	e.call(http.MethodPost, pathReauth, tok, reauthBody(t, acctPass+"x"))
	e.call(http.MethodPost, pathReauth, tok, reauthBody(t, acctPass))
	e.call(http.MethodPost, pathIdentity, tok, identity(t, acctIss, acctSub))
	oidc := loginOIDC(t, e, acctIss, acctSub)
	e.call(http.MethodPost, pathOIDC, "", identity(t, acctIss, "unlinked-sub-unique"))
	e.call(http.MethodGet, pathSession, oidc, "")
	e.call(http.MethodDelete, pathSession, oidc, "")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	e.request(ctx, http.MethodPost, pathLocal, "", creds(t, acctUser, acctPass))
	e.st.Close()
	for _, path := range []string{pathLocal, pathImport} {
		if rec := e.call(http.MethodPost, path, "", creds(t, acctUser, acctPass)); rec.Code != 500 {
			t.Errorf("%s on a closed store: %d %s", path, rec.Code, rec.Body)
		}
	}
	e.call(http.MethodPost, pathOIDC, "", identity(t, acctIss, acctSub))
	e.call(http.MethodGet, pathSession, tok, "")

	logged := e.log.String()
	if !strings.Contains(logged, `"level":"ERROR"`) {
		t.Fatalf("expected the closed store to be logged: %s", logged)
	}
	for name, s := range map[string]string{
		"password": acctPass, "token": tok, "oidc token": oidc, "username": acctUser, "unknown username": "nobody.user",
		"iss": "issuer-unique", "sub": acctSub, "unlinked sub": "unlinked-sub-unique",
	} {
		if strings.Contains(logged, s) {
			t.Errorf("log contains the %s: %s", name, logged)
		}
	}
}
