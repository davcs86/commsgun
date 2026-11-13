# commsgun API and Worker Implementation Plan (plan 1 of 3)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build the Go service that accepts email/SMS over HTTP, queues them durably in SQLite, sends them through a provider within app and provider limits, tracks bounces, and exposes a read-only admin API.

**Architecture:** One Go binary, two HTTP listeners (enqueue 9200, admin 9201), one worker loop and one poller loop, all sharing a single `*store.Store` that owns SQLite. Time comes from an injected clock and providers from an interface, so every behaviour is testable without a network or a sleep.

**Tech Stack:** Go (go directive `1.24`; builder image newest stable, digest-pinned), standard library `net/http` and `log/slog`, `modernc.org/sqlite` (pure Go), `github.com/google/uuid`. No web framework, no ORM.

**Spec:** `docs/superpowers/specs/2026-11-13-commsgun-design.md` — sections 6 and 8. Read it first.

**Sibling plans:** `2026-11-13-commsgun-ui-release.md` (plan 2) consumes the admin API frozen below; `2026-11-13-commsgun-edge.md` (plan 3) consumes the ports and env vars.

## Global Constraints

- Every limit, duration and cap lives in `api/internal/constants/constants.go`. No literal TTL, cap or limit elsewhere.
- Logs are `slog` JSON lines and carry message ids only — never a key, body, subject, or address.
- Keys and tokens are compared with `crypto/subtle.ConstantTimeCompare`.
- All timestamps are stored as UTC unix milliseconds (`INTEGER`) and serialised as RFC 3339 UTC.
- No goroutine calls `time.Now()` directly; everything takes a `clock.Clock`.
- The process runs as uid 10001 on a read-only root filesystem; it writes only under `DATA_DIR`.
- Comments state constraints only, at most 2 lines.
- No test is skipped, disabled or weakened to get green. Run `go test ./... -race` from `api/`.
- Commit after every task with the trailer lines given in the session's attribution reminder.

## Frozen contracts (do not change; stop and report instead)

| # | Contract | Value |
|---|---|---|
| K1 | Enqueue listener | container port `9200`: `POST /v1/messages`, `GET /healthz`, `GET /healthz/backlog` |
| K2 | Admin listener | container port `9201`: `GET /admin/v1/{stats,messages,messages/{id},facets,overview}`, `GET /healthz`; `Authorization: Bearer <ADMIN_API_TOKEN>`. Amended (`2026-11-13-hello-world-probe-design.md`): `GET, POST /admin/v1/probes`, the only admin path that accepts a write |
| K3 | Env | `COMMSGUN_CONFIG` (JSON, below), `RESEND_API_KEY`, `ADMIN_API_TOKEN`, `DATA_DIR` (default `/data`), `COMMSGUN_ALLOW_FAKE` |
| K4 | Enqueue request/response | spec 6.2, verbatim |
| K5 | Body cap | 262144 bytes |
| K6 | Client address | first address of `X-Forwarded-For` is stored as `source_ip` |

`COMMSGUN_CONFIG`:

```json
{
  "apps": [
    {"id": "xstockstrat", "key": "<≥32 chars>",
     "channels": {
       "email": {"per_minute": 10, "per_day": 80,
                 "senders": ["Alerts <alerts@example.com>"],
                 "default_sender": "Alerts <alerts@example.com>"},
       "sms": {"per_minute": 5, "per_day": 50}}}
  ],
  "providers": {
    "email": {"name": "resend", "per_minute": 60, "per_day": 100},
    "sms": null
  }
}
```

A channel absent from an app's `channels` is not enabled for it (403). `providers.<channel>: null` means "no provider": messages are held.

Admin API JSON (K2), all fields snake_case:

```
GET /admin/v1/stats?from&to&channel&app&state&error_type&recipient&sender
→ {"from","to","bucket_seconds",
   "totals":{"enqueued","sent","delivered","bounced","complained","failed","expired","retries","queued_now"},
   "series":[{"t","enqueued","sent","delivered","bounced","complained","failed","expired","retries"}],
   "by_error_type":[{"error_type","count"}], "by_app":[{"app","count"}], "by_channel":[{"channel","count"}]}

GET /admin/v1/messages?<same filters>&cursor
→ {"items":[Message], "next_cursor": "<opaque>"|null}

GET /admin/v1/messages/{id} → {"message": Message, "attempts":[Attempt]}   (404 when unknown)

GET /admin/v1/facets → {"apps":[...], "senders":[...], "error_types":[...], "channels":["email","sms"], "states":[...]}

GET /admin/v1/overview
→ {"queue_depth", "oldest_waiting_at": ts|null,
   "providers":[{"channel","name": string|null,"minute":{"used","limit"},"day":{"used","limit","resets_at"},
                 "paused_until": ts|null, "pause_reason": string|null, "held": int}]}

Message = {"id","request_id","app","channel","recipient","sender","state","hold_reason","attempts",
           "error_type","provider","provider_message_id","body_bytes",
           "created_at","sent_at","final_at","expires_at","next_attempt_at"}
Attempt = {"n","started_at","duration_ms","outcome","error_type","provider_status","provider_error","message"}
```

