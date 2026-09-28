# Architecture

Source: tag `v0.1.0` (`8fca2d5`), released 2026-09-28. What shipped and its known
limits: [v0.1.0 release notes](releases/v0.1.0.md). Product direction:
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
  '-- Pi -> integrations/pi extension --------> xmustard-api  tool routes (+ xmustard_expand)
            '-- Pi built-in tool output -------> .../evidence/capture: 503 in release builds

xmustard-api (Go; default 127.0.0.1:8042)
  exposure -> body limit -> auth -> route gates -> evidence delivery -> handlers
  |-- mcpserver     nine tools, schemas, workspace resolution (in process for /mcp)
  |-- workspaceops  memory, grounding, outcomes, registration, tokens, diagnostics
  |     |-- govstore    governance.db (SQLite WAL): memory, votes, events, outcomes
  |     |-- injection   instruction-pattern scan, data framing, quarantine
  |     '-- redact      secret redaction on memory ingest and command output
  |-- evidence      scoped originals, bounded projections, recovery handles
  |-- groundbudget  ground's output budget
  |-- budget        transient-byte pool, helper-child cap, heavy slot, RSS governor
  '-- rustcore  --> xmustard-core (Rust): one process per call by default, or one
                    resident `xmustard-core serve` worker with XMUSTARD_CORE_WORKER=1
                      |-- index/        index.db per repository
                      |-- search.rs     hybrid search (six lanes, RRF K=60)
                      '-- changetrack, symbolgraph, explain, diagnostics, verification

xmustard-ops (Go CLI)   -> the data dir directly: approvals, merge attestations; mcp-config only prints
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
  reports its memory and recycles it under pressure.

## Where state lives

