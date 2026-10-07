# Configuration — WS-CFG spec (owner review pending)

**Status.** Spec only. Nothing reads `xmustard.toml` yet; this document is what goal §3.5b's WS-CFG row lands before any code, and the owner reviews it before the code row starts. Everything in "Today" was measured on `feat/parity-v2` at `b94221fb` with a read site cited for each claim. Everything in "Proposed" is planned behaviour with no reader, written as planned with its row (WS-CFG; the code row carries WS-48's struct).

## Today

Configuration reaches the binaries four ways, none of them one operator-editable file:

1. **Environment variables** (`XMUSTARD_*`), read at startup or per call.
   - server: `XMUSTARD_API_HOST`, `XMUSTARD_API_PORT`, `XMUSTARD_AUTH`, `XMUSTARD_API_TLS_CERT`, `XMUSTARD_API_TLS_KEY`, `XMUSTARD_ALLOW_INSECURE_BIND`, `XMUSTARD_DATA_DIR` (`api-go/cmd/xmustard-api/main.go`, `serverConfig`/`loadServerConfig`), `XMUSTARD_SHUTDOWN_DRAIN_SECONDS` (`main.go:144`).
   - exposure posture: `XMUSTARD_PROFILE` (`core|platform`, default core), `XMUSTARD_PLATFORM`, `XMUSTARD_CORE_ONLY` (legacy), `XMUSTARD_READ_ONLY`, `XMUSTARD_DISABLED_TOOLS`, `XMUSTARD_ALLOWED_HOSTS`, `XMUSTARD_ALLOWED_ORIGINS`, `XMUSTARD_WORKSPACE_ALLOWLIST`, `XMUSTARD_MAX_BODY_BYTES`, `XMUSTARD_MAX_INFLIGHT_BODIES`, `XMUSTARD_EVIDENCE_MAX_ORIGINAL_BYTES` (`api-go/cmd/xmustard-api/security_middleware.go`, `route_gates.go`).
   - MCP: `XMUSTARD_MCP_SCHEMA` (`lean|full`, blank is lean; `api-go/internal/mcpserver/schema.go:129`), `XMUSTARD_MCP_STANDING_MEMORY` (on unless `0`; `api-go/cmd/xmustard-api/mcp_routes.go:116`), `XMUSTARD_MCP_AUTO_REGISTER`, `XMUSTARD_WORKSPACE_ID`.
   - registration: `XMUSTARD_REGISTER_ROOTS`, `XMUSTARD_REGISTER_LIMIT`, `XMUSTARD_REGISTER_CWD_WHEN_ROOTS_UNKNOWN` (off by default; `api-go/cmd/xmustard-mcp/main.go:53`, `api-go/internal/mcpserver/server.go:174`), `XMUSTARD_UPSTREAM_ROOTS`.
   - memory: `XMUSTARD_MEMORY_PENDING_QUOTA` (default 500), `XMUSTARD_MEMORY_ACTIVE_ENVELOPE` (default 100000) (`api-go/internal/workspaceops/memory_propose.go:570`); `XMUSTARD_INDEX_CONTENT_RETENTION` (`rust-core/src/index/config.rs:179`).
   - core: `XMUSTARD_CORE_BIN`, `XMUSTARD_CORE_WORKER` (opt-in; `api-go/internal/rustcore/worker.go:5`).
   - hooks: `XMUSTARD_HOOK_TIMEOUT_MS` (client, default 200; `api-go/cmd/xmustard-hook/main.go:101`), `XMUSTARD_HOOK_BUDGET_MS` (daemon, 20-5000, default 180; `api-go/cmd/xmustard-api/hooks_routes.go:102`), `XMUSTARD_HOOK_SOCKET`.
   - search: `XMUSTARD_SEARCH_TEXT_LANES=off`, the text-lane kill switch (`rust-core/src/search.rs:1080`).
   - clients and ops: `XMUSTARD_API_BASE`, `XMUSTARD_API_TOKEN`, `XMUSTARD_APPROVER_TOKEN`, `XMUSTARD_CODEX_BIN`, `XMUSTARD_LOG_FILE`, `XMUSTARD_LOG_MAX_BYTES`, `XMUSTARD_LOG_KEEP`, `XMUSTARD_WHY_FAILED_COMMANDS`.