`from`/`to` are RFC 3339 instants; default range is the last hour. `by_app` and `by_channel` count enqueued messages; `by_error_type` counts attempts plus message-level error types.

## Review Focus

1. **SQLite is busy or the disk is full during enqueue** → the caller gets 503 with `Retry-After`, never a 500, and no recipient of the request is half-inserted. (Task 3)
2. **Resend answers 2xx with a body that has no `id`, or the connection drops after the request was written** → retryable with the same idempotency key, never recorded as sent without a provider id. (Task 4)
3. **Config changes under queued messages** — an app or a sender is removed from `COMMSGUN_CONFIG` while its messages wait → they still send; the sender was validated at enqueue. (Task 5)
4. **Awkward addresses** — mixed case, plus-addressing, an IDN domain, `from` given with a different display name than the allow-list entry → accepted, deduplicated case-insensitively, sender matched by address only. (Task 3)
5. **Filter text containing `%`, `_` or `'`** in the recipient/sender filters → matched literally, never as a wildcard or SQL. (Task 8)

## File Structure

```
api/
  go.mod, go.sum
  Dockerfile
  cmd/commsgun-api/main.go          wiring, signals, the two servers
  internal/constants/constants.go   every limit and duration
  internal/clock/clock.go           Clock interface, Real, Fake
  internal/config/config.go         parse + validate COMMSGUN_CONFIG and env
  internal/model/model.go           State, Channel, ErrorType, Message, Attempt
  internal/store/store.go           Open, migrations, tx helper
  internal/store/schema.sql         embedded schema
  internal/store/enqueue.go         Enqueue (idempotency, app limits, depth)
  internal/store/dispatch.go        ClaimDue, RecordAttempt, ExpireDue, provider counters and pauses
  internal/store/poll.go            DuePolls, RecordPoll
  internal/store/sweep.go           Sweep
  internal/store/query.go           Stats, ListMessages, GetMessage, Facets, Overview, Backlog
  internal/provider/provider.go     Provider interface and result types
  internal/provider/fake.go         in-memory provider
  internal/provider/resend.go       Resend client
  internal/enqueue/handler.go       POST /v1/messages
  internal/enqueue/validate.go      body validation, address parsing
  internal/worker/worker.go         dispatch loop
  internal/worker/poller.go         bounce polling loop
  internal/admin/handler.go         admin API
  internal/health/health.go         /healthz, /healthz/backlog
```

Tests sit beside each file as `*_test.go`.

---

### Task 1: Module scaffold, constants, clock, model, config

**Files:**
- Create: `api/go.mod`, `api/internal/constants/constants.go`, `api/internal/clock/clock.go`, `api/internal/model/model.go`, `api/internal/config/config.go`
- Test: `api/internal/config/config_test.go`, `api/internal/clock/clock_test.go`

**Interfaces:**
- Produces:
  - `constants`: `MaxBodyBytes = 262144`, `MaxRecipients = 10`, `MaxAttempts = 8`, `BackoffBase = 30*time.Second`, `BackoffMax = time.Hour`, `BackoffJitter = 0.2`, `MaxSendWindow = 24*time.Hour`, `Retention = 30*24*time.Hour`, `EnqueueMaxConcurrent = 32`, `MaxQueueDepth = 10000`, `LeaseDuration = 2*time.Minute`, `SendTimeout = 15*time.Second`, `WorkerTick = time.Second`, `DispatchBatch = 4`, `WorkerFresh = 30*time.Second`, `BacklogThreshold = 15*time.Minute`, `SweepInterval = time.Hour`, `MonthlyQuotaPause = 6*time.Hour`, `MinSecretLen = 32`, `IdempotencyKeyMaxLen = 200`, `SubjectMaxLen = 998`, `SMSBodyMaxLen = 1600`, `AttemptMessageMaxLen = 300`, `AdminPageSize = 50`, `PollSchedule = []time.Duration{1m, 5m, 30m, 2h, 12h, 24h, 48h}` (offsets from `sent_at`).
  - `clock.Clock` interface `{ Now() time.Time }`; `clock.Real{}`; `clock.NewFake(t time.Time) *Fake` with `Advance(d)` and `Set(t)`.
  - `model.Channel` (`"email"`, `"sms"`); `model.State` (`queued`, `sending`, `sent`, `delivered`, `bounced`, `complained`, `failed`, `expired`); `model.ErrorType` (the eleven values in spec 6.4); `model.Message`, `model.Attempt` with the fields of the admin JSON above; `State.Final() bool` (true for delivered, bounced, complained, failed, expired).
  - `config.Load(getenv func(string) string) (*Config, error)`; `Config{Apps map[string]App; Providers map[model.Channel]*ProviderCfg; ResendAPIKey, AdminAPIToken, DataDir string; AllowFake bool}`; `App{ID, Key string; Channels map[model.Channel]ChannelCfg}`; `ChannelCfg{PerMinute, PerDay int; Senders []Sender; DefaultSender *Sender}`; `Sender{Raw, Address string}` (Address lower-cased); `ProviderCfg{Name string; PerMinute, PerDay int}`.
  - `(*Config).Authenticate(appID, bearer string) (*App, bool)` — constant time, same cost for unknown app and wrong key.

