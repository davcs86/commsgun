# Hello-World Probe Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A header button in the dashboard opens a modal that sends a fixed "hello world" email or SMS as app `ops` to one destination.

**Architecture:** The API gains `GET` and `POST /admin/v1/probes` on the admin listener; the POST builds an enqueue request and runs it through the same function as `POST /v1/messages`. The dashboard gains a `/probe` server endpoint that calls it with `ADMIN_API_TOKEN`, and a modal in the layout that talks to `/probe` with `fetch`.

**Tech Stack:** Go 1.x standard library (`api/`); SvelteKit 3, Svelte 5 runes, shadcn-svelte on bits-ui, Vitest, Playwright (`ui/`).

**Spec:** `docs/superpowers/specs/2026-11-13-hello-world-probe-design.md`

## Global Constraints

- No destination, body, subject or secret in a log or an error string; logs carry status, channel and ids only.
- Limits and caps live only in `api/internal/constants/constants.go`.
- `ADMIN_API_TOKEN` is read only in `*.server.ts` / `src/lib/server/`; the browser never receives it.
- The enqueue listener's behaviour (contract K1, K4–K6) does not change; its existing tests pass unmodified.
- Every admin path other than `/admin/v1/probes` stays `GET`-only.
- Fixed content: email subject `commsgun probe`, text `Hello world`; SMS body `Hello world`. App id `ops`.
- UI: built and tested at 390 px first, no horizontal page scroll, shadcn-svelte components and theme tokens, no hard-coded colours.
- Comments state constraints only, at most 2 lines. No test is skipped or weakened.
- Checks: `cd api && gofmt -l . && go vet ./... && go test ./... -race`; `cd ui && pnpm check && pnpm lint && pnpm test && pnpm test:e2e`.

## Review Focus

1. **Double-click or retry after a timeout** with unchanged fields → exactly one message. (Tasks 2, 6)
2. **A cross-origin `POST /probe`** → 403 and no call to the API. (Task 6)
3. **A hostile or overlong destination** (`<script>`, CR/LF, 400 characters) → reported on the field, rendered as text, absent from every log line. (Tasks 2, 3, 6)
4. **Options changed between opening the modal and sending** (provider differs, `ops` removed) → no send; the modal reloads its options. (Tasks 2, 6)
5. **The API is down** when the modal opens or on Send → an inline "unavailable" state with a retry; the page does not crash. (Task 6)

## File Structure

```
api/internal/constants/constants.go     + ProbeMaxBodyBytes
api/internal/enqueue/submit.go          new: Submit, Outcome, Failure, SourceIP (moved out of handler.go)
api/internal/enqueue/handler.go         HTTP only; calls Submit
api/internal/admin/probe.go             new: GET/POST /admin/v1/probes
api/internal/admin/probe_test.go        new
api/internal/admin/handler.go           route + method gate; NewHandler takes an enqueue.Enqueuer
ui/src/lib/probe.ts                     new: types, parseProbeInput, validDestination (pure)
ui/src/lib/probe.test.ts                new
ui/src/lib/server/api.ts                + probeOptions, sendProbe
ui/src/routes/probe/+server.ts          new: GET options, POST send
ui/src/lib/components/ui/dialog/…       new: shadcn-svelte dialog
ui/src/lib/components/ProbeDialog.svelte new
ui/src/routes/+layout.svelte            header button
ui/e2e/stub-api.ts, ui/e2e/probe.spec.ts stub endpoints, new spec
scripts/smoke.sh                        app `ops` in the config, one probe through the UI
```

---

### Task 1: Shared enqueue function

**Files:**
- Create: `api/internal/enqueue/submit.go`
- Modify: `api/internal/enqueue/handler.go`
- Test: `api/internal/enqueue/submit_test.go`; existing `handler_test.go` unmodified

**Interfaces:**
- Produces:

