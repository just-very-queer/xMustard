# Architecture

Source: `v0.1.1`. What shipped and its known limits:
[v0.1.1 release notes](releases/v0.1.1.md), after the
[v0.1.0 release notes](releases/v0.1.0.md) (tag `v0.1.0`, `8fca2d5`). Product direction:
[VISION](VISION.md). Security posture: [SECURITY](SECURITY.md).

xMustard is small on purpose. A Go API owns delivery, policy and state. A Rust core owns
repository semantics. Agents reach both through nine MCP tools. Everything runs
locally, with no Docker, and binds to `127.0.0.1:8042` by default.

## The shape in one picture

```text
Coding agent (any MCP client, or Pi)
  |-- URL-capable client ---------------------> xmustard-api  /mcp  (Streamable HTTP)
  |-- stdio client -> xmustard-relay ---------> xmustard-api  /mcp  (Rust, std-only)
  |-- stdio client -> xmustard-mcp -----------> xmustard-api  tool routes (deprecated Go shim)
  |-- Claude Code plugin hooks (http) --------> xmustard-api  /api/hooks/claude/<Event>
  |     '-- SessionStart, WorktreeRemove -> xmustard-hook -> the same routes (Unix socket, else TCP)
  '-- Pi -> integrations/pi extension --------> xmustard-api  tool routes (+ xmustard_expand)
            '-- Pi built-in tool output -------> .../evidence/capture (redacted, reduced, retained)

xmustard-api (Go; default 127.0.0.1:8042)
  exposure -> body limit -> auth -> route gates -> evidence delivery -> handlers
  |-- mcpserver     nine tools, schemas, workspace resolution (in process for /mcp)
  |-- workspaceops  memory, grounding, outcomes, registration, tokens, diagnostics
  |     |-- govstore    governance.db (SQLite WAL): memory, votes, events, outcomes
  |     |-- injection   instruction-pattern scan, data framing, quarantine
  |     '-- redact      secret redaction on memory ingest, command output and captures
  |-- evidence      scoped originals, bounded projections, recovery handles; capture
  |                 streams through redact.Writer before the spool
  |-- hooks         Claude Code hook events: capture, pushed memory, index hits
  |-- groundbudget  ground's output budget
  |-- budget        transient-byte pool, helper-child cap, heavy slot, RSS governor
  '-- rustcore  --> xmustard-core (Rust): one process per call by default, or one
                    resident `xmustard-core serve` worker with XMUSTARD_CORE_WORKER=1
                      |-- index/        index.db per repository; the watcher and its
                      |                 refresh queue (resident worker only)
                      |-- search.rs     hybrid search (six lanes, RRF K=60)
                      '-- changetrack, symbolgraph, explain, diagnostics, verification

xmustard-ops (Go CLI)   -> the data dir directly: approvals, merge attestations, store
                           backup/check/restore; setup installs the API as a
                           launchd or systemd user service; mcp-config only prints
xmustard-eval (Go CLI)  -> drives real or fake clients for paired outcome evaluations
Optional React UI       -> platform-profile HTTP routes
```

Three facts make this shape cheap to run:

- **One API process serves every agent.** A client that takes a URL needs no
  per-agent process at all. A stdio-only client runs `xmustard-relay`, measured at
  2,192 to 2,240 KiB RSS per agent on macOS arm64, against 13,824 to 13,888 KiB for
  the older Go shim ([relay RSS](benchmarks/2026-09-26-ws13-relay-rss.md)).
- **Every tool call takes the full front door.** A call over `/mcp` re-enters the API
  through the same middleware stack as a direct HTTP call, as the caller, with the
  caller's token (`api-go/cmd/xmustard-api/mcp_routes.go`). Route gates, output
  budgets and scoped auto-registration apply over `/mcp` exactly as through the
  stdio shim.
- **The Rust core runs on demand.** By default each call starts `xmustard-core` and
  it exits. `XMUSTARD_CORE_WORKER=1` (opt-in, off by default) keeps one supervised
  worker resident instead (`api-go/internal/rustcore/worker.go`). The budget governor
  reports its memory and recycles it under pressure. That worker also runs the
  watcher (WS-15): reads register their root, and the API turns the watcher's
  debounced batches into `index update --paths` runs in the heavy slot. Without the
  worker nothing is watched, and reads refresh the index themselves.

## Where state lives

