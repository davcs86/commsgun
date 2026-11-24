# commsgun — email/SMS delivery hub: design

Status: **approved by the owner** (2026-11-13), including the 24-hour send deadline (6.4). The limits in
section 9 are still to be confirmed before the first deploy. Plans: `docs/superpowers/plans/`.

Amended 2026-11-24 for single sign-on and the Authelia mail path (sections 12 and 13): **specified, not built.** The
shared design is `davcs86/homelab-edge` `docs/superpowers/specs/2026-11-23-authelia-sso-respec.md` (decisions 4–9, 22,
23; § 3 app contract; § 5; § 9; phase P5). Until an image with section 12 ships and the edge sets `AUTH_MODE`, the hub
behaves as sections 1–11 describe, except that the admin's password moves into the API's store on first start (12.6).

Items marked **(call)** are the designer's choices, not owner decisions — correct any of them.
Items marked **(owner)** were decided by the owner in the design conversation.

## 1. Intent

Homelab services need to send email and SMS. Today each one would carry its own provider client,
credentials, retry logic and quota handling (`xstockstrat-notify` already has a SendGrid sender with
no retry and a single fixed recipient). `commsgun` is one hub that owns all of that: a service hands
it a message and forgets about it; the hub queues it durably, respects each provider's limits,
retries, tracks the outcome, and shows the owner what happened.

It is the second custom-code service on the edge and is released exactly like the first,
`webhooks-mcp`: image built by CI here, pinned by digest in `davcs86/homelab-edge`, live after a
manual deploy there.

## 2. Goals, non-goals, anti-goals

**Goals**

- G1. An HTTP enqueue endpoint for email and SMS, reachable only inside the tailnet.
- G2. Subscribers are identified by a header and authenticated by a per-app key; each app has its own
  rate limits.
- G3. Durable queue in SQLite; nothing accepted is lost by a restart or a crash.
- G4. Per-provider rate limits, so the hub never pushes a provider past its quota.
- G5. Email through Resend, including bounce tracking. SMS behind a provider interface with no real
  provider yet; SMS messages are accepted and held so downstream integrations can be built **(owner)**.
- G6. A dashboard, behind a single admin login, with delivery stats filterable by message type, error
  type, address/number, date range and subscriber app, with quick ranges 1h (default), 3h, 6h, 12h,
  24h, 3d, 7d, 14d, 30d.
- G7. History kept for 30 days.

**Non-goals (stage one)**

- A status endpoint or callbacks to subscribers: enqueue is fire-and-forget **(owner)**.
- Dashboard actions (retry, cancel, edit) **(owner)**. Amended: the one exception is the hello-world
  probe, `2026-11-13-hello-world-probe-design.md` **(owner)**. Amended 2026-11-24: plus linking an SSO identity
  to one's own account and logging out (12.5).
- Attachments, cc/bcc, templates, scheduled sends.
- Public ingress of any kind, including provider webhooks. Amended 2026-11-24: the dashboard alone becomes public at
  `commsgun.public.<domain>` behind single sign-on (section 12); enqueue, admin API and SMTP stay private.
- A staging route. Port 8443 is being removed from the edge **(owner)** and the staging VM is denied
  on `*.ts:443`; staging gets access as part of the post-launch `xstockstrat-notify` work, on
  whatever route replaces 8443.
- Connecting `xstockstrat-notify`. That is post-launch **(owner)**, through that repo's SDD pipeline.
- ~~Multiple dashboard users, roles,~~ password change in the UI. Amended 2026-11-24: the API stores users with a
  role (only `admin` exists) and their identities (12.1); users are created, reset, disabled and unlinked by a CLI only.
- OpenTelemetry, nightly backup of the store, inbound email/SMS.

**Anti-goals** — the design has failed if any of these happen, even if everything else works

- A message is sent twice because of a retry, a crash or a duplicate enqueue.
- An accepted message disappears without a recorded final state.
- A secret, a message body or a recipient address appears in a log.
- The enqueue endpoint, the admin API or the SMTP listener is reachable from the internet, or the dashboard is
  reachable from it other than through single sign-on or, in `local`/`both`, the rate-limited password login
  (amended 2026-11-24).
- A disabled user, an unlinked SSO identity or a person outside the IdP's `commsgun` group reaches a dashboard page.
- A password-reset or enrolment email from app `authelia` keeps its body after the link expires (1 hour).
- The hub burns through the Resend free quota (100 emails/day) and keeps hammering the API.

## 3. Constraints found in the code

1. The edge is one `s-1vcpu-2gb-amd` droplet; existing containers are capped at 128–192 MB
   (`homelab-edge/roles/stack/templates/compose.yml.j2`).
2. Every container there uses the `x-hardening` anchor (no new privileges, all capabilities dropped),
   `read_only: true`, and binds ports to `127.0.0.1` only; Caddy is the only entry point.
3. Data must be a **named volume**: the container runs as uid 10001 and a bind mount would be
   root-owned (`homelab-edge/docs/decisions.md`, webhooks-mcp trap).
4. Tailnet-only names are served by the `*.{{ magicdns_base_domain }}` site, which already imports
   `tailnet_only` and `deny_staging_vm` and holds a wildcard certificate — no new certificate or deSEC
   policy is needed for a `*.ts.<domain>` name.
5. Any tailnet peer with a grant on `tag:edge:443` can reach a tailnet-only route (the Proxmox and DB
   hosts have one). A header alone therefore identifies a subscriber but does not authenticate it.
6. `tag:xstockstrat → tag:edge:443` already exists in `policy/policy.hujson`; no policy change is
   needed for the future prod subscriber.
7. Secrets reach containers through an Ansible-rendered `0600` env file read from
   `secrets/edge.sops.yaml`; generated keys are declared in `scripts/init_secrets.py`, and
   `scripts/secrets-keys-test.py` fails CI when a role reads a key that file lacks.
8. Images are pinned by digest in `inventories/prod/group_vars/all/versions.yml` with a row in
   `docs/versions.md`; `scripts/bump_image_pin.py --var <name> --image <ref>` rewrites both.