```go
type Outcome struct {
	Result     store.EnqueueResult
	Channel    model.Channel
	Recipients int
	Held       bool // no provider is configured for Channel
}

// Failure is rendered by the caller; Code is the JSON "error" value.
type Failure struct {
	Status     int
	Code       string
	Field      string        // 422 only
	Window     string        // 429 only
	RetryAfter time.Duration // 429 and 503
}

func Submit(ctx context.Context, cfg *config.Config, st Enqueuer, now time.Time,
	app *config.App, key string, body []byte, sourceIP string) (Outcome, *Failure)

func SourceIP(r *http.Request) string // the former sourceIP
```

- [ ] **Step 1: Write failing tests** in `submit_test.go` using `testConfig()` and `fakeStore`: `TestSubmitQueues` (202-equivalent: nil failure, `Recipients == 1`, `Held == false` for email, `Held == true` for SMS); `TestSubmitFailures` table: empty key → 422 `idempotency_key_required` field `Idempotency-Key`; bad key → 422 `invalid_idempotency_key`; bad JSON → 422 `invalid_json`; app `emailonly` + SMS → 403 `channel_not_allowed`; store `RateLimitedError{Window:"day",RetryAfter:90s}` → 429 `rate_limited`, Window `day`, RetryAfter 90s; `store.ErrQueueFull` → 503 `unavailable`; lookup hash mismatch → 409 `idempotency_conflict`; unknown store error → 500 `internal`.
- [ ] **Step 2: Run** `cd api && go test ./internal/enqueue/ -run TestSubmit` — expected: build failure, `Submit` undefined.
- [ ] **Step 3: Implement** `Submit` by moving the body of `handler.serve` from the key check to the store call; `serve` keeps method, body read, 413, authentication, then calls `Submit` and renders `Outcome`/`Failure` exactly as today (including `Retry-After` through `retryAfterSeconds`). An internal store failure is logged by the caller, not by `Submit`.
- [ ] **Step 4: Run** `cd api && gofmt -l . && go vet ./... && go test ./... -race` — expected: all pass, `handler_test.go` untouched.
- [ ] **Step 5: Commit** `refactor(api): share the enqueue path between handlers`.

### Task 2: Probe endpoints on the admin listener

**Files:**
- Create: `api/internal/admin/probe.go`, `api/internal/admin/probe_test.go`
- Modify: `api/internal/admin/handler.go`, `api/internal/constants/constants.go`, `api/cmd/commsgun-api/main.go` (only if the `NewHandler` call needs it)

**Interfaces:**
- Consumes: `enqueue.Submit`, `enqueue.Outcome`, `enqueue.Failure`, `enqueue.SourceIP`, `enqueue.Enqueuer` (Task 1).
- Produces: `constants.ProbeMaxBodyBytes = 4 << 10`; `admin.NewHandler(st Store, cfg, clk, log)` where `Store` is an interface the `*store.Store` satisfies (the existing read methods plus `enqueue.Enqueuer`); the HTTP contract in spec 5.1.

Package constants in `probe.go`: `probeApp = "ops"`, `probeSubject = "commsgun probe"`, `probeText = "Hello world"`, `probeKeyPrefix = "probe:"`.

Check order for POST: body over the cap → 413; JSON (unknown field, wrong type, trailing data) → 422 `invalid_json` / `unknown_field`; channel not `email`/`sms` → 422 `invalid_channel` field `channel`; app `ops` absent → 409 `probe_app_missing`; `provider` key absent → 422 `provider_required` field `provider`; `provider` ≠ configured name (or non-null when none) → 409 `provider_mismatch`; `Idempotency-Key` empty → 422 `idempotency_key_required`; then `Submit` with key `probe:<key>` and the body `{"channel","to":[to],"subject","text"}` (email) or `{"channel","to":[to],"body"}` (SMS).

