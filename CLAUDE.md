# commsgun — CLAUDE.md

Two containers built from one repo. `api/` (Go): enqueue listener 9200, admin listener 9201, SQLite under
`DATA_DIR` (`/data`), one worker. `ui/` (SvelteKit, Node adapter): read-only admin dashboard on 9202 that
calls the admin API server-side. Design: `docs/superpowers/specs/2026-11-13-commsgun-design.md`.

## Commands

- API: `cd api && gofmt -l . && go vet ./... && go test ./... -race` (`gofmt -l` must print nothing)
- UI: `cd ui && pnpm check && pnpm lint && pnpm test && pnpm test:e2e` (e2e uses fixed ports 4173 and the stub API)
- Images and smoke (needs Docker): `docker build -t commsgun-api:dev api && docker build -t commsgun-ui:dev ui`
  then `scripts/smoke.sh commsgun-api:dev commsgun-ui:dev`

## Rules

1. **Ports, paths, env var names, response codes, the admin JSON and the image names are a contract** with
   `davcs86/homelab-edge` (deployment) and the subscribers. The "Frozen contracts" tables in
   `docs/superpowers/plans/` are that contract; never change one here alone.
2. **Limits, caps and durations live only in `api/internal/constants/constants.go`.**
3. The enqueue port (9200) is tailnet-only; the admin port (9201) is never published to the host.
4. No secret, message body, subject or address in a log or an error string; logs carry message ids only.
5. Secrets (`ADMIN_PASSWORD`, `SESSION_SECRET`, `ADMIN_API_TOKEN`, app keys) are read only in server-only
   code, compared in constant time, and never appear in a fixture.
6. Base images are pinned by digest in the Dockerfiles and `ci.yml` actions by commit SHA; bump deliberately.
7. Comments state constraints only, at most 2 lines. How and why belong in the PR or the spec.

## Release

Merging to `main` deploys **nothing**. CI builds both images, smoke-tests them together, pushes
`ghcr.io/davcs86/commsgun-api` and `ghcr.io/davcs86/commsgun-ui` (`:<sha>`, plus the moving tag `main`) and
prints both `@sha256:…` references in the `images` job summary. The edge keeps running the digests pinned in
`davcs86/homelab-edge` until they are changed there.

A change is live only after a second PR, in `davcs86/homelab-edge`: its `bump-commsgun` workflow opens it
(daily, or at once from **Actions → bump-commsgun → Run workflow**), and after it merges the owner runs
**Actions → deploy** there. Every merge to `main` publishes new digests, so a docs- or CI-only merge also
produces a bump PR that changes nothing on the edge.

When you finish a change, say in your report which image content changed (`api/` or `ui/` sources, Dockerfile,
dependencies) and that the edge still needs the bump PR merged and a deploy; do not open a bump PR by hand.
