# commsgun api — Constitution Findings

Defects and drift surfaced by `/context-constitution` (context-forge) on 2026-11-15; last refreshed 2026-11-15 from branch
`claude/context-constitution-parity-zggeq3` at commit `6ca1c16` while deriving the constitution. These are for triage,
not governance: a defect is not a rule. This log is maintained (**CF-N12**): `/context-constitution refresh` re-verifies
every open row and retires the rows that no longer reproduce to `## Resolved`.

## Documentation that lies (docs claim behavior the code lacks)

| What the docs say | What the code does | Evidence | Suggested action |
|---|---|---|---|
| Spec §11 ("Left to the builder") leaves the SQLite migration mechanism to the builder | None was built: `initSchema` has only an exact version check | `api/internal/store/store.go#initSchema` | Build migrations before the first schema change (API-02) |

## Latent bugs (looks broken, not merely non-obvious)

| Issue | Impact | Evidence |
|---|---|---|
| `admin.fail` still logs the raw `err.Error()` of an unexpected store error (key fixed to `"error"`, 2026-11-15). `database/sql` Scan conversion errors quote the source value, so a type mismatch on a text column could put stored text in a log. The worker and poller log store errors the same way | Unlikely (fixed schema types), but outside Rule 4's intent if it fires | `api/internal/admin/handler.go#fail`, `api/internal/worker/worker.go#send` ("recording attempt failed") |

## Dead / orphaned code

_None open._ See `## Resolved`.


## Open questions (unresolved *why* — needs a maintainer)

- `store.go#translate` also matches the substring `"database is locked"` on top of the `sqlite.Error` code check.
  Which driver path produces that without a code? Status: **open**.
- The probe enqueues as `ops` without that app's key; the admin token is the only check (`admin/probe.go#probeSend`).
  Confirm this is intended. Status: **open**.

## Dismissed (won't fix)

_None._

## Resolved

| Was | Resolved | How confirmed |
|---|---|---|
| ⚠ `resend.go#Send` stored `ae.Name` raw into `attempts.provider_error` | 2026-11-15, `fix(provider)` | `Send` and `Status` share `safeErrorName` (`errorNamePattern` + `ProviderErrorNameMaxLen`); a failing name is stored as NULL. `TestResendSendSanitisesProviderErrorName` fails without the fix and passes with it |
| Admin filter errors returned prose in `error` | 2026-11-15, `fix(admin)` | Snake codes `invalid_instant`, `invalid_range`, `range_too_long`, `invalid_channel`, `invalid_state`, `filter_too_long`, `invalid_cursor`; `TestAdminBadParams` asserts each code and the exact two-key body. The UI maps admin GET failures by status only (`ui/src/lib/server/api.ts#call`), and the e2e stub already sent `invalid_cursor` |
| The `"range must not exceed 31 days"` literal duplicated `constants.AdminMaxRange` | 2026-11-15, `fix(admin)` | The prose is gone; the code is `range_too_long` |
| `DuePolls` read from the writer `s.w` (owner: accidental) | 2026-11-15, `fix(store)` | Reads `s.r`. `TestDuePollsReadsCommittedAttemptFromReadPool` shows a committed `RecordAttempt` on `s.w` is visible to the next `DuePolls` (WAL snapshot at read start) |
| Duplicated literals: `unknownFieldPrefix`, `"Idempotency-Key"` (`headerKey`/`headerIdemKey`), inline Retry-After math in `admin.fail` | 2026-11-15, `refactor(api)` | `enqueue.UnknownField`, `enqueue.HeaderIdempotencyKey` and `enqueue.RetryAfterSeconds` are the only copies; `TestAdminFailBusyAndInternal` pins `Retry-After: 1`. `provider/resend.go` keeps Resend's own outbound header (API-10) |
| `admin.fail` logged under `"err"` | 2026-11-15, `refactor(api)` | Logs under `"error"`; asserted in `TestAdminFailBusyAndInternal`. The raw-text half stays open above |
| `Overview` and `Backlog` ran several queries without one snapshot | 2026-11-15, `fix(store)` | Each runs inside one read-only `s.r` transaction, as `Stats` does; existing Overview/Backlog/health tests pass under `-race` |
| Exported `enqueue.Validate` used only by tests | 2026-11-15, `refactor(enqueue)` | Removed; `validate_test.go` keeps an unexported helper running the same `parse` + `validateRequest` path as `SubmitNamespaced` |
| Spec §6.5 doc-lie: a full provider window wrote no `provider_pauses` row | 2026-11-15, `feat(store)` (owner: implement) | `ProviderReserve` upserts `window_minute`/`window_day` until the window resets, in its writer transaction; `ProviderRelease` and startup (`ClearWindowPauses`) clear only `window_*` rows. Covered by `TestProviderReserveWindows`, `TestProviderReleaseKeepsProviderPause`, `TestClearWindowPausesKeepsProviderPauses`, `TestOverviewShowsWindowPause`; §6.5 names the reasons |
| Spec §6.6: `messages.source_ip` written but never read | 2026-11-15, `feat(admin)` (owner: expose, list and detail) | `source_ip` is in `model.Message`, `adminMessageCols`, `ui/src/lib/types.ts#Message` and `ui/e2e/stub-api.ts` (CG-01), and the plan's frozen `Message` is amended. `TestAdminEndpointsShape` asserts it on both endpoints; UI check, lint, unit and e2e pass |

---
_Surfaced by [context-forge](https://github.com/davcs86/agent-plugins). Open items above are defects to
action, not rules to keep — nothing the scan found is discarded (CF-N8); resolved and dismissed items
stay on record rather than being deleted (CF-N12). Re-run `/context-constitution refresh` to catch
newly-resolved rows and pick up new defects._
