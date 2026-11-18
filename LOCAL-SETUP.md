# ARTEX local setup (this host)

Installed from https://github.com/BaaRai/ARTEX into `/home/taxmachine/ARTEX`
(upstream checkout, `dev` version built locally by `docker compose build`).

## LLM

Uses the same IKHDev gateway as OpenCode's `ikhdev` provider:

| Setting | Value |
|---|---|
| `ARTEX_LLM_PROVIDER` | `openai` |
| `ARTEX_LLM_BASE_URL` | `http://172.17.0.1:13030/v1` (local retry proxy → `https://api.ikhdev.xyz`) |
| `ARTEX_LLM_MODEL` | `deepseek-v4.1-flash-1017` |
| `OPENAI_API_KEY` | IKHDev key (from `opencode.db` `credential` row `ikhdev` / label `IKHDev`) |

The key is also stored in `.env` (mode 0600, git-ignored). ARTEX appends
`/chat/completions` itself, so the base URL must stay at the `/v1` root.

Verified from inside the container: `GET /v1/models` -> 200, and the model
honours OpenAI-style `tools` / `tool_calls`, which ARTEX needs.

Other models available on the same key if you want to switch
(`.env` -> `ARTEX_LLM_MODEL`, then `docker compose up -d`):
`deepseek-v4-pro`, `glm-5.3`, `glm-5.3-prime`, `kimi-k3`, `qwen3.8-max`,
`claude-opus-5-5`, `claude-sonnet-5-5`, `gpt-6.1-sol`, ...

## Run

```bash
cd /home/taxmachine/ARTEX
docker compose up -d          # start (image already built)
docker compose logs -f artex  # logs
docker compose down           # stop (data/ and keys/ + pgdata volume persist)
```

- UI: http://127.0.0.1:8787
- Recording proxy: http://127.0.0.1:8788
- Postgres: container-internal only (`pgdata` named volume)

First-run setup token (printed once by the server) is shown by:

```bash
docker compose logs artex | grep "Setup token"
```

Open `/setup` and use that token to create the admin account. After an admin
password exists the token is no longer needed.

## Host-specific fix: SELinux

Bazzite/Fedora runs SELinux in enforcing mode. The compose file bind-mounts
`./data`, `./keys` and `./skills` from the home directory (`user_home_t`), which
the container is not allowed to write. They were relabelled once:

```bash
chcon -Rt container_file_t data keys skills
```

Without this the backend fails with
`write secret key: open /app/keys/secret.key: permission denied` and restart-loops.
Re-apply if those directories are recreated (fresh clone, `rm -rf keys`, etc.).

## Lab MCP servers (registered)

Both are remote `http` MCPs on the Docker bridge address, registered in
`mcp_servers` and discovered at startup:

| Name | URL | Tools | Backing service |
|---|---|---|---|
| `windows-lab` | `http://172.17.0.1:13020/mcp` | 8 | `/home/taxmachine/windows-lab-mcp` (unit `windows-lab-mcp`) |
| `local-hexbrain` | `http://172.17.0.1:13014/mcp` | 9 | local Go container (`local-hexbrain-server-1`), NVD/KEV/EPSS/payloads/templates |
| `nuclei-runner` | `http://172.17.0.1:13015/mcp` | 6 | scoped nuclei scans of lab targets (detached scans, unit `nuclei-mcp`) |
| `semgrep` | `http://172.17.0.1:13016/mcp` | 2 | static audit, local rules + any `p/` registry passthrough | static audit of lab source with local semgrep-rules (unit `semgrep-mcp`) |
| `ida` | `http://172.17.0.1:13011/mcp` | 33 | IDA flatpak daemon `13010` + auth bridge (`artex-mcp-bridge-ida`) |
| `hexbrain` | `http://172.17.0.1:13013/mcp` | 11 | hosted MCP at `https://hexbrainai.com/api/mcp` + auth bridge (`artex-mcp-bridge-hexbrain`) |

