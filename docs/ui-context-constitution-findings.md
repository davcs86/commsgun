# commsgun ui — Constitution Findings

Defects and drift surfaced by `/context-constitution` (context-forge) on 2026-11-15 from branch
`claude/context-constitution-parity-zggeq3` at commit `e139e1b` while deriving the constitution. These are for triage,
not governance: a defect is not a rule. This log is maintained (**CF-N12**): `/context-constitution refresh` re-verifies
every open row and retires the rows that no longer reproduce to `## Resolved`.

## Documentation that lies (docs claim behavior the code lacks)

| What the docs say | What the code does | Evidence | Suggested action |
|---|---|---|---|
| Spec §7.2: "A server hook guards every route except `/login` and `/healthz`" | `/logout` is public too | `ui/src/hooks.server.ts#PUBLIC_PATHS` | Add `/logout` to §7.2 |
| Spec §7.5 lists only the password-length start refusal | Start also fails on a non-http(s) `API_BASE_URL`/`ORIGIN` | `ui/src/lib/server/env.ts#isHttpUrl` | Document it |

## Latent bugs (looks broken, not merely non-obvious)

| Issue | Impact | Evidence |
|---|---|---|
| ⚠ It is unverified whether SvelteKit's server-only-module guard fires for imports through the `#lib/server` subpath alias. Client code already imports a *type* from `#lib/server/api.js` | A future non-type import from client code could bundle the token-handling module into the browser. Owner (2026-11-15): record as a finding to test | `ui/src/lib/components/FilterSheet.svelte` ("#lib/server/api.js"), `ui/package.json` (`imports`) |
| `e2e/login.spec.ts` hardcodes `'commsgun_session'` instead of importing `SESSION_COOKIE` | Renaming the cookie silently weakens its "cookie is undefined" assertions | `ui/e2e/login.spec.ts` ("const SESSION_COOKIE = 'commsgun_session'") |
| The stub's `ENDPOINTS` excludes `probes` and `messages/<id>` | `fail[]` cannot simulate those endpoints failing alone | `ui/e2e/stub-api.ts#ENDPOINTS` |
| UI size limits live outside the UI constants files (`env.ts` `MIN_*`, `probe.ts` `PROBE_KEY` 64, `PROVIDER_NAME` 32) | Drift from their API twins is harder to spot (CG-02) | `ui/src/lib/server/env.ts#MIN_ADMIN_API_TOKEN`, `ui/src/lib/probe.ts#PROBE_KEY` |
| `package.json#engines` says `node >=22` and `@types/node` is `^22`, but the image and CI run Node 24 | Low risk; misleading | `ui/package.json`, `ui/Dockerfile` |

## Dead / orphaned code

_None._

## Open questions (unresolved *why* — needs a maintainer)

- `ui/src/lib/utils.ts` re-exports `cn` from the npm package `cn` (devDep `^0.5.2`), not shadcn's
  `clsx` + `tailwind-merge`. Does it do the Tailwind conflict merging the generated `components/ui/**` relies on?
  Status: **open**.
- `ui/src/lib/session.ts#verifySession` re-encodes the signature and compares it to the input before
  `timingSafeEqual`. Is that meant to reject malleable base64url? Status: **open**.
- The login cookie is set `secure: true` while e2e runs on plain-http 127.0.0.1, relying on Chromium treating
  loopback as a secure context. Intended? Status: **open**.

## Dismissed (won't fix)

_None._

## Resolved

_None._

---
_Surfaced by [context-forge](https://github.com/davcs86/agent-plugins). Open items above are defects to
action, not rules to keep — nothing the scan found is discarded (CF-N8); resolved and dismissed items
stay on record rather than being deleted (CF-N12). Re-run `/context-constitution refresh` to catch
newly-resolved rows and pick up new defects._