| State | Location | Owner |
| --- | --- | --- |
| Governed memory: entries, revisions, votes, append-only events, sessions, feedback, run outcomes, merge attestations; review records and findings (review build tag) as subjects on the same tables | `<data dir>/governance.db`, one SQLite database in WAL mode, at schema version 3 (migration 3 adds `subject_kind`; v0.1.0 knew version 2 and refuses a newer file) | `api-go/internal/govstore/` |
| Store backups and restores | `<data dir>/backups/governance-<UTC stamp>.db` (`xmustard-ops store backup`); a restore keeps the replaced store as `governance.db.pre-restore-<stamp>` | `govstore/backup.go`, `cmd/xmustard-ops/setup.go` |
| Service units and the daemon log | macOS: `~/Library/LaunchAgents/com.xmustard.api.plist`; Linux: `~/.config/systemd/user/xmustard-api.socket` and `.service`; the log at `~/Library/Logs/xmustard/api.log` or `~/.local/state/xmustard/api.log` (`--log-file`; the API reads `XMUSTARD_LOG_FILE`), rotated at 10 MiB, 3 generations | `api-go/internal/daemon/` |
| Hook socket | `$XDG_RUNTIME_DIR/xmustard/hook.sock`, else `xmustard-<uid>/hook.sock` in the temp dir (`XMUSTARD_HOOK_SOCKET`); 0700 directory, 0600 socket | `api-go/internal/hooks/transport/` |
| Tokens and the auth audit log | `<data dir>/agent_tokens.json`, `<data dir>/auth_audit.json` | `workspaceops/auth.go`, `auth_audit.go` |
| Workspace registry and settings | `<data dir>/workspaces.json`, `<data dir>/settings.json` | `workspaceops/workspace_lifecycle.go` (writes) and `workspace_registry.go` (resident reads); settings load in `run_control.go` |
| Retained evidence originals | `<data dir>/evidence/`, keyed by SHA-256 of the handle | `api-go/internal/evidence/` |
| Per-workspace operational records and scan snapshots | `<data dir>/workspaces/<id>/` (JSON, locked and fsynced across processes) | `workspaceops` stores |
| Code index | `<git-dir>/xmustard-cache/index-v3/<scope>/index.db`; for a non-Git directory, the per-user cache directory | `rust-core/src/index/` |
| Legacy graph and per-file symbol caches, tracked-file hash cache | `<git-dir>/xmustard-cache/index-v2/` (graph caches) and `filehash-v1/` (hash cache) | `indexcache.rs`, `hashcache.rs` |
| Diagnostics baseline and platform materializations (optional) | Postgres, named by `postgres_dsn` in `settings.json` | `workspaceops/pg*.go`, `backend/sql/` |

The data dir is `XMUSTARD_DATA_DIR`. Its default, `../backend/data`, is relative to
the process's working directory, release binaries included, so set an absolute path.

Postgres is never the home of governed memory. The `diagnostics` tool is the one core
tool that needs it: without `postgres_dsn` it answers `Postgres DSN is required to read
diagnostics` (`workspaceops/diagnostics.go`).

## How a call flows

Take `recall q="token refresh" paths=internal/auth/token.go` from a stdio client:

1. `xmustard-relay` forwards the JSON-RPC message to `POST /mcp`. The bearer token
   comes from `XMUSTARD_API_TOKEN` and goes only in the `Authorization` header.
2. `mcpserver` validates the arguments against the closed schema and resolves the
   workspace: explicit `workspace_id`, then the connection binding, then
   `XMUSTARD_WORKSPACE_ID`, then an absolute path argument, then the client's roots,
   then the server's working directory, which only the stdio shim has
   (`mcpserver/resolve_workspace.go`).
3. The call re-enters the API as `GET /api/workspaces/{id}/context/active`. The route
   gate needs the `reader` role.
4. `workspaceops` ranks memory from `governance.db` (BM25, path overlap, trust,
   recency, feedback), re-hashes each entry's anchored files to flag drift, and labels
   every entry with `injection_flags`.
5. The result goes back under a `max_chars` budget (4,000 by default on MCP). Entries
   that do not fit are left out, `output_budget` says what was cut, and a signed
   `next_cursor` continues the page.

`search`, `explain` and `impact` take the same path to their own routes, and
`workspaceops` asks `xmustard-core` through `rustcore`. With an index present, search
reads its text lanes from `index.db`, and explain and impact run on index snapshots.
Without one, the legacy graph answers and search lists what is missing under
`degradations` (`rust-core/src/search.rs`).

## Go module map

