# commsgun SSO, Accounts and SMTP Submission Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give commsgun stored users, revocable server-side sessions, a provider-neutral OIDC login with explicit linking, a
break-glass CLI, and an SMTP submission listener for Authelia, while every image keeps today's behaviour until the edge
sets the new variables.

**Architecture:** The API becomes the authority: it stores users, identities and sessions in new SQLite tables (via a
forward-only migration runner), hashes passwords with argon2id one at a time, enforces `AUTH_MODE`, and requires a
`Commsgun-Session` header on every admin route but four. The UI is the OIDC relying party (`openid-client`), keeps only an
opaque session token in `commsgun_session`, and imports today's `ADMIN_PASSWORD` once at start. A third listener (go-smtp,
STARTTLS, `AUTH PLAIN`) turns each `DATA` into one enqueue request through `SubmitWindowed`.

**Tech Stack:** Go 1.27 (`net/http`, `log/slog`, `modernc.org/sqlite`), new direct deps `golang.org/x/crypto/argon2`,
`github.com/emersion/go-smtp`, `github.com/emersion/go-sasl`; SvelteKit 3.1.1 / adapter-node 6.0.1 (both exact, CG-04),
`openid-client` 6 (exact-pinned devDependency), Vitest, Playwright; bash `scripts/smoke.sh`.

**Spec:** `docs/superpowers/specs/2026-11-13-commsgun-design.md` §§ 12–13, and the 2026-11-24 rows K1, K2, K3, K7 of
`2026-11-13-commsgun-api.md` and U1, U2, U4 of `2026-11-13-commsgun-ui-release.md`. Cross-repo source of truth:
`davcs86/homelab-edge` `docs/superpowers/specs/2026-11-23-authelia-sso-respec.md` § 3, § 4.3, § 4.6 (`commsgun` client),
§ 5 (commsgun row), § 9, § 10 P5, and `docs/runbooks/sso.md`. Read the spec sections first.

**Out of scope:** everything in `homelab-edge` (compose, networks and fixed addresses, Caddy zones and log redaction,
secrets, the `commsgun-admin` service, Gatus probes). That is a separate plan.

**Open questions:** listed at the end. A step that cites `Q<n>` waits for the owner's answer; the value in that question
is the plan's proposal, not a decision.

## Global Constraints

- Upgrade safety: an image built at any task boundary keeps today's behaviour with today's edge env: `AUTH_MODE` unset =
  `local`; `ENQUEUE_ADDR` `:9200`, `ADMIN_ADDR` `:9201`; `SMTP_ADDR` unset = no SMTP listener; OIDC unset = no SSO.
- `AUTH_MODE` is read by the API only: `local` (default) | `oidc` | `both`; any other value refuses to start.
- Users: `username` 3–32 of `a-z0-9._-`, unique, permanent, never reused; `role` `admin` only. Passwords 16 code points
  to 1024 bytes (the UI counts `[...v].length`), argon2id m = 19456 KiB, t = 2, p = 1, 16-byte salt, 32-byte key, stored
  as a PHC string; at most one hash at a time.
- Session token: 32 random bytes, base64url unpadded (43 chars), returned only at creation, stored as its SHA-256, 12 h
  fixed lifetime, sent only in the `Commsgun-Session` header, never in a query string or a log.
- Cookies: `commsgun_session` `HttpOnly; Secure; SameSite=Strict; Path=/`, `Max-Age` from the session's `expires_at`;
  `commsgun_oidc` `HttpOnly; Secure; SameSite=Lax; Path=/auth`, 10 minutes, sealed with a key derived from
  `SESSION_SECRET` alone.
- Re-auth: `ReauthWindow` 5 min, `ReauthMaxFailures` 5 per session. Identity: `iss` an `https` URL, `sub` 1–255 bytes;
  account bodies at most `AccountMaxBodyBytes` 4096 (no separate `iss` limit), over it an empty 413.
- Link (spec 12.5, owner 2026-11-27): every link sends `prompt=login` and `max_age=1`; the callback refuses an ID token
  without `auth_time` or with `auth_time` before the flow's start minus `AUTH_TIME_SKEW_MS` (30 s).
- SMTP: container port 9203 on `SMTP_ADDR`; STARTTLS, TLS 1.2+; `AUTH PLAIN` only, as an app with `smtp_submission: true`;
  8 concurrent sessions, 60 s read timeout, `WriteTimeout` 30 s, 10 messages per connection; `DATA` ≤ `MaxBodyBytes`;
  `send_window_seconds` default and maximum 86400.
- Limits and durations live only in `api/internal/constants/constants.go` (Rule 2); UI limits in
  `ui/src/lib/server/constants.ts` / `ui/src/lib/constants.ts`; a mirrored limit changes with its twins (CG-02).
- Account JSON changes in all three copies in the same task (CG-01): Go producers, `ui/src/lib/types.ts`,
  `ui/e2e/stub-api.ts`. Request bodies decode strictly as the probe's (API-11).
- A new UI env var goes into `ui/src/env.ts#variables` and `ui/src/lib/server/env.ts#loadEnv` (CG-08); a new
  `COMMSGUN_CONFIG` key is a coordinated edge change (report it, do not touch the edge).
- Logs: JSON lines of ids and enumerated facts; never a password, session token, username, `iss`, `sub`, address,
  subject, `Message-ID` or body (Rule 4; spec 12.8, 12.10). Secrets compared in constant time (Rule 5).
- Every UI package is a devDependency; `openid-client` is added with `-D -E` (exact). New Go deps are only the three
  above, each at the newest release at least two weeks old (spec § 11), pinned by `go.mod`/`go.sum`.
- New UI requests in tests and smoke carry the proxy-header quartet (CG-03); a new JSON mutation endpoint copies the
  probe's `Origin` check (UI-01). Store writes use the single-writer shape and `translate` (API-03); state changes are
  guarded (API-04); store methods take `now` (API-05).
- Comments state constraints only, ≤ 2 lines. No test is skipped, disabled or weakened. Commit after every task with
  the trailer lines from the session's attribution reminder.

## Review Focus

1. **The admin API is down or slow while the hook validates a session** → the page answers 503 with "The delivery API
   is not responding", the session cookie is kept, there is no redirect to `/login`, and `/healthz` still answers 200.
   (Task 7)
2. **The issuer is unreachable while the mode is `both`** → `/login` renders and the password login works; "Sign in
   with SSO" returns to `/login` with "SSO is unavailable. Try again later.", never a 500. (Task 8)
3. **A hostile `next` carried through the SSO round trip** (`//evil.example`, `/\evil`, `/login`, 3 000 characters) →
   the browser lands on `/`; nothing from `next` reaches the IdP URL except inside the sealed cookie. (Task 8)
4. **A junk `commsgun_session` cookie** (empty, 42 or 44 chars, non-base64url, 10 KB) → treated as signed out without
   calling the API, and the cookie is deleted. (Task 7)
5. **Authelia resends a message after a lost reply** (same `Message-ID`, header in another case, extra whitespace,
   minutes later) → `250 2.0.0` and no second message. (Task 10)

---

## File Structure

```
api/
  go.mod, go.sum                               + x/crypto, go-smtp, go-sasl
  internal/constants/constants.go              + account, hashing, session and SMTP limits
  internal/store/store.go                      Open calls migrate instead of initSchema
  internal/store/migrate.go                    NEW forward-only runner over embedded migrations
  internal/store/migrations/0002_accounts.sql  NEW users, identities, sessions
  internal/store/accounts.go                   NEW users, identities, sessions
  internal/password/password.go                NEW argon2id PHC, Hasher (slot, dummy hash)
  internal/config/config.go                    + AuthMode, listener addresses, SMTP TLS paths, smtp_submission,
                                                 send_window_seconds, DataDir
  internal/admin/handler.go                    dispatches the account routes; session gate
  internal/admin/account.go                    NEW the routes of spec 12.7
  internal/admin/session.go                    NEW Commsgun-Session resolution and the mode rule
  internal/admin/decode.go                     NEW strict JSON decoding, shared with probe.go
  internal/admin/probe.go                      uses decode.go
  internal/enqueue/handler.go                  refuses smtp_submission apps (401)
  internal/enqueue/submit.go                   + SubmitWindowed
  internal/enqueue/validate.go                 validRecipient → ValidRecipient
  internal/submission/message.go              NEW DATA → enqueue request, reply mapping
  internal/submission/server.go               NEW go-smtp backend and session
  cmd/commsgun-api/main.go                     addresses from config; SMTP listener; `admin` dispatch
  cmd/commsgun-api/admin.go                    NEW CLI verbs
ui/
  package.json, pnpm-lock.yaml                 + openid-client (exact, dev)
  src/env.ts                                   + OIDC_ISSUER, OIDC_CLIENT_ID, OIDC_CLIENT_SECRET, OIDC_SCOPES, OIDC_AUTO_PROVISION
  src/app.d.ts                                 Locals.session
  src/hooks.server.ts                          init: import; handle: session via the API
  src/lib/types.ts                             + AuthMode, User, Session, SessionInfo
  src/lib/login.ts                             NEW which login controls a mode shows
  src/lib/server/constants.ts                  + session, OIDC and import constants; − SESSION_TTL_MS, SESSION_KEY_LABEL
  src/lib/server/env.ts                        ADMIN_PASSWORD optional, OIDC block, oidcKey; − sessionKey
  src/lib/server/api.ts                        session header; account calls
  src/lib/server/bootstrap.ts                  NEW start-up import with retry
  src/lib/server/session.ts                    − HMAC cookie functions (keeps safeNext, LoginLimiter)
  src/lib/server/limiter.ts                    NEW one password limiter for /login and /account
  src/lib/server/oidc.ts                       NEW relying party
  src/lib/server/oidc-cookie.ts                NEW sealed transaction cookie
  src/routes/login/+page.server.ts, +page.svelte, login/sso/+server.ts (NEW)
  src/routes/auth/callback/+page.server.ts, +page.svelte (NEW); auth/link/+server.ts (NEW)
  src/routes/account/+page.server.ts, +page.svelte (NEW)
  src/routes/logout/+server.ts, +layout.svelte, +page.server.ts, messages/**, probe/+server.ts
  e2e/stub-api.ts                              account routes; session gate
  e2e/stub-issuer.ts                           NEW stub OIDC issuer (HTTPS from a test CA made by openssl at its
                                                 start; served by Playwright, imported by unit tests)
  e2e/*.spec.ts, playwright.config.ts          UI-05 rewrite; sso.spec.ts, account.spec.ts (NEW); issuer webServer
  .gitignore                                   + /e2e/.tls
scripts/smoke.sh                               API at --memory 96m; healthz waits for the import; CLI disable
README.md, docs/**                             Task 12
```