| State | Location | Owner |
| --- | --- | --- |
| Governed memory: entries, revisions, votes, append-only events, sessions, feedback, run outcomes, merge attestations | `<data dir>/governance.db`, one SQLite database in WAL mode | `api-go/internal/govstore/` |
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
| MCP protocol and tool schemas | `api-go/internal/mcpserver/`: the tool table (`tools.go`), one `tool_<name>.go` per tool, `streamable.go` (Streamable HTTP), `evidence.go` (evidence delivery and resources), `elicitation.go` (human confirmation), `compat.go` (argument repair via `toolcompat`) | Nine tools in a fixed order; protocol 2025-06-18, with 2024-11-05 negotiated; closed schemas with bounds; `tools/list` limited to the caller's usable tools (`GET /api/auth/whoami`) and held under tested byte caps per schema profile, protocol version and role (`testdata/tools_list_budget.json`); advanced arguments accepted but listed only with the full schema profile and documented at `xmustard://docs/tools` |
| Streamable HTTP endpoint | `api-go/cmd/xmustard-api/mcp_routes.go` | `POST`/`DELETE /mcp`; `GET /mcp` answers 405 (no server-initiated stream). Query: `workspace`, `mode=full\|readonly`, `client=claude-code\|codex\|cursor\|opencode\|pi\|letta`, `schema=lean\|full`. Sessions belong to the opening principal. An HTTP session has no working directory and does not read the API's environment, so bind a workspace or let the client answer `roots/list` |
| Deprecated stdio shim | `api-go/cmd/xmustard-mcp/` | Runs `mcpserver` in its own process and calls the API's tool routes at `XMUSTARD_API_BASE` (default `http://127.0.0.1:8042`); resolves the workspace from its working directory |
| HTTP, authentication, exposure | `api-go/cmd/xmustard-api/` (`main.go`, `route_gates.go`, `security_middleware.go`, `workspace_register.go`, `human_presence.go`) | Loopback bind by default; `XMUSTARD_AUTH=auto\|required\|off`; route gate table (profile, role, read-only); exposure middleware ([SECURITY](SECURITY.md)) |
| Governed memory: proposals, votes, edits, recall, drift | `api-go/internal/workspaceops/memory_{propose,verify,edit,recall,rank,render,seen,history,provenance,store}.go` on `api-go/internal/govstore/` | One SQLite WAL database per data dir is the source of truth; every transition is one transaction with an append-only event (principal, time, HEAD, digests); edits add revisions and votes bind to the served revision; one trust-label rule (`govstore.VerificationMode`); a workspace's legacy `context_entries.json` is imported once and kept as `.govstore-import.bak` |
| Injection safety | `api-go/internal/injection/`; `workspaceops/memory_injection.go` | Instruction-pattern scan, data framing, surface policy and capture quarantine. Recall, fetch by id, remember and every evidence projection are scanned and labelled ([SECURITY](SECURITY.md#injection-safety-ws-56)) |
| Secret redaction | `api-go/internal/redact/` | One engine for strings and streams, with rule-named markers. Wired into memory ingest and `why_failed` output. The streaming form (`stream.go`) is not yet wired into evidence capture, which is why capture is refused in release builds |
| Tool-argument repair | `api-go/internal/toolcompat/` | Maps misspelled keys to canonical names and records every change; the write tools (`remember`, `verify`) are never repaired |
| Evidence delivery | `api-go/internal/evidence/`; the API and MCP evidence routes | Scoped originals, byte-safe pages and search, bounded projection and expiry. The nine tools' results use `xm-reduce/1`, and only they reach the store in release builds. Captured native output is built to go through a per-family reducer (`registry.go`: shell, test, build, lint, log, git, diff, grep, read, list, glob, structured) and a per-client shape (`shapes.go`), but capture answers `503 redaction_unavailable` in release builds: no streaming redactor is wired into `captureRedactor`, so it fails closed. Only the `xmustard_e2e` test build wires one. The production redactor is planned for v0.1.1 |
| Grounding | `workspaceops/grounding*.go`; `api-go/internal/groundbudget/` | Composes changes, runs, index drift, memory and principal; `ground` output budget (`sections`, `max_chars` default 6,000, range 2,000 to 65,536, per-section caps, degradation ladder, `output_budget` report) |
| Run-independent outcomes (`why_failed`) | `workspaceops/outcomes.go`, `outcome_commands.go`, `failure_explainer.go`; `cmd/xmustard-api/outcome_routes.go`; `run_outcomes` in govstore | Reads the last MiB of a pasted log, an evidence original or a run; redacts, analyzes and records one outcome per source key; `ground.recent_failed_runs` lists open failures. Command mode is off by default ([SECURITY](SECURITY.md#commands-why_failed-runs-ws-21)) |
| Human approvals | `cmd/xmustard-ops/approval.go`; `workspaceops/human_approval.go`, `human_confirm.go`, `merge_approval.go`; `cmd/xmustard-api/human_presence.go` | `xmustard-ops approve\|reject\|queue\|review`; merge attestations are govstore events and never merge anything ([SECURITY](SECURITY.md#human-approvals-ws-57)) |
| Tokens, roles, registration, workspace scope | `workspaceops/auth.go`, `auth_roles.go`, `register_roots.go`, `workspace_registry.go` | Roles joined with `+`; scoped auto-registration under `XMUSTARD_REGISTER_ROOTS` |
| Postgres materialization | `workspaceops/pg*.go`, `backend/sql/` | Optional; diagnostics baseline and platform views |
| Rust process invocation | `api-go/internal/rustcore/` | Binary resolution (`XMUSTARD_CORE_BIN`, then `xmustard-core` on `PATH`, then `rust-core/target/release/xmustard-core`, then `cargo run`), deadlines, bounded output, the opt-in resident worker and its governor |
| Resource accounting | `api-go/internal/budget/` | A 24 MiB transient-byte pool and a helper-child cap: work that does not fit now gets 503/-32000, work that could never fit gets a permanent answer (413, -32600 or a tool error). One heavy slot behind an RSS watchdog, taken by the whole-repository Rust builds (index build and update, legacy graph build, change-tracking baseline) and by bulk imports, never by captures. Go memory limit at the daemon's 28 MiB line with a GOGC floor. Byte admission is not a complete RSS bound |
| Local operator CLI | `api-go/cmd/xmustard-ops/` | Works on the data dir directly (local filesystem authority, not HTTP-token isolation); `mcp-config` prints client entries for Streamable HTTP or the relay |
| Outcome evaluation | `api-go/cmd/xmustard-eval/` | Paired arms with real headless clients (`claude -p`, `codex exec --json`, `pi --mode rpc`) or fake drivers; runs outside the measured process tree |
| Pi client adapter | `integrations/pi/` | Pinned extension; the nine tools as direct HTTP calls plus `xmustard_expand` once a handle exists. Its built-in tool reduction, masking and compaction need capture, so in release builds reduction and compaction fall back to Pi's own behavior and masking covers only results that already carry a handle ([Pi README](../integrations/pi/README.md)). The only packaged client adapter in v0.1.0 |
| Shared wire models | Go request and record structs, each Rust module's `Serialize` types, `frontend/src/lib/types.ts` | Contract changes need matching consumers |
| Optional operator UI | `frontend/src/` | Needs `XMUSTARD_PROFILE=platform`; outside the current focus |

`go list ./...` in `api-go` reports 14 packages: the four commands above and ten
internal packages.

## Rust module map

Paths are under `rust-core/src/`.

| Responsibility | Source owner | Interface and constraints |
| --- | --- | --- |
| Subcommand table | `bin/xmustard-core.rs`, `dispatch.rs` | One table serves the one-shot CLI and the resident worker. A handler returns a value and never prints or exits |
| Resident worker | `serve.rs` | `xmustard-core serve`: JSON-RPC 2.0 over stdio with `Content-Length` framing; keeps index snapshots open per repository root; supports cancellation and `$/stats` |
| Code index store | `index/` (`scan`, `extract`, `writer`, `schema`, `facts`, `chunks`, `fts`, `docs`, `csr`, `reader`, `envelope`, `impact`, `ignore`, `lexical`) | One SQLite `index.db` per workspace. Streams one file at a time; full builds swap in a fresh file, updates rewrite changed files under a dirty flag. A declared scale envelope bounds files, bytes and symbols, and coverage says when the index is partial. Readers hold snapshots and report a freshness envelope |
| Language packs | `index/lang/` (queries in `index/lang/queries/`) | 15 tree-sitter packs behind one provider contract: Rust, Go, TypeScript, TSX, JavaScript, Python, Java, Ruby, C, C++, C#, PHP, Kotlin, Swift and Bash. The release notes list 13 languages (TypeScript, TSX and JavaScript as one). The ten added grammars are Cargo features in the default `lang-all`; a pack built without its grammar is indexed lexically and reported `unsupported` |
| Scope and import resolver | `index/resolve/` | Go and TS/JS: typed symbol edges (CALLS, USES, ACCESSES, EXTENDS, IMPLEMENTS, IMPORTS, HAS_METHOD, HAS_PROPERTY) with confidence tiers (same file 0.95, import-scoped 0.9, global 0.5), provenance and reasons; unbound references are counted per cause in `resolve_drops` |
| Hybrid search | `search.rs`, `index/fts.rs`, `index/rerank.rs` | Six lanes fused with RRF (K=60): `bm25` over function-aligned chunks, `docs` over heading sections, `name`, `fuzzy` (trigram typo tolerance, not meaning), `structural` and `proximity`. The top 40 are reranked. Snippets mask credential-shaped words |
| Secret paths | `secretpath.rs` (copy of `api-go/internal/redact/secretpath.go`) | SSH keys and config, netrc, registry tokens, Docker auth and `.env` files are refused by path before search reads their text (the docs lane and snippet reads); both copies are tested against `testdata/secret_path_golden.tsv` |
| Legacy graph and caches | `symbolgraph.rs`, `indexcache.rs`, `treesitter.rs`, `repomap.rs` | Answers when no index exists yet or the index lags the caller |
| Change tracking | `changetrack.rs`, `hashcache.rs` | Content hashing, the index baseline, drift and dirty symbols; stat-keyed hash cache with a racy-timestamp guard |
| Diagnostics and language servers | `diagnostics.rs`, `lsp.rs`, `lsp_session.rs` | LSP is optional |
| Verification and retained goal runtime | `verification.rs`, `goalruntime.rs` | Bounded process execution for verification and the `/goal` contract |
| Stdio relay | `bin/xmustard-relay.rs` | std-only, no async runtime; plain `http://` only; the token goes only to a loopback host unless `--allow-insecure-remote` |

The default Rust build does not enable `semantic-onnx`, the opt-in neural lane.

## Binaries

A release archive holds five binaries and the LICENSE.

| Binary | Language | Role |
| --- | --- | --- |
| `xmustard-api` | Go | The API and the `/mcp` endpoint; `xmustard-api mint-token <id> [role]` mints a token with no server running |
| `xmustard-core` | Rust | Repository semantics, one-shot or as the resident worker |
| `xmustard-relay` | Rust | Stdio relay to `/mcp` for command-only clients |
| `xmustard-mcp` | Go | Deprecated stdio shim, kept for compatibility |
| `xmustard-ops` | Go | Local operator CLI: approvals, merge attestations, `mcp-config` |

`make build` builds all five from source, and `make install PREFIX=/usr/local` copies
them into `$(PREFIX)/bin` (`Makefile`).

## Public agent interface

The nine MCP tools are `ground`, `recall`, `remember`, `verify`, `search`, `explain`,
`impact`, `diagnostics` and `why_failed`. MCP also advertises `resources/list` and
`resources/read` for authorized evidence recovery, for `xmustard://docs/tools`, the
reference to arguments that `tools/list` does not list, and for `xmustard://memory/index`,
the promoted memory titles. Resources are not a tenth
tool. The Pi adapter adds `xmustard_expand` only once a recovery handle exists. The
wider HTTP platform exists for the UI; it does not widen the agent interface.

The API serves the core profile by default. `XMUSTARD_PROFILE=platform` adds the
platform routes the UI uses ([SECURITY](SECURITY.md#profiles)). Independent
verification needs distinct principals, and `verify` needs the `verifier` role.

## Known seams in v0.1.0

- **Capture is refused in release builds.** `POST .../evidence/capture` answers
  `503 redaction_unavailable`, because no streaming secret redactor is wired into
  `captureRedactor` (`cmd/xmustard-api/evidence_capture_routes.go`). Only the
  `-tags xmustard_e2e` test build installs one. It fails closed on purpose: a captured
  original is retained and searchable, so it must be redacted first. So in v0.1.0 the
  tool-family reducers, the Pi adapter's built-in tool reduction and compaction, and
  capture from client hooks do not work; Pi falls back to its own behavior, and its
  masking covers only results that already carry a handle. The nine tools' own results
  are unaffected. The production redactor is the next fix, planned for v0.1.1.
- **One packaged client adapter.** `integrations/pi` is the only one. Codex and
  OpenCode get MCP configuration only (`?client=` labels, `xmustard-ops mcp-config
  --client`). There is no Codex hook package or OpenCode plugin yet; the capture route
  already decodes their hook bodies (`evidence/hookbody.go`), for when capture works.
- **Drift is missed for some nested anchors.** `openWorkspaceFileBeneath`
  (`workspaceops/safepath_unix.go`) closes the root descriptor during its walk, then
  compares the final descriptor with the root's old number. When the OS hands that
  number back, a present file reads as missing, and the memory records a missing
  baseline. In a v0.1.0 run, a memory anchored to `pkg/auth.go` was never flagged
  stale after the file changed, while one anchored to `main.go` was. A copy of the
  function misreads two- and four-component paths and reads one- and three-component
  paths correctly. The walk still refuses symlinks, so confinement holds.
- **`impact` walks a lexical graph.** Edges come from name matches and import lines,
  so distance 1 or more is a lead, not proof. The typed resolver edges are walked
  by the core's `xmustard-core index impact`, which no tool calls yet.
- **`diagnostics` needs Postgres.** Database-free diagnostics is planned, not built.
- **The resident worker is opt-in.** It stays off until the parity-scale budget gate
  passes. Without it, every call opens the store once and exits.
- **Two large Go units.** `workspaceops` and `cmd/xmustard-api` are big. Extract
  modules around concrete shared invariants, such as one recall transaction or one
  process lifecycle, with tests through their interface. Moving directories alone
  repairs nothing.

## Historical: the 2026-09-24 candidate

This section is kept as history; v0.1.0 supersedes it.

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
