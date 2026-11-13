# commsgun Dashboard and Release Implementation Plan (plan 2 of 3)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build the admin dashboard as its own container, and the CI that tests, smoke-tests and publishes both commsgun images.

**Architecture:** A SvelteKit server (Node adapter) owns the login session and renders every page from server-side `load` functions that call the Go admin API with a shared token; the browser never talks to the Go container. All filter and range state lives in the URL. CI builds both images from one commit and runs them together against the `fake` provider.

**Tech Stack:** SvelteKit 2 / Svelte 5, `@sveltejs/adapter-node`, TypeScript, Tailwind 4, shadcn-svelte (components and chart), Vitest, Playwright, pnpm, Node 24 in the image (the suite must also pass on Node 22). GitHub Actions, GHCR.

**Spec:** `docs/superpowers/specs/2026-11-13-commsgun-design.md` — sections 7 and 8. Read it first.

**Depends on:** plan 1 (`2026-11-13-commsgun-api.md`) for the admin API JSON in its "Frozen contracts" section. Tasks 1–5 here need only that contract (they test against a stub); Task 6 needs plan 1's image.

## Global Constraints

- The browser never receives `ADMIN_API_TOKEN`, `ADMIN_PASSWORD` or `SESSION_SECRET`; they are read only from `$env/dynamic/private` in `*.server.ts` files.
- No page shows or fetches a message body or subject; the admin API does not return them.
- Mobile-first: every page is built and tested at 390 px width first; no horizontal page scroll at that width.
- UI components come from shadcn-svelte (`pnpm dlx shadcn-svelte@latest add …`); use its theme tokens, no hard-coded colours. Light and dark follow `prefers-color-scheme`.
- Counts are always shown as numbers as well as by chart colour.
- Times: the server renders UTC; the browser re-formats in its own zone and shows the zone.
- The container runs as uid 10001 on a read-only root filesystem and writes nothing to disk.
- Dependencies are locked (`pnpm-lock.yaml`, `--frozen-lockfile` in CI and the image). Base images are pinned by digest.
- Comments state constraints only, at most 2 lines.
- No test is skipped, disabled or weakened to get green.

## Frozen contracts (do not change; stop and report instead)

| # | Contract | Value |
|---|---|---|
| U1 | Dashboard listener | container port `9202`; `GET /healthz` unauthenticated → 200 `{"status":"ok"}` |
| U2 | Env | `ADMIN_PASSWORD` (≥16 chars), `SESSION_SECRET` (≥32), `ADMIN_API_TOKEN` (≥32), `API_BASE_URL`, `ORIGIN`, `PORT` (default 9202) |
| U3 | Images | `ghcr.io/davcs86/commsgun-api` and `ghcr.io/davcs86/commsgun-ui`, tags `<git sha>` and `main`, label `org.opencontainers.image.revision=<git sha>` on both |
| U4 | Session cookie | name `commsgun_session`; `HttpOnly; Secure; SameSite=Strict; Path=/`; lifetime 12 h |

## Review Focus

1. **The admin API is down or slow** → pages render an inline "API unavailable" state with a retry, the server does not crash or hang past 5 s, and `/healthz` still answers 200. (Task 3)
2. **A tampered, truncated or expired session cookie** → treated as logged out and redirected to `/login`; never a 500. (Task 2)
3. **Hand-edited URL parameters** — an unknown range, `from` after `to`, a non-date, a 400-character filter → fall back to defaults or are clamped; never a 500 and never passed unchecked to the API. (Task 3)
4. **Hostile text in data** — a recipient or error message containing `<script>` or very long unbroken text → rendered as text and wrapped; the layout does not scroll sideways. (Task 5)
5. **An empty store** (first deploy, nothing sent yet) → every page shows a clear empty state and a zero-filled chart, not a blank area or `NaN`. (Task 4)

## File Structure

