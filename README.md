# commsgun

A small email and SMS delivery hub for a homelab. Apps POST a message to one HTTP endpoint; commsgun queues
it durably in SQLite, sends it through a provider within the app and provider rate limits, tracks bounces,
and shows the outcome on a dashboard. The dashboard is read-only except for one action: a fixed "hello world" probe sent as app `ops`.

Two images, built from this repo:

| Image | Source | Ports |
|---|---|---|
| `ghcr.io/davcs86/commsgun-api` | `api/` (Go) | 9200 enqueue, 9201 admin (internal only) |
| `ghcr.io/davcs86/commsgun-ui` | `ui/` (SvelteKit) | 9202 dashboard |

The enqueue port has no TLS and is meant for a private network (the tailnet). Design: `docs/superpowers/specs/`.

## Enqueue contract

```sh
curl -X POST http://localhost:9200/v1/messages \
  -H 'X-Commsgun-App: myapp' \
  -H 'Authorization: Bearer <that app key>' \
  -H 'Idempotency-Key: 2026-11-13-alert-42' \
  -H 'Content-Type: application/json' \
  -d '{"channel":"email","to":["a@example.com"],"subject":"Hello","text":"Hi"}'
```

SMS: `{"channel":"sms","to":["+15551234567"],"body":"Hi"}`. One message is created per recipient (1 to 10
recipients). `from`, `html`, `reply_to` and `expires_at` are optional for email; `expires_at` defaults to 24 hours
after enqueue.

| Status | Meaning |
|---|---|
| 202 | queued; `{"request_id","message_ids","duplicate":false}` |
| 200 | same `Idempotency-Key` and body as an earlier request; same ids, `"duplicate":true` |
| 401 | missing or unknown app, or wrong key (same body for every cause) |
| 403 | channel not enabled for this app |
| 409 | key reused with a different body |
| 413 | body over 256 KiB |
| 422 | invalid body, sender not allowed, bad recipient; an unknown field is `{"error":"unknown_field","field":"<name>"}` |
| 429 | app over its per-minute or per-day limit; `Retry-After` |
| 503 | too many requests in flight or queue full; `Retry-After`, retry with the same key |

`GET /healthz` is 200 while the database answers and the worker is ticking; `GET /healthz/backlog` is 503 when a
sendable message has waited more than 15 minutes.

## Configuration

API (`ghcr.io/davcs86/commsgun-api`):

| Env var | Meaning |
|---|---|
| `COMMSGUN_CONFIG` | JSON: `apps` (id, key of at least 32 characters, per-channel `per_minute`, `per_day`, `senders`, `default_sender`) and `providers` (per channel: `{"name","per_minute","per_day"}` or `null` to hold messages) |
| `RESEND_API_KEY` | Resend key; required when the email provider is `resend` |
| `ADMIN_API_TOKEN` | At least 32 characters; shared with the dashboard |
| `DATA_DIR` | SQLite directory, default `/data` (the only writable path) |
| `COMMSGUN_ALLOW_FAKE` | `1` permits the `fake` provider (tests and smoke only) |

The process refuses to start on an invalid config, including an app key or `ADMIN_API_TOKEN` shorter than 32
characters.

Dashboard (`ghcr.io/davcs86/commsgun-ui`):

| Env var | Meaning |
|---|---|
| `ADMIN_PASSWORD` | Login password, at least 16 characters; the user name is `admin` |
| `SESSION_SECRET` | Cookie signing key, at least 32 characters |
| `ADMIN_API_TOKEN` | Same value as the API's |
| `API_BASE_URL` | The admin listener, for example `http://commsgun-api:9201` |
| `ORIGIN` | The dashboard's public origin; any other derived origin gets 400 (except `/healthz`) |
| `PORT` | Default 9202 |

The dashboard refuses to start when `ADMIN_PASSWORD`, `SESSION_SECRET` or `ADMIN_API_TOKEN` is shorter than its
minimum. Changing `ADMIN_PASSWORD` or `SESSION_SECRET` signs everyone out. On the dashboard, **Sent** counts
messages the provider accepted in the range; **Delivered**, **Bounced** and the other outcomes are confirmed later
and counted by when that happened, so the chart stacks only those final outcomes.

The image sets `PROTOCOL_HEADER`, `HOST_HEADER`, `ADDRESS_HEADER` and `XFF_DEPTH` so the origin and client address
come from `X-Forwarded-Proto`, `X-Forwarded-Host` and `X-Forwarded-For`. Put the dashboard behind a reverse proxy
that sends all three.

## Run locally

```sh
docker build -t commsgun-api:dev api && docker build -t commsgun-ui:dev ui
scripts/smoke.sh commsgun-api:dev commsgun-ui:dev   # runs both together on the fake provider
```

Tests: `cd api && go test ./... -race`; `cd ui && pnpm install && pnpm check && pnpm lint && pnpm test && pnpm test:e2e`.
CI (`.github/workflows/ci.yml`) runs the same, then publishes both images on `main`.