- [ ] **Step 1: Write failing tests** in `probe_test.go` with a real `store.OpenMemory()` and a config holding app `ops` (email with default sender `Ops <ops@example.com>`, limits 2/min 5/day; SMS 5/50) and provider `email: resend`:
  - `TestProbeOptions`: GET → 200, body equals the spec 5.1 example; with no app `ops` → `{"app":"ops","available":false,"channels":[]}`.
  - `TestProbeSendEmail`: POST `{"channel":"email","to":"me@example.com","provider":"resend"}` key `k1` → 202, `held:false`, one id; `st.GetMessage` shows app `ops`, recipient `me@example.com`, sender `Ops <ops@example.com>`, provider `resend`, `body_bytes == len("commsgun probe")+len("Hello world")`.
  - `TestProbeSendSMSHeld`: `{"channel":"sms","to":"+15551234567","provider":null}` → 202, `held:true`, message `hold_reason` `no_provider`.
  - `TestProbeIdempotent`: same key and body twice → 202 then 200 `duplicate:true`, same id, one message in the store; same key, other `to` → 409 `idempotency_conflict`.
  - `TestProbeKeyIsPrefixed`: after a probe with key `k1`, `st.LookupRequest(ctx, "ops", "probe:k1")` is found and `"k1"` is not.
  - `TestProbeRejects` table: every row of the check order above; `to` of `Me <me@example.com>`, `a@b.c\r\nBcc: x@y.z`, a 400-character address, `5551234567` for SMS → 422 `invalid_recipient` field `to`; app `ops` without SMS → 403 `channel_not_allowed`; third email in a minute → 429 with `Retry-After` and `window:"minute"`.
  - `TestProbeLogHasNoDestination`: capture the logger; after a 202 and a 422 the output contains `request_id` and neither `me@example.com` nor `Hello world`.
  - `TestProbeMethods`: `PUT`/`DELETE /admin/v1/probes` → 405, `Allow: GET, POST`; `POST /admin/v1/stats` → 405, `Allow: GET` (unchanged); unauthenticated POST → 401.
- [ ] **Step 2: Run** `cd api && go test ./internal/admin/` — expected: FAIL (404 / undefined).
- [ ] **Step 3: Implement** `probe.go` and the routing: in `ServeHTTP`, after authorization, dispatch `rest == "probes"` before the `GET`-only gate. Log one line `probe` with `status`, `channel`, `request_id`. Responses reuse `writeJSON`; 202/200 body `{"request_id","message_ids","duplicate","held"}`.
- [ ] **Step 4: Run** `cd api && gofmt -l . && go vet ./... && go test ./... -race` — expected: all pass.
- [ ] **Step 5: Commit** `feat(api): hello-world probe endpoints on the admin listener`.

### Task 3: Probe model and API client

**Files:**
- Create: `ui/src/lib/probe.ts`, `ui/src/lib/probe.test.ts`
- Modify: `ui/src/lib/server/api.ts`, `ui/src/lib/server/api.test.ts`, `ui/src/lib/constants.ts`

**Interfaces:**
- Produces:

```ts
// probe.ts
export type ProbeChannel = { channel: Channel; enabled: boolean; provider: string | null; sender: string | null };
export type ProbeOptions = { app: string; available: boolean; channels: ProbeChannel[] };
export type ProbeInput = { channel: Channel; to: string; provider: string | null; key: string };
export type ProbeResult =
	| { kind: 'queued'; messageId: string; held: boolean }
	| { kind: 'invalid'; field: 'to' | 'from' | 'other' }
	| { kind: 'rate_limited'; window: 'minute' | 'day'; retryAfterSeconds: number }
	| { kind: 'stale' }
	| { kind: 'unavailable' }
	| { kind: 'error' };
export function validDestination(channel: Channel, to: string): boolean;
export function parseProbeInput(body: unknown): ProbeInput | null;

// api.ts additions
probeOptions(): Promise<ApiResult<ProbeOptions>>;
sendProbe(input: ProbeInput, clientAddress: string): Promise<ProbeResult>;
```

Constants: `PROBE_DESTINATION_MAX = 320`, `PROBE_KEY = /^[A-Za-z0-9-]{1,64}$/`.

