# commsgun (root) — Constitution

Derived by `/context-constitution` (context-forge) on 2026-11-15; last refreshed 2026-11-15 from branch
`claude/context-constitution-parity-zggeq3` at commit `6ca1c16` — the repo state this analysis reflects; a later
`refresh` reruns against a newer commit. This file captures the **non-obvious**: the seams between `api/` and `ui/`,
and between this repo and `davcs86/homelab-edge`, that no doc states, plus the scars behind them. It does **not**
restate `CLAUDE.md`, the plans' "Frozen contracts" tables (K1–K6, U1–U4) or CI (see `## Pointers`). Module-specific
rules live in `api/docs/context-constitution.md` (`API-*`) and `docs/ui-context-constitution.md` (`UI-*`, kept out of `ui/` so it never enters the UI image build context).

> **ID scheme (CF-N5).** `K*`/`U*` in `docs/superpowers/plans/` are frozen wire contracts. The codebase invariants
> below use the sibling namespace `CG-*` and cite `K*`/`U*` where they touch one.

## Floor (`CG-*`) — never-do, non-overridable

| ID | Rule | Why | Evidence |
|---|---|---|---|
| **CG-01** | Never change the admin JSON in `api/` alone. A field rename or addition updates **all three hand-written copies** in one PR: the Go producers, `ui/src/lib/types.ts` (and `ui/src/lib/probe.ts`), and the e2e stub `ui/e2e/stub-api.ts`. | There is no shared schema or codegen. UI unit and e2e tests run against the stub, so a Go-only change stays green everywhere except the few strings `scripts/smoke.sh` checks in the CI `images` job. | `api/internal/store/query_admin.go#Stats`, `#Overview`, `#Facets`, `#ProviderStatus`, `api/internal/model/model.go#Message`, `api/internal/admin/handler.go#messages`, `api/internal/admin/probe.go#probeSend` → `ui/src/lib/types.ts#Stats`, `#Message`, `ui/src/lib/probe.ts#ProbeOptions`, `ui/e2e/stub-api.ts` |

## Rules (`CG-*`) — binding, easy-to-miss conventions

| ID | Rule | Why | Evidence | Example |
|---|---|---|---|---|
| **CG-02** | Limits are mirrored across modules and across repos with no single source. When one changes, change its twins: secret minimums (`api` `MinSecretLen` 32 ↔ `ui` `server/constants.ts` `MIN_*` 16/32 ↔ the edge length assert), filter text (`AdminFilterMaxLen` 200 ↔ `TEXT_FILTER_MAX_BYTES`), range (`AdminMaxRange` 31 d ↔ `MAX_CUSTOM_RANGE_MS` 30 d), and page size (`AdminPageSize` 50 ↔ stub `PAGE_SIZE`). | `CLAUDE.md` Rule 2 makes `constants.go` look like the only home. A Go-only change makes the UI truncate or block differently, or makes the edge reject a now-valid secret. | `api/internal/constants/constants.go#MinSecretLen`, `#AdminFilterMaxLen`, `#AdminMaxRange`, `#AdminPageSize`; `ui/src/lib/server/constants.ts#MIN_ADMIN_API_TOKEN`; `ui/src/lib/constants.ts#TEXT_FILTER_MAX_BYTES`, `#MAX_CUSTOM_RANGE_MS`; `ui/e2e/stub-api.ts#PAGE_SIZE`; homelab-edge `roles/stack/tasks/main.yml` ("Commsgun secrets are as long as the service requires") | — |
| **CG-03** | The adapter-node proxy-header quartet (`PROTOCOL_HEADER=x-forwarded-proto`, `HOST_HEADER=x-forwarded-host`, `ADDRESS_HEADER=x-forwarded-for`, `XFF_DEPTH=1`) is set identically in three places: the UI image, the Playwright test env, and every UI request in `scripts/smoke.sh`. A new UI curl or test context sends the `X-Forwarded-*` headers. | `hooks.server.ts` returns 400 "Bad origin" when the derived origin ≠ `ORIGIN`. Setting only `ORIGIN` does nothing: adapter-node 6 derives the origin from the headers (U2). | `ui/Dockerfile` ("PROTOCOL_HEADER=x-forwarded-proto"), `ui/playwright.config.ts#testEnv`, `scripts/smoke.sh#ui_curl`; `ui/src/hooks.server.ts#handle` | `ui/playwright.config.ts#testEnv` |
| **CG-04** | `@sveltejs/adapter-node` and `@sveltejs/kit` are pinned **exactly** because the origin and client-address checks depend on the adapter's header behaviour. Never caret them. A bump re-runs the origin e2e (`ui/e2e/origin-theme.spec.ts`) and smoke. | Owner answer 2026-11-15: the pin is load-bearing. If a future adapter honours `ORIGIN`, the hook's origin check silently becomes a no-op. | `ui/package.json` ("\"@sveltejs/adapter-node\": \"6.0.1\""), `ui/src/hooks.server.ts#handle` | — |
| **CG-05** | The probe is a cross-module contract by **error name**. The UI maps `sender_required`, `provider_mismatch`/`probe_app_missing` (409), any 403, `window` + `Retry-After`, and requires `message_ids[0]` to match `^[A-Za-z0-9-]{1,64}$`. The probe app id `ops` is a constant in `api/` and config on the edge. Renaming an error string, the 409/403 split, the message-id alphabet, or `ops` on one side breaks the probe; the stub mirrors the table, so e2e won't catch it. | Confirmed from both sides. | `api/internal/admin/probe.go#probeApp`, `#probeSend`, `api/internal/enqueue/submit.go#WriteFailure` → `ui/src/lib/server/api.ts#sendProbe`, `ui/e2e/stub-api.ts#handleProbe`; homelab-edge `commsgun_apps` id `ops` | — |
| **CG-06** | Edge-coupled budgets. `ShutdownTimeout` (10 s) stays under the edge's `stop_grace_period: 30s`. The smoke memory ceilings (`API_MEM_LIMIT_MIB=60`, `UI_MEM_LIMIT_MIB=140`) stay under the edge `mem_limit` (96m/192m); smoke itself runs at 256m, so it won't catch an edge OOM. Changing a ceiling is an owner decision (plan). | A raise on this side alone ships an image the edge kills. | `api/internal/constants/constants.go#ShutdownTimeout`, `scripts/smoke.sh` ("API_MEM_LIMIT_MIB=60"); homelab-edge `roles/stack/templates/compose.yml.j2#commsgun-api` | — |
| **CG-07** | Image publication is a contract with the edge bump bot. CI pushes `:<sha>` for **both** images before moving `:main`, and both carry `org.opencontainers.image.revision=$GITHUB_SHA`. Never publish one image alone, drop the label, or reorder the pushes. | `homelab-edge/scripts/bump_image_pin.py` (`--tag main`, `REVISION_LABEL`) and `bump-commsgun.yml` refuse a pair whose revisions differ. | `.github/workflows/ci.yml` ("Push to GHCR and print the digests"); homelab-edge `scripts/bump_image_pin.py#REVISION_LABEL` | — |
| **CG-08** | A new UI env var is added in three places: `ui/src/env.ts#variables` (declared lenient on purpose), `ui/src/lib/server/env.ts#loadEnv` (the real validation, run eagerly by `hooks.server.ts#init`), and the edge's "Commsgun dashboard environment" task. A new `COMMSGUN_CONFIG` key is added on the edge and in `api` together, because `config.Load` uses `DisallowUnknownFields`. | One-sided additions either never reach the container or make the API refuse to start. | `ui/src/env.ts#variables`, `ui/src/lib/server/env.ts#loadEnv`, `api/internal/config/config.go#Load`; homelab-edge `roles/stack/templates/commsgun-config.json.j2` | — |