- [ ] **Step 1: Write the failing config tests**

```go
func TestLoadRejects(t *testing.T) {
    cases := map[string]string{ // name → mutation applied to a valid config
        "duplicate app id":            `two apps with id "a"`,
        "app id not ^[a-z0-9][a-z0-9-]{0,31}$": `id "Bad_ID"`,
        "key shorter than 32":         `key of 31 chars`,
        "default sender not in senders": ``,
        "email enabled with no senders": ``,
        "non-positive limit":          `per_day 0`,
        "unknown provider name":       `providers.email.name "smtp"`,
        "resend without RESEND_API_KEY": ``,
        "fake without COMMSGUN_ALLOW_FAKE=1": ``,
        "ADMIN_API_TOKEN shorter than 32": ``,
        "invalid JSON":                ``,
    }
    // each case: Load returns a non-nil error whose text names the offending field and never contains a key value
}
func TestLoadValid(t *testing.T)            // DataDir defaults to "/data"; providers.sms == nil is accepted
func TestAuthenticate(t *testing.T)         // right key → app; wrong key, unknown app, empty header → false
```

- [ ] **Step 2: Run to verify they fail** — `cd api && go test ./internal/config/` → build failure (package missing).
- [ ] **Step 3: Implement** the files above. `Authenticate` compares against a fixed dummy key when the app is unknown so both paths do one comparison.
- [ ] **Step 4: Run** `cd api && go vet ./... && go test ./... -race` → PASS.
- [ ] **Step 5: Commit** — `feat(api): scaffold, constants, clock, model, config`.

---

### Task 2: Store — schema and transactional enqueue

**Files:**
- Create: `api/internal/store/store.go`, `schema.sql`, `enqueue.go`
- Test: `api/internal/store/enqueue_test.go`

**Interfaces:**
- Consumes: Task 1 types.
- Produces:
  - `store.Open(dataDir string) (*Store, error)` — file `commsgun.db`, WAL, `busy_timeout=5000`, `foreign_keys=ON`, one writer connection (`SetMaxOpenConns(1)` on the write handle, a separate read-only pool for queries). `store.OpenMemory()` for tests. `(*Store).Close()`.
  - Tables per spec 6.6: `messages`, `requests` (unique `(app, idempotency_key)`), `attempts`, `rate_counters` (primary key `(scope, window_kind, window_start)`), `provider_pauses` (`provider` primary key, `until`, `reason`), `schema_version`.
  - `EnqueueInput{App string; Channel model.Channel; IdempotencyKey string; BodyHash [32]byte; Recipients []string; Sender, Subject, Text, HTML, ReplyTo, SMSBody string; ExpiresAt time.Time; SourceIP string; PerMinute, PerDay int; Provider string; Held bool}`
  - `EnqueueResult{RequestID string; MessageIDs []string; Duplicate bool}`
  - `(*Store).Enqueue(ctx, now time.Time, in EnqueueInput) (EnqueueResult, error)`
  - Errors: `ErrIdempotencyConflict`, `ErrQueueFull`, `*RateLimitedError{Window string /* "minute"|"day" */; RetryAfter time.Duration}`, `ErrBusy` (wraps SQLITE_BUSY/SQLITE_FULL).

- [ ] **Step 1: Write the failing tests**

```go
func TestEnqueueCreatesOneMessagePerRecipient(t *testing.T) // 3 recipients → 3 ids, all state queued, next_attempt_at == now
func TestEnqueueIdempotentRepeat(t *testing.T)   // same app+key+hash → same RequestID and ids, Duplicate true, counters unchanged
func TestEnqueueIdempotencyConflict(t *testing.T)// same app+key, different hash → ErrIdempotencyConflict
func TestEnqueueKeyScopedToApp(t *testing.T)     // same key under another app → a new request
func TestEnqueueMinuteLimit(t *testing.T)        // PerMinute 2: 2 ok, 3rd → RateLimitedError{Window:"minute", RetryAfter: time to next UTC minute}
func TestEnqueueDayLimitAtWindowEdge(t *testing.T) // PerDay 2 at 23:59:59Z: full; at 00:00:00Z next day: accepted
func TestEnqueueLimitIsAllOrNothing(t *testing.T)// PerMinute 2, request with 3 recipients → rejected, 0 rows, counter still 0
func TestEnqueueQueueFull(t *testing.T)          // MaxQueueDepth unfinished rows present → ErrQueueFull
func TestEnqueueHeld(t *testing.T)               // Held true → hold_reason "no_provider", provider NULL
func TestCountersSurviveReopen(t *testing.T)     // file-backed store, close, reopen → limit still enforced
```