```
ui/
  package.json, pnpm-lock.yaml, svelte.config.js, vite.config.ts, tsconfig.json, components.json
  Dockerfile, .dockerignore
  src/app.html, src/app.css, src/app.d.ts
  src/hooks.server.ts                    env check at boot, session guard
  src/lib/server/env.ts                  read + validate env once
  src/lib/server/session.ts              sign/verify cookie, login rate limit
  src/lib/server/api.ts                  typed admin API client
  src/lib/filters.ts                     URL ⇄ filter/range model (pure)
  src/lib/time.ts                        range presets, formatting helpers (pure)
  src/lib/types.ts                       Stats, Message, Attempt, Overview, Facets
  src/lib/components/ui/…                shadcn-svelte generated components
  src/lib/components/RangeChips.svelte   quick ranges
  src/lib/components/FilterSheet.svelte  filters (bottom sheet on phone, popover on desktop)
  src/lib/components/StatTiles.svelte
  src/lib/components/OutcomeChart.svelte
  src/lib/components/ProviderUsage.svelte
  src/lib/components/Breakdown.svelte
  src/lib/components/MessageList.svelte  cards on phone, table from md
  src/lib/components/LocalTime.svelte    UTC on server, local in browser
  src/lib/components/ApiError.svelte
  src/routes/+layout.svelte, +layout.server.ts
  src/routes/+page.svelte, +page.server.ts            overview
  src/routes/login/+page.svelte, +page.server.ts
  src/routes/logout/+server.ts
  src/routes/messages/+page.svelte, +page.server.ts
  src/routes/messages/[id]/+page.svelte, +page.server.ts
  src/routes/healthz/+server.ts
  e2e/stub-api.ts                        stub admin API for Playwright
  e2e/*.spec.ts
scripts/smoke.sh
.github/workflows/ci.yml
CLAUDE.md, README.md
```

---

### Task 1: Scaffold, env validation, health

**Files:**
- Create: the `ui/` project files, `src/lib/server/env.ts`, `src/hooks.server.ts`, `src/routes/healthz/+server.ts`
- Test: `ui/src/lib/server/env.test.ts`

**Interfaces:**
- Produces: `loadEnv(source: Record<string, string | undefined>): Env` throwing `Error` on a bad value; `Env = { adminPassword, sessionSecret, adminApiToken, apiBaseUrl, origin: string }`; `getEnv(): Env` (memoised, reads `$env/dynamic/private`); scripts `pnpm check`, `pnpm lint`, `pnpm test` (Vitest), `pnpm test:e2e` (Playwright), `pnpm build`.

- [ ] **Step 1: Scaffold.** `pnpm dlx sv create ui` (minimal template, TypeScript, add-ons: tailwindcss, vitest, playwright, eslint, prettier), switch to `@sveltejs/adapter-node`, then `pnpm dlx shadcn-svelte@latest init` (base colour neutral). Add components: `button card input label badge sheet popover select table skeleton alert separator chart calendar range-calendar`.
- [ ] **Step 2: Write the failing env test**

```ts
it.each([
  ['ADMIN_PASSWORD', 'x'.repeat(15)],
  ['SESSION_SECRET', 'x'.repeat(31)],
  ['ADMIN_API_TOKEN', 'x'.repeat(31)],
  ['API_BASE_URL', 'not a url'],
  ['ORIGIN', ''],
])('rejects %s = %s', (key, value) => {
  expect(() => loadEnv({ ...valid, [key]: value })).toThrow(key);   // message names the variable
});
it('never puts a value in the error', () => { /* message does not contain the bad value */ });
it('accepts a valid environment', () => { expect(loadEnv(valid).apiBaseUrl).toBe('http://api:9201'); });
```

- [ ] **Step 3: Run** `cd ui && pnpm test` → FAIL.
- [ ] **Step 4: Implement** `env.ts`; `hooks.server.ts` calls `getEnv()` in its `init` export so a bad environment stops the server at boot; `/healthz` returns `{"status":"ok"}`.
- [ ] **Step 5: Run** `pnpm check && pnpm lint && pnpm test && pnpm build` → all pass.
- [ ] **Step 6: Commit** — `feat(ui): scaffold SvelteKit, shadcn-svelte, env validation, healthz`.

---

### Task 2: Session and login