- `windows-lab` is the allowlisted lab control surface (VMs, campaigns,
  artifacts). VM/campaign/script names live in
  `/home/taxmachine/windows-lab-mcp/registry.json`; the agent cannot pass a
  command or an arbitrary SSH target.
- `ida` is `re-mcp` served from the IDA flatpak. The daemon needs
  `--share=network` (its sandbox has a private network namespace) and requires a
  bearer token; `mcp-bridge/bridge.py` injects it because ARTEX's remote-MCP
  config has no header field.
- `hexbrain` is a hosted vulnerability-report library (HackerOne disclosures, payload
  references, CVE checks). The API key lives in
  `/home/taxmachine/mcp-bridge/hexbrain.token` (0600) and the bridge injects the
  `Authorization: Bearer` header ARTEX cannot send. Call it
  `mcp__hexbrain__*` (e.g. `hexbrain_plan_tests`, `hexbrain_search_reports`,
  `hexbrain_find_cves`).
- A `windbg` bridge is already listening on `http://172.17.0.1:13012/` but is
  **not registered**: its upstream (`127.0.0.1:8766` → Windows VM tunnel) is down
  unless the debug VM is running. Register it in the UI when that tunnel is up.
- Host-side details: `/home/taxmachine/mcp-bridge/README.md` and
  `/home/taxmachine/windows-lab-mcp/README.md`.

Units (user scope, enabled):

```bash
systemctl --user status artex-ida-mcp artex-mcp-bridge-ida artex-mcp-bridge-windbg windows-lab-mcp
```

## Lab tooling repo (`~/labkit`)

All lab tooling lives in the committable `~/labkit` repo (commit `0733487` and
later): skills, MCP servers, proxies, wp-lab, systemd units, scripts, secrets
templates. Live paths (`~/mcp-bridge`, `~/windows-lab-mcp`, …) are symlinks
into it; ARTEX skills are bind-mounted per-skill from `labkit/skills/`
(see `docker-compose.yml`). After editing anything under labkit, commit there —
nothing else to sync. Secrets (`*.token`, `.env`, `windbg.env`) are gitignored;
bootstrap from `*.example` files (README has the full fresh-machine procedure).

## LLM profiles

All 21 models from OpenCode's `ikhdev` provider are stored in `llm_profiles`:

- `format` = `openai`, `context_window_k` = **1000** (1M), `streaming` on;
- `base_url` = `http://172.17.0.1:13030/v1` (the retry proxy, so profiles get the
  same 522 resilience as the env config);
- the API key is sealed at rest (`enc:v1:`), hint `…2Xvw`;
- **no profile is default** (`is_default=false` for all), so the env-configured
  model keeps driving tasks. Activate one in the UI, or `POST
  /api/llm/profiles/active`, to make it the default; a task can also pin its own
  profile/chain.

Availability checked against the live gateway (2026-11-16):

| Result | Models |
|---|---|
| working | deepseek-v4.1-flash, deepseek-v4.1-flash-1017, deepseek-v4-pro, deepseek-v4-flash, glm-5.3, glm-5.3-prime, glm-5.3-flash, glm-5.2, kimi-k3, qwen3.8-max, qwen3.8-max-0902, gpt-6-astra, gpt-6.1-sol, gpt-6.1-terra, gpt-6-luna |
| 404 model_not_found | kimi-k3.1-pro, kimi-k3.1-pro-highspeed (names in opencode.jsonc are stale) |
| 503 upstream_capacity | claude-opus-5-5, claude-sonnet-5-5, claude-haiku-5-5, gemini-4-argon (temporarily unavailable upstream) |

### Hypervisor support

- Skill `skills/hypervisor-guest-escape/` (worker/auto/pentest): qtest-driven virtio methodology validated against Fedora QEMU 10.2.2 on this host (`b64read`/`b64write` guest-RAM R/W, PIO/MMIO, IRQ intercept work; `clock_step`, `qom-list`, `device_add` absent). Backed by `windbg` + `ida` MCPs for triage/code. No execution MCP by design - campaigns are operator-run.
- Template *Hypervisor guest-to-host escape (virtio device)* under Task templates.
- `artex_task_create.py`: create a task from a seed JSON (used by the Patch-Tuesday pipeline: `artex_task_create.py ntvuln/reports/patchtuesday_DATE.seed.json`). Signs its own short-lived JWT from the instance key.

