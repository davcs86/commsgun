<!-- context-forge:behavioral-contract:start -->
## How to Act

Read this first — it governs *how* you work here; everything below is the *what* you work with.
These four behaviors are the operating defaults; the rest of this file (and the constitution) is
context you load per task.

1. **Don't assume — ask, and surface tradeoffs.** Before acting, always list every assumption you
   are making on your own — scope, interpretation, defaults, a choice between options — each with
   its basis, or say "none". On ambiguity, a missing detail, or a design fork, stop and raise it.
   *Red flag:* "it's obvious" / "too small to ask" — that *is* the assumption; list it.
   *(Enforced by the frozen contracts K1–K6/U1–U4 and the seams **CG-01/CG-02/CG-05/CG-08**, where a one-sided
   guess breaks the UI or the edge.)*
2. **Write the minimum that solves the stated problem.** Nothing speculative — no abstraction,
   option, or "while I'm here" scaffolding. *Red flag:* "might need it later", "for flexibility".
   *(Enforced by **API-07** narrow interfaces and the UI devDependency budget in the root gotchas.)*
3. **Touch only what the task requires.** Surgical, auditable diffs; clean up only orphans *you*
   introduced. *Red flag:* "cleaner if I also…" — propose it, don't do it.
   *(Enforced by **API-01/API-02/API-04** and **CG-04**: store, schema and pinned-adapter edits ripple.)*
4. **Define success up front, then loop until verified.** State the pass condition before you
   start; claim done only with the check's output from this session in hand.
   *Red flag:* "should work", "looks right", "probably passes".
   *(Enforced by the `Commands` pipelines and `scripts/smoke.sh`; **CG-01** — the stub keeps UI tests green on
   a broken contract, so smoke is the real check.)*

No exceptions for small or obvious tasks — honoring the letter while dodging the intent is a violation.

> Litmus test for any future line in this file: *does it shape how the agent thinks (a behavior), or
> restate a fact the agent can read from the code?* If it's a fact already in the repo, leave it out.
<!-- context-forge:behavioral-contract:end -->
# commsgun — CLAUDE.md

Two containers built from one repo. `api/` (Go): enqueue listener 9200, admin listener 9201, SQLite under
`DATA_DIR` (`/data`), one worker. `ui/` (SvelteKit, Node adapter): admin dashboard on 9202 that
calls the admin API server-side; read-only except the hello-world probe (`POST /admin/v1/probes`, sent as app
`ops`). Design: `docs/superpowers/specs/2026-11-13-commsgun-design.md`.

<!-- context-forge:constitution-pointer:start -->
> **Constitutions:** cross-module and edge seams (`CG-*`: the three-copy admin JSON, mirrored limits, the
> proxy-header quartet, the exact adapter pin, probe error names, image publication) live in
> [`docs/context-constitution.md`](docs/context-constitution.md); module invariants in
> [`api/docs/context-constitution.md`](api/docs/context-constitution.md) (`API-*`) and
> [`docs/ui-context-constitution.md`](docs/ui-context-constitution.md) (`UI-*`; kept out of `ui/`, which is the UI
> image build context). Defects and doc drift are in each one's sibling `*-findings.md` file.
<!-- context-forge:constitution-pointer:end -->

## Commands

- API: `cd api && gofmt -l . && go vet ./... && GOTOOLCHAIN=$(go env GOVERSION) go run
  honnef.co/go/tools/cmd/staticcheck@v0.8.2 ./... && go test ./... -race` (`gofmt -l` must print nothing; staticcheck
  must run on the module's Go, not an older one)
- UI: `cd ui && pnpm install --frozen-lockfile && pnpm check && pnpm lint && pnpm test && pnpm test:e2e` (e2e needs
  `pnpm exec playwright install chromium`, or `PW_CHROMIUM_PATH` pointing at an existing Chromium)
- Images and smoke (needs Docker): `docker build -t commsgun-api:dev api && docker build -t commsgun-ui:dev ui`
  then `scripts/smoke.sh commsgun-api:dev commsgun-ui:dev`

## Rules

1. **Ports, paths, env var names, response codes, the admin JSON and the image names are a contract** with
   `davcs86/homelab-edge` (deployment) and the subscribers. The "Frozen contracts" tables in
   `docs/superpowers/plans/` are that contract; never change one here alone.
2. **API limits, caps and durations live only in `api/internal/constants/constants.go`.**
3. The enqueue port (9200) is tailnet-only; the admin port (9201) is never published to the host.
4. No secret, message body, subject or address in a log or an error string; logs carry message ids only.
5. Secrets (`ADMIN_PASSWORD`, `SESSION_SECRET`, `ADMIN_API_TOKEN`, app keys) are read only in server-only
   code, compared in constant time, and never appear in a fixture.
6. Keep base-image digest and `ci.yml` action SHA pins; bump them deliberately.
7. Comments state constraints only, at most 2 lines. How and why belong in the PR or the spec.

## Release

Release mechanics: `README.md#Release`. When you finish a change, say in your report
which image content changed (`api/` or `ui/` sources, Dockerfile, dependencies) and that the edge still needs the
bump PR merged and a deploy; do not open a bump PR by hand.