**Files:**
- Create: `src/lib/server/session.ts`, `src/routes/login/+page.svelte`, `+page.server.ts`, `src/routes/logout/+server.ts`
- Modify: `src/hooks.server.ts`
- Test: `src/lib/server/session.test.ts`, `e2e/login.spec.ts`

**Interfaces:**
- Produces: `signSession(secret: string, now: number): string` (payload `{exp}` base64url + `.` + HMAC-SHA256); `verifySession(secret: string, cookie: string | undefined, now: number): boolean`; `checkPassword(given: string, expected: string): boolean` (constant time via `timingSafeEqual` over SHA-256 digests); `LoginLimiter` with `allow(key: string, now: number): boolean` (5 per 60 s per key); `SESSION_COOKIE = 'commsgun_session'`; `SESSION_TTL_MS = 12 * 60 * 60 * 1000`. `event.locals.authed: boolean`.

- [ ] **Step 1: Write the failing unit tests**

```ts
it('accepts a fresh cookie and rejects it after 12h');
it('rejects a cookie signed with another secret');
it('checkPassword is false for a prefix, a longer string, and empty');
it('LoginLimiter allows 5 then blocks until 60s have passed; keys are independent');
// Review Focus:
it.each(['', 'garbage', 'a.b.c', validCookie.slice(0, -2), validCookie + 'x', 'eyJleHAiOiJ4In0.' + sig])
  ('verifySession(%s) is false and does not throw');
```

- [ ] **Step 2: Write the failing Playwright test** (`e2e/login.spec.ts`, phone viewport 390×844):

```ts
// GET / unauthenticated → redirected to /login?next=%2F
// wrong password → stays on /login, shows "Incorrect username or password", no cookie set
// username other than "admin" with the right password → same message
// right credentials → lands on "/" (or on ?next when it is a same-origin path); cookie has HttpOnly, Secure, SameSite=Strict
// ?next=https://evil.example → after login lands on "/"
// 6th attempt within a minute → message "Too many attempts. Try again in a minute." (status 429)
// POST /logout → cookie cleared, GET / redirects to /login
// GET /healthz works without a cookie
```

- [ ] **Step 3: Run** both → FAIL.
- [ ] **Step 4: Implement.** Until Task 4 builds the overview, `/` is a placeholder page that calls no API, so this task's Playwright `webServer` starts only the built app. The hook sets `locals.authed` and redirects (303) every route except `/login`, `/logout` and `/healthz` when false. The limiter key is `event.getClientAddress()`. The login form is a SvelteKit form action (works without JavaScript).
- [ ] **Step 5: Run** `pnpm test && pnpm test:e2e` → PASS.
- [ ] **Step 6: Commit** — `feat(ui): admin login and session`.

---

### Task 3: Filter/range model and admin API client

**Files:**
- Create: `src/lib/filters.ts`, `src/lib/time.ts`, `src/lib/types.ts`, `src/lib/server/api.ts`, `src/lib/components/ApiError.svelte`
- Test: `src/lib/filters.test.ts`, `src/lib/time.test.ts`, `src/lib/server/api.test.ts`

**Interfaces:**
- Consumes: the admin API JSON in plan 1.
- Produces:

```ts
export const RANGES = ['1h','3h','6h','12h','24h','3d','7d','14d','30d'] as const;
export type RangeKey = (typeof RANGES)[number];
export const DEFAULT_RANGE: RangeKey = '1h';
export type Filters = {
  range: RangeKey | 'custom'; from: Date; to: Date;          // resolved instants
  channel?: 'email' | 'sms'; app?: string; state?: string; errorType?: string;
  sender?: string; recipient?: string;
};
export function parseFilters(url: URLSearchParams, now: Date): Filters;
export function toSearchParams(f: Filters): URLSearchParams;   // quick range → only ?range=; custom → ?from=&to= (ISO UTC)
export function toApiParams(f: Filters): URLSearchParams;      // always from/to instants + set filters
export function activeFilterCount(f: Filters): number;         // excludes range
// api.ts
export type ApiResult<T> = { ok: true; data: T } | { ok: false; status: number; reason: 'unavailable' | 'error' | 'not_found' };
export function createApi(env: Env, fetchFn: typeof fetch): {
  stats(f: Filters): Promise<ApiResult<Stats>>; messages(f: Filters, cursor?: string): Promise<ApiResult<MessagePage>>;
  message(id: string): Promise<ApiResult<MessageDetail>>; facets(): Promise<ApiResult<Facets>>; overview(): Promise<ApiResult<Overview>>;
};
```