- [ ] **Step 1: Write failing tests.** `probe.test.ts`: `validDestination('email', 'me@example.com')` true; false for `''`, `'Me <me@example.com>'`, `'a@b'+'\r\n'`, a 400-character address, `'no-at'`; `validDestination('sms', '+15551234567')` true, false for `'5551234567'`, `'+0123'`, `'+1555 123 4567'`. `parseProbeInput` returns null for non-objects, an unknown channel, a non-string `to`, `provider` neither string nor null, a key failing `PROBE_KEY`; returns the trimmed input otherwise. `api.test.ts`: `sendProbe` POSTs to `http://api:9201/admin/v1/probes` with `authorization`, `idempotency-key`, `x-forwarded-for`, `content-type: application/json` and body `{channel,to,provider}`; mapping: 202 → `queued` with the first id and `held`; 200 → `queued`; 422 field `to` → `invalid/to`; 422 `sender_required` → `invalid/from`; other 422 → `invalid/other`; 429 with `Retry-After: 42` and `window:"day"` → `rate_limited/day/42`; 409 `provider_mismatch`, 409 `probe_app_missing`, 403 → `stale`; 409 `idempotency_conflict`, 500 → `error`; 503, network error, timeout → `unavailable`. A failure logs the fixed-shape line with `endpoint: 'probe'` and never the destination.
- [ ] **Step 2: Run** `cd ui && pnpm test` — expected: FAIL, module not found.
- [ ] **Step 3: Implement.** `validDestination` mirrors the API: email is one bare address (`/^[^\s<>"(),;:\\@]+@[^\s<>"(),;:\\@]+$/`, ≤ 320 chars), SMS is `/^\+[1-9][0-9]{1,14}$/`. `sendProbe` uses the same timeout as `call`.
- [ ] **Step 4: Run** `cd ui && pnpm check && pnpm lint && pnpm test` — expected: pass.
- [ ] **Step 5: Commit** `feat(ui): probe model and admin API client methods`.

### Task 4: `/probe` endpoint, dialog and header button