- [ ] **Step 2: Run** `go test ./internal/store/ -run Enqueue` → FAIL.
- [ ] **Step 3: Implement.** One `BEGIN IMMEDIATE` transaction does, in order: idempotency lookup, depth check, both counter checks, counter increments, request insert, message inserts. Window starts are the UTC minute and UTC day of `now`. Counter scope string: `app:<id>:<channel>`.
- [ ] **Step 4: Run** `go test ./internal/store/ -race` → PASS.
- [ ] **Step 5: Commit** — `feat(api): store schema and transactional enqueue`.

---

### Task 3: Enqueue HTTP handler

**Files:**
- Create: `api/internal/enqueue/handler.go`, `validate.go`
- Test: `api/internal/enqueue/handler_test.go`, `validate_test.go`

**Interfaces:**
- Consumes: `config.Config`, `store.Enqueue`, `clock.Clock`.
- Produces: `enqueue.NewHandler(cfg *config.Config, st Enqueuer, clk clock.Clock, log *slog.Logger) http.Handler` where `Enqueuer` is the one-method interface over `store.Enqueue`; `enqueue.Validate(app *config.App, raw []byte, now time.Time) (*store.EnqueueInput, *ValidationError)` with `ValidationError{Message, Field string}`.

- [ ] **Step 1: Write the failing tests** (table-driven over `httptest`), each asserting the exact status and JSON body of spec 6.2:

```go
// 202 {"request_id","message_ids":[…],"duplicate":false}; 200 with duplicate:true on repeat
// 401 {"error":"unauthorized"} — identical body and headers for: no X-Commsgun-App, unknown app, no Authorization, wrong key
// 403 {"error":"channel_not_allowed"} — app without the sms channel sends sms
// 409 {"error":"idempotency_conflict"}
// 413 — body of 262145 bytes; a body of exactly 262144 bytes is not 413
// 422 with "field" set, for each of: bad JSON; unknown channel; to empty; to with 11 entries;
//     "Name <a@b.c>" in to; address with CR/LF; sms number not E.164; subject missing; subject with "\n";
//     subject of 999 chars; neither text nor html; from not in the app's senders; sms body empty / 1601 chars;
//     expires_at in the past; expires_at more than 24h ahead; missing Idempotency-Key; key of 201 chars; key with a space
// 429 with Retry-After (integer seconds ≥ 1) and {"error":"rate_limited","window":"minute"}
// 503 with Retry-After when the store returns ErrQueueFull or ErrBusy, and when 33 requests are in flight
// defaults: expires_at absent → now+24h; from absent → the app's default sender
// source_ip is the first X-Forwarded-For entry
```

Review Focus tests owned here:

```go
func TestStoreBusyIs503(t *testing.T)            // Enqueuer returns store.ErrBusy → 503 + Retry-After, body {"error":"unavailable"}
func TestAwkwardAddresses(t *testing.T) {
    // to ["A@Example.com","a@example.com"] → one message, recipient stored as given first
    // "user+tag@example.com" and "user@xn--bcher-kva.example" accepted
    // from "Other Name <alerts@example.com>" accepted when senders has "Alerts <alerts@example.com>"; stored sender is the request's value
}
```

- [ ] **Step 2: Run** `go test ./internal/enqueue/` → FAIL.
- [ ] **Step 3: Implement.** Read at most `MaxBodyBytes+1` bytes. `BodyHash` is SHA-256 of the canonical re-encoded request (so key order and whitespace do not cause a false conflict). A buffered channel of size `EnqueueMaxConcurrent` is the in-flight gate. Address parsing uses `net/mail.ParseAddress` and then rejects any display name for `to`.
- [ ] **Step 4: Run** `go test ./internal/enqueue/ -race` → PASS.
- [ ] **Step 5: Commit** — `feat(api): enqueue endpoint`.

---

### Task 4: Provider interface, fake provider, Resend client

**Files:**
- Create: `api/internal/provider/provider.go`, `fake.go`, `resend.go`
- Test: `api/internal/provider/resend_test.go`, `fake_test.go`

**Interfaces:**
- Produces:

```go
type Outcome int // Accepted, Retryable, Permanent, Throttled
type SendResult struct {
    Outcome       Outcome
    ProviderID    string          // set when Accepted
    ErrorType     model.ErrorType // set when Retryable or Permanent
    Status        int             // provider HTTP status, 0 if none
    ProviderError string          // provider's error name
    Message       string          // short, no addresses
    PauseFor      time.Duration   // Throttled: relative pause (retry-after, MonthlyQuotaPause)
    PauseUntilUTCMidnight bool    // Throttled: daily quota
    PauseReason   string          // "rate_limit" | "daily_quota" | "monthly_quota"
}
type DeliveryStatus int // Pending, Delivered, Bounced, Complained, Rejected
type Provider interface {
    Name() string
    Send(ctx context.Context, m model.Message, body Body) SendResult
}
type StatusChecker interface { Status(ctx context.Context, providerID string) (DeliveryStatus, error) }
type Body struct{ Subject, Text, HTML, ReplyTo, SMSBody string }
func NewResend(apiKey string, hc *http.Client, baseURL string) *Resend   // baseURL "" → https://api.resend.com
func NewFake() *Fake   // Fake.Sent() []model.Message; Fake.Script(results ...SendResult); Fake.SetStatus(id string, s DeliveryStatus)
```

- [ ] **Step 1: Write the failing Resend tests** against an `httptest.Server`:

```go
// request: POST /emails, Authorization "Bearer <key>", Idempotency-Key == message id,
//          JSON {from, to:[recipient], subject, text?, html?, reply_to?}; text/html omitted when empty
// 200 {"id":"x"}                         → Accepted, ProviderID "x"
// 429 rate_limit_exceeded, retry-after 3 → Throttled, PauseFor 3s, reason "rate_limit"
// 429 daily_quota_exceeded               → Throttled, PauseUntilUTCMidnight, reason "daily_quota"
// 429 monthly_quota_exceeded             → Throttled, PauseFor MonthlyQuotaPause, reason "monthly_quota"
// 500, 503, 408                          → Retryable, provider_unavailable
// 409 concurrent_idempotent_requests     → Retryable, provider_unavailable
// client timeout                         → Retryable, timeout
// connection refused / reset             → Retryable, network
// 401, 403 *_api_key                     → Permanent, auth
// 403 validation_error "domain is not verified" → Permanent, sender_not_verified
// 422, 400 validation_error              → Permanent, provider_rejected (invalid_recipient when the message names the `to` field)
// 409 invalid_idempotent_request         → Permanent, provider_rejected
// any other status                       → Permanent, unknown
// Status(): GET /emails/{id}; last_event delivered→Delivered, bounced→Bounced, complained→Complained,
//           failed|suppressed|canceled→Rejected, anything else (sent, queued, delivery_delayed, opened, clicked, unknown)→Pending;
//           non-2xx → error
// Message never contains the recipient address even when the provider echoes it
```

Review Focus tests owned here:

```go
func TestResend2xxWithoutIDIsRetryable(t *testing.T)   // 200 "{}" and 200 "not json" → Retryable, provider_unavailable
func TestResendConnectionDropAfterWriteIsRetryable(t *testing.T) // server hijacks and closes → Retryable, network
```

- [ ] **Step 2: Run** `go test ./internal/provider/` → FAIL.
- [ ] **Step 3: Implement.** Before coding the `last_event` mapping, read Resend's "email events" documentation and add any value it lists that is not in the test above to the test first. The HTTP client is passed in; `SendTimeout` is applied by the caller's context.
- [ ] **Step 4: Run** `go test ./internal/provider/ -race` → PASS.
- [ ] **Step 5: Commit** — `feat(api): provider interface, fake and Resend client`.

---

### Task 5: Worker — claim, limits, send, retry, expire

**Files:**
- Create: `api/internal/store/dispatch.go`, `api/internal/worker/worker.go`
- Test: `api/internal/store/dispatch_test.go`, `api/internal/worker/worker_test.go`

**Interfaces:**
- Consumes: `provider.Provider`, `store.Store`, `clock.Clock`, `config.Config`.
- Produces:
  - `(*Store).ClaimDue(ctx, now, limit int, providers []string) ([]ClaimedMessage, error)` — due, unlocked, state `queued`, `hold_reason IS NULL`, `provider IN providers`, oldest `next_attempt_at` first; sets state `sending` and `locked_until = now+LeaseDuration`. Also reclaims rows whose lease has expired. `ClaimedMessage{model.Message; Body provider.Body}`.
  - `(*Store).ProviderReserve(ctx, now, provider string, perMinute, perDay int) (ok bool, until time.Time, reason string, err error)` — false while a row in `provider_pauses` is in force or a window is full; on true, increments both counters.
  - `(*Store).ProviderRelease(ctx, now, provider string)` — undoes one reservation (used when a send is throttled).
  - `(*Store).PauseProvider(ctx, provider string, until time.Time, reason string)`.
  - `(*Store).RecordAttempt(ctx, now, id string, r provider.SendResult, started time.Time, backoff time.Duration) error` — writes the attempt row and applies the transition and body deletion of spec 6.4 in one transaction.
  - `(*Store).Unclaim(ctx, id string)` — back to `queued` without consuming an attempt.
  - `(*Store).ExpireDue(ctx, now) (int, error)` — `queued` rows past `expires_at` → `expired`, error_type `expired`, bodies deleted.
  - `(*Store).ReleaseHeld(ctx, channel model.Channel, provider string) (int, error)` — clears `no_provider` holds when a provider is configured.
  - `worker.New(st *store.Store, providers map[model.Channel]provider.Provider, cfg *config.Config, clk clock.Clock, log *slog.Logger, rnd func() float64) *Worker`; `(*Worker).Tick(ctx) error` (one pass, used by tests); `(*Worker).Run(ctx)` (ticks every `WorkerTick`); `(*Worker).LastTick() time.Time`.
  - `worker.Backoff(attempt int, rnd func() float64) time.Duration` = `min(30s·2^(attempt-1), 3600s)` × `(1 ± 0.2)`.