Task order and releasability: Tasks 1–5 are API-only and additive. Task 6 adds the import (UI) while the old login still
runs. Task 7 flips both modules together (the API gate and the UI's opaque session must ship in one image pair, CG-07).
Tasks 8–9 add SSO behind unset OIDC env; Tasks 10–11 add SMTP behind unset `SMTP_ADDR`.

---

### Task 1: Forward-only migrations and the accounts schema

**Files:**
- Create: `api/internal/store/migrate.go`, `api/internal/store/migrations/0002_accounts.sql`
- Modify: `api/internal/store/store.go` (`Open`, remove `initSchema` and `schemaVersion`)
- Test: `api/internal/store/migrate_test.go`

**Interfaces:**
- Produces: `migrate(w *sql.DB, base string, migrations fs.FS) error` — runs `schema.sql` (version 1) on an empty
  database, then each `NNNN_<name>.sql` whose number exceeds `schema_version`, in order, each in its own writer
  transaction that also sets `schema_version`; refuses a database newer than the newest file, and a gap in numbering.
  `Open` passes the embedded `migrations` directory. Tables (0002): `users(id TEXT PK, username TEXT NOT NULL UNIQUE,
  role TEXT NOT NULL CHECK (role='admin'), password_hash TEXT NOT NULL, disabled INTEGER NOT NULL DEFAULT 0, created_at,
  updated_at INTEGER NOT NULL)`; `identities(user_id REFERENCES users(id), issuer, subject TEXT NOT NULL, created_at,
  UNIQUE(issuer, subject), UNIQUE(user_id, issuer))`; `sessions(token_hash BLOB PK, user_id REFERENCES users(id), kind
  TEXT NOT NULL CHECK (kind IN ('local','oidc')), created_at, expires_at INTEGER NOT NULL, reauth_at INTEGER,
  reauth_failures INTEGER NOT NULL DEFAULT 0)`, with an index on `sessions(user_id)`. Times are UTC unix ms.

- [ ] **Step 1: Write the failing tests**

```go
func TestFreshDatabaseReachesLatest(t *testing.T)   // Open(t.TempDir()) → schema_version 2; users, identities, sessions exist
func TestMigratesTodaysSchema(t *testing.T)          // db built from schema.sql alone (v1) + one messages row → Open → v2, row intact
func TestMigrationsApplyOnceInOrder(t *testing.T)    // MapFS{0002 creates a, 0003 alters a}: applied in order; reopening applies nothing
func TestNewerDatabaseRefused(t *testing.T)          // schema_version 3, newest file 0002 → error containing "newer", no table touched
func TestFailedMigrationRollsBack(t *testing.T)      // 0002 with bad SQL → error, version still 1, no partial table
func TestMigrationGapRefused(t *testing.T)           // MapFS{0002, 0004} → error naming 0003
```

Existing `TestSchemaCreationIsAtomic` and `TestOpenPragmas` must pass unchanged.

- [ ] **Step 2: Run** `cd api && go test ./internal/store/ -run 'Migrat|Fresh|Newer|Gap'` → FAIL (undefined `migrate`).
- [ ] **Step 3: Implement** `migrate` with `//go:embed migrations/*.sql`; never edit `schema.sql` (API-02).
- [ ] **Step 4: Run** the API pipeline:
      `cd api && gofmt -l . && go vet ./... && GOTOOLCHAIN=$(go env GOVERSION) go run honnef.co/go/tools/cmd/staticcheck@v0.8.2 ./... && go test ./... -race`
      → `gofmt` prints nothing, all PASS.
- [ ] **Step 5: Commit** — `feat(store): forward-only migrations and the accounts schema`.

---

### Task 2: Password hashing

**Files:**
- Create: `api/internal/password/password.go`
- Modify: `api/internal/constants/constants.go`, `api/go.mod`, `api/go.sum` (`go get golang.org/x/crypto@<newest ≥ 2 weeks old>`)
- Test: `api/internal/password/password_test.go`

**Interfaces:**
- Produces:
  - constants `Argon2MemoryKiB = 19456`, `Argon2Time = 2`, `Argon2Threads = 1`, `PasswordHashConcurrency = 1`,
    `PasswordMinChars = 16`, `PasswordMaxBytes = 1024`, `Argon2SaltBytes = 16`, `Argon2KeyBytes = 32`.
  - `type Params struct{ MemoryKiB uint32; Time uint32; Threads uint8 }`; `Default() Params` (the constants).
  - `Valid(pw string) bool` — `PasswordMinChars` code points (`utf8.RuneCountInString`) to `PasswordMaxBytes` bytes.
  - `NewHasher(p Params, concurrency int) *Hasher`; `(*Hasher).Hash(ctx, pw string) (string, error)` →
    `$argon2id$v=19$m=<m>,t=<t>,p=<p>$<salt>$<key>` (unpadded std base64); `(*Hasher).Verify(ctx, pw, phc string)
    (bool, error)` reads the parameters from `phc`; `(*Hasher).VerifyDummy(ctx, pw string) error` verifies against a
    dummy hash made once per Hasher with its own params. All three wait for a slot within `ctx` and return `ctx.Err()`
    if it ends first; comparison is `subtle.ConstantTimeCompare`.

- [ ] **Step 1: Write the failing tests** (tiny `Params` except where named)

```go
func TestHashIsPHC(t *testing.T)                 // NewHasher(Default(),1).Hash → prefix "$argon2id$v=19$m=19456,t=2,p=1$"; salt decodes to 16 bytes, key to 32; two hashes of one password differ
func TestVerify(t *testing.T)                    // right → true; wrong, prefix, longer → false
func TestVerifyReadsParamsFromHash(t *testing.T) // hash made with {64,1,1} verifies under a Hasher built with Default()
func TestVerifyMalformed(t *testing.T)           // "", "$argon2i$…", bad base64, m=0, missing field → (false, err), no panic
func TestValid(t *testing.T)                     // 15 code points false; 16 true; "😀"×15 (60 bytes) false; "😀"×16 true; 1024 bytes true; 1025 false
func TestOneHashAtATime(t *testing.T)            // slot held by the test: Verify with a ctx cancelled after 50ms returns context.Canceled; after release Hash succeeds
func TestVerifyDummy(t *testing.T)               // returns nil for any password and waits for the slot like Verify
```

- [ ] **Step 2: Run** `go test ./internal/password/` → FAIL (package missing).
- [ ] **Step 3: Implement** with `argon2.IDKey`; the slot is a buffered channel of size `concurrency`.
- [ ] **Step 4: Run** the API pipeline (Task 1 Step 4) → PASS.
- [ ] **Step 5: Commit** — `feat(api): argon2id password hashing, one hash at a time`.

---

### Task 3: Accounts store

**Files:**
- Create: `api/internal/store/accounts.go`
- Modify: `api/internal/constants/constants.go`, `api/internal/model/model.go` (`SessionKind`: `store` imports `config`,
  so `config` cannot import `store`)
- Test: `api/internal/store/accounts_test.go`

**Interfaces:**
- Consumes: Task 1 tables.
- Produces:
  - constants `UsernameMinLen = 3`, `UsernameMaxLen = 32`, `SessionTokenBytes = 32`, `SessionTTL = 12 * time.Hour`,
    `ReauthWindow = 5 * time.Minute`, `ReauthMaxFailures = 5`, `IdentitySubjectMaxBytes = 255`.
  - `model.SessionKind` (`model.KindLocal` `"local"`, `model.KindOIDC` `"oidc"`).
  - `ValidUsername(s string) bool`; `type User struct{ ID, Username, Role string; Disabled bool }`;
    `type Session struct{ User User; Kind model.SessionKind; ExpiresAt time.Time; ReauthAt *time.Time }`.
  - Errors `ErrUsersExist`, `ErrUserExists`, `ErrAlreadyLinked`; missing rows are `ErrNotFound`.
  - `(*Store).CreateFirstUser(ctx, now, username, hash string) (User, error)` — only while `users` is empty, in one
    writer transaction (create and import); `AddUser(ctx, now, username, hash)`; `CountUsers(ctx) (int, error)`;
    `UserByUsername(ctx, username) (User, string /*hash*/, error)`; `UserByIdentity(ctx, iss, sub) (User, error)`;
    `SetPassword(ctx, now, username, hash) error`; `SetDisabled(ctx, now, username string, disabled bool) error`;
    `Unlink(ctx, now, username) (int, error)` (removes all of the user's identities); `Link(ctx, now, userID, iss, sub)
    error`; `CreateSession(ctx, now, userID string, kind model.SessionKind, tokenHash [32]byte) (time.Time, error)`
    (expires `now+SessionTTL`); `SessionByHash(ctx, now, tokenHash) (Session, error)` (`ErrNotFound` when missing,
    expired or the user is disabled); `DeleteSession(ctx, tokenHash) error` (nil when absent);
    `MarkReauth(ctx, now, tokenHash) error`; `RecordReauthFailure(ctx, tokenHash) (deleted bool, err error)`
    (the `ReauthMaxFailures`th deletes the session). `SetPassword`, `SetDisabled(…, true)` and `Unlink` delete the
    user's sessions in the same transaction.

- [ ] **Step 1: Write the failing tests**

```go
func TestValidUsername(t *testing.T)            // "ab" f, "abc" t, 32 chars t, 33 f, "Admin" f, "a b" f, "a.b_c-d" t
func TestCreateFirstUserOnlyWhileEmpty(t *testing.T) // second call → ErrUsersExist
func TestCreateFirstUserRace(t *testing.T)      // 8 goroutines → exactly 1 User, 7 ErrUsersExist, COUNT(*)=1
func TestAddUserNamesArePermanent(t *testing.T) // duplicate (also of a disabled user) → ErrUserExists
func TestSessionLifetime(t *testing.T)          // expires now+12h; SessionByHash at +12h-1ms ok, at +12h ErrNotFound
func TestEndingAccessDeletesSessions(t *testing.T) // SetPassword, SetDisabled(true), Unlink each leave 0 sessions for the user, others untouched
func TestDisabledUserSessionRefused(t *testing.T)  // disabled flipped by SQL alone → SessionByHash ErrNotFound
func TestLinkUniqueness(t *testing.T)           // pair on another user, second identity for the same issuer, same pair twice → ErrAlreadyLinked; another issuer ok
func TestReauthFailures(t *testing.T)           // 4 failures → deleted false; 5th → true, session gone
func TestMarkReauth(t *testing.T)               // ReauthAt == now
```

- [ ] **Step 2: Run** `go test ./internal/store/ -run 'User|Session|Link|Reauth'` → FAIL.
- [ ] **Step 3: Implement** in the API-03 shape; guarded updates (API-04).
- [ ] **Step 4: Run** the API pipeline → PASS.
- [ ] **Step 5: Commit** — `feat(store): users, identities and revocable sessions`.

---

### Task 4: Break-glass CLI

**Files:**
- Create: `api/cmd/commsgun-api/admin.go`
- Modify: `api/cmd/commsgun-api/main.go` (`main` dispatches `admin`), `api/internal/config/config.go` (export `DataDir`)
- Test: `api/cmd/commsgun-api/admin_test.go`

**Interfaces:**
- Consumes: Task 2 `Hasher`, `Valid`; Task 3 store methods.
- Produces: `config.DataDir(getenv func(string) string) string` (default `/data`, used by `Load` too);
  `runAdmin(ctx context.Context, args []string, getenv func(string) string, stdin io.Reader, stdout, stderr io.Writer,
  clk clock.Clock, h *password.Hasher) int` — verbs `create`, `add`, `reset-password`, `disable`, `enable`, `unlink`,
  each with exactly one `<username>`; the password (create, add, reset-password) is read from stdin, one trailing `\n` or
  `\r\n` stripped. Exit 0 done, 1 refused, 2 usage. Reads only `DATA_DIR`; never calls `config.Load`; opens no socket.
  Output never contains the password.

- [ ] **Step 1: Write the failing tests**

```go
func TestAdminCreate(t *testing.T)             // empty store → 0, user role admin; again → 1 "users already exist"
func TestAdminAdd(t *testing.T)                // after create → 0; same name → 1
func TestAdminResetPassword(t *testing.T)      // new hash verifies; the user's sessions are gone
func TestAdminDisableEnable(t *testing.T)      // disable → sessions gone, SessionByHash refused; enable → login possible again
func TestAdminUnlink(t *testing.T)             // identities and sessions gone
func TestAdminRefusesBadInput(t *testing.T)    // 15-char password → 1; bad username → 1; unknown verb, missing or extra arg → 2
func TestAdminNeedsOnlyDataDir(t *testing.T)   // getenv knows only DATA_DIR → 0
func TestAdminWhileServerRuns(t *testing.T)    // run() serving on the same DATA_DIR; disable → 0 and the running admin API refuses that session
func TestAdminNeverPrintsPassword(t *testing.T)
```

- [ ] **Step 2: Run** `go test ./cmd/commsgun-api/ -run Admin` → FAIL.
- [ ] **Step 3: Implement.** `main` calls `os.Exit(runAdmin(…))` when `os.Args[1] == "admin"`, before signal setup.
- [ ] **Step 4: Run** the API pipeline → PASS.
- [ ] **Step 5: Commit** — `feat(api): admin CLI for bootstrap and break-glass`.

---

### Task 5: Account routes on the admin API

**Files:**
- Create: `api/internal/admin/account.go`, `api/internal/admin/session.go`, `api/internal/admin/decode.go`
- Modify: `api/internal/admin/handler.go`, `api/internal/admin/probe.go` (use `decode.go`), `api/internal/config/config.go`
  (`AuthMode`), `api/internal/constants/constants.go` (`AccountMaxBodyBytes = ProbeMaxBodyBytes`, 4096),
  `api/cmd/commsgun-api/main.go` (build the Hasher, pass it)
- Test: `api/internal/admin/account_test.go`, `api/internal/config/config_test.go`

**Interfaces:**
- Consumes: Tasks 2–3.
- Produces:
  - `config.AuthMode` (`AuthLocal`, `AuthOIDC`, `AuthBoth`) in `Config.AuthMode`; `(AuthMode).Allows(kind
    model.SessionKind) bool`.
  - `admin.NewHandler(st *store.Store, cfg *config.Config, clk clock.Clock, log *slog.Logger, h hasher)` where `hasher`
    is admin's own interface `{Hash; Verify; VerifyDummy}` (API-07).
  - Routes and JSON exactly as spec 12.7: `GET auth`, `POST sessions/local`, `POST sessions/oidc`, `POST
    bootstrap/import`, `GET|DELETE session`, `POST sessions/reauth`, `POST identities`. `User =
    {"id","username","role"}`; `Session = {"token","expires_at","user"}`; `GET session → {"user","kind","expires_at"}`.
  - `HeaderSession = "Commsgun-Session"`; `(*handler).session(r) (store.Session, [32]byte, bool)` deletes and refuses a
    session whose kind the mode forbids. Order of checks: bearer → session → mode → `session_kind` → body → credentials.
  - `decodeStrict(r *http.Request, max int64, dst any) (status int, code, field string)` — 413 with an empty body over
    `max`, 422
    `invalid_json` / `unknown_field` with `field`, as the probe answers today.
  - Existing routes are **not** gated yet (Task 7).

- [ ] **Step 1: Write the failing tests** (fake hasher counts calls)

```go
func TestAuthModeConfig(t *testing.T)     // unset → local; "oidc","both" accepted; "OIDC", "sso" → Load error naming AUTH_MODE
func TestAuthRoute(t *testing.T)          // each mode → 200 {"mode":…}; no session needed; wrong bearer → 401 unauthorized
func TestSessionsLocal(t *testing.T) {
  // 201, keys exactly token/expires_at/user{id,username,role}; token matches ^[A-Za-z0-9_-]{43}$; expires_at == now+12h
  // wrong password, unknown user, disabled user → 401 {"error":"invalid_credentials"}, byte-identical
  // mode oidc → 403 {"error":"mode"} and no hash run
}
func TestUnknownUsernameUsesDummyHash(t *testing.T) // VerifyDummy 1, Verify 0
func TestSessionsOIDC(t *testing.T)       // linked → 201; unlinked, disabled → 403 not_linked; mode local → 403 mode;
                                          // iss "http://x", "", "not a url"; sub "", 256 bytes → 422 {"error":"invalid_identity","field":…}
func TestBootstrapImport(t *testing.T)    // empty → {"result":"imported"}; again → skipped with no Hash call;
                                          // 15 chars, 1025 bytes → 422 {"error":"invalid_password","field":"password"} even when users exist
func TestBootstrapImportRace(t *testing.T)// 8 concurrent → one imported, seven skipped, one row
func TestSessionGetDelete(t *testing.T)   // 200 {"user","kind","expires_at"}; missing/unknown/expired/?commsgun-session= → 401 {"error":"session"};
                                          // DELETE → 204 for valid, missing and unknown; GET after DELETE → 401
func TestModeSwitchEndsRefusedSessions(t *testing.T) // local session + mode oidc → 401 session and row deleted; oidc session + mode local → same
func TestReauth(t *testing.T)             // both+local: 204, reauth_at set; wrong → 401 invalid_credentials; 5th wrong → 401 and session gone;
                                          // mode local/oidc → 403 mode; oidc session in both → 403 session_kind
func TestIdentities(t *testing.T)         // no reauth → 403 reauth_required; at reauth+5m → 204; at reauth+5m+1s → 403 reauth_required;
                                          // pair taken, issuer already linked → 409 already_linked; mode / session_kind refusals; then sessions/oidc → 201
func TestAccountBodiesStrict(t *testing.T)// malformed → 422 invalid_json; {"username":…,"x":1} → 422 unknown_field "x"; 4097 bytes → 413, empty body; 4096 bytes not 413
func TestAccountMethods(t *testing.T)     // wrong method on each route → 405 with Allow
func TestAccountLogsCarryNoSecrets(t *testing.T) // no password, token, username, iss or sub in the captured log
```

`TestProbe*` must pass unchanged after `probe.go` moves to `decodeStrict`.

- [ ] **Step 2: Run** `go test ./internal/admin/ ./internal/config/` → FAIL.
- [ ] **Step 3: Implement.** Tokens from `crypto/rand`; a hashing call whose `ctx` ends answers 503 `unavailable` via `h.fail`.
- [ ] **Step 4: Run** the API pipeline → PASS.
- [ ] **Step 5: Commit** — `feat(admin): account routes (spec 12.7), not yet required`.

---

### Task 6: Start-up import of the env admin

**Files:**
- Create: `ui/src/lib/server/bootstrap.ts`
- Modify: `ui/src/hooks.server.ts` (`init`), `ui/src/lib/server/api.ts` (`bootstrapImport`), `ui/src/lib/server/constants.ts`
  (`IMPORT_RETRY_MS = 1000`), `ui/e2e/stub-api.ts` (`POST /admin/v1/bootstrap/import`, `GET /__imports`),
  `ui/e2e/healthz.test.ts`, `scripts/smoke.sh`
- Test: `ui/src/lib/server/bootstrap.test.ts`, `ui/src/lib/server/api.test.ts`

**Interfaces:**
- Consumes: Task 5 `POST /admin/v1/bootstrap/import`.
- Produces: `createApi(...).bootstrapImport(username: string, password: string): Promise<AccountResult<{ result:
  'imported' | 'skipped' }>>` with `AccountResult<T> = { ok: true; data: T } | { ok: false; status: number; error: string }`
  (`error` is the API's code, or `unavailable` for network/timeout); `importAdmin(api, password: string, sleep?: (ms:
  number) => Promise<void>): Promise<'imported' | 'skipped'>` — retries every `IMPORT_RETRY_MS`, with no limit on
  attempts, while `unavailable` or `status >= 500`, throws on anything else with a message naming the code, never the
  password. `init` awaits it when `ADMIN_PASSWORD` is set and logs once `{"level":"warn","msg":"ADMIN_PASSWORD is ignored: users exist"}` on `skipped`.
  adapter-node awaits `server.init` before it listens, so `/healthz` is unreachable until the import ends and a throw
  stops the process.

- [ ] **Step 1: Write the failing tests**

```ts
it('retries a network error and a 503, then resolves imported');      // two sleeps of IMPORT_RETRY_MS
it('resolves skipped');
it.each([[401, 'unauthorized'], [422, 'invalid_password'], [404, 'not_found'], [400, 'x']])
  ('throws on %i %s, naming the code and never the password');
it('bootstrapImport posts {"username":"admin","password"} with the bearer token and no session header');
// e2e/healthz.test.ts
test('the UI imported the admin before /healthz answered');           // GET /__imports → [{ username: 'admin' }]
```

- [ ] **Step 2: Run** `cd ui && pnpm test` → FAIL.
- [ ] **Step 3: Implement**; `ADMIN_USERNAME` stays the import's username.
- [ ] **Step 4: Change `scripts/smoke.sh`:** run the API with `--memory 96m` (the edge's `mem_limit`; spec 12.1, CG-06);
      start the UI container **before** the API and assert for 3 s that `ui_curl $UI_URL/healthz` never answers 200
      (`PASS ui healthz waits for the import`), then start the API. Keep `API_MEM_LIMIT_MIB=60` (steady state).
- [ ] **Step 5: Run** the UI pipeline `cd ui && pnpm install --frozen-lockfile && pnpm check && pnpm lint && pnpm test && pnpm test:e2e`
      and the images: `docker build -t commsgun-api:dev api && docker build -t commsgun-ui:dev ui && shellcheck scripts/smoke.sh && scripts/smoke.sh commsgun-api:dev commsgun-ui:dev`
      → every line `PASS`, `smoke: ok`. If the API is OOM-killed or exceeds 60 MiB, stop and report the figures: the
      argon2 parameters go down or the owner raises a ceiling (spec 12.1).
- [ ] **Step 6: Commit** — `feat(ui): import the env admin into the API at start`.

---

### Task 7: Revocable sessions end to end

**Files:**
- Modify (API): `api/internal/admin/handler.go` (gate), `api/internal/admin/handler_test.go`, `probe_test.go` (send a
  session), `api/cmd/commsgun-api/main_test.go`
- Modify (UI): `ui/src/hooks.server.ts`, `ui/src/app.d.ts`, `ui/src/lib/server/{api,env,session,constants}.ts`,
  `ui/src/lib/types.ts`, `ui/src/routes/login/+page.server.ts`, `ui/src/routes/logout/+server.ts`,
  `ui/src/routes/+page.server.ts`, `ui/src/routes/messages/+page.server.ts`, `ui/src/routes/messages/[id]/+page.server.ts`,
  `ui/src/routes/probe/+server.ts`, `ui/e2e/stub-api.ts`, `ui/e2e/{login,overview,messages,probe}.spec.ts`,
  `ui/playwright.config.ts`
- Create: `ui/src/lib/server/limiter.ts`
- Modify: `scripts/smoke.sh`
- Test: `api/internal/admin/account_test.go`, `ui/src/lib/server/{api,env,session}.test.ts`, the e2e specs above

**Interfaces:**
- Consumes: Task 5 routes, Task 6 import.
- Produces:
  - API: every `/admin/v1/*` route needs a valid `Commsgun-Session` except `auth`, `sessions/local`, `sessions/oidc`,
    `bootstrap/import` (`DELETE session` always 204).
  - UI types (CG-01): `AuthMode`, `User`, `Session`, `SessionInfo` as spec 12.7.
  - `createApi(env: Env, fetchFn: typeof fetch, session?: string)` sends `commsgun-session` on every call when given;
    adds `authMode()`, `createLocalSession(username, password)`, `session()`, `deleteSession()` (all `AccountResult`).
  - `App.Locals = { authed: boolean; session: { token: string; kind: 'local' | 'oidc'; expiresAt: string; user: User } | null }`.
  - Constants: `SESSION_TOKEN = /^[A-Za-z0-9_-]{43}$/` (twin of `SessionTokenBytes`), `SESSION_HEADER = 'commsgun-session'`;
    removed: `SESSION_TTL_MS`, `SESSION_KEY_LABEL`, and `deriveSessionKey`, `signSession`, `verifySession`,
    `checkPassword` with their tests. `Env` loses `sessionKey`; `adminPassword: string | null` (unset or empty = null;
    if set, ≥ `MIN_ADMIN_PASSWORD` code points, `[...v].length`).
  - `passwordLimiter` (one `LoginLimiter`) in `limiter.ts`, used by `/login` now and `/account` in Task 9.
  - Stub: `STUB_SESSION_TOKEN = 'fake-session-token-000000000000000000000000'`, `STUB_SESSION_TOKEN_2` (same shape, for
    Task 9), user `{"id":"fake-user-0001","username":"admin","role":"admin"}`; gated stub routes answer 401
    `{"error":"session"}` without the header; `POST /__auth` sets `{ mode, sessionsLocal?: 'unauthorized' | 'mode' }`.

- [ ] **Step 1: Write the failing API test**

```go
func TestEveryRouteNeedsSessionButFour(t *testing.T) // stats, messages, messages/x, facets, overview, GET+POST probes, session, sessions/reauth, identities:
                                                     // no header → 401 {"error":"session"}; the four exempt routes answer without one
```

and update the existing admin and `main_test.go` helpers to create a user and a session first (import, then
`sessions/local`); no assertion is removed.

- [ ] **Step 2: Write the failing UI tests**

```ts
// api.test.ts
it('sends commsgun-session on every call when created with a token, never in the URL');
it('maps 401 unauthorized, 401 invalid_credentials, 403 mode, 503 and a network error to distinct AccountResult codes');
// env.test.ts
it('accepts an unset or empty ADMIN_PASSWORD as null; rejects 15 code points and "😀".repeat(15) (30 UTF-16 units); accepts "😀".repeat(16)');
// e2e/login.spec.ts (rewritten; only login specs drive the form, UI-05)
// right credentials → cookie value == stub token, HttpOnly, Secure, SameSite=Strict, Path=/, expires within 60s of the stub's expires_at; lands on ?next
// wrong password → 400 "Incorrect username or password", no cookie
// stub sessionsLocal 'unauthorized' → "The delivery API returned an error", not the credentials message
// stub sessionsLocal 'mode' → "Password sign-in is turned off"
// POST /logout → the stub saw DELETE /admin/v1/session with the header; cookie cleared; GET / → /login
// a cookie the stub refuses (401 session) → 303 /login and the cookie is deleted
// 6th attempt in a minute → 429 "Too many attempts. Try again in a minute."
// Review Focus 4: '', 42 chars, 44 chars, 'a'.repeat(10240), '!!!…' → 303 /login, cookie deleted, stub received no /admin/v1/session call
// Review Focus 1: scenario 'down' with a valid cookie → 503 page "The delivery API is not responding", cookie kept, no redirect; /healthz 200
// overview/messages/probe specs: beforeEach sets commsgun_session = STUB_SESSION_TOKEN (UI-05 rewritten)
```

- [ ] **Step 3: Run** `cd api && go test ./internal/admin/` and `cd ui && pnpm test && pnpm test:e2e` → FAIL.
- [ ] **Step 4: Implement.** The hook validates only cookies matching `SESSION_TOKEN`; `error` `session` deletes the
      cookie; `unavailable`/`error`/`unauthorized` on a non-public path answers `error(503, …)`. Login sets `maxAge`
      from `expires_at`. Every `createApi` call site passes `locals.session?.token` (UI-02).
- [ ] **Step 5: Change `scripts/smoke.sh`:** the existing curl login now goes through the API (unchanged requests); at
      the end, run `docker run --rm --network none --read-only --cap-drop ALL --user 10001:10001 -v "$VOLUME":/data
      "$API_IMAGE" admin disable admin` → exit 0, then `ui_page /` with the old cookie → 303 to `/login`
      (`PASS disabling a user ends their session`).
- [ ] **Step 6: Run** the API pipeline, the UI pipeline, and the images + smoke (Task 6 Step 5) → all PASS.
- [ ] **Step 7: Commit** — `feat: revocable server-side sessions (U4 amended, K2 session gate)`.

---

### Task 8: SSO login

**Files:**
- Create: `ui/src/lib/server/oidc.ts`, `ui/src/lib/server/oidc-cookie.ts`, `ui/src/lib/login.ts`,
  `ui/src/routes/login/sso/+server.ts`, `ui/src/routes/auth/callback/+page.server.ts`, `+page.svelte`,
  `ui/e2e/stub-issuer.ts`, `ui/e2e/sso.spec.ts`
- Modify: `ui/package.json`, `ui/pnpm-lock.yaml` (`pnpm add -D -E openid-client@<newest 6.x ≥ 2 weeks old>`),
  `ui/src/env.ts`, `ui/src/lib/server/env.ts`, `ui/src/lib/server/constants.ts`, `ui/src/lib/server/api.ts`
  (`createOidcSession`), `ui/src/hooks.server.ts` (`PUBLIC_PATHS` + `/login/sso`, `/auth/callback`),
  `ui/src/routes/login/+page.server.ts`, `+page.svelte`, `ui/e2e/stub-api.ts` (`POST sessions/oidc`),
  `ui/playwright.config.ts`, `ui/.gitignore` (`/e2e/.tls`)
- Test: `ui/src/lib/server/{env,oidc,oidc-cookie}.test.ts`, `ui/src/lib/login.test.ts`, `ui/e2e/sso.spec.ts`

**Interfaces:**
- Produces:
  - `Env.oidc: { issuer: string; clientId: string; clientSecret: string; scopes: string } | null` (all three set or all
    unset; `OIDC_SCOPES` default `openid profile email`; issuer `https:` only, loopback hosts too, and the RP never sets
    openid-client's `allowInsecureRequests`); `Env.oidcKey: Buffer` =
    HMAC-SHA256(`SESSION_SECRET`, `OIDC_KEY_LABEL`); `OIDC_AUTO_PROVISION` present (even empty) → throws. `src/env.ts`
    declares it with a schema that keeps `undefined`.
  - Constants `OIDC_COOKIE = 'commsgun_oidc'`, `OIDC_TX_TTL_MS = 600_000`, `OIDC_KEY_LABEL = 'commsgun-oidc-key'`,
    `DEFAULT_OIDC_SCOPES = 'openid profile email'`, `AUTH_TIME_SKEW_MS = 30_000` (twin of the SSO spec § 3 skew, CG-02).
  - `type Transaction = { purpose: 'login' | 'link'; state: string; nonce: string; verifier: string; next: string;
    started: number; exp: number; binding?: string; username?: string; iss?: string; sub?: string; preferredUsername?:
    string | null }` (`started` = the flow's start, ms; SSO spec § 3 "Callback state");
    `sealTransaction(key: Buffer, tx: Transaction): string` (AES-256-GCM, base64url); `openTransaction(key: Buffer, value:
    string | undefined, now: number): Transaction | null` (tampered, expired, junk → null, never throws).
  - `beginAuth(oidc: OidcEnv, input: { purpose; next; binding?; username? }, now: number, fetchFn?: typeof fetch):
    Promise<{ url: URL; tx: Transaction }>` — discovery on every call (no cache, so an issuer outage is seen at once),
    `id_token_signed_response_alg: 'RS256'`, `ClientSecretBasic`, `enableNonRepudiationChecks`, PKCE S256, `state`,
    `nonce`, `redirect_uri` = `${origin}/auth/callback`, `started = now`, and `prompt=login` with `max_age=1` when
    `purpose === 'link'`.
    `completeAuth(oidc, tx, callbackUrl: URL, fetchFn?): Promise<{ ok: true; iss: string; sub: string;
    preferredUsername: string | null } | { ok: false; reason: 'state' | 'token' | 'unavailable' }>` — discovery on
    every call too, with `enableNonRepudiationChecks` (ID-token signatures verified against the issuer's JWKS), so each
    callback verifies on a key set fetched within it; an optional `deps.jwksCache` is passed to openid-client's exported
    `setJwksCache` (test seam only, Q9). openid-client's `checks.maxAge` is never passed: it compares `auth_time` with the
    callback's clock, not the flow's start. For `link`, a missing `auth_time` or
    `auth_time * 1000 < tx.started - AUTH_TIME_SKEW_MS` → reason `token`, the same path as a wrong `nonce`.
    `fetchFn` is passed as openid-client's `customFetch`.
  - `loginOptions(mode: AuthMode | null, ssoConfigured: boolean): { password: boolean; sso: 'button' | 'not_configured'
    | 'hidden' }` (`null` mode = API unreachable: password form plus `<ApiError>`).
  - `ui/e2e/stub-issuer.ts`: `createIssuer(issuerUrl: string)` → `{ handle(req: Request): Promise<Response>; next(claims:
    Partial<{ sub; preferred_username; iss; aud; exp; nonce; alg; auth_time: number | null }>): void; setLiveSession(sub:
    string): void; down(on: boolean): void }` with RS256 keys from `generateKeyPairSync` (no key fixture), one-time
    codes, PKCE and Basic-auth checks; `auth_time` defaults to the moment the code is issued (`null` omits the claim);
    run as a server on a **different site** from the UI (`localhost:4182` vs `127.0.0.1:4173`) so the callback is a
    cross-site navigation. Exports `ISSUER_URL = 'https://localhost:4182'` and `ISSUER_CA_FILE` (absolute
    `ui/e2e/.tls/ca.pem`). Run as `node e2e/stub-issuer.ts`, before listening it makes a fresh test CA and a `localhost`
    leaf in `e2e/.tls/` with `execFileSync('openssl', …)` (importing the module runs nothing, so unit tests need no
    openssl): `req -x509 -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes -days 1 -subj /CN=commsgun-e2e-ca -keyout
    ca-key.pem -out ca.pem`, then `req -x509 -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes -days 1 -subj
    /CN=localhost -addext subjectAltName=DNS:localhost -addext basicConstraints=critical,CA:FALSE -CA ca.pem -CAkey
    ca-key.pem -keyout key.pem -out cert.pem` (OpenSSL 3), and serves `node:https` with the leaf; `next`,
    `setLiveSession` and `down` are `POST /__issuer/{next,live-session,down}` JSON controls. `e2e/` is already in
    `ui/.dockerignore`, so the keys never enter the image.
  - `playwright.config.ts`: a `webServer` entry for the issuer **before** the UI's (Playwright starts webServers in
    order, each awaited, and before any `globalSetup`, so the CA exists when the UI process starts; a `globalSetup`
    would run too late), with `url: ${ISSUER_URL}/.well-known/openid-configuration` and `ignoreHTTPSErrors: true`.
    `testEnv` adds `OIDC_ISSUER: ISSUER_URL`, `OIDC_CLIENT_ID=commsgun`, a `fake-`-prefixed `OIDC_CLIENT_SECRET` and
    `NODE_EXTRA_CA_CERTS: ISSUER_CA_FILE` (Node reads it once at start; the UI's `fetch` trusts the stub only through
    it). `use.ignoreHTTPSErrors: true` lets the browser open the stub's authorize page; specs send the issuer
    controls with Playwright's `request` fixture, which honours it.

- [ ] **Step 1: Write the failing unit tests**

```ts
// env.test.ts
it.each(['OIDC_ISSUER', 'OIDC_CLIENT_ID', 'OIDC_CLIENT_SECRET'])('rejects %s alone (all or none)');
it.each(['http://issuer.example', 'http://localhost:4182', 'http://127.0.0.1:4182'])('rejects OIDC_ISSUER=%s'); // no loopback exception
it.each(['false', '', 'true'])('rejects OIDC_AUTO_PROVISION=%s');
it('defaults OIDC_SCOPES to "openid profile email"');
// oidc-cookie.test.ts: round trip; one flipped byte → null; at exp → null; other key → null; '', 'x', 10 KB → null
// oidc.test.ts — conformance (SSO spec § 3) against createIssuer('https://issuer.test') via fetchFn
it('authorization URL: response_type=code, code_challenge_method=S256, state, nonce, scope, redirect_uri; no prompt or max_age for login');
it('link purpose adds prompt=login and max_age=1; tx.started == now');
it('link: auth_time absent → { ok: false, reason: "token" }');            // conformance: missing auth_time on a link
it('link: auth_time at started - 31 s → token; at started - 30 s → ok'); // conformance: stale auth_time on a link
it('login: an absent or old auth_time is not checked');
it('success returns iss, sub and preferred_username; the token request used HTTP Basic');
it.each(['bad state', 'wrong iss', 'wrong aud', 'expired', 'wrong nonce', 'HS256', 'none', 'signed by another key'])
  ('%s → { ok: false }');                                     // state → reason 'state', the rest 'token'
it('a replayed code fails the second time');
it('the jwks_uri fetch fails (fetchFn rejects that URL only) → { ok: false }');
it('deps.jwksCache with uat = now - 301 s and the jwks_uri fetch failing → { ok: false }; uat = now - 10 s → ok'); // conformance: aged key set, refresh failing (Q9)
it('discovery failure → unavailable, and the next beginAuth fetches discovery again');
it('an http: issuer passed to beginAuth directly is refused by openid-client'); // allowInsecureRequests is never set
// login.test.ts: local → password only; oidc+configured → sso only; oidc unconfigured → not_configured; both → both; null → password
```

- [ ] **Step 2: Write the failing e2e tests** (`sso.spec.ts`, phone viewport)

```ts
// mode both: /login shows the password form and "Sign in with SSO"; mode oidc: no password field; mode oidc + no OIDC env is unit-tested
// SSO success → 200 page with <meta http-equiv="refresh"> to next and a "Continue" link; following it lands on next signed in;
//   commsgun_session set (Strict), commsgun_oidc cleared; the stub API got {"iss","sub"} only
// unlinked sub (stub 403 not_linked) → "This SSO account is not linked to a commsgun user", no session cookie, commsgun_oidc cleared
// stub 403 mode → "SSO sign-in is turned off"
// tampered state, and /auth/callback with no commsgun_oidc → "This sign-in has expired. Start again.", cookie cleared
// Review Focus 2: issuer down → /login renders, password login works; "Sign in with SSO" → /login?sso=unavailable "SSO is unavailable. Try again later."
// Review Focus 3: next = //evil.example, /\evil, /login, 'x'.repeat(3000) → lands on "/"
```

- [ ] **Step 3: Run** `pnpm test && pnpm test:e2e` → FAIL.
- [ ] **Step 4: Implement.** `/login/sso`: no OIDC → 303 `/login?sso=not_configured`; `beginAuth` fails → log and 303
      `/login?sso=unavailable`; else set `commsgun_oidc` and 303 to the IdP. The callback clears `commsgun_oidc` for
      `login` whatever the outcome, and on any failed check. "SSO is not configured" is logged once per process.
- [ ] **Step 5: Run** the UI pipeline (the e2e run needs `openssl` 3 on `PATH`; `ubuntu-latest` has it), then the
      images + smoke (Task 6 Step 5) → PASS (smoke runs without OIDC: unchanged). `sso.spec.ts` passing is the proof
      that the UI trusted the stub's CA through `NODE_EXTRA_CA_CERTS`; `grep -rn allowInsecureRequests ui/src` prints
      nothing; `git status --short` shows nothing under `ui/e2e/.tls`.
- [ ] **Step 6: Commit** — `feat(ui): OIDC login (spec 12.4)`.

---

### Task 9: Link SSO from `/account`

**Files:**
- Create: `ui/src/routes/account/+page.server.ts`, `+page.svelte`, `ui/src/routes/auth/link/+server.ts`, `ui/e2e/account.spec.ts`
- Modify: `ui/src/routes/auth/callback/+page.server.ts`, `+page.svelte` (link branch),
  `ui/src/routes/+layout.svelte` (header "Account" link), `ui/src/lib/server/api.ts`
  (`reauth`, `linkIdentity`), `ui/src/lib/server/oidc-cookie.ts` (`bindingOf`, `bindingMatches`), `ui/e2e/stub-api.ts`
  (`sessions/reauth`, `identities`)
- Test: `ui/src/lib/server/oidc-cookie.test.ts`, `ui/e2e/account.spec.ts`

**Interfaces:**
- Consumes: Task 8 `beginAuth`, `completeAuth`, transaction cookie; Task 7 `passwordLimiter`.
- Produces: `bindingOf(token: string): string` (SHA-256 hex); `bindingMatches(token: string, binding: string): boolean`
  (constant time). `/account` shows the form only when mode `both`, session kind `local` and OIDC configured; its one
  action re-authenticates and starts `beginAuth({ purpose: 'link', next: '/account', binding, username })`. The callback
  for `link` reads no session, refuses a missing or stale `auth_time` (Task 8 `completeAuth`; cookie cleared, no
  confirmation), re-seals the cookie with `iss`, `sub`, `preferredUsername` (keeping `started` and `exp`), and renders
  "You are linking <preferred_username> to <username>" (`your SSO account` when the claim is absent) with Confirm and
  Cancel posting to `/auth/link`. `POST /auth/link` checks `Origin` (UI-01), clears `commsgun_oidc` always, requires
  `purpose === 'link'` and `bindingMatches(locals.session.token, tx.binding)`, then 303 `/account?link=<linked |
  cancelled | already_linked | reauth_required | error>`.

- [ ] **Step 1: Write the failing tests**

```ts
// oidc-cookie.test.ts
it('bindingMatches is true only for the bound token');
// e2e/account.spec.ts
// mode both + local session: form shown; mode local, mode oidc, or an oidc-kind session: no form and a one-line reason
// wrong password → "Incorrect password"; 5 failures share /login's budget: a 6th POST to /login or /account → 429
// right password → IdP URL has prompt=login and max_age=1; with the stub issuer's live session set to another sub, the confirmation names
//   the re-authenticated user, not the live one (conformance: link while another IdP session is live)
// the callback request carried no commsgun_session (cross-site) and still rendered the confirmation
// stub next({ auth_time: null }), and next({ auth_time: <2 min before the re-auth POST> }) → no confirmation, commsgun_oidc cleared, no identities call
// Confirm → stub got POST /admin/v1/identities {"iss","sub"} with the session header; /account shows "SSO account linked"; commsgun_oidc cleared
// Cancel → no identities call; "Linking cancelled"; cookie cleared
// Confirm with STUB_SESSION_TOKEN_2 as the session → no identities call, "Linking failed. Try again."
// stub 409 → "That SSO account is already linked"; stub 403 reauth_required → "Confirm your password again: the 5-minute window passed"
// POST /auth/link with Origin https://evil.example → 403, no identities call
```

- [ ] **Step 2: Run** `pnpm test && pnpm test:e2e` → FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** the UI pipeline → PASS.
- [ ] **Step 5: Commit** — `feat(ui): link an SSO identity after re-auth (spec 12.5)`.

---

### Task 10: SMTP app config and the DATA mapping

**Files:**
- Create: `api/internal/submission/message.go`
- Modify: `api/internal/config/config.go` (`smtp_submission`, `send_window_seconds`, `AuthenticateSMTP`),
  `api/internal/enqueue/handler.go` (401 for an `smtp_submission` app), `api/internal/enqueue/submit.go`
  (`SubmitWindowed`), `api/internal/enqueue/validate.go` (`ValidRecipient`)
- Test: `api/internal/submission/message_test.go`, `api/internal/config/config_test.go`, `api/internal/enqueue/handler_test.go`

**Interfaces:**
- Produces:
  - `App.SMTPSubmission bool`, `App.SendWindow time.Duration` (default `MaxSendWindow`; ≤ 0 or > 86400 s refuses to
    start); `(*Config).AuthenticateSMTP(appID, key string) (*App, bool)` — `Authenticate` plus the flag.
  - `enqueue.ValidRecipient(ch model.Channel, r string) bool` (renamed, same body).
  - `enqueue.SubmitWindowed(ctx, cfg, st Enqueuer, now time.Time, app *config.App, namespace, key string, body []byte,
    sourceIP string, window time.Duration) (Outcome, *Failure)` — as `SubmitNamespaced`, but `expires_at = now + window`
    is applied after hashing; `SubmitNamespaced` calls it with 0 (unchanged behaviour) (spec 12.8; Q2 resolved).
  - `submission.Reply{ Code int; Enhanced [3]int; Text string }`; `submission.BuildRequest(raw []byte, mailFrom string,
    rcpts []string, app *config.App) (body []byte, key string, rej *Reply)` — the request JSON of spec 6.2 (`channel`
    email, `to` = `rcpts`, `from` = header `From` re-formatted with its display name, `subject` RFC 2047-decoded, `text`
    and/or `html`, `reply_to`) and `key` = hex SHA-256 of the trimmed `Message-ID`; `submission.ReplyFor(f
    *enqueue.Failure) Reply` — the table of spec 12.8 (unknown codes → `451 4.3.0`, the caller logs a defect).

- [ ] **Step 1: Write the failing tests**

```go
func TestSMTPAppConfig(t *testing.T)         // smtp_submission default false; send_window_seconds default 86400; 0, 86401 → Load error
func TestSMTPAppRefusedOnHTTP(t *testing.T)  // right key → 401 body byte-identical to a wrong key's
func TestAuthenticateSMTP(t *testing.T)      // smtp app + key → ok; non-smtp app with its key → false; unknown app → false
func TestBuildPlainAndAlternative(t *testing.T) // text/plain; multipart/alternative text+html → both fields
func TestTransferEncodings(t *testing.T)     // 7bit, 8bit, quoted-printable, base64 decoded; absent = 7bit
func TestCharsets(t *testing.T)              // utf-8 ok; absent = us-ascii, a non-ASCII byte under it → 554 5.6.0; iso-8859-1 → 554 5.6.0
func TestRefusedContent(t *testing.T)        // attachment, nested multipart, two text/plain, other type, no Message-ID,
                                             // two Reply-To addresses, encoded CR/LF in Subject → 554 5.6.0
func TestHeaderFromMustMatchEnvelope(t *testing.T) // other address → 550 5.7.1; same address other case or display name → ok, display name kept
func TestRecipientsFromEnvelopeOnly(t *testing.T)  // To, Cc ignored; Bcc never forwarded
func TestSubjectRFC2047(t *testing.T)
func TestReplyFor(t *testing.T)              // queued/duplicate 250 2.0.0; channel_not_allowed, sender_not_allowed 550 5.7.1;
                                             // invalid_subject, body_required, invalid_reply_to, idempotency_conflict 554 5.6.0;
                                             // rate_limited 451 4.7.1; 503 451 4.3.2; invalid_json, unknown_field, internal,
                                             // too_many_recipients_for_limit (RCPT caps first, Task 11) 451 4.3.0
func TestSendWindowExpiresBodies(t *testing.T)     // real store: window 3600s, provider paused; at +1h ExpireDue → expired, bodies NULL (anti-goal)
func TestDuplicateMessageID(t *testing.T)    // Review Focus 5: same message at t and t+10m, Message-ID case/space varied in the header line only → second is 250, one message
```

- [ ] **Step 2: Run** `go test ./internal/submission/ ./internal/config/ ./internal/enqueue/` → FAIL.
- [ ] **Step 3: Implement** with `net/mail`, `mime`, `mime/multipart`, `mime/quotedprintable`, `encoding/base64`.
- [ ] **Step 4: Run** the API pipeline → PASS (every existing enqueue and probe test unchanged).
- [ ] **Step 5: Commit** — `feat(api): SMTP submission apps and the DATA-to-enqueue mapping`.

---

### Task 11: SMTP listener and per-network bind addresses

**Files:**
- Create: `api/internal/submission/server.go`
- Modify: `api/internal/config/config.go` (`EnqueueAddr`, `AdminAddr`, `SMTPAddr`, `SMTPTLSCertFile`, `SMTPTLSKeyFile`),
  `api/internal/constants/constants.go` (`SMTPMaxSessions = 8`, `SMTPReadTimeout = 60 * time.Second`,
  `SMTPMaxMessagesPerConn = 10`), `api/cmd/commsgun-api/main.go`, `api/go.mod`, `api/go.sum` (go-smtp, go-sasl),
  `api/Dockerfile` (`EXPOSE 9200 9201 9203`)
- Test: `api/internal/submission/server_test.go`, `api/cmd/commsgun-api/main_test.go`, `api/internal/config/config_test.go`

**Interfaces:**
- Consumes: Task 10.
- Produces:
  - Config: addresses default `:9200`, `:9201`, unset; `SMTP_TLS_*` required when `SMTP_ADDR` is set and refused
    otherwise; each address parses with `net.SplitHostPort`.
  - `submission.NewServer(cfg *config.Config, st enqueue.Enqueuer, clk clock.Clock, log *slog.Logger, tlsCfg *tls.Config)
    *smtp.Server` — `TLSConfig` (MinVersion TLS 1.2), `MaxMessageBytes = MaxBodyBytes`, `MaxRecipients = MaxRecipients`,
    `ReadTimeout = SMTPReadTimeout`, `WriteTimeout = WriteTimeout` (30 s), `ErrorLog` discards library text;
    `AllowInsecureAuth` stays off, so `AUTH` before STARTTLS gets go-smtp's own `523 5.7.10`; the session implements
    `smtp.AuthSession` (`PLAIN` only), refuses `MAIL` before STARTTLS and, after it, before AUTH (530 5.7.0, RFC 4954),
    checks `MAIL FROM` against the app's senders, each `RCPT` with `enqueue.ValidRecipient` (553 5.1.3) and against
    `min(MaxRecipients, per_minute, per_day)` of the app (452 4.5.3), caps sessions at `SMTPMaxSessions` (`NewSession`
    → 451 4.3.2) and messages per connection at `SMTPMaxMessagesPerConn` (the 11th message's `MAIL` → 451 4.3.2);
    `Data` calls `BuildRequest` then
    `SubmitWindowed(…, "smtp", key, …, peer IP, app.SendWindow)` and answers `ReplyFor`. One log line per session:
    outcome, app (after AUTH only), counts, reply codes.
  - `run(ctx context.Context, getenv func(string) string, stdout io.Writer) error` — addresses from config; loads the
    key pair with `tls.LoadX509KeyPair` before binding (failure refuses to start); logs `"listening"` with `listener`
    `smtp` when on; shuts the SMTP server down with the others.

- [ ] **Step 1: Write the failing tests** (test CA and server certificate generated in the test with `crypto/x509`; a
      raw `net/textproto` client asserts exact codes)

```go
func TestListenerAddressConfig(t *testing.T) // defaults; SMTP_ADDR without TLS files, TLS files without SMTP_ADDR, "9203" → Load error
func TestSTARTTLSRequired(t *testing.T)      // EHLO advertises STARTTLS, not AUTH; MAIL before STARTTLS → 530 5.7.0; AUTH before STARTTLS → 523 5.7.10
func TestAuthOneReply(t *testing.T)          // wrong key, unknown app, non-smtp app, mismatched authzid → 535 5.7.8, identical text
func TestMailBeforeAuth(t *testing.T)        // after STARTTLS, MAIL before AUTH → 530 5.7.0
func TestEnvelopeReplies(t *testing.T)       // MAIL FROM not allowed → 550 5.7.1; malformed RCPT → 553 5.1.3; 11th RCPT → 452 4.5.3;
                                             // app per_minute 2: 3rd RCPT → 452 4.5.3; app per_day 1: 2nd RCPT → 452 4.5.3; DATA over SIZE → 552 5.3.4
func TestSubmitQueues(t *testing.T)          // real store: 250 2.0.0, one message per RCPT, request key "smtp/<hex>", expires_at = now+window
func TestSessionCaps(t *testing.T)           // 9th concurrent session → 451 4.3.2; MAIL of message 11 on one connection → 451 4.3.2
func TestTLS11Refused(t *testing.T)
func TestSMTPLogsCarryNoSecrets(t *testing.T)// no address, subject, Message-ID, key or attempted username
// main_test.go
func TestAddressesFromEnv(t *testing.T)      // ENQUEUE_ADDR/ADMIN_ADDR 127.0.0.1:0 honoured; no smtp listener by default
func TestSMTPOnWithCertificate(t *testing.T) // SMTP_ADDR + test cert files → "listening" smtp; unreadable cert → run() error before any bind
```

Existing `main_test.go` helpers move from `run(…, enqAddr, admAddr)` to env vars; no assertion is removed.

- [ ] **Step 2: Run** `go test ./internal/submission/ ./internal/config/ ./cmd/commsgun-api/` → FAIL.
- [ ] **Step 3: Implement**; `go get github.com/emersion/go-smtp@<newest ≥ 2 weeks old> github.com/emersion/go-sasl@latest`.
- [ ] **Step 4: Run** the API pipeline, then the images + smoke (Task 6 Step 5) → PASS (SMTP off in smoke).
- [ ] **Step 5: Commit** — `feat(api): SMTP submission listener for Authelia (K7)`.

---

### Task 12: Docs, constitutions and the final check

**Files:**
- Modify: `README.md` (Configuration: K3/U2 vars and `COMMSGUN_CONFIG` keys; a short "Accounts" section with the CLI
  verbs and the `docker compose run --rm -T commsgun-admin admin <verb>` shape; Tests: the e2e run needs `openssl` for
  the stub issuer's test CA), `docs/context-constitution.md`
  (CG-01 evidence: account JSON; CG-02 twins: `PasswordMinChars` ↔ `MIN_ADMIN_PASSWORD`, `SessionTokenBytes` ↔
  `SESSION_TOKEN`, `AUTH_TIME_SKEW_MS` ↔ the SSO spec § 3 skew; CG-06: smoke runs the API at `--memory 96m`; CG-08
  evidence: `OIDC_*`, `smtp_submission`, `send_window_seconds`; smoke-strings gotcha), `api/docs/context-constitution.md` (API-02: never edit `schema.sql` or an
  applied migration, add a numbered one; API-05 evidence: the CLI; API-07: admin's `hasher`; API-09: namespace `smtp`),
  `api/docs/context-constitution-findings.md` (migration row → Resolved), `docs/ui-context-constitution.md` (UI-05
  rewritten per spec 12.10; UI-02 `createApi(…, session)`; UI-01 adds `/auth/link`; UI-06 the stub's session gate and
  `stub-issuer.ts`), `docs/superpowers/specs/2026-11-13-commsgun-design.md` (status line: built), the K1/K2/K3/K7 and
  U1/U2/U4 rows (drop "not built" only; values unchanged)

- [ ] **Step 1: Re-read each touched doc against the code** (`grep` every symbol it names).
- [ ] **Step 2: Run everything:** the API pipeline, the UI pipeline, `shellcheck scripts/smoke.sh`, the images + smoke,
      and `grep -n "uses:" .github/workflows/ci.yml | grep -vE "@[0-9a-f]{40}"` (prints nothing; CI is unchanged).
      Record the smoke memory lines.
- [ ] **Step 3: Commit** — `docs: accounts, SSO and SMTP submission (spec 12)`.
- [ ] **Step 4: Report** (CLAUDE.md § Release): image content changed in `api/` (sources, `go.mod`/`go.sum`,
      `Dockerfile`) and `ui/` (sources, `package.json`, `pnpm-lock.yaml`); the edge still needs its bump PR merged and a
      deploy, and `homelab-edge`'s `commsgun.md` must change **before** that bump merges (SSO spec § 13: the first start
      imports the env admin, so SOPS password rotation moves to `admin reset-password`). Do not open a bump PR.

---

## Open questions for the owner

| # | Question | Plan's proposal | Blocks |
|---|---|---|---|
| Q1 | Values the spec leaves open: (a) argon2id salt and key length; (b) the account routes' body cap and its refusal (12.7 lists no 413); (c) a maximum `iss` length; (d) the UI's import retry interval; (e) the SMTP write timeout | **Resolved (owner, 2026-11-27):** the proposal stands: (a) argon2id 16 B salt, 32 B key (RFC 9106); (b) `AccountMaxBodyBytes` 4096 (= `ProbeMaxBodyBytes`), empty 413 like the probe; (c) no `iss` length limit beyond (b); (d) `IMPORT_RETRY_MS` 1000, no upper bound; (e) the SMTP listener reuses `WriteTimeout` 30 s (spec 12.1, 12.6, 12.7, 12.8; Tasks 2, 5, 6, 11) | — |
| Q2 | Spec 12.8 says a resent `Message-ID` with the same content is `250` (duplicate), but `SubmitNamespaced` hashes the whole request including `expires_at`, which the SMTP path derives from `now`, so a later resend hashes differently and answers `idempotency_conflict` (554) | **Resolved (owner, 2026-11-27):** the proposal stands: build the request without `expires_at`; `SubmitWindowed` applies `now + send_window` after hashing; HTTP and probe unchanged (spec 12.8) | — |
| Q3 | Spec 12.8: `AUTH` before STARTTLS → `530 5.7.0`. go-smtp v0.26 answers `523 5.7.10` itself unless `AllowInsecureAuth` is on, which also advertises `AUTH` before TLS (inviting clients to send the key in clear) | **Resolved (owner, 2026-11-27):** keep `AllowInsecureAuth` off; `AUTH` before STARTTLS answers `523 5.7.10` (spec 12.8 row amended); `MAIL` before STARTTLS stays `530 5.7.0` (Task 11) | — |
| Q4 | Gaps in the 12.8 reply table: `MAIL` after STARTTLS but before `AUTH`; the 11th message on one connection; more `RCPT`s than the app's `per_minute`/`per_day` (enqueue answers `too_many_recipients_for_limit`, which the table maps to `451 4.3.0` "defect") | **Resolved (owner, 2026-11-27):** `MAIL` after STARTTLS but before `AUTH` → `530 5.7.0` (RFC 4954); the 11th message on one connection → `451 4.3.2`; an `RCPT` beyond `min(10, per_minute, per_day)` of the app → `452 4.5.3` at `RCPT` (spec 12.8 table; Tasks 10, 11) | — |
| Q5 | "16 characters": Go can count code points, but the UI's `loadEnv` counts UTF-16 units (`v.length`); a non-ASCII `ADMIN_PASSWORD` could pass the UI and fail the import | **Resolved (owner, 2026-11-27):** both count code points (`utf8.RuneCountInString` in Go, `[...v].length` in the UI); the spec says code points (12.1, 12.6, 12.9; Tasks 2, 7) | — |
| Q6 | `openid-client` 6 refuses a non-`https` issuer unless `allowInsecureRequests` is set; the e2e stub issuer is plain HTTP | **Resolved (owner, 2026-11-27; not the proposal):** the e2e stub issuer serves HTTPS from a CA made by `openssl` when the stub starts, trusted by the UI process through `NODE_EXTRA_CA_CERTS`; production `loadEnv` refuses every non-`https:` issuer and the RP never sets `allowInsecureRequests` (Task 8) | — |
| Q7 | Link re-auth: spec 12.5 sends `prompt=login`; SSO spec S17 pairs it with `max_age=1` and a fresh `auth_time` | **Resolved (owner, 2026-11-27; SSO spec § 3 "Linking"):** every link sends `prompt=login` and `max_age=1`; the callback refuses a missing `auth_time` or one before the flow's start minus `AUTH_TIME_SKEW_MS` (30 s), as a failed ID-token check (spec 12.5; Tasks 8, 9) | — |
| Q8 | § 13 is a draft pending owner review before P5; this plan builds what it states | Review § 13 (and its residuals 1–7) before P5 | P5, not a task |
| Q9 | Resolved (2026-11-27, cross-repo check): openid-client 6 exports `setJwksCache`/`getJwksCache`, so the shared conformance case "an ID token on a key set older than 3600 s with the refresh failing" is tested as happybabytracker's plan does: a `deps.jwksCache` seam seeds a cache with a real `uat` of now − 301 s (past the library's 300 s refetch age) and the `jwks_uri` fetch fails → refused; now − 10 s passes. No fake clock | — | Task 8 |

**Readings the plan takes (correct any):** `ReauthMaxFailures` counts every failure on a session and a success does not
reset it; `OIDC_AUTO_PROVISION` counts as set when present, even empty, and an empty `ADMIN_PASSWORD` counts as unset;
`admin unlink <username>` removes all of that user's identities; expired session rows are refused at check, not swept;
the import validates the password before answering `skipped` and never hashes on `skipped`; go-smtp's own error text is
discarded (Rule 4); a message submitted over SMTP stores the SMTP peer's address as `source_ip`; UI copy not fixed by
the spec is the text quoted in the tests above.