- [ ] **Step 1: Write the failing tests**

```ts
// filters
it('defaults to the last hour when the URL is empty');           // range '1h', to === now, from === now - 3600s
it.each(RANGES)('range %s resolves to the right span');           // 3d → 259200s, 30d → 2592000s
it('round-trips every field through toSearchParams/parseFilters');
it('a quick range serialises as ?range= only, so a bookmarked view stays relative');
// Review Focus:
it('falls back to 1h for range=bogus');
it('falls back to 1h when from is after to, or either is not a date');
it('clamps a custom range longer than 30 days to 30 days ending at `to`');
it('drops channel/state values outside the known sets and truncates text filters to 100 chars');
// api
it('sends Authorization: Bearer <token> and the API params');
it('maps a network error and a 5s timeout to { ok:false, reason:"unavailable" }');   // Review Focus
it('maps 404 to not_found and other non-2xx to error');
it('never includes the token in a thrown or returned message');
```

- [ ] **Step 2: Run** `pnpm test` → FAIL. **Step 3: Implement** (5 s `AbortSignal.timeout` on every call). **Step 4: Run** → PASS.
- [ ] **Step 5: Commit** — `feat(ui): filter model and admin API client`.

---

### Task 4: Overview page

**Files:**
- Create: `src/routes/+layout.svelte`, `+layout.server.ts`, `src/routes/+page.svelte`, `+page.server.ts`, `RangeChips`, `FilterSheet`, `StatTiles`, `OutcomeChart`, `ProviderUsage`, `Breakdown`, `LocalTime` components, `e2e/stub-api.ts`
- Test: `e2e/overview.spec.ts`

**Interfaces:**
- Consumes: `parseFilters`, `createApi`.
- Produces: `e2e/stub-api.ts` — a Node HTTP server implementing plan 1's admin routes from in-memory fixtures, with `setScenario('normal' | 'empty' | 'down' | 'hostile')`; Playwright's `webServer` starts it and the built app with `API_BASE_URL` pointing at it. `+page.server.ts` `load` returns `{ filters, stats, overview, facets }`, each an `ApiResult`.

Layout, top to bottom at 390 px (spec 7.3 sketch): header (title, refresh, logout); range chips in one horizontally scrollable row; a "Filters · N" button and a "Custom range" button; six tiles in a 3×2 grid — Delivered, Sent, Queued, Bounced, Failed, Retries; the stacked outcome chart; provider usage (one row per channel: used/limit bar, reset time, pause reason, held count); "Errors by type"; "By app". From `md` up: tiles in one row, chart and provider usage side by side.

- [ ] **Step 1: Write the failing Playwright test** (390×844 unless stated):

```ts
// default view: chip "1h" is pressed (aria-pressed=true); tiles show the fixture numbers
//   Delivered 41, Sent 3, Queued 0, Bounced 1, Failed 0, Retries 4
// tapping "7d" navigates to /?range=7d and the stub received from/to 7 days apart
// Filters sheet: choosing channel=email and app=xstockstrat → URL has both, button reads "Filters · 2",
//   "Clear" removes them; browser Back restores the previous view
// Custom range: picking two dates puts ISO UTC from/to in the URL and unpresses every chip
// provider row shows "47 / 100", a reset time, and for sms "No provider · 2 held"
// a paused provider shows its reason ("Daily quota reached") and when it resumes
// times: an element with data-testid="utc-time" shows the browser zone's rendering after hydration (emulate timezoneId 'America/Mexico_City')
//   and the UTC rendering with JavaScript disabled
// auto-refresh: with page.clock, after 30s the stub has received a second /stats call; none while document.hidden
// no horizontal scroll: document.documentElement.scrollWidth <= clientWidth at 390 and at 1280
// accessibility: no axe-core violations of impact serious or critical (@axe-core/playwright)
// Review Focus — scenario 'empty': tiles all 0, chart area shows "No messages in this range", no "NaN" anywhere in the page text
// Review Focus — scenario 'down': an alert "The delivery API is not responding" with a Retry button; /healthz still 200
```