### Model routing (big vs subagents)

Agent bindings take precedence over a task chain and over the global profile, so the
split is enforced per role:

| Role | Agent key(s) | Profile |
|---|---|---|
| big model (planning + chat) | `planner`, `mainagent`, `goals` | **Kimi K3** (profile 11) |
| subagents / workers | `worker`, `auto`, `pentest`, `reporter`, `retester` | **DeepSeek V4.1 Flash 1017** (profile 2) |

The global default profile is DeepSeek 1017, so anything unbound falls back cheap.
Verify with `GET /api/tasks/{id}/llm/resolution` — `source` should read
`agent_binding`. `kimi-k3` is the gateway's current Kimi id; `kimi-k3.1-pro` in
opencode.jsonc does not exist upstream (404).

## LLM resilience (522 / connection resets)

`api.ikhdev.xyz` is behind Cloudflare and intermittently returns 522 or resets the
connection. ARTEX retries a failed pre-commit stream once after 500 ms, which is not
enough, so requests are routed through `llm-retry-proxy`:

- unit `artex-llm-retry`, listens `172.17.0.1:13030`, upstream `https://api.ikhdev.xyz`,
  key in `/home/taxmachine/llm-retry-proxy/ikhdev.token` (0600);
- retries 408/425/429/500/502/503/504/520-527 and transport errors up to 6 attempts
  (0.5/1/2/4/8 s backoff), only before any body byte reaches the client;
- retries are logged to `/home/taxmachine/llm-retry-proxy/logs/retries.log`;
- a broken stream *after* it started is not replayed (the client already has partial
  output; ARTEX decides).

If the gateway is fully down, nothing local fixes it; the proxy only masks transient
failures.

## Task timeouts and budgets

- `timeout_seconds` is per task. Task #1 was created with a 6 h limit and hit it while
  the gateway was failing; it is now `0` (no time limit) and `deadline_at` is cleared.
- Token/turn budgets are the `ARTEX_TASK_BUDGET_*` / `ARTEX_INTENT_BUDGET_*` env vars.
  **Unset means unlimited**, which is the current state - there is no smaller budget to
  raise. Set them only if you want a cap.
- A terminal task is revived by adding a human goal (`POST /api/tasks/{id}/goals`),
  which also starts a fresh run budget and clears the deadline.

## Finding categories

Findings carry a `category`: `operating-systems`, `hypervisors`, `web`,
`applications`, `others` (DB default, backfilled: kernel findings ->
operating-systems, WP/Woo -> web). The findings page shows per-category stat
cards (click to filter) next to the severity cards. Agents set it via
`report_finding` (optional, defaults to `others`); the UI has a Category column,
filter dropdown, inline edit and detail field; `PATCH /api/exploration/findings/{id}`
accepts `category` (strict: unknown values are 400, `Web` normalizes to
`web`); list/export/group endpoints accept `?category=`. Migration
`db/migrations/0004_findings_category.sql` (IF NOT EXISTS, safe to re-run).

## WinDbg tunnel

Restored 2026-11-17: the `win11` libvirt VM (TaxMachine, 192.168.122.247 - not
win11-26h2, which stays untouched) runs the `windbg-mcp` service; user unit
`windbg-mcp-tunnel.service` forwards localhost:8765; the filter proxy and the
`172.17.0.1:13012` bridge inject the bearer token. If `health` fails with a
missing `/tmp/windbg-mcp/.listen_token`, re-copy `C:\ProgramData\windbg-mcp\token`
from the VM. ARTEX `windbg` server holds 61 tools; the `windbg` skill is enabled
for worker/auto/pentest.

## Local HexBrain: CPE version matching

