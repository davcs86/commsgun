# commsgun Edge Deployment Implementation Plan (plan 3 of 3)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Run the two commsgun containers on the edge droplet, tailnet-only, with secrets from SOPS, health probes, and an automated digest-bump PR — the same mechanism that deploys `webhooks-mcp`.

**Architecture:** All changes are in `davcs86/homelab-edge` (Ansible only). Two compose services join the existing stack; two host matchers join Caddy's existing tailnet-only wildcard site; the stack role renders one `0600` env file per container from the inventory registry merged with SOPS values. It lands as **two PRs** because CI refuses a role that reads a SOPS key the committed secrets file does not hold yet.

**Tech Stack:** Ansible, Jinja2, Docker Compose, Caddy, Gatus, SOPS, GitHub Actions, Python 3 `unittest` and bash test scripts already in that repo.

**Spec:** `davcs86/commsgun` → `docs/superpowers/specs/2026-11-13-commsgun-design.md`, section 9. Read it first, then `homelab-edge/CLAUDE.md` — it binds every task here.

**Depends on:** plans 1 and 2 merged to `commsgun` `main` and both images published (their digests are pinned in Task 2). Task 1 has no such dependency and can start at once.

## Global Constraints

- `homelab-edge/CLAUDE.md` rules apply: never assume an owner value (use a `TODO(ask)` marker and report it); public surface stays 80/443/3478; no secret in the repo, a log or a commit message; every secret-handling task sets `no_log: true`; pin everything and record why in `docs/versions.md` in the same change; Ansible only; comments ≤ 2 lines; update any doc whose behaviour you change in the same PR.
- Nothing is added to the `*.ts:8443` site, to `edge_sites`, or to `policy/policy.hujson`.
- The admin port 9201 is never published to the host.
- Work on branch `ccr-e9883634-2xy3lb` in `homelab-edge`; PRs target `main`. The owner merges and deploys; workers never merge or deploy.
- Success is shown, not claimed: each task ends by running the named check and reporting its output.
- No test is skipped, disabled or weakened to get green.

## Frozen contracts consumed (from plans 1 and 2 — do not change)

| Item | Value |
|---|---|
| API image / UI image | `ghcr.io/davcs86/commsgun-api`, `ghcr.io/davcs86/commsgun-ui`; label `org.opencontainers.image.revision` |
| Ports | API enqueue 9200, API admin 9201 (compose network only), UI 9202 |
| API env | `COMMSGUN_CONFIG` (JSON), `RESEND_API_KEY`, `ADMIN_API_TOKEN` |
| UI env | `ADMIN_PASSWORD`, `SESSION_SECRET`, `ADMIN_API_TOKEN`, `API_BASE_URL=http://commsgun-api:9201`, `ORIGIN=https://<dashboard host>` (must equal exactly the origin browsers use; the server answers 400 otherwise) |
| UI proxy headers | The dashboard trusts `X-Forwarded-Proto`, `X-Forwarded-Host` and the last `X-Forwarded-For` entry. Caddy's `reverse_proxy` sets all three by default; do not strip them on the dashboard route, and never publish 9202 beyond `127.0.0.1` |
| Health | API `GET /healthz`, `GET /healthz/backlog` on 9200; UI `GET /healthz` on 9202 |
| Body cap | 262144 bytes on the enqueue route |
| Runtime | both containers uid 10001, read-only root; API data at `/data` (named volume) |

`COMMSGUN_CONFIG` shape is in plan 1, "Frozen contracts".

## Names introduced

| Kind | Name | Value |
|---|---|---|
| Inventory | `commsgun_enqueue_hostname` | `comms.{{ magicdns_base_domain }}` |
| Inventory | `commsgun_ui_hostname` | `commsgun.{{ magicdns_base_domain }}` |
| Inventory | `commsgun_enqueue_port`, `commsgun_ui_port` | `9200`, `9202` |
| Inventory | `body_limits.commsgun_enqueue` | `262144` |
| Inventory | `commsgun_providers`, `commsgun_apps` | registry, see Task 2 |
| Pins | `commsgun_api_image`, `commsgun_ui_image` | digest refs |
| SOPS, generated | `commsgun_admin_password`, `commsgun_session_secret`, `commsgun_admin_api_token`, `commsgun_app_key_xstockstrat`, `commsgun_app_key_ops` | `secrets.token_urlsafe(36)` each (48 chars) |
| SOPS, owner | `resend_api_key` | full-access Resend key |
| SOPS, owner | `commsgun_senders` | map: app id → `{senders: [..], default_sender: ..}` |

