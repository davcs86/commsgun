# commsgun api — Constitution

Derived by `/context-constitution` (context-forge) on 2026-11-15; last refreshed 2026-11-15 from branch
`claude/context-constitution-parity-zggeq3` at commit `6ca1c16` — the repo state this analysis reflects. This file
captures the **non-obvious** patterns of the Go module that no doc states. It does not restate `CLAUDE.md` or CI.

> Inherits all rules of the root constitution (`../../docs/context-constitution.md`, `CG-*`). This file lists only
> what is specific to **api/**.

## Floor (`API-*`) — never-do, non-overridable

| ID | Rule | Why | Evidence |
|---|---|---|---|
| **API-01** | Every terminal transition (`sent`, `failed`, `expired`) clears the bodies **in the same UPDATE** by appending the `clearBodies` const. A new path to a terminal state that writes only `state` is a defect. | Subjects can hold one-time codes. Bodies left behind stay on disk for 30 days, which breaks spec §6.4 and owner decision 14. Only tests enforce this. | `api/internal/store/dispatch.go#clearBodies`, used in `#RecordAttempt`, `#failTx`, `#ExpireDue`, `#ExpireClaimed` (×4) |
| **API-02** | Never edit `api/internal/store/schema.sql` for an existing table, or bump `schemaVersion`, without first building a migration mechanism. | `initSchema` runs `schema.sql` only when no version exists and refuses to start on a mismatch. A column change never reaches an existing `/data` volume, and a version bump makes the process exit. Owner decision (2026-11-14): build migrations before the first schema change. | `api/internal/store/store.go#initSchema`, `#schemaVersion`; owner answer |

## Rules (`API-*`) — binding, easy-to-miss conventions

| ID | Rule | Why | Evidence | Example |
|---|---|---|---|---|
| **API-03** | Writes go only to the single writer `s.w` (`SetMaxOpenConns(1)`, `_txlock=immediate`), in the shape `BeginTx` → deferred rollback on the named `err` → `translate(err)`. Reads go to the read-only pool `s.r`. | Without `translate`, a lock or full-disk error is not `ErrBusy`, so it surfaces as a 500 instead of a 503 with `Retry-After`. Writing through `s.r` fails at runtime (`mode=ro`). | `api/internal/store/store.go#Open`, `#translate`; `api/internal/store/enqueue.go#Enqueue`, `dispatch.go#ClaimDue`, `#ProviderReserve`, `#RecordAttempt`, `poll.go#advancePoll` (×5); consumers `api/internal/enqueue/submit.go#storeFailure`, `api/internal/admin/handler.go#fail` | `api/internal/store/enqueue.go#Enqueue` |
| **API-04** | State transitions are guarded: `UPDATE … WHERE id=? AND state=?` plus `affectedOne`/`affectedSent`, returning `ErrNotSending`/`ErrNotSent`. Never write an unguarded `WHERE id=?`. | An unguarded update races the lease or the sweep and can resurrect a finished message. The poller treats `ErrNotSent` as benign. | `api/internal/store/dispatch.go#Unclaim`, `#ExpireClaimed`, `poll.go#RecordPoll`, `#ClearPoll` (×4 + in-txn checks); `api/internal/worker/poller.go#poll` | `api/internal/store/dispatch.go#Unclaim` |
| **API-05** | Store methods never read a clock. They take `now time.Time` and persist `now.UTC().UnixMilli()`. `clock.Real{}` is constructed only in `main.go#runWith`; randomness is injected too (`rand.Float64` → `worker.New`). | `time.Now()` in store or worker code makes tests nondeterministic and breaks the stats bucket arithmetic. | `api/internal/clock/clock.go` (package doc), `api/cmd/commsgun-api/main.go#runWith`; `api/internal/store/enqueue.go#Enqueue`, `sweep.go#Sweep`, `poll.go#DuePolls`; `api/internal/worker/worker.go#Backoff` | `api/internal/store/sweep.go#Sweep` |
| **API-06** | Any write that follows a provider call uses `context.WithoutCancel(ctx)`. | After SIGTERM, an accepted send would otherwise go unrecorded, or a rate reservation would never be released. | `api/internal/worker/worker.go#send` ("bg := context.WithoutCancel"), `#dispatch` (release, `ExpireClaimed`), `api/internal/worker/poller.go#poll` (×3) | `api/internal/worker/worker.go#send` |
| **API-07** | Consumers depend on narrow interfaces they define themselves. An exported constructor takes the concrete `*store.Store`, and tests use hand-written fakes. Never widen the dependency or mock `*store.Store`. | Fits the existing fakes; a wide interface forces every fake to grow. | `api/internal/enqueue/handler.go#Enqueuer`, `api/internal/worker/worker.go#dispatchStore`, `api/internal/worker/poller.go#pollStore`, `api/internal/health/health.go#Pinger` (×4); `api/internal/enqueue/helpers_test.go#fakeStore` | `api/internal/worker/worker.go#dispatchStore` |
| **API-08** | Rate-counter and pause keys are composite **scope strings**: `app:<app>:<channel>` and `provider:<channel>:<name>`. `provider_pauses.provider` stores the scope, not the provider name. Windows come only from `enqueue.go#windowsAt` (UTC minute / UTC day). | `WHERE provider='resend'` silently matches nothing. | `api/internal/store/enqueue.go#Enqueue` ("app:%s:%s"), `api/internal/store/dispatch.go#providerScope`, `#PauseProvider`, `api/internal/store/query.go#Backlog`, `api/internal/store/query_admin.go#Overview` | `api/internal/store/dispatch.go#providerScope` |
| **API-09** | A new admin write path enqueues through `SubmitNamespaced` (keys stored as `<ns>/<key>`), never `Submit`. | `/` is outside `keyPattern`, so namespaced keys cannot collide with the real `ops` client's idempotency keys. | `api/internal/enqueue/submit.go#SubmitNamespaced`, `#namespaceSep`; `api/internal/admin/probe.go#probeSend` | `api/internal/admin/probe.go#probeSend` |
| **API-10** | The Resend `Idempotency-Key` is the message id, and the send window is bounded by `SendDeadline` (24 h − `IdempotencySafetyMargin`). Never raise `MaxSendWindow` or the margin past Resend's 24 h memory, and never change the key. | Past 24 h, a retry sends the message twice. | `api/internal/provider/resend.go#Send`, `api/internal/store/dispatch.go#SendDeadline`, `api/internal/worker/worker.go#dispatch`, `dispatch.go#ExpireDue` | — |

## Norms (`API-*`) — defaults & asymmetry guidance

| ID | Norm | Why | Evidence | Example |
|---|---|---|---|---|
| **API-11** | Error responses never carry request data. The enqueue path and admin's fixed errors use `{"error":"<snake_code>"[,"field"|"window"]}`. Admin filter validation returns a prose message in `error` (an inconsistency logged in the findings). Reuse `enqueue.WriteFailure` / `admin.fail`. Parse bearer tokens case-insensitively the way enqueue does; admin's exact `"Bearer "` is the outlier. | Consistency with the existing wire shape; the UI is admin's only client today. | `api/internal/enqueue/submit.go#WriteFailure`, `api/internal/admin/handler.go#fail`, `api/cmd/commsgun-api/main.go#notFound`; bearer: `api/internal/enqueue/handler.go#bearer` (norm) vs `api/internal/admin/handler.go#authorized` (outlier) | `api/internal/enqueue/submit.go#WriteFailure` |

## Gotchas & scars

- **Resend `Send` stores the provider error name raw.** `resend.go#Status` sanitises `ae.Name` through
  `errorNamePattern`/`ProviderErrorNameMaxLen`, but `#Send` stores it unsanitised into `attempts.provider_error`, which
  the admin API and UI display. Never log that field or reuse it in a log line. Evidence:
  `api/internal/provider/resend.go#Send`, `#Status`.
- **`sweep never deletes unfinished messages`** (commit `e608662`) and **an idempotent repeat returns the original
  result** (commit `8122f81`) are scars from the final-review fix waves. Keep both behaviours when touching
  `sweep.go` or `Enqueue`.

## Candidate rules (unverified)

| Candidate | Why suspected | What would confirm it |
|---|---|---|
| `outcomeName` mapping `Throttled` → `permanent` is unreachable and would mislabel logs if the early return in `send` moved | `api/internal/worker/worker.go#outcomeName` | A test removing the early return |

## Pointers

| What | Where |
|---|---|
| Limits only in `constants.go`; logs carry ids only; constant-time secrets | root `CLAUDE.md#Rules` 2, 4, 5 |
| Lifecycle, retries, storage, admin API | `docs/superpowers/specs/2026-11-13-commsgun-design.md` §6 |
| `fake` provider requires `COMMSGUN_ALLOW_FAKE=1` | `README.md` Configuration, `api/internal/config/config.go#buildProviders` |

---
_Forged by [context-forge](https://github.com/davcs86/agent-plugins). It captures the
non-obvious — nothing here is invented; re-run `/context-constitution` to refresh after the code changes._