- [ ] **Step 1: Write the failing tests**

```go
func TestBackoff(t *testing.T) // rnd 0.5 (no jitter): n=1→30s, 2→60s, 7→1920s, 8→3600s; rnd 0→×0.8, rnd 1→×1.2
func TestSendSuccess(t *testing.T)  // → sent, sent_at set, provider id stored, subject/text/html NULL, body_bytes kept, attempt outcome "accepted"
func TestRetryableThenSuccess(t *testing.T) // Retryable → queued, attempts 1, next_attempt_at = now+backoff; after Advance → sent with 2 attempt rows
func TestAttemptsExhausted(t *testing.T)    // 8 Retryable → failed, error_type of the last attempt, bodies NULL, no 9th send
func TestPermanentFails(t *testing.T)       // Permanent → failed after 1 attempt, bodies NULL
func TestProviderDailyLimitPauses(t *testing.T) // per_day 2, 3 queued: 2 sent, 3rd still queued with attempts 0; at next UTC midnight it sends
func TestProviderMinuteLimit(t *testing.T)  // per_minute 1: second message waits for the next UTC minute, attempts 0
func TestThrottled429PausesWithoutAttempt(t *testing.T) // Throttled daily_quota → provider paused until UTC midnight, message queued, attempts 0, reservation released
func TestExpiry(t *testing.T)               // expires_at passed while paused → expired, never handed to the provider
func TestNoSendAfterDeadline(t *testing.T)  // a claimed message whose expires_at ≤ now is expired, not sent
func TestHeldSMSNotClaimed(t *testing.T)    // sms with no provider stays queued/no_provider; expires at its deadline
func TestCrashBetweenSendAndRecord(t *testing.T) {
    // provider accepts, then the worker "dies" before RecordAttempt (inject via a store wrapper that fails once);
    // after LeaseDuration a new Worker reclaims and calls Send again with the SAME message id;
    // the fake provider, keyed by id like Resend's Idempotency-Key, reports a single delivery
}
func TestLeaseBlocksSecondClaim(t *testing.T) // a row in "sending" with a live lease is not returned by ClaimDue
```

Review Focus test owned here:

```go
func TestQueuedMessageSurvivesConfigRemoval(t *testing.T) // enqueue for app "a" with sender S; build a Worker whose cfg lacks app "a" → the message still sends with sender S
```

- [ ] **Step 2: Run** `go test ./internal/store/ ./internal/worker/` → FAIL.
- [ ] **Step 3: Implement.** A tick does: `ExpireDue`; `ReleaseHeld` for each configured channel; then for each provider up to `DispatchBatch` times: `ProviderReserve` → if false stop for that provider; `ClaimDue(…, 1, …)` → if none, `ProviderRelease` and stop; send with a `SendTimeout` context; on `Throttled` call `ProviderRelease`, `PauseProvider`, `Unclaim`; otherwise `RecordAttempt`. The worker reads limits from `cfg.Providers`, never from the message's app.
- [ ] **Step 4: Run** `go test ./... -race` → PASS.
- [ ] **Step 5: Commit** — `feat(api): dispatch worker with provider limits, retries and expiry`.

---

### Task 6: Bounce poller

**Files:**
- Create: `api/internal/store/poll.go`, `api/internal/worker/poller.go`
- Test: `api/internal/worker/poller_test.go`

**Interfaces:**
- Consumes: `provider.StatusChecker`, Task 5's `RecordAttempt` (which sets `next_poll_at = sent_at + PollSchedule[0]` for a provider that implements `StatusChecker`).
- Produces: `(*Store).DuePolls(ctx, now, limit int) ([]model.Message, error)`; `(*Store).RecordPoll(ctx, now, id string, s provider.DeliveryStatus) error`; `worker.NewPoller(st, checkers map[string]provider.StatusChecker, clk, log) *Poller`; `(*Poller).Tick(ctx) error`; `(*Poller).Run(ctx)`.

