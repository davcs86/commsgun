# commsgun (root) — Constitution Findings

Defects and drift surfaced by `/context-constitution` (context-forge) on 2026-11-15; last refreshed 2026-11-15 from branch
`claude/context-constitution-parity-zggeq3` at commit `6ca1c16` while deriving the constitution. These are for triage,
not governance: a defect is not a rule. This log is maintained (**CF-N12**): `/context-constitution refresh` re-verifies
every open row and retires the rows that no longer reproduce to `## Resolved`.

## Documentation that lies (docs claim behavior the code lacks)

| What the docs say | What the code does | Evidence | Suggested action |
|---|---|---|---|
| `docs/superpowers/plans/2026-11-13-commsgun-api.md#Tech Stack`: "go directive `1.24`" | `api/go.mod` declares `go 1.27` (`toolchain go1.27.4`) | `api/go.mod` | Historical plan; annotate or leave (owner) |
| `README.md` (Run locally): `cd ui && pnpm install && …` | `CLAUDE.md`, the Dockerfile and CI use `pnpm install --frozen-lockfile` | `README.md` ("pnpm install && pnpm check"), `.github/workflows/ci.yml` | Add `--frozen-lockfile` |

## Latent bugs (looks broken, not merely non-obvious)

| What the docs say / issue | Evidence (was) | Resolved | How confirmed |
|---|---|---|---|
| ⚠ "The enqueue log line records the caller-supplied `X-Commsgun-App` header even on a 401" | `api/internal/enqueue/handler.go#logInfo` | 2026-11-15 | False positive from the forge: re-checking `api/internal/enqueue/handler.go#ServeHTTP` shows `info.app = app.ID` (the config id) is set only after `Authenticate` succeeds; a 401 returns first and logs `app=""` (context-scrubber audit) |

## Dead / orphaned code

_None at the root._ Module rows: `api/docs/context-constitution-findings.md`, `docs/ui-context-constitution-findings.md`.

## Open questions (unresolved *why* — needs a maintainer)

- Dashboard probes spend app `ops`'s real quota (edge: email 2/min, 5/day). Intended? Status: **open**.

## Dismissed (won't fix)

_None._

## Resolved

| Was | Resolved | How confirmed |
|---|---|---|
| `CLAUDE.md` Rule 4 said "logs carry message ids only" | 2026-11-15, `docs(claude)` | Reworded to its intent (no secret, body, subject or address; ids and enumerated facts; provider text only once sanitised). Every `log.*` call in `api/` was checked against the new text. The poller's `err.Error()` comes from `resend.go#Status`, whose errors are fixed strings plus a pattern-checked name, so no address can reach it |

---
_Surfaced by [context-forge](https://github.com/davcs86/agent-plugins). Open items above are defects to
action, not rules to keep — nothing the scan found is discarded (CF-N8); resolved and dismissed items
stay on record rather than being deleted (CF-N12). Re-run `/context-constitution refresh` to catch
newly-resolved rows and pick up new defects._