NVD `configurations` are indexed (3.29M CPE rows, DB ~1.2 GB). `find_cves`
accepts `product` + `version` and returns `affected` (pinned version or range
match), `unconfirmed` (NVD states no versions), or `check-version`. Aliases
cover common names (`apache http server` -> `http_server`). Refresh keeps CPEs
current (full NVD re-ingest on the daily timer).

## Patch-Tuesday pipeline

`ntvuln/scripts/patch_tuesday.py` diffs 130 staged binaries against winbindex
remote (gh-pages data branch); report + ARTEX task seed land in
`ntvuln/reports/patchtuesday_*.md(.seed.json)`. User timer
`ntvuln-patchtuesday` runs 06:00 on the second Tuesday monthly (verified next
elapse). Binary acquisition is manual: update a disposable clone, pull files.

## Task templates

One template is seeded under **Task templates**: *Windows kernel LPE hunt
(user-reachable component)*. It encodes the lab's evidence rules (lpeuser only,
crash != LPE, instrumentation aborts are not crashes), the Phase A/B split
(reachability + static map + harness, then campaign + triage), and the tooling
naming (ida, windows-lab, windbg, hexbrain). Replace `<COMPONENT>` and the binary
path when creating a task from it.

## Local HexBrain (Docker)

`/home/taxmachine/local-hexbrain`: Go service (`internal/store` SQLite/FTS5 +
`internal/ingest` + `cmd/server`, mcp-go) in `local-hexbrain:1.0`,
compose service `local-hexbrain-server-1`, published on `172.17.0.1:13014`
only. DB at `./data/local_hexbrain.db` (~655 MB: 426k CVEs, 407k EPSS,
1.8k KEV, 14k nuclei templates, 142 payload docs / 67 topics, 148 WSTG playbooks). SELinux needed
`chcon -Rt container_file_t data` like the other bind mounts.

- Daily refresh: user timer `local-hexbrain-refresh` runs the host `./bin/ingest
  --years recent,modified` (NVD deltas + KEV + EPSS + repo pulls) against the same
  DB file the container serves. The host systemd *server* unit is disabled; the
  container owns the port.
- Skill `skills/local-hexbrain/SKILL.md` (worker/auto/pentest) tells agents to
  prefer it for CVE/payload/template questions and the hosted hexbrain for
  program history and write-ups.

## Skills

Skills are per-agent: a skill only exists for an agent listed in
`agent_skill_visibility`, and an MCP named in a skill's `mcps:` is **deferred**
(locked) until that skill is loaded. `hexbrain`, `local-hexbrain`, `windows-lpe-triage`, `nuclei-runner` and `semgrep` are
enabled for `worker`, `auto` and `pentest`; enable more in the UI under the
agent's skills if you want the planner or main agent to see them too.

`skills/hexbrain/SKILL.md` explains what the HexBrain library is for (prior art
from disclosed reports, payloads, bypasses, CVE checks), which tool to call when,
and what it must not be used for (kernel/binary work, live-asset claims).

`skills/windows-lpe-triage/SKILL.md` carries the lab's evidence rules into
ARTEX: `lpeuser`-executed proof only, crash ≠ LPE, restricted-token SYSTEM
(SearchFilterHost) vs unrestricted SYSTEM (MsMpEng), instrumentation aborts are
not crashes, and the cleanup checklist. `skills/` is bind-mounted, so editing it
needs no rebuild.

## Instance login

The instance password was already set before this setup finished; it is not
stored here. The MCP rows were inserted directly into Postgres because the
management API requires an authenticated session. Re-open `/api/mcp` in the UI
(or click refresh on a server) to re-run discovery; the cache currently holds
8 + 33 tools.

## Notes

- `data/` and `keys/` files created by the container are owned by the
  container-root uid (rootless mapping), so the host user cannot delete them
  without a container-side `rm`; that is expected and harmless.
- `keys/` holds `jwt.key` and `secret.key`. Losing them invalidates sessions and
  the encrypted at-rest secrets.
- Containers use `restart: unless-stopped`, so they come back with the Docker
  daemon after a reboot.
