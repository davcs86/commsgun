# commsgun

A small email and SMS delivery hub for a homelab. Apps POST a message to one HTTP endpoint; commsgun queues
it durably in SQLite, sends it through a provider within the app and provider rate limits, tracks bounces,
and shows the outcome on a dashboard. The dashboard is read-only except for a fixed "hello world" probe sent as app
`ops`, logout and linking an SSO identity to an account.

Two images, built from this repo:

| Image | Source | Ports |
|---|---|---|
| `ghcr.io/davcs86/commsgun-api` | `api/` (Go) | 9200 enqueue, 9201 admin (internal only), 9203 SMTP submission (off by default) |
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
| `COMMSGUN_CONFIG` | JSON: `apps` (id, key of at least 32 characters, per-channel `per_minute`, `per_day`, `senders`, `default_sender`; optional `smtp_submission` (default `false`; `true` makes the key SMTP-only) and `send_window_seconds` (SMTP `expires_at`, 1 to 86400, default 86400)) and `providers` (per channel: `{"name","per_minute","per_day"}` or `null` to hold messages) |
| `RESEND_API_KEY` | Resend key; required when the email provider is `resend` |
| `ADMIN_API_TOKEN` | At least 32 characters; shared with the dashboard |
| `DATA_DIR` | SQLite directory, default `/data` (the only writable path) |
| `COMMSGUN_ALLOW_FAKE` | `1` permits the `fake` provider (tests and smoke only) |
| `AUTH_MODE` | `local` (default, password only), `oidc` (SSO only) or `both`; the API enforces it |
| `ENQUEUE_ADDR`, `ADMIN_ADDR` | Listener `host:port`, default `:9200` and `:9201` |
| `SMTP_ADDR` | SMTP submission listener `host:port`; unset (default) turns it off |
| `SMTP_TLS_CERT_FILE`, `SMTP_TLS_KEY_FILE` | Paths to PEM files for STARTTLS, read once at start; required with `SMTP_ADDR`, refused without |

The process refuses to start on an invalid config, including an app key or `ADMIN_API_TOKEN` shorter than 32
characters.

Dashboard (`ghcr.io/davcs86/commsgun-ui`):

| Env var | Meaning |
|---|---|
| `ADMIN_PASSWORD` | Optional, at least 16 characters: imported as user `admin` while the API has no users, then ignored |
| `SESSION_SECRET` | At least 32 characters; seals the SSO transaction cookie |
| `ADMIN_API_TOKEN` | Same value as the API's |
| `API_BASE_URL` | The admin listener, for example `http://commsgun-api:9201` |
| `ORIGIN` | The dashboard's public origin; any other derived origin gets 400 (except `/healthz`) |
| `PORT` | Default 9202 |
| `OIDC_ISSUER`, `OIDC_CLIENT_ID`, `OIDC_CLIENT_SECRET` | SSO relying party, all set or all unset; the issuer is an `https:` URL |
| `OIDC_SCOPES` | Optional, default `openid profile email` |
| `OIDC_AUTO_PROVISION` | Not supported: the dashboard refuses to start if it is set |

The dashboard refuses to start when `ADMIN_PASSWORD`, `SESSION_SECRET` or `ADMIN_API_TOKEN` is shorter than its
minimum, or when the API refuses the import; `/healthz` answers 200 only after the import. Sessions live in the API
and end on logout, after 12 hours, or when the user is disabled, reset or unlinked; changing `ADMIN_PASSWORD` or
`SESSION_SECRET` signs no one out. On the dashboard, **Sent** counts
messages the provider accepted in the range; **Delivered**, **Bounced** and the other outcomes are confirmed later
and counted by when that happened, so the chart stacks only those final outcomes.

The image sets `PROTOCOL_HEADER`, `HOST_HEADER`, `ADDRESS_HEADER` and `XFF_DEPTH` so the origin and client address
come from `X-Forwarded-Proto`, `X-Forwarded-Host` and `X-Forwarded-For`. Put the dashboard behind a reverse proxy
that sends all three.

## Accounts

Users live in the API's store. The API binary's `admin` subcommand manages them offline: it reads only `DATA_DIR`,
opens no socket, and reads a password only from stdin. Verbs: `create <username>` (only while there are no users),
`add <username>`, `reset-password <username>` (these three read the password), `disable <username>`,
`enable <username>`, `unlink <username>` (drops the user's SSO identities). `reset-password`, `disable` and `unlink`
end the user's sessions. Run it with the service's image, volume and uid and no network, never as
`docker compose run commsgun-api` (that container would claim the service's addresses):

```sh
docker compose run --rm -T commsgun-admin admin <verb> <username>
```

An SSO identity is linked from the dashboard's **Account** page (`AUTH_MODE=both`, after re-entering the password).

## Run locally

```sh
docker build -t commsgun-api:dev api && docker build -t commsgun-ui:dev ui
scripts/smoke.sh commsgun-api:dev commsgun-ui:dev   # runs both together on the fake provider
```

Tests: `cd api && go vet ./... && go test ./... -race` (CI also runs `gofmt -l` and staticcheck; see `CLAUDE.md`);
`cd ui && pnpm install --frozen-lockfile && pnpm check && pnpm lint && pnpm test && pnpm test:e2e` (the e2e
run needs OpenSSL 3 on `PATH`: the stub issuer makes its test CA with it).
CI (`.github/workflows/ci.yml`) runs the same, then publishes both images on `main`.

## Release

Merging to `main` deploys nothing. CI builds both images, smoke-tests them together, pushes
`ghcr.io/davcs86/commsgun-api` and `ghcr.io/davcs86/commsgun-ui` (`:<sha>`, plus the moving tag `main`) and prints
both `@sha256:…` references in the `images` job summary. The edge keeps running the digests pinned in
`davcs86/homelab-edge` until they are changed there.

A change is live only after a second PR, in `davcs86/homelab-edge`: its `bump-commsgun` workflow opens it (daily, or
at once from **Actions → bump-commsgun → Run workflow**), and after it merges the owner runs **Actions → deploy**
there. Every merge to `main` publishes new digests, so a docs- or CI-only merge also produces a bump PR that changes
nothing on the edge.
