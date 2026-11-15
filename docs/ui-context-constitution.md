# commsgun ui — Constitution

Derived by `/context-constitution` (context-forge) on 2026-11-15 from branch
`claude/context-constitution-parity-zggeq3` at commit `e139e1b` — the repo state this analysis reflects. This file
captures the **non-obvious** patterns of the SvelteKit dashboard that no doc states. It does not restate
`CLAUDE.md`, the spec or CI.

> Inherits all rules of the root constitution (`context-constitution.md`, `CG-*`). This file lists only what is
> specific to **ui/**. It lives under the root `docs/`, not `ui/docs/`, because `ui/Dockerfile`'s `COPY . .` would
> pull it into the image build context (owner decision 2026-11-15).

## Floor (`UI-*`) — never-do, non-overridable

| ID | Rule | Why | Evidence |
|---|---|---|---|
| **UI-01** | A new JSON mutation endpoint copies the explicit `Origin` check from `probe/+server.ts#POST`. | SvelteKit's built-in CSRF check covers form posts, not JSON bodies. | `ui/src/routes/probe/+server.ts#POST` ("request.headers.get('origin') !== env.origin") |

## Rules (`UI-*`) — binding, easy-to-miss conventions

| ID | Rule | Why | Evidence | Example |
|---|---|---|---|---|
| **UI-02** | Every admin API read goes through `createApi(getEnv(), fetch)`, built per load with SvelteKit's `fetch`. A new endpoint is added to the `Endpoint` union. Never call global `fetch(API_BASE_URL…)`. | The wrapper owns the bearer token, `AbortSignal.timeout(API_TIMEOUT_MS)`, the never-log-a-body `logFailure` shape, and `MESSAGE_ID` validation. The unit tests inject the fetch. | `ui/src/lib/server/api.ts#createApi`, `#logFailure`; `ui/src/routes/+page.server.ts#load`, `ui/src/routes/messages/+page.server.ts#load`, `ui/src/routes/messages/[id]/+page.server.ts#load`, `ui/src/routes/probe/+server.ts#GET` (×4) | `ui/src/routes/messages/+page.server.ts#load` |
| **UI-03** | API failures are returned as data (`ApiResult` `{ok:false, reason}`), and each page section renders its own `<ApiError>`, so one failed call never blanks the page. The only exception is `messages/[id]`, where `not_found` → `error(404)`. | `error(502)` or a throw replaces the whole page. e2e asserts partial failure. | `ui/src/routes/+page.svelte`, `ui/src/routes/messages/+page.svelte`, `ui/src/lib/components/FilterSheet.svelte`; `ui/e2e/overview.spec.ts` ("setScenario('normal', ['facets'])"); exception `ui/src/routes/messages/[id]/+page.server.ts#load` | `ui/src/routes/+page.svelte` |
| **UI-04** | Filters live only in the URL, and the clock is injected: `parseFilters(url.searchParams, new Date())`. `toApiParams` always sends absolute `from`/`to`, with text cut at 200 UTF-8 bytes. Never send `range=` to the API or read `Date.now()` in `filters.ts`. | The API takes absolute bounds only; injected time keeps the filter tests deterministic. | `ui/src/lib/filters.ts#parseFilters`, `#toApiParams`; `ui/src/routes/+page.server.ts#load` | `ui/src/lib/filters.ts#parseFilters` |
| **UI-05** | e2e auth is forged, not logged in. `beforeEach` signs the cookie with `deriveSessionKey` + `signSession` imported from `src/lib/server/session.js`, and specs import `SESSION_COOKIE`. Stub fixtures are `fake-`-prefixed and sized to `loadEnv`'s minimums (16/32/32). | Driving the login form burns the 5/min limiter. Shorter fixtures make the server refuse to start. | `ui/e2e/overview.spec.ts`, `ui/e2e/messages.spec.ts`, `ui/e2e/probe.spec.ts` (×3); `ui/e2e/stub-api.ts#STUB_TOKEN` | `ui/e2e/probe.spec.ts` |
| **UI-06** | `ui/e2e/stub-api.ts` is the only mock of the admin API, and its state is global, which forces `workers: 1`. Every spec starts with `setScenario('normal')`. A new endpoint goes into `handleAdmin` (and `ENDPOINTS` if it should be failable). Never add `page.route` mocks or raise workers. Every browser context sends `x-forwarded-proto: http` (CG-03); login tests use a unique `x-forwarded-for` per test. | Shared state gets flaky. Without the header, every request is 400 "Bad origin". | `ui/e2e/stub-api.ts#startStub`, `ui/playwright.config.ts` ("workers: 1", `extraHTTPHeaders`), `ui/e2e/origin-theme.spec.ts`, `ui/e2e/login.spec.ts` | `ui/e2e/overview.spec.ts` |
| **UI-07** | The probe idempotency key (`crypto.randomUUID()`) is regenerated **only after `queued`**, never on retry. `sendProbe` forwards `idempotency-key`, `x-forwarded-for: getClientAddress()` and the `provider` echoed from `GET /probe`. Session expiry inside `fetch('/probe')` is a 303 from the hook, which `ProbeDialog` detects via `res.redirected`. | A regenerated key sends a duplicate; dropping XFF breaks the API's per-address limit; a 401 instead of a 303 breaks the reload path. | `ui/src/lib/components/ProbeDialog.svelte#send`, `#load`; `ui/src/lib/server/api.ts#sendProbe`; `ui/src/hooks.server.ts#handle` | — |

## Norms (`UI-*`) — defaults & asymmetry guidance

| ID | Norm | Why | Evidence | Example |
|---|---|---|---|---|
| **UI-08** | Import through the Node subpath alias `#lib/*` with a `.js` extension, not `$lib/...`. | One style across N≥10 sites; `hooks.server.ts`'s relative extensionless import is the outlier. | `ui/package.json` (`imports`), `ui/src/routes/+page.server.ts`, `ui/src/routes/probe/+server.ts` vs `ui/src/hooks.server.ts` (outlier) | `ui/src/routes/probe/+server.ts` |

## Gotchas & scars

- **Fix waves hardened the redirect and limiter.** Dot-segment open redirect in `safeNext` (`cf23b21`), a bounded
  login limiter and a strict signature (`4ef7102`), and preserved form edits and filters on failure (`4c9a504`). Keep
  `safeNext` and the limiter bounds when touching login. Evidence: those commits, `ui/src/routes/login/+page.server.ts`.

## Candidate rules (unverified)

| Candidate | Why suspected | What would confirm it |
|---|---|---|
| The login limiter is safe only while 9202 stays bound to 127.0.0.1 behind Caddy (in memory, keyed by XFF) | `ui/src/routes/login/+page.server.ts#limiter` | homelab-edge compose port binding |

## Pointers

| What | Where |
|---|---|
| Server-only admin calls; read-only except the `ops` probe; secrets server-only, constant-time, never in fixtures | root `CLAUDE.md` intro, Rules 4–5 |
| Check pipeline and `PW_CHROMIUM_PATH` | root `CLAUDE.md#Commands` |
| Cookie flags, 12 h TTL, key derivation, 5/min login limit, 30 s overview refresh, proxy env | spec §7.2–7.5 |
| `components/ui/**` is generated shadcn code (ESLint-ignored); runes forced; `expect.requireAssertions` | `ui/eslint.config.js`, `ui/vite.config.ts` |

---
_Forged by [context-forge](https://github.com/davcs86/agent-plugins). It captures the
non-obvious — nothing here is invented; re-run `/context-constitution` to refresh after the code changes._