- [ ] **Step 2: Run** `pnpm test:e2e` → FAIL.
- [ ] **Step 3: Implement.** Refresh uses `invalidateAll()` on a 30 s interval gated on `document.visibilityState`. `LocalTime` renders `<time datetime=ISO>` with UTC text on the server and re-formats in `onMount` with `Intl.DateTimeFormat(undefined, { timeZoneName: 'short' })`. The chart is shadcn-svelte's chart (LayerChart) as a stacked bar per bucket with series delivered, sent, bounced, complained, failed, expired.
- [ ] **Step 4: Run** `pnpm check && pnpm lint && pnpm test && pnpm test:e2e` → PASS.
- [ ] **Step 5: Commit** — `feat(ui): overview dashboard`.

---

### Task 5: Message list and detail

**Files:**
- Create: `src/routes/messages/+page.svelte`, `+page.server.ts`, `src/routes/messages/[id]/+page.svelte`, `+page.server.ts`, `MessageList.svelte`
- Test: `e2e/messages.spec.ts`

**Interfaces:**
- Consumes: `createApi().messages`, `.message`, the filter components from Task 4.
- Produces: `/messages` (same range chips and filters, `?cursor=` paging) and `/messages/[id]`.

- [ ] **Step 1: Write the failing Playwright test**

```ts
// at 390px each message is a card: state badge, channel, recipient, app, created time; at 1280px a table with the same fields plus sender, attempts, error type
// the list honours the same URL filters as the overview; a "Messages" link on the overview carries the current query string across
// "Older" loads the next page via ?cursor=…; hidden on the last page
// tapping a card opens /messages/<id>: metadata, a timeline (enqueued → sent → final) and one row per attempt
//   with n, time, duration, outcome, error type, provider status and error name, message
// a held SMS shows "Held: no SMS provider configured" and its expiry time
// the detail page has no body/subject section at all
// unknown id → a 404 page with a link back to the list
// state badges: text label always present (not colour alone)
// Review Focus — scenario 'hostile': recipient `<script>alert(1)</script>@x.io` and a 300-char unbroken error message
//   are shown as text, no dialog fires, and scrollWidth <= clientWidth at 390px
```

- [ ] **Step 2: Run** → FAIL. **Step 3: Implement** (never use `{@html}`; long values get `break-all`). **Step 4: Run** the full suite → PASS.
- [ ] **Step 5: Commit** — `feat(ui): message list and detail`.

---

### Task 6: Images, smoke test, CI, repository docs

**Files:**
- Create: `ui/Dockerfile`, `ui/.dockerignore`, `scripts/smoke.sh`, `.github/workflows/ci.yml`, `CLAUDE.md`
- Modify: `README.md`

**Interfaces:**
- Consumes: plan 1's `api/Dockerfile` and the `fake` provider.
- Produces: `scripts/smoke.sh <api-image> <ui-image>` (exit 0 on success); CI jobs `api`, `ui`, `images`; published images per U3.