| Responsibility | Source owner | Interface and constraints |
| --- | --- | --- |
| MCP protocol and tool schemas | `api-go/internal/mcpserver/`: the tool table (`tools.go`), one `tool_<name>.go` per tool, `streamable.go` (Streamable HTTP), `evidence.go` (evidence delivery and resources), `elicitation.go` (human confirmation), `compat.go` (argument repair via `toolcompat`), `arg_errors.go` (argument errors and did-you-mean hints) | Nine tools in a fixed order; protocol 2025-06-18, with 2024-11-05 negotiated; closed schemas with bounds; `tools/list` limited to the caller's usable tools (`GET /api/auth/whoami`) and held under tested byte caps per schema profile, protocol version and role (`testdata/tools_list_budget.json`); advanced arguments accepted but listed only with the full schema profile and documented at `xmustard://docs/tools`. An argument that fails validation (unknown, wrong type, out of range or enum, missing, conflicting alias) is a `tools/call` result with `isError: true`: the error, a `Hint:` line when a did-you-mean row applies, and the tool's arguments, with `_meta["xmustard/argument_error"]`. An unknown tool or a malformed request is JSON-RPC `-32602` (MCP 2025-11-25 error handling). `testdata/old_install_args.json` keeps every argument the June install advertised accepted |
| Streamable HTTP endpoint | `api-go/cmd/xmustard-api/mcp_routes.go` | `POST`/`DELETE /mcp`; `GET /mcp` answers 405 (no server-initiated stream). Query: `workspace`, `mode=full\|readonly`, `client=claude-code\|codex\|cursor\|opencode\|pi\|letta`, `schema=lean\|full`. Sessions belong to the opening principal. An HTTP session has no working directory and does not read the API's environment, so bind a workspace or let the client answer `roots/list` |
| Deprecated stdio shim | `api-go/cmd/xmustard-mcp/` | Runs `mcpserver` in its own process and calls the API's tool routes at `XMUSTARD_API_BASE` (default `http://127.0.0.1:8042`); resolves the workspace from its working directory. Its read loop is a `stdioServer` that classifies each frame into a closed kind (malformed, response, notification, request) |
| HTTP, authentication, exposure | `api-go/cmd/xmustard-api/` (`main.go`, `route_gates.go`, `security_middleware.go`, `workspace_register.go`, `human_presence.go`) | Loopback bind by default; `XMUSTARD_AUTH=auto\|required\|off`; route gate table (profile, role, read-only); exposure middleware ([SECURITY](SECURITY.md)) |
| Governed memory: proposals, votes, edits, recall, drift | `api-go/internal/workspaceops/memory_{propose,verify,edit,recall,rank,render,seen,history,provenance,store}.go` on `api-go/internal/govstore/` | One SQLite WAL database per data dir is the source of truth; every transition is one transaction with an append-only event (principal, time, HEAD, digests); edits add revisions and votes bind to the served revision; one trust-label rule (`govstore.VerificationMode`); a workspace's legacy `context_entries.json` is imported once and kept as `.govstore-import.bak` |
| Injection safety | `api-go/internal/injection/`; `workspaceops/memory_injection.go` | Instruction-pattern scan, data framing, surface policy and capture quarantine. Recall, fetch by id, remember and every evidence projection are scanned and labelled ([SECURITY](SECURITY.md#injection-safety-ws-56)) |
| Secret redaction | `api-go/internal/redact/` | One engine for strings and streams, with rule-named markers. Wired into memory ingest, `why_failed` output, review findings and every evidence capture. The push-mode `redact.Writer` (`stream.go`) buffers 128 KiB, decides 8 KiB at a time with a lookahead of the longest detector span (about 33 KiB), and gives the same bytes as one pass over the input; `secretpath.go` is the secret-path denylist ([SECURITY](SECURITY.md#evidence-capture-redaction-ws-fix-03)) |
| Tool-argument repair | `api-go/internal/toolcompat/` | Maps misspelled keys to canonical names and records every change; the write tools (`remember`, `verify`) are never repaired |
| Evidence delivery | `api-go/internal/evidence/`; the API and MCP evidence routes | Scoped originals, byte-safe pages and search, bounded projection and expiry. The nine tools' results use `xm-reduce/1`. Any tool's output captured through `POST .../evidence/capture` (raw, or a Claude, Codex, Cursor, Pi or OpenCode hook body, stream-decoded) is reduced by a versioned tool-family reducer (`registry.go`: shell, test, build, lint, log, git, diff, grep, read, list, glob, structured) and shaped and validated per client (`shapes.go`: one shaper per entry of the client policy table, which also records aliases such as `claude-code` for Claude; an unknown `client` is refused with 400 `invalid_client`; Codex's text is measured in bytes against its 10,000-byte spill line). A capture may pass a lower-only `target` (1 KiB to 1 MiB, `ObservationInput.Target`; 400 `invalid_target` otherwise) so a client can retain a small output behind a handle before masking or compacting it; each retained original counts at least `MinRetainedCharge` (16 KiB) against the workspace quota. Every capture, from the capture route or a hook, streams through the one shared secret redactor (`redact.Writer` with `workspaceops.OutputRedactor`'s rules, wired in every build in `cmd/xmustard-api/capture_redactor.go`) before it reaches the spool, because originals are retained and searchable: the output of a secret path is refused (422 `secret_path`), and capture fails closed without a working redactor (503 `redaction_unavailable` or `redaction_failed`). Enforced byte admission is not an RSS ceiling |
| Client hook service | `api-go/internal/hooks/` (Claude Code event table, input and output shapes, pattern extraction, per-context steering: a session, and each subagent in it), `api-go/cmd/xmustard-api/hooks_routes.go` (`POST /api/hooks/claude/<Event>`, the Unix socket), `workspaceops/hook_reads.go` (spawn-free reads: workspace by cwd, index hits and syntax errors from a running resident worker only, ground's spawn-free part, memory candidates, client-reported changes fed to the watcher's batch through `watch note`); `api-go/cmd/xmustard-hook/` (static command-hook client); `integrations/claude-code/` (plugin: `hooks/hooks.json`, `.mcp.json`) | PostToolUse captures through the same redactor as the capture route, reduces and returns a shape-matched `updatedToolOutput`; a capture refused for a secret path or a redactor failure leaves the client its native output and is not counted as busy; pre-tool hooks inject index hits and memory through the WS-56 pushed-surface policy; an answer past the hook budget (~200 ms) fails open to an empty 200, while auth refusals stay 401/403; the socket is used only when private to its user (`hooks/transport`); the daemon starts no process for a hook (`rustcore.WithResidentOnly`), counter-asserted; Claude Code starts the static client for the two command hooks (SessionStart, WorktreeRemove); only PostToolUse replaces an output ([SECURITY](SECURITY.md#client-hooks-ws-23)) |
| Grounding | `workspaceops/grounding*.go`; `api-go/internal/groundbudget/` | Composes changes, runs, index drift, memory and principal; `ground` output budget (`sections`, `max_chars` default 6,000, range 2,000 to 65,536, per-section caps, degradation ladder, `output_budget` report) |
| Run-independent outcomes (`why_failed`) | `workspaceops/outcomes.go`, `outcome_commands.go`, `failure_explainer.go`; `cmd/xmustard-api/outcome_routes.go`; `run_outcomes` in govstore | Reads the last MiB of a pasted log, an evidence original or a run; redacts, analyzes and records one outcome per source key; `ground.recent_failed_runs` lists open failures. Command mode is off by default ([SECURITY](SECURITY.md#commands-why_failed-runs-ws-21)) |
| Human approvals | `cmd/xmustard-ops/approval.go`; `workspaceops/human_approval.go`, `human_confirm.go`, `merge_approval.go`; `cmd/xmustard-api/human_presence.go` | `xmustard-ops approve\|reject\|queue\|review`; merge attestations are govstore events and never merge anything; an attestation that cites a review record written by the approver, in open mode or under the approver's owner is refused ([SECURITY](SECURITY.md#human-approvals-ws-57)) |
| Review finding anchoring (review build tag, off by default) | `api-go/internal/anchor/` (Apache-2.0 port of open-code-review's resolver, see NOTICE), `api-go/internal/review/`; `workspaceops/review_anchor.go` and `xmustard-ops review anchor` under `-tags review` | Places quoted code on exact lines without a model: a hunk's new side, its old side, the whole file at head (for a file outside the change, only that), then a unique re-filing to another changed file; several matches anchor nothing. Snippets are at most 40 lines and 4 KiB. Findings arrive as a findings file or an evidence handle (at most 50, 4 MiB, closed item schema) and are anchored against the diff merge approval digests, within line as well as byte bounds; checks are `code_present` (a partial Ground A), `in_changed_hunk` and `in_scope`, each `yes`, `no` or `unknown`. A head over a bound was never searched, so a finding it could decide is `head_unread` and `unchecked`, never `unsupported`, and every finding's own file is read before any re-filing. Evidence only: nothing is stored or approved. `Locate` and `Reanchor` serve memory quoted-code anchors (WS-27, WS-28). `go list -deps` finds neither package in the default binaries |
| Review findings store (review build tag, off by default) | `api-go/internal/govstore/review.go` and migration 3 (`schema_v3_review_subjects.sql`); `workspaceops/review_record.go` and `xmustard-ops review record\|show\|triage` under `-tags review` | A review record and its findings are subjects on the shared events, anchors, outcomes and jobs tables (`subject_kind`), never on tables of their own; review event types cannot be written through `AppendEvent`. A record binds the change merge approval digests and a coverage manifest over every changed file (`reviewed` or `not_reviewed(reason)`; unreported files are `not_reviewed(not_reported)`). Findings are anchored by WS-65 and deduplicated within a lineage: same path and side, lines in the same commit, strict IoU > 0.6 (a single line never matches a range) and the same normalized quoted code is `duplicate_of`; overlap alone is `possible_duplicate` with a `review_duplicate` job; only a distinct principal's `confirm` corroborates. A lineage (by default `<base>@<merge base>:<head branch>`) is bound to its repository and base ref by its first record. A record at a new head first re-anchors, in one batch, the lineage findings whose commit is an ancestor of that head (kept, moved or outdated); the others stay where they are (diverged). Reads carry `anchor_current`. The default builds carry the store code, migration 3 and the attestation check, not the commands. Evidence only: nothing approves a change |
| Tokens, roles, registration, workspace scope | `workspaceops/auth.go`, `auth_roles.go`, `register_roots.go`, `workspace_registry.go` | Roles joined with `+`; scoped auto-registration under `XMUSTARD_REGISTER_ROOTS` |
| Postgres materialization | `workspaceops/pg*.go`, `backend/sql/` | Optional; diagnostics baseline and platform views |
| Rust process invocation | `api-go/internal/rustcore/` | Binary resolution (`XMUSTARD_CORE_BIN`, then `xmustard-core` on `PATH`, then `rust-core/target/release/xmustard-core`, then `cargo run`), deadlines, bounded output, the opt-in resident worker and its governor. `refresh.go` is the watcher's orchestrator: every code-index read and ground registers its root (`WatchRoot`), a `$/refresh.due` notice takes the pending batch and runs `index update --paths` in the heavy slot, a replacement worker registers its predecessor's roots before it takes a call, and `watch` calls run only on the worker. `WithResidentOnly` makes a call run only on a worker that is already running (hooks) |
| Resource accounting | `api-go/internal/budget/` | A 24 MiB transient-byte pool and a helper-child cap: work that does not fit now gets 503/-32000, work that could never fit gets a permanent answer (413, -32600 or a tool error). One heavy slot behind an RSS watchdog, taken by the whole-repository Rust builds (index build and update, legacy graph build, change-tracking baseline) and by bulk imports, never by captures. Go memory limit at the daemon's 28 MiB line with a GOGC floor. Byte admission is not a complete RSS bound. The 25 MiB line an `index build` declares for the heavy slot (`rustcore/heavy.go`) is the release binary's; the Rust memory guard tests (`rust-core/tests/memprobe/`) bound a process's dirty memory (`RssAnon + RssShmem` on Linux) strictly and its RSS by build profile |
| Local operator CLI | `api-go/cmd/xmustard-ops/` | Works on the data dir directly (local filesystem authority, not HTTP-token isolation); `mcp-config` prints client entries for Streamable HTTP or the relay: a `[mcp_servers.xmustard]` table for Codex's `config.toml` with `--client codex`, the JSON `mcpServers` entry otherwise |
| Daemon lifecycle | `api-go/internal/daemon/` (units, service managers, health probe, socket activation, rotated log); `api-go/cmd/xmustard-ops/setup.go` (`setup`, `uninstall`, `daemon`, `store`); `api-go/cmd/xmustard-api/daemon_lifecycle.go`; `govstore/backup.go`; `workspaceops/memory_store_health.go` | A launchd agent (RunAtLoad, KeepAlive on failure) or a systemd socket plus service (socket activation, Restart=on-failure), loopback only, no credential in the unit; setup waits for `/api/health`, puts the previous units back when the daemon does not answer and refuses units it did not write; the API adopts an activated socket under the bind interlock, logs to a size-capped rotated file with stdout and stderr on it, and migrates and quick_checks the store once it listens (a damaged store fails memory calls closed; `store` on `/api/health`); `store backup` (VACUUM INTO, verified) runs beside the daemon, `store restore` needs the store unused, keeps the replaced files and restarts the daemon; the relay waits up to 10 s for a restarting API ([SECURITY](SECURITY.md#daemon-service-ws-58)) |
| Outcome evaluation | `api-go/cmd/xmustard-eval/`; `eval/tasks/parity/` (the `parity-v1` corpus) | Paired arms with real headless clients (`claude -p`, `codex exec --json`, `pi --mode rpc`) or fake drivers; runs outside the measured process tree. The corpus holds eight tasks on pi-mono (MIT) and cline (Apache-2.0) at pinned commits, each with a hidden oracle validated to fail on the starting state and pass on the reference, plus stale-memory (even and odd path depth) and adversarial-injection fixtures; `adversarial_unflagged` must be 0. No real-model run is recorded |
| Pi client adapter | `integrations/pi/` | Pinned extension; the nine tools as direct HTTP calls plus `xmustard_expand` once a handle exists. Its built-in tool reduction, masking and compaction go through capture, which redacts in every build since v0.1.1; a refused capture leaves Pi's own result ([Pi README](../integrations/pi/README.md)). In v0.1.0 they fell back to Pi's own behavior |
| Shared wire models | Go request and record structs, each Rust module's `Serialize` types, `frontend/src/lib/types.ts` | Contract changes need matching consumers |
| Optional operator UI | `frontend/src/` | Needs `XMUSTARD_PROFILE=platform`; outside the current focus |

`go list ./...` in `api-go` reports 20 packages: five commands (`xmustard-api`,
`xmustard-eval`, `xmustard-hook`, `xmustard-mcp`, `xmustard-ops`) and fifteen internal
packages.

## Rust module map

Paths are under `rust-core/src/`.

| Responsibility | Source owner | Interface and constraints |
| --- | --- | --- |
| Subcommand table | `bin/xmustard-core.rs`, `dispatch.rs` | One table serves the one-shot CLI and the resident worker. A handler returns a value and never prints or exits |
| Resident worker | `serve.rs` | `xmustard-core serve`: JSON-RPC 2.0 over stdio with `Content-Length` framing; keeps index snapshots open per repository root; supports cancellation and `$/stats` (which counts every Git child, `indexcache::git_spawns`) |
| Watcher and refresh loop | `index/watch.rs`, `index/refresh_queue.rs` (inside `serve`) | One native watcher per registered root (FSEvents on macOS, inotify on Linux with at most 32,768 directory watches per root; at most 4 roots, least recently used out); 300 ms debounce, 1 s max wait; a full refresh on more than 1,000 pending paths, an ignore-file change, a platform rescan or an OS watch limit; batches never overlap, a batch is taken only after a cookie file written into the Git dir comes back, and failures retry from 0.5 s doubling to 30 s (degraded after 6 in a row); a full verification every hour; `$/refresh.due` and `$/watch.state` notifications; `watcher_state` (ok, overflow, degraded, absent) and the last run's reparsed/reresolved/escalated counters in the freshness envelope; while `ok`, queries spawn no Git and a watched root's repository observation is reused until an event under it. `watch note` takes a client-reported path (hooks) as a native event |
| Code index store | `index/` (`scan`, `extract`, `writer`, `schema`, `facts`, `chunks`, `fts`, `docs`, `csr`, `reader`, `envelope`, `impact`, `ignore`, `lexical`) | One SQLite `index.db` per workspace. Streams one file at a time; full builds swap in a fresh file, updates rewrite changed files under a dirty flag. A declared scale envelope bounds files, bytes and symbols, and coverage says when the index is partial. Readers hold snapshots and report a freshness envelope. `index::facts::flow` owns the returns > branches > writes flow classification every extractor calls |
| Language packs | `index/lang/` (queries in `index/lang/queries/`) | 15 tree-sitter packs behind one provider contract: Rust, Go, TypeScript, TSX, JavaScript, Python, Java, Ruby, C, C++, C#, PHP, Kotlin, Swift and Bash. The release notes list 13 languages (TypeScript, TSX and JavaScript as one). The ten added grammars are Cargo features in the default `lang-all`; a pack built without its grammar is indexed lexically and reported `unsupported` |
| Scope and import resolver | `index/resolve/` | Go and TS/JS: typed symbol edges (CALLS, USES, ACCESSES, EXTENDS, IMPLEMENTS, IMPORTS, HAS_METHOD, HAS_PROPERTY) with confidence tiers (same file 0.95, import-scoped 0.9, global 0.5), provenance and reasons; unbound references are counted per cause in `resolve_drops` |
| Hybrid search | `search.rs`, `index/fts.rs`, `index/rerank.rs` | Six lanes fused with RRF (K=60): `bm25` over function-aligned chunks, `docs` over heading sections, `name`, `fuzzy` (trigram typo tolerance, not meaning), `structural` and `proximity`. The top 40 are reranked. Snippets mask credential-shaped words |
| Secret paths | `secretpath.rs` (copy of `api-go/internal/redact/secretpath.go`) | SSH keys and config, netrc, registry tokens, Docker auth and `.env` files are refused by path before search reads their text (the docs lane and snippet reads); both copies are tested against `testdata/secret_path_golden.tsv` |
| Legacy graph and caches | `symbolgraph.rs`, `indexcache.rs`, `treesitter.rs`, `repomap.rs` | Answers when no index exists yet or the index lags the caller |
| Change tracking | `changetrack.rs`, `hashcache.rs` | Content hashing, the index baseline, drift and dirty symbols; stat-keyed hash cache with a racy-timestamp guard. Drift and working changes read one observation of the worktree, `indexcache::repo_state` (the source identity and its `git status` entries), so with HEAD at the baseline's they compare only the paths Git lists, and report `drift_checked {mode, hashed, stat_skipped, compared}`; ground's drift hashes 0 files on an unchanged tree |
| File-level impact | `symbolgraph.rs` (`file_impact`), `index/reader.rs` (`Snapshot::file_impact`) | `xmustard-core symbolgraph impact-file`, resident in the worker: the files that reference any symbol a file defines, up to 4 hops; the legacy graph and the index snapshot share one `dependents` walk with their symbol impact, so the two forms cannot drift; `found: false` when the graph does not hold the path |
| Syntax check for hooks | `syntaxcheck.rs` | `xmustard-core syntax-check`, resident only: tree-sitter ERROR and MISSING nodes by position and line digest, no content, root-confined, secret paths refused, at most 20 errors per file |
| Diagnostics and language servers | `diagnostics.rs`, `lsp.rs`, `lsp_session.rs` | LSP is optional |
| Verification and retained goal runtime | `verification.rs`, `goalruntime.rs` | Bounded process execution for verification and the `/goal` contract |
| Stdio relay | `bin/xmustard-relay.rs` | std-only, no async runtime; plain `http://` only; the token goes only to a loopback host unless `--allow-insecure-remote`; a POST whose connection is refused (the API restarting) retries with backoff for up to 10 s, and nothing is sent on a refused connection, so no request repeats |

The default Rust build does not enable `semantic-onnx`, the opt-in neural lane.

## Binaries

A release archive holds five binaries and the LICENSE. `make release VERSION=vX.Y.Z`
builds it for the host (Rust with `cargo build --release --locked`, Go with
`CGO_ENABLED=0 -trimpath`, build paths remapped, archive entries with the commit's
time, fixed modes and owner 0), and `.github/workflows/release.yml` runs it on a
`v*` tag for linux-x86_64 on `ubuntu-22.04` and darwin-arm64 on `macos-15`, then
drafts a GitHub release with the archives, their `.sha256` files and `SHA256SUMS`.
The owner publishes the draft; `packaging/homebrew/bump.sh <tag>` then moves the
formula to it.

| Binary | Language | Role |
| --- | --- | --- |
| `xmustard-api` | Go | The API and the `/mcp` endpoint; `xmustard-api mint-token <id> [role]` mints a token with no server running |
| `xmustard-core` | Rust | Repository semantics, one-shot or as the resident worker |
| `xmustard-relay` | Rust | Stdio relay to `/mcp` for command-only clients |
| `xmustard-mcp` | Go | Deprecated stdio shim, kept for compatibility |
| `xmustard-ops` | Go | Local operator CLI: approvals, merge attestations, `mcp-config`, `setup` and `uninstall`, `daemon status\|restart\|stop`, `store backup\|check\|restore` |

`make build` builds all five from source, and `make install PREFIX=/usr/local` copies
them into `$(PREFIX)/bin` (`Makefile`). The Claude Code plugin's static hook client,
`xmustard-hook` (Go, about 3.5 MB, under 5 MiB RSS), is not in the archive or in
`make build`: the plugin's README builds it into `integrations/claude-code/hooks/bin/`.
Release builds leave the `review` build tag off.

## Public agent interface

The nine MCP tools are `ground`, `recall`, `remember`, `verify`, `search`, `explain`,
`impact`, `diagnostics` and `why_failed`. `impact` takes one of no arguments (the
current changes), `symbol=`, `path=` (a file's blast radius) or `from=` and `to=`. MCP
also advertises `resources/list` and
`resources/read` for authorized evidence recovery, for `xmustard://docs/tools`, the
reference to arguments that `tools/list` does not list, and for `xmustard://memory/index`,
the promoted memory titles. Resources are not a tenth
tool. The Pi adapter adds `xmustard_expand` only once a recovery handle exists. The
wider HTTP platform exists for the UI; it does not widen the agent interface.

The API serves the core profile by default. `XMUSTARD_PROFILE=platform` adds the
platform routes the UI uses ([SECURITY](SECURITY.md#profiles)). Independent
verification needs distinct principals, and `verify` needs the `verifier` role.

## Known seams

- **One-shot and resident paths differ.** The resident worker is opt-in
  (`XMUSTARD_CORE_WORKER=1`) and stays off until the parity-scale budget gate
  passes. Without it every call opens the store once and exits, nothing is watched
  (reads refresh the index themselves), and the Claude Code hooks add no index hits
  and no syntax report. After the worker's 2-minute idle exit nothing is watched
  until the next call starts a worker.
- **Each refresh batch scans the tree.** A batch's `index update --paths` still runs
  `git ls-files -s` and stats every tracked file: 16 to 23 ms at 5,000 tracked
  files and 114 to 153 ms at 50,000 on the Linux build box
  ([WS-15 record](plans/2026-09-25-parity-build-plan.md#ws-15--watcher-incremental-refresh-loop-and-identity-unification)).
  The Go identity cache still validates with its own stat walk.
- **Two packaged client adapters.** `integrations/pi` and the Claude Code plugin
  (`integrations/claude-code`). Codex, OpenCode and Cursor get MCP configuration
  only (`?client=` labels, `xmustard-ops mcp-config --client`); the capture route
  already decodes and shapes their hook bodies (`evidence/hookbody.go`,
  `evidence/shapes.go`), for their adapters (WS-40).
- **`impact` walks a lexical graph.** Edges come from name matches and import lines,
  so distance 1 or more is a lead, not proof. The typed resolver edges are walked
  by the core's `xmustard-core index impact`, which no tool calls yet.
- **`diagnostics` needs Postgres.** Database-free diagnostics is planned, not built.
- **Two large Go units.** `workspaceops` and `cmd/xmustard-api` are big. Extract
  modules around concrete shared invariants, such as one recall transaction or one
  process lifecycle, with tests through their interface. Moving directories alone
  repairs nothing.

### Fixed since v0.1.0

- **Capture was refused in release builds.** v0.1.0 wired no streaming secret
  redactor into `captureRedactor` (`cmd/xmustard-api/evidence_capture_routes.go`),
  so `POST .../evidence/capture` answered `503 redaction_unavailable`; only the
  `-tags xmustard_e2e` test build installed one. The tool-family reducers, the Pi
  adapter's built-in tool reduction and compaction, and capture from client hooks
  did not work. v0.1.1 wires `redact.Writer` into every build (WS-FIX-03).
- **Drift was missed for even-depth anchors.** `openWorkspaceFileBeneath`
  (`workspaceops/safepath_unix.go`) closed the root descriptor during its walk, then
  compared the final descriptor with the root's old number. When the OS handed that
  number back, a present file read as missing, and the memory recorded a missing
  baseline. In a v0.1.0 run, a memory anchored to `pkg/auth.go` was never flagged
  stale after the file changed, while one anchored to `main.go` was: every path with
  an even number of components was affected. The walk still refused symlinks, so
  confinement held. v0.1.1 tracks whether a component was opened instead (`7d380c4`,
  `TestConfinedReadOfEveryDepth`).

## Historical: the 2026-09-24 candidate

This section is kept as history; v0.1.0 and later supersede it.

On 2026-09-24 this page described an uncommitted candidate at `cd13e2b` on
`feat/product-v1`. It said the Rust core ran only on demand with no resident index
daemon, and that JSON files were the operational write authority, with optional
Postgres as a queryable materialization. The fixed sampled process-tree RSS gate
passed then at 80.6 MB, after two same-source runs at 72.3 MB and 84.9 MB
([benchmark and raw evidence](benchmarks/2026-09-24-lean-context.md)). The baseline
defects of that time are in the [Go audit](reviews/2026-09-24-go-audit.md) and the
[Rust audit](reviews/2026-09-24-rust-audit.md), with repairs in the
[implementation report](reviews/2026-09-24-implementation-results.md) and the
[Rust report](reviews/2026-09-24-rust-implementation-results.md). The earlier
architecture page is in [history](history/architecture-before-2026-09-24.md).

## Historical and local material

- `backend/` holds data and SQL, not an active Python application.
- `archive/2026-06-16-python-backend/` is a local retired implementation.
- `archive/codex-sessions/`, historical handoffs, `docs/prompts/` and `goal/` preserve
  provenance. They are not runtime inputs or current specifications.
- `research/` contains local reference clones and is ignored by Git.
- Some runtime files under `backend/data/` are still tracked despite ignore rules.
  Untracking them waits on the owner's approval.
