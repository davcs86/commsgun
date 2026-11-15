# commsgun (root) — Constitution Findings

Defects and drift surfaced by `/context-constitution` (context-forge) on 2026-11-15 from branch
`claude/context-constitution-parity-zggeq3` at commit `e139e1b` while deriving the constitution. These are for triage,
not governance: a defect is not a rule. This log is maintained (**CF-N12**): `/context-constitution refresh` re-verifies
every open row and retires the rows that no longer reproduce to `## Resolved`.

## Documentation that lies (docs claim behavior the code lacks)

| What the docs say | What the code does | Evidence | Suggested action |
|---|---|---|---|
| `CLAUDE.md` Rule 4: "logs carry message ids only" | The enqueue log also carries `app`, `channel`, the recipient count, `request_id`, `status` and `duration_ms`; the poller logs the provider `err.Error()` text | `api/internal/enqueue/handler.go#logInfo` ("h.log.Info(\"enqueue\""), `api/internal/worker/poller.go#poll` ("status poll failed") | Reword Rule 4 to its intent (no secret, body, subject or address; ids and enumerated request facts only), and check whether provider error text can carry an address |
| `docs/superpowers/plans/2026-11-13-commsgun-api.md#Tech Stack`: "go directive `1.24`" | `api/go.mod` declares `go 1.27` (`toolchain go1.27.4`) | `api/go.mod` | Historical plan; annotate or leave (owner) |
| `README.md` (Run locally): `cd ui && pnpm install && …` | `CLAUDE.md`, the Dockerfile and CI use `pnpm install --frozen-lockfile` | `README.md` ("pnpm install && pnpm check"), `.github/workflows/ci.yml` | Add `--frozen-lockfile` |

## Latent bugs (looks broken, not merely non-obvious)

| Issue | Impact | Evidence |
|---|---|---|
| ⚠ The enqueue log line records the caller-supplied `X-Commsgun-App` header even on a 401 | An unauthenticated client writes arbitrary text into the log | `api/internal/enqueue/handler.go#logInfo` |

## Dead / orphaned code

_None at the root._ Module rows: `api/docs/context-constitution-findings.md`, `docs/ui-context-constitution-findings.md`.

## Open questions (unresolved *why* — needs a maintainer)

- Dashboard probes spend app `ops`'s real quota (edge: email 2/min, 5/day). Intended? Status: **open**.

## Dismissed (won't fix)

_None._

## Resolved

_None._

---
_Surfaced by [context-forge](https://github.com/davcs86/agent-plugins). Open items above are defects to
action, not rules to keep — nothing the scan found is discarded (CF-N8); resolved and dismissed items
stay on record rather than being deleted (CF-N12). Re-run `/context-constitution refresh` to catch
newly-resolved rows and pick up new defects._