- [ ] **Step 1: Write `ui/Dockerfile`.** Stage 1: digest-pinned `node:24-slim`, `corepack enable`, `pnpm install --frozen-lockfile`, `pnpm build`, `pnpm prune --prod`. Stage 2: the same digest-pinned base; copy `build/`, `node_modules/`, `package.json`; `ENV NODE_ENV=production PORT=9202 HOST=0.0.0.0`; `USER 10001:10001`; `EXPOSE 9202`; `CMD ["node","build"]`; the same two labels as the API image.
- [ ] **Step 2: Write `scripts/smoke.sh`** (bash; this script runs only in CI and on a developer machine, never in an image). It must:
  1. Create a docker network; start the API image `--read-only` with a named volume, `COMMSGUN_ALLOW_FAKE=1`, a config with one app (`smoke`, email via `fake`, sms with no provider), and generated 48-char secrets; start the UI image `--read-only` with `API_BASE_URL=http://<api>:9201` and `ORIGIN=http://localhost:<port>`.
  2. Wait for both `/healthz` (30 s limit).
  3. `POST /v1/messages` with 2 email recipients → expect 202; repeat with the same key → 200 and `"duplicate":true`; wrong key → 401; one SMS → 202.
  4. Poll until the UI's data shows 2 `sent`: log in with `curl` (cookie jar; `-H "Origin: …"` so SvelteKit's CSRF check passes), then fetch `/messages` and expect both recipients and the text `Held`.
  5. Unauthenticated `GET /` on the UI → 303 to `/login`. `GET http://<api>:9200/admin/v1/stats` → 404.
  6. Restart the API container → the 2 sent messages are still listed (volume persisted).
  7. Read each container's memory with `docker stats --no-stream --format '{{.MemUsage}}'`; fail above **60 MiB** (API) or **140 MiB** (UI). Print both figures.
  8. Check both containers run as uid 10001 (`docker exec … id -u` for the UI; `docker inspect --format '{{.Config.User}}'` for the distroless API).
  9. Always remove containers, network and volume on exit (trap).
- [ ] **Step 3: Run it locally**

Run: `docker build -t commsgun-api:dev api && docker build -t commsgun-ui:dev ui && scripts/smoke.sh commsgun-api:dev commsgun-ui:dev`
Expected: every check prints `PASS`, both memory figures are printed, exit 0. If the UI exceeds 140 MiB, stop and report the measured figure — the ceiling is the spec's estimate (7.6) and changing it is an owner decision.

- [ ] **Step 4: Write `.github/workflows/ci.yml`**, modelled on `davcs86/webhooks-mcp`'s (same triggers, permissions, concurrency, action SHAs for `actions/checkout`):
  - `api`: `actions/setup-go` (pinned SHA, `go-version-file: api/go.mod`), `go vet ./...`, `gofmt -l .` must print nothing, `go test ./... -race`.
  - `ui`: `actions/setup-node` (pinned SHA, Node 24), `corepack enable`, `pnpm install --frozen-lockfile`, `pnpm check`, `pnpm lint`, `pnpm test`, `pnpm exec playwright install --with-deps chromium`, `pnpm test:e2e -- --trace off`.
  - `images` (needs both): build both with `--label org.opencontainers.image.revision=${GITHUB_SHA}`, run `scripts/smoke.sh`, `shellcheck scripts/smoke.sh`; then, only on a push to `main`, log in to GHCR with `GITHUB_TOKEN`, push `:<sha>` and `:main` for both, and write both `ghcr.io/davcs86/commsgun-…@sha256:…` references to the job summary.
- [ ] **Step 5: Write `CLAUDE.md`** on the pattern of `davcs86/webhooks-mcp/CLAUDE.md`: what the two containers are; commands for each (`cd api && go test ./... -race`; `cd ui && pnpm check && pnpm test && pnpm test:e2e`; the smoke command); rules — the frozen contracts in the two plans are a contract with `davcs86/homelab-edge`; constants only in `api/internal/constants`; the enqueue port is tailnet-only and the admin port is never published; no secret, body or address in a log; comments ≤ 2 lines; and a Release section stating that merging to `main` deploys nothing and that `homelab-edge`'s `bump-commsgun` workflow opens the pin PR. Replace `README.md` with: what it is, the enqueue contract with a `curl` example, configuration tables (spec 6.9, 7.5), local run instructions.
- [ ] **Step 6: Verify the workflow file** — `python3 -c "import yaml,sys; yaml.safe_load(open('.github/workflows/ci.yml'))"` exits 0, and every `uses:` is pinned to a 40-character SHA: `grep -n "uses:" .github/workflows/ci.yml | grep -vE "@[0-9a-f]{40}"` prints nothing.
- [ ] **Step 7: Commit and push** — `ci: build, smoke-test and publish both images; repository docs`. Confirm the PR's `api`, `ui` and `images` jobs are green before reporting the plan complete.