**Files:**
- Create: `ui/src/routes/probe/+server.ts`, `ui/src/lib/components/ProbeDialog.svelte`, `ui/src/lib/components/ui/dialog/*` (`pnpm dlx shadcn-svelte@latest add dialog`; if the registry is unreachable, write the same files by hand on `bits-ui`'s `Dialog`, following `ui/sheet`)
- Modify: `ui/src/routes/+layout.svelte`

**Interfaces:**
- Consumes: Task 3's `probe.ts` and API client.
- Produces: `GET /probe` → 200 `ProbeOptions`, or 503 `{"error":"unavailable"}`; `POST /probe` (JSON `ProbeInput`) → 200 `ProbeResult`; 403 when the `Origin` header is not the configured origin; 400 when `parseProbeInput` returns null or `validDestination` is false is **not** used — an invalid destination returns 200 `{kind:'invalid',field:'to'}` so the modal has one code path.

Dialog behaviour, exactly as spec 5.2. Accessible names the e2e relies on: button `Send probe`; dialog title `Send a hello-world probe`; fields `Message type`, `Provider`, `Destination email` / `Destination phone number`; buttons `Send`, `Cancel`; status region `role="status"` for outcomes. Copy:

- held notice: `No SMS provider is configured. The message will be accepted and held, not delivered.`
- queued: `Probe queued.` / `Probe accepted and held.` followed by a link `View message` to `/messages/<id>`
- invalid `to`: `Enter one valid email address.` / `Enter a number in international format, for example +15551234567.`
- invalid `from`: `App ops has no default sender configured.`
- rate limited: `The ops per-minute limit is reached. Try again in <n> s.` / `The ops daily limit is reached. Try again in <duration>.`
- stale: `The configuration changed. Options were reloaded; check them and send again.`
- unavailable: `The API is unavailable.` with a `Retry` button
- not available: `App ops is not configured, so probes cannot be sent.`

- [ ] **Step 1: Implement `+server.ts`** as specified; the client address comes from `event.getClientAddress()`.
- [ ] **Step 2: Add the dialog component** and `ProbeDialog.svelte` (props: none; owns its open state and the header trigger). Key handling: `crypto.randomUUID()` on open, on any field change and after a `queued` result. Options are fetched on open. After `queued`, call `invalidateAll()`.
- [ ] **Step 3: Mount it** in `+layout.svelte` before the Refresh button, inside the `data.authed` block.
- [ ] **Step 4: Run** `cd ui && pnpm check && pnpm lint && pnpm test` — expected: pass.
- [ ] **Step 5: Commit** `feat(ui): hello-world probe dialog in the header`.

### Task 5: Stub API and end-to-end tests

**Files:**
- Modify: `ui/e2e/stub-api.ts`
- Create: `ui/e2e/probe.spec.ts`

**Interfaces:**
- Produces in the stub: `GET /admin/v1/probes` (spec example; scenario `down` → 503; new control `POST /__probe-mode` with `normal | missing | mismatch` reset by `setScenario`); `POST /admin/v1/probes`: `to` `limited@example.com` → 429 `Retry-After: 42` `window:"minute"`; invalid destination → 422 field `to`; mode `mismatch` → 409 `provider_mismatch`; same key twice → 200 `duplicate:true` with the same id; otherwise 202 with a message id that exists in the stub's list; SMS → `held:true`. Helper `receivedProbes(): Promise<{key: string; body: unknown}[]>` (distinct accepted sends).

- [ ] **Step 1: Write `probe.spec.ts`** (390 px, session cookie as in `overview.spec.ts`):
  - opens from the header on `/` and on `/messages`; fields and the sender line `From: Ops <ops@example.com>` are visible; axe finds no violations with the dialog open; no horizontal overflow.
  - email: fill `me@example.com`, Send → status `Probe queued.`; `View message` navigates to a 200 detail page; `receivedProbes()` has one entry with `{channel:'email',to:'me@example.com',provider:'resend'}`.
  - SMS: choosing SMS shows provider `None configured` and the held notice, clears the destination; `+15551234567` → `Probe accepted and held.`
  - double click on Send → `receivedProbes()` has length 1 (Review Focus 1).
  - `limited@example.com` → `Try again in 42 s`; `<script>alert(1)</script>` and a 400-character value → the field error, no dialog fired, nothing received (Review Focus 3).
  - mode `mismatch` → the stale message and a second `GET /admin/v1/probes` (Review Focus 4); mode `missing` → the not-available text and Send disabled.
  - scenario `down` → `The API is unavailable.`; after `setScenario('normal')`, `Retry` loads the form (Review Focus 5).
  - `request.post('/probe', {headers:{origin:'https://evil.example'}, data:{…}})` with the session cookie → 403 and nothing received (Review Focus 2); without a session → redirect to `/login`.
- [ ] **Step 2: Run** `cd ui && pnpm test:e2e --trace off` — expected: the new spec passes and no existing spec regresses.
- [ ] **Step 3: Commit** `test(ui): probe e2e and stub endpoints`.

### Task 6: Smoke test and documents

**Files:**
- Modify: `scripts/smoke.sh`, `docs/superpowers/specs/2026-11-13-commsgun-design.md`, `docs/superpowers/plans/2026-11-13-commsgun-api.md`, `CLAUDE.md`, `README.md`, `docs/superpowers/specs/2026-11-13-hello-world-probe-design.md` (status line)

- [ ] **Step 1: `smoke.sh`**: add app `ops` (email with a default sender, SMS) to `$CONFIG`; after login, `POST $UI_URL/probe` with the session cookie, `Origin: $ORIGIN` and `{"channel":"email","to":"<RCPT>","provider":"fake","key":"smoke-probe-1"}` → body contains `"kind":"queued"`; the same request again → still `queued` and the messages page lists that recipient once; the same POST with `Origin: https://evil.example` → 403.
- [ ] **Step 2: Documents** as listed in spec 5.4; K2 gains "Amended: `GET, POST /admin/v1/probes`"; the probe spec's status becomes approved. Also record in the probe spec that an absent `provider` key is a 422 `provider_required`.
- [ ] **Step 3: Run** `bash -n scripts/smoke.sh`, and the full smoke when Docker is available.
- [ ] **Step 4: Commit** `docs: record the probe in the spec, contracts and smoke test`.
