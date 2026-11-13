# Hello-world probe from the dashboard: design

Status: **approved by the owner** (2026-11-13) and implemented; plan
`docs/superpowers/plans/2026-11-13-hello-world-probe.md`. Amends `2026-11-13-commsgun-design.md` (sections 2, 5.16, 6.7, 7.3)
and frozen contract K2.

Items marked **(owner)** were decided by the owner; items marked **(call)** are the designer's choices.

## 1. Intent

The owner wants to check from the dashboard that the hub can actually send: press a button, pick a
message type and provider, type one destination, and a fixed "hello world" message goes out as the `ops`
app. Today that check needs a hand-built `curl` with the `ops` key against the enqueue port.

## 2. Goals, non-goals, anti-goals

**Goals**

- A header-bar button, on every signed-in page, that opens a modal **(owner)**.
- The modal has: message type, provider, one destination (email address or E.164 number), Send **(owner)**.
- Subject and body are fixed and not editable **(owner)**.
- The message is sent as app `ops` with that app's configured `default_sender` **(owner)**.
- The probe is an ordinary message: same validation, limits, idempotency, retries, tracking, and it shows
  in the stats and the message list under app `ops`.

**Non-goals**

- Multi-provider routing. The selector lists the provider configured for the chosen type **(owner)**.
- Any other dashboard action (retry, cancel, edit) — still out of scope.
- Editing subject, body, sender, reply-to, expiry or sending to several recipients.
- A limit bypass. Probes count against the `ops` limits and the provider limits **(owner)**.

**Anti-goals**

- The destination, or any secret, in a log or an error string (CLAUDE.md rule 4).
- A double-click, a retry after a timeout, or a page reload sending the probe twice.
- The `ops` app key, or any new secret, reaching the dashboard container or the browser.
- A cross-site request triggering a send.

## 3. Constraints found in the code

- The admin listener (9201) is `GET`-only (`api/internal/admin/handler.go`) and K2 freezes that list.
- The dashboard holds only `ADMIN_API_TOKEN`; app keys exist only inside the API's `COMMSGUN_CONFIG`.
- Config has one provider per channel (`config.Providers`); the worker routes by channel. SMS has none,
  so SMS messages are accepted and held (`hold_reason: no_provider`) until they expire.
- `ops` is an app id in the edge inventory, not a concept in this repo. Its proposed limits are 2/min and
  5/day for email.