- [ ] **Step 1: Write the failing tests**

```go
func TestPollScheduleOffsets(t *testing.T) // polls happen at sent_at + 1m, 5m, 30m, 2h, 12h, 24h, 48h and not between
func TestPollDelivered(t *testing.T)       // → delivered, final_at set, next_poll_at NULL
func TestPollBounced(t *testing.T)         // → bounced, error_type bounced
func TestPollComplained(t *testing.T)      // → complained, error_type complained
func TestPollRejected(t *testing.T)        // → failed, error_type provider_rejected
func TestPollPendingToEnd(t *testing.T)    // Pending 7 times → still sent, next_poll_at NULL, no 8th poll
func TestPollErrorKeepsSchedule(t *testing.T) // Status returns an error → poll count advances, state unchanged
func TestSMSIsNeverPolled(t *testing.T)    // provider without StatusChecker → next_poll_at NULL at sent
```

- [ ] **Step 2: Run** → FAIL. **Step 3: Implement** — at most 5 polls per tick, one tick per second, which stays far under Resend's 10 requests/second. **Step 4: Run** `go test ./... -race` → PASS.
- [ ] **Step 5: Commit** — `feat(api): bounce polling`.

---

### Task 7: Sweep and health

**Files:**
- Create: `api/internal/store/sweep.go`, `api/internal/health/health.go`; add `Backlog` to `api/internal/store/query.go`
- Test: `api/internal/store/sweep_test.go`, `api/internal/health/health_test.go`

**Interfaces:**
- Produces: `(*Store).Sweep(ctx, now) (SweepResult, error)` with `SweepResult{Expired, Messages, Requests, Counters int}`; `(*Store).Backlog(ctx, now) (oldestDue time.Time, ok bool, err error)` — the oldest `queued` row with `hold_reason IS NULL`, `next_attempt_at ≤ now`, whose provider is not paused and has window capacity; `(*Store).Ping(ctx) error`; `health.Handler(st, lastTick func() time.Time, clk) http.Handler` serving `/healthz` and `/healthz/backlog`.

- [ ] **Step 1: Write the failing tests**

```go
func TestSweepDeletesAfter30Days(t *testing.T)  // message created 30d+1s ago: row, its attempts and its request row gone; 29d23h old: kept
func TestSweepExpiresUnfinishedFirst(t *testing.T) // an unfinished row past its deadline is expired (final_at set) before age is judged
func TestSweepDropsStaleCounters(t *testing.T)  // minute counters older than 1h and day counters older than 2d removed
func TestHealthz(t *testing.T)                  // 200 {"status":"ok"}; 503 when Ping fails; 503 when lastTick is older than 30s
func TestBacklog(t *testing.T) {
    // due 14m ago → 200; due 16m ago → 503
    // due 16m ago but provider paused (daily quota) → 200
    // held no_provider for hours → 200
}
```

- [ ] **Step 2–4:** fail, implement (sweep ends with `PRAGMA incremental_vacuum`; the schema sets `auto_vacuum=INCREMENTAL`), pass.
- [ ] **Step 5: Commit** — `feat(api): retention sweep and health endpoints`.

---

### Task 8: Admin API

**Files:**
- Create: `api/internal/store/query.go` (rest of it), `api/internal/admin/handler.go`
- Test: `api/internal/store/query_test.go`, `api/internal/admin/handler_test.go`

**Interfaces:**
- Consumes: the frozen admin JSON above.
- Produces: `store.Filter{From, To time.Time; Channel, App, State, ErrorType, Recipient, Sender string}`; `(*Store).Stats(ctx, f Filter, now) (Stats, error)`; `(*Store).ListMessages(ctx, f Filter, cursor string, limit int) ([]model.Message, string, error)`; `(*Store).GetMessage(ctx, id) (model.Message, []model.Attempt, error)` (`ErrNotFound`); `(*Store).Facets(ctx) (Facets, error)`; `(*Store).Overview(ctx, now, cfg *config.Config) (Overview, error)`; `store.BucketFor(d time.Duration) time.Duration`; `admin.NewHandler(st, cfg, clk, log) http.Handler`.

- [ ] **Step 1: Write the failing tests**

