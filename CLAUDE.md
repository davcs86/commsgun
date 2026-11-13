# commsgun — CLAUDE.md

Two containers built from one repo. `api/` (Go): enqueue listener 9200, admin listener 9201, SQLite under
`DATA_DIR` (`/data`), one worker. `ui/` (SvelteKit, Node adapter): admin dashboard on 9202 that
calls the admin API server-side; read-only except the hello-world probe (`POST /admin/v1/probes`, sent as app
`ops`). Design: `docs/superpowers/specs/2026-11-13-commsgun-design.md`.

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

Merging to `main` deploys **nothing**; it only publishes new image digests, and the edge goes live only after
`davcs86/homelab-edge` bumps and deploys them (`README.md#Release`). When you finish a change, say in your report
which image content changed (`api/` or `ui/` sources, Dockerfile, dependencies) and that the edge still needs the
bump PR merged and a deploy; do not open a bump PR by hand.