9. Resend (checked against its API reference on 2026-11-13):
   - `POST https://api.resend.com/emails`, `Authorization: Bearer`, supports an `Idempotency-Key`
     header (≤ 256 chars, remembered 24 h; reuse with a different body is 409).
   - `GET /emails/{id}` returns `last_event`. A *sending-access-only* API key cannot call it
     (`restricted_api_key`), so bounce tracking needs a **full-access key**.
   - 10 requests/second per team. 429 carries `retry-after`.
   - Free plan: 100 emails/day (`daily_quota_exceeded`, resets at midnight UTC) and a monthly quota
     (`monthly_quota_exceeded`). Each recipient counts as one email.
10. `xstockstrat-notify`'s existing webhook queue uses an `Idempotency-Key` header, exponential
    backoff `min(base·2^(n-1), max)` with 8 attempts, 30 s base, 3600 s cap, and retries 408/429/5xx/
    timeouts/network errors. The hub mirrors these so the later integration is unsurprising.

## 4. Approaches considered

**A. Two images: Go API/worker + a separate dashboard server (chosen by the owner).** The Go
container owns the queue, the providers and SQLite. A second container serves the dashboard, owns the
login session, and reads through the Go container's internal admin API. A dashboard crash cannot
touch delivery. Cost: two digests to pin, which must move together.

**B. One image: Go serves a statically exported dashboard.** Smallest footprint (one ~40 MB
container, one digest), but no server-side UI and a Go crash takes the dashboard with it. Rejected by
the owner in favour of isolation.

**C. One container running a Go process and a Node server.** Needs a process supervisor inside a
read-only hardened container; memory cost of A without its isolation. Dropped.

**Dashboard framework.** Next.js was the original ask; the owner then asked for a lighter
alternative and chose **SvelteKit with shadcn-svelte, mobile-first** **(owner)**. SvelteKit's Node
adapter keeps server-side login and data loading at roughly a third of Next.js's memory (estimate,
not measured — see 7.6 for the enforced ceiling). Go-rendered pages with htmx were leaner still but
were not chosen.

**Backend language.** Go **(owner accepted the default)**: small static binary, low idle memory,
straightforward worker concurrency.

**Bounce tracking.** Resend reports bounces by public webhook, which conflicts with tailnet-only.
The hub instead **polls** `GET /emails/{id}` for each sent email until it reaches a final event.
At ≤ 100 emails/day the polling load is negligible.

## 5. Owner decisions