2. **`settings.json`** in the data directory, written through the admin API (`api-go/internal/workspaceops/runtime_settings.go`, `AppSettings`): `local_agent_type`, `codex_bin`, `codex_args`, `codex_model`, `opencode_bin`, `opencode_model`, `postgres_dsn`, `postgres_schema`, `require_multi_agent_verification`, `context_verification_threshold`, `principal_distinctness` (`token|owner`), `trusted_run_key_principals`.
3. **Compiled tables**: the per-client delivery policy `clientPolicies` (`api-go/internal/evidence/shapes.go:71`), the heavy-slot table, the route-gate table, and the `tools_list_budget.json` caps (owner-edited).
4. **Per-repository guidance files** `.xmustard.yaml`/`.yml`/`.json` (`api-go/internal/workspaceops/project_truth.go:86`): path-specific instructions and filters. This row does not touch them.

Two behaviours the design must respect, both measured:

- **The static hook client fails open by design** (`api-go/cmd/xmustard-hook/main.go:11`): it always exits 0 and prints nothing on failure or budget exhaustion. Claude Code's `WorktreeRemove` depends on this (a failing hook there blocks the removal).
- **The trusted-run-key setting fails closed when unset** (`api-go/internal/workspaceops/memory_scopes.go:243`): with no principals configured, a run-key scope assertion is refused (WS-29b; owner decision 2026-10-03).

## Proposed: one `xmustard.toml`

