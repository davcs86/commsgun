# Context Scrub — Findings

Low-signal context surfaced by `/context-scrubber` (mode `apply`) on 2026-11-15 from branch
`claude/context-constitution-parity-zggeq3` at commit `45dde77`, the repo state this audit reflects. Each row is a
line in an **auto-loaded** context file (`CLAUDE.md`), a generated constitution or findings file, or the opted-in
`README.md`. Every row cites both the context line and the evidence it fails.

**How the rows were produced.** One read-only `context-auditor` pass covered the eight files. The orchestrator
re-opened the evidence for every row carried here:

- the `enqueue` 401 path (`api/internal/enqueue/handler.go#ServeHTTP`)
- `admin/handler.go#fail`
- the relative imports in `ui/src/lib/server/`
- spec §11's heading
- the five scar commits, all of which exist

Sizes come from the auditor (lines exact, characters ±5%).

## Summary

| Category | Failing lines | Lines | Characters | Tokens (only if measured) |
|---|---|---|---|---|
| Stale citations | 1 | 1 | — | — |
| Restated (agent reads for free) | 3 | 4 | ≈ 520 | — |
| Cross-file duplication | 3 | 3 | ≈ 380 | — |
| Contradicted by code | 4 | — | — | — |
| Should be just-in-time | 0 | — | — | — |
| Brittle / over-specified | 0 | — | — | — |
| Bloat / low-value prose | 1 (keep-but-verify) | — | — | — |
| **Removable total** (excludes keep-but-verify + contradicted) | 5 | 6 | ≈ 760 | ≈ 190 (`chars ÷ 4`) |
| Keep-but-verify (unconfirmed) | 6 | — | — | — |

## Stale citations

| Context line | Citation it makes | Reality | Suggested action |
|---|---|---|---|
| `docs/ui-context-constitution-findings.md#Open questions`: "`ui/src/lib/session.ts#verifySession` …" | `ui/src/lib/session.ts` | the file is `ui/src/lib/server/session.ts` (the symbol exists) | re-ground at the step-3 refresh |

## Restated facts (agent reads for free) — fails CF-N4

| Context line | What restates it (free to read) | Why it fails | Suggested action |
|---|---|---|---|
| `api/docs/context-constitution.md#Gotchas & scars`: "`/healthz/backlog` is mounted only on the enqueue listener…" | `api/cmd/commsgun-api/main.go#runWith`, `main_test.go` ("backlog on admin listener"), plan K1/K2 | visible in the one file an agent would edit, and tested | remove |
| `docs/context-constitution.md` CG-04: "(`6.0.1`, `3.1.1`)" | `ui/package.json` | the numbers go stale on every bump; the rule is "exact, never caret" | trim the numbers |
| `docs/context-constitution.md` CG-07: "…and both carry `org.opencontainers.image.revision=$GITHUB_SHA`" | plan U3, `.github/workflows/ci.yml` comment ("Both :<sha> tags exist before…") | already stated | **kept**: the ordering and label are what the edge rejection depends on, and trimming would leave the rule unreadable |

## Cross-file duplication — CF-N3

| Context line | Duplicate location(s) | Which copy to keep | Suggested action |
|---|---|---|---|
| `docs/context-constitution.md#Candidate rules`: "Dashboard probes are meant to spend app `ops`'s real quota…" | `docs/context-constitution-findings.md#Open questions` | findings | remove from the constitution |
| `CLAUDE.md#Release`: "Merging to `main` deploys **nothing**; it only publishes new image digests, and the edge goes live only after `davcs86/homelab-edge` bumps and deploys them" | `README.md#Release` (cited) | README for the mechanism; CLAUDE.md keeps the directive | trim to the pointer and the directive |
| `docs/context-constitution.md` CG-03 "Why": "adapter-node 6 derives the origin from the headers (U2)" | plan U2, `README.md#Configuration` | the plan | **kept**: one clause; it is the reason the three-site rule exists |
| `README.md#Run locally` repeats `CLAUDE.md#Commands` (and drifts) | `CLAUDE.md#Commands` | `CLAUDE.md` | **no action**: the README is the human surface; the drift is a findings row |

## Contradicted by code

Defects (CF-N9), routed to the step-3 `/context-constitution refresh`. Never `apply`-deleted.