1. Hub and edge deployment only; `xstockstrat-notify` is connected post-launch.
2. Approach A; Go backend; SvelteKit + shadcn-svelte dashboard, mobile-first.
3. Subscriber = `X-Commsgun-App` header **plus** a per-app bearer key; unknown apps are rejected.
4. The app registry and all limits are static configuration in `homelab-edge`, changed by a deploy.
5. Over an app's limit: 429 with `Retry-After` at enqueue. Provider limits never reject; they delay.
6. Limits are per-minute and per-day, for apps and for providers. Resend is on the free tier: 100/day.
7. Email fields: `to` (1–10), `subject`, `text` and/or `html`, optional `reply_to`.
8. The sender domain and the allowed `from` addresses live in `homelab-edge` secrets.
9. `Idempotency-Key` is required; a repeat returns the original result.
10. Retries: 8 attempts, exponential backoff from 30 s to 1 h, optional per-message `expires_at`
    (its 24-hour default and cap are a designer's call, 6.4).
11. SMS is queued (held) until a provider exists.
12. Fire-and-forget: no status endpoint.
13. Bounces are in scope.
14. Message bodies are deleted once a message reaches a final state; recipients and metadata stay
    for the 30 days.
15. The store is not in the nightly backup.
16. Dashboard: stats, charts, message list with per-attempt error detail; no actions. Amended: one
    action, the hello-world probe sent as app `ops`.
17. Login: fixed username `admin`, password from SOPS, session cookie. Times in the browser's
    timezone, UTC as the default. Amended 2026-11-24: users in the API's store, `AUTH_MODE` `local` | `oidc` | `both`,
    server-side sessions; the SOPS password is imported once (section 12).
18. Alerting: Gatus health checks that fail on a stuck queue; no per-message alert.
19. Hostnames: `comms.ts.<domain>` (enqueue) and `commsgun.ts.<domain>` (dashboard). Amended 2026-11-24: the
    dashboard moves to `commsgun.public.<domain>` and its private name is removed (homelab-edge SSO spec § 5).
20. Images are public on GHCR; the owner merges PRs and runs deploys.

## 6. The API and worker (`api/`, image `ghcr.io/davcs86/commsgun-api`)

### 6.1 Process

One Go binary, one process, **two listeners**, mirroring webhooks-mcp's split so a routing mistake
cannot expose the wrong surface:

| Listener | Container port | Serves | Reachable from |
|---|---|---|---|
| Enqueue | 9200 | `POST /v1/messages`, `GET /healthz`, `GET /healthz/backlog` | Caddy (tailnet-only route), Gatus |
| Admin | 9201 | `GET /admin/v1/*`, `POST /admin/v1/probes`, `GET /healthz`; the account routes of 12.7 (amended) | the dashboard container only (compose network; never published to the host) |
| SMTP submission (amended 2026-11-24) | 9203 | ESMTP with STARTTLS (12.8); off unless `SMTP_ADDR` is set | Authelia only (network `authelia-smtp`) |

SQLite through a pure-Go driver (`modernc.org/sqlite`), so the binary is static and the runtime
image is distroless **(call)**. One process owns the database; the dashboard never opens it. Amended 2026-11-24: the
admin CLI of 12.6 is a short-lived second process on the same volume (`docker compose run --rm`); it writes through the
same single-writer shape (API-03) and the running process treats its lock as `ErrBusy`.

### 6.2 Enqueue contract

```
POST /v1/messages
X-Commsgun-App: <app id>
Authorization: Bearer <that app's key>
Idempotency-Key: <1–200 chars, [A-Za-z0-9._:-]>
Content-Type: application/json
```

Email:

```json
{
  "channel": "email",
  "to": ["a@example.com"],
  "from": "Alerts <alerts@example.com>",
  "subject": "…",
  "text": "…",
  "html": "…",
  "reply_to": "ops@example.com",
  "expires_at": "2026-11-13T12:05:00Z"
}
```

SMS:

```json
{ "channel": "sms", "to": ["+15551234567"], "body": "…", "expires_at": "…" }
```

Rules:

- `to`: 1–10 entries, deduplicated case-insensitively. Email addresses are validated strictly
  (single address, no display name, no CR/LF); SMS numbers are E.164.
- **One message per recipient (call).** A request with N recipients creates N messages, each sent to
  the provider on its own. Reasons: per-recipient outcome and bounce status, recipients never see
  each other, and it matches how Resend counts quota. Rate limits count messages, not requests.
- Email needs `subject` (≤ 998 chars, no CR/LF) and at least one of `text`/`html`.
- `from` is optional. If present it must match, by address, an entry in the app's allowed senders;
  if absent the app's default sender is used. SMS has no `from` in the request; the sender number is
  provider configuration.
- SMS `body`: 1–1600 characters **(call)**.
- `expires_at` is optional, RFC 3339, must be in the future and at most `MAX_SEND_WINDOW` = 24 hours
  ahead; when absent it defaults to 24 hours after enqueue **(call — see 6.4 "Send deadline")**. A
  message not accepted by the provider by then becomes `expired` and is never sent.
- Body at most `MAX_BODY_BYTES` = 256 KiB; Caddy's cap on the route equals it.
- Idempotency is scoped to the app. Same key and same request body → the original response with
  `duplicate: true`. Same key, different body → 409. Keys are remembered for the full 30 days.
- A request is all-or-nothing: either every recipient is queued or none is.

| Result | Status | Body |
|---|---|---|
| Queued | 202 | `{"request_id": "...", "message_ids": ["..."], "duplicate": false}` |
| Idempotent repeat | 200 | same ids, `"duplicate": true` |
| Missing/unknown app, wrong key | 401 | `{"error": "unauthorized"}` (same body for every cause) |
| Channel not enabled for this app | 403 | `{"error": "channel_not_allowed"}` |
| Key reused with a different body | 409 | `{"error": "idempotency_conflict"}` |
| Too large | 413 | |
| Invalid body, sender not allowed, bad recipient | 422 | `{"error": "...", "field": "..."}` |
| Unknown request field (e.g. a misspelt `expire_at`, or `cc`) | 422 | `{"error": "unknown_field", "field": "<name>"}` |
| App over its per-minute or per-day limit | 429 | `Retry-After: <seconds>`, `{"error": "rate_limited", "window": "minute|day"}` |
| More than `ENQUEUE_MAX_CONCURRENT` (32) requests in flight, or more than `MAX_QUEUE_DEPTH` (10 000) unfinished messages | 503 | `Retry-After`; the subscriber retries with the same key |

Keys are compared in constant time. The message ids are returned for the subscriber's own logs;
there is no endpoint to look them up **(owner: fire-and-forget)**.

### 6.3 App rate limits

Per app, per channel: `per_minute` and `per_day`. Fixed windows (the UTC minute, the UTC day),
counted in messages, stored in SQLite so a restart does not reset them **(call)**. A request that
would cross either limit is rejected whole. An idempotent repeat costs nothing.

### 6.4 Message lifecycle

```
queued ──► sending ──► sent ──► delivered        (email, learned by polling)
   │           │          ├───► bounced
   │           │          ├───► complained
   │           │          └───► failed (provider reports failed/suppressed after accepting)
   │           ├──► queued (retryable error; attempt recorded; next_attempt_at pushed out)
   │           └──► failed (permanent error, or attempts exhausted)
   └──► expired (expires_at passed before the provider accepted it)
```

- `sent` means the provider accepted the message. For SMS, and for an email whose polling window
  ends without a final event, `sent` is where it stays.
- **Bodies** (`subject`, `text`, `html`, SMS `body`) are deleted in the same transaction that moves a
  message to `sent`, `failed` or `expired` — the earliest point the hub no longer needs them. Sizes
  are kept. **(call: the subject is treated as body, since it can carry a one-time code.)**
- **Claiming.** The worker claims due messages by setting a lease (`locked_until`). A crash leaves
  the lease to expire and the message is retried. The provider call carries the message id as its
  `Idempotency-Key`, so a crash between "provider accepted" and "recorded as sent" does not send
  twice. Resend remembers a key for 24 h from the first call, and the send deadline below guarantees
  no call is made later than 24 h minus `IDEMPOTENCY_SAFETY_MARGIN` after enqueue, so every retry falls
  inside that memory.
- **Send deadline (call).** Every message has one: `expires_at`, defaulting to and capped at 24 hours
  after enqueue. **(Amended in the final review:)** the worker never hands a message to the provider at
  or after `created_at + 24 h − IDEMPOTENCY_SAFETY_MARGIN` (5 minutes); such a message is `expired`
  (error type `expired`) instead, even when its `expires_at` is later. The enqueue contract is
  unchanged: `expires_at` may still be up to 24 hours ahead. It bounds three things at once: retries
  stay inside the provider's idempotency memory (above); a message delayed by a quota pause is dropped rather than sent stale; and held SMS
  expire instead of flushing weeks-old texts on the day a provider is configured. With polling ending
  48 h after `sent`, every message is final within about 72 h, so the 30-day sweep never deletes an
  unfinished one.
- **Retries.** Up to `MAX_ATTEMPTS` = 8; delay `min(30 s · 2^(n-1), 3600 s)` with ±20 % jitter
  **(call: jitter)**. Retryable: timeouts, network errors, 408, 5xx, Resend
  `concurrent_idempotent_requests`. Everything else is permanent → `failed`.
- **Throttling is not an attempt.** When the provider's own limit (6.5) or a provider 429 holds a
  message back, no attempt is consumed; the message waits.
- **Provider authentication failure is not an attempt (amended in the final review).** A Resend 401, or
  a 403 whose error name ends in `_api_key`, pauses the provider for `AUTH_PAUSE` = 10 minutes with pause
  reason `auth` and returns the message to the queue unconsumed, so a rotated or mistyped key holds the
  queue instead of failing it; messages still expire at their send deadline. `sender_not_verified`
  stays permanent.
- **Held SMS.** With no SMS provider configured, SMS messages stay `queued` with hold reason
  `no_provider` until one is configured or their send deadline passes (then `expired`).
- Order: oldest `next_attempt_at` first. No priorities **(call)**.

Every provider call writes an **attempt** row: message, attempt number, start, duration, outcome
(`accepted`, `retryable`, `permanent`), a normalised `error_type`, the provider's status code and
error name, and a message truncated to 300 characters with addresses stripped.

`error_type` values: `timeout`, `network`, `provider_unavailable`, `provider_rejected`,
`invalid_recipient`, `sender_not_verified`, `auth`, `bounced`, `complained`, `expired`, `unknown`.
The last three before `unknown` are set on the message itself rather than on an attempt.

### 6.5 Providers and their limits

A provider is an interface: `Send(message) → accepted(provider_id) | retryable | permanent |
throttled(until)`, plus optional `Status(provider_id) → pending | delivered | bounced | complained |
failed`.

| Provider | Channel | Stage one |
|---|---|---|
| `resend` | email | real |
| `fake` | email, sms | tests and the image smoke test only; records sends in memory. The process refuses to start with `fake` unless `COMMSGUN_ALLOW_FAKE=1` |
| — | sms | none configured; SMS is held |

Each configured provider has `per_minute` and `per_day` limits **(owner)**, enforced by the worker
before every send: durable fixed-window counters, the day being the UTC day (matches Resend's reset).
At the limit the provider is paused until the window turns over (a `provider_pauses` row with reason
`window_minute` or `window_day`; a released reservation or a restart clears it). Independently, a 429 from Resend
pauses the provider for `retry-after` (`rate_limit_exceeded`), until the next UTC midnight
(`daily_quota_exceeded`), or for 6 hours and re-probe (`monthly_quota_exceeded`) **(call)**. Status
polls are spaced to stay far under the 10 requests/second team limit.

**Bounce polling (call: the schedule).** After `sent`, an email is polled at 1 min, 5 min, 30 min,
2 h, 12 h, 24 h and 48 h. `delivered`, `bounced` and `complained` end polling and set that state;
`failed` or `suppressed` end it with state `failed`, `error_type` `provider_rejected`. After the last
poll the message stays `sent`. The builder confirms the full
`last_event` vocabulary against Resend's documentation and maps any other value to "keep polling".

### 6.6 Storage

SQLite, WAL mode, on a volume at `/data`.

- `messages`: id (UUID), request id, app, channel, recipient, sender, state, hold reason, body
  columns (nullable once deleted), body sizes, attempts used, `next_attempt_at`, `locked_until`,
  `expires_at`, provider, provider message id, final `error_type`, `next_poll_at`, poll count,
  created/updated/sent/final timestamps, source IP (first address of `X-Forwarded-For`).
- `requests`: app, idempotency key (unique together), body hash, request id, created at.
- `attempts`: as in 6.4.
- `rate_counters`: scope (app+channel, or provider), window kind, window start, count.

Indexes serve the two hot paths: the worker's "due and unlocked" scan and the dashboard's
"by created time, filtered" scans.

A sweep runs at start and hourly: deletes messages, their attempts and their request rows older than
`RETENTION` = 30 days (by creation time), drops stale counters, and returns freed pages. As a
safety net it first moves any still-unfinished message past its deadline to `expired`.

All limits and durations live in one constants file; no literal TTL, cap or limit elsewhere.

### 6.7 Admin API (internal)

Every request needs `Authorization: Bearer <ADMIN_API_TOKEN>`. Read-only, except `/admin/v1/probes`
(amended; contract in `2026-11-13-hello-world-probe-design.md` section 5.1).

Common filters: `from`/`to` (UTC instants), `channel`, `app`, `state`, `error_type`, `recipient`,
`sender` (the last two: case-insensitive substring match).

| Endpoint | Returns |
|---|---|
| `GET /admin/v1/stats` | Totals for the range — enqueued, sent, delivered, bounced, complained, failed, expired, queued now, retries (attempts beyond the first) — plus a time series of the same counts and breakdowns by error type, app and channel |
| `GET /admin/v1/messages` | Cursor-paged list, newest first, 50 per page; never includes bodies |
| `GET /admin/v1/messages/{id}` | One message with its attempts |
| `GET /admin/v1/facets` | The apps, senders and error types that exist, for the filter controls |
| `GET /admin/v1/probes` | Whether app `ops` exists, its channels, the configured provider and the default sender |
| `POST /admin/v1/probes` | Queues one fixed "hello world" message as app `ops` through the enqueue path |
| `GET /admin/v1/overview` | Queue depth, oldest waiting message, each provider's state, usage against its limits, and any pause with its reason and end time |

The requirement's "sending address/number" is read as the **sender**; the recipient filter is added
because bounces are investigated by recipient **(call)**.

The stats window is snapped to whole buckets (end rounded up, span rounded up), and the response's
`from`/`to` are the snapped values — so "last hour" is always exactly 60 one-minute buckets.

**Stats count events by when they happened (call).** In a range, "enqueued" counts messages created
in it, "sent" those accepted by the provider in it, each final state those that reached it in it, and
retries/errors the attempts made in it — so "last 1h" answers "what happened in the last hour", and a
bounce on yesterday's email shows up when it is learned. "Queued now" is the current figure and
ignores the range. The message list filters by creation time. Bucket size follows the range: 1 min for
1h, 5 min up to 6h, 15 min up to 24h, 1 h up to 7d, 6 h for 14d, 1 day for 30d **(call)**.
Aggregation runs directly on the tables; at this volume (≤ a few thousand rows) no rollup is needed.

### 6.8 Health

- `GET /healthz`: 200 when the database answers and the worker loop has ticked within 30 s.
- `GET /healthz/backlog`: 503 when the oldest message that *could* be sent now (not held for
  `no_provider`, not behind a provider pause) has been due for more than 15 minutes **(call)**.
  A quota pause is visible on the dashboard, not an alert.

### 6.9 Configuration

| Env var | Meaning |
|---|---|
| `COMMSGUN_CONFIG` | JSON: the app registry (id, key, per channel: enabled, `per_minute`, `per_day`, allowed senders, default sender) and the providers (which provider serves each channel, `per_minute`, `per_day`) |
| `RESEND_API_KEY` | Full-access Resend key (secret) |
| `ADMIN_API_TOKEN` | Shared with the dashboard container (secret) |
| `DATA_DIR` | Defaults to `/data` |

The process refuses to start on an invalid config: duplicate app ids, a key shorter than 32
characters, a default sender outside the allow-list, a channel whose provider is unknown, or an
email provider without its key. Logs are JSON lines and carry message ids only — never keys, bodies,
subjects or addresses.

## 7. The dashboard (`ui/`, image `ghcr.io/davcs86/commsgun-ui`)

### 7.1 Stack

SvelteKit 3 (Svelte 5) with the Node adapter, shadcn-svelte components on Tailwind 4, Node 24.
Mobile-first: every page is designed at phone width first and widens to a table/grid layout.

### 7.2 Login

Amended 2026-11-24: replaced by section 12 once built. Until then, as below.

- One user, `admin`. The password is the `ADMIN_PASSWORD` env var, compared in constant time.
- Success sets a signed session cookie (`HttpOnly`, `Secure`, `SameSite=Strict`), valid 12 hours
  **(call)**. Logout clears it. **(Amended in the final review:)** the signing key is
  `HMAC-SHA256(SESSION_SECRET, "commsgun-session-key\0" + ADMIN_PASSWORD)`, computed once at start, so
  changing the password or the secret in SOPS and redeploying invalidates every session.
- Failed logins are limited to 5 per minute per client address, in memory **(call)**.
- A server hook guards every route except `/login`, `/logout` and `/healthz`.
- The browser only ever talks to the SvelteKit server; server-side `load` functions call the admin
  API with `ADMIN_API_TOKEN`. The token and the Go container are never exposed to the browser.

### 7.3 Pages

| Route | Content |
|---|---|
| `/login` | Username and password |
| `/` | Overview: quick ranges, filters, totals, time-series chart, breakdowns, provider usage |
| `/messages` | The message list under the same filters |
| `/messages/[id]` | One message: metadata, state timeline, attempts with error detail. No body |
| `/probe` | JSON endpoint behind the session for the header's "Send probe" dialog (amended) |
| `/healthz` | Unauthenticated liveness for Gatus |

Phone-width sketch of the overview:

```
┌──────────────────────────────────┐
│ commsgun                 ⟳  ⎋    │
├──────────────────────────────────┤
│ [1h] 3h 6h 12h 24h 3d 7d 14d 30d │  ← scrolls sideways; 1h is the default
│ [ Filters · 2 ]  [ Custom range ]│  ← filters open in a bottom sheet
├──────────────────────────────────┤
│ Delivered   Sent      Queued     │
│   41        44          0        │
│ Bounced     Failed    Retries    │
│    1         0          4        │
├──────────────────────────────────┤
│ ▂▃▅▇▅▃▂▁▂▃  stacked by outcome   │
├──────────────────────────────────┤
│ Resend   47 / 100 today          │
│ ████████░░░░░░░░   resets 00:00Z │
│ SMS      no provider · 2 held    │
├──────────────────────────────────┤
│ Errors by type                   │
│ timeout ............ 3           │
│ bounced ............ 1           │
├──────────────────────────────────┤
│ By app                           │
│ xstockstrat ........ 44          │
└──────────────────────────────────┘
```

- Filters: message type, subscriber app, state, error type, sender, recipient, date range.
- All filter and range state lives in the URL query, so a view can be bookmarked and the back button
  works. A quick range is relative to "now"; a custom range is two instants.
- The overview refreshes itself every 30 seconds while the tab is visible **(call)**.
- Messages are cards on a phone and a table from tablet width up; tapping one opens its detail.
- Charts use shadcn-svelte's chart components; counts are also given as numbers, never by colour
  alone. Light and dark follow the system setting.
- **(Amended in the final review.)** The chart stacks only the five final outcomes (delivered,
  complained, bounced, failed, expired), each counted by when it happened. `sent` counts every message
  the provider accepted in the range, whatever happened to it later, so it overlaps those outcomes and
  is not stacked; the **Sent** tile stays, with the note "Sent = accepted by the provider; Delivered =
  confirmed later".

### 7.4 Time

The server renders times in UTC. In the browser they are re-formatted in the browser's timezone with
the zone shown; the custom range picker works in that zone and puts UTC instants in the URL. Chart
buckets are fixed UTC intervals, labelled in local time.

### 7.5 Configuration

| Env var | Meaning |
|---|---|
| `ADMIN_PASSWORD` | The admin password (secret). The server refuses to start if it is shorter than 16 characters |
| `SESSION_SECRET` | Cookie signing key (secret, ≥ 32 characters) |
| `ADMIN_API_TOKEN` | Shared with the API container (secret) |
| `API_BASE_URL` | `http://commsgun-api:9201` on the edge. The server refuses to start unless it is an `http`/`https` URL |
| `ORIGIN` | The dashboard's public origin, `https://commsgun.ts.<domain>`. The server refuses to start unless it is an `http`/`https` URL, and rejects (400) any request whose derived origin differs, except `/healthz` |
| `PROTOCOL_HEADER`, `HOST_HEADER`, `ADDRESS_HEADER`, `XFF_DEPTH` | Set **in the image** to `x-forwarded-proto`, `x-forwarded-host`, `x-forwarded-for`, `1`: the Node adapter derives the request origin and the client address (the login limiter's key) from the proxy's headers, not from `ORIGIN`. Safe only because the port is bound to `127.0.0.1` behind Caddy |
| `PORT` | 9202 |

### 7.6 Memory ceiling

The edge sets `mem_limit` on both containers (section 9). The image smoke test starts each container,
exercises it, and fails if resident memory exceeds 60 MB (API) or 140 MB (dashboard) **(call)** — the
SvelteKit figure is an estimate until this test measures it.

## 8. Build and release (this repo)

Layout: `api/` (Go module), `ui/` (SvelteKit app), one `Dockerfile` in each, `scripts/smoke.sh`,
`.github/workflows/ci.yml`, `CLAUDE.md`, `README.md`.

- Both Dockerfiles pin their base images by digest, run as uid 10001, and work with a read-only root
  filesystem (only `/data` writable in the API; nothing in the dashboard).
- CI on every PR and on `main`: Go vet, lint and tests; dashboard type-check, lint and unit tests;
  build both images; `scripts/smoke.sh` runs them together with the `fake` provider — enqueue a
  message, see it reach `sent`, log in, load the overview, check the memory ceilings.
- On `main` only: push `ghcr.io/davcs86/commsgun-api:<sha>` and `ghcr.io/davcs86/commsgun-ui:<sha>`,
  move the tag `main` on each, label both with `org.opencontainers.image.revision`, and print both
  digests in the job summary.
- Merging to `main` deploys nothing.

Tests the builder must include: idempotent repeat and conflict; app limit at the window edge; the
provider daily limit pausing the worker without consuming attempts; crash between send and record
(no second send); expiry; body deletion at each final state; the 30-day sweep; a 401 that does not
reveal which part was wrong; every Resend error class mapped as section 6.4 says; bounce polling to
each final state; logs contain no address, subject or key.

## 9. Deployment (`davcs86/homelab-edge`)

Everything here follows that repo's `CLAUDE.md` (Ansible only, pin everything, `no_log` on secrets,
docs updated in the same PR).

**Compose** (`roles/stack/templates/compose.yml.j2`): two services under the hardening anchor, both
`read_only`.

| Service | Image var | Ports | Volume | `mem_limit` |
|---|---|---|---|---|
| `commsgun-api` | `commsgun_api_image` | `127.0.0.1:9200:9200` (9201 not published) | `commsgun-data:/data` | 96m **(call)** |
| `commsgun-ui` | `commsgun_ui_image` | `127.0.0.1:9202:9202` | none | 192m **(call)** |

**Caddy** (`Caddyfile.j2`): two host matchers inside the existing `*.{{ magicdns_base_domain }}` 443
site, so both inherit `tailnet_only` and `deny_staging_vm`:

- `comms.ts.<domain>` → `127.0.0.1:9200`, body cap equal to the hub's 256 KiB.
- `commsgun.ts.<domain>` → `127.0.0.1:9202`.

Nothing is added to the 8443 site or to `edge_sites`. `scripts/caddy-routing-test.sh` gains cases:
both names abort from outside the tailnet, answer from inside, are denied to the staging VM, and the
enqueue name enforces the body cap.

**Policy**: no change. `tag:xstockstrat → tag:edge:443` already exists; members reach the dashboard
through their existing grant.

**Secrets** (`secrets/edge.sops.yaml`, declared in `scripts/init_secrets.py`, documented in
`secrets/README.md`):

| Key | Source |
|---|---|
| `commsgun_admin_password` | generated (32 URL-safe chars); the owner reads it with `sops decrypt` or replaces it with `sops set` **(call)** |
| `commsgun_session_secret`, `commsgun_admin_api_token` | generated |
| `commsgun_app_key_<app>` | generated, one flat key per registered app (the generator creates flat keys only) |
| `resend_api_key` | owner-provided, full access |
| `commsgun_senders` | owner-provided: per app, the allowed `from` addresses and the default **(owner)** |

**Inventory** (`inventories/prod/group_vars/all/main.yml`): hostnames, ports, body cap, the app
registry (ids, enabled channels, limits) and the provider limits. Proposed starting values — **every
number here is a call for the owner to confirm before the first deploy**:

| Scope | per minute | per day |
|---|---|---|
| Provider `resend` | 60 | 100 (the free-tier quota) |
| App `xstockstrat` (email) | 10 | 80 |
| App `ops` (email) — the owner's manual test identity | 2 | 5 |
| SMS, any app | 5 | 50 (held anyway until a provider exists) |

`xstockstrat` is registered now so the post-launch integration needs no edge change beyond handing
over its key **(call)**. The role renders `COMMSGUN_CONFIG` from the inventory registry merged with
the SOPS keys and senders, into a `0600` env file per container.

**Monitoring** (`gatus-config.yaml.j2`): `commsgun-api` (`/healthz`), `commsgun-backlog`
(`/healthz/backlog`), `commsgun-ui` (`/healthz`), all in the existing `edge` group with ntfy alerts.

**Pins and bumps**: `commsgun_api_image` and `commsgun_ui_image` in `versions.yml`, with rows in
`docs/versions.md`. A `bump-commsgun` workflow, modelled on `bump-webhooks-mcp`, pins **both** in one
branch (`bump/commsgun`) and one PR, and refuses to propose a pair whose revision labels differ, so
the API and dashboard never ship from different commits **(call)**.

**Docs**: one entry in `docs/decisions.md` (enqueue is authenticated but must stay tailnet-only; the
admin port is never published; store not backed up), the Context Guide row in `CLAUDE.md`, and a
short runbook: add a subscriber app, rotate an app key, change limits, configure an SMS provider.

## 10. Order of work

1. **commsgun PR**: API, dashboard, CI, docs. Owner merges; CI publishes both images. The owner
   makes the two GHCR packages public once (as was done for webhooks-mcp).
2. **homelab-edge PR**: compose, Caddy, secrets declarations, inventory, Gatus, bump workflow, docs,
   with both digests pinned. Owner prerequisites, done by hand outside Ansible: the sender domain is
   verified in Resend (its DKIM/SPF records added at the domain's DNS host) and a full-access API key
   exists. Owner then adds `resend_api_key` and `commsgun_senders` to SOPS, confirms the limits
   table, merges, runs deploy.
3. **Verification on the edge**, by the owner with the `ops` app: one email to their own address
   reaches `delivered`; one to Resend's bounce test address reaches `bounced`; one SMS shows as held;
   a sixth email in a day gets 429; both names are unreachable from outside the tailnet.
4. **Post-launch**: `xstockstrat-notify` integration through xstockstrat's SDD pipeline, including
   staging access on whatever route replaces 8443.

## 11. Left to the builder

- Go package layout, HTTP router, migration mechanism for the SQLite schema, lint configuration.
- Exact SQL and indexes, provided the two hot paths in 6.6 are indexed.
- Component composition and visual detail within shadcn-svelte's defaults, provided the overview
  matches the sketch's content and order at phone width.
- Exact pinned versions of Go, Node, SvelteKit, shadcn-svelte and base images (newest stable, at
  least two weeks old, pinned by digest/lockfile).
- The Resend `last_event` vocabulary mapping (6.5), confirmed against current documentation.

## 12. Single sign-on, accounts and the Authelia mail path (amended 2026-11-24, not built)

Implements the shared app contract (homelab-edge SSO spec § 3) for commsgun. commsgun stays a provider-neutral OIDC
client: nothing here names Authelia, and a fork runs with `AUTH_MODE=local` and no IdP.

### 12.1 Store

A forward-only migration mechanism lands first (API-02: today's `initSchema` refuses any schema change); migrations are
embedded, applied at start, never reverted, so an image is never rolled back past one **(call)**. New tables:

| Table | Columns |
|---|---|
| `users` | id (UUID), `username` (3–32 of `a-z0-9._-`, unique, permanent, never reused), `role` (`admin`, the only value), `password_hash` (argon2id), `disabled`, created/updated |
| `identities` | `user_id`, `issuer`, `subject`, created; unique on `(issuer, subject)`, at most one per user and issuer **(call)** |
| `sessions` | SHA-256 of a 32-byte random token, `user_id`, `kind` (`local` \| `oidc`), created, `expires_at`, `reauth_at` (12.5) |

Every user keeps a local password (break-glass). Passwords: at least 16 characters (the `ADMIN_PASSWORD` minimum,
CG-02), argon2id **(call)**.

### 12.2 Modes

`AUTH_MODE` is read by the **API**, which is the authority **(call)**: `local` (default) = password only; `oidc` = SSO
only; `both` = SSO and password on one login page. The API refuses a session request whose kind the mode does not
allow (403 `mode`), so a UI bug cannot reopen password logins. The UI reads the mode from `GET /admin/v1/auth` on each
login-page render. In `both` the password login keeps working while the issuer is unreachable. Switching mode needs no
login: change the API's env on the edge and deploy.

The UI holds the relying-party settings: `OIDC_ISSUER`, `OIDC_CLIENT_ID`, `OIDC_CLIENT_SECRET`, `OIDC_SCOPES` (default
`openid profile email`), all set or all unset. With the mode at `oidc`/`both` and them unset, the login page says SSO is
not configured and logs it once. `OIDC_AUTO_PROVISION` is **not** implemented: commsgun has one role, so every
provisioned user would be an admin; the variable, if set, is refused at start **(call: a deliberate deviation from the
shared contract's config list)**.

### 12.3 Password login

`POST /login` (UI) → `POST /admin/v1/sessions/local {username, password}` → 201 `{token, user}`, or 401
`{"error": "unauthorized"}` for every cause (unknown user, wrong password, disabled), or 403 `mode`. The UI's limiter
(7.2: 5 per minute per address) stays; Caddy's `commsgun_login` zones sit in front (homelab-edge SSO spec § 4.3).

### 12.4 SSO login

1. `GET /login/sso` (UI): authorization code flow with PKCE (S256), `state` and `nonce`, kept in a transaction cookie
   `commsgun_oidc` (`HttpOnly; Secure; SameSite=Lax; Path=/auth`; 10 minutes; sealed with `SESSION_SECRET`) **(call)**.
2. `GET /auth/callback` (UI): checks `state`, exchanges the code (`client_secret_basic`), validates the ID token
   (`alg` RS256 from the issuer's JWKS, `iss`, `aud`, `exp`, `nonce`), clears the transaction cookie.
3. `POST /admin/v1/sessions/oidc {iss, sub}` → 201 `{token, user}`, or 403 `not_linked` (no identity, or the user is
   disabled: one answer) **(call)**, or 403 `mode`. Email and name are not sent: commsgun stores neither.

The relying party is `openid-client` (exact pin, like CG-04) **(call)**. Group gating (`commsgun`) is the IdP's job
(SSO spec decision 6); commsgun does not read groups. The conformance cases of the SSO spec § 3 (bad `state`, wrong
`iss`/`aud`, expired token, unlinked `sub`, replayed code, link while another IdP session is live) are UI tests against
a stub issuer.

### 12.5 Link SSO

Only in `both`, only for a session of kind `local`, on a new page `/account`:

1. The user re-enters their password: `POST /admin/v1/sessions/reauth` sets `reauth_at`; a link must complete within 5
   minutes of it **(call)**.
2. The UI starts 12.4 with `prompt=login` (SSO spec S17); the transaction cookie also binds the session token's hash
   and the purpose `link`, so a callback cannot link to a different session.
3. The callback shows "You are linking <preferred_username> to <username>" with Confirm and Cancel; Confirm (a POST,
   same-origin) calls `POST /admin/v1/identities {iss, sub}` with the session → 201; 409 `already_linked` if the
   pair belongs to anyone, or the user already has an identity for that issuer; 403 `reauth_required` after 5 minutes.

Unlinking is CLI-only (12.6). This page and logout are the dashboard's only writes besides the probe (section 2
amended).

### 12.6 Sessions, bootstrap and break-glass

- **Sessions (U4 amended).** `commsgun_session` keeps its name and attributes (`HttpOnly; Secure; SameSite=Strict;
  Path=/`) and its 12-hour fixed lifetime, but now holds the opaque token. The UI's hook validates it on every
  request with `GET /admin/v1/session`; logout calls `DELETE /admin/v1/session`. Disabling a user, a password reset
  and an unlink delete that user's sessions in the same transaction. Changing `ADMIN_PASSWORD` or `SESSION_SECRET` no
  longer signs anyone out.
- **Import (upgrade safety).** Today only the UI holds `ADMIN_PASSWORD`, and the edge's bump bot changes digests only
  (SSO spec F12). So the **UI** imports: at start, if `ADMIN_PASSWORD` is set, it calls `POST
  /admin/v1/bootstrap/import {username: "admin", password}`, which creates user `admin` only while `users` is empty and
  answers 200 `imported` or `skipped` **(call)**. The UI retries until the API answers. After a `skipped`, the UI logs
  once that `ADMIN_PASSWORD` is ignored; the edge retires `commsgun_admin_password` in the cutover change.
- **CLI** in the API binary, run as `docker compose run --rm -T commsgun-api admin <verb>` (EDGE-03 shape), password on
  stdin, never in argv: `create <username>` (only while `users` is empty), `reset-password <username>`, `disable
  <username>`, `enable <username>`, `unlink <username>` **(call: the last three serve offboarding and a lost link)**.

### 12.7 Admin API additions (K2 amended)

All behind `Authorization: Bearer <ADMIN_API_TOKEN>`; the session token travels in a `Commsgun-Session` header, never a
query string **(call)**.

| Endpoint | Purpose |
|---|---|
| `GET /admin/v1/auth` | `{mode}` |
| `POST /admin/v1/sessions/local`, `POST /admin/v1/sessions/oidc` | Create a session (12.3, 12.4) |
| `POST /admin/v1/sessions/reauth` | Re-authenticate before a link (12.5) |
| `GET /admin/v1/session`, `DELETE /admin/v1/session` | Validate (returns `{user}`), log out |
| `POST /admin/v1/identities` | Link (12.5) |
| `POST /admin/v1/bootstrap/import` | One-time import (12.6) |

The three hand-written copies of this JSON change together (CG-01). The read-only endpoints of 6.7 now also need a valid
session header **(call)**, so the admin token alone no longer reads message data.

### 12.8 SMTP submission listener for Authelia (K7)

Authelia sends reset links and enrolment codes through it (SSO spec decision 23, § 9).

- **Binding (owner, 2026-11-24).** Each listener's address is configurable: `ENQUEUE_ADDR` (default `:9200`),
  `ADMIN_ADDR` (default `:9201`), `SMTP_ADDR` (default unset = listener off). The edge gives `commsgun-api` fixed IPv4
  addresses on `commsgun` and `authelia-smtp` and binds enqueue and admin to the first, SMTP to the second, so Authelia
  can reach port 9203 alone. Gatus (host networking) probes the SMTP address directly; no loopback publish. A new spike
  item checks that the enqueue port's `127.0.0.1` publish still reaches the per-address bind (homelab-edge S26).
- **TLS.** STARTTLS is required before `AUTH` or `MAIL` (else 530). Certificate and key from `SMTP_TLS_CERT` and
  `SMTP_TLS_KEY` (PEM, secrets), issued by the edge-local CA that Authelia trusts; TLS 1.2 or later **(call)**.
- **Auth.** `AUTH PLAIN` only: username = app id, password = that app's key (constant time), and only an app whose
  `COMMSGUN_CONFIG` entry has `smtp_submission: true` **(call)**. Failures → 535, same reply for every cause.
- **Mapping to the enqueue path** (6.2 validation, app limits and one message per recipient, unchanged):
  envelope `MAIL FROM` must be one of the app's allowed senders, and a header `From` must carry the same address (its
  display name is kept); `RCPT TO` 1–10; `DATA` at most K5 (`SIZE` advertised); `Subject` (RFC 2047 decoded); a
  `text/plain` and/or `text/html` part, single or `multipart/alternative`; anything else (attachments, other parts) →
  554. `Message-ID` is required; the idempotency key is `smtp-` + the first 64 hex characters of its SHA-256, so a
  resend after a lost reply is a duplicate.
- **Expiry.** A new per-app key `send_window_seconds` (default and maximum 86400, `MAX_SEND_WINDOW`) sets `expires_at`
  for SMTP submissions; the edge sets 3600 for `authelia`, so its bodies (subject included, 6.4) are gone within the
  link's hour whatever the outcome.
- **Replies.** 250 queued or duplicate; 451 4.7.1 over an app limit; 451 4.3.2 queue full or over concurrency; 535; 550
  sender not allowed; 553 bad recipient; 552 too large; 554 unsupported content. At most 8 concurrent sessions, 60 s
  read timeout, 10 messages per connection **(call)**; all in `constants.go`.
- **Logs** carry the session outcome, app, counts and the reply class only: never an address, subject, `Message-ID`
  or body (Rule 4).
- The SMTP server is `github.com/emersion/go-smtp` with `go-sasl`, pinned **(call: the only new Go dependencies)**.

### 12.9 Configuration added (K3, U2 amended)

| Container | Env var | Meaning |
|---|---|---|
| API | `AUTH_MODE` | 12.2; default `local` |
| API | `ENQUEUE_ADDR`, `ADMIN_ADDR`, `SMTP_ADDR` | 12.8 |
| API | `SMTP_TLS_CERT`, `SMTP_TLS_KEY` | PEM (secrets); required when `SMTP_ADDR` is set, refused otherwise |
| UI | `OIDC_ISSUER`, `OIDC_CLIENT_ID`, `OIDC_CLIENT_SECRET` (secret), `OIDC_SCOPES` | 12.2; all or none |
| UI | `ADMIN_PASSWORD` | now import-only (12.6); optional once imported |
| UI | `ORIGIN` | follows the hostname: `https://commsgun.public.<domain>` |

`COMMSGUN_CONFIG` gains, per app, `smtp_submission` and `send_window_seconds` (CG-08: the edge and `api` change
together).

### 12.10 Tests the builder adds

Every route of 12.7 and its refusals; `mode` refusals in each mode; the import's `imported`/`skipped` and its
race-free single row; each CLI verb, including `disable` and `reset-password` deleting sessions; the link flow's
re-auth window, session binding, `already_linked` and confirmation; the conformance cases (12.4); the SMTP listener
against a TLS client with a test CA: STARTTLS required, 535 for every bad credential, each reply code of 12.8, a
duplicate `Message-ID`, the 1-hour window and body deletion at expiry; a migration from today's schema; logs with no
address, subject, token or password.

## 13. Threat model (draft, pending owner review before P5)

Required by the SSO spec § 3 before the dashboard goes public.

| Surface | Exposure | Auth | Limits | Secrets in URLs and logs | Session | CSRF |
|---|---|---|---|---|---|---|
| Dashboard `commsgun.public.<domain>` | internet, through Caddy | SSO; password login only in `local`/`both` | Caddy `commsgun_login` and `_hourly` zones on `POST /login`; UI limiter 5/min/address | `code`/`state` in the callback query: Caddy drops query, `Referer`, `Location` from logs (SSO spec § 4.3); no token in any URL | opaque, server-side, 12 h fixed, deleted on logout, disable, reset, unlink | SvelteKit origin check on every POST (7.5); `SameSite=Strict` session; `state` + PKCE + `nonce` |
| `/auth/callback` | internet | `state` bound to the transaction cookie; ID-token checks (12.4) | Caddy's per-IP zone for the site | as above | transaction cookie 10 min, `Lax`, `Path=/auth` | `state`; link purpose bound to the session hash |
| Admin API 9201 | `commsgun` network only, address-bound (12.8) | bearer `ADMIN_API_TOKEN` + session header | none beyond the address bind **(call)** | session token in a header only | — | not browser-reachable |
| Enqueue 9200 | tailnet only (`comms.ts`) | app id + key | app limits (6.3) | none | — | not browser-reachable |
| SMTP 9203 | `authelia-smtp` network only, address-bound | STARTTLS, then AUTH PLAIN as app `authelia` | app limits 5/min, 30/day (edge); 8 sessions | reset links live only in bodies, deleted by expiry within 1 h | — | — |

Residuals for the owner to accept or reject:

1. While `both` is on, a password login is reachable from the internet (SSO spec decision 18).
2. The `authelia_session` cookie is scoped to `public.<domain>`; the edge strips it before this upstream (SSO spec
   § 4.3). If that strip is missing, commsgun still ignores the cookie.
3. A compromised UI container holds `ADMIN_API_TOKEN` but needs a live session token for message data (12.7).
4. Reset links rest in the API's SQLite for at most an hour, and in Resend's logs on their side.