```go
func TestBucketFor(t *testing.T) // ≤1h→1m, ≤6h→5m, ≤24h→15m, ≤7d→1h, ≤14d→6h, else 24h
func TestStatsCountsByEventTime(t *testing.T) {
    // message created 26h ago, sent 26h ago, bounced 10m ago; range = last 1h
    // → totals.enqueued 0, sent 0, bounced 1; by_error_type has bounced:1
}
func TestStatsRetriesAreAttemptsBeyondFirst(t *testing.T) // 3 attempts in range on one message → retries 2
func TestStatsQueuedNowIgnoresRange(t *testing.T)
func TestStatsSeriesIsDense(t *testing.T)   // 1h range → exactly 60 buckets, zero-filled, aligned to UTC minutes
func TestStatsFilters(t *testing.T)         // channel, app, state, error_type each narrow totals and series
func TestListMessagesPaging(t *testing.T)   // 120 rows → 50, 50, 20; newest first; next_cursor null on the last page; stable when rows are inserted between pages
func TestListNeverReturnsBodies(t *testing.T) // response JSON has no subject/text/html/body keys
func TestOverview(t *testing.T)             // day.used/limit, resets_at == next UTC midnight, paused_until + pause_reason, held count for sms with name null
func TestAdminAuth(t *testing.T)            // missing or wrong bearer → 401 on every /admin/v1 route; /healthz needs none
func TestAdminRejectsNonGET(t *testing.T)   // POST → 405
func TestAdminBadParams(t *testing.T)       // from after to, unparseable instant, range over 31 days → 400 {"error":…,"field":…}
```

Review Focus test owned here:

```go
func TestFilterTextIsLiteral(t *testing.T) // recipients "a_b@x.io","aXb@x.io","100%@x.io": filter "a_b" matches only the first; "%" matches only the third; "' OR 1=1 --" matches none and errors nothing
```

- [ ] **Step 2: Run** → FAIL.
- [ ] **Step 3: Implement.** All SQL uses bound parameters; substring filters use `LIKE ? ESCAPE '\'` with `%`, `_` and `\` escaped, compared on lower-cased columns. Queries run on the read-only pool. The cursor is the base64 of `(created_at, id)`.
- [ ] **Step 4: Run** `go test ./... -race` → PASS.
- [ ] **Step 5: Commit** — `feat(api): read-only admin API`.

---

### Task 9: Wiring, log hygiene, image

**Files:**
- Create: `api/cmd/commsgun-api/main.go`, `api/Dockerfile`, `api/.dockerignore`
- Test: `api/cmd/commsgun-api/main_test.go`

**Interfaces:**
- Consumes: everything above.
- Produces: `run(ctx context.Context, getenv func(string) string, stdout io.Writer, enqueueAddr, adminAddr string) error` (so the test can start the whole process on `127.0.0.1:0`); binary `commsgun-api`; an image exposing 9200 and 9201.

- [ ] **Step 1: Write the failing end-to-end test**

```go
func TestEndToEndWithFakeProvider(t *testing.T) {
    // run() with COMMSGUN_ALLOW_FAKE=1 and providers.email.name "fake", DATA_DIR = t.TempDir()
    // POST /v1/messages (2 recipients) → 202; within 5s GET /admin/v1/messages shows both "sent"
    // GET /healthz on both listeners → 200; /admin/v1/* is 404 on the enqueue listener and /v1/messages is 404 on the admin listener
}
func TestLogsCarryNoSecretsOrAddresses(t *testing.T) {
    // capture stdout during the scenario above plus one 401, one 422 and one failed send;
    // assert the log contains none of: the app key, ADMIN_API_TOKEN, any recipient, the sender address, the subject, the text body
}
func TestRefusesToStartOnBadConfig(t *testing.T) // run() returns an error before binding any port
func TestGracefulShutdown(t *testing.T)          // cancel ctx during a slow fake send → run() returns within LeaseDuration and the message is sent or back in queued, never stuck in sending with a future lease beyond LeaseDuration
```

- [ ] **Step 2: Run** → FAIL.
- [ ] **Step 3: Implement `run`.** Both `http.Server`s set `ReadHeaderTimeout` 5 s, `ReadTimeout` 15 s, `WriteTimeout` 30 s. Worker, poller and an hourly sweep (also once at start) run as goroutines tied to `ctx`; SIGTERM/SIGINT cancel it.
- [ ] **Step 4: Write `api/Dockerfile`.** Two stages: a digest-pinned `golang` builder (`CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" ./cmd/commsgun-api`), then digest-pinned `gcr.io/distroless/static-debian12:nonroot`. `USER 10001:10001`, `ENV DATA_DIR=/data`, `VOLUME /data`, `EXPOSE 9200 9201`, labels `org.opencontainers.image.source=https://github.com/davcs86/commsgun` and `licenses=MIT`. The `/data` directory must exist in the image owned by 10001 (create it in the builder stage and `COPY --chown`).
- [ ] **Step 5: Verify**

Run: `cd api && go vet ./... && go test ./... -race && docker build -t commsgun-api:dev . && docker run --rm --read-only -e COMMSGUN_CONFIG='{}' commsgun-api:dev; echo "exit=$?"`
Expected: tests PASS; the container prints one JSON error line naming the config problem and `exit=1`.

- [ ] **Step 6: Commit** — `feat(api): process wiring and container image`.