| Context line | What the code does | Evidence | Suggested action |
|---|---|---|---|
| `docs/context-constitution-findings.md#Latent bugs`: "⚠ The enqueue log line records the caller-supplied `X-Commsgun-App` header even on a 401" | **Does not reproduce.** `info.app = app.ID` (the config id) is set only after `Authenticate` succeeds; a 401 returns first and logs `app=""` | `api/internal/enqueue/handler.go#ServeHTTP` ("info.app = app.ID") | retire to `## Resolved` (a false positive from step 1) |
| `api/docs/context-constitution.md` API-11: "Error responses are `{"error":"<snake_code>"…}`" | `admin.fail` returns `pe.msg` prose ("range must not exceed 31 days") as `error` | `api/internal/admin/handler.go#fail` | re-scope API-11, and log a findings row |
| `docs/ui-context-constitution.md` UI-08: "`hooks.server.ts`'s relative extensionless import is the outlier" | `ui/src/lib/server/env.ts` and `session.ts` are also relative and extensionless; lib-internal modules use relative `./x.js` | `ui/src/lib/server/env.ts` ("from './session'") | re-scope UI-08 to routes and components |
| `CLAUDE.md#Rules` 4: "logs carry message ids only" | logs app, channel, counts, request_id, status, duration and the provider error text | `api/internal/enqueue/handler.go#ServeHTTP`, `api/internal/worker/poller.go#poll` | already in root findings |

## Should be just-in-time (pre-loaded → pointer)

_None._

## Brittle / over-specified (anti-altitude)

_None._ UI-06 is borderline (seven directives in one row).

## Bloat / low-value prose

_None removable._ The repeated provenance headers and footers are context-forge machinery (see keep-but-verify).

## Context budget (file-level)

| File | Measured lines | Measured characters | Over soft budget? |
|---|---|---|---|
| `docs/context-constitution.md` | 70 | ≈ 8,900 | yes (on demand) |
| `api/docs/context-constitution.md` | 64 | ≈ 7,600 | yes (on demand) |
| `docs/ui-context-constitution.md` | 58 | ≈ 6,300 | yes (on demand) |
| `README.md` | 102 | ≈ 5,200 | yes (opt-in human doc) |
| `CLAUDE.md` | 75 | ≈ 4,900 | yes (≈ 2,300 of that is the sentinel spans) |
| findings ×3 | 42–51 | ≈ 2,900–4,000 | yes (on demand) |

## Silent skills (weak trigger surface)

_None._ Scanned: 0 · excluded as symlinked-out: 0.

## Keep-but-verify (unconfirmed — CF-1)

- `api/docs/context-constitution.md#Gotchas`: "Resend `Send` stores the provider error name raw" (confirmed in
  `api/internal/provider/resend.go#Send`). This is unfiltered third-party text shown in the admin API and UI, so
  it is arguably a latent defect with no findings row. Proposed for the step-3 refresh. Status: **unverified (owner)**.
- `api/docs/context-constitution-findings.md`: "Spec §11 promises a migration mechanism". §11 is "Left to the
  builder", so reword it to "left to the builder; none was built". Proposed for the step-3 refresh.
- CG-09 (distroless → `docker inspect`) may be inferable from `scripts/smoke.sh#api_user`. Status: **unverified**.
- The cross-repo anchors in CG-02 and CG-05–CG-08 were verified from the homelab-edge checkout during the step-1
  scan, not by this auditor. Status: **verified by orchestrator**.
- The provenance header and footer repeated across the six generated files are context-forge machinery, not
  scrubbable here. Status: **out of scope**.
- `CLAUDE.md` intro restates ports and `DATA_DIR`. Kept as a short orientation. Status: **kept**.

## Protected blocks (reported, never trimmed)

| Block | Location | Marker |
|---|---|---|
| behavioral contract | `CLAUDE.md` (by marker) | `context-forge:behavioral-contract` |
| constitution pointer | `CLAUDE.md` (by marker) | `context-forge:constitution-pointer` |

---
_Surfaced by [context-forge](https://github.com/davcs86/agent-plugins). These are low-signal lines to trim, not
rules to keep — nothing grounded is dropped (**CF-N8**). Re-run `/context-scrubber` to re-audit._