The spec calls the app keys `commsgun_app_keys`; they are flat `commsgun_app_key_<app>` keys here because `scripts/init_secrets.py` generates flat keys only.

## Review Focus

1. **A deploy while the owner has not yet added `resend_api_key` or `commsgun_senders`** → the stack role fails early with a message naming the missing key, before touching compose; it never renders an env file with an empty value. (Task 2)
2. **An app in the inventory registry with email enabled but no entry in `commsgun_senders`** (or the reverse) → the same early, named failure. (Task 2)
3. **A request for the dashboard or enqueue name arriving on the staging port 8443** → 404 from that site's default handle, never proxied. (Task 3)
4. **The enqueue name used for the dashboard's paths and vice versa** (`comms.ts…/login`, `commsgun.ts…/v1/messages`) → each name reaches only its own upstream. (Task 3)
5. **One image published and the other not** (a CI run that failed between the two pushes) → the bump workflow proposes nothing and says why; it never pins a mismatched pair. (Task 5)

---

## PR 1 — declare the secrets

### Task 1: Secret declarations

**Files:**
- Modify: `scripts/init_secrets.py` (the `GENERATED` dict and `OPERATOR_KEYS` list), `scripts/init-secrets-test.py`, `secrets/README.md`

**Interfaces:**
- Produces: the seven SOPS key names in "Names introduced".

- [ ] **Step 1: Extend the test first.** In `scripts/init-secrets-test.py`, add to the existing fill-missing test: after a fill, the five generated commsgun keys exist, each is 48 characters, and the two operator keys (`resend_api_key`, `commsgun_senders`) are **not** created.
- [ ] **Step 2: Run** `python3 scripts/init-secrets-test.py` → FAIL on the new assertions.
- [ ] **Step 3: Implement.** Add the five entries to `GENERATED` (`lambda: secrets.token_urlsafe(36)`) and the two operator names to `OPERATOR_KEYS`.
- [ ] **Step 4: Run** `python3 scripts/init-secrets-test.py && python3 scripts/secrets-keys-test.py` → PASS (no role reads the new keys yet).
- [ ] **Step 5: Document** in `secrets/README.md`: what each key holds; that the admin password is read with `sops decrypt --extract '["commsgun_admin_password"]'` and may be replaced with `sops set`; that `resend_api_key` must be a full-access key (bounce tracking reads email status); and the exact shape of `commsgun_senders` with a `sops set` example using placeholder addresses on `example.com`.
- [ ] **Step 6: Commit, push, open PR 1** titled `commsgun: declare its secrets`. Its body lists the owner's steps after merging:
  1. Run **Actions → init-secrets** with `fill_missing`.
  2. `sops set` `resend_api_key`.
  3. `sops set` `commsgun_senders` for apps `xstockstrat` and `ops`.

**Gate:** PR 2 is not opened until `python3 -c "import sys; sys.path.insert(0,'scripts'); import secret_keys; print(sorted(k for k in secret_keys.file_keys() if 'commsgun' in k or k=='resend_api_key'))"` on `origin/main` prints all seven keys.

---

## PR 2 — the deployment

### Task 2: Inventory, pins, env files, compose

**Files:**
- Modify: `inventories/prod/group_vars/all/main.yml`, `inventories/prod/group_vars/all/versions.yml`, `docs/versions.md`, `roles/stack/tasks/main.yml`, `roles/stack/templates/compose.yml.j2`, `roles/tailscale/tasks/main.yml` (the `_names` list at line 84)
- Create: `scripts/commsgun-config-test.py`; register it in `.github/workflows/ci.yml` beside the Gatus config test

**Interfaces:**
- Consumes: the SOPS keys from Task 1; the published image digests.
- Produces: compose services `commsgun-api` and `commsgun-ui`; env files `{{ edge_root }}/commsgun/api.env` and `ui.env`; the inventory variables in "Names introduced".