**The file.** One `xmustard.toml` per workspace root, a sibling of `.xmustardignore` and `.xmustard.yaml`. The path can be overridden with the `XMUSTARD_CONFIG` environment variable or an ops flag (exact flag surface is the code row's, with WS-48). A missing file is not an error: every key falls back to its default, so nothing changes for an operator who never writes one. A file that fails to parse, names an unknown key, or holds an out-of-range value is refused (owner question OQ-1).

**Secrets stay out.** The file never carries tokens, keys or deployment endpoints: `XMUSTARD_AUTH_TOKENS`, `XMUSTARD_API_TOKEN`, `XMUSTARD_APPROVER_TOKEN`, the TLS key paths, the Postgres DSN and the API host/port stay environment- or flag-only. The file is expected to be committed to a repository; nothing that authenticates may land in it.

### `[engine]`

| Key | Type and default | Environment override | Source today |
|---|---|---|---|
| `profile` | `core` \| `platform`, default `core` | `XMUSTARD_PROFILE` | environment |
| `schema` | `lean` \| `full`, default `lean` (the tool profile) | `XMUSTARD_MCP_SCHEMA` | environment |
| `tools_disabled` | list of tool names, default `[]` | `XMUSTARD_DISABLED_TOOLS` | environment |
| `read_only` | bool, default `false` | `XMUSTARD_READ_ONLY` | environment |
| `worker` | bool, default off (opt-in; unchanged) | `XMUSTARD_CORE_WORKER` | environment |
| `core_bin` | path, default empty | `XMUSTARD_CORE_BIN` | environment |
| `search_text_lanes` | `on` \| `off`, default `on` | `XMUSTARD_SEARCH_TEXT_LANES` | environment |
| `index_content_retention` | enum, default as compiled | `XMUSTARD_INDEX_CONTENT_RETENTION` | environment |
| `register.roots` | list of paths, default `[]` | `XMUSTARD_REGISTER_ROOTS` | environment |
| `register.limit` | int | `XMUSTARD_REGISTER_LIMIT` | environment |
| `register.cwd_when_roots_unknown` | bool, default `false` | `XMUSTARD_REGISTER_CWD_WHEN_ROOTS_UNKNOWN` | environment |
| `hook.timeout_ms` | int, default 200 | `XMUSTARD_HOOK_TIMEOUT_MS` | environment |
| `hook.budget_ms` | int 20-5000, default 180 | `XMUSTARD_HOOK_BUDGET_MS` | environment |
| `hook.socket` | path, default compiled | `XMUSTARD_HOOK_SOCKET` | environment |
| `semantic.enabled` | bool, default `false` | `XMUSTARD_ENGINE_SEMANTIC_ENABLED` (reserved; WS-37 lands the reader) | none yet |
| `lsp.enabled` | bool, default `false` | `XMUSTARD_ENGINE_LSP_ENABLED` (reserved; WS-45 lands the reader) | none yet |

The two reserved keys exist so the surface is named before their rows land: `semantic.enabled` is the WS-37 switch and ships off, and turning it on by default is G§5.12, an owner decision; `lsp.enabled` is the WS-45 pool switch. Until those rows merge, the keys parse, reject unknown sub-keys, and change no behaviour. The `worker` default stays as it is today; whether it flips to default-on is G§5.9, an owner decision.

### `[engine.hooks.<client>]`

One sub-table per client, starting with `claude` and `codex` (the clients `internal/hooks` serves today); adding a client is one table entry. `<CLIENT>` in the overrides is the upper-case client name.

| Key | Type and default | Environment override | Notes |
|---|---|---|---|
| `enabled` | bool, default `true` | `XMUSTARD_ENGINE_HOOKS_<CLIENT>_ENABLED` | `false` refuses the client's hook events at the daemon and makes the client exit without posting |
| `fail` | `open` \| `closed`, default `open` | `XMUSTARD_ENGINE_HOOKS_<CLIENT>_FAIL` | what happens when the daemon is unreachable or the budget runs out; `open` is today's behaviour (`cmd/xmustard-hook/main.go:11`), `closed` denies the tool call the hook guards |
| `timeout_ms` | int, default 200 | `XMUSTARD_ENGINE_HOOKS_<CLIENT>_TIMEOUT_MS` | per-event client budget |

The `fail` policy has to be knowable without the daemon, because the daemon is unreachable exactly when the policy matters (round-1 finding r1-2). The static hook client therefore reads its policy from its own repository copy of the file, or its environment override, before it dials anything; a client that can read no policy (no file, unparsable file) defaults to `open` and exits 0, which is today's behaviour. The daemon independently refuses a disabled client's events at the route, so `enabled=false` holds even for a client that never reads the file.

In v1 the policy is per client, not per event (owner question OQ-2). `WorktreeRemove` stays fail-open in v1 either way, because a failing hook there blocks the removal; a per-event carve-out is the owner's call.

### `[memory]`

| Key | Type and default | Environment override | Source today |
|---|---|---|---|
| `pending_quota` | int, default 500 | `XMUSTARD_MEMORY_PENDING_QUOTA` | environment |
| `active_envelope` | int, default 100000 | `XMUSTARD_MEMORY_ACTIVE_ENVELOPE` | environment |
| `standing_memory` | bool, default `true` | `XMUSTARD_MCP_STANDING_MEMORY` | environment |
| `trusted_run_key_principals` | list of principals, default `[]` | `XMUSTARD_MEMORY_TRUSTED_RUN_KEY_PRINCIPALS` (comma-separated) | environment or `settings.json`; never the repository file |

`trusted_run_key_principals` is the same setting `settings.json` holds (WS-29b), and it is deliberately not file-settable: the file is committed to the repository, so a trust list read from it would let whoever can commit to the repository grant run-key scope authority — a trust decision must stay in an operator-controlled source (round-1 finding r1-1; the owner decision of 2026-10-03 makes it a setup choice). The goal row's "trusted run-key agents" switch is this environment variable and the `settings.json` key, both reported by `config check`. The default stays empty: with no principals configured anywhere, run-key scope assertions are refused. The effective value is the value from the highest-precedence source that sets it, never the union of sources — a union would silently re-add a principal an operator removed.

### Precedence

Proposed: environment variable over `xmustard.toml` over `settings.json` over the compiled default, one winner per key. One carve-out: a trust list never comes from the repository file (the escalation path round-1 finding r1-1 closes), so `trusted_run_key_principals` is sourced from the environment or `settings.json` only. `xmustard-ops config check` prints, for every key, its effective value and the source it came from (`default`, `file`, `env`, or `settings`). Whether `settings.json` — which the admin API writes at runtime — should outrank the repository file for the remaining keys is owner question OQ-3; the proposed order puts the repository file above it so a committed config is the operator's statement of record.

### `xmustard-ops config check`

`xmustard-ops config check [--config <path>]` loads the environment, the file and `settings.json`, then:

- refuses an unknown key, a bad value, or an out-of-range number, naming the key, the value and the file;
- prints the effective configuration as a table, one row per key with its value and source;
- exits 0 when the configuration is valid, non-zero otherwise.

It changes nothing; it is a read-only validator and report. It runs against the same loading code the API and core startup use, so what it accepts is what the binaries accept.

## Boundaries (not decided here)

- G§5.9 (worker default-on), G§5.11 (WS-21B isolation policy — the surface gains no isolation keys in v1), G§5.12 (semantic lane default-on), G§5.8 (thresholds and parity wording), G§5.13 (no third-party or hosted model keys). None of these is expressed as a config key here.
- The surface adds no security guard and moves none: `secretpath`, `AdmitMemory`, the route-gate table, read-only mode and path confinement keep their guard shape and their refusal-path tests (§5.1 rule 7). A refused value stays refused; no key can turn a refusal into an acceptance.
- `.xmustard.yaml`/`.yml`/`.json` guidance files, `settings.json`'s admin API, `clientPolicies`, the route-gate table and `tools_list_budget.json` are unchanged by the code row, except that `config check` reads and reports them.
- No existing environment variable is renamed, removed or reinterpreted. New variables follow `XMUSTARD_<SECTION>_<KEY>`, and each lands with a reader on a non-test path, a test, a documentation line and its WS-48 note (§6 A7).

## Open questions for the owner

- **OQ-1** Malformed configuration: refuse startup (proposed) or log and continue with defaults.
- **OQ-2** Hook failure policy per client (proposed for v1) or per event; and whether any event may be `closed`, given `WorktreeRemove` must stay silent.
- **OQ-3** Precedence between `xmustard.toml` and `settings.json` for the keys the file may carry (proposed: file above settings; trust lists are settled — never from the file).
- **OQ-4** File location: the workspace root (proposed) or an additional install-level path. Settled by round 1: the static hook client reads its own repository copy, so `fail=closed` is enforceable without the daemon.

## Tests the code row runs

- `config check` refuses an unknown key and an out-of-range value, naming the key and the file.
- One test per precedence boundary: environment over file, file over `settings.json`, `settings.json` over the compiled default, with the printed source asserted.
- `trusted_run_key_principals` unset everywhere keeps WS-29b's refusal; an environment-set list behaves exactly like a `settings.json`-set list; the sources are never unioned; the file loader refuses the key wherever it appears in `xmustard.toml`, naming the rule.
- A disabled hook client takes its refusal path; the fail-open default is pinned, with the `WorktreeRemove` must-stay-silent case asserted.
- The `schema` flip (`lean`/`full`) stays inside the code `TestToolsListStaysWithinByteCaps` exercises; the `CAPS` gate covers it.
- The reserved `semantic` and `lsp` keys parse, reject unknown sub-keys, and change nothing.
- Every new environment variable has a reader test and a documentation line.

Gate for the code row: `MAC`/`BOXALL` per its row, plus `CAPS` (the tool profile flip), and `SECDOC` only if a route or gate row changes. Nothing resident changes, so `BUD` is not owed unless the code row makes it so.