- `enqueue.validateRequest` already resolves the default sender when `from` is absent.
- The UI has no dialog component; `bits-ui` (which shadcn-svelte's dialog wraps) is already a dependency.

## 4. Approaches

**How the probe reaches the queue**

| | Approach | Trade-off |
|---|---|---|
| A (picked, **owner**) | New admin endpoint on 9201; the API enqueues in-process as `ops` | No new secret, no `homelab-edge` change. Amends K2: the admin listener is no longer read-only |
| B | The UI server posts to `/v1/messages` on 9200 with the `ops` key | Reuses the public contract untouched, but adds a secret and a second base URL to the UI (contract U2) and needs a coordinated edge change |
| C | The UI opens the SQLite store | Rejected: one process owns the database (spec 6.1) |

**What the provider selector means**

| | Approach | Trade-off |
|---|---|---|
| A (picked, **owner**) | Lists the provider configured for the chosen type; the API rejects a mismatch | One entry per type today. Becomes a real choice if multi-provider routing is added, with no change to this contract |
| B | Build multi-provider routing first | Changes K3 with the edge, the worker and the limit model; a separate project |

**SMS with no provider**: selectable, with a notice that the message will be held, not delivered **(owner)**.

## 5. Design

### 5.1 Admin API (amends K2 and spec 6.7)

Both need `Authorization: Bearer <ADMIN_API_TOKEN>`. Every other admin path stays `GET`-only.

**`GET /admin/v1/probes`** — what the modal may offer.

```json
{
  "app": "ops",
  "available": true,
  "channels": [
    {"channel": "email", "enabled": true, "provider": "resend", "sender": "Ops <ops@example.com>"},
    {"channel": "sms", "enabled": true, "provider": null, "sender": null}
  ]
}
```

`available` is false when the config has no app `ops`; `channels` is then empty. `enabled` is whether
`ops` has that channel. `sender` is the `ops` default sender, null for SMS or when none is configured.

**`POST /admin/v1/probes`** — send one probe.

```
Idempotency-Key: <same rules as enqueue>
Content-Type: application/json

{"channel": "email", "to": "me@example.com", "provider": "resend"}
```

- `to` is exactly one destination, validated by the enqueue rules for that channel.
- `provider` is required: the configured provider's name, or `null` when the channel has none. It must
  equal the current configuration.
- Unknown fields are rejected. The body cap is 4 KiB (a constant) **(call)**.
- Content is fixed in the API **(call)**: email subject `commsgun probe`, text `Hello world`; SMS body
  `Hello world`. No `from`, so the `ops` default sender applies.
- The API builds the equivalent enqueue request and runs it through the same code path as
  `POST /v1/messages` for app `ops`: validation, idempotency, app limits, queue-depth cap, hold.
- The stored idempotency key is `probe:<key>`, so a probe can never collide with a key the real `ops`
  client used **(call)**.
- `source_ip` is the first address of `X-Forwarded-For` (the dashboard forwards the browser's address),
  as on the enqueue listener **(call)**.
- The log line carries status, channel and request id only.

| Result | Status | Body |
|---|---|---|
| Queued | 202 | `{"request_id", "message_ids": ["…"], "duplicate": false, "held": <bool>}` |
| Idempotent repeat | 200 | same, `"duplicate": true` |
| Invalid destination, channel, JSON, key; no default sender | 422 | `{"error", "field"}` (enqueue's error names) |
| `provider` key absent | 422 | `{"error": "provider_required", "field": "provider"}` |
| Body over 4 KiB | 413 | |
| `ops` lacks the channel | 403 | `{"error": "channel_not_allowed"}` |
| App `ops` not configured | 409 | `{"error": "probe_app_missing"}` |
| `provider` differs from the configuration | 409 | `{"error": "provider_mismatch"}` |
| Key reused with a different body | 409 | `{"error": "idempotency_conflict"}` |
| Over the `ops` limit | 429 | `Retry-After`, `{"error": "rate_limited", "window": "minute\|day"}` |
| Store busy or queue full | 503 | `Retry-After`, `{"error": "unavailable"}` |
| Other method on `/probes` | 405 | `Allow: GET, POST` |

The app id `ops` is a named constant in the API **(call)**: no new config field, so `COMMSGUN_CONFIG`
(K3) and the edge are untouched. If the edge ever renames the app, the modal reports it as unavailable.

**API structure (call).** The enqueue handler's core (idempotency lookup, validation, channel check,
store call) moves into one function both handlers call, so the probe cannot drift from the real path.
The enqueue listener's behaviour and tests do not change.

### 5.2 Dashboard

**Server.** `src/routes/probe/+server.ts`, behind the existing session guard:

- `GET /probe` → the options from `GET /admin/v1/probes`.
- `POST /probe` → JSON `{channel, to, provider, key}`. It checks that the `Origin` header equals the
  configured origin (the session cookie is already `SameSite=Strict`; a mismatch is a 403), validates
  the shape (a malformed body is a 400), then calls the API. Like `/login`, it needs the proxy's
  `X-Forwarded-For` header (contract U2). It returns a small result: `queued` (message id, held), `invalid` (field),
  `rate_limited` (window, retry seconds), `stale` (options changed), `unavailable`, `error`.
- The API client gains `probeOptions()` and `sendProbe()`; failures log the same fixed-shape line as today.

**Header.** A "Send probe" button before Refresh: icon only on a phone (with an accessible name), icon
and label from `sm` up.

**Modal** (shadcn-svelte `dialog`, a new generated component). Phone-width sketch:

```
┌──────────────────────────────────┐
│ Send a hello-world probe      ✕  │
│ Sends a fixed test message as    │
│ app ops.                         │
│                                  │
│ Message type                     │
│ [ Email            ▾ ]           │
│ Provider                         │
│ [ resend           ▾ ]           │
│ Destination email                │
│ [ me@example.com     ]           │
│ From: Ops <ops@example.com>      │
│                                  │
│              [ Cancel ] [ Send ] │
└──────────────────────────────────┘
```

- Options load when the modal opens (not on every page load) **(call)**; a loading and an
  "API unavailable · Retry" state cover the wait and the failure.
- Message type lists Email and SMS; a channel `ops` does not have is shown disabled.
- Provider lists the configured provider for the type. With none: "None configured", and a notice
  "No SMS provider is configured. The message will be accepted and held, not delivered."
- Destination is one field: `type="email"` for email, `type="tel"` with an E.164 hint for SMS. It is
  cleared when the type changes.
- The sender line is read-only.
- Send is disabled while the destination is empty, while a send is in flight, and when `ops` is not
  configured (the modal then says so).
- **Idempotency.** The browser generates a UUID key when the modal opens and a new one whenever a field
  changes or a send succeeds. A retry with unchanged fields reuses the key, so it cannot send twice.
- **Outcomes**, shown inline in the modal: queued (with a link to `/messages/<id>`, and "held" when
  applicable); invalid destination (on the field, checked in the browser first); rate limited ("`ops` minute/day limit reached, retry
  in …"); stale options (reloads them); unavailable (retry). After a success the page data is refreshed.
- The modal closes on navigation, so "View message" lands on the detail page.

### 5.3 Tests

- API: handler tests for every row of the result table, the fixed content, the `probe:` key prefix, the
  idempotent repeat, the log line carrying no destination, and the unchanged `GET`-only behaviour of the
  other admin paths.
- UI unit: client methods and the `/probe` endpoint (origin check, validation, result mapping).
- UI e2e (stub API gains both endpoints): open at 390 px, send an email probe, SMS held notice, rate
  limited, invalid destination, API down, axe checks, no horizontal scroll.
- `scripts/smoke.sh`: one probe through the real images with the `fake` provider.

### 5.4 Documents changed in the same PR

- `2026-11-13-commsgun-design.md`: non-goal "Dashboard actions" and decision 16 name the probe as the
  one exception; 6.7 and 7.3 gain the endpoint and the modal.
- `2026-11-13-commsgun-api.md`: K2 amended ("Amended: `GET, POST /admin/v1/probes`").
- `CLAUDE.md` and `README.md`: "read-only admin dashboard" becomes read-only except the probe.

## 6. Release

Both images change (`api/` and `ui/` sources). Nothing changes in `davcs86/homelab-edge` beyond the usual
bump PR and deploy. The feature works there only if the edge config has app `ops` with a
`default_sender`, which the edge plan already specifies.

## 7. Left to the builder

- The exact wording of notices and errors.
- Whether the type and provider controls are `select` or a segmented control, as long as they pass axe
  and fit 390 px.
- How the shared enqueue function is named and where it lives inside `api/internal/enqueue`.