Inventory registry (every number is the spec's proposed starting value; mark the block `# TODO(ask): owner confirms these limits before the first deploy` and list it in the PR body):

```yaml
commsgun_providers:
  email: {name: resend, per_minute: 60, per_day: 100}
  sms: null
commsgun_apps:
  - id: xstockstrat
    key: commsgun_app_key_xstockstrat
    channels:
      email: {per_minute: 10, per_day: 80}
      sms: {per_minute: 5, per_day: 50}
  - id: ops
    key: commsgun_app_key_ops
    channels:
      email: {per_minute: 2, per_day: 5}
      sms: {per_minute: 5, per_day: 50}
```

- [ ] **Step 1: Write the failing test** `scripts/commsgun-config-test.py` (`unittest`, same style as `scripts/gatus-config-test.py`: render with Jinja2 from the real inventory plus fake secrets). It renders the `COMMSGUN_CONFIG` expression that Step 3 puts in a dedicated template `roles/stack/templates/commsgun-config.json.j2` and asserts:

```python
# parses as JSON; apps ids == ["xstockstrat", "ops"]; each key equals the fake secret for its `key` name
# xstockstrat.channels.email == {"per_minute": 10, "per_day": 80, "senders": [...fake...], "default_sender": "...fake..."}
# sms channels carry only per_minute/per_day; providers == {"email": {"name":"resend","per_minute":60,"per_day":100}, "sms": None}
# the rendered text is a single line (it goes in an env file)
# compose: both services use the hardening anchor, read_only true; api ports == ["127.0.0.1:9200:9200"] (no 9201);
#          ui ports == ["127.0.0.1:9202:9202"]; api volume "commsgun-data:/data"; mem_limit 96m / 192m;
#          ui depends_on commsgun-api; images are the two pin variables
```

- [ ] **Step 2: Run** `python3 scripts/commsgun-config-test.py` → FAIL.
- [ ] **Step 3: Implement.**
  - `versions.yml`: `commsgun_api_image: "ghcr.io/davcs86/commsgun-api@sha256:<digest>"` and the UI line, in exactly that quoted form (the bump script's regex requires it). Take both digests from the `images` job summary of the `commsgun` `main` run. Add two rows to `docs/versions.md` whose pin cells read `` `ghcr.io/davcs86/commsgun-api@sha256:xxxxxxxx…`, digest only (commit `xxxxxxx`, YYYY-MM-DD) `` — the format `scripts/bump_image_pin.py` rewrites.
  - `roles/stack/tasks/main.yml`: add `{{ edge_root }}/commsgun` (mode `0750`) to the directory loop; then, before the "Compose file" task, a block with `no_log: true` that (a) asserts `edge_secrets.resend_api_key` and `edge_secrets.commsgun_senders` are defined and non-empty, and that every app with an `email` channel has a `commsgun_senders` entry with a non-empty `senders` list containing its `default_sender`, each failing with `fail_msg` naming the key or app (never a value); (b) writes `api.env` (`COMMSGUN_CONFIG`, `RESEND_API_KEY`, `ADMIN_API_TOKEN`) and `ui.env` (`ADMIN_PASSWORD`, `SESSION_SECRET`, `ADMIN_API_TOKEN`, `API_BASE_URL`, `ORIGIN`), mode `0600`. Follow the existing "Webhooks-mcp secrets" task at lines 96–109.
  - `compose.yml.j2`: the two services after `webhooks-mcp`, and `commsgun-data` under `volumes`.
  - `roles/tailscale/tasks/main.yml:84`: append `commsgun_enqueue_hostname, commsgun_ui_hostname` to `_names` so both get split-horizon records.
- [ ] **Step 4: Add the Review Focus cases to the test**: rendering with `commsgun_senders` lacking app `ops`, and with a `default_sender` absent from `senders`, makes the assertion expressions (import them from the task file with PyYAML and evaluate with Jinja2) evaluate false.
- [ ] **Step 5: Run** `python3 scripts/commsgun-config-test.py && python3 scripts/secrets-keys-test.py && yamllint -s . && ansible-lint && for p in playbooks/*.yml; do ansible-playbook --syntax-check "$p"; done` → all pass.
- [ ] **Step 6: Commit** — `commsgun: inventory, pins, env files and compose services`.

### Task 3: Caddy routes

**Files:**
- Modify: `roles/stack/templates/Caddyfile.j2` (the `*.{{ magicdns_base_domain }}` 443 site only), `scripts/caddy-routing-test.sh`

**Interfaces:**
- Consumes: the hostname, port and body-limit variables from Task 2.
- Produces: two `handle` blocks, placed after `@webhooks_ingest` and before the catch-all:

```
@commsgun_enqueue host {{ commsgun_enqueue_hostname }}
handle @commsgun_enqueue {
	request_body {
		max_size {{ body_limits.commsgun_enqueue }}
	}
	reverse_proxy 127.0.0.1:{{ commsgun_enqueue_port }}
}

@commsgun_ui host {{ commsgun_ui_hostname }}
handle @commsgun_ui {
	reverse_proxy 127.0.0.1:{{ commsgun_ui_port }}
}
```

- [ ] **Step 1: Add the failing cases** to `scripts/caddy-routing-test.sh`, following its existing `expect` pattern and mock-upstream convention (add mock upstreams on 9200 and 9202 and the two hostnames to the `hosts.j2` line at line 28):

```
enqueue aborts publicly                      000
enqueue from the tailnet                     200 commsgun-api POST /v1/messages
enqueue body at exactly 262144 bytes         200 commsgun-api POST /v1/messages
enqueue body over 262144 bytes               413
dashboard aborts publicly                    000
dashboard from the tailnet                   200 commsgun-ui GET /login
staging VM denied on enqueue                 000
staging VM denied on dashboard               000
# Review Focus
enqueue name on the staging port             404   (no upstream reached)
dashboard name on the staging port           404   (no upstream reached)
enqueue name reaches only the API            200 commsgun-api GET /login        (not commsgun-ui)
dashboard name reaches only the UI           200 commsgun-ui POST /v1/messages  (not commsgun-api)
```

- [ ] **Step 2: Run** the script as CI does (build `edge-caddy:ci`, then `scripts/caddy-routing-test.sh edge-caddy:ci`) → the new cases FAIL.
- [ ] **Step 3: Implement** the two handles. Do not touch the 8443 site.
- [ ] **Step 4: Run** the script again and `shellcheck scripts/*.sh` → every line `PASS`.
- [ ] **Step 5: Commit** — `commsgun: tailnet-only Caddy routes`.

### Task 4: Gatus probes

**Files:**
- Modify: `roles/stack/templates/gatus-config.yaml.j2`, `scripts/gatus-config-test.py`

- [ ] **Step 1: Add the failing test** beside `test_webhooks_mcp_is_probed_publicly_for_401_and_its_ingest_over_the_tailnet`:

```python
def test_commsgun_is_probed_over_the_tailnet(self):
    for name, url in (("commsgun-api", f"https://{V['commsgun_enqueue_hostname']}/healthz"),
                      ("commsgun-backlog", f"https://{V['commsgun_enqueue_hostname']}/healthz/backlog"),
                      ("commsgun-ui", f"https://{V['commsgun_ui_hostname']}/healthz")):
        e = ENDPOINTS[name]
        self.assertEqual((e["group"], e["url"], e["interval"]), ("edge", url, "1m"))
        self.assertEqual(e["conditions"], ["[STATUS] == 200"])
        self.assertEqual(e["alerts"], [{"type": "ntfy"}])
        self.assertEqual(e["client"]["dns-resolver"], "udp://100.100.100.100:53")
```

- [ ] **Step 2: Run** `python3 scripts/gatus-config-test.py` → FAIL. **Step 3: Implement** three endpoints after `webhooks-ingest`, using `<<: *tailnet`. **Step 4: Run** → PASS.
- [ ] **Step 5: Commit** — `commsgun: Gatus probes`.

### Task 5: Bump workflow

**Files:**
- Create: `.github/workflows/bump-commsgun.yml`
- Modify: `scripts/bump-image-pin-test.py`

**Interfaces:**
- Consumes: `scripts/bump_image_pin.py` unchanged (`--var`, `--image`, `--digest`; step outputs `changed`, `digest`, `short`, `previous`, `revision`).
- Produces: workflow `bump-commsgun`, branch `bump/commsgun`, one PR titled `Bump commsgun to <api short>… / <ui short>…`.

Copy `bump-webhooks-mcp.yml` and change only what follows: name, concurrency group and branch; cron `"41 15 * * *"`; **two** pin steps (ids `api`, `ui`) for the two images, run in order, each failing the job on error; inputs `api_digest` and `ui_digest` instead of `digest`; a guard step after them:

```bash
# refuse a pair built from different commits, or with an unlabelled image
if [ "$API_REV" = unknown ] || [ "$API_REV" != "$UI_REV" ]; then
  echo "::warning::commsgun images are from different commits (api ${API_REV}, ui ${UI_REV}); nothing proposed."
  echo "commsgun images are from different commits (api \`${API_REV}\`, ui \`${UI_REV}\`); nothing proposed." >> "$GITHUB_STEP_SUMMARY"
  echo "proceed=false" >> "$GITHUB_OUTPUT"; exit 0
fi
```

The PR step runs when the guard says proceed and **either** image changed; its body lists previous and new digest for both and the shared source commit, with the same three closing sentences as the webhooks-mcp body. When one image is already pinned (`changed == false`) and the other changed, the PR is still opened.

- [ ] **Step 1: Add failing tests** to `scripts/bump-image-pin-test.py` in the style of its existing workflow tests (it parses the workflow YAML and runs its shell step against fake `git`/`gh`):

```python
# the workflow parses; concurrency == {"group": "bump-commsgun", "cancel-in-progress": True}; permissions match bump-webhooks-mcp
# it runs bump_image_pin.py twice: --var commsgun_api_image --image ghcr.io/davcs86/commsgun-api, and the ui pair
# every `uses:` is pinned to a 40-char SHA; checkout has persist-credentials false
# Review Focus: guard step with API_REV=abc, UI_REV=def → proceed=false, the summary names both, no git push and no gh pr create happen
# Review Focus: guard step with UI_REV=unknown → proceed=false
# same revisions, only the UI changed → branch bump/commsgun pushed, PR created, body contains both new digests
# an open PR with the same title → nothing pushed (as for webhooks-mcp)
```

- [ ] **Step 2: Run** `python3 scripts/bump-image-pin-test.py` → FAIL. **Step 3: Write the workflow.** **Step 4: Run** it and `yamllint -s .` → PASS.
- [ ] **Step 5: Commit** — `commsgun: bump workflow that pins both images together`.

### Task 6: Documentation

**Files:**
- Create: `docs/runbooks/commsgun.md`
- Modify: `docs/decisions.md`, `docs/versions.md` (prose for the two rows), `CLAUDE.md` (Context Guide), `README.md` (workflow list at line 62; the repository-setting note at line 78 now covers both bump workflows), `docs/runbooks/onboard-service.md`

- [ ] **Step 1: Write `docs/runbooks/commsgun.md`** with these sections, each a numbered procedure ending in the check that proves it worked:
  1. First deploy: Resend prerequisites done by hand (domain verified with its DKIM/SPF records at the domain's DNS host, full-access key), the three owner steps of PR 1, confirm the limits, deploy (`tailnet`), then the spec's verification list (section 10, step 3) as commands.
  2. Add a subscriber app: inventory entry, `GENERATED` key, `init-secrets` fill_missing, `commsgun_senders` entry, deploy, hand the key over.
  3. Rotate an app key or the admin password.
  4. Change a limit.
  5. Configure an SMS provider (points at the `commsgun` repo; held messages older than 24 h will already have expired).
  6. Read the dashboard: what "paused: daily quota" means and when it clears.
- [ ] **Step 2: Add one "Known traps" entry to `docs/decisions.md`**: the enqueue route is authenticated but must stay tailnet-only; 9201 is never published and is guarded by a token because every stack container shares the compose network; `/data` must be the named volume (uid 10001, read-only root); the store is not backed up by decision; both pins move together and the bump workflow refuses a mismatched pair; the Resend key is full-access because bounce tracking reads email status.
- [ ] **Step 3: Add the Context Guide row** to `CLAUDE.md`: "Deploy, configure or operate commsgun (email/SMS hub) | `docs/runbooks/commsgun.md`", and mention `bump-commsgun.yml` in the pin row.
- [ ] **Step 4: Re-read every doc touched in this PR against the code** (the repo's Teardown rule) and fix any mismatch.
- [ ] **Step 5: Run the whole CI set locally** — the commands of `.github/workflows/ci.yml`'s lint and caddy jobs — and report the output.
- [ ] **Step 6: Commit, push, open PR 2** titled `commsgun: deploy the email/SMS hub (tailnet-only)`. Its body lists: the open `TODO(ask)` limits block, the Resend prerequisites, and that merging deploys nothing until the owner runs **Actions → deploy** (`tailnet`).

### Task 7: Owner verification (not automated; recorded in the PR)

- [ ] After the owner deploys, they run section 1 of the runbook. The worker's part ends at reporting the commands; a green CI is not a working hub (`homelab-edge/CLAUDE.md` rule 6). Expected results: an email to the owner's own address reaches `delivered`; one to `bounced@resend.dev` reaches `bounced`; one SMS shows as held; the sixth `ops` email in a day returns 429; both hostnames abort from outside the tailnet; the three Gatus probes are green.
