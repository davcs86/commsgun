# commsgun api — Constitution Findings

Defects and drift surfaced by `/context-constitution` (context-forge) on 2026-11-15 from branch
`claude/context-constitution-parity-zggeq3` at commit `e139e1b` while deriving the constitution. These are for triage,
not governance: a defect is not a rule. This log is maintained (**CF-N12**): `/context-constitution refresh` re-verifies
every open row and retires the rows that no longer reproduce to `## Resolved`.

## Documentation that lies (docs claim behavior the code lacks)

| What the docs say | What the code does | Evidence | Suggested action |
|---|---|---|---|
| Spec §6.5: "At the limit the provider is paused until the window turns over" | A full provider window writes no `provider_pauses` row: `ProviderReserve` returns `(until, reason)` and its only caller discards both, so the tick just ends and is re-checked every second | `api/internal/store/dispatch.go#ProviderReserve`, `api/internal/worker/worker.go#dispatch` ("ok, _, _, err :=") | Owner (2026-11-15): doc-lie, fix later. Implement the pause row or correct §6.5 |
| Spec §11 promises a "migration mechanism for the SQLite schema" | `initSchema` has only an exact version check | `api/internal/store/store.go#initSchema` | Build migrations before the first schema change (API-02) |
| Spec §6.6 lists `messages.source_ip` as stored data | It is written on every enqueue but never read (not in `messageCols`/`adminMessageCols` or the admin API) | `api/internal/store/enqueue.go#Enqueue` ("source_ip") | Expose it or stop writing it |

## Latent bugs (looks broken, not merely non-obvious)

| Issue | Impact | Evidence |
|---|---|---|
| `DuePolls` reads from the writer `s.w`, while every other read-only query uses `s.r` | Poll reads queue behind the single write connection. Owner (2026-11-15): accidental, should use the read pool | `api/internal/store/poll.go#DuePolls` ("s.w.QueryContext") |
| The `"range must not exceed 31 days"` literal duplicates `constants.AdminMaxRange` | The message drifts if the constant changes (Rule 2 in spirit) | `api/internal/admin/handler.go#filter` |
| Duplicated literals: `unknownFieldPrefix` in two packages; `"Idempotency-Key"` as `headerKey` and `headerIdemKey`; inline Retry-After math in `admin.fail` | Drift on change | `api/internal/enqueue/validate.go`, `api/internal/admin/probe.go#headerIdemKey`, `api/internal/admin/handler.go#fail` |
| `Overview` and `Backlog` run several queries without one read snapshot (`Stats` uses a read-only txn) | Numbers can be slightly inconsistent with each other | `api/internal/store/query_admin.go#Overview`, `api/internal/store/query.go#Backlog` |
| `admin.fail` logs under key `"err"` and logs raw `err.Error()`; everywhere else uses `"error"` | Inconsistent log schema; the raw text may carry detail Rule 4 forbids | `api/internal/admin/handler.go#fail` |

## Dead / orphaned code

| What | Why it looks dead | Evidence |
|---|---|---|
| Exported `enqueue.Validate` | Only `validate_test.go` calls it; production goes through `parse` + `validateRequest` in `SubmitNamespaced` | `api/internal/enqueue/validate.go#Validate` |

## Open questions (unresolved *why* — needs a maintainer)

- `store.go#translate` also matches the substring `"database is locked"` on top of the `sqlite.Error` code check.
  Which driver path produces that without a code? Status: **open**.
- The probe enqueues as `ops` without that app's key; the admin token is the only check (`admin/probe.go#probeSend`).
  Confirm this is intended. Status: **open**.

## Dismissed (won't fix)

_None._

## Resolved

_None._

---
_Surfaced by [context-forge](https://github.com/davcs86/agent-plugins). Open items above are defects to
action, not rules to keep — nothing the scan found is discarded (CF-N8); resolved and dismissed items
stay on record rather than being deleted (CF-N12). Re-run `/context-constitution refresh` to catch
newly-resolved rows and pick up new defects._
