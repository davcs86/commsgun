# commsgun ui — Constitution Findings

Defects and drift surfaced by `/context-constitution` (context-forge) on 2026-11-15; last refreshed 2026-11-15 from branch
`claude/context-constitution-parity-zggeq3` at commit `6ca1c16` while deriving the constitution. These are for triage,
not governance: a defect is not a rule. This log is maintained (**CF-N12**): `/context-constitution refresh` re-verifies
every open row and retires the rows that no longer reproduce to `## Resolved`.

## Documentation that lies (docs claim behavior the code lacks)

_None open._

## Latent bugs (looks broken, not merely non-obvious)

_None open._

## Dead / orphaned code

_None._

## Open questions (unresolved *why* — needs a maintainer)

_None open._

## Dismissed (won't fix)

_None._

## Resolved

| What the docs say / issue | Evidence (was) | Resolved | How confirmed |
|---|---|---|---|
| ⚠ Unverified whether SvelteKit's server-only guard fires for imports through the `#lib/server` subpath alias | `ui/src/lib/components/FilterSheet.svelte` (type import from `#lib/server/api.js`), `ui/package.json` (`imports`) | 2026-11-15 | Not a bug: the guard fires. Kit 3.1.1 `plugins/guard.js` tests the *resolved* path for `/server/`, not the specifier. Temporarily made `FilterSheet.svelte` import and use `createApi` from `#lib/server/api.js`: `pnpm build` failed with `server_only_import` (chain `src/routes/+page.svelte` → `FilterSheet.svelte` → `#lib/server/api.ts`), and `vite dev` answered 500 with the same error for `/src/lib/server/api.ts`. Type-only imports are erased (baseline build passes). Reverted; no code change, UI-08 stands |
| Spec §7.2 omits `/logout` from the unguarded routes | `ui/src/hooks.server.ts#PUBLIC_PATHS` | 2026-11-15 | §7.2 now lists `/login`, `/logout` and `/healthz` |
| Spec §7.5 lists only the password-length start refusal | `ui/src/lib/server/env.ts#isHttpUrl` | 2026-11-15 | §7.5 `API_BASE_URL` and `ORIGIN` rows document the http(s) start check |
| `e2e/login.spec.ts` hardcodes `'commsgun_session'` | `ui/e2e/login.spec.ts` | 2026-11-15 | Imports `SESSION_COOKIE` from `src/lib/server/constants.js`; login specs pass |
| The stub's `ENDPOINTS` excludes `probes` and `messages/<id>` | `ui/e2e/stub-api.ts#ENDPOINTS` | 2026-11-15 | Added `message` (`messages/<id>`) and `probes` (GET and POST); `messages` no longer also fails the detail. New e2e: "with only the detail endpoint down…" (`messages.spec.ts`) and "with only probes down…" (`probe.spec.ts`) pass |
| `engines` `node >=22` and `@types/node ^22` vs Node 24 in the image and CI | `ui/package.json`, `ui/Dockerfile` | 2026-11-15 | `engines.node >=24`, `@types/node ^24` (24.19.2). Full UI pipeline green on Node 24.22.3; with `engine-strict=true` Node 22 now refuses `pnpm install` |
| UI size limits lived outside the UI constants files (`env.ts` `MIN_*`, `probe.ts` `PROBE_KEY` 64, `PROVIDER_NAME` 32) | `ui/src/lib/server/env.ts`, `ui/src/lib/probe.ts` | 2026-11-15 | Moved unchanged: `MIN_ADMIN_PASSWORD`/`MIN_SESSION_SECRET`/`MIN_ADMIN_API_TOKEN` to `ui/src/lib/server/constants.ts`, `PROBE_KEY`/`PROVIDER_NAME` to `ui/src/lib/constants.ts`; CG-02 evidence updated. `api.ts#MESSAGE_ID` (same 64-char alphabet, CG-05) left in place (out of scope for this change). UI pipeline green |
| Open question: does the npm `cn` (`^0.5.2`) do the Tailwind conflict merging `components/ui/**` relies on? | `ui/src/lib/utils.ts` | 2026-11-15 | Yes. It is shadcn's drop-in `clsx` + `tailwind-merge` replacement (package README); in Node, `cn('px-2 py-1','px-4')` → `py-1 px-4`, `cn('bg-red-500','bg-blue-500')` → `bg-blue-500`, `cn('text-sm text-muted-foreground','text-lg')` → `text-muted-foreground text-lg` |
| Open question: is the re-encode compare in `verifySession` meant to reject malleable base64url? | `ui/src/lib/server/session.ts#verifySession` | 2026-11-15 | Yes, and it is load-bearing: Node's lenient `base64url` decode maps a flipped unused last-char bit, a trailing `=` and an injected `!` to the same 32 bytes, so without it several cookie strings would verify for one signature. Already pinned by `session.test.ts` (`'non-canonical signature'` case): deleting the line fails that test |
| Open question: is `secure: true` on the login cookie intended while e2e runs on plain-http 127.0.0.1? | `ui/src/routes/login/+page.server.ts`, `ui/playwright.config.ts` | 2026-11-15 | Intended: spec §7.2 requires `Secure`, and `e2e/login.spec.ts` ("set a hardened cookie") asserts `secure: true`. It works over http because Chromium treats 127.0.0.1 as a secure context. Moving e2e off loopback or off Chromium would break login tests, not production (Caddy serves https) |

---
_Surfaced by [context-forge](https://github.com/davcs86/agent-plugins). Open items above are defects to
action, not rules to keep — nothing the scan found is discarded (CF-N8); resolved and dismissed items
stay on record rather than being deleted (CF-N12). Re-run `/context-constitution refresh` to catch
newly-resolved rows and pick up new defects._