## Norms (`CG-*`) — defaults & asymmetry guidance

| ID | Norm | Why | Evidence | Example |
|---|---|---|---|---|
| **CG-09** | Smoke checks that need a shell target the UI image only. The API image is distroless, so inspect it from outside (`docker inspect`), never with `docker exec`. | `scripts/smoke.sh#api_user` vs the UI `id -u` check | `scripts/smoke.sh#api_user` | `scripts/smoke.sh#api_user` |

## Gotchas & scars

- **XFF is read from opposite ends, and that is only safe behind Caddy.** The API takes the *first*
  `X-Forwarded-For` entry (K6, `api/internal/enqueue/submit.go#SourceIP`), while the UI takes the *last*
  (`XFF_DEPTH=1`). On the probe hop the UI sends a single address (`ui/src/lib/server/api.ts#sendProbe`). On the
  enqueue route, Caddy (no `trusted_proxies`) overwrites any inbound XFF, so first-entry is not spoofable. Never add
  `trusted_proxies` to `@commsgun_enqueue`, and never expose 9200 without Caddy. Evidence: homelab-edge
  `roles/stack/templates/Caddyfile.j2#@commsgun_enqueue`, confirmed from both sides.
- **UI packages are all devDependencies on purpose.** adapter-node bundles devDependencies into `build/` and keeps
  `dependencies` external. `pnpm add <lib>` (into `dependencies`) ships extra `node_modules` against the 140 MiB smoke
  ceiling. Evidence: `ui/package.json` (no `dependencies` key), `ui/Dockerfile` ("pnpm prune --prod").
- **Smoke greps UI copy and wire strings.** `'1 held'`, `commsgun_session`, `"kind":"queued"`, the probe's 403 for a
  foreign `Origin`, the 303 to `/login`, and `/admin/v1/stats` → 404 on 9200 are asserted by `scripts/smoke.sh`.
  Rewording any of them fails only the CI `images` job.

## Candidate rules (unverified)

| Candidate | Why suspected | What would confirm it |
|---|---|---|
| Dashboard probes are meant to spend app `ops`'s real quota (email 2/min, 5/day on the edge) | The probe enqueues as `ops` through `SubmitNamespaced` | The owner confirms, or a dedicated probe app |

## Pointers (already documented or CI-enforced — not restated here)

| What | Where |
|---|---|
| Ports, paths, env names, response codes, admin JSON, image names are frozen (K1–K6, U1–U4) | `CLAUDE.md#Rules` 1; `docs/superpowers/plans/*` "Frozen contracts" |
| API limits only in `constants.go`; 9200 tailnet-only, 9201 unpublished; no secret/body/address in logs; server-only constant-time secrets; digest and SHA pins | `CLAUDE.md#Rules` 2–6 |
| Design: lifecycle, retries, storage, admin API, probe | `docs/superpowers/specs/2026-11-13-commsgun-design.md`, `…-hello-world-probe-design.md` |
| CI gates: gofmt, vet, staticcheck v0.8.2, `go test -race`, pnpm check/lint/test/e2e, shellcheck, revision-label assert, smoke | `.github/workflows/ci.yml` |
| Release and bump flow | `README.md#Release`, `CLAUDE.md#Release` |

---
_Forged by [context-forge](https://github.com/davcs86/agent-plugins). It captures the
non-obvious — nothing here is invented; re-run `/context-constitution` to refresh after the code changes._
