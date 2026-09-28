# Parity build plan — 2026-09-25

Source: `docs/research/PARITY_REQUIREMENTS_2026-09-25.md` (requirements, processes, budget) with the critic's corrections from its §12 applied here. Integration branch: `feat/parity-v2`. Each workstream is built on its own `parity/<id>` branch in an isolated worktree, reviewed adversarially through at least two lenses, fixed, rechecked, and stacked onto `feat/parity-v2`. **The human performs the merge to `main`.** Agent reviews and green tests do not authorize it.

## Execution decisions taken for this build

- **Benchmark fixtures** are Apache/MIT repositories (cline, pi-mono). GitNexus (PolyForm Noncommercial) is a design reference only and is not run as a comparator arm without an explicit owner decision.
- **D-02 storage** is closed by measurement in WS-07's first deliverable (FTS5 in rusqlite vs tantivy).
- **D-08 hook client** for Cursor is Rust from the start; Claude Code uses http hooks.
- **Parity thresholds** are numeric and set in WS-50 before any parity claim.
- **Diagnostics work** in `wip/diagnostics-live-set` belongs to a parallel session; it is not in HEAD and touches `budget.go`, `rustcore/root.go`, `main.go`, and `xmustard-ops/main.go`. Workstreams touching those files keep diffs small and additive so that session can rebase.

## Workstreams

| ID | Wave | Size | Lang | MiB | Depends on | Title |
|---|---|---|---|---|---|---|
| WS-00 | 1 | M | go+rust | 0 | — | Review, rebase and prepare the in-flight parity/w0-* branches for human merge |
| WS-01 | 1 | L | go | 3 | — | govstore: SQLite WAL governance store package |
| WS-02 | 1 | L | go+rust | 3 | — | Resident Rust worker (xmustard-core serve) plus Go supervisor client |
| WS-03 | 1 | L | go | 0.2 | WS-00 | Per-request context kernel: workspace registry, identity sampled once, evidence page identity cache, grounding split |
| WS-04 | 1 | L | go | 0 | WS-00 | MCP server package and protocol modernization |
| WS-05 | 1 | M | go | 0.5 | — | toolcompat and redact libraries |
| WS-06 | 1 | M | go | 0.5 | — | Budget governor, runtime hygiene and health budget/counters |
| WS-07 | 1 | L | rust | 0 | — | Code index DB and streaming per-file fact extraction (existing languages) |
| WS-08 | 1 | L | go | 1.5 | — | Evidence tool-family reducers, client shape adapters, capture metadata and search-in-original |
| WS-09 | 1 | M | go | 0 | WS-00 | Security kernel: roles, gates, exposure posture and core-only default |
| WS-10 | 1 | M | mixed | 0 | — | Parity-scale budget gate v2 and CI |
| WS-11 | 1 | L | go | 0 | — | Outcome evaluation executor (xmustard-eval) |
| WS-12 | 2 | L | go | 0 | WS-00, WS-01 | Governance cutover to govstore and split of context_governance.go |
| WS-13 | 2 | L | go+rust | -10 | WS-04, WS-05, WS-09 | MCP Streamable HTTP endpoint on the API and native stdio relay |
| WS-14 | 2 | L | go+rust | 12 | WS-02, WS-07, WS-06 | Resident index service, query side: CSR graph, name index, freshness envelope, cap removal |
| WS-15 | 2 | L | go+rust | 2 | WS-14, WS-06, WS-22, WS-17 | Watcher, incremental refresh loop and identity unification |
| WS-16 | 2 | L | rust | 0 | WS-07 | Language packs via the provider contract |
| WS-17 | 2 | L | rust | 2 | WS-07 | Scope/import resolver for Go and TS/JS with typed edges, confidence tiers and epistemic counters |
| WS-18 | 2 | L | go+rust | 5 | WS-07, WS-14 | Code BM25 lane, hybrid fusion, snippets and explainable reasons |
| WS-19 | 2 | L | go | 1 | WS-12, WS-04, WS-05 | Memory lifecycle writes: history API, supersede/retire/retract/purge, CAS edits, expiry, provenance, verify outcomes |
| WS-20 | 2 | L | go | 1.5 | WS-12, WS-04 | Recall v2: ranking, filters, disclosure, budgets and verification-queue visibility |
| WS-21 | 2 | M | go | 0.5 | WS-01, WS-03, WS-04 | why_failed and ground failure signals without platform runs |
| WS-21B | 3 | M | go+rust | 0 | WS-21 | why_failed command-mode trust policy and hardening; one environment-scrub policy in the Rust core for every run-* subcommand (follow-up of Astra decision B) |
| WS-22 | 2 | M | go+rust | 0 | WS-03, WS-09 | Governed automatic index baseline and bounding of residual unbounded work |
| WS-23 | 2 | L | go | 1.5 | WS-03, WS-08, WS-05, WS-04, WS-14, WS-20 | Hook service, static hook client and Claude Code adapter plugin |
| WS-24 | 2 | M | ts | 0 | WS-08, WS-04; production capture needs WS-05's redactor wired into `captureRedactor` | Pi adapter extended to built-in tools, masking and custom compaction |
| WS-25 | 2 | S | rust | 0 | WS-00, WS-02 | Remove remaining unreachable Rust code |
| WS-26 | 2 | S | mixed | 0 | WS-10 | Release hygiene and packaging |
| WS-27 | 3 | L | go | 3 | WS-19, WS-14 | Dedupe, code anchors, tiered conflicts and structured claims |
| WS-28 | 3 | L | go | 1 | WS-27, WS-15 | Change-driven invalidation, as_of, graph-proximity recall and memories on explain/impact |
| WS-29 | 3 | L | go | 0.5 | WS-19, WS-20, WS-09 | Memory scopes and shared collections with grants |
| WS-30 | 3 | M | go+rust | 0.5 | WS-14, WS-04 | Cross-workspace code query, residency LRU, worktree identity and path rewriting |
| WS-31 | 3 | L | go | 1 | WS-19, WS-13, WS-23 | Tiered memory, write policy, revision-pinned injection, guidance artifacts and onboarding |
| WS-32 | 3 | L | go | 1 | WS-27, WS-19 | Consolidation work queue, memory health, skills export and Markdown/git export-import |
| WS-33 | 3 | L | go | 1.5 | WS-23, WS-12 | Session ledger, compaction snapshot and restore, handoff capsule and cross-client handoff |
| WS-34 | 3 | L | go | 1.5 | WS-33, WS-05 | Session transcript store, session search, new-session bootstrap and cross-harness import |
| WS-35 | 3 | L | go+rust | 0.5 | WS-17, WS-14 | Impact v2: tiers, risk, epistemic envelope, typed filters, trace caps, diff-to-symbol and import cycles |
| WS-36 | 3 | L | go+rust | 1 | WS-14, WS-17, WS-18 | Symbol navigation surfaces and staged degradation contract |
| WS-37 | 3 | L | rust | 9 | WS-18 | Static-embedding semantic lane with binary/int8 vector index |
| WS-38 | 3 | M | go+rust | 1 | WS-17 | Functional areas, execution flows, orientation packet and flow-grouped search |
| WS-39 | 3 | L | rust | 0 | WS-17, WS-16 | Resolvers for Rust, Python and Java |
| WS-40 | 3 | L | mixed | 0 | WS-23, WS-08 | Codex, OpenCode and Cursor adapters, Letta recipe and adapter capability registry |
| WS-41 | 3 | M | go+rust | 0.5 | WS-08, WS-03 | Command-wrapper reducer, retention classes and upstream references |
| WS-42 | 3 | M | go | 1.5 | WS-08, WS-03 | Repeat guard, revision-keyed result cache and recoverable history compaction library |
| WS-43 | 3 | L | go | 5 | WS-13, WS-08, WS-19 | Opt-in upstream MCP gateway, progress event bus and memory notifications |
| WS-44 | 3 | L | go | 0 | WS-40, WS-23 | Install/doctor/uninstall per client, conformance suite, ops CLI, ignore/retention config and fixture capture |
| WS-45 | 3 | M | go | 1.5 | WS-36, WS-10 | Opt-in LSP pool wiring into explain, impact and diagnostics |
| WS-46 | 3 | M | rust | 0 | WS-17 | SCIP precise-lane ingestion |
| WS-47 | 3 | M | go | 1 | WS-35 | Diagnostics tool deepening after the diagnostics session hands off |
| WS-48 | 3 | M | go | 0 | WS-13, WS-23 | Single config struct replacing repeated env lookups |
| WS-49 | 3 | L | go | -4 | WS-48, WS-12 | Kernel package extraction and platform behind a build tag |
| WS-50 | 3 | L | mixed | 0 | WS-11, WS-10, WS-31, WS-33, WS-35, WS-37, WS-40 | Parity evaluation suite and final parity-scale gate |
| WS-51 | 3 | M | go+rust | 0.5 | WS-06, WS-37 | Helper-model adapter contract, optional ONNX sidecar and Needle3 measurement |
| WS-52 | 3 | M | go+rust | 1 | WS-18, WS-27, WS-35 | Revision-aware reads, commit/diff history search and task-shaped retrieval |
| WS-53 | 3 | M | go | 2 | WS-21, WS-19 | Bounded verification and run-lineage runner |
| WS-54 | 2 | — | go | — | WS-04 | Output-budget contract for ground and tools/list (critic addition) |
| WS-55 | 3 | — | go+rust | — | WS-14, WS-15 | Worktree overlay index (critic addition) |
| WS-56 | 2 | — | go | — | WS-12, WS-09 | Injection-safety policy (critic addition) |
| WS-57 | 2 | — | go | — | WS-09, WS-12 | Human-approval surface (critic addition) |
| WS-58 | 2 | — | go | — | WS-01, WS-13 | Daemon lifecycle (critic addition) |
| WS-59 | 3 | — | go+rust | — | WS-17, WS-18 | impact op=rename|delete plans and search mode=exact|regex (critic addition) |
| WS-60 | 3 | — | rust | — | WS-16, WS-38 | Framework entry-point extractors and extra language packs (critic addition) |
| WS-61 | 3 | — | rust | — | WS-14, WS-17 | Personalized PageRank repo map fitted to a token budget (critic addition) |
| WS-62 | 3 | — | go | — | WS-19, WS-31 | Path-scoped client rule-file export (critic addition) |
| WS-63 | 2 | — | mixed | — | WS-11 | Evaluation corpus authoring (critic addition) |

## Workstream detail

### WS-00 — Review, rebase and prepare the in-flight parity/w0-* branches for human merge

**Goal.** Get the already-written wave-0 fixes into a merge-ready stack on top of HEAD 7ab61a0 so later workstreams build on them instead of colliding with them. The fixes are: models.rs removal and honest tool descriptions (w0-cleanup), honest open mode plus author-bound edits plus strict JSON (w0-kernel), feedback taken off the request path (w0-feedback), and stat-cached drift hashing (w0-drift). A human performs every merge; the agent only prepares the stack.

**Requirements.** PAR-RT-08, PAR-ADP-01, PAR-GOV-01, PAR-SEC-01, PAR-RET-11, PAR-FRESH-01

**Files.** `rust-core/src/lib.rs`, `rust-core/src/models.rs`, `rust-core/src/changetrack.rs`, `rust-core/src/indexcache.rs`, `rust-core/src/symbolgraph.rs`, `rust-core/src/hashcache.rs`, `api-go/cmd/xmustard-mcp/main.go`, `api-go/cmd/xmustard-mcp/descriptions_test.go`, `integrations/pi/src/tools.ts`, `api-go/cmd/xmustard-api/main.go`, `api-go/cmd/xmustard-api/feedback_shutdown_test.go`, `api-go/internal/workspaceops/context_governance.go`, `api-go/internal/workspaceops/grounding.go`, `api-go/internal/workspaceops/feedback.go`, `api-go/internal/workspaceops/feedback_recorder.go`, `api-go/internal/workspaceops/knowledge.go`, `api-go/internal/workspaceops/shutdown.go`  
**New modules.** `rust-core/src/hashcache.rs`, `api-go/internal/workspaceops/feedback_recorder.go`

**Tests required.**
- make check-backend on each branch rebased individually onto 7ab61a0
- make check-backend on the combined stack in order cleanup -> kernel -> feedback -> drift
- Conflict-resolution tests for context_governance.go and knowledge.go, which both w0-kernel and w0-feedback touch
- Existing descriptions_test.go and Pi tools.ts parity test pass
- Drift tests with same-size edits and preserved mtimes still detect changes, because a full-verify path exists

**Acceptance.** Four rebased branches plus a combined integration branch, all passing cargo test/clippy and go test/build, with review notes that list each behavioral change and its risk. No merge is done by the agent. The human merges in the documented order.

**Collision risk.** The hot files for everything else: main.go, context_governance.go, grounding.go, knowledge.go, xmustard-mcp/main.go, lib.rs, symbolgraph.rs, changetrack.rs, indexcache.rs. Must land before WS-03, WS-04, WS-09 and WS-12. It does not touch the diagnostics session files. That session's branch at /private/tmp/xmustard-opus-l9b1F4 sits at the older cd13e2b and also modified main.go and context_governance.go, so coordinate before its eventual landing.

**Correction.** DONE by the orchestrating session: the four w0 branches were implemented, reviewed twice, and re-fixed; they sit on 7ab61a0 and are stacked onto feat/parity-v2 (integration branch). Human merges feat/parity-v2 to main.

### WS-01 — govstore: SQLite WAL governance store package

**Goal.** Add a new package with a transactional, cross-process-safe governance store. It uses modernc.org/sqlite (cached locally as v1.59.0). Its schema is designed up front for history, supersession, temporal validity, anchors, claims, sessions, outcomes, grants, jobs and FTS5, so later memory features only add queries. Nothing consumes it yet; WS-12 cuts over to it.

**Requirements.** PAR-STORE-01, PAR-PROV-01, PAR-STORE-04

**Files.** `api-go/go.mod`, `api-go/go.sum`, `api-go/internal/govstore/store.go`, `api-go/internal/govstore/schema.sql`, `api-go/internal/govstore/migrate.go`, `api-go/internal/govstore/entries.go`, `api-go/internal/govstore/revisions.go`, `api-go/internal/govstore/events.go`, `api-go/internal/govstore/votes.go`, `api-go/internal/govstore/anchors.go`, `api-go/internal/govstore/sessions.go`, `api-go/internal/govstore/outcomes.go`, `api-go/internal/govstore/grants.go`, `api-go/internal/govstore/jobs.go`, `api-go/internal/govstore/fts.go`, `api-go/internal/govstore/retention.go`, `api-go/internal/govstore/import_json.go`, `api-go/internal/govstore/store_test.go`, `api-go/internal/govstore/concurrency_test.go`, `api-go/internal/govstore/import_test.go`  
**New modules.** `api-go/internal/govstore`

**Tests required.**
- Migrations are idempotent and a schema fingerprint is recorded
- Compare-and-set on base_revision returns a conflict carrying the current revision and digest
- The events table is append-only: update and delete are rejected by triggers
- Two OS processes (helper-process test) propose and verify concurrently with no lost updates
- A kill -9 helper mid-transaction leaves a consistent database after reopen
- The JSON importer round-trips a context_entries.json fixture including verifications, path hashes and content digests, and is idempotent
- Pragmas asserted: journal_mode=wal, cache_size cap, mmap_size=0, busy_timeout, synchronous level for governance commits
- FTS5 query over title, content and anchors returns ranked results
- Build-tagged RSS probe: open adds ≤3 MiB; a 100k-row bulk insert inside a single scope stays ≤14 MiB above baseline

**Acceptance.** The govstore package exposes a Store interface (entries, revisions, votes, events, anchors, claims, relations, sessions, outcomes, grants, jobs, feedback, evidence_meta) with passing tests and measured RSS within the numbers above. There are no consumers yet. go test ./... is green.

**Collision risk.** go.mod/go.sum only. No other wave-1 workstream adds Go dependencies. No diagnostics-session files.

### WS-02 — Resident Rust worker (xmustard-core serve) plus Go supervisor client

**Goal.** Stop exec'ing xmustard-core on every call. Add a long-lived JSON-RPC serve mode that dispatches the existing commands in-process and keeps the graph snapshot cached per source identity. Add a Go worker client that follows the cursor-bridge CodexClient/MCPClient pattern: pending map, read loop, failAllPending, lazy restart with backoff, cancel messages. It keeps the TrackChild/KillProcessTree kill boundary and ChildLimit admission. It is off by default behind XMUSTARD_CORE_WORKER until the gate passes.

**Requirements.** PAR-RT-01, PAR-RT-12

**Files.** `rust-core/src/serve.rs`, `rust-core/src/lib.rs`, `rust-core/src/bin/xmustard-core.rs`, `api-go/internal/rustcore/worker.go`, `api-go/internal/rustcore/worker_test.go`, `api-go/internal/rustcore/root.go`, `api-go/internal/rustcore/children.go`  
**New modules.** `rust-core/src/serve.rs`, `api-go/internal/rustcore/worker.go`

**Tests required.**
- Rust unit tests for framing (length/line caps, 64 MiB frame limit) and dispatch of each method the Go side calls
- Parity test: worker output equals one-shot CLI output for search, symbolgraph build/impact/trace/clusters/hotspots/blast-radius, explain-path, changetrack, repo-key, path-symbols and build-repo-map on a fixture repo
- Crash mid-request returns an error to the caller; the next call restarts the worker
- Context cancellation sends a cancel message and releases the child slot
- Idle exit after the configured timeout, and lazy restart afterwards
- Concurrent requests bounded by ChildLimit
- Every existing api-go test passes with XMUSTARD_CORE_WORKER=1

**Acceptance.** With the flag on, the nine tools run with zero per-call xmustard-core execs; only git children remain until WS-15. Warm-call latency and process counts are reported against the one-shot path. A subcommand dispatch table is introduced so later workstreams add one entry each.

**Collision risk.** rust-core/src/bin/xmustard-core.rs and lib.rs are also touched by WS-00 (cleanup, drift) and WS-07, so land the dispatch-table refactor first and keep it small. WS-03 changed the `repo-key` handler of xmustard-core.rs (`repo_key` in the dispatch table calls `repo_key_identity`, which adds `ignored_dirs`). root.go was touched at an older base by the diagnostics session's tree; that change is likely already at HEAD, so risk is low. No diagnostics_*.go files.

**Follow-up from WS-03.** Carry the request's repository identity (observed once per request by the Go RequestContext) on the worker request, so symbol-graph paths reuse it instead of recomputing `source_identity` (PAR-FRESH-02's remaining part; WS-14 keys the graph cache by it).

**Implementation record (branch parity/ws-02, 2026-09-25).** These notes record what was built and measured, and where it differs from the text above.
- *Files outside the list.* `rust-core/src/dispatch.rs` (new: table types), `symbolgraph.rs` (`symbol_graph_for_query`, `symbol_graph_with_coverage` and the resident-snapshot hook in `cached_graph`), `search.rs`, `wiki.rs` and `ownership.rs` (they read the graph through those functions), `Cargo.toml` (rustix `stdio` feature), one comment line in each of ten `workspaceops` `*Ctx` functions, and `api-go/cmd/xmustard-api/worker_process_test.go`. WS-14 and WS-18 should rebase onto `symbol_graph_for_query` and `symbol_graph_with_coverage` instead of calling `build_symbol_graph_cached`. The dispatch-table commit also converts one-shot-only handlers (goal, swarm, bench, the lsp-* commands) to return values. WS-25 deletes a command by removing its table entry and its handler.
- *Residency.* These stay one-shot: `symbolgraph build` (it rebuilds and prints the whole graph), `build-lsp`, `blast-radius` (it reads every tracked file on each call and uses no cached graph) and `changetrack index`. `symbolgraph hotspots` and `ownership subsystems` now read the shared cached graph, so they are resident and in the worker-versus-CLI parity cases. For build and blast-radius, the parity requirement above is met by construction, because the one-shot CLI is the only code path. The Go client does not start a worker for a one-shot call. It uses the last handshake when it has one, and otherwise a hint table that a real-core test checks.
- *Cancellation.* A cancelled caller returns at once. Its ChildLimit slot is released when the worker has ended the request, not at the cancel, so admission counts abandoned work that is still running. A queued request is answered -32800 at once. If a running handler is still busy 2 s after the cancel, the worker is retired and replaced. Other calls already on it finish under their own deadlines, and it exits when none is left. One slow request no longer fails unrelated calls.
- *Budget.* Record these against WS-02 in the WS-10 ledger, not against WS-14's CSR line. They were measured on a pi-mono clone (1,929 tracked files, 800 graph files, 6,607 symbols), using ps RSS of one worker. The worker base is 6.6 MiB after `initialize`, against the 3 MiB base line. With one resident JSON `SymbolGraph`, the worker reaches 15.3–15.7 MiB after impact calls: +8.7 MiB, or about 1.35 KB per symbol, not WS-14's 150 B per symbol target. A mixed sequence (impact, clusters, trace, two searches, subsystems, hotspots) plateaued at 22.5–22.6 MiB. Before this round, subsystems rebuilt the graph and reached 32.6–37.1 MiB. XMUSTARD_CORE_WORKER stays off by default until WS-14's CSR replaces the JSON snapshot (or `--max-snapshots=0` is set) and the WS-10 gate measures the tree.
- *Idle trim (PAR-RT-01): partial.* The trim drops the snapshots, but RSS fell only 0–3.5 MiB in 6 s (from 19–23 MiB). Freed memory stays in the allocator, and `#![forbid(unsafe_code)]` rules out a malloc purge through FFI. Memory is returned when the worker exits: at the 2-minute idle exit, or through `RecycleCoreWorker`, the lever for WS-06's governor.
- *Tests with the flag on.* `go test ./...` passes with XMUSTARD_CORE_WORKER=1. The process-level API tests (`startAPIProc`) remove inherited XMUSTARD_* settings on purpose, because their scripted cores cannot serve. `worker_process_test.go` starts the real API with the flag and a real core, and checks the tool routes, the exec count and shutdown.
- *Spawn-free (PAR-RT-12) is conditional.* A warm worker serves the nine tools' Rust work with zero xmustard-core execs; the process-level API test checks this. A burst of calls after a gap longer than the idle timeout (default 2 minutes) costs one `serve` exec and a graph load from disk.
- *After merging with WS-06.* The worker path applies the same budget hooks as the one-shot path: the worker starts with `coreChildEnv` (the Linux `MALLOC_ARENA_MAX` cap), its start counts as one core spawn (`noteCoreSpawn`), and each call's result is held to `coreStdoutCap`, so output the pool can never hold is the permanent "output too large" error, not a retryable overload. Still open at that point: nothing called `RecycleCoreWorker` (no governor hook or RSS-pressure trigger), the worker had no `budget.Reserve` line (its RSS showed under `helper_children`), and `/api/health` did not report `CoreWorkerStats`. WS-06B closes all three (see its record below).

### WS-03 — Per-request context kernel: workspace registry, identity sampled once, evidence page identity cache, grounding split

**Goal.** Stop tool paths from parsing snapshot.json (5-6 times per ground) and from sampling repository identity repeatedly. Add a resident workspace registry and a per-request context that carries the root and identity to handlers and Rust. Write tools skip identity entirely. Cache identity per evidence page window, keyed by HEAD plus porcelain/stat digest. Split grounding.go into section files to reduce later collisions.

**Requirements.** PAR-RT-06, PAR-FRESH-02, PAR-RT-12, PAR-RT-11

**Files.** `api-go/internal/workspaceops/workspace_registry.go`, `api-go/internal/workspaceops/request_context.go`, `api-go/internal/workspaceops/changes.go`, `api-go/internal/workspaceops/coverage.go`, `api-go/internal/workspaceops/grounding.go`, `api-go/internal/workspaceops/grounding_index.go`, `api-go/internal/workspaceops/grounding_memory.go`, `api-go/internal/workspaceops/grounding_runs.go`, `api-go/internal/workspaceops/run_review_reads.go`, `api-go/internal/workspaceops/workspace_reads.go`, `api-go/internal/workspaceops/repo_identity.go`, `api-go/cmd/xmustard-api/evidence_routes.go`, `api-go/internal/evidence/store.go`, `api-go/internal/workspaceops/workspace_registry_test.go`, `api-go/internal/workspaceops/request_context_test.go`  
**New modules.** `api-go/internal/workspaceops/workspace_registry.go`, `api-go/internal/workspaceops/request_context.go`

**Tests required.**
- A counting hook shows zero loadSnapshot calls on the nine tool paths
- Identity is sampled exactly once per core read tool and zero times for remember/verify
- Evidence pages inside the TTL reuse identity with no repo-key spawn while HEAD and digest are unchanged; they re-sample and label stale when changed
- The registry invalidates on snapshot or workspace-record mtime change
- ground surfaces decode and ListRuns errors as unknown rather than 0
- Allocation benchmark for ground shows reduced allocs versus baseline
- Existing grounding_bounded_test.go and recall_contract_test.go pass

**Acceptance.** ground does ≤1 identity sample and 0 snapshot.json parses. Expanding a 16 MiB original no longer spawns a process per page, and its measured expansion time is recorded. grounding.go is split into section files with no behavior change.

**Collision risk.** grounding.go (w0-kernel via WS-00; later WS-20/21/22/28/31/33 own the section files this creates). evidence/store.go is shared with WS-08, which only adds files plus a reduce.go hook; WS-03 owns the store.go change. evidence_routes.go is also touched by WS-41 later. workspace_reads.go is not touched by the diagnostics session at HEAD.

**Closeout notes (parity/ws-03).**
- Files touched beyond the list above: `grounding_session.go` (created here; WS-33 and WS-34 modify it), `repo_stat_unix.go` / `repo_stat_other.go` (new: the spawn-free working-tree fingerprint), `symbolgraph.go` and `workspace_lifecycle.go` (registry lookups in place of snapshot parses; WS-22 owns both), `knowledge.go` and `semantic_materialization.go` (registry lookups; WS-14 and WS-18 touch knowledge.go), `frontend/src/lib/api.ts` (SessionGrounding nullable counts; type-checked: `npm run build`, i.e. `tsc -b` plus `vite build`, passes in the ws-03 worktree, and `npm run lint` reports only 4 existing errors in AdminPanel.tsx and MemoryPanel.tsx, none in api.ts), `rust-core/src/indexcache.rs` and `rust-core/src/bin/xmustard-core.rs` (`repo-key` also reports `ignored_dirs`, the directories git ignores as a whole, from the same `git status` run with `--ignored=matching`; the key is unchanged), and `scripts/e2e/mcp_evidence.py`.
- `ground` reports an undeterminable count or blocked flag as null (listed under `unknown`). WS-04's portable schema subset has no null type, so at the merge into feat/parity-v2 the `ground` outputSchema stopped declaring the members that can be null (the counts, the blocked flags, `stale_memory`). It declares `workspace_id`, `summary` and `generated_at`. The schema stays open, so the other members still arrive, and a client that validates structuredContent never sees a null where a type was promised (tool_ground.go; pinned by `TestGroundOutputSchemaDeclaresNoNullableMember`).
- Search in an original (WS-08's `Store.Search`) follows the rule `Store.Read` follows since the merge into feat/parity-v2. It reads the current identity only for a bound capture, so searching a hook observation or a posted result runs no repo-key. It also reports `current_key_cached` and `current_key_age_ms`.
- PAR-FRESH-02 is partial. The per-request identity is observed once and consumed by the evidence middleware (before and after execution). Handlers take the request's resolved root but not its identity, and the Rust graph paths (`build_symbol_graph_cached`, `build_symbol_graph`, `build_graph` in symbolgraph.rs) still compute their own `source_identity` per call. Passing the request identity to the core is a follow-up for WS-02 (a request field on the worker protocol) and WS-14 (graph cache keyed by it).
- Freshness semantics: a page labelled `current` is current as of an identity observation at most `current_key_age_ms` old. The identity cache reuses an identity only while a fingerprint of everything `git status --untracked-files=all` reads is identical and settled under Git's racy rule. That covers every non-ignored directory (its own stat key, its listing and each file's stat key), the index, HEAD and the loose refs it resolves through (by content and by stat key), packed-refs and reftable, a linked worktree's or submodule's gitfile and `commondir`, the repository config and `config.worktree`, the config files any config includes (`[include]` / `[includeIf]`), and the ignore and attributes files they name. Where no earlier walk exists to compare with (a cold-cache handler, or a sample paired with a walk taken at another moment), only stat keys expose a change to the racy rule. Each directory's own stat key is what exposes a removed file (review round 2: before it, a removal could bind or cache the pre-removal key). The stat keys of HEAD and the loose refs expose a branch moved by `git reset --soft` or `git update-ref`, which rewrites only the loose ref (review round 3: before them, such a move during a cold-cache handler bound the result, and a move between a sample and its pairing walk cached the pre-move key). A loose ref, the index, a file in `info/` or the XDG ignore or attributes file that is missing enters by the stat keys of the directories that held it, which expose its removal (review round 4: before them, `git update-ref -d HEAD`, the usual way to undo a root commit, bound the result during a cold-cache handler and cached the pre-removal key between an isolated read and the read that paired its sample; removing the index or `info/exclude` did the same). Those directories are keyed only while the file is missing, so writing another branch while the checked-out one is a loose ref costs no cache hit. A linked worktree's `info/sparse-checkout` is keyed in its own git dir, where git reads it. The blind spots that remain are listed in `repo_stat_unix.go`: system config under other install prefixes, config injected through `GIT_CONFIG_COUNT` / `GIT_CONFIG_PARAMETERS` in repo-key's environment, `%(prefix)` include paths, the clocks of network filesystems, and, where no earlier walk exists to compare with, the removal of `config.worktree`, of the global, system or an included config file, or of a `core.excludesFile` / `core.attributesFile` outside `info/` and the XDG git directory (the directories that would date it change too often to key). Across calls the 5 s TTL bounds them; for capture binding the window is the handler's duration.
- Identity read cost against base, where base is one repo-key run per read. An evidence page reads identity through `CurrentRepoIdentity`. Measured on a clone of research/auto-code-rover (37,318 tracked files) with the real core on an Apple M1 under load average 8-12 from other agents. Figures are medians of 7 per run, given as the range over 3-4 runs:
  - base (one repo-key run): 251-372 ms;
  - hit (one walk): 75-109 ms;
  - cold read with no live pairing, i.e. the first read or a read after a used pairing (one run plus one pairing walk): 291-511 ms, 1.0-1.9x base;
  - isolated read after a pairing expired unused (one run, with the pairing walk skipped): 225-353 ms, the same as base. Its sample is kept unpaired, and the next read within the TTL pairs it with that read's own walk when the tree has been quiet since before the sample. That read costs one walk, like a hit (not separately timed), so a burst of N pages after an LLM turn longer than the TTL costs one run and N-1 walks. Before review round 3 the second page paid a run plus a walk, so the burst cost two runs;
  - read after a settled change (one walk plus one run): 324-462 ms;
  - read after a change inside the racy window (walk, run, walk): 488-575 ms.
  The cost judge turns the walk off for 60 s once two walks in a row cost more than half a run (a single costly walk did until WS-FIX-02). These measurements ran with the judge disabled so every path could be timed.

### WS-FIX-02 — Deflake TestExpanding16MiBOriginalSpawnsNoProcessPerPage on Linux

**Symptom.** On feat/parity-v2 (abff6d9) the remote Linux gate failed about one run in four in cmd/xmustard-api with `--- FAIL: TestExpanding16MiBOriginalSpawnsNoProcessPerPage`. Reproduced on the build box with `go test -count=20 -run TestExpanding16MiB` (2 of 20 failed: "249 pages spawned repo-key 212 times; want 0", and 127 times). The `http: panic serving ... tool handler panic` line in the failing log is not this test: `evidence_seam_test.go` panics a handler on purpose, and the package log shows it whenever any test in the package fails.

**Root cause.** The identity cache's cost judge (`repo_identity.go`) turned the fingerprint off for `fingerprintRetryAfter` (60 s) whenever the last single walk cost more than half the smoothed repo-key time. A walk's wall time includes any GC pause or preemption it sits through. Instrumented on the build box over 1,609 walks of the fixture: p50 90 us, p90 119 us, p99 1.2 ms, max 3.4 ms, against a fake repo-key of 4-11 ms. One stalled walk during the 16 MiB expansion (a failing run logged walk 2.69 ms against repo-key 3.89 ms) turned the fingerprint off, and every later page sampled repo-key. It was not pool admission, the identity TTL or a cleanup race: those paths cost at most one sample per TTL. The same one-sample judgment applies in production, where one GC pause or preemption during a walk disabled the cache for a minute.

**Fix (product).** The judge acts on the cheaper of the last two walks (`rootState.noteWalk`): one stall no longer turns the fingerprint off; a tree that is really costly to walk is costly twice in a row and is judged off on the second walk. After judging off, both measurements reset so the next probes measure afresh. Tests: `TestCostJudgeIgnoresOneStalledWalk` (a stall among cheap walks never turns it off; two in a row do), and the "walk not clearly cheaper than repo-key" case now expects the second walk to turn the fingerprint off. No timeout was raised and no retry added. After the fix: 40/40 runs of the test pass on the build box, and 15/15 runs of the four identity route tests pass while `go test ./internal/...` loads the box.

**Decision: 16 MiB original against the 24 MiB default pool.** `DefaultMaxOriginal` stays 16 MiB and is not derived from the pool, and core output is not streamed to the evidence spool. The spool streams to disk in O(window) memory, so posted results and streamed tool output reach 16 MiB under any pool. A core-backed tool decodes and re-encodes the core's JSON before the spool sees it, so streaming core stdout past the handler would not remove the in-memory copy; the pool-derived core output cap (`coreStdoutCap`) is that path's real bound: 6.98 MiB under the default pool, 16 MiB from a pool of about 52 MiB. Past it the tool answers the permanent "output too large" error, not a truncated capture. The 16 MiB acceptance test keeps its 64 MiB pool because only a core-backed read binds its capture to an identity. `TestCoreOutputCapAgainstTheEvidenceCaptureLimit` (rustcore) pins those figures; the comment on `DefaultMaxOriginal` records the decision.

### WS-FIX-04 — Client adapter corrections and a per-client shaper registry

**Source.** The 2026-09-28 integrations comparison (xMustard against OpenCodeReview, checked against `research/codex` at 44fe510 and `research/opencode` at 03e6717), its findings C1-C12 and O1-O14 and adopt items 1-6, 11 and 14. Requirements touched: PAR-CTX-01 and PAR-CTX-03 (hook bodies, per-client shapes), PAR-ADP-05 (client profiles, `mcp-config`), PAR-ADP-07 and PAR-ADP-08 (the server side of the Codex and OpenCode adapters; the host-side hooks and plugin stay WS-40a/b), and the WS-11 eval driver.

**Implementation record (branch parity/ws-fix-04, 2026-09-28).** These notes record what was built and measured, and where it differs from the direction the workstream was given.
- *One shaper registry (adopt 11, §7).* `evidence/shapes.go` no longer switches on the client string in `shapeName`, `buildPayload` and `ValidateShape`, and `claudeBuiltin`/`claudeFixups` are gone. Each `clientPolicies` entry carries a `shaper` (unexported interface: `shape`, `build`, `validate`, `foreignTool`) with five implementations: `claudeShaper` (a table of the built-ins, each with its validator and its scalar fixups), `codexShaper`, `piShaper`, `opencodeShaper`, and `mcpShaper` for mcp, letta and http, which `cursorShaper` embeds and overrides only to observe built-ins. Adding a client is one entry. The shared guards live in one place: `textOnly` refuses a body with status members or image blocks a text payload cannot carry.
- *claude vs claude-code.* A policy lists its aliases; `claude-code` (the MCP client profile) resolves to the `claude` policy through `LookupClient`, and a capture records the canonical name. `POST .../evidence/capture` refuses an unknown `?client=` with 400 `invalid_client` and lists `ClientNames()`; before, `client=claude-code` silently got the http envelope. `PolicyFor` keeps its http fallback for in-package callers (`client` blank or raw).
- *Codex policy (C1, C8).* `MaxChars` is 10,000 and counts bytes (`MaxUnit: "bytes"`): Codex spills hook text past 2,500 approximate tokens of 4 bytes. `Target` is 8,464 bytes, the 10,000 less a 1,536-byte reserve for the recovery line and the data-framing note. The reason ends with the recovery line (the note moved before it), so a spilled reason's kept tail still names the handle; every other client keeps the recovery line first, then the note. The additional-context notice is capped in bytes for a byte-measured client. Image blocks now refuse the replacement for every text-only payload.
- *Hook bodies (C2, O5, cross-client bug).* An `error` member marks a body failed only when it is a non-empty string (`"error": null`, `""` or an object says nothing). FormatCodex reads `turn_id`, `agent_id` and `transcript_path` (new `HookBody` and `CaptureMeta` fields) and no longer maps `call_id` or `error`, which Codex never sends. `exitKeys` include `exit` (OpenCode `metadata.exit`). The decoder's metadata roles fill their fields through one table instead of a case per role.
- *OpenCode names (O7, O8).* `opencodeShaper.foreignTool` treats an underscored name as `<server>_<tool>` (or a plugin's `<namespace>_<tool>`) unless it is one of OpenCode's underscored built-ins (`apply_patch`, `plan_enter`, `plan_exit`), so another server's JSON result is reduced as structured output; the other clients keep `NamespacedTool` (Cursor now uses it too; it covers the old `isMCPTool` prefixes). toolcompat aliases `patchText` to `patch`.
- *codex_args (C11).* `sanitizeCodexArgs` is an allow-list. The flags xMustard pins (model, working root, sandbox, approval, JSON output, `exec`) are dropped as before; `--ephemeral`, `--strict-config`, `--oss`, `--color` (always, never, auto) and `--local-provider` (lmstudio, ollama) pass; anything else is refused as invalid input (400), both when the settings are saved and when a run is built.
- *mcp-config (C5, C6).* `--client codex` prints a `[mcp_servers.xmustard]` table for `config.toml`: `url`, `bearer_token_env_var = "XMUSTARD_API_TOKEN"` and `http_headers` with `X-Xmustard-Workspace`, or for the relay `command`, `args` and `env_vars = ["XMUSTARD_API_TOKEN"]`. Other clients keep the JSON `mcpServers` form. The URL and the relay's `--client` carry the normalized profile name. README shows the Codex form.
- *Pi recall.* The Pi adapter sends `max_chars=4000` on recall, the MCP tool's default, so both clients issue the same request; a unit test reads the Go constant.
- *Eval (C10).* A Codex `{"type":"error"}` event is fatal only when no `turn.completed` follows it; the error text is the last error's. `cache_write_input_tokens` is read (it is part of `input_tokens`, like cached reads), and an unset `cache_write_per_mtok` prices cache writes at the input rate.
- *Fixtures.* `api-go/internal/evidence/testdata/codex/0.156.1/` holds three PostToolUse bodies recorded from the installed codex-cli 0.156.1 (a small Bash call, a 3,006-line Bash call, an MCP call) by `testdata/codex/record/record.sh`: `codex exec` runs against a local mock Responses API and a command hook logs the payload, with no model call; local paths are replaced by `/Users/dev/...`. `testdata/codex/schema/post_tool_use_subagent.json` is not a recording: it is derived from `hooks/src/schema.rs` `PostToolUseCommandInput` at 44fe510 (a subagent call with `agent_id`, `agent_type` and a null `transcript_path`), which the recorder cannot produce.
- *Measured on codex-cli 0.156.1 (local, mock model).*
  - A block reason of exactly 10,000 bytes reached the model unchanged; 10,001 bytes came back as a 10,103-byte head-and-tail preview plus `Full hook output saved to: <temp path>`, with the last line kept. The reason replaced the tool output the model saw (`function_call_output`).
  - The Bash `tool_response` is the output text alone, already Codex's own preview: 40,109 bytes for a 3,006-line output, with the middle cut (`…9732 tokens truncated…`) before any hook runs, so a failure in the middle is gone before xMustard sees it. The exit status appears only in the model-facing header (`Process exited with code 3`), never in the payload (C4 holds).
  - An MCP call that needed approval, and one whose result had `isError: true`, produced no PostToolUse payload (C3 holds). A successful MCP call's `tool_response` is `{content, isError: false}` and its `tool_name` is `mcp__cilogs__fetch_log`.
  - `codex exec --json` usage carries `cache_write_input_tokens`, and a run against an unreachable backend emitted `{"type":"error","message":"Reconnecting... n/5 ..."}` before its final error (C10 holds).
  - Both `mcp-config` TOML forms load: `codex mcp list --json` showed the http entry as `streamable_http` with `bearer_token_env_var` and the header, and the relay entry as `stdio` with `env_vars`.
  - With a 96-character workspace id and an injection flag, the recovery line and note were 1,002 bytes and the reason 8,718 bytes (`TestCodexReasonKeepsTheHandleLastWithTheInjectionNote`).
- *Deviations.*
  - Target is 8,464 bytes, not a round 9 KiB: a 9 KiB projection plus a 1 KB footer would pass the spill limit. The 1,536-byte reserve holds workspace ids up to about 200 characters with every injection flag set; past that the byte check fails the shape and Codex gets the fallback notice, never a spilled reason.
  - The image-block refusal applies to every text-only shaper (codex, mcp, letta, http, cursor, opencode, pi), not only Codex: the guard is shared, and those payloads dropped images silently before.
  - `codex_args` refuses more than the flags listed in the direction: also `-p/--profile` (a config layer that can set the sandbox), `--ignore-rules`, `-o/--output-last-message`, `--worktree`, `-i/--image` and positional arguments. Two tests that expected `--profile` and `--config=fast` to pass were rewritten.
  - The capture route's client check and the `turn_id`/`transcript_path` fields in `CaptureMeta` go beyond the listed items; both follow from the registry and the new roles.
  - OpenCode's MCP payload (O1: set `content`, not `output`) and its `mcp` config form are unchanged; they wait for WS-40b's check against the installed OpenCode 2.0.1.
  - The first recording attempt set `openai_base_url`, which 0.156.1 ignored: its requests went to api.openai.com with a dummy key and were refused with 401, so no model ran. The recorder uses a custom model provider instead.
  - Pi was not type-checked (`npm run typecheck` needs `node_modules`, which this worktree lacks); `node --test test/unit.test.ts` passed 45/45.
- *Checks.* No Rust file changed. On the Linux box, `xm-remote-check.sh all -count=1` passed on its second run: every Rust test target and all 14 Go packages passed, `go vet ./...` was clean, 6 clippy warnings (the base's count), remote_exit=0. The first run failed only outside this change: govstore could not create its databases because the box's shared `/tmp` filled for a moment ("database or disk is full"; the Go suite then passed on its own), and `index_build`'s debug-binary 25 MiB peak test, which sits at its line (WS-15 record), failed once and passed three times alone. Pi: `node --test test/unit.test.ts` 45/45 locally.

### WS-FIX-06 — Memory guard tests measure the memory the budget protects

**Source.** An orchestrator decision (recorded in followups). The WS-23 merge was refused because `index_build::build_of_5000_files_has_no_file_cap_and_peaks_under_25_mib` measured the total RSS of the debug core. By the orchestrator's measurement, WS-23's added debug code (about 290 KB) moved file-backed pages from 24.6 to 25.0 MiB, onto the assert, while the peak RssAnon stayed about 4.2 MiB and the release binary peaked at 16.5-16.7 MiB. `index_query::resident_rss_on_a_100k_symbol_resolved_graph_stays_within_the_line` also failed under box load. The direction: follow WS-16's all-packs test with two bounds per guard, a strict one on the process's own dirty memory set from measured values with headroom and a looser total-RSS one for debug builds, note that the 25 MiB heavy-slot line is a release-binary line, make the resident test robust to load, and weaken no other assertion. No PAR row names this workstream; the guards serve PAR-IMP-02 (WS-14's service line) and WS-07's heavy-slot line.

**Implementation record (branch parity/ws-fix-06, 2026-09-28).** These notes record what was built and measured, and where it differs from the direction.
- *One probe (`rust-core/tests/memprobe/mod.rs`, a module each test crate includes).* `sample(pid)` reads one process: on Linux its `/proc/<pid>/status` (`VmRSS`, `VmHWM`, and `RssAnon + RssShmem` as the dirty reading); on macOS `proc_pid_rusage(RUSAGE_INFO_V4)` (resident size, physical footprint and its lifetime maximum). `child_peaks` runs a command to completion and returns the child's dirty peak (sampled every millisecond on Linux, exact on macOS), its own RSS peak and the wait4(2) maximum, which covers its `git` children. `rss_line(release, debug)` picks an RSS bound by the build profile: `cargo test` builds the core in the test's profile. The probe replaces the two wait4 helpers of `index_build.rs`, the one in `lang_fixtures.rs` and the `ps` spawns of `index_query.rs`.
- *Index build, 5,000 files.* Strict bound: dirty peak ≤ 6 MiB on Linux (measured 4.3 MiB with the debug binary and 3.9 with the release one, the same in every run). RSS (the wait4 maximum, as before): ≤ 25 MiB, the heavy slot's line, in a release build; ≤ 32 MiB in a debug build. The constant's comment says the line is the release binary's: the Go side declares 25 MiB when `index build` takes the heavy slot (`rustcore/heavy.go`), and gate v2 measures the release core (see the deviations).
- *Resident service.* Both readings come from the service's own status file, sampled from the test process; nothing is spawned per sample, and the swap sampler keeps separate maxima for RSS and dirty memory. Sequential queries: dirty ≤ +12 MiB above base (measured +9.4 to +10.0); RSS ≤ +15 MiB (the acceptance line) in a release build and ≤ +20 MiB in a debug build. Overlapping queries: dirty ≤ +18 MiB (measured +10.2 to +14.8); the RSS regression guard stays +32 MiB in both builds. About 5 MiB of the debug RSS delta is file-backed: code pages first touched by the query paths after the base reading.
- *Other guards.* The nested/dense build, the 120,000-entry build and the max-size ignore-file build keep their 25 MiB RSS bounds on the same basis (wait4 maximum, worker VmHWM, worker VmHWM) through the shared probe, and now print their dirty peaks. The all-packs test in `lang_fixtures.rs` keeps its bounds (dirty ≤ 25, RSS ≤ 64); its Linux dirty reading now adds `RssShmem` (0 for the core), so it cannot read lower than before.
- *Measurements (Linux build box, x86_64, 6 cores, shared with other agents at load average 5-20; the core binary is the same before and after, only the tests changed).*

  | Guard | Before (old metric) | Dirty after | RSS after, debug | RSS after, release | Bounds now |
  |---|---|---|---|---|---|
  | index build, 5,000 files | debug RSS 24.5-24.9 MiB over 5 runs against 25.0; release 15.8-17.0 over 4 | 4.3 MiB debug (8 runs), 3.9 release (3) | 23.6-24.3 | 15.9-16.1 | dirty 6; RSS 25 release, 32 debug |
  | resident, sequential (above base) | debug RSS +13.4 to +14.9 over 2 runs against 15 | +9.4 to +10.0 (4 debug, 2 release) | +14.3 to +15.6 (4 runs) | +12.9 to +13.4 (2) | dirty 12; RSS 15 release, 20 debug |
  | resident, overlapping (above base) | debug RSS +13.5 to +18.6 against the 32 guard | +10.2 to +14.8 | +15.7 to +19.2 | +15.1 to +18.4 | dirty 18; RSS 32 |
  | nested/dense, 120k entries, max-size ignore files | same metric | 4.5, 2.3-2.4 and 1.6 | 20.1-21.0, 19.1-19.8 and 18.0-18.7 | — | unchanged: RSS 25 |
  | all packs (`lang_fixtures`) | same metric (dirty now adds RssShmem) | 10.5 | 47.8-48.5 | — | unchanged: dirty 25, RSS 64 |

  Three of the four sequential debug runs after the change read more than +15.0 MiB of RSS in some phase (+15.2, +15.4 and +15.6), one of them beside another index test binary: the old assertion would have failed them. Their dirty deltas were +9.8 to +10.0.
- *Deviations.*
  - The direction names `/proc/self/status`. The core runs as the test's child, so the probe reads that child's `/proc/<pid>/status`: the file the core itself reads as `/proc/self/status`.
  - macOS: the index build's strict dirty bound is not asserted there, and the comment says why: no macOS measurement of this fixture exists (nothing is compiled on the Mac in this program), and the footprint counts allocator state that RssAnon leaves out. macOS still asserts the RSS bound (25 MiB release, 32 debug). The resident test stays ignored off Linux, as WS-26 left it. The macOS branch of the probe was type-checked and clippy-clean for `aarch64-apple-darwin` on the box (the target was added with rustup); it was not run.
  - The 25 MiB note is narrower than "enforced by gate v2". Gate v2 runs the release core, whose reads build and update the code index in the heavy slot (`index update` from `ensureCodeIndex`), and gates the process tree's peak (95.4 MiB) and each process's growth against the ledger, but it has no absolute check of one index build against 25 MiB. The direct check of the line on this fixture is a release run of the test (`cargo test --release --test index_build`); `make check-backend` runs the tests in the debug profile only, and adding a release test build to it is left to the owner.
  - No retry on a load spike. The dirty readings did not move with load (the run beside another index test binary gave the same range), so the test measures dirty memory instead of retrying. What moved was the file-backed part of RSS.
  - The release RSS delta of the sequential case (+12.9 to +13.4 MiB) is above WS-14's recorded +8.9 to +10.6: later workstreams changed the service, which was not investigated here. It leaves 1.6 MiB to the 15 MiB line in a release run.
  - The other index_build guards were not given the dual bounds: in a debug build that would loosen their 25 MiB RSS bounds, which the direction rules out. They sit 4.0-7.0 MiB below it in debug builds.
- *Checks.* Only Rust test files and this record changed; feat/parity-v2 was merged in at efd4143 (no Rust changes). On the Linux box, `xm-remote-check.sh all -count=1` passed (remote_exit=0): the Rust release build, all 14 cargo test binaries (`index_query` with the resident test in 408 s, `index_build` 29/29), clippy at 6 warnings (the base's count; `cargo clippy --all-targets` shows none in the test crates), `go vet` and all 16 Go packages. `rustfmt --check` is clean on the changed files. Pi is unchanged and was not run.

### WS-04 — MCP server package and protocol modernization

**Goal.** Move the tool table and dispatch out of cmd/xmustard-mcp/main.go into api-go/internal/mcpserver, one file per tool, so the stdio shim, the HTTP endpoint (WS-13) and hooks share them. Add: protocol version negotiation, initialize.instructions (static workflow text for now), tool annotations, outputSchema plus structuredContent, rejection of out-of-range values, hidden compatibility aliases, optional workspace_id resolved from env/roots/cwd/path, and the API bounds that are already supported but hidden (recall limit, search limit, impact depth, verify note).

**Requirements.** PAR-ADP-01, PAR-ADP-02, PAR-ADP-04, PAR-CTX-05, PAR-HAR-03

**Files.** `api-go/internal/mcpserver/server.go`, `api-go/internal/mcpserver/dispatch.go`, `api-go/internal/mcpserver/schema.go`, `api-go/internal/mcpserver/instructions.go`, `api-go/internal/mcpserver/resolve_workspace.go`, `api-go/internal/mcpserver/tool_ground.go`, `api-go/internal/mcpserver/tool_recall.go`, `api-go/internal/mcpserver/tool_remember.go`, `api-go/internal/mcpserver/tool_verify.go`, `api-go/internal/mcpserver/tool_search.go`, `api-go/internal/mcpserver/tool_explain.go`, `api-go/internal/mcpserver/tool_impact.go`, `api-go/internal/mcpserver/tool_diagnostics.go`, `api-go/internal/mcpserver/tool_why_failed.go`, `api-go/internal/mcpserver/server_test.go`, `api-go/cmd/xmustard-mcp/main.go`, `api-go/cmd/xmustard-mcp/evidence.go`, `integrations/pi/src/tools.ts`, `integrations/pi/test/unit.test.ts`  
**New modules.** `api-go/internal/mcpserver`

**Tests required.**
- tools/list snapshot test covering annotations, outputSchema and closed schemas
- Protocol negotiation works for 2024-11-05 clients and for the current version
- Each tool resolves workspace_id from XMUSTARD_WORKSPACE_ID, roots/list or cwd, and fails with a clear error otherwise
- Out-of-range numeric arguments are rejected, not clamped
- A hidden alias is accepted but not advertised; the Claude Code 'query' drop case is covered
- structuredContent and text content are both present and consistent
- A generated tool-table JSON is consumed by a Pi unit test to prevent description drift
- Existing xmustard-mcp tests (framing, cancellation, ingress, admission, evidence) pass

**Acceptance.** The shim becomes a thin wrapper around mcpserver. All nine tools advertise annotations and honest descriptions, and accept optional workspace_id. Bounds are exposed. The static instructions are served. No behavior regressions.

**Collision risk.** api-go/cmd/xmustard-mcp/main.go and integrations/pi/src/tools.ts are also changed by w0-cleanup (WS-00). Later tool-surface workstreams edit the per-tool files this creates. No diagnostics-session files.

### WS-05 — toolcompat and redact libraries

**Goal.** Add two pure libraries. toolcompat ports the owner's cursor-bridge argument alias table and canonical keys, per-tool repair, required-field validation, validation signatures (sha256) and recorded normalizations. redact provides an RE2 secret pattern set with an entropy check, env-value-to-name helper, header allow-list and a streaming redactor over io.Reader. Both are consumed later by MCP, hooks, capture, remember and imports.

**Requirements.** PAR-ADP-03, PAR-SEC-04

**Files.** `api-go/internal/toolcompat/compat.go`, `api-go/internal/toolcompat/aliases.go`, `api-go/internal/toolcompat/compat_test.go`, `api-go/internal/redact/redact.go`, `api-go/internal/redact/patterns.go`, `api-go/internal/redact/entropy.go`, `api-go/internal/redact/stream.go`, `api-go/internal/redact/redact_test.go`  
**New modules.** `api-go/internal/toolcompat`, `api-go/internal/redact`

**Tests required.**
- Table tests ported from cursor-bridge bridge_orchestrator_test.go:152-399, with provenance recorded in the commit message
- Mutating arguments of remember/verify are never altered silently; normalization is only reported
- Secret corpus: bearer tokens, AWS/GitHub/Slack/OpenAI/Anthropic keys, PEM blocks, JSON secret fields
- False-positive corpus: sha256 hashes, UUIDs, base64 images
- The streaming redactor handles secrets split across chunk boundaries
- Benchmark: redacting a 16 MiB stream uses O(1) memory

**Acceptance.** Both packages are fully tested, with no dependents yet. Documented APIs are ready for WS-13, WS-19, WS-23 and WS-34.

**Collision risk.** None; new packages only.

### WS-FIX-03 — Production streaming secret redactor for evidence capture

**Symptom.** Verified in v0.1.0: production builds left `captureRedactor` nil (`api-go/cmd/xmustard-api/evidence_capture_routes.go:53-55, 88-93`), so `POST .../evidence/capture` answered `503 redaction_unavailable` to every request. Pi's built-in tool reduction, masking and compaction fell back to Pi's own, and no hook body could be captured. Only `capture_redactor_e2e.go` (tag `xmustard_e2e`) installed a redactor, and it replaced one test marker.

**Goal.** A production `evidence.StreamRedactor` on the WS-05 `redact` rules, including the WS-72 secret-path denylist, with the tested patterns reused, not forked. It must stream with a bounded hold-back equal to the longest detector span, be correct across any write boundaries, never buffer the stream, and fit `captureWindowBytes` (2 MiB). It is wired into `captureRedactor` in every build, not behind a build tag. Capture refuses when the redactor fails, and the e2e marker stays for the Pi e2e only.

**Requirements.** PAR-CTX-01 ("Redaction applies"), PAR-SEC-04 (capture). This unblocks the capture-backed halves of PAR-CTX-07 and PAR-HAR-04 (Pi) that WS-24 recorded as blocked on this wiring.

**Implementation record (branch parity/ws-fix-03, 2026-09-28).** These notes record what was built and measured, and where it differs from the text above.
- *redact.Writer* (`api-go/internal/redact/stream.go`) is the push-mode side of `Reader`, and both now share one input buffer (`streamBuf`: fill, `step`, compact). Writes fill the Reader's fixed windows (128 KiB plus `contextLen`). A full buffer runs one engine window, which emits everything decided and holds back the lookahead (`pemSpan+512`, about 33 KiB: the longest span a detector reads past its anchor, a private key body and its END marker). `Flush` runs the final window, writes the rest, and ends the input: the next write starts a new input with the synthetic left boundary, and `engine.restart` keeps the report and scratch slices. The output between flushes is byte for byte `oneShot`/`Bytes` over that input. A destination error sticks. The patterns are unchanged, and so was the engine in this first build; review round 1 (below) changed how a stream is windowed.
- *Wiring* (`api-go/cmd/xmustard-api/capture_redactor.go`, no build tag). `captureRedactor` is initialized to `workspaceops.OutputRedactor().NewWriter`. That is the rule set `why_failed` already applies to command output (default rules plus the daemon's secret-named environment values), exported from `outcomes.go` so the two share it rather than build two copies. The route wraps every redactor in `failClosed`. A redactor panic is logged with its stack and becomes `errRedactionFailed`, which is sticky; Observe returns it after discarding the spool, and the route answers `503 redaction_failed` instead of dropping the connection. A nil `captureRedactor` still answers `503 redaction_unavailable`. The hook decoder already flushes the redactor at every section boundary, so each output string is one input; the overflow section and a raw body are one input each.
- *Secret-path denylist (WS-72).* `evidence.Observe` refuses a capture whose selected path is on `redact.MatchSecretPath`'s list (`ErrSecretPath`, mapped to `422 secret_path`). The selected path is the caller's `path`, else the body's `file_path`, `filePath`, `path`, `target_file`, `notebook_path`, `directory` or `dir_path`. The check runs after decoding, because a hook body may name its path after its output. The spool is discarded, so nothing is retained, and the client keeps its own result. Review round 1 (below) checks every path the capture names, not only the selected one.
- *e2e marker.* `capture_redactor_e2e.go` now chains its `XM_E2E_SECRET_*` marker in front of the production redactor instead of replacing it, so the e2e runs the production rules too. The Pi e2e also echoes a GitHub-shaped token (assembled at run time) and asserts `[REDACTED:github_token]` in the retained original and no token in the projection.
- *Code style.* `writeCaptureError`'s five-case switch became a sentinel table (`captureErrors`) with the two new rows; the `MaxBytesError` check (an `errors.As`) stays an explicit guard before it. Every row replies with the error's own text, so `incomplete_body` now says "unexpected EOF" rather than "the request body ended early"; its status and reason are unchanged.
- *Tests.*
  - `redact/writer_test.go`. `TestWriterSplitsEveryGoldenSecretAtEveryBoundary` places every `secretCorpus` sample (56 samples, including the PEM shapes) so that the window end, and separately the decision limit, falls before each of its bytes. It writes the input in random-size and one-byte writes, and checks the output and the redaction count against the one-window reference. Each sample alone is also split at every byte. `TestWriterMatchesOneShotRandomized` checks the dense and sticky corpora in random writes of up to 40 KB. `TestWriterFlushEndsAnInput` and `TestWriterDestinationErrorSticks` cover flushing and errors. `TestWriterMemoryIsBounded` covers the allocation bound: 32 MiB in random writes grows the live heap by at most 386 to 461 KiB, and 16 MiB of each dense trigger or secret unit allocates at most 2.7 MiB in total and retains at most 1.1 MiB. Those units were not the worst case; review round 1 (below) adds it. `BenchmarkWriter16MiB` gives the throughput.
  - `cmd/xmustard-api/capture_redactor_test.go`. The default build (no test redactor) redacts a Claude hook body, a Pi `tool_result` and a raw body with six rule families past the first 128 KiB, in the reply, the paged original and search. A panicking redactor gets `503 redaction_failed` with nothing retained. Secret paths (`.env`, `.env.production` with the path after the output, `.ssh/config`, and a raw `path=.npmrc`) get `422 secret_path` with nothing retained, while `.env.example` is captured. `TestCaptureRedactorFitsTheCaptureWindow` writes 16 MiB of pure secrets in 32 KiB writes through `captureRedactor` and measures a live-heap peak of 1,079 KiB, under the 2,048 KiB window (first build, `password=` units; see review round 1). `TestCaptureRouteRedactsSecrets` and the 16 MiB chunked-body test now run the production redactor; the test-only `secretRedactor` is gone.
- *Measured on the build box* (first build; AMD Ryzen 5 3500X, load 3 to 4).
  - `BenchmarkWriter16MiB` (32 KiB writes, a secret pair every 4 KiB) runs at 139.8 MB/s, with 0.95 MB and 4,662 allocations per 16 MiB. `BenchmarkReader16MiB` runs at 128.1 MB/s.
  - End to end against a real `xmustard-api`, open mode, with retention on. Each figure is the range of three runs, compared with the same build using a pass-through redactor (a temporary build-tagged file, not committed):
    - 6 sequential raw 16 MiB captures: 43.2 to 45.9 MB/s redacted, 51.3 to 54.6 MB/s pass-through.
    - 200 Claude hook bodies of 256 KiB, 4 at a time: 79.5 to 81.5 MB/s redacted, 101.5 to 107.0 MB/s pass-through.
    - API peak RSS (`VmHWM`): 37.4 to 40.0 MiB redacted, 35.6 to 36.5 MiB pass-through. Idle RSS was 19.3 to 19.6 MiB for both.
  - The Pi e2e sampled the xMustard-owned tree at a peak of 49.7 MiB. The base measured 51.2 MiB in the same run conditions.
- *Pi e2e* (`scripts/e2e/pi-adapter.sh` on the box, `XM_E2E_ALLOW_NO_POSTGRES=1`, since the box has no PostgreSQL binaries). Typecheck and the 44 unit tests pass. The WS-24 suite passes 3 of 3: built-ins through capture with both markers redacted, masking, and compaction. The masking test's stub regex was stale: it did not allow the `; expires <ts>` that the adapter writes (`masking.ts` `STUB_RE`), and was fixed here. **Deviation: the e2e as a whole does not pass.** Nine tests of the first suite fail, and they fail identically on unmodified `feat/parity-v2` (845868d), run the same way on the box: tests 3 to 8, 11, 13 and 15. All nine need a multi-page result from `impact` with no arguments. Since WS-22's automatic registration baseline, a fresh fixture has no changes against its baseline, so that call returns about 400 bytes and issues no handle ("no evidence footer", "impact issued a handle"). They are independent of capture, and making them pass needs a new large-result fixture, left to a follow-up owner.
- *Collision note.* `parity/ws-23` (not merged) carries `afb1673`, an equivalent `redact.Writer` (same API: `NewWriter`, `Write`, `Flush`, `Report`) and a `//go:build !xmustard_e2e` `capture_redactor.go`. Merging both conflicts in `stream.go`, `capture_redactor.go`, `capture_redactor_e2e.go`, `writer_test.go` and `evidence_capture_routes.go`. Keep this branch's versions: the redactor is unconditional, the e2e chains onto it, and the tests are exhaustive. Point any WS-23 hook route that calls `captureRedactor` directly at `failClosed` as well.
- *Merge resolution (2026-09-28, WS-FIX-03 into feat/parity-v2 after WS-23).* One implementation remains: this branch's `redact.Writer` (8 KiB decision span) and its unconditional `capture_redactor.go`; WS-23's `!xmustard_e2e` redactor and its own `Writer` were dropped, and the e2e marker chains in front of the production redactor in every build. WS-23's `TestWriterMatchesOneShotPerSegment` and `TestWriterHoldsBackUndecidedBytes` moved into `writer_test.go`; its `TestWriterKeepsTheFirstError` was dropped as covered by `TestWriterDestinationErrorSticks`. `hookServer.capture` now wraps the redactor in `failClosed` too. A hook capture refused with `evidence.ErrSecretPath` or `errRedactionFailed` returns nil (the client keeps its native output), retains nothing and is not counted by `NoteBusy`, which only `budget.ErrOverloaded` feeds (`TestHookRefusedCaptureFailsOpenWithoutBusyNote`).
- *Checks* (`xm-remote-check.sh ... all -count=1` on the build box). go vet and all 14 Go packages pass, and clippy reports 6 warnings, the same as the base. Every Rust test binary passes except one test, `index_query::resident_rss_on_a_100k_symbol_resolved_graph_stays_within_the_line` ("Sequential: after swap +15.0 MiB over 15 MiB"), so `remote_exit=1`. This branch changes no Rust code. The same test fails in the same way in `xm-remote-check.sh <base 845868d> rust` on the box at the same time, with about 12 GB of the host's swap in use by other sessions. Run alone, it passes in release mode on this branch and in debug mode on the base. Two earlier full runs failed Go packages with SQLite "database or disk is full": other sessions' jobs had filled the shared tmpfs `/tmp` for a moment, and those packages pass on rerun. The Pi unit tests (44) pass locally and on the box.
- *Docs.* `docs/SECURITY.md` has a new section, "Evidence capture redaction (WS-FIX-03)". There is a new `docs/releases/v0.1.1.md`, and v0.1.0's known limits name the bug. `docs/ARCHITECTURE.md` (evidence row), the Pi README (failure table and e2e notes) and the e2e script comment are updated.
- *Known limits.* Redaction is pattern-based, so a secret no rule knows is retained as written; the redact package documentation lists the limits. A secret file read through a shell (`cat .env`) is not matched by path, nor are the matches of a search over a directory (a Grep over `/repo`, with or without a `glob` such as `.env*`). A secret split across two flushed inputs, which are two separate output strings of a hook body, is not joined. A redactor error that is not a panic (the spool's own write error) keeps its existing mapping (500), still with nothing retained.
- *Review round 1 (2026-09-28).* One major and two minor findings, all resolved. feat/parity-v2 was merged first at db04d40 (WS-26, WS-FIX-04); the one conflict, the evidence row of `docs/ARCHITECTURE.md`, keeps WS-FIX-04's shaper wording and this branch's redaction wording.
  - *The memory bound (major).* The first build decided the whole 128 KiB buffer in one window, so a window's candidates (88 B each, in a slice whose capacity doubles) and its output (a marker is longer than a short secret) grew with the density of secrets. The 0.3 and 1.2 MiB figures came from units that were not the worst case. Measured again on the box with the worst units, 16 MiB in 32 KiB writes, the live heap peaked at +2,122 KiB for `secret=x\n`, +2,010 KiB for `"secret":"x"` and +3,416 KiB for an 8-byte `WithEnv` value (`hunter22`) repeated, above the 2 MiB `captureWindowBytes` a capture reserves. The fix is in the engine, which `Reader` and `Writer` share:
    - `streamBuf.step` shows the engine at most `decideSpan` (8 KiB) and the lookahead after it. The buffer stays 128 KiB plus `contextLen`; the Writer compacts it only when it is full, and the Reader only when no whole window is left. `engine.span` caps a final window too: with eof set, `limit = min(n, from+span)`, and `evalFrom` advances as it does for a non-final window.
    - Inside a window the detectors run over slices of `span` bytes (`engine.detect`). Each slice is bounded exactly as a window is: skip and memo are cleared, trigger literals are read `anchorReach` past the slice, and anchors at or past its end are dropped and found again by the next slice. Each slice's candidates are settled at once (`engine.settle`: first the covering-value resume, then the drop of what lies inside the region already redacted). Without slicing, a region carried across a limit (a long quoted password holding an env secret every 8 bytes) was evaluated again over up to the whole lookahead in one pass, and peaked at 873 KiB.
    - The commit order sort is `slices.SortStableFunc(commitOrder)`. The reflect-based `sort.SliceStable` cost three allocations per window, which mattered at 8 KiB windows (7,205 against 4,662 allocations per 16 MiB).
    - The one-shot path (`String`, `Bytes`, `Check` and `Findings` up to 128 KiB, and `oneShot` in the tests) is unchanged, since `span` is zero there.
    - The output is unchanged. The whole redact suite passes. Its window-boundary tests moved to the new boundaries (`windowLimit` and `windowEnd` in `redact_test.go`, a window limit at 128 KiB so the string APIs stream too). The escaped-quote test targets the window end of a carried region, (⌊128 KiB/(8 KiB+lookahead)⌋+1)×(8 KiB+lookahead), which a probe confirmed (the closing backslash is the byte the continuation carries). The Writer's golden test adds the buffer end as a third boundary.
    - Measured after the fix (16 MiB in 32 KiB writes, live-heap peak): 178 to 293 KiB for the trigger literals and `password=`/`Authorization` units, 284 to 286 KiB for `secret=x\n`, `"secret":"x"` and `hunter22\n`, 374 KiB for `hunter22`, 373 KiB for `API_TOKEN=` followed by 8,192 `hunter22`, and 345 KiB for the same inside a quoted password. Allocations were 178 to 933 KiB. Through `captureRedactor` the peak was 284 to 391 KiB. `TestWriterMemoryIsBounded` now asserts under 512 KiB live and 2 MiB allocated for every unit. `TestCaptureRedactorFitsTheCaptureWindow` asserts under a quarter of `captureWindowBytes` for `password=`, `secret=x\n`, `"secret":"x"` and an 8-byte env value with and without a newline; the env redactor is built as `OutputRedactor` builds it, from an environment that holds `DB_PASSWORD=hunter22`.
    - Why 8 KiB: the CPU time of 20 runs of 16 MiB was within 2% for spans of 8, 16 and 32 KiB and for the first build (sparse 2.42 to 2.48 s, dense 9.44 to 9.78 s; the host was loaded). 8 KiB holds about half the scratch of 16 KiB (372 against 579 KiB for `hunter22`). With the final code, the CPU time is 1 to 4% below the first build's.
    - The bound: per window, the scratch is one slice's candidates plus the survivors, which lie in the decided span, whatever the input length and whatever a window evaluates again. What remains is the rule set's density, where the densest single rule is an 8-byte env literal (one candidate per 8 bytes); several env values that occur densely at once add up. `captureWindowBytes` stays 2 MiB, and the redactor now uses under a quarter of it.
    - `capture_redactor.go`, `docs/SECURITY.md`, `docs/releases/v0.1.1.md` and the redact package documentation state the measured bound (0.4 MiB at most).
  - *Secret paths (minor).* `evidence.secretPath` checks every path the capture names, before the selector is built: the caller's `path`, each `pathKeys` field of the tool input (the list `selectorFor` now shares), and each such field of the response skeleton at any depth (`responsePaths`; Claude Read's `file.filePath`). The reviewer's case (`path=README.md` with a Read body that names `/home/dev/app/.env`) now answers 422 and retains nothing. The route test adds it, a body whose tool input names README.md while its response names `.env`, and a caller `path=deploy/.npmrc` over a README body, and still captures README and `.env.example`. Glob and include arguments are not checked: a search over a directory can match a secret file whatever its glob, so the SECURITY.md limits and this record's known limits name the case instead (a Grep over `/repo`, with or without `glob: ".env*"`, passes through the content rules only). SECURITY.md now says "any path the capture names".
  - *Pi e2e (minor).* The failures got an owner: WS-24's record has an open follow-up for the large-result fixture. `docs/releases/v0.1.1.md` records the e2e as partially passing. This round's run on the box (`XM_E2E_ALLOW_NO_POSTGRES=1`, after the merge, with the release core rebuilt): the 45 unit tests pass, the WS-24 suite passes 3 of 3, and the first suite fails the same nine tests as before (3 to 8, 11, 13 and 15), 9 of 18 e2e tests in all. The xMustard-owned tree peaked at 50.0 MiB (54.7 MiB in an earlier run of the round).
  - *Measured end to end* (the table in `docs/releases/v0.1.1.md`), three interleaved runs of three builds at a host load of about 6 to 10: this branch, the first build, and this branch with a pass-through redactor (a temporary build-tagged file, not committed). Six sequential raw 16 MiB captures: 31.4 to 33.0 MB/s, 32.3 to 33.5 and 33.3 to 39.3. Four raw 4 MiB captures of nothing but `secret=x` lines: 6.5, 6.1 to 6.5 and 6.5 to 6.7 MB/s (the shell reducer bounds it). 200 Claude bodies of 256 KiB, 4 at a time: 40.1 to 44.1, 39.8 to 40.1 and 46.8 to 48.9 MB/s. API peak RSS (`VmHWM`): 36.4 to 37.0, 37.7 to 39.4 and 44.2 to 44.8 MiB; idle 18.7 to 19.0 MiB for all three. A raw 16 MiB of `secret=x` lines answers 413 in both redacted builds: the markers grow it to 55 MiB, past the 16 MiB original limit, which applies to the redacted bytes. The v0.1.1 compatibility notes say so.
  - *Checks* (on the build box). Two full runs of `xm-remote-check.sh ... all -count=1`: after merging feat/parity-v2 at db04d40, and at the final tree after merging it again at efd4143 (WS-65, no conflicts). Both pass go vet and every Go package (16 in the final run, with WS-65's anchor and review), and clippy reports 6 warnings, the base count. Both are `remote_exit=1` on one Rust RSS-line test, a different one each time, and this branch's Rust tree is identical to feat/parity-v2's (`git diff feat/parity-v2 HEAD -- rust-core` is empty). The first run failed `index_build::build_of_5000_files_has_no_file_cap_and_peaks_under_25_mib`: the xmustard-core child peaked at 25.3 to 25.5 MiB in three runs alone, over its 25.0 MiB line (WS-26 recorded 24.4 to 24.9 MiB on the base; the margin belongs to the index-build owner, WS-07/WS-22). It passed in the final run. The final run failed `index_query::resident_rss_on_a_100k_symbol_resolved_graph_stays_within_the_line`, which passed alone right after (377.9 s, host load average 17, 11 GB of swap in use by other sessions), as the first build's record describes. `cargo test --no-fail-fast` and single-binary runs cover every other test binary, and all pass. `gofmt -l` is clean on the changed Go files. The Pi unit tests pass on the box (45).
  - *Deviations.* (1) The streaming window is now 8 KiB of decisions over the same 128 KiB buffer, rather than the first build's single window per buffer; the spec's "bounded hold-back equal to the longest pattern span" is unchanged (the lookahead). (2) Glob and include arguments are not matched against the secret-path list; the case is documented as a limit. (3) The Pi e2e still passes only in part, as above, with an owner. (4) Neither full check is `remote_exit=0`: each failed one of the base's Rust RSS-line tests under host load, as above.

### WS-06 — Budget governor, runtime hygiene and health budget/counters

**Goal.** Extend the budget package with: static component reservations, a single heavy slot with a wait bound and owner label, and an RSS watchdog over the own process tree (phys_footprint on darwin, smaps_rollup on linux) that refuses new heavy work near a soft ceiling. Lower the default transient pool to 24 MiB. Set debug.SetMemoryLimit when GOMEMLIMIT is unset. Call FreeOSMemory after heavy release. Add data-movement counters: child and git spawns, bytes hashed, captures. Expose it all as a /api/health budget block.

**Requirements.** PAR-RT-04, PAR-RT-05, PAR-OPS-01, PAR-EVAL-04

**Files.** `api-go/internal/budget/governor.go`, `api-go/internal/budget/rss_darwin.go`, `api-go/internal/budget/rss_linux.go`, `api-go/internal/budget/rss_other.go`, `api-go/internal/budget/counters.go`, `api-go/internal/budget/governor_test.go`, `api-go/internal/budget/budget.go`, `api-go/internal/budget/budget_test.go`, `api-go/cmd/xmustard-api/health_budget.go`, `api-go/cmd/xmustard-api/main.go`  
**New modules.** `api-go/internal/budget/governor.go`, `api-go/cmd/xmustard-api/health_budget.go`

**Tests required.**
- Heavy slot exclusivity: a second holder waits, then gets ErrOverloaded after the bound
- Watchdog refuses heavy work when the simulated tree RSS exceeds the soft ceiling
- The default pool change keeps existing admission tests correct (expectations updated deliberately)
- Health JSON shape includes reservations, the heavy-slot owner and queue, and counters
- Counters increment on rustcore spawns and captures
- The memory limit is applied only when GOMEMLIMIT is unset

**Acceptance.** /api/health reports the budget block. The heavy slot is available for WS-07/15/37/51 callers. Existing budget, admission and body-limit tests are green. The main.go diff is 1-3 lines.

**Collision risk.** Small main.go registration (also touched by WS-09 and WS-00). budget.go was modified in the diagnostics session's older tree; confirm it is already at HEAD before editing.

**Correction.** Critic: captures never use the heavy slot (WS-08 streams O(window)); acceptance adds "no hook or capture path waits on the heavy slot"; GOMEMLIMIT = daemon line (~26-28 MiB), not 40.

### WS-06B — Governor wiring for the resident worker and the heavy slot (orchestrator addition)

**Goal.** Connect WS-06's governor to WS-02's resident worker. Register the worker as a governed component that reports its memory in the `/api/health` budget block, recycle it on memory pressure or when idle under the governor's policy, and run the heavy Rust operations through `AcquireHeavy` with a bounded wait and explicit overload errors. Captures stay out of the heavy slot (critic). Show the full `/api/health` view, which includes host-wide counters and heavy-slot owner labels, only to an admin or unscoped operator token.

**Requirements.** PAR-RT-01 (Go RSS watchdog over the resident service, idle trim), PAR-RT-04, PAR-OPS-01, PAR-SEC-03 (exposure of host-wide activity)

**Implementation record (branch parity/ws-06b, 2026-09-25).**
- *Governor (`budget/pressure.go`, `governor.go`).* A component can declare `Enabled` (opt-in: listed, reserves nothing while off), `Descendant` (lives in an owned child, so `helper_children` leaves it out) and `Reclaim` (a non-blocking hook that returns a channel closed when the memory is back with the OS). When heavy work is refused for memory, the governor asks for memory, waits up to 3 s (`ReclaimWait`), measures again and admits the work if it now fits. A sample over the soft ceiling (from heavy work, health polls or `CheckPressure`) asks for memory at most once per 5 s (`PressureInterval`). Hook and capture paths ask for memory but never wait. A reclaimable component's steady line does not lower the permanent heavy bound. `Level()` reports normal, tight (less room than the heavy line), over or unknown. Health gains a `reclaim` block and per-component `enabled`/`reclaimable`.
- *Worker (`rustcore/worker_governor.go`).* `rust_worker` is resident, enabled with `XMUSTARD_CORE_WORKER`, with a steady line of 24 MiB and a peak line of 32 MiB. Its used value is the running worker's ps RSS. Recycling retires the worker: its calls finish, it exits, and the next call starts a fresh worker. A worker asked for memory while idle is retired at once, and the heavy admission waits for its exit. A busy worker is marked and retired when its last call ends, so no second worker starts beside it. The worker is also recycled when it goes idle above 64 MiB (runaway), and when it has been idle for its trim period (30 s) while the level is tight or over, or while it is still above 32 MiB. Going idle also triggers a `CheckPressure` sample, off the caller's path. The peak line is not checked on every idle transition, because an agent's calls leave the worker idle between them and its working set passes 32 MiB during ordinary queries. `/api/health` reports `core_worker` (counters, recycles by reason, RSS and footprint, lines, timers, policy).
- *Heavy slot (`rustcore/heavy.go`).* `symbolgraph build` and `changetrack index` take the slot in `runCoreCtx`, declaring 44 MiB each. POST /index, GET /symbol-graph, GET /issue-symbol-edges and POST /pg/materialize reach them. A busy slot means a bounded wait, then `ErrOverloaded` (503 + Retry-After). Hook and capture paths are refused at once, a declaration that can never be admitted is the permanent `ErrTooLarge`, and refused builds never start the core. Queries (impact, trace, clusters, hotspots, blast-radius) and captures never take the slot.
- *WS-07 index and the governance store (after merging feat/parity-v2).* `index build` and `index update` (an update turns into a full rebuild when enough files changed) take the heavy slot with the design's 25 MiB line; `index stats` does not. `scripts/bench/index-rss.sh` on macOS, 2 runs each: a cold build peaked at 20.8–21.0 MiB ps RSS on pi-mono (1,636 files) and 23.0–23.1 MiB on cline (2,660 files), a one-file update at 18.3–18.8 MiB. No Go code calls `index` yet (WS-14 does), so the table entry is what that caller inherits. `govstore` is a resident component (`govstore/governed.go`): enabled while this process holds a store open, steady 3 MiB and peak 5 MiB (the RSS probe's open line and §7.2's steady range). Both legacy imports (`context_entries.json`, `agent_feedback.json`) run in the heavy slot as `govstore:import/<file>`, declaring the probe's 14 MiB bulk line; a refused import writes nothing. Nothing in production opens a store yet (WS-12 cuts governance over).
- *Health view (`health_budget.go`).* While auth is enforced, the full view (the budget block plus the live pool and child counters) needs an admin token, or another non-reader token with no workspace scope. Reader-only tokens and workspace-scoped tokens of any role, admin included, get the public view (status, pool size, child cap, gate, soft ceiling, and a detail explaining why), and their polls never sample the tree. `docs/SECURITY.md` has the regenerated route row and a Health endpoint section.
- *Measurements.* These use one worker, ps RSS sampled every 50 ms over the API, its descendants and two stdio shims. The workload runs `XMUSTARD_CORE_WORKER=1` with a mixed MCP sequence (ground, search, explain, impact) and then POST /index with the worker warm. The arms are merge-base cfc2f5d and this branch, both built with -trimpath, same release core, 3 reps per fixture. The Linux build box (x86_64, 6 cores, load 9–13 from other agents) gave:
  - POST /index: the base arm admitted every run with the warm worker beside the index child. Its tree peaked at 90.9–94.8 MiB on pi-mono and 92.4–106.2 MiB on cline, 2 of 6 runs over the 95.4 MiB gate. This branch recycled the worker first in 6 of 6 runs, and every run was admitted after reclaim, at 60.3–74.3 MiB on pi-mono and 67.9–70.5 MiB on cline. The highest point of each run was 90.9–106.2 MiB before and 71.5–75.6 MiB after.
  - Query latency for the 10-call sequence was unchanged (5.1–5.9 s before, 5.2–5.7 s after). The worker's working set during queries was 32.5–36.4 MiB.
  - API alone, idle with the worker off (7 interleaved reps, medians): 15,008 KiB before and 14,356 KiB after, 17,016 and 16,620 KiB after 50 health polls. That is no measurable cost. A path-dependent build had shown a +2.3 MiB gap, entirely file-backed binary pages (anonymous memory 3.5 MiB in both), and it disappeared with -trimpath.
  - Heavy builds on Linux, max RSS: `symbolgraph build` 32.0 MiB (pi-mono) and 24.2–24.4 MiB (cline); `changetrack index` 19.6–19.8 and 28.7 MiB. On macOS: 36.7–42.4 and 38.3–40.0 MiB, and 22.7–25.6 and 38.5–41.8 MiB. The 44 MiB declaration is the macOS peak rounded up.
  - macOS (8 GB, shared, load 12–200) is less reliable. The stdio shims measured 21–28 MiB there against 18 MiB on Linux, and the API 24–28 against 19–24 MiB, so after the reclaim the tree plus 44 MiB still passed the 90 MiB soft ceiling in most runs, and POST /index answered 503. Before this branch, the same runs admitted the index and reached 102.5–146.8 MiB.
- *Open.* The 44 MiB declaration is above the design's 25 MiB heavy line until WS-07's streaming worker lands. With two fat stdio shims, POST /index can be refused indefinitely, which is correct under the soft ceiling but needs WS-13 (MCP over HTTP) or WS-07 to go away. Admission counts every xmustard-mcp shim of this user on the host (WS-06's over-count), so other sessions can refuse an index. On the 5,652-file gitnexus source tree (local, not shipped) both builds reached 124.7 MiB ps RSS with a 37–45 MiB footprint, so the declaration can undercount a large repository's ps-RSS peak; the watchdog still samples the real tree. The worker's lines are over the design line (3 MiB service plus a 12 MiB CSR graph) until WS-14. A tree that stays over the soft ceiling restarts the worker at most every 5 s.

### WS-07 — Code index DB and streaming per-file fact extraction (existing languages)

**Goal.** Create the Rust-owned per-workspace index store (rusqlite bundled, cached locally as 0.32.1) under .git/xmustard-cache/index-v3. Schema: files, symbols (nested qualified path, byte and line ranges, UID with collision suffixes), references (strings and comments excluded), imports, chunks (contentless FTS5 postings), and meta (analyzer version, schema fingerprint, incremental_in_progress flag, last_commit, coverage). Add `xmustard-core index build|update|stats`. It streams one file at a time with one thread, drops each tree after extraction, uses a content-addressed per-file fact cache, and never stores source text when content_retention=none.

**Requirements.** PAR-STORE-03, PAR-SYM-01, PAR-RT-02, PAR-FRESH-04, PAR-FRESH-08, PAR-RT-09

**Files.** `rust-core/Cargo.toml`, `rust-core/src/lib.rs`, `rust-core/src/bin/xmustard-core.rs`, `rust-core/src/treesitter.rs`, `rust-core/src/index/mod.rs`, `rust-core/src/index/schema.rs`, `rust-core/src/index/facts.rs`, `rust-core/src/index/extract.rs`, `rust-core/src/index/uid.rs`, `rust-core/src/index/chunks.rs`, `rust-core/src/index/writer.rs`, `rust-core/src/index/meta.rs`, `rust-core/tests/index_build.rs`  
**New modules.** `rust-core/src/index`

**Tests required.**
- Extraction fixtures for Go, Rust, TS/TSX and JS/JSX: nested classes, impl blocks, methods, closures, re-exports
- UID stability across unrelated edits, and collision suffixes on overloads
- Strings and comments produce no references (regression for the known lexical false edges)
- Incremental update touches only changed files, with reparsed counters
- Delete, rename and create sequences leave a correct index
- A leftover dirty flag or a schema fingerprint change forces a full rebuild; the escalation gate triggers above 50% and 50 files
- content_retention=none stores no source text
- RSS: build on a 5k-file fixture peaks ≤25 MiB with no 800-file cap

**Acceptance.** `xmustard-core index build` indexes every eligible file of the gitnexus clone at ≤25 MiB peak and reports coverage. Output is deterministic. No consumer is switched yet (WS-14 does that).

**Collision risk.** Cargo.toml, lib.rs and bin/xmustard-core.rs (WS-00 cleanup/drift, WS-02); rebase onto WS-02's dispatch table. treesitter.rs was modified in the diagnostics session's older tree (likely at HEAD).

**Correction.** Critic: first deliverable is an FTS5-vs-tantivy RSS and ranking measurement that closes D-02; schema includes an edges table populated by a port of the legacy lexical edge builder (provenance=lexical) so WS-14 can reach legacy parity before WS-17.

### WS-08 — Evidence tool-family reducers, client shape adapters, capture metadata and search-in-original

**Goal.** Generalize the evidence reducer beyond the nine tools. Add a versioned reducer registry keyed by (client tool name, argv0) for shell, test runners, build, git, grep, read, ls/glob, logs and lints, with failure-first head+tail excerpts and per-kind projections. Add per-client output-shape validators (Claude Bash/Read/Grep/Glob, Pi, OpenCode, Codex). Add capture metadata and stream-decode of hook JSON bodies into the spool, streaming pattern/line search inside originals, and a heuristic token estimate.

**Requirements.** PAR-CTX-01, PAR-CTX-02, PAR-CTX-03, PAR-CTX-04, PAR-CTX-13

**Files.** `api-go/internal/evidence/registry.go`, `api-go/internal/evidence/reduce_shell.go`, `api-go/internal/evidence/reduce_testrun.go`, `api-go/internal/evidence/reduce_grep.go`, `api-go/internal/evidence/reduce_read.go`, `api-go/internal/evidence/reduce_list.go`, `api-go/internal/evidence/reduce_diff.go`, `api-go/internal/evidence/shapes.go`, `api-go/internal/evidence/capture_meta.go`, `api-go/internal/evidence/hookbody.go`, `api-go/internal/evidence/search_original.go`, `api-go/internal/evidence/tokens.go`, `api-go/internal/evidence/reduce.go`, `api-go/internal/evidence/reducers_test.go`, `api-go/internal/evidence/shapes_test.go`, `api-go/internal/evidence/search_original_test.go`  
**New modules.** `api-go/internal/evidence/registry.go`, `api-go/internal/evidence/shapes.go`

**Tests required.**
- Golden fixture: one failing assertion among 5,000 passing lines is kept with its first and last stack frames and the exit code
- Grep projection caps per file and reports totals; read projection returns a line-numbered range
- Structured payloads are byte-bounded and status fields are never dropped (cursor-bridge gap)
- A 16 MiB Claude Code hook JSON body is stream-decoded into the spool, with an allocation test below a fixed bound
- Shape validators accept valid per-client payloads and reject mismatches, which triggers fallback
- Search-in-original streams 1 MiB chunks with a match cap
- Existing invariants_test.go and store_test.go pass; the nine-tool projections are unchanged unless a tool reducer is selected

**Acceptance.** The registry is selectable per tool family. Every projection records its reducer id and version, and bounded memory (O(window)) is shown by test. Ready for hooks (WS-23), Pi (WS-24) and the wrapper (WS-41).

**Collision risk.** reduce.go gets a registry hook only. evidence/store.go is owned by WS-03 in this wave, so do not edit it here. No diagnostics files.

### WS-09 — Security kernel: roles, gates, exposure posture and core-only default

**Goal.** Add principal roles (reader, proposer, verifier, human-approver, admin) bound to tokens. verify requires verifier. POST /index gets a role gate and joins the core allowlist. Add a security middleware: Host/Origin allowlist on loopback, read-only mode, workspace allowlist, rejection of query-string tokens, constant-time compare everywhere. Cache the token store. Enforce path confinement through safepath.go. Flip core-only to the default and make the platform half opt-in with XMUSTARD_PLATFORM=1.

**Requirements.** PAR-SEC-02, PAR-SEC-03, PAR-SEC-05, PAR-SEC-07, PAR-FRESH-06, PAR-RT-07

**Files.** `api-go/internal/workspaceops/auth.go`, `api-go/internal/workspaceops/auth_roles_test.go`, `api-go/internal/workspaceops/safepath.go`, `api-go/cmd/xmustard-api/security_middleware.go`, `api-go/cmd/xmustard-api/security_middleware_test.go`, `api-go/cmd/xmustard-api/main.go`, `api-go/cmd/xmustard-api/core_only_test.go`  
**New modules.** `api-go/cmd/xmustard-api/security_middleware.go`

**Tests required.**
- Role matrix: each route and tool is allowed or denied per role
- verify with a proposer-only token is denied, with an error that names the missing role
- POST /index is denied to the agent role and allowed to admin/indexer; it is present in core-only mode
- Read-only mode filters writes from tools/list and returns 403 on write routes
- A spoofed Host header on loopback is rejected; ?api_key= is rejected
- The token file cache is invalidated on mint, rotate and revoke
- Path escapes via absolute paths and symlinks are rejected
- Core-only default serves the nine tools, health and evidence; platform routes 404 unless XMUSTARD_PLATFORM=1

**Acceptance.** All existing auth, core-only and scope tests pass with the updated defaults. The route gate table is documented. The frontend or platform needs the opt-in flag, noted in the PR.

**Collision risk.** main.go (w0-kernel via WS-00, and a small WS-06 registration). auth.go. The core-only default flip may affect the diagnostics route wiring in the diagnostics session, so coordinate the allowlist entry for GET /diagnostics.

### WS-10 — Parity-scale budget gate v2 and CI

**Goal.** Add a new gate (rss_bench.py is frozen at 6411f391). It records dual metrics per process (ps-RSS as the gate, plus phys_footprint or PSS/USS), splits anon from file-backed memory, attributes components by registered role, and lists externals separately. Add pinned parity fixtures (gitnexus and cline clones at fixed shas, cloned to scratch and never vendored) and scenarios: 1/2/4 agents, watcher on, reindex during queries, captures during index. Add a GitHub Actions workflow running make check-backend plus the retrieval gate.

**Requirements.** PAR-EVAL-05, PAR-EVAL-08, PAR-EVAL-04

**Files.** `scripts/bench/rss_bench_v2.py`, `scripts/bench/rss_v2.sh`, `scripts/bench/parity_fixtures.json`, `scripts/bench/test_rss_bench_v2.py`, `.github/workflows/check.yml`, `Makefile`  
**New modules.** `scripts/bench/rss_bench_v2.py`, `.github/workflows/check.yml`

**Tests required.**
- Unit tests of the sampler and attribution on synthetic ps/footprint samples
- v2 reproduces the v1 numbers within noise on the existing 501-file workload
- Parity fixtures clone to scratch at pinned shas and are verified by hash
- CI workflow runs make check-backend and the retrieval gate on PRs

**Acceptance.** `make bench-parity` produces JSON and markdown reports with the gate verdict (≤95.4 MiB ps-RSS), footprint, anon/file split and external lines. CI is green on a test PR.

**Collision risk.** Makefile was modified by the diagnostics session's older tree (low risk; add targets only). Do not edit README.md or docs/STATUS.md, which the diagnostics session edits.

**Correction.** Critic: per-component budget ledger file, CI fails on overrun; scenarios for queries-during-reindex, snapshot swap under load, 4 agents in 4 worktrees; fixtures are Apache/MIT repos (cline, pi-mono) not GitNexus (PolyForm NC).

### WS-11 — Outcome evaluation executor (xmustard-eval)

**Goal.** Build the paired-arm outcome harness that the parity claims require. It reads a task corpus schema and runs arms (baseline, baseline_nomcp, xmustard_mcp, xmustard_mcp_hooks placeholder, xmustard_memory) in a fresh detached worktree per run. Hidden oracles are run by the harness only. Drivers are claude -p stream-json, codex exec --json and pi --mode rpc. Tokens and cost come from each client's final events. It records diff churn and RSS samples via WS-10, and reuses the memory_harness.go statistics (McNemar, bootstrap).

**Requirements.** PAR-EVAL-01, PAR-EVAL-09, PAR-EVAL-02

**Files.** `api-go/cmd/xmustard-eval/main.go`, `api-go/cmd/xmustard-eval/corpus.go`, `api-go/cmd/xmustard-eval/arms.go`, `api-go/cmd/xmustard-eval/drivers.go`, `api-go/cmd/xmustard-eval/oracle.go`, `api-go/cmd/xmustard-eval/worktree.go`, `api-go/cmd/xmustard-eval/accounting.go`, `api-go/cmd/xmustard-eval/report.go`, `api-go/cmd/xmustard-eval/main_test.go`, `eval/tasks/README.md`, `eval/tasks/seed.yaml`  
**New modules.** `api-go/cmd/xmustard-eval`, `eval/tasks`

**Tests required.**
- A fake driver emitting canned stream-json runs end to end
- The hidden oracle is isolated from the model-visible verify step
- Worktrees are created detached and cleaned up on success, failure and interrupt
- Token and cost accounting parsed correctly from each driver's final event shape
- Report is deterministic given the same inputs; statistics call into memory_harness.go

**Acceptance.** A dry run with the fake driver completes all arms on seed tasks. The corpus schema is documented. A real-model run is an operator step, since it needs credentials, and is documented. The executor runs outside the measured tree.

**Collision risk.** None. It is a new command, and memory_harness.go is reused read-only.

**Correction.** Critic: pair with WS-63 corpus authoring; numeric parity thresholds are defined in WS-50.

### WS-12 — Governance cutover to govstore and split of context_governance.go

**Goal.** Move propose/verify/recall/edit/feedback from JSON arrays to govstore. Split context_governance.go into memory_propose.go, memory_verify.go, memory_edit.go, memory_recall.go and memory_store.go. Emit append-only events for every transition. Import existing JSON on first open, with a backup. Route xmustard-ops writes through the store. Apply flock+fsync hardening to the JSON stores that remain (runs, tokens).

**Requirements.** PAR-STORE-01, PAR-STORE-02, PAR-PROV-01, PAR-GOV-05

**Files.** `api-go/internal/workspaceops/context_governance.go`, `api-go/internal/workspaceops/memory_propose.go`, `api-go/internal/workspaceops/memory_verify.go`, `api-go/internal/workspaceops/memory_edit.go`, `api-go/internal/workspaceops/memory_recall.go`, `api-go/internal/workspaceops/memory_store.go`, `api-go/internal/workspaceops/feedback.go`, `api-go/internal/workspaceops/feedback_recorder.go`, `api-go/internal/workspaceops/storelock.go`, `api-go/internal/workspaceops/verification.go`, `api-go/internal/workspaceops/context_governance_test.go`, `api-go/cmd/xmustard-api/main.go`, `api-go/cmd/xmustard-ops/main.go`  
**New modules.** `api-go/internal/workspaceops/memory_store.go`

**Tests required.**
- All existing governance, recall-contract and feedback tests pass against SQLite
- First open imports JSON idempotently and keeps a backup; rollback is documented
- Two API processes plus the ops CLI running concurrently show no lost updates
- Every transition writes an event with principal, time, HEAD and digests
- Recall p50 is not worse than the JSON baseline on a 1k-entry fixture
- Remaining JSON writers fsync and take a cross-process lock

**Acceptance.** context_entries.json is no longer written. Governance is multi-process safe. The file split lets WS-19, WS-20 and WS-27 own disjoint files. make check-backend is green.

**Collision risk.** context_governance.go, feedback.go and main.go are hot. Must follow WS-00 (w0-kernel, w0-feedback). The diagnostics session's older tree also modified context_governance.go, so coordinate before its landing.

**Delivered and accepted deviations (2026-09-26).**
- Governed memory (propose, verify, edit, recall, trust labels) runs on govstore; context_entries.json is imported once and never written again. The five-file split is in place.
- Feedback stays in agent_feedback.json rather than moving to govstore's path_feedback table. It is a ranking hint written off the request path by the coalescing recorder; moving it would put a store transaction on every flush for no trust gain. Instead every remaining JSON store (feedback, runs, tokens, the workspace registry, audit logs) now writes durably (fsync of the temp file, rename, fsync of the directory) and takes lockStore, which pairs the in-process mutex with an exclusive flock on a sibling `<store>.lock` file (unix; in-process only elsewhere). A lock file that cannot be taken fails the write closed. Moving feedback into govstore remains open for WS-49 if the kernel extraction wants one store.
- Multi-process safety is tested by re-executing the test binary: `TestMemoryStoreAcrossProcessesLosesNoUpdates` (two API-style processes plus an ops-style handle on governance.db) and `TestJSONStoresLockAcrossProcesses` (three processes bumping one feedback counter, one run record and the token file). `TestJSONStoreWritesFsync` pins the fsync.
- The one-time legacy import takes the heavy slot only for files of 1 MiB or more (`legacyImportHeavyFrom`). govstore owns that slot for its imports (WS-06B); workspaceops passes `ImportOptions.Inline` for a smaller file rather than taking the slot a second time. A smaller file imports in about one ordinary write, and queueing it behind an index build, or refusing it under memory pressure, would fail the first memory request of an upgraded workspace for no memory saving.
- Trust tightening: the author of the served revision is not a peer (see docs/SECURITY.md, "Who counts as a peer verifier"). Two open-mode quorum tests now use a third principal for that reason.

### WS-13 — MCP Streamable HTTP endpoint on the API and native stdio relay

**Goal.** Serve the nine tools, resources/read and cancellation over Streamable HTTP at :8042/mcp from the API process, using the mcpserver package. The bearer principal is taken per connection, and client profiles and read-only mode come from query params. Wire toolcompat normalization. Ship a tiny native Rust stdio-to-HTTP relay (target ≤3 MiB) for stdio-only clients. Record per-tool usage counters.

**Requirements.** PAR-RT-03, PAR-ADP-03, PAR-ADP-05, PAR-SEC-03, PAR-EVAL-04

**Files.** `api-go/cmd/xmustard-api/mcp_http.go`, `api-go/cmd/xmustard-api/mcp_http_test.go`, `api-go/cmd/xmustard-api/main.go`, `api-go/internal/mcpserver/dispatch.go`, `api-go/internal/mcpserver/usage.go`, `rust-core/src/bin/xmustard-relay.rs`, `Makefile`  
**New modules.** `api-go/cmd/xmustard-api/mcp_http.go`, `rust-core/src/bin/xmustard-relay.rs`

**Tests required.**
- MCP over HTTP: initialize, tools/list, tools/call, resources/read paging, cancellation and progress passthrough
- Principal attribution per connection; 4 concurrent clients
- Read-only profile filters tools/list and rejects writes
- Toolcompat aliases are normalized and recorded
- The relay forwards stdio to HTTP with cancellation and passes the existing shim test suite
- RSS: relay ≤3 MiB; measured savings against the Go shim recorded with gate v2

**Acceptance.** Agents configured with the URL work with the same nine tools. Stdio-only clients use the relay. The Go shim is kept but deprecated in docs. The gate shows per-agent savings.

**Collision risk.** main.go registration (1 line). Makefile shared with WS-10 and WS-26. bin/ directory in rust-core is shared by convention only.

**Correction.** Critic: relay is std-only Rust (no tokio/reqwest), parses Streamable-HTTP SSE; per-project workspace binding (/mcp?workspace= or X-Xmustard-Workspace header) and roots/list conformance test per client.

### WS-14 — Resident index service, query side: CSR graph, name index, freshness envelope, cap removal

**Goal.** Have the WS-02 worker load index.db (WS-07) and build a compact CSR (forward and reverse, u32 ids, interned strings, bounded block cache, ≤150 B/symbol target) and a name/UID index. Serve search, explain and impact from it instead of deserializing the JSON graph. Attach the freshness envelope and coverage. Replace the 800-file cap with the declared scale envelope.

**Requirements.** PAR-RT-01, PAR-IMP-02, PAR-FRESH-05, PAR-RT-09

**Files.** `rust-core/src/index/reader.rs`, `rust-core/src/index/csr.rs`, `rust-core/src/index/names.rs`, `rust-core/src/index/envelope.rs`, `rust-core/src/serve.rs`, `rust-core/src/search.rs`, `rust-core/src/symbolgraph.rs`, `api-go/internal/rustcore/worker.go`, `api-go/internal/rustcore/symbolgraph.go`, `api-go/internal/rustcore/knowledge.go`, `rust-core/tests/index_query.rs`  
**New modules.** `rust-core/src/index/csr.rs`, `rust-core/src/index/reader.rs`

**Tests required.**
- CSR built from index.db equals the edges in the DB; reverse edges are consistent
- BFS, trace and blast-radius give identical results to the legacy graph on fixtures (while edges are still lexical)
- Snapshot swap under concurrent readers never produces torn reads
- Synthetic 100k-symbol index: service steady RSS ≤15 MiB above base
- Freshness envelope present on search, explain and impact with correct status values
- Coverage reports losses instead of silent capping

**Acceptance.** No JSON graph deserialization on tool paths when an index exists. The 800-file cap is gone behind the envelope config. The gate v2 run is attached.

**Collision risk.** search.rs is shared with WS-18 (swap the graph source only; leave lanes to WS-18). symbolgraph.rs was changed by w0-drift (WS-00). serve.rs is from WS-02. WS-03 changed indexcache.rs (`repo_key_identity`, `ignored_dirs`) and knowledge.go (registry lookups), so rebase on it.

**Correction.** Critic: RSS acceptance uses a real resolved graph (>=5 edges/symbol) and includes swap double-buffering; decide file-backed CSR segments vs in-memory from measurement.

### WS-15 — Watcher, incremental refresh loop and identity unification

**Goal.** Add a watcher (notify crate: FSEvents, kqueue, inotify) with a serialized debounced refresh queue: 300 ms debounce, max wait, overflow triggers a full refresh, ignore-file change triggers a full refresh, retry with backoff. The Go governor schedules `index update --paths` in the heavy slot, and the service swaps snapshots. Unify changetrack identity onto indexcache::source_identity and the stat cache. ground stops hashing the tree.

**Requirements.** PAR-FRESH-03, PAR-FRESH-04, PAR-FRESH-01, PAR-RT-02, PAR-RT-12

**Files.** `rust-core/Cargo.toml`, `rust-core/src/index/watch.rs`, `rust-core/src/index/refresh_queue.rs`, `rust-core/src/serve.rs`, `rust-core/src/changetrack.rs`, `rust-core/src/indexcache.rs`, `rust-core/src/hashcache.rs`, `api-go/internal/rustcore/worker.go`, `api-go/internal/rustcore/refresh.go`, `rust-core/tests/refresh.rs`  
**New modules.** `rust-core/src/index/watch.rs`, `rust-core/src/index/refresh_queue.rs`, `api-go/internal/rustcore/refresh.go`

**Tests required.**
- Edit, rename, delete and create sequences produce a correct index after refresh
- Debounce coalesces bursts; overflow triggers a full refresh; a .gitignore change triggers a full refresh
- A crash mid-update (dirty flag) forces a full rebuild; escalation gate thresholds work
- Counters for reparsed, reresolved and escalated are exposed
- No git spawn per query; git runs only on overflow or HEAD change (counter assertion)
- ground on an unchanged tree hashes 0 files

**Acceptance.** Results are fresh within ≤1 s after an edit on a 5k-file fixture. ground cost no longer grows with tracked bytes (compared against the 14.8 s gitnexus clone measurement). The watcher_state appears in the envelope.

**Collision risk.** changetrack.rs and indexcache.rs were changed by w0-drift (WS-00), and WS-22 also edits changetrack.rs; sequence WS-22 first. Cargo.toml is shared with WS-16 and WS-37.

**Implementation record (branch parity/ws-15, 2026-09-28).** These notes record what was built and measured, and where it differs from the text above. Measurements: `docs/benchmarks/2026-09-28-ws15-watcher.md`. The work was finished after the first agent was cut off. It was merged with feat/parity-v2 at 49feb45 (WS-21), 3ab0b17 (WS-20) and 144123c (WS-57). Final checks on the Linux box after the last merge (`xm-remote-check.sh all -count=1`): every Rust test target and Go package passed (remote_exit=0), with 6 clippy warnings, the base's count. After review round 1 the check ran three times: the third passed everything (remote_exit=0, 6 clippy warnings); the first two failed only on the debug-binary RSS tests described under the deviations (`index_build` at 25.1 MiB, `index_query` at +15.2 MiB).
- *Watcher (PAR-FRESH-03).* `rust-core/src/index/watch.rs`, inside `serve`, uses notify 8.2. It runs one watcher per registered root, at most 4 roots, least recently used first out. A root that is stopped or evicted is announced `absent`. `watch start` finds or registers the root under one lock, so concurrent starts for a root (parallel first reads) register one watcher.
  - Backends: macOS uses FSEvents, one recursive stream. Linux uses inotify, with one watch per covered directory (at most 32,768 per root) plus the Git dir, `logs/`, `info/` and every `refs/` directory. Directories that appear later get watches, and the files already in them are queued. The watched directories are kept as a set: a directory notify already watches is counted once, and one removed or renamed away leaves the set with those below it (notify drops their watches), so `dirs_watched` and the limit check count live watches.
  - Coverage: the whole root except the directories Git ignores as a whole. That list comes from `git status --ignored=matching` at start and again after an ignore file changes (a restart and a full refresh); without it, the built-in exclusions apply. After a Git index change the list is learned again without a restart (one `git rev-parse` and one `git status --ignored=matching` in the watcher's thread each time it handles events that touch the Git index): a directory that is no longer ignored as a whole (a file in it was force-added with `git add -f`) is covered, its files are queued and, on inotify, its directories are watched. Files over 32 MiB are dropped.
  - Git state: in the Git dir, HEAD, the index, packed-refs, config, `logs/HEAD` and `refs/` count as a Git state change. `.gitignore`, `.xmustardignore` and `info/exclude` count as ignore files, except inside a directory Git ignores (Git never reads those), such as `.venv/.gitignore`.
- *Refresh queue.* `index/refresh_queue.rs` is pure and takes the clock as input.
  - Timing: a batch is due 300 ms after the last change and no later than 1 s after the first.
  - Full refreshes: more than 1,000 pending paths, an ignore file, a platform rescan (inotify queue overflow, FSEvents must-scan-subdirs, a watch error) or an OS watch limit makes the batch a full refresh. The first reason is kept. A full verification is also queued every hour, because same-size edits can pass a stat check.
  - Serialization: batches never overlap. A failed batch is retried after 0.5 s, doubling to 30 s. After 6 failures in a row the queue stops until the next change, and the state is `degraded`. A batch in flight longer than 11 minutes is failed. A read's forced take supersedes a lost batch and merges its paths back.
  - Sync: a batch is taken only after a cookie file written into the Git dir comes back as an event, the Watchman scheme. Without the cookie the batch says `synced: false`, and the whole tree is refreshed.
- *Orchestrator (Go).* `api-go/internal/rustcore/refresh.go` and `workspaceops/code_index.go`.
  - Registration: every code-index read and ground registers its root with the live worker's watcher (`WatchRoot`).
  - Background refresh: the worker sends id-less `$/refresh.due` and `$/watch.state` frames, which `worker.go` now reads instead of discarding. On `$/refresh.due`, `onRefreshDue` observes the identity and starts a background refresh. That refresh takes the batch (`watch take`), runs `index update --identity-key K --paths ...` through `rustcore.RunIndex`, and reports the result (`watch done`). RunIndex takes the heavy slot (WS-06B), and nothing on this path takes it again.
  - Whole-tree fallback: without `--paths`, the whole tree is checked. That happens for a full batch, an unsynced batch, no watcher, or a read's forced take that found nothing pending although the identity moved.
  - Heavy work: while heavy work holds or waits for the slot, a due batch is left pending. The watcher announces it again after 10 s, so a read never joins a refresh queued behind a build.
  - A read that finds the index behind its identity takes the pending batch at once, without waiting for the debounce.
  - `watch` calls run only on the worker, never one-shot. With the worker off, nothing is watched and reads refresh as in WS-14.
  - A replacement worker registers the roots its predecessor watched right after its handshake, before it takes a call (`rewatch`). The governor recycles the worker under memory pressure; without this, gate v2 saw reads that reported `absent` between a recycle and the next registration.
- *Snapshot swap.* A successful `watch done` reloads the root's resident snapshot when one is resident. `reload_if_resident` releases the slot list before it locks the slot.
- *Counters (PAR-FRESH-04).* `freshness.refresh {reparsed, reresolved, escalated}` comes from the index's `last_run` meta. `reresolved` counts the resolver's dependents set (WS-17). `watch status` shows `last_refresh` with the mode and the reason. The escalation gate and the dirty flag are WS-07's; the tests drive them through the loop.
- *No Git per query (PAR-RT-12).* While the watcher is `ok`, the commit relation is kept past its 2 s TTL until a Git state event. Every Git child outside the tests is counted (`indexcache::git_spawns`, shown in `$/stats`), including the index scan's streamed listings and `ownership owners`.
- *watcher_state in the envelope.* The value is `ok`, `overflow` (a full refresh pending or running), `degraded` or `absent`. `degraded` means the native watcher is down (ENOSPC or `MaxFilesWatch`, more than 32,768 directories, another watch error) or refreshes keep failing. The detail says why. A full refresh (`watch_limit` or `rescan`) is queued, and the native watcher is retried after 60 s, doubling to 30 min. Reads refresh the index themselves meanwhile, so nothing is silently stale. `symbolgraph coverage` now carries freshness, so ground's coverage block shows the generation, the watcher state and the counters (the WS-16 note).
- *One identity scheme (PAR-FRESH-01).*
  - `indexcache::repo_state` is one observation of the worktree: the source identity, the status entries it was computed from, the branch and the remote. `changetrack` drift and working changes read it instead of running their own listings.
  - When HEAD equals the baseline's, drift compares only the paths `git status` lists, plus those an explicit baseline took in (new `taken_in`). A path deleted from the Git index (`D `, as after `git rm --cached`) counts as absent whatever the worktree holds, as in the full comparison. It falls back to the per-file map of every tracked file (through the stat cache) on a HEAD move, a root below its worktree top level, a legacy dirty baseline, or a listed path that could not be identified.
  - The identity hashes dirty and untracked files through a stat cache of its own (`hashcache::hash_files_budgeted`, keeping the identity's byte budget and order), so an unchanged dirty file is not read again. A reused file is charged its size against the 64 MiB budget like a read one, so which files the key covers depends on their sizes and order only, and the key stays a function of the tree.
  - Inside `serve`, a watched root's observation is reused until any event under the root: no Git and no hashing.
  - drift and changed-since report `drift_checked {mode: watcher|identity|full|none, hashed, stat_skipped, compared}`.
- *Tests.* `rust-core/tests/refresh.rs` covers:
  - edit, rename, delete and create equal to a full build;
  - burst coalescing;
  - `.gitignore`, overflow and an injected ENOSPC turning into full refreshes and `degraded`;
  - on Linux, the per-root directory limit, and the directory count staying put over `mkdir -p`, a rename and a removal;
  - a crash (dirty flag) forcing a full rebuild, and escalation at 60 of 100 files;
  - the counters in the envelope and in `watch status`;
  - zero Git spawns over 20 queries past the TTL, and relating again after a commit. The spec's "git runs only on overflow or HEAD change" holds for queries, not for refresh batches (see the deviations);
  - ground on an unchanged tree hashing 0 files and, under the watcher, spawning no Git;
  - eight concurrent `watch start` calls registering one root;
  - a file force-added into an ignored directory reaching the batches, with the index equal to a full build.
  The queue, the classification (including an ignore file inside an ignored directory), the budgeted hash pass (a warm pass keeps what the cold pass kept) and drift after `git rm --cached` have unit tests. The Go tests cover the batch and argument tables, notice dispatch, no one-shot spawn for `watch`, the real worker's batches, a replacement worker registering its predecessor's roots, and an API test where an edit becomes one `index update --paths src/engine.go` and the next search finds the new symbol with no core exec.
- *Gate v2.* `rss_v2.sh run --core-worker` runs every API of the run with the resident worker. The watcher lives there, and so does the `rust_index_service` process the WS-15 line is on. The report records `core_worker`, and `--workstream` refuses a baseline measured the other way.
- *Measured.* All three freshness paths stay under 1 s on 5,000 files:
  - Linux, inotify, in-process: p50 424 ms, max 447 ms.
  - macOS, FSEvents, in-process: p50 488 ms, max 504 ms.
  - Linux, through the API with the worker (one-shot `index update --paths` in the heavy slot): p50 497 ms, max 549 ms.
  - ground's drift hashes 0 files on an unchanged tree of any size. On the 7,288-file, 85 MB composite (Linux, settled Git index), drift takes 42-46 ms against 104-109 ms at base (164 ms with the base's stat cache cold); `git status` alone takes 18 ms. Under the watcher it spawns no Git at all.
  - Gate v2 with the worker on (parity composite, three runs per scenario): the WS-15 check on the designated agents-2 is `BELOW_RESOLUTION`. `rust_index_service` PSS p50 moved -0.1 MiB, RSS p50 -1.4 MiB, and the owned tree -3.3 MiB. With edits under load, the service's p50 is 18.0-18.5 MiB with the watcher against 21.5 MiB without it at base.
  - Every scenario fails the 95.4 MiB gate at base and head alike (agents-2: 200.9-209.1 base, 201.3-208.6 head). The resident worker peaks at 171-185 MiB, almost all anonymous. That fits WS-14's open item: while the index lags the reads, the worker builds the composite's legacy graph. The ledger records this under WS-15's `note`.
  - With the watcher, the swap under load happens: at base, no snapshot-swap-under-load run advanced the generation. Head swapped in 3 of 3 runs in its first set and 1 of 3 in the rerun; the heavy slot still refuses refreshes while the worker's peak holds the tree over the soft ceiling.
- *Deviations.*
  - kqueue is not used: notify uses FSEvents on macOS and kqueue only on the BSDs.
  - Each refresh batch runs Git. PAR-FRESH-03 and the test list say Git runs only on overflow or a HEAD or Git index change; that holds for queries under the watcher. But every batch's one-shot `index update --paths` scans the tree: `git ls-files -s` plus a stat of every tracked file. The scan took 16-23 ms at 5,000 tracked files, 49-52 ms at 20,000 and 114-153 ms at 50,000 (whole update 68-80, 149-154 and 219-279 ms; Linux box, `docs/benchmarks/evidence/2026-09-28/ws15-scan-cost-linux.txt`). The listing alone is 4-17 ms. The 1 s freshness acceptance was measured at 5,000 files only; at 50,000 a batch still lands in about 0.55 s by these numbers. Skipping the listing for a synced batch would need the update to trust the batch's paths for tracked-set changes, which WS-15 does not do.
  - The watcher lives in the opt-in resident worker. After the worker's 2-minute idle exit nothing is watched until the next call starts a worker, which registers the roots again. Their start-up batches then check the whole tree.
  - The Go identity cache still validates with its own stat walk. PAR-RT-12's "invalidated by watcher events" is done for the Rust side only (the commit relation and `repo_state`).
  - `content_hash` is still computed when a baseline is written and by `changetrack fingerprint`. Drift no longer compares it.
  - recall's `drift_checked` stays WS-20's integer count. impact reports `drift_checked` for its changes mode only.
  - The ENOSPC path was tested with an injected `MaxFilesWatch` and a lowered per-root limit; the kernel's `max_user_watches` could not be lowered on the box without root.
  - The `index_query` resident-RSS test (WS-14, debug build, 15 MiB line) failed once in a full Linux suite while a base build ran beside it. It passed alone (+14.5 MiB steady on head, +13.9 on base) and in the final suite. It sits near its line on Linux in debug builds. In the review round 1 checks it failed once in the full suite at +15.2 MiB after the swap and passed alone (+14.7).
  - The `index_build` 5,000-file peak test (debug binary, 25 MiB line) sits at its line after review round 1: 24.7-25.1 MiB against 24.1-24.7 MiB before the round, over ten runs each on the box. The difference is in file-backed pages of the 126 MB debug binary; anonymous memory is 3.5 MiB on both (sampled from `/proc`). Those pages move with code layout and with how much of the binary is in the page cache: a variant with a `HashSet` in place of the `BTreeSet` had a smaller binary and a higher peak, and variants measured later, with their binaries no longer freshly written, peaked at 21.9-23.8 MiB. In release builds the same test peaks at 15.7-15.9 MiB on head against 16.7-17.3 MiB before. The line was left as it is; measuring the release binary, or leaving the binary's own mapping out, is for the test's owner (WS-07) to decide.
- *Review round 1 (2026-09-28).* Fixed: concurrent starts registering a root twice (the major finding: the duplicate's start-up batch stayed pending, was announced again every 10 s and kept the worker from its idle exit); drift missing a file untracked in place; the ignored-directory list going stale after `git add -f`; the inotify directory count; ignore files inside ignored directories restarting the watcher; the layout cloned per event (now shared); Git spawns not counted in `ownership owners` and the index scan; and the identity key depending on the stat cache past the byte budget. The per-batch Git scan is recorded as a deviation above. Checked on the Linux box only (this round's rule keeps builds off the 8 GB Mac): the FSEvents paths of the new code (the ignored-directory recheck, the classification order) were not re-run on macOS.
- *Files outside the list.*
  - Rust: `index/envelope.rs`, `index/reader.rs`, `index/mod.rs`, `symbolgraph.rs`, `bin/xmustard-core.rs` (the `watch` command, coverage freshness), `index/scan.rs` and `ownership.rs` (Git spawn counting) and `go-calls.txt`.
  - Go: `rustcore/root.go` (`watch` is worker-only), `workspaceops/code_index.go`, `workspaceops/grounding_index.go`, and the tests `rustcore/refresh_test.go`, `workspaceops/code_index_test.go` and `cmd/xmustard-api/worker_process_test.go`.
  - Gate: `scripts/bench/rss_bench_v2.py`, its test and the README; `scripts/bench/budget_ledger.json` (a prose `note` on the WS-15 line, not a governance change: `rss_v2.sh ledger --diff` lists none).
  - Docs: `docs/ARCHITECTURE.md`, `docs/benchmarks/2026-09-28-ws15-watcher.md` and its evidence under `docs/benchmarks/evidence/2026-09-28/ws15-*`.

### WS-16 — Language packs via the provider contract

**Goal.** Add feature-gated language packs for Python, Java, C, C++, C#, Ruby, PHP, Kotlin, Swift and Bash (Scala and Lua optional). Grammar crates are cached locally. Queries are written originally or taken from upstream MIT tags.scm, never from GitNexus. Precompile the regex fallbacks once. Report coverage honestly per language.

**Requirements.** PAR-SYM-05, PAR-RT-11

**Files.** `rust-core/Cargo.toml`, `rust-core/src/treesitter.rs`, `rust-core/src/repomap.rs`, `rust-core/src/index/lang/mod.rs`, `rust-core/src/index/lang/python.rs`, `rust-core/src/index/lang/java.rs`, `rust-core/src/index/lang/c.rs`, `rust-core/src/index/lang/cpp.rs`, `rust-core/src/index/lang/csharp.rs`, `rust-core/src/index/lang/ruby.rs`, `rust-core/src/index/lang/php.rs`, `rust-core/src/index/lang/kotlin.rs`, `rust-core/src/index/lang/swift.rs`, `rust-core/src/index/lang/bash.rs`, `rust-core/src/index/lang/queries`, `rust-core/tests/lang_fixtures.rs`  
**New modules.** `rust-core/src/index/lang`

**Tests required.**
- Per-language fixtures: definitions, nested scopes, imports, references, doc comments
- Coverage report lists supported, unsupported and failed files per language
- Regex fallback compiles once (benchmark)
- Binary size delta recorded; the worker with all packs peaks ≤25 MiB on the 5k-file fixture

**Acceptance.** 14 languages are indexed through the provider contract with honest coverage in search, explain, impact and ground. License provenance is noted for each query file.

**Collision risk.** Cargo.toml is shared with WS-15 and WS-37 (dependency commits first). treesitter.rs was touched by WS-07. repomap.rs has no other in-flight edits.

**Status.** Implemented on `parity/ws-16`: 15 packs (the 14 languages; TS and TSX are separate rows) in the `index::lang` provider table, the ten added grammars behind `lang-*` features (default `lang-all`), golden fixtures in `rust-core/tests/lang_fixtures.rs`, per-language `language_support` in index coverage and `languages` in graph coverage, and both regex fallbacks compiled once. Measurements (binary +22.2 MB; the spec fixture stays under 25 MiB RSS; with every pack parsed the worker peaks at about 50 MiB RSS, of which 16–21 MiB is dirty) are in `docs/benchmarks/2026-09-26-ws16-language-packs.md`. Import resolution for the added languages is WS-39. Entry-point patterns are WS-60.

### WS-17 — Scope/import resolver for Go and TS/JS with typed edges, confidence tiers and epistemic counters

**Goal.** Replace lexical file-to-file edges with resolved symbol edges for Go and TS/JS. Edge kinds: CALLS, IMPORTS, EXTENDS, IMPLEMENTS, HAS_METHOD, HAS_PROPERTY, ACCESSES, USES. Confidence is 0.95 same-file, 0.9 import-scoped and 0.5 global. Provenance is lexical, scope, scip or lsp. Import semantics: named and namespace for TS, wildcard-leaf for Go. Receiver typing covers simple cases. Record drop counters for the epistemic envelope. Re-resolve only the dependents of changed exports.

**Requirements.** PAR-IMP-01, PAR-IMP-05

**Files.** `rust-core/src/index/resolve/mod.rs`, `rust-core/src/index/resolve/scope.rs`, `rust-core/src/index/resolve/imports_go.rs`, `rust-core/src/index/resolve/imports_ts.rs`, `rust-core/src/index/resolve/emit.rs`, `rust-core/src/index/resolve/counters.rs`, `rust-core/src/index/schema.rs`, `rust-core/tests/resolve_fixtures.rs`  
**New modules.** `rust-core/src/index/resolve`

**Tests required.**
- Resolution fixtures: same-file, import-scoped, global fallback, re-exports, namespace imports, Go package-qualified calls, method receivers
- Names inside strings and docstrings never create edges (regression for the memory note)
- Confidence values and provenance are set per tier
- Only dependents of changed exports are re-resolved (counter assertion)
- Drop counters are populated for unresolved receivers and external calls
- Precision against gopls/tsserver ground truth on a small fixture is measured and reported

**Acceptance.** impact on Go and TS/JS returns resolved symbol edges with confidence and provenance. Lexical edges remain only as provenance=lexical fallback. The precision number is recorded for EVAL-09.

**Decision (2026-09-26).** Go and TS/JS files keep their lexical `structure` and `flow` layers next to the `symbol` layer. `index impact` reads only the symbol layer for these languages, so the lexical layers are the fallback in data, not in queries. They stay because WS-14 ports the legacy consumers (repo map, MCP `impact`) onto `index.db` and needs file edges at legacy parity for every language first. Cost: 2 to 7 s more on a full build (`docs/benchmarks/2026-09-26-ws17-resolver.md`). Revisit after WS-14: when nothing reads the lexical layers for resolver languages, skip them where `resolve::resolves(lang)` holds.

**Collision risk.** schema.rs (from WS-07) gets an edges table addition. No in-flight collisions.

### WS-18 — Code BM25 lane, hybrid fusion, snippets and explainable reasons

**Goal.** Add BM25 over names, subtokens, paths, doc comments and function-aligned chunks (FTS5 in index.db, incremental per file). Rewrite fusion: RRF K=60 as a deliberate choice, lanes_matched, per-lane scores and reasons. The explainable rerank uses declaration identifier match, window coverage, identifier normalization and camel/snake split, over the top K. Return bounded line-numbered snippets. Serve the docs lane from the index instead of per-query rereads. Remove or honestly rename the FNV hashing lane.

**Requirements.** PAR-RET-01, PAR-RET-02, PAR-ADP-01, PAR-RT-11

**Files.** `rust-core/src/index/fts.rs`, `rust-core/src/index/chunks.rs`, `rust-core/src/search.rs`, `rust-core/src/index/rerank.rs`, `api-go/internal/workspaceops/knowledge.go`, `api-go/internal/mcpserver/tool_search.go`, `scripts/bench/gold`, `rust-core/tests/search_hybrid.rs`  
**New modules.** `rust-core/src/index/fts.rs`, `rust-core/src/index/rerank.rs`

**Tests required.**
- Body-term queries find functions whose names do not contain the term
- Snippets are bounded, line-numbered and at most 3 per hit
- reasons[] present and deterministic
- No per-query file enumeration or doc rereads (syscall/read counter)
- The retrieval gate (scripts/bench/retrieval_gate.py) improves by the agreed delta without regressions on existing gold queries
- The 'semantic' naming is removed from the hashing lane

**Acceptance.** search returns {path, lines, snippet, lanes_matched, scores, reasons, uid}. The retrieval gate passes at the improved threshold. The BM25 cache stays within 5-8 MiB (gate v2).

**Collision risk.** search.rs is shared with WS-14 (graph source) and WS-37 (embedding lane), so sequence the edits. knowledge.go was changed by w0-feedback (WS-00) and by WS-03 (registry lookups).

**Implementation record (branch parity/ws-18, 2026-09-28).** These notes record what was built and measured, and where it differs from the text above. Measurements: `docs/benchmarks/2026-09-28-ws18-hybrid-search.md`.
- *Lanes and fusion.* `rust-core/src/search.rs` fuses six lanes with RRF, K=60 (documented as a deliberate choice in the module docs): `bm25` (FTS5 over `chunk_fts`: body words, comments, the names a chunk declares, its path; AND over the query terms, then OR), `docs` (FTS5 over the new `doc_fts`), `name` (the old IDF identifier lane, renamed from `lexical`), `fuzzy` (the FNV char-trigram sketch, renamed from `semantic`; it reports `semantic` only when built with `semantic-onnx` and a model loads), `structural` and `proximity`. Every sort breaks ties by path, line, kind and name, and scores are rounded to six decimals, so equal inputs render equal bytes. The top 40 fused candidates are reranked (`rust-core/src/index/rerank.rs`): declaration identifier match (exact, normalized across case/`_`/`-`, or partial over camel/snake subtokens), three-line window coverage with query order, dense line and symbol-like line; each feature that moves a hit is a reason.
- *Hit contract.* Hits keep `kind`, `name`, `path`, `line`, `score` and `reason` (now `reasons` joined) and add `lines` [start, end], `uid`, `scores` (each lane's raw score plus `rrf` and `rerank`), `lanes_matched`, `reasons[]` and `snippet` (up to three ±1-line windows, `N: text`, lines clipped at 160 characters). Results add `offset`, `omitted` and `degradations`; the API adds `next_cursor`.
- *Docs lane from the index.* Tracked `doc`/`guide` files (`symbolgraph::repo_role`) are scanned with the code (at most 2,000, 1 MiB each, same ignore rules), kept in `docs`/`doc_chunks`/`doc_fts` (schema version 3 → 4, so existing indexes rebuild once), and synced by stat key on every build and update in one transaction (`rust-core/src/index/docs.rs`). A doc-only change never starts a new graph generation. Retention applies: `symbol` posts an unordered bag without credential-shaped words, `none` indexes no prose. `symbolgraph::tracked_doc_files` (the per-query `git ls-files` plus rereads, PAR-RT-11's docs item) is gone. The content digest covers the doc tables.
- *Cursor (PAR-RCL-06, search part).* WS-20's signed rule (its `r2` codec after review fix d37b18f): base64url of `s2.<offset>.<mac>`, the mac an HMAC-SHA256 (16 bytes, hex) under a random key held by the API process, over the offset and the ranking key (workspace_id, q, seed, path_glob), compared in constant time. Go rejects, never clamps, a cursor that is over 64 characters, malformed, edited (another offset under the same mac), unsigned (`s1`), from another query, workspace or kind (a recall cursor), or past the 200-hit window; a page stops at the window. Cursors die with the process, as recall's do. The core takes `--offset=N` and `--path-glob=G` before its positionals and stops reading flags at the root, so a query or seed shaped like a flag stays a positional. `omitted` counts the ranked hits after the page. The feedback boost now reorders within a page only, so pages stay disjoint, and a boosted hit always lists `feedback` in `reasons[]` (an exact entry, not a substring of `reason`).
- *MCP and Pi.* `cursor` and `path_glob` are Advanced on `search` (full profile only, documented at `xmustard://docs/tools`). The description now states BM25 over bodies, RRF (K=60) and "trigram typo tolerance (not meaning)", and is 59 bytes shorter; `omitted` and `next_cursor` joined the output schema. Lean caps went down (2024-11-05 by 59 bytes, 2025-06-18 by 1); full caps grew by the Advanced arguments' 198 bytes less the description saving. The Pi mirror carries the new description and forwards `cursor`/`path_glob` when present.
- *Measured.* Retrieval gate: before 12/12, spans 10/10, MRR@5 0.85-0.95, body 0/6; after 12/12, 10/10, 1.0, 6/6 in nine of nine samples. New thresholds (measured minus noise, noise 0): `pass_min` 12 (was 11), `span_min` 10, `mrr_min` 1.0, `body_pass_min` 6. Resident BM25 cache on Linux: +2.6 MiB (cline) and +3.1 MiB (pi-mono) steady and peak, under the 5/8 MiB line. One-shot search: +2.5-2.9 MiB with the text lanes, and 0.16-0.23 s against the base's 0.38-0.45 s. Index writer peak unchanged within 0.3 MiB (22.4 MiB on cline). Gate v2 CI suite on Linux (three repeats each): v1-workload 61.2 → 62.0 MiB and agents-2-relay 66.3 → 66.0 MiB, both PASS; WS-18's own line is NOT_CHECKABLE there (see the deviations).
- *Secret paths and snippets (review fix).* Snippets are the first search path that puts file content into agent context, so they follow `secretpath`'s rule: `fts::chunk_text` refuses a secret path before any read (stored text included) and the hit says `no snippet: secret path`; the scan lists a doc on a secret path with the `secret_path` loss, so it is never read, chunked or posted (its `docs` row keeps the path and that status). Snippet lines mask credential-shaped words (`chunks::is_secret_like`) as `[redacted]` under every retention. Under `content_retention=full` the stored chunk text answers only while the file's stat key equals its non-racy indexed key; otherwise the range is read and hash-checked like the default, so an unseen edit withholds the snippet (`file changed since it was indexed`) instead of showing old lines as current.
- *Rerank scope (review fix).* Hits of a page past the reranked top 40 are read for their span and snippet only: they report `scores.rerank` 0 and no rerank reasons, so `score` = `scores.rrf` there, as the hit contract says.
- *Deviations.*
  - Lanes are weighted in the fusion (evidence lanes 1.0, fuzzy and proximity 0.5, structural 0.25) and the fuzzy lane ranks only candidates above its 0.30 gate. With K=60, membership alone is worth nearly a lane's head (1/61 versus 1/70 at rank 10), so a lane that ranks every referenced file outvoted a body match; the weights are listed in `Lane::weight`.
  - The BM25 cache is measured by a dedicated resident-service test on real corpora (text lanes on minus off; it asserts steady ≤ 5 MiB and peak ≤ 8 MiB), not by gate v2: gate v2 strips `XMUSTARD_*` and runs the core per call, so it never starts the resident worker. Gate v2 learning to start the resident worker is a follow-up shared with WS-14. Gate v2's CI suite was run for the tree totals. `XMUSTARD_SEARCH_TEXT_LANES=off` exists for that measurement and as an operator kill switch; the answer lists it under `degradations`.
  - `path_glob` is the only new filter. `mode='text'` and a `lang` filter for hybrid mode (PAR-RET-01's interface) are not built: `lang` stays the pattern-mode hint. Both are carried as a follow-up (skip the non-text lanes; filter by `Lang::for_path`).
  - The search cursor has its own process key and codec in `knowledge.go`; WS-20's recall codec is not on `feat/parity-v2` yet, so sharing one helper waits for that merge.
  - The MCP server instructions now say search covers code bodies, names, paths and doc sections with BM25 and line snippets (1,394 of 1,400 bytes).
  - When the legacy graph answers (no index, or the index lags the caller's identity) there is no BM25, docs lane, UID or snippet; `degradations` says so. The legacy docs reread was not kept as a fallback. A store of schema 3 (before the docs tables) likewise answers without the text lanes until its next update rebuilds it.
  - With a `path_glob`, the SQL filter is a GLOB superset of the gitignore glob and the exact match runs on the returned rows, so a narrow glob can return fewer than the lane's 100 rows.
  - The `name` and `fuzzy` lanes still walk every symbol name of the snapshot per query (in memory, per distinct name); only the rerank is limited to the top K.
  - New modules beyond the list: `rust-core/src/index/docs.rs`. `index/chunks.rs` gained the doc chunk planner. The cursor lives in `knowledge.go` (not a shared package), following WS-20's in-package codec.
  - `rust-core/tests/search_hybrid.rs` holds the contract tests; the per-query read counter is a thread-local count of chunk-range reads (`fts::chunk_reads`), and the "no rereads" proof is that a doc edited after indexing still answers from its indexed section with the snippet withheld.

### WS-19 — Memory lifecycle writes: history API, supersede/retire/retract/purge, CAS edits, expiry, provenance, verify outcomes

**Goal.** Implement the governed write side on govstore. Add remember supersedes and op=retire|edit with a required reason and base_revision CAS. Verify outcomes become approve|reject|duplicate_of|retract|helpful|misleading|stale_harm, with a note and an evidence handle checked at vote time. Purge is admin-only and leaves a digest tombstone. Add expiry, kinds and tags on write, provenance binding (HEAD, branch, dirty, evidence, run and session), redaction on ingest, and an optional owner-distinct principal policy.

**Requirements.** PAR-PROV-01, PAR-GOV-04, PAR-GOV-05, PAR-GOV-06, PAR-GOV-12, PAR-GOV-09, PAR-GOV-16, PAR-PROV-04, PAR-PROV-05, PAR-SEC-04, PAR-SEC-05, PAR-ADP-04

**Files.** `api-go/internal/workspaceops/memory_propose.go`, `api-go/internal/workspaceops/memory_verify.go`, `api-go/internal/workspaceops/memory_edit.go`, `api-go/internal/workspaceops/memory_history.go`, `api-go/internal/govstore/entries.go`, `api-go/internal/govstore/revisions.go`, `api-go/cmd/xmustard-api/memory_routes.go`, `api-go/cmd/xmustard-api/main.go`, `api-go/internal/mcpserver/tool_remember.go`, `api-go/internal/mcpserver/tool_verify.go`, `api-go/internal/workspaceops/memory_lifecycle_test.go`  
**New modules.** `api-go/internal/workspaceops/memory_history.go`, `api-go/cmd/xmustard-api/memory_routes.go`

**Tests required.**
- Supersede: promoting the new entry atomically marks the old entries superseded with invalidated_at, and history is kept
- Retire and retract are governed; purge is admin-only and leaves a tombstone with the digest
- CAS conflict returns the current revision and digest; a no-op edit is rejected; a reason is required
- Verify binds to a specific revision; the edit-revision diff is returned
- Expired entries are hidden from lists, still fetchable by id, and restorable
- Provenance fields are captured at propose and promote
- Secrets in content are rejected or redacted with the pattern named
- The owner-distinct policy blocks self-verification across two tokens of one owner

**Acceptance.** The MCP remember/verify schemas carry the new arguments with terse descriptions. The HTTP PUT/DELETE context routes move to memory_routes.go. All lifecycle tests are green.

**Collision risk.** Owns memory_propose/verify/edit.go. WS-20 owns memory_recall.go, so disjoint. mcpserver tool_remember.go and tool_verify.go are per-tool files. main.go gets one registration line.

**Correction.** Critic: split into WS-19a (lifecycle states, CAS edit, expiry) and WS-19b (provenance binding, evidence-bound votes, owner-distinct policy, ingest redaction).

**WS-19A delivered (2026-09-26).** Lifecycle states, CAS edits and expiry go through the existing tools. None is advertised in the lean tools/list: remember takes op=propose|supersede|edit|retire|restore, entry_id, base_revision, reason, old_string, new_string, description, supersedes and expires. verify takes outcome=approve|reject|retract and revision. recall takes entry_id and history. remember's content is no longer required, because edit, retire and restore act on an existing entry. The full-profile caps grew by exactly the cost of the new Advanced arguments, and the lean caps shrank by 23 bytes. Accepted deviations:
- A pending supersession is kept in the new entry's `supersedes` metadata. It is applied in the promoting transaction and then cleared, so no schema migration is needed.
- There is no separate retire verdict. Retiring promoted memory casts the caller's retract verdict and needs the entry's quorum, like verify(outcome=retract). An admin or human approver archives at once, and an author archives their own unpromoted proposal at once. Protected-memory human gating waits for WS-31's tiers.
- Fetch by id is served on the recall route (`GET context/active?entry_id=`), so the nine tool routes stay one per tool. A reader sees verified content only; unverified text, the pending diff and history=true need the verifier or human-approver role, and history is capped at 48 KiB. Agents restore through remember(op=restore). Admin and approver retract and purge are `DELETE context/{id}`, and a pure approver restores through `POST context/{id}/restore`, both in memory_routes.go.
- Review fixes: expiry on peer-verified memory is governed (admin or approver), one edit is pending at a time, a retire vote needs the verifier role, a replacement takes the strictest gate of what it supersedes, and a purge whose checkpoint fails still reports 200 with a warning.
- duplicate_of and the helpful, misleading and stale_harm outcomes, with evidence handles checked at vote time, are left to WS-19B.

**WS-19B delivered (2026-09-26).** Provenance binding, evidence-bound votes, the owner-distinct policy and ingest redaction go through the existing tools, advertised only in the full schema profile: remember takes evidence and run_id; verify takes outcome=duplicate_of|helpful|misleading|stale_harm, target and evidence_handle. The full-profile caps grew by exactly the cost of these Advanced arguments; the lean caps are unchanged. Tokens carry an owner and a kind (D-16), and `principal_distinctness=owner` refuses approve and reject from a principal that shares an owner with the entry's or the revision's author. Every event a write appends records its call id, run and evidence under `provenance`, and `recall(entry_id)` returns provenance, verification_basis and feedback. Remember, verify notes, legacy PUT content and approver reasons are redacted before they are stored. Accepted deviations:
- The dirty flag is not sampled at propose or promote (it would spawn git per write); HEAD and branch are recorded.
- Revision-level evidence lives in the event provenance, not in a revisions column, so no migration was needed.
- The owner policy is enforced when a vote is cast: a second approve or reject from another token of an owner that already voted on the revision is refused, so the quorum counts owners. Votes cast before an operator turns it on keep counting, and the store's peer trigger still compares token ids.
- Review fixes: an edit's resulting content is redacted (not only new_string), the revision author's owner comes from the owner its edit event recorded, an over-long transport call id is recorded as a bounded label instead of refusing the write, and an unknown principal_distinctness in POST /api/settings is 400.
- Feedback outcomes are recorded and summarized but do not yet bias ranking (WS-20 owns recall ranking).
- The Pi integration's tool list does not carry the WS-19A or WS-19B Advanced arguments.

### WS-20 — Recall v2: ranking, filters, disclosure, budgets and verification-queue visibility

**Goal.** Rebuild recall on govstore. FTS5 BM25 is fused with path overlap, trust, recency and a stale penalty, and explain=true returns score_details. Filters: kind, tags, topic, path_prefix, since/until, by, status (promoted, pending, awaiting_me), include_pending, include_superseded, show_expired. Add cursor pagination, names_only, a compact render with max_chars, session-seen suppression with TTL, and history=true. Add pending_for_you and memory_pressure to ground.

**Requirements.** PAR-RCL-01, PAR-RCL-04, PAR-RCL-05, PAR-RCL-06, PAR-GOV-02, PAR-GOV-09, PAR-ADP-04

**Files.** `api-go/internal/workspaceops/memory_recall.go`, `api-go/internal/workspaceops/memory_render.go`, `api-go/internal/workspaceops/grounding_memory.go`, `api-go/internal/govstore/fts.go`, `api-go/internal/mcpserver/tool_recall.go`, `api-go/internal/mcpserver/tool_ground.go`, `api-go/internal/workspaceops/memory_recall_test.go`, `api-go/internal/workspaceops/recall_contract_test.go`  
**New modules.** `api-go/internal/workspaceops/memory_render.go`

**Tests required.**
- Ranking fixtures: stemming and IDF beat bag-of-words, and score_details sum consistently
- Superseded entries are hidden by default and shown with include_superseded
- A pending entry appears only with include_pending or status=pending, labeled unverified with votes_needed
- awaiting_me excludes the caller's own entries and entries they already voted on
- Session-seen suppression and re-show when content or stale state changes
- max_chars is honored with truncation reported; the cursor is stable across pages
- ground.pending_for_you counts are correct
- The existing recall contract (digest binding, 3 retries, withholding) is preserved

**Acceptance.** recall exposes the documented arguments and fields. The verification queue is discoverable over MCP alone. Recall latency and RSS are recorded.

**Collision risk.** grounding_memory.go (created by WS-03) is also touched later by WS-28, WS-31 and WS-33. govstore/fts.go was created by WS-01. The mcpserver tool_recall.go and tool_ground.go files are per-tool.

**Implementation record (branch parity/ws-20, 2026-09-26; review rounds 2 and 3 on 2026-09-28).** These notes record what was built and measured, and where it differs from the text above.
- *Ranking (PAR-RCL-01).* `govstore.Ranking` reads a content-free view of the requested rank states (served, pending, superseded, expired; the four conditions exclude each other and are ORed in SQL; superseded is only an entry that was once promoted, so a pending or rejected proposal that a promoted entry replaced is never ranked) with caller authorship and vote flags, peer approvals, recorded author owner and WS-19B outcome counts; `MemoryScores` returns FTS5 BM25 (porter-stemmed, IDF-weighted) over title, body and anchors. `memory_rank.go` sums signed signals: bm25 scaled so the best visible match scores 3, path 1.5 per focus path, anchor 0.5 per nested path, trust (verification mode, approvals, rank state), recency, feedback (helpful +0.2, misleading -0.5, stale_harm -0.75, bounded) and the stale penalty (-1). An explicit query or path gates relevance at 0.15. `explain=true` returns `score_details` with reasons. Equal totals order by `updated_at` (newest first) then id, so a store renders the same page every time. The bm25 scale and the recency origin are taken over the entries the caller can see (after states and filters), so an invisible pending or rejected entry never shrinks a visible one's score.
- *Filters, queue and ground (PAR-GOV-02, PAR-GOV-09).* kind, tags, topic (`a/b` matches `a/b/c`), path_prefix, since/until (UTC date or RFC 3339; until exclusive), by; status promoted, pending or awaiting_me; include_pending, include_superseded, show_expired. `remember` accepts kind (one of eleven), topic and tags. Reading unverified text (status pending or awaiting_me, include_pending, entry_id with history) needs the verifier or human-approver role; the check uses the caller's effective roles, so with `XMUSTARD_AUTH=off` while tokens exist the anonymous reader gets 401 (fail closed), and open mode still passes. awaiting_me and `ground.pending_for_you` leave out entries the caller wrote or voted on and, under `principal_distinctness=owner`, peer-gated entries written under the caller's owner. Not pre-filtered: an edit by a same-owner principal or a sibling token's earlier vote; RecordVote still refuses those with ErrSameOwner. ground reports `pending_for_you` and `memory_pressure {core_pct, pending_count}` in the memory section (cap 768 to 1,024 B).
- *Disclosure and budgets (PAR-RCL-04, -05).* `names_only`, `render=compact` (160-byte line), fetch by `entry_id`. `max_chars` bounds the whole JSON result; entries that do not fit are dropped, the last may be cut (`content_truncated`), and `output_budget` reports it. The MCP tool always sends `max_chars` (default 4,000); over plain HTTP it is opt-in. Session-seen suppression (`session_id`, at most 128 bytes: HTTP 400, MCP maxLength) is held in the API process under a SHA-256 of workspace, caller and session, with 16-byte seen-prints, at most 1,024 sessions and 65,536 entries across all of them (the least recently used other session is dropped to make room; a session that alone would pass the cap starts over) and a 30-minute TTL. It re-shows an entry when its revision, stale flag or rank state changes, and counts only a full, uncut render as shown: a names_only or compact recall does not hide the entry from a later full recall. The promoted-title index is the MCP resource `xmustard://memory/index` (recall names_only over promoted memory, 50 entries, max_chars 10,000, next_cursor to continue through recall), listed in resources/list.
- *Cursor (PAR-RCL-06).* `next_cursor` and `omitted` on recall. A cursor is `base64("r2.<offset>.<block>.<mac>")`, where mac is HMAC-SHA256 (process key from crypto/rand, first 16 bytes) over the kind, the offset, the block size, the workspace and every argument that orders or filters the ranking, the caller included. Recall and WS-18's search cursor (`s2.<offset>.<mac>`) share one codec, `workspaceops/cursor.go`, with the kind prefix as a parameter, so a cursor of one kind never decodes as the other. An edited offset, another workspace's, query's or caller's cursor, a search cursor, and any cursor after an API restart are rejected (HTTP 400, MCP tool error); an offset past the end of a ranking that shrank since is rejected with a message to recall again. The first page fixes a block size, 4 × limit (at least 16); the stale penalty re-orders entries only within their block, and a page loads whole blocks covering the positions it scans (up to max(block, 4 × limit) from the offset, for session-seen skips). So pages never repeat or skip an entry, also when the page size changes between pages, and a page loads and drift-checks at most max(block, 4 × limit) entries plus two blocks, whatever its offset (the review's crafted offset of 1,000,000 is now rejected; a deep legitimate page costs the same as the first).
- *Measured (build box: Linux 7.0, 6 cores; base = feat/parity-v2 d9199ac, after = this branch).* `BenchmarkRecall1k` (1,000 promoted entries, query `gadgets topic7`, which matches every entry): base 21.7 ms/op, 3.15 MB/op, 46,178 allocs/op; after 26.7 ms/op, 3.39 MB/op, 37,934 allocs/op (8 runs each, interleaved; spread under 1 ms). API process (fresh data dir, 1,000 memories proposed over HTTP in open mode, 20 warm-up and then 300 recalls with a rotating query, limit 8, timed with curl): p50 23.5 ms, p95 25.8 ms before; p50 28.5 ms, p95 31.0 ms after, and the same (28.8 / 31.5 ms) with max_chars=4000. RSS from /proc/PID/status: idle 17.7 MB both; seeded 29.0 MB both; after 600 recalls 30.3 MB before and 31.5 MB after; VmHWM 31.2 MB before and 33.3 MB after. The +5 ms is the BM25 signal (MemoryScores scores every match, about 4 ms here) and the wider ranking view. Round 2 dropped the unused search-token column from the view and replaced the per-row CASE filter with the ORed state conditions, which cut allocations below the base. The local M1 numbers (a heavily loaded machine) are too noisy to quote.
- *Review round 3 (final targeted round, 2026-09-28; base feat/parity-v2 fd083c2 merged first).* (M1) `rankStateCases` and its Go twin `Entry.RankState` require `promoted = 1` for superseded, so `include_superseded` (a reader's read) never returns a pending or rejected proposal that a promoted entry replaced; the HTTP regression (a reader token, a pending and a rejected entry superseded by an approved one) failed before the fix, returning both with their unverified text, and passes after it. (M2) session_id is bounded to 128 bytes, the sets are keyed by a digest and capped globally, as above. Measured on the build box with the API process in open mode (200 memories, then 1,536 sessions with 128-byte ids, two recalls of 32 each, so 1,024 sets of 64 entries hold 65,536 entries from session 1,024 on and 512 more sessions keep evicting; 110 s): VmRSS 17.1 MB idle, 28.0 MB seeded, 29.4 MB after 200 recalls without a session, 33.2 MB at 256 sessions, 41.7 MB at 1,024 and 42.2 MB at 1,536; VmHWM 43.2 MB. A 129-byte session_id gets HTTP 400. The sets alone hold 6.0 MB of live heap at both caps (95 B per entry, measured in-process); the rest of the rise is GC headroom. The reviewer's probe measured 772 MB at the old caps. (Minors) The cursor MAC covers the workspace; the recall and search cursors share one codec (above) and both packages keep their cross-kind rejection tests, now also with a cursor of the other kind signed over the very same ranking. The stale-paging guard seeds 200 entries and bounds each page by 2 × block + max(block, 4 × limit); with `pageSpan` loading from 0 it fails at page 7 (64 entries against a bound of 60). `fitRecallBudget` sizes each rendered entry once, bounds the prefix by those sizes and binary-searches it: at most 2 + log2(n) builds, none over max_chars bytes of entries (the whole page is tried first, since only it can drop next_cursor; short of it the result grows with every entry). On the build box, 50 promoted entries of 64 KB with limit 50: 21.6 ms and 10.9 MB per recall with max_chars 4,000 against 19.0 ms and 17.0 MB without a budget (the review measured 94 ms and 108.6 MB against 15 ms and 23.9 MB before the fix, on its own machine). govstore caps an entry's tags at 32 (`MaxTags`, ErrInvalid, so HTTP 400) for every writer. recall normalizes `topic` in validate the way remember stores it (trimmed, no leading or trailing "/"). Gate: `xm-remote-check.sh all -count=1` passes (remote_exit=0; Go vet and every Go package, Rust release build and tests, clippy at 6 warnings, the base count); Pi `node --test test/unit.test.ts` passes (43 tests; Pi is unchanged).
- *Deviations.* (1) PAR-RCL-04 says max_chars is clamped to 1,000 to 10,000; recall rejects out-of-range values (HTTP 400, MCP -32602) like WS-54's ground contract (owner decision). (2) The full-profile tools/list caps rose by exactly the measured cost of the new Advanced arguments (reader and verifier +921 B, proposer and agent +1,218 B with remember's kind, topic and tags, both protocol versions); the lean caps did not change (owner decision). (3) PAR-RCL-05 puts the promoted-title index in MCP instructions; the instructions are static and at their 1,400 B budget, so the index is the resource above and the instructions' resources line was rewritten to point to it (1,397 B). (4) PAR-RCL-06's `next_cursor` and `omitted` on search moved to WS-18; recall's cursor and ground's memory_pressure are here. (5) Pi's mirror is unchanged: its parity test covers the lean schema, which did not change, so Pi agents get neither the Advanced recall arguments nor the default max_chars (as with ground in WS-54); `node --test test/unit.test.ts` passes (43 tests). (6) Review round 3: session_id's maxLength (Advanced) raised the full-profile caps by exactly its measured 16 bytes for every role and protocol version, the lean caps unchanged at WS-18's lowered values; maxLength counts characters and the API counts bytes, so a multi-byte id between 128 bytes and 128 characters passes MCP validation and gets the API's 400. (7) The per-session entry cap was replaced by the global one, as the review asked; the 1,024-session cap stays, since it bounds the per-set overhead.
- *Tests.* Ranking (stemming and IDF, score_details sum, feedback), superseded and pending visibility and labels, awaiting_me (own, voted and owner-policy siblings) with pending_for_you, filters, cursor (stable pages, other query, other caller, edited offset, shrunken ranking), stale-entry paging with changing page sizes and the window bound, tie order, max_chars and names_only, session-seen with compact and names_only, bm25 scale over visible entries, the HTTP queue gate including the anonymous reader, the `xmustard://memory/index` resource, and the expired state in the govstore view. Round 3 added: a reader's include_superseded over HTTP never receiving a never-promoted entry, the govstore view and Go twin agreeing on it, the session_id bound (workspaceops, HTTP 400, MCP -32602 and the in-bound edge), the global seen-set caps and eviction order, the cross-workspace cursor, the 200-entry window guard, fitRecallBudget against a linear scan with its build count, the tag cap and the topic normalization. The recall contract tests (digest binding, 3 attempts, withholding) pass unchanged; a withheld entry keeps its ranking position and is skipped.

### WS-21 — why_failed and ground failure signals without platform runs

**Goal.** Make why_failed work in the core product. It accepts a command (run through the bounded run-managed-command), an evidence handle or a pasted log. It writes an outcome record to govstore, reads a bounded tail (last 1 MiB) instead of an unbounded ReadFile, stays idempotent on GET (no feedback writes), and links implicated memories. Feed ground.recent_failed_runs from core outcomes.

**Requirements.** PAR-HAR-06, PAR-RET-11, PAR-RT-11

**Files.** `api-go/internal/workspaceops/failure_explainer.go`, `api-go/internal/workspaceops/outcomes.go`, `api-go/internal/workspaceops/grounding_runs.go`, `api-go/internal/govstore/outcomes.go`, `api-go/cmd/xmustard-api/outcome_routes.go`, `api-go/cmd/xmustard-api/main.go`, `api-go/internal/mcpserver/tool_why_failed.go`, `api-go/internal/workspaceops/failure_explainer_test.go`  
**New modules.** `api-go/internal/workspaceops/outcomes.go`, `api-go/cmd/xmustard-api/outcome_routes.go`

**Tests required.**
- why_failed(command) runs with a timeout and a process-group kill, then records the outcome
- why_failed(evidence_handle) reads a bounded tail from evidence
- Repeated GETs do not change feedback counters
- ground.recent_failed_runs is populated in core-only mode
- A large run output is never fully loaded (allocation test)

**Acceptance.** why_failed is functional without any platform route, and ground shows core failures. Existing failure-explainer tests are adapted and green.

**Collision risk.** grounding_runs.go was created by WS-03. main.go gets one registration line. failure_explainer.go was modified by the diagnostics session's older tree (likely at HEAD).

**Implementation record (branch parity/ws-21, 2026-09-28).** These notes record what was built and measured, and where it differs from the text above.
- *Files.* New: `api-go/internal/workspaceops/outcomes.go` (sources, guards, analysis, recording, the capture hook, ground's reader), `api-go/cmd/xmustard-api/outcome_routes.go`, `api-go/internal/govstore/run_outcomes.go` with `schema_v2_run_outcomes.sql`, `api-go/internal/evidence/tail.go`, and their tests. Changed: `failure_explainer.go` (additive fields, bounded tail read, the GET's feedback write removed, a keyword prefilter before the error-line regexp), `grounding_runs.go` (+1 line in `grounding.go` to pass the request context), `route_gates.go`, `evidence_routes.go` (route registration and the tool-route table), `evidence_capture_routes.go` (one hook call), `mcpserver` (`tool_why_failed.go`, `schema.go`, `dispatch.go`, `usage.go`), `integrations/pi/src/tools.ts`, and `rust-core/src/verification.rs`. **main.go is unchanged**: the routes need the evidence store, so they register next to the evidence routes in `newAPIHandlerFor`, and the existing GET why-failed handler in main.go now also serves outcome ids through `ExplainRunFailureCtx`.
- *Store.* The govstore `outcomes` table (WS-12) is per-memory feedback with a foreign key to entries, so run outcomes are a new `run_outcomes` table added as **migration 2** (files created by the cutover upgrade in place; a test opens a v1-only file). Deviation: the rows live in `govstore/run_outcomes.go`, not `govstore/outcomes.go`. A row keeps the status (`passed`, `failed`, `timed_out`), exit code, command or tool, cwd, evidence handle, total and analyzed bytes, output digest, a 4 KiB redacted tail, the analysis JSON (signals, error lines, mentioned paths, failing tests, redaction count), principal, session and HEAD. `source_key` makes recording idempotent per workspace (log: its digest; evidence and retained captures: the handle's digest, shared by a capture and a later `why_failed(evidence_handle)`; an unretained capture: session, call id and output digest; a command: a random nonce). `subject_key` (the command's cwd and argv) lets the newest outcome of a command resolve its open failures (`resolved_by`, `resolved_at`). Retention prunes outcomes after 30 days (`RetentionPolicy.RunOutcomes`).
- *Sources and guards.* `POST /api/workspaces/{ws}/why-failed` (proposer, mutating, tool `why_failed`) takes exactly one of `command` (split into words like a shell would, nothing expanded) or `argv`, `evidence_handle`, or `log`. A command is checked in this order, fail closed (`workspaceops/outcome_commands.go`): NUL, words that start like a shell operator or redirection (`|`, `&&`, `;`, `>`, `2>/dev/null`, `<`) and substitutions (`$(`, backticks) refused; at most 256 arguments and 16 KiB; the program must be in a closed table keyed by its last path element (subcommands such as `go test|vet|build` and `cargo test|nextest|check|clippy|build`, check-named package scripts for npm/pnpm/yarn/bun, check-named tasks for make/just/task/gradle/mvn with only `-k -s -q -B -jN`, direct test runners and linters, `python -m pytest|...`), with per-program refused flags (`go -exec|-toolexec|-vettool|-ldflags|...`, `cargo --config|-Z`, `--script-shell|--node-options|...`); the cwd must be a directory inside the root (symlinks resolved); no argument may name an absolute, `~` or `..` path anywhere a program may read one, or resolve through a symlink out of the root; a program named by path is resolved from the cwd with its symlinks, must be an executable file inside the root and runs by its resolved path, and a bare name must be on `PATH` (else 400); the timeout is 1 to 240 s (default 45). It runs through `rustcore.RunManagedCommandEnv` only (never the unbounded Go fallback the planner has), which TERMs then KILLs the process group at the timeout, with the daemon's environment minus every `XMUSTARD_*` variable and every variable `redact.SecretEnv` flags, detached from request cancellation, one command at a time. Deviation (superseded by round 3 below): commands ran on a loopback bind unless `XMUSTARD_WHY_FAILED_COMMANDS=0`, and beyond loopback only with `=1`, for any proposer. Command mode is now off by default and serves an authenticated admin only, where the operator opted in. Logs and evidence handles work everywhere. An evidence handle is read with `Store.Tail` as the caller (another principal's, expired or revoked originals answer 404). The last 1 MiB of any source is redacted (the secret rules plus this process's secret-named environment values) before it is analyzed or stored.
- *Reads and ground.* `GET .../runs/{id}/why-failed` reads a platform run or, for an `oc_` id, an outcome, and never writes: the old per-GET `run_fail` bump is removed, and `run_fail` is recorded once, when an outcome is first recorded, for the changed files it implicates (a second report of the same log or original returns `created: false` and feeds nothing back). A platform run's output is read as its last MiB (`readFileTail`) instead of `os.ReadFile`. Implicated files now also match a trailing part of a changed path or an absolute path under the root; answers link up to 8 promoted memories anchored to the implicated files (or, when none is implicated, to the files the output names), and list `changed_files`/`memories` in `unknown` when they cannot be read. `GET .../outcomes?open=true&limit=N` (reader) lists outcomes. `ground.recent_failed_runs` lists open outcome failures of the last 24 h next to failed platform runs, newest first; more than 50 is reported in `unknown`, never cut silently, and an unreadable store is `unknown` too.
- *Captured outputs (PAR-HAR-06).* A capture (`POST .../evidence/capture`) whose family is test, build or lint and that failed (reported error or exit code, parsed exit code, or failing tests) becomes an outcome from the retained original's tail or the unretained projection, keyed as above. A passing capture records an outcome only when its command has an open failure, which it then resolves. The hook runs no Rust or git process, is skipped in read-only mode (capture stays ReadSafe there) and for retention captures (`target` set: Pi re-capturing a result it already delivered), and is best effort: the capture answers either way.
- *MCP and Pi.* `run_id` is no longer required; `command`, `cwd`, `timeout_seconds` (1 to 50 s through MCP, below the clients' 60 s call timeout), `evidence_handle` and `log` (up to 1 MiB) are Advanced arguments. Build sends GET for a run id alone and POST otherwise (a run id with a source travels along so the API refuses the mix). The tool is now `readOnlyHint: false`, `destructiveHint: true`, `idempotentHint: false`, because a build or `make clean` can change the tree; a new `Tool.WriteArgs` keeps it listed on read-only connections, serving run-id reads and refusing the writing arguments before any API call. The reader role profile and `maxResultSizeChars` tests now key on "serves reads". The Pi mirror carries the new description and the optional `run_id`, sends POST without one, and cannot send the Advanced sources (its schema is closed, as for the other Advanced arguments).
- *Gates.* `toolGates` picks a tool's read route as its primary one, so `why_failed` stays usable by readers and a disabled `why_failed` refuses both routes (`403 tool_disabled`). The delivery middleware's route-to-tool ladder became a table and maps the POST to `why_failed`, so MCP gets the usual projection and handle. `docs/SECURITY.md` renders the two new rows and has a section on what the command route runs.
- *Rust.* The managed-command excerpt limit was exactly head plus tail, so the dropped-bytes marker pushed the end of the tail (the runner's summary line) out. It now has 256 bytes of room; a Rust test checks the last line survives, and Go reads the streams' true size from the marker, so `output.truncated` is honest.
- *Measured.* A 1 s timeout on `make test` that backgrounds a `sleep 60` grandchild returns in about 1.2 s and the grandchild is gone (real core; `TestWhyFailedCommandTimeoutKillsTheProcessGroup`). `why_failed` on a 16 MiB platform run output allocates 3,463 KiB on the dev Mac and 4,656 KiB on the Linux box (was a whole-file read); `Store.Tail` of 1 MiB from a 16 MiB original allocates 1,028 KiB. One 1 MiB analysis takes about 39 ms and 4.1 MB of allocations on the dev Mac under load (310 ms before the keyword prefilter, since the case-insensitive error-line regexp ran on every line). The analysis admits its window from the request's transient pool: three copies of the analyzed bytes (3 MiB for an evidence tail or a long log, about 400 KiB at most for a command's two bounded excerpts, admitted after the command ends); the 16 MiB chunked capture test still peaks at 5,186 KiB (limit 6 MiB). tools/list: the lean lists are unchanged at 2025-06-18 (6,643 to 8,776 B, the destructive hints paid for by the shorter description and the dropped `required`) and 44 B smaller at 2024-11-05 (caps lowered to 3,955 to 5,279 B); the full caps grew by exactly the five Advanced arguments (+630 B at 2025-06-18, +589 B at 2024-11-05). Tool descriptions total 1,767 of 1,794 B; the instructions stay at 1,399 of 1,400 B. No resident component was added; the command's processes are external to the owned tree the budget gate measures. Linux gate (`xm-remote-check.sh ... all -count=1`, after merging WS-16 and WS-17): remote_exit=0, clippy warnings 6 (base 7); the process-group test runs there (not skipped) with the real core and `make`.
- *Review fixes (round 2, 2026-09-28).* (a) A cancelled request (client disconnect, MCP cancellation, the stdio backend's 60 s HTTP timeout) used to kill only rust-core, whose command runs in its own process group, and orphan the command without its timeout, outside the owned tree and its helper slot. The runner now runs on `context.WithoutCancel(ctx)` bounded by the timeout plus the runner buffer, so rust-core always TERMs then KILLs the group; the caller gets its cancellation once the command has ended and nothing is recorded. `TestWhyFailedCancelledCommandStillEndsItsProcessGroup` (real core, 3 s timeout, cancel at 1.5 s) returns after about 3.1 s with the grandchild gone; with the old code it returned at 1.5 s. (b) The command no longer inherits the daemon's environment: `RunManagedCommandEnv` passes the scrubbed one; a real-core test prints `tokens=[] deploy=[] visible=[visible]`. (c) The gate is a closed program table instead of `evidence.CommandFamily`, the capture classifier, whose build branch admitted `go run pkg@latest`, `cargo install`, `npm install`, `make deploy`, `mvn deploy`, `node x.js --test`, `cmake -P` and more; `evidence.CommandFamily` is removed (capture labelling never used it), and arguments are now confined too. (d) One why_failed command runs at a time and a second answers 503 at once (`acquireCommandSlot`), so at least three of the four helper slots stay free for tool calls; the analysis window is admitted after the command ends, sized to its output, inside `workspaceops` (`admitAnalysis`), which also replaces the handler's and the capture hook's inline reservations. (e) Revoking an original (`DELETE .../evidence/{handle}`) or the admin purge removes the outcomes made from it (`govstore.DeleteEvidenceOutcomes`), and an admin removes any outcome with the new `DELETE .../outcomes/{outcome_id}` (gate row added). (f) A program named by path is confined relative to the cwd with symlinks resolved and runs by its resolved path; a program that cannot start answers 400. (g) The package's int-only `min`/`max` shadows (`context_replays.go`, `coverage.go`) are removed so the builtins work for `int64` and `min64` goes; the `argvFamily` doc is restored with `CommandFamily`'s removal; `docs/SECURITY.md` describes the table, the argument rule, the environment and the outcome removal. Decisions: the cancel fix detaches (the reviewer's first option) instead of teaching rust-core to forward a termination, which keeps the Rust runner unchanged; the command cap is a constant 1, not a new setting; spawn failures are caught in Go (`exec.LookPath`, an executable-file check) rather than by a new rust-core exit code. Linux gate after these fixes (`xm-remote-check.sh ... all -count=1`, base `feat/parity-v2` 2bf6b2c unchanged): remote_exit=0, clippy warnings 6 (base 7); the three real-core tests (timeout, cancel, environment) take about 4.3 s there together, so they run rather than skip.
- *Deviations and follow-ups.* (1) Platform runs no longer feed `run_fail` at all (it happened on every GET); recording it once when a platform run finishes belongs to the run lifecycle and has no owner yet. (2) A pasted log always records as `failed`, and outcomes without a command (logs, explicit evidence) leave ground only by the 24 h window. (3) Capture outcomes depend on the capture route, which still answers 503 until a production streaming redactor is wired into `captureRedactor`; the hook is exercised with the test redactor. (4) `why_failed(evidence_handle)` of an original a capture already recorded returns that outcome; an outcome shows its evidence handle to every reader, but the handle stays bound to its issuing principal. (5) The capture hook reads the retained original's tail (up to 1 MiB) on the capture path of a failing test/build/lint output, which adds tens of milliseconds to those captures only. (6) A cancelled command runs on to its end or timeout and holds the one command slot meanwhile; a caller that retries at once gets 503 until then. (7) The table is deliberately narrow: `clean` tasks, flags before a subcommand (`cargo +nightly`, `npm --prefix`), absolute paths inside the root and `..` within it are refused, so an agent passes relative paths and sets `cwd`. (8) Deleting an outcome that resolved an earlier failure leaves that failure resolved. (9) The platform planner and runtime probe (`runManagedCommandWithFallback`) still run with the daemon's environment; they start the configured agent runtime in the platform profile, outside this workstream. (10) Command-mode trust policy and hardening, and one environment-scrub policy in the Rust core for every `run-*` subcommand instead of the Go caller's `commandEnv`, are follow-up WS-21B below.
- *Review round 3 (2026-09-28, Astra decision B: split).* The command mode is split off and made opt-in; the findings' five conditions are the merge recheck list.
  - *Default off, operator opt-in plus admin (condition 1).* `XMUSTARD_WHY_FAILED_COMMANDS=1` is read once at startup into the posture (`exposurePosture.Commands`, a bool; unset or `0` is off on every bind, loopback included; any other value stops startup). The POST route checks, in this order and before any spawn, command slot or analysis window: the route gate (proposer, read-only mode, a disabled tool), the body, exactly one source, the operator's opt-in (`403 commands_disabled`), an authenticated principal (`403 admin_required`: open mode has none) and the admin role (`403 missing_role`, audited) (`commandPermit`, `outcome_routes.go`). `workspaceops.RunFailureCommand` re-checks the permit it is handed (`CommandPermit{OperatorOptIn, Admin}`, zero value runs nothing) before `prepareCommand`, the slot and the runner, and refuses with the new `ClassForbidden` (403). The MCP tool (stdio shim and `/mcp`) reaches the same route as its caller, so it meets the same guards; the capture hook and the GET routes never run commands.
  - *Evidence and log never execute (condition 2).* The workspaceops API is split: `RecordFailureOutcome(FailureRequest{EvidenceHandle | Log})` has no command fields, and `RunFailureCommand(CommandRequest)` is the only path to the runner. The route refuses with 400, before either is used, a body with no source or more than one (`command`, `argv`, `evidence_handle`, `log`), a `run_id`, and `cwd` or `timeout_seconds` without a command.
  - *Tests (condition 3).* A fake core counts its `run-managed-command` invocations. `TestWhyFailedDeniedCommandsNeverReachTheRunner` (HTTP): opt-in off (admin, agent), open mode, agent and reader with the opt-in, and eight mixed or odd bodies on each of the three servers all answer before the runner (0 invocations, no outcome); an admin with the opt-in runs (1). `TestMCPWhyFailedCommandsNeedAnAdmin` does the same through MCP over HTTP. `TestWhyFailedLogAndEvidenceRecordWithoutCommands`: with command mode off, in the core profile, a log and an evidence handle each record a govstore outcome (read back with `ListRunOutcomes`) that `ground.recent_failed_runs` lists, with 0 runner invocations. In workspaceops, `TestWhyFailedCommandNeedsAPermit` shows a refused permit takes no runner call, slot, budget byte or outcome, even for a command the table would refuse, and `TestWhyFailedReadsNeedExactlyOneSource` covers the read-only API.
  - *Docs (condition 4).* `docs/SECURITY.md` says enabled command mode is trusted host-code execution by admin credentials, that the program allow-list is not a sandbox, and lists the guard order; its role table, posture row and regenerated route row follow. The tool keeps its lean entry: `command` left the description (`Why a run_id, log or evidence_handle failed: ...`), the Advanced `command` argument says it is for admins where the operator enabled it, and the docs resource (`xmustard://docs/tools`) prints a "Command mode" section from a new `Tool.Doc` field (ground's output-budget doc moved onto the same field, replacing a tool-name check). tools/list, measured after merging WS-18 and WS-22: lean 3,887 to 5,211 B at 2024-11-05 and 6,636 to 8,769 B at 2025-06-18 (9 B under feat/parity-v2 at 2025-06-18, 53 B under at 2024-11-05; caps lowered), full 5,107 to 7,759 B and 7,856 to 11,317 B (feat/parity-v2's full caps plus exactly the five Advanced arguments, 577 and 621 B). The instructions say `why_failed explains a failed run or a log`. `docs/ARCHITECTURE.md` and the PAR-HAR-06 row say command mode is opt-in and admin-only.
  - *Bypasses closed (defense in depth).* `go`: package arguments must be local (`.` or `./...`; `all`, standard-library names and import paths are refused), read with a table of the value-taking go flags (`-run TestA` still works; after `-args` the rest is the test binary's; a flag value this table does not know must be written with `=`); `-mod` only `readonly` or `vendor`; `-modfile` and `-overlay` refused. `mvn`/`mvnw`: lifecycle phases only, a word with a colon (a plugin goal) is refused. A program named by a path must be `gradlew` or `mvnw` (a `byPath` flag in the table); `./node_modules/.bin/jest`, `venv/bin/python -m pytest` and `./make` are refused.
  - *Kept (condition 5).* Cancel still ends the process group (`TestWhyFailedCancelledCommandStillEndsItsProcessGroup`), the command still gets the scrubbed environment through `RunManagedCommandEnv` (`TestWhyFailedCommandEnvironmentHasNoDaemonSecrets`), and one command runs at a time with the analysis window admitted after the run (`TestWhyFailedRunsOneCommandAtATime`, `TestWhyFailedAdmitsTheAnalysisWindowAfterTheRun`); the three real-core tests run on the Linux box (1.1 s, 3.1 s and 0.1 s).
  - *Deviations.* The tool stays `readOnlyHint: false`, `destructiveHint: true` although command mode is off by default: a server may enable it, and the annotations describe the writing calls. `whoami` does not report whether command mode is on; a caller learns it from the 403. The route's own mode check comes before the opt-in, so a mixed request answers 400 even where commands are off.
  - *Checks.* Linux gate (`xm-remote-check.sh ... all -count=1`) at e65b888, after merging feat/parity-v2 fd083c2 (WS-18, WS-22): remote_exit=0, every Rust test binary passing, 6 clippy warnings (6 on the base), go vet and every Go package. The Pi unit tests pass locally (43).

### WS-21B — why_failed command-mode trust policy and hardening (follow-up)

**Goal.** Decide and enforce the trust policy for why_failed's command mode, which WS-21 ships off by default and admin-only (Astra decision B). Enabled, it is trusted execution of the repository's code on the host by admin credentials; the closed program table is defense in depth, not a sandbox. Candidates: run commands in an isolated worktree or container-free sandbox where the platform offers one (namespaces, `sandbox-exec`), a per-deployment allow-list the operator writes instead of the built-in table, a network-off mode, and an audit event per command. Move the environment scrub (today `workspaceops.commandEnv`, applied by the Go caller through `rustcore.RunManagedCommandEnv`) into the Rust core, so every `run-*` subcommand, including the platform planner's and the runtime probe's `run-managed-command` calls, starts its child with one policy.

**Requirements.** PAR-HAR-06 (hardening), PAR-SEC-02

**Files.** `rust-core/src/verification.rs` (the runner), `rust-core/src/bin/xmustard-core.rs`, `api-go/internal/rustcore/managed_command.go`, `api-go/internal/workspaceops/outcome_commands.go`, `api-go/internal/workspaceops/run_control.go` (`runManagedCommandWithFallback`), `docs/SECURITY.md`

**Tests required.**
- Every `run-*` subcommand's child sees no `XMUSTARD_*` variable and no redactor-classified secret, whichever Go caller started it
- A command refused by the operator's policy never reaches the runner; an allowed one is audited
- The chosen isolation, where available, keeps a command from writing outside its worktree

**Acceptance.** SECURITY.md states the enforced policy instead of "not a sandbox", and the scrub has one owner in the Rust core.

### WS-22 — Governed automatic index baseline and bounding of residual unbounded work

**Goal.** Create the drift/contract baseline automatically at workspace registration or first ground, rebaseline it on HEAD change or admin action, keep its history, and never let an agent reset it silently. Also bound the known cliffs: the changed-since no-baseline branch, the per-symbol file re-read in symbol_signature, and explain clustering on a context.Background spawn. Report 'unknown' instead of 0 on errors.

**Requirements.** PAR-FRESH-06, PAR-RT-11

**Files.** `api-go/internal/workspaceops/workspace_lifecycle.go`, `api-go/internal/workspaceops/changes.go`, `api-go/internal/workspaceops/grounding_index.go`, `api-go/internal/workspaceops/symbolgraph.go`, `api-go/cmd/xmustard-api/main.go`, `rust-core/src/changetrack.rs`, `api-go/internal/workspaceops/baseline_test.go`, `rust-core/tests/changetrack_bounds.rs`  
**New modules.** `api-go/internal/workspaceops/baseline_test.go`

**Tests required.**
- Registering a workspace creates the baseline; ground reports baseline {head, indexed_at, auto}
- An agent-role reset is denied; an admin rebaseline is recorded in history
- The no-baseline impact is capped and reports truncation
- symbol_signature reads each file once per call (read counter)
- explain clustering is cancellable through the request context

**Acceptance.** ground and impact work on a fresh install in core-only mode, and the cliffs are bounded with tests.

**Collision risk.** changetrack.rs was changed by w0-drift (WS-00) and is also touched by WS-15, so land WS-22 first. main.go gets a single line to thread the clustering context. grounding_index.go was created by WS-03, which also changed workspace_lifecycle.go and symbolgraph.go (registry lookups in place of snapshot parses), so rebase on it.

**Implementation record (branch parity/ws-22, 2026-09-28).** These notes record what was built and measured, and where it differs from the text above. The first review round's fixes are folded in; the last bullet lists them.
- *Baseline policy (PAR-FRESH-06).* The automatic triggers:
  - Registration: the API's registration route (which the MCP auto-registration calls) starts the build after it has answered, detached, under its own 5-minute context, one per workspace at a time (`StartRegistrationBaseline`). The one-shot ops CLI (`xmustard-ops workspace load`) builds in line (`EnsureRegistrationBaseline`). `LoadWorkspace` itself builds nothing. Reason `registration`.
  - ground builds a missing baseline (`first_ground`) and rebuilds it whenever drift reports a HEAD move (`head_changed`), whatever the worktree holds. POST /index always rebuilds (`admin`).
  The rules:
  - An automatic baseline is the committed state. A tracked path with uncommitted changes (`git diff <head> --raw`, renames split) is taken as HEAD has it: HEAD's bytes, read in `git cat-file` passes (sizes first, content in batches of at most 8 MiB, a blob past the 8 MiB hashing cap left out as hashing leaves it out), or its absence. Uncommitted changes therefore stay visible against the baseline and their contract breaks are reported, so no dirty-worktree hold is needed and untracked files never affect the rebuild. Before the first commit an automatic baseline is empty.
  - An explicit rebaseline accepts the worktree as it is. Its `dirty` flag (the baseline fingerprint's count of uncommitted tracked paths it took in) reaches ground as `baseline.dirty`.
  - Nothing is rebuilt when drift could not fingerprint the worktree or does not say whether a baseline exists. A stored baseline that exists but cannot be read (drift's `baseline_error`) is not replaced automatically, since it may be an admin one: ground holds and says so, and an indexer rebaselines. The history of that rebaseline records `replaced: true` and `previous_error`.
  - Builds of one workspace take WS-12's store lock on the baseline file, and a caller that got it re-reads drift first, so racing first grounds build once. Registration and POST /index wait for the lock. ground only tries it (`sync.Mutex.TryLock` and `flock LOCK_EX|LOCK_NB`): when another build holds it, ground answers at once with `held: "a baseline build is in progress; a later ground reports it"`.
  - ground builds under `budget.WithoutHeavyWait`: behind other heavy work the rebuild is held to a later call and ground still answers.
- *History.* Every build is a govstore `index_baseline` event (a new type in `events.go`; no migration). Its principal is the caller for POST /index and `xmustard:auto-baseline` for automatic builds. It records HEAD and branch, and data `{auto, reason, head, indexed_at, dirty, from_head, replaced, previous_head, previous_error?, tracked_files, signatures}`. The history is written before the baseline is replaced: `changetrack index --stage` writes `index_baseline.staged.json`, Go appends the event, then renames the staged file over the baseline and fsyncs the directory. A build whose event cannot be appended is discarded and the stored baseline kept. A recorded build that cannot be renamed is an error that says so. `IndexBaselineHistory` reads the history; no HTTP route lists it. POST /index was already core and role-gated by WS-09. The tests here show an agent refused (403 naming `indexer`, nothing recorded) and admin and indexer rebaselines recorded under their principals.
- *ground.* A new member `baseline {head, indexed_at, auto, reason, dirty, held?}` follows `drift`. It is declared in WS-54's drift section as a signal that keeps head, indexed_at, auto and dirty. It is null, and listed in `unknown` with the reason, when there is no usable baseline. `auto` is derived from the reason (every reason but `admin`); a core that does not report `baseline_dirty` reads as dirty. ground takes the core's `unknown` entries with their reasons, and counts files and symbols from the core's totals. No tool description or schema changed, so tools/list stays at its WS-54 caps (`tools_list_budget_test` passes unchanged).
- *Bounds (PAR-RT-11).* `ChangeBounds::DEFAULT` lists at most 1,000 changed files and 2,000 dirty symbols and reads at most 200 changed source files; `truncation` says what was cut.
  - Without a baseline, changed-since lists at most 1,000 tracked files as added and reads none of them.
  - A pass reads each file once: `read_file_symbols` takes the symbols and every signature from one read through `read_source_beneath`, counted by `source_reads_on_this_thread` (HEAD blobs of a baseline build count too). Modified files are read first. Only files the extractor reads (not vendor/, node_modules/ and other default exclusions) count toward the 200-file cap and `symbol_files_total`.
  - The symbol cap limits only the listing. Contract breaks are listed first, and `dirty_symbols_total` and `contract_breaks` stay exact; only files left unread (the 200-file cap or a read error) make them unknown.
  - The baseline build reads each tracked source file once and no longer lists and hashes the tree a second time for signatures. It is written atomically (compact JSON), and `changetrack index` prints a summary with the HEAD it replaced instead of the whole baseline.
- *Unknown instead of 0.* When `git ls-files` or `git status` fails, changed-since and working-changes return null counts with the reason in `unknown`, and ground reports those fields unknown with the same reason. Drift carries `error` and stays stale. The baseline build and `incorporate` fail instead of writing an empty baseline or recording every file deleted. Working-changes without a baseline reports contract breaks unknown. A baseline file that cannot be parsed is reported unreadable (`baseline_error`), reads as missing for comparisons, and is kept.
- *Explain clustering.* WS-14 had already moved explain's cluster read to `PathGraphCtx(r.Context())`. `TestExplainClusteringIsCancelledWithTheRequest` shows that its `cluster-of` child dies with the request. The last `context.Background` clustering call, the platform `/clusters` route, now uses the request context (`WorkspaceClustersCtx`).
- *Files outside the list.*
  - `api-go/internal/govstore/events.go` (the event type) and `api-go/internal/groundbudget/sections.go` (the baseline member).
  - `api-go/internal/workspaceops/storelock.go`, `storelock_unix.go`, `storelock_other.go` (`tryLockStore`); `api-go/cmd/xmustard-ops/main.go` (the in-line registration build).
  - `rust-core/src/bin/xmustard-core.rs` (`index --reason= --stage`, the summary, fingerprint errors), `rust-core/src/repomap.rs` (`should_scan_file` is `pub(crate)`) and `rust-core/src/indexcache.rs` (`run_git_bounded_input`, the bounded Git runner with stdin, for `cat-file`).
  - `api-go/cmd/xmustard-api/baseline_routes_test.go`: the role-gate, fresh-install and explain-cancel tests need the HTTP stack.
  - Fixture updates in `ground_routes_test.go` (content-stale drift, since a HEAD move now rebuilds), `health_governor_test.go` (the fake core stages its build), `grounding_bench_test.go`, `grounding_split_test.go` and `grounding_unknown_test.go`; `recall_route_test.go` and `security_middleware_test.go` wait for detached registration builds before their temp dirs go.
  - `frontend/src/lib/api.ts` and `Cockpit.tsx` (nullable change lists, the baseline and drift fields). `tsc -b` passes and eslint is clean on both files.
  - main.go changed in four places, not one: the index handler records the caller, `/clusters` takes the request context, and the registration route starts the detached build. `workspace_lifecycle.go` is unchanged.
- *Measured.* macOS, on a scratch clone of research/pi-mono (1,929 tracked files, 1,650 of them source). The base is feat/parity-v2 at fc91aec. Runs alternate base and new at a load average of 6 to 15.
  - changed-since without a baseline: base 2,013,300 B in 10.3 to 10.9 s at 29 to 30 MiB max RSS; now 75,850 B in 0.02 to 0.1 s at 7.3 MiB.
  - `changetrack index`: base 14.6 to 17.2 s, printing 1,567,693 B; now 8.6 to 10.8 s (3.4 to 3.8 s at a load of 6), printing 245 to 282 B. Max RSS 26.5 to 28.2 MiB against 28.5 to 29.1 MiB. Both record 8,886 signatures; the baseline file is 1.57 MB.
  - After the review fixes, at a load of 6 to 9: a clean-tree automatic build 1.98 to 2.00 s at 26.4 to 27.0 MiB; with 24 `.ts` files carrying an uncommitted signature change, the automatic build (24 paths from HEAD) 1.92 s at 26.8 MiB and the explicit one 1.87 s at 26.6 MiB. Against the automatic baseline, working-changes reports 25 contract breaks; against the explicit one, 1. The 24 edits are the difference; the remaining break is present in both.
  - 250 committed changed `.ts` files: base 383,451 B; now 303,229 B, listing 2,000 of 2,507 symbols from 200 of 250 files, with contract breaks unknown. Time is within noise (0.4 to 1.1 s against 0.6 s).
  - 24 dirty files with a changed signature: base and new agree on 322 dirty symbols and 20 contract breaks.
  - ground's steady path makes no extra core call. A due rebuild adds a drift re-read, the build and a drift after it. Registration no longer adds the build to its request.
- *Test changes.* In `TestGroundFitsDefaultBudgetAndPagesSections`, a drift that carries a baseline makes the 6,000-character result omit the principal and drift sections, and their signals move to `output_budget.signals`, as WS-54's contract allows. The test now accepts principal and baseline either as members or as those signals. `TestAPIWithTheCoreWorkerServesToolRoutesWithoutPerCallExecs` now also expects one `changetrack index` exec: its seeded workspace has no baseline, so the first ground builds one, one-shot under the heavy slot like the code index update.
- *Not done here.* Three PAR-RT-11 items are left: why_failed's unbounded read belongs to WS-21, the docs lane still re-reads docs on every search, and the regex fallback is already compiled once (`LazyLock`). Read-only mode still allows automatic baseline builds, which change derived state only, like the code index refresh. POST /index and the registration build still wait for the baseline lock without following their context; the wait is bounded by one build. HEAD blobs are read without clean/smudge filters, so under `core.autocrlf` or LFS a reverted file can read as modified until the next rebuild.
- *Review round 1 (fixed).* An automatic baseline of a dirty worktree took in the uncommitted signatures, so ground reported 0 contract breaks where it should have reported the break: automatic builds now take the committed state, ground carries `baseline.dirty`, and `TestBaselineLifecycleWithTheRealCore` registers with an uncommitted signature change and expects the break. The HEAD-move hold counted untracked files as dirty and could hold forever: the hold is gone (the committed-state baseline makes it unnecessary), and the same test moves HEAD with an untracked file and a new edit and expects `head_changed`. ground no longer blocks on the baseline lock (`TestGroundNeverWaitsForAnotherBaselineBuild`, both the in-process and the cross-process lock). Registration no longer runs the build in its request. An unreadable baseline is held, not replaced (`TestUnreadableBaselineIsKeptForAnIndexer`, `staged_rebaseline_replaces_nothing_and_names_an_unreadable_predecessor`). History is written before the baseline is replaced (`TestBaselineIsNotReplacedUnrecorded`). The symbol-file cap ignores files the extractor never reads (`symbol_file_cap_counts_only_files_the_extractor_reads`). The ground method is renamed `fillBaseline`.
- *Checks.* The Linux build box gate (`xm-remote-check.sh ... all -count=1`) passed at 088e7e7 with `remote_exit=0`: all Rust suites, 6 clippy warnings (7 on the base), go vet and every Go package. The first run failed on the worker exec-count test, fixed in a00ccc5. The frontend's `tsc -b` and `vite build` pass locally. After the review fixes (7a04eee) the gate passed again with `remote_exit=0`: 12 Rust test binaries with no failures, 6 clippy warnings, and every Go package.
- *Requirement status.* PAR-FRESH-06 is met: automatic baselines never take in uncommitted changes, rebuild on every HEAD move, and are recorded before they replace anything. PAR-RT-11 is met for changed-since, symbol_signature, explain clustering and the ground and impact unknowns; why_failed (WS-21) and the docs lane stay open.

### WS-23 — Hook service, static hook client and Claude Code adapter plugin

**Goal.** Add /api/hooks/claude/<event> routes in the daemon, with a thin Go fallback client (xmustard-hook, Unix socket, fail-open at ~200 ms). The Claude Code plugin uses http and mcp_tool hooks. Behaviors:
- PostToolUse: capture, reduce and return shape-matched updatedToolOutput.
- PreToolUse Grep/Glob/Bash rg|grep: inject BM25/graph hits plus verified memories for the pattern.
- Read/Edit: inject path-bound memories.
- Grep-burst nudge with a cooldown, and a post-git freshness notice.
- SessionStart: ground summary plus core memories.
- FileChanged: feeds the dirty set.
No Rust or git spawn happens per hook.

**Requirements.** PAR-HAR-01, PAR-HAR-02, PAR-ADP-06, PAR-CTX-01, PAR-FRESH-07, PAR-PROV-07, PAR-RT-12

**Files.** `api-go/cmd/xmustard-api/hooks_routes.go`, `api-go/cmd/xmustard-api/hooks_routes_test.go`, `api-go/cmd/xmustard-api/main.go`, `api-go/internal/hooks/claude.go`, `api-go/internal/hooks/pattern.go`, `api-go/internal/hooks/steering.go`, `api-go/internal/hooks/hooks_test.go`, `api-go/cmd/xmustard-hook/main.go`, `api-go/cmd/xmustard-hook/main_test.go`, `integrations/claude-code/hooks.json`, `integrations/claude-code/mcp.json`, `integrations/claude-code/README.md`, `integrations/claude-code/testdata`  
**New modules.** `api-go/internal/hooks`, `api-go/cmd/xmustard-hook`, `integrations/claude-code`

**Tests required.**
- Golden hook JSON in and out for each event, using recorded Claude Code payload fixtures
- A shape mismatch falls back to the original within budget
- Timeout fails open with empty output
- Parallel PostToolUse calls are handled concurrently
- Counter assertion: zero Rust/git spawns per hook
- Captures are redacted; the session and agent ids are recorded as attribution
- Pattern extraction skips value-taking flags; steering cooldown works

**Acceptance.** The plugin installs manually and reduces native Bash/Read/Grep outputs with recoverable handles, and SessionStart context works. Conformance fixtures pass. The main.go diff is one registration line.

**Collision risk.** main.go gets one line. Otherwise new packages only.

**Correction.** Critic: add SubagentStart/Stop, CwdChanged, WorktreeCreate/Remove, UserPromptSubmit keyword-trigger recall, PostToolUse(Edit|Write) new-diagnostics delta (tree-sitter syntax errors first), explicit http-hook timeouts.

**Implementation record (branch parity/ws-23, 2026-09-28).** These notes record what was built and measured, and where it differs from the text above. Event shapes and the `hooks.json` format were read from the Claude Code hooks reference (code.claude.com/docs/en/hooks, fetched 2026-09-28), not guessed.
- *Hook service.* `api-go/internal/hooks` holds the protocol: a table of 15 Claude Code events (`claude.go`; each is answered within the budget or queued), the part of each body it reads (`Decode` skips a field of an unexpected type instead of failing), the answer (`hookSpecificOutput` with `additionalContext`, `updatedToolOutput`, `watchPaths`, each only where the event takes it; nothing to say is a 200 with an empty body), pattern extraction (`pattern.go`), steering state (`steering.go`) and rendering (`render.go`). `cmd/xmustard-api/hooks_routes.go` registers one gated route per event, `POST /api/hooks/claude/<Event>`, from a handler table. A handler runs under the hook budget (`XMUSTARD_HOOK_BUDGET_MS`, default 180 ms); past it the route writes an empty 200 at once, waits for the cancelled work, and counts the timeout. Work that need not delay the answer (recording a run outcome) runs after it is written, on a context detached from the connection. `/api/health`'s operator view gains `hook_usage`.
- *Events.*
  - PostToolUse on Bash, Read, Grep, Glob, WebFetch and other servers' MCP tools stream-decodes the body through the redactor into the spool (`Store.Observe`, format claude), reduces it by tool family, and returns the shape-matched payload as `updatedToolOutput`. A shape rejection falls back to the original with the shape notice as context. Test, build and lint captures become run outcomes (WS-21). A Bash call also feeds the dirty set from Claude Code's `bashEditDiff.changedFiles` and, after `git commit|merge|rebase|cherry-pick|pull|revert|reset|checkout|switch|am`, drops the cached identity and gives the freshness notice (30 s cooldown).
  - PostToolUse on Edit, Write and NotebookEdit feeds the dirty set and reports the tree-sitter syntax errors the edit introduced: PreToolUse records the file's errors, and the delta is a multiset difference keyed by kind, node and a digest of the source line, so errors that only moved are not reported.
  - PreToolUse on Grep, Glob and Bash `rg`/`grep`/`git grep` injects up to five index hits (the hybrid search: BM25, names, structure, proximity) framed as data, and the memories recall ranks for the pattern. On Read and Edit it injects the memories bound to the file, labeling the stale ones. Four searches within a minute, or three in one PostToolBatch, earn one nudge toward `search`/`impact` (2-minute cooldown).
  - SessionStart and SubagentStart inject ground's spawn-free part (`workspaceops.HookGrounding`: failed runs and open failures, stale and pending memory; the change, drift and coverage fields are listed unknown) and the core-tier memories; SessionStart returns the files memory is anchored to as `watchPaths`. UserPromptSubmit pushes the memories a prompt keyword triggers. CwdChanged always returns `watchPaths`, the new workspace's or `[]` (which clears Claude Code's dynamic list). FileChanged feeds the dirty set. WorktreeRemove forgets the cached identity of a worktree at or under the caller's workspace root.
  - PreCompact, PostCompact, Stop, SubagentStop and SessionEnd are queued (bounded; a full queue drops and counts) and answered at once. The consumer ends the session's state (SessionEnd: the main thread's and its subagents'), drops a subagent's (SubagentStop, only with an `agent_id`), and after compaction lets memory be pushed again into the compacted context.
- *Memory.* Every push goes through `workspaceops.AdmitMemory` (hook surface; the core tier on the core surface), so only human-approved, unquarantined, clean memory arrives, framed; the withheld ones are counted in one line with the `recall` call that shows them. A memory is considered once per context until compaction: the main thread and each subagent (its `agent_id`; Claude Code sends the parent's `session_id` from a subagent) have steering state of their own (`hooks.SessionKey`). The push frames admitted memories one at a time against the context left; one that does not fit is counted `too_large` in the withheld note. A memory counts as given only once the answer that carries its frame, or the note that withheld it, has been written. Candidates come from recall (`HookMemories`: relevance-gated by the path or the pattern, never by the working changes, which would run the core) and from one read of the served ranking view (`ReadHookMemoryIndex`: core tier, triggers, anchors).
- *Spawn-free (PAR-RT-12).* The daemon starts no process for a hook, asserted by the spawn counters and by a core binary that leaves a marker. Claude Code itself starts the static client once per event for the two command hooks (SessionStart, WorktreeRemove); the other 13 events are http hooks to the running daemon. `rustcore.WithResidentOnly` makes a bridge call run only on a resident worker that is already running (no start, no one-shot fallback; `ErrNotResident`). Index hits also need an index this process brought up (so the core answers from it rather than building the legacy graph), and the syntax check is a new resident subcommand, `xmustard-core syntax-check` (`rust-core/src/syntaxcheck.rs`: ERROR and MISSING nodes by position and line digest, no content, root-confined, secret paths refused, 20 errors per file). The repository identity is never sampled; hook captures are `captured_identity=unknown`.
- *Static client.* `cmd/xmustard-hook` reads the event on stdin and posts it over the Unix socket (`internal/hooks/transport.SocketPath`), falling back to TCP, and dials the socket only when `transport.CheckSocket` passes (a directory owned by the user with no group or other bits, a socket owned by the user), so no token goes to a socket another user planted; it speaks just enough HTTP/1.1 (no `net/http`, no `os/exec`, pinned by a test), always exits 0, and prints nothing on any failure or after `XMUSTARD_HOOK_TIMEOUT_MS` (default 200). The daemon serves the socket from `main.go`'s one added line: a directory that passes `transport.CheckDir` (owner and mode), a 0600 socket, only `/api/hooks/`, the full middleware stack, never taken from a live daemon, and only a stale socket of the user's is ever removed.
- *Plugin.* `integrations/claude-code`: `.claude-plugin/plugin.json`, `hooks/hooks.json` (http hooks with explicit timeouts of 2 s and 1 s for SessionEnd, the token and optional workspace from `allowedEnvVars` headers; command hooks for SessionStart and WorktreeRemove, run from `hooks/bin/xmustard-hook` because a top-level `bin/` goes on the Bash tool's PATH and claude.ai and Cowork refuse a plugin that has one; the MCP matcher excludes xMustard's own tools), `.mcp.json` (the Streamable HTTP endpoint), `README.md`, and 22 recorded payload fixtures with golden answers (one is a subagent's Read).
- *Redaction wired (WS-05).* `redact.Writer`, the write-side counterpart of `Reader` (each segment between flushes equals `Bytes` over it, window-bounded), is installed as `captureRedactor` in production builds (`capture_redactor.go`, excluded under `-tags xmustard_e2e`), with the daemon's secret-bearing environment values. (Superseded at the WS-FIX-03 merge: WS-FIX-03's `redact.Writer` and unconditional `capture_redactor.go` are the one implementation, and the hook routes wrap it in `failClosed`; see the WS-FIX-03 merge resolution.) `POST .../evidence/capture` no longer answers 503 `redaction_unavailable` in production, which also lifts the block WS-24 recorded for Pi (not re-measured here).
- *Attribution (PAR-PROV-07).* A captured observation records the session and, new, the subagent (`Observation.agent_id`), for attribution only.
- *Fixed on the way.* `openWorkspaceFileBeneath` compared the final descriptor with the root's, and the kernel reuses the closed root's number, so every path of even depth (`src/a.go`) was refused as the root itself: memory anchored to such a path was baselined as missing and never flagged stale. It now tracks whether a component was opened (`TestConfinedReadOfEveryDepth`).
- *Tests.* Golden in/out for every event in one scripted session with the whole session counter-asserted spawn-free (`TestHookGoldenSession`); shape mismatch falls back within the budget; timeout fails open with an empty 200 and a throttled busy note later; four parallel PostToolUse captures meet at a barrier (concurrency); captures are redacted and attributed; workspace scope, roles and audit; the event, handler, queue, gate and `hooks.json` tables agree, with SessionStart as a command hook, SessionEnd within 1.5 s and WorktreeCreate absent; the socket serves only hook routes; pattern extraction (value-taking flags skipped, `-e`, `--`, pipelines, env prefixes); steering cooldowns and per-principal session keys; the syntax delta; the client's fail-open cases and imports; `redact.Writer` against one-shot redaction; resident-only calls; a noted path reaching the real watcher's next batch with no spawn (`watch note`, in Rust and through the Go worker); the spawn-free reads; the Rust syntax check. Review round 1 added: a subagent's context is its own, both ways (`TestHookSubagentHasItsOwnContext`, golden `15a`); a memory too large for the context left is noted `too_large` and one admitted into a timed-out answer is pushed on the next hook (`TestHookPushBudgetsFramesAndMarksOnlyWhatIsDelivered`); CwdChanged out of any workspace answers `watchPaths: []`; the keyword note names `trigger-<keyword>`; the client skips a socket in a 0755 directory or a path that is not a socket and sends its token only over TCP (`TestSkipsASocketThatIsNotPrivate`); the daemon keeps a regular file at the socket path and replaces a stale socket; a short grep pattern is never replaced by its path operand.
- *Measured (build box, Ryzen 5 3500X).*
  - `BenchmarkHookAnswers` (in process, the full handler stack, the resident worker faked): PreToolUse(Read) pushing a memory 1.98 ms/op (185 KB, 2,376 allocs); PreToolUse(Grep) 0.78 ms; PostToolUse(Bash) capturing a 30,000-character output 1.98 ms (1.1 MB); SessionStart 1.66 ms.
  - `scripts/bench/hooks-load.py` against the built daemon, 600 calls of each from 4 concurrent clients: PreToolUse(Read) p50 4.2 ms, p95 7.5 ms, max 15 ms; PostToolUse(Bash, 30k) p50 4.0 ms, p95 8.5 ms, max 12 ms, 600 of 600 outputs replaced; API RSS 27.7 → 35.8 MiB, peak (VmHWM) 41.7 MiB; spawns 0.
  - `xmustard-hook`: 3.5 MB binary; max RSS 4.8 MiB (median of 20 SessionStart runs, max 4.9); wall under 10 ms (the resolution of `/usr/bin/time`); with no daemon it prints nothing and exits in under 10 ms. D-08 expected 5 to 10 MiB for a Go client; without `net/http` it is under 5.
- *Deviations.*
  - SessionStart uses the static client, not `mcp_tool`: the reference says SessionStart takes only command and `mcp_tool` hooks, and Claude Code skips `mcp_tool` hooks at launch.
  - WorktreeCreate is not hooked: its hook replaces git worktree creation and must return the new path. WorktreeRemove is a command hook, because a failing WorktreeRemove hook (an http hook to a stopped daemon, too) blocks the removal, and the client always exits 0.
  - The plugin uses Claude Code's standard layout (`hooks/hooks.json`, `.mcp.json`, `.claude-plugin/plugin.json`) instead of the flat `hooks.json`/`mcp.json` the file list names. http hook URLs name `127.0.0.1:8042`, because Claude Code does not expand variables in a hook URL. With the daemon down, an http hook shows a non-blocking hook error notice; command hooks stay silent.
  - Index hits and the syntax delta need `XMUSTARD_CORE_WORKER=1`, a running worker and an index this process brought up; otherwise they are skipped, never started. Without a watcher vouching for the root, the resident reader's freshness envelope can still run `git rev-parse` inside the worker once per envelope TTL; that is the worker's child, not a hook spawn (WS-15's watcher removes it while it vouches).
  - Keyword triggers are the tag convention `trigger-<keyword>` (tags take no `:`); WS-19's `triggers[]` was not built.
  - Every hook route needs the proposer role, not reader, so "no non-GET route grants reader" still holds (`TestNoWriteRouteGrantsReader`).
  - Hook bodies are read whole under the body-limit middleware's admission (Claude Code clamps tool output), and capture stream-decodes from that buffer; the body is not streamed from the socket into the spool.
  - "The dirty set" is WS-15's (merged into this branch before the final checks): `workspaceops.NoteChangedPaths` feeds a changed path to the resident watcher's pending batch through a new `xmustard-core watch note` (a native modify event, so it is classified, size-checked and debounced like any other; resident-only, so nothing starts) and drops the root's cached identity. With no watcher on the root it only drops the identity: reads then check the whole tree, and a hook-observed path is never used as a partial refresh list, since hooks miss edits made outside the tools. WS-15's periodic verification stays the full check for unobserved edits.
  - Not built: PreToolUse `updatedInput` command wrapping (WS-41), compiler and LSP diagnostics in the post-edit delta (WS-45/47), other clients' hook routes (WS-40), and the WS-75 packaging folded here (review skill, OCR recipe, 8-client test), which belongs with WS-40/WS-44's conformance suite. Stop and PreCompact only mark the queue; WS-33 adds the ledger and snapshot behind it. PostToolUseFailure adds context only when it parsed failing tests.
  - Files beyond the list: `internal/hooks/render.go`, `internal/hooks/transport/`, `workspaceops/hook_reads.go` and its test, `workspace_registry.go` (the record map), `safepath_unix.go` (the fix), `evidence/store.go` and `capture_meta.go` (`agent_id`), `redact/stream.go` and `writer_test.go`, `capture_redactor.go`, `evidence_routes.go` (the one registration line: the evidence store lives there; `main.go`'s one line starts the socket), `route_gates.go` (15 rows), `core_only_test.go`, `health_budget.go`, `rustcore/worker.go`, `root.go` and `knowledge.go`, `rust-core/src/syntaxcheck.rs`, `lib.rs`, `bin/xmustard-core.rs`, `index/watch.rs` (`watch note`) and `tests/refresh.rs`, `rustcore/refresh.go` (`NoteChanged`), `go-calls.txt`, `scripts/bench/hooks-load.py`, `docs/SECURITY.md`, `docs/ARCHITECTURE.md` and `.gitignore`.
- *Checks.* After merging `feat/parity-v2` at `845868d` (WS-15's watcher; the conflicts were the core's subcommand table and `go-calls.txt`, both sides kept), the full Linux gate (`xm-remote-check.sh all -count=1`: Rust release build, tests and clippy, then go vet and every Go package) passes, with 6 clippy warnings, the same as the base.
- *Review round 1 (2026-09-28).* Fixed: the socket is trusted only when private to the user (`transport.CheckDir`/`CheckSocket`, called by the client before it dials and by the daemon before it listens; the daemon removes only a stale socket of its own user); steering state is per context (`hooks.SessionKey`: principal, session, `agent_id`), so a subagent and its parent no longer share what was pushed or edit baselines, SubagentStop drops the agent's state (only with an `agent_id`) and SessionEnd the session's; the push frames admitted memories one at a time against the context left (a 512-character reserve keeps the withheld note), counts the rest `too_large`, and marks ids only when the written answer carries their frame or note (`hookResult.offered`, checked after `writeHookAnswer` succeeds); CwdChanged always sends `watchPaths` (`Specific.WatchPaths` is a pointer, so `[]` is sent and nil is not); the keyword note names `recall(tags=["trigger-<kw>"])`; the first positional of `rg`/`grep` is the pattern whatever its length; WorktreeRemove acts only at or under the bound workspace root; the plugin's client moved to `hooks/bin/`; SECURITY.md, the README and ARCHITECTURE.md say auth refusals are 401/403 (non-blocking hook errors), not empty 200s. Deviations: a memory noted `too_large` is marked seen once the note is delivered (like a policy-withheld one), so it is not re-offered in that context until compaction; a SubagentStop followed by a resume of the same subagent re-pushes its memories (harmless duplicates). A mutation check (marking before Compose, and keying without `agent_id`) makes the new route tests and the golden session fail.
- *Checks (review round 1).* Focused on the build box: `internal/hooks`, `cmd/xmustard-hook`, `TestHook*` and `TestSecurityDocRouteGateTable` in `cmd/xmustard-api`, the workspaceops admission tests, and `GOOS=windows`/`GOOS=darwin` builds of the client and `internal/hooks` all pass. The full gate (`xm-remote-check.sh all -count=1`, run twice) passes every Go package and go vet, with 6 clippy warnings (the base's count), but exits 1 on one Rust test: `index_query::resident_rss_on_a_100k_symbol_resolved_graph_stays_within_the_line` (debug, run beside its sibling tests: "Sequential: after swap +15.3 MiB over 15 MiB"). This round changed no Rust. The same test fails the same way on an export of `feat/parity-v2` (845868d) on the same box ("steady +15.3 MiB over 15 MiB"), and passes on this branch run alone in debug (326 s) and as the whole binary in release (80 s). It is a load-sensitive RSS bound that predates WS-23's review round, not a regression from it.
- *Review round 2 (2026-09-28).* The ten round-1 findings were re-verified against the code: the three majors (socket trust, per-subagent steering, marking before `Compose`) and the minors (CwdChanged `[]`, the trigger tag, short patterns, WorktreeRemove scope, fail-open wording, `hooks/bin/`, `commandPrefixes`) are fixed as recorded above. This round added: `TestHookWorktreeRemoveStaysInTheWorkspace` (the root and paths under it are forgotten; a relative path, a path climbing out with `..`, another directory and a cwd in no workspace are not; a mutation that drops the scope check fails it), with `hookForgetRoot` made replaceable like `hookSearch`. The per-event behavior is now documented as the comparison report (`INTEGRATIONS_COMPARISON_2026-09-28`) found it: the README's event table gains an Answer column (only PostToolUse can carry `updatedToolOutput`; SessionStart and CwdChanged `watchPaths`; FileChanged, WorktreeRemove and the queued events nothing), the README, SECURITY.md, ARCHITECTURE.md and the route comment say that the daemon starts no process while Claude Code starts `hooks/bin/xmustard-hook` for SessionStart and WorktreeRemove (measured above: 3.5 MB, under 5 MiB, under 10 ms), and `hooks.json`'s description no longer claims that a missing daemon leaves behavior unchanged (a stopped daemon or a refused token shows a non-blocking hook error). Known residual, not fixed: the daemon marks a memory given once its answer is written to the connection; the static client reads it within its own 200 ms budget, which starts before the daemon's 180 ms one, so a client that gives up in the last ~20 ms (a heavily loaded host) drops an answer the daemon counted as given, and those memories are not pushed into that context again until compaction. http hooks have a 2 s timeout and are not affected.
- *Checks (review round 2).* Focused on the build box: go vet of the hook packages; `internal/hooks`, `cmd/xmustard-hook`, and `TestHook*`, `TestSecurityDoc*` and `TestNoWriteRouteGrantsReader` in `cmd/xmustard-api` pass; `GOOS=windows` and `GOOS=darwin` builds of the client and `internal/hooks` pass. The full gate (`xm-remote-check.sh all -count=1` at `e62c37c`, `feat/parity-v2` still at `845868d`, so nothing to merge) passes with `remote_exit=0`: the Rust release build and every Rust test, including `index_query`'s resident-RSS test that failed under load in round 1 (8 of 8, 319 s); every Go package and go vet; 6 clippy warnings, the base's count. This round changed no Rust and no Pi code.
- *Requirement status.* PAR-HAR-01 and PAR-HAR-02 are met for Claude Code (other clients: WS-40). PAR-ADP-06 is met except the optional `updatedInput` wrapping and `mcp_tool` hooks. PAR-CTX-01: hook capture works with redaction in production. PAR-FRESH-07: Claude Code's FileChanged, edits and Bash changed files feed WS-15's watcher batch when a watcher runs. PAR-PROV-07: captures record session and agent; `proposed_by` on recall and ground is unchanged. PAR-RT-12: the daemon's hook path is spawn-free and counter-asserted (Claude Code starts the static client for the two command hooks), and edit events invalidate the identity cache.

### WS-24 — Pi adapter extended to built-in tools, masking and custom compaction

**Goal.** Project Pi's bash/read/grep/find/ls/edit/write results through the capture API, preserving isError and details. Add turn_end context_edit masking of older results in polling windows, exempting the latest failure and edited files. Add a session_before_compact custom summary whose details carry handles. Keep xmustard_expand activation.

**Requirements.** PAR-ADP-09, PAR-CTX-07, PAR-HAR-04, PAR-CTX-01

**Files.** `integrations/pi/src/index.ts`, `integrations/pi/src/delivery.ts`, `integrations/pi/src/masking.ts`, `integrations/pi/src/compaction.ts`, `integrations/pi/src/tools.ts`, `integrations/pi/test/unit.test.ts`, `integrations/pi/test/e2e/pi-adapter.e2e.ts`  
**New modules.** `integrations/pi/src/masking.ts`, `integrations/pi/src/compaction.ts`

**Tests required.**
- Built-in tool results are projected with handles, and isError/details are preserved
- Masking advances only every N turns and exempts the latest failure and actively edited files
- The custom compaction entry carries handles in details and is recoverable
- e2e with pinned Pi 0.87.1 and the scripted faux provider: the replacement reaches the next model request

**Acceptance.** Pi agents get reduced built-in outputs with recovery. The xMustard-owned tree RSS is recorded in the Pi e2e summary.

**Collision risk.** integrations/pi/src/tools.ts, which WS-00 and WS-04 changed or generated. No Go collisions.

**Implementation record (branch parity/ws-24, 2026-09-25).** These notes record what was built and measured, and where it differs from the text above.
- *Files outside the list.* `integrations/pi/src/config.ts` (new settings), `integrations/pi/src/workspace.ts` (`callerTools`), `test/e2e/harness.ts` and `test/fixtures/scripted-provider.ts` (RPC-driven runs, Pi settings, a per-request digest of every tool result and user text), `scripts/e2e/pi-adapter.sh`, `README.md` and `package.json`. There are two Go changes. First, `POST .../evidence/capture` takes a lower-only `target` (1 KiB to 1 MiB; `evidence.ObservationInput.Target`), so Pi can retain a result behind a handle before it masks or compacts it. Below the 32 KiB Pi target, the store retains nothing. Second, `api-go/cmd/xmustard-api/capture_redactor_e2e.go` is built only with `-tags xmustard_e2e`. `tools.ts` keeps the Go descriptions verbatim and only adds the built-in tool lists.
- *Blocked outside WS-24.* In a production build, capture answers `503 redaction_unavailable` until the WS-05 streaming redactor is wired into `captureRedactor`. Until then, built-in results pass through with the reason in `details.xmustard.reason`, capture pauses for 30 s, masking masks only results that already carry a handle, and compaction falls back to Pi's own. The e2e proves the path with the build-tagged test redactor (one fixed marker), not a secret scanner. `tool_call` input mutation for command wrapping (PAR-ADP-09) needs WS-41's `xmustard-core run --`, which does not exist; Pi does not need it, because `tool_result` replaces results directly.
- *Decisions.*
  - Built-in results at or below the target are never posted: Go would return them unchanged and retain nothing.
  - `details` are not sent to capture. They are model-invisible, and Pi's bash `details.truncation.content` would double the original. Pi's own `details` are kept and gain an `xmustard` member.
  - Masking: `cutoff = turn - AFTER_TURNS` on turns where `turn % EVERY_TURNS == 0` (defaults 10 and 5), counting assistant messages on the branch. These are exempt: the latest failing result, and `read`/`edit`/`write` results of files that an `edit`/`write` changed after the cutoff.
  - A stub is trusted only where a `context_edit` put it.
  - Compaction is a deterministic snapshot of at most 2 KB. Pi's summarizer runs instead for `/compact <instructions>`, when capture is unavailable, or when any retention fails.
  - The Pi half of PAR-CTX-07 closes here; the OpenCode half stays with WS-40.
- *Requirement status.* Under the test build (`-tags xmustard_e2e`) the e2e shows each behavior. In a production build every capture answers 503 until the redactor is wired, so built-in projection, retention for masking and compaction all fall back. PAR-CTX-01 (Pi capture), PAR-CTX-07 (Pi half) and PAR-HAR-04 (Pi half) are therefore **partial, blocked on the WS-05 redactor wiring**, not closed. PAR-ADP-09 is **partial**: its `tool_call` input mutation is not built and waits on WS-41 (or an amendment that drops it for Pi).
- *Review round (2026-09-26).*
  - Handles carry `expires_at`. Masking and compaction treat a handle that expires within an hour as absent and retain the model-visible text again. An in-context stub whose handle has expired is rewritten from the raw entry at the next `turn_end`, even between windows. Carried compaction handles that expired are retained again from the branch, or counted as `expired_dropped`.
  - Mask stubs and snapshot failure lines skip the adapter's own `[xmustard evidence|page|search]` lines, so a masked failure shows the tool's real last line.
  - `session_start` keeps `xmustard_expand` active when the branch already names a handle (a mask stub, an xMustard compaction's handles or index, or a projected result), so resume, reload and fork keep recovery.
  - Compaction retains one index document (the snapshot in full, every handle and failure, an earlier model-written summary in full, and the compacted messages with outputs of 1 KiB or less inline) whenever the whole document does not fit in 2 KB. The snapshot's second line names that handle. Nothing compaction removes is left without a recovery path, including small outputs and other extensions' tool outputs (those appear in the index, so they reach Go's redacting capture route).
  - Paths resolve the way Pi's `resolveToCwd` does (`@` prefix, unicode spaces, `file://`).
  - Built-in captures send `tool_version=pi-coding-agent/<VERSION>`; the adapter's own documents and the nine tools keep `pi-adapter/<version>`.
  - Go: each retained original counts at least `MinRetainedCharge` (16 KiB) against the workspace quota. That keeps the number of originals (and their uncharged metadata) per quota where it was when every original exceeded a 16 KiB client target. The `target` contract is recorded in `docs/ARCHITECTURE.md` for WS-23 and WS-40. It still needs the plan owner's approval, since the plan listed no Go change for WS-24.
- *Caller-scoped tools.* At `session_start`, `GET /api/auth/whoami` (2 s bound) deactivates the nine tools the caller cannot use, so a reader token is not offered `remember`/`verify`. Any failure keeps all nine active.
- *Measurements.* All figures are sampled ps RSS (100 ms, lower bounds) unless marked.
  - `scripts/e2e/pi-adapter.sh` with native Postgres: the first full run passed 37 unit and 18 e2e tests. In the xMustard-owned tree, the built-in phase peaked at 22.2 MiB, masking at 33.6 MiB, compaction at 32.9 MiB, and the whole run at 69.6 MiB (the existing concurrency test). A second full run, after the last hardening commits and at a host load average near 99, passed all three WS-24 e2e tests, at 30.1, 27.6 and 30.8 MiB, and peaked at 65.1 MiB overall. In that run, and in a third run at a load average of 37 to 93, the existing "activated xmustard_expand whose Go endpoint fails" test failed. Its lowered 1,500 ms tool deadline also covers the `impact` call that issues the handle, and that call timed out. Unmodified `feat/parity-v2` code and binaries failed the same way at a load average near 200, after a warm-up. The failure comes from host load, not from WS-24.
  - A capture workload served the pi-mono (MIT) fixture: 40 Pi-format captures of about 50 KB, 256 retention captures at `target=1024`, then 60 page reads and 60 searches, 4 at a time. The API's maximum RSS was 34.8 to 35.0 MiB, measured with `/usr/bin/time -l`. The sampled tree was 31.3 MiB during built-in capture and 34.9 MiB during retention. It reached 79.1 to 79.5 MiB during the concurrent page and search phase: 35 MiB for the API, plus four `xmustard-core` at 6.4 MiB and four `git` at 4 to 5 MiB. Each page read samples repository identity with a core and git spawn. That is pre-existing behavior, and WS-03's page-identity cache is the fix, but masked stubs make parallel expansion more likely.
- *Open follow-up: the e2e's large-result fixture (owner WS-24; assigned 2026-09-28 by the WS-FIX-03 review).* Nine tests of the first e2e suite fail on feat/parity-v2 since WS-22's automatic registration baseline, independent of capture: "a large result is projected at the source", "repository mutation", "hook delivery", "concurrency", "API restart", "expiry", "projection deadline", "activated xmustard_expand whose Go endpoint fails" and "auth". Each needs a multi-page result from `impact` with no arguments, and a fresh fixture now has no changes against its baseline, so that call returns about 400 bytes and issues no handle. The fix is a fixture that gives `impact` a large result after registration (for example, edits across the 400 handler files once the baseline exists, or a symbol or file form once WS-35 or WS-FIX-05 lands). Until it lands, v0.1.1 records the e2e as partially passing.

### WS-25 — Remove remaining unreachable Rust code

**Goal.** Remove the swarm and bench subcommands and modules, the symbolgraph build-lsp and flow subcommands, and the lsp-references/definition/implementation/type-definition/rename subcommands, all of which have no Go caller. Keep upgrade_graph_with_lsp and LspWorkspaceSession (future LSP lane), wiki.rs (called by rustcore/knowledge.go) and goalruntime.rs (called by rustcore/goals.go).

**Requirements.** PAR-RT-08

**Files.** `rust-core/src/swarm.rs`, `rust-core/src/benchmark.rs`, `rust-core/src/lib.rs`, `rust-core/src/bin/xmustard-core.rs`, `api-go/internal/rustcore/bridge_test.go`

**Tests required.**
- cargo test and cargo clippy clean
- A Go bridge test asserts that every subcommand Go calls still exists (wiki, goal, lsp-hover, lsp-document-symbols and the others)

**Acceptance.** Smaller binary; all checks green; nothing Go calls is removed.

**Collision risk.** lib.rs and bin/xmustard-core.rs are shared with WS-02, WS-07 and WS-41. Use the dispatch table.

**Implementation record (branch parity/ws-25, 2026-09-25; corrected after review round 1).**
- *Reachability evidence.* `rust-core/go-calls.txt` lists every xmustard-core call that code reachable from an api-go main makes (41 `called` lines) and the four commands kept without one (`kept`, each with its reason). `TestCoreCallManifestMatchesGoSources` (api-go/internal/rustcore/bridge_test.go) holds the file to the Go sources in both directions. It parses api-go's non-test sources from `coreInvocation` outward and marks each call reachable or not by a name-based walk from the `main` and `init` functions and package-level initializers. Methods are matched by selector name, and a method that no selector names counts as reachable because it may satisfy a standard-library interface, so the walk errs toward reachable. It reads family members given as literals, constants or slice literals, and it fails on a family call whose member it cannot read. `deadcode ./cmd/...` (golang.org/x/tools v0.50.0) agrees: of the rustcore functions that start the core, it reports only `ParseLCOVCoverage`, `RunVerificationCommand` and `RunLspHover` as unreachable, the same three dead wrappers the scan reports. The Makefile calls only `scan-signals`, and scripts/ calls `repo-key` (also a Go call) and WS-07's `index build` (scripts/bench/index-review-r2.py), so `index` stays. A dead_code pass with the library compiled into the binary crate found the unreachable Rust items. Rerun after merging feat/parity-v2 (WS-02 dispatch, WS-07 index): every item it still flags has a test caller (`index/ignore.rs` glob helpers, `secretpath.rs` with its golden test, `indexcache::cheap_key`, `build_symbol_graph_cached`, `hash_repo_file_beneath`, `treesitter::extract_symbols`) or serves the kept `upgrade_graph_with_lsp`/`LspWorkspaceSession`, so nothing more was removed.
- *Removed.* The `swarm` and `bench` subcommands with swarm.rs and benchmark.rs, and `goalruntime::list_iterations`, which only swarm called. The five `lsp-*` position subcommands with `lsp_session::live_{references,definition,implementation,type_definition,rename}`. `symbolgraph build-lsp` and `flow`. Port-era scaffolding with no caller: `initial_{scanner,repomap,verification}_plan`, `run_migration_verification` and `indexcache::file_hash`. The one-shot hints in worker.go were trimmed to match.
- *Kept without a reachable caller.* `lsp-hover`, `parse-coverage-lcov` and `run-verification-command` have Go wrappers that nothing calls (`RunLspHover`, `ParseLCOVCoverage`, `RunVerificationCommand`). None was a WS-25 target. The spec names `lsp-hover` among the Go calls to keep. The serve residency tests send `parse-coverage-lcov`, and the environment-hygiene test routes `run-verification-command`. Each command's owner decides whether to remove it together with its wrapper. `semantic-search` / semantic.rs has no Go wrapper: Go runs ast-grep itself, and WS-37 lists semantic.rs, so WS-37 or the owner decides. `upgrade_graph_with_lsp` and `LspWorkspaceSession` are kept per the goal above. Go never sends `goal lint`; goal becomes platform-only in WS-49.
- *Flow edges kept.* With `symbolgraph flow` removed, no Rust, Go, frontend or integration code reads `SymbolGraph.flow_edges`. The flow pass still runs on every build, and the disk cache and the resident `serve` snapshot still hold its edges. `symbolgraph build` still emits them, and it is reachable through the Go passthrough route, so removing the pass would change that route's JSON. That change is outside this workstream. On pi-mono (800 files indexed) the flow pass produces 2,942 flow edges beside 10,908 structural ones, 585,639 of the 3,706,749 bytes of `symbolgraph build` output (measured). The reviewer's A/B against a core that skips the pass moved warm `serve` RSS by 0–1 MiB, within run-to-run noise. A later cleanup, or PAR-SYM-09, can drop the pass and use `#[serde(default)]` or a cache version bump so old caches still load. No budget credit is booked for it.
- *Files outside the list.* `lsp_session.rs`, `goalruntime.rs`, `scanner.rs`, `repomap.rs`, `verification.rs`, `indexcache.rs`, two lines of `symbolgraph.rs`, `rust-core/tests/serve_process.rs`, `api-go/internal/rustcore/worker.go` (the one-shot hints) and the new `rust-core/go-calls.txt`. Every Rust hunk only deletes an item that nothing calls. The exception is `verification.rs`, which also collapses one nested `if` that clippy flagged. `indexcache.rs` is a WS-00 hot file that WS-15 edits, and WS-16 lists `repomap.rs`. The `rust-core/src/scanner.rs` conflict with WS-07 (WS-25 deletes `ScannerMilestone` next to the exclusion constants WS-07 deletes) was resolved on this branch when it merged feat/parity-v2, by dropping both sides.
- *Measurements (macOS arm64, release).* Binary: 9,420,112 to 9,305,808 bytes (−114,304), and `__text` from 2,733,972 to 2,651,752 bytes (−82,220). Max RSS from `/usr/bin/time -l`, merge-base core (cfc2f5d) against this branch's core, alternating runs with warm caches on a clone of pi-mono (1,929 files):

  | Run | Base MiB | Branch MiB |
  |---|---|---|
  | `repo-key` (3 runs each) | 14.75–14.91 | 14.94–15.23 |
  | `symbolgraph hotspots` (3 runs each) | 15.05–15.28 | 14.80–15.20 |
  | `search` (3 runs each) | 18.27–18.83 | 18.39–18.64 |
  | warm `serve` after 12 queries (8 runs each) | 13.56–21.58 | 14.56–18.28 |

  No resident pages went away, because the removed code was never faulted in. The measured saving is 0 MiB, not the −2 MiB the plan estimated. The table above and PAR-RT-08 say 0, and nothing is booked in the WS-10 ledger.
- *Tests.* Rust: `go_called_commands_exist` reads go-calls.txt and fails when a listed subcommand or family member leaves COMMANDS. `subcommands_without_a_caller_stay_removed` fails on the base table. Round 0's `every_command_has_a_caller` required the table to equal the list; it has been narrowed, so a new subcommand without a Go caller (WS-07 `index`, WS-41 `run`, WS-46 SCIP ingestion) needs no edit here. Go: `TestCoreCallManifestMatchesGoSources` needs no built core, so it runs under `go test ./...` on a clean checkout. `TestEveryCoreSubcommandGoCallsExists` runs every scanned call, every listed command and the worker.go hints against the core. Without `XMUSTARD_CORE_BIN` it runs `cargo build --release` first, which is a no-op when the binary is fresh, so it neither skips nor trusts a stale binary. `TestCoreCallScanFollowsConstantsAndReachability` covers constants, unreadable members, dead wrappers and forwarding on a synthetic module. The review's case passed round 0 and now fails both Go tests: a workspaceops file that calls `RunSymbolgraph(ctx, sgFlowMode, ...)` with `const sgFlowMode = "flow"`, plus `RunChangetrack(ctx, mode, ...)`. The Go bridge test also fails on the base core, where the stale `bench` hint ran the benchmark into its timeout.
- *Clippy.* `cargo clippy` reports 7 warnings, down from 11 on the merge base, and none is new. The remaining 7 are in diagnostics.rs, semantic.rs, symbolgraph.rs, treesitter.rs and verification.rs (one complex type and two long argument lists). "Clippy clean" here means no new warning.

### WS-26 — Release hygiene and packaging

**Goal.** Untrack the 80 backend/data runtime files (git rm --cached, human confirms), reconcile .gitignore, add a tagged-release target that builds a prebuilt xmustard-core and the relay, and fix the HEAD-only Homebrew formula.

**Requirements.** PAR-OPS-03

**Files.** `.gitignore`, `packaging/homebrew/xmustard.rb`, `Makefile`, `.github/workflows/release.yml`  
**New modules.** `.github/workflows/release.yml`

**Tests required.**
- A clean clone builds and passes make check-backend with no backend/data tracked files
- The release workflow dry-run produces artifacts

**Acceptance.** No machine-specific data is tracked, and a release can be cut reproducibly. Untracking is presented to the human for approval.

**Collision risk.** .gitignore and the Makefile were modified in the diagnostics session's older tree. Coordinate. Do not edit README.md or docs/STATUS.md.

**Implementation record (branch parity/ws-26, 2026-09-28).**
- *Untracking: prepared for the owner, not run.* `git ls-files -- backend/data` lists 80 files (4.6 MB), the runtime state of a March–April 2026 install: `settings.json`, `workspaces.json`, two `metrics/run_*.json` files and six workspace directories (snapshots, activity logs, run records and logs, terminal logs, and a stray `run_ea4bf722c9b2.json.<hex>.tmp` from an interrupted write). 39 of them contain absolute `/Users/...` paths. A scan for credential shapes (OpenAI, GitHub, AWS and Slack token prefixes, private-key headers) found none. No code or test reads these files. The Go tests that name `backend/data` create it under `t.TempDir()`, the Rust tests use synthetic paths, and the API creates the directory on its first write. `git rm --cached` leaves the files in history; rewriting history is a separate owner decision. The owner approval packet below holds the list, the script and the procedure for other checkouts.
- *The matching `.gitignore` change* is on this branch, additive: `backend/data/` (the older per-directory rules stay). Git does not apply ignore rules to tracked files, so until the script runs the 80 files stay tracked and runtime edits to them still show in `git status`. New runtime files (`govstore.db`, new workspaces) no longer show. The other additions are `/dist/` (release output), `!docs/releases/` (release notes are a curated public entry point) and `api-go/xmustard-eval` (the one Go binary the list missed). Nothing was removed, and nothing is appended at the end of the file, where the diagnostics branch appends its block.
- *The script, checked in scratch clones of this branch.* It staged exactly 80 deletions and left all 80 files on disk. It refused a second run, an extra tracked file under backend/data, a `.gitignore` without the rule, and an index with a staged change. A second clone held live data: two tracked files modified, plus `govstore.db` and a new workspace directory. A plain merge of the untrack commit there refused because of the modified files. The procedure below then kept all 82 files byte-identical (sha256 of every file before and after) and left `git status` clean.
- *Clean clone.* A fresh clone of the simulated untrack commit has no `backend/data`; `backend/` holds only `sql/`. On the build box that clone passed `cargo build --locked` (debug and release) and `make check-backend` with `GOFLAGS=-count=1`: all 14 Go packages and every cargo test binary passed, and clippy ran. `xmustard-api` then started with its default data directory (`../backend/data`) absent and answered `/api/health`. It does not create the directory at start-up; `writeJSON` creates it on the first write, which the settings tests exercise under `t.TempDir()`.
- *`make release`* (Makefile, additive). `make release VERSION=vX.Y.Z` builds `xmustard-core` and `xmustard-relay` with `cargo build --release --locked`, and `xmustard-api`, `xmustard-mcp` and `xmustard-ops` with `CGO_ENABLED=0 go build -trimpath -ldflags="-s -w"`. These are the flags the v0.1.0 binaries show (`go version -m`: go1.26.1, `-trimpath=true`, `CGO_ENABLED=0`, stripped). Without cgo the Go binaries are static, but they lack the platform profile's PTY terminals (`terminal_pty_unix.go` needs cgo; the stub returns "pty terminals require cgo"). The core profile is unaffected, and the formula caveats state the limit. For the Rust build the recipe replaces `RUSTFLAGS` with `--remap-path-prefix` and `CFLAGS` with `-ffile-prefix-map` (tree-sitter's C asserts embed their source paths), mapping the checkout to `.` and `CARGO_HOME` to `/cargo`. It then checks that the core prints its usage (exit 2) and that `xmustard-mcp` answers `tools/list` with `remember`. It packs the five binaries and LICENSE into `dist/xmustard-<version>-<os>-<arch>.tar.gz`, the v0.1.0 asset naming (`uname -s` lower-cased, `uname -m`). Every entry gets the commit's time (`git log -1` in UTC; `SOURCE_DATE` overrides it), mode 0755 or 0644, and owner and group 0. Entries are listed in a fixed order, with no extended attributes (bsdtar otherwise stores `com.apple.provenance`) and no macOS `._` entries, and `gzip -n` drops the gzip timestamp. Finally it writes the archive's `.sha256` (`shasum -a 256`, present on both platforms), then runs `make release-sums`. That target checks every archive of `VERSION` in `DIST` against its `.sha256` (`shasum -c`) and writes `SHA256SUMS` from them, so a local run gives the same three kinds of file as the workflow. `VERSION` defaults to `git describe --tags --always --dirty`, or `dev` without git. The recipe parses and expands under macOS's GNU Make 3.81 (`make -n release`), and the tar and gzip flags were checked with the Mac's bsdtar 3.7.4 (two runs gave the same bytes); nothing was compiled on the Mac. It builds for the host only; there is no cross-compilation. `make release` and `make build` share `rust-core/target`, so switching between them rebuilds the Rust dependencies, because the flags differ.
- *`.github/workflows/release.yml`* (new). It runs on a `v*` tag push, plus a manual dry run that only uploads artifacts. The build matrix is `ubuntu-22.04` (linux-x86_64) and `macos-15` (darwin-arm64). The Rust binaries need the build runner's glibc or newer, so the Linux leg uses the oldest GitHub-hosted Ubuntu image (glibc 2.35); the Go binaries are static. musl was not chosen: the RSS lines are set on Linux with glibc's allocator, and a musl build changes both allocator speed and resident memory, so it would ship binaries the gates never measured. If the 22.04 image is retired, an older-glibc container or cargo-zigbuild keeps the floor. Each leg validates the tag with an anchored regex (`vX.Y.Z` or `vX.Y.Z-pre`) and runs `make release VERSION=<tag>`. It checks that the archive carries the matrix platform name, so a runner-image change cannot mislabel an archive, then uploads the archive and its `.sha256`. The publish job runs only on tag pushes and is the only job with `contents: write`. It downloads both archives, runs `make release-sums VERSION=<tag>` (the same `shasum -c` and `SHA256SUMS` step as a local run), and runs `gh release create --draft --verify-tag`. Notes come from `docs/releases/<tag>.md` when present, else `--generate-notes`. The owner publishes the draft. `gh release create` fails on an existing release, so a re-pushed tag never replaces assets. Toolchains are pinned (Go 1.26.1, Rust 1.93.1: the versions that built v0.1.0, from the Go build info and the rustc commit in the core). No build cache is restored, so a release never consumes a cache written by a pull-request run. Checkout runs with `persist-credentials: false`. The actions are pinned to the same commit SHAs as check.yml. Each SHA was checked against its tag (GitHub tags API), and each input against the action's `action.yml` at that SHA. `actionlint` 1.7.12 (run on the build box) reports no problems in release.yml or check.yml. shellcheck was not installed, so the `run:` scripts were not shellchecked.
- *Dry run of the workflow on the build box.* The workflow cannot run here because nothing is pushed. The equivalent ran on the Linux x86_64 build box (Ubuntu, glibc 2.43, go1.26.1, rustc 1.93.1, gcc 15.2, GNU tar 1.35) at a9689f8. It ran the build leg's `make release VERSION=v0.1.0-25-ga9689f8 SOURCE_DATE=202609280734.13` plus its name check, then the publish job's `shasum -c` and `SHA256SUMS` step. The box's copy has no `.git`, so the version and commit time were passed as a checkout of that commit computes them. The only step left out is `gh release create`. Artifacts:

  | File | Bytes | sha256 |
  |---|---|---|
  | `xmustard-v0.1.0-25-ga9689f8-linux-x86_64.tar.gz` | 24,539,784 | `bf377ce42ed48a4f003ed51f6f04232dcd8a055fd5df2f6dcfcf746b1e163c89` |
  | `xmustard-v0.1.0-25-ga9689f8-linux-x86_64.tar.gz.sha256` | 114 | (the line above) |
  | `SHA256SUMS` | 114 | (the same line) |

  Archive contents, all 0/0 and dated 2026-09-28 07:34:13 UTC: `xmustard-core` 37,167,848 bytes, `xmustard-api` 22,143,138, `xmustard-ops` 17,817,762, `xmustard-mcp` 7,057,570 (the same size as v0.1.0's), `xmustard-relay` 611,712, and `LICENSE` (0644). `strings` finds no `/home/` path, user name or checkout path in any of the five binaries. The core's 116 dependency source paths now read `/cargo/registry/src/...`. *Reproducibility:* a second `make release` ran from a copy at another path, with another `CARGO_HOME` (a copy of the registry) and an empty Rust target directory (81 crates compiled). It produced a byte-identical archive (the same sha256) and identical binaries. Each build took 79–93 s. Built on this box, the core needs glibc 2.39 and the relay 2.34, because the box has glibc 2.43. The ubuntu-22.04 leg links against glibc 2.35, so its floor can be at most 2.35. That was not measured, because the box has no container runtime. The first dry run at 54907b5, before the path remap, is superseded. Its binaries held build-box paths, and its archive bytes changed with every build.
- *Homebrew formula.* The formula at branch time was no longer HEAD-only: main's 4ba4481 had pinned it to the v0.1.0 source tarball on every platform. A `stable do` block now gives every platform v0.1.0, checked against a sha256. macOS arm64 installs the prebuilt v0.1.0 archive, with the sha256 from the release's `SHA256SUMS` (downloaded and checked; it holds the five binaries under one top directory, which Homebrew enters). macOS Intel, Linux arm64 and Linux x86_64 build the tagged source tarball, whose sha256 `e4799036...` was checked by downloading it again. go and rust are build dependencies of that spec only. Linux x86_64 builds from source because v0.1.0's prebuilt Linux archive needs glibc 2.39 (core) and 2.34 (relay), so it would install and then fail to start on Ubuntu 22.04, Debian 12 or RHEL 9. It can move to the archive of a release that release.yml builds on ubuntu-22.04 (floor 2.35) when the owner bumps the formula. `head` builds main, with its own go and rust build dependencies. `install` builds whenever it finds `rust-core/Cargo.toml`, which the source tarball and HEAD have and the archives do not (they hold only the five binaries and LICENSE). The build is the base formula's: `cargo install --locked` through `std_cargo_args`, and `std_go_args` for the Go binaries, with cgo on, so source builds keep the PTY terminals. The source build was not run in this round: it compiles, which the Mac may not do, and the box has no Homebrew. The test block is unchanged; it passed against the darwin-arm64 archive on the Mac (core exit 2 with "usage"; `tools/list` names `ground` and `remember`). The formula was loaded with `Formulary.from_contents` under `Homebrew::SimulateSystem` (Homebrew 7.0.6) for the four os/arch pairs, and each gives stable 0.1.0. macOS arm64 gets the darwin-arm64 archive with no dependencies. macOS Intel, Linux x86_64 and Linux arm64 get the source tarball with go and rust as build dependencies. Head lists go and rust once on every pair. So `brew install xmustard` no longer stops in `install/check.rb`, which refuses a formula with no stable spec unless `--HEAD` is given. `brew style` reports no formula offense, only the Sorbet sigil and frozen-string cops that apply to files outside a tap; the base formula shows the same ones. It had asked for `version` inside the stable block, where it now is. The caveats ask for an absolute `XMUSTARD_DATA_DIR`, since the binaries default to `../backend/data`, which is relative to the working directory; the single-config fix belongs to WS-48. They also say the prebuilt binaries lack the platform profile's PTY terminals. The glibc caveat is gone, since no Linux platform installs a prebuilt binary.
- *Formatting (separate commits, no logic).* `gofmt -w` on the 10 files `gofmt -l api-go` listed; `git diff -w` shows only two removed blank lines. `rustfmt` (edition 2024) changed `scanner.rs` and `verification.rs`. `indexcache.rs` was already clean after WS-15 merged, so it is unchanged. `cargo fmt --check` and `gofmt -l api-go` are now clean.
- *Deviations from the spec.* (1) The untracking itself is not done: the packet waits for the owner, as the plan requires. Only the inert `.gitignore` rule is applied. (2) The formula was no longer HEAD-only at branch time (see above). Only macOS arm64 installs a prebuilt archive. The other platforms keep the base formula's checksummed source build: Linux x86_64 because of v0.1.0's glibc 2.39 floor, and macOS Intel and Linux arm64 because they have no archive. (3) `indexcache.rs` needed no rustfmt change. (4) Only the Linux archive was dry-run, because nothing may be compiled on the Mac. The darwin-arm64 leg is covered by `make -n release` under Make 3.81 and by the v0.1.0 darwin archive, which was built with the same flags. (5) The release is a draft that the owner publishes. The formula bump is a helper the owner runs after publishing (`packaging/homebrew/bump.sh`), not a workflow step: the formula lives in this repository, and the release job does not push. (6) The release toolchains are pinned (Go 1.26.1, Rust 1.93.1), while check.yml tests with go.mod's 1.26.0 and Rust stable. (7) Files outside the list: the formatting-only Go and Rust files above, `rust-core/tests/index_query.rs` (the Linux gate below) and the new `packaging/homebrew/bump.sh`. (8) The Linux leg's glibc floor on ubuntu-22.04 (at most 2.35) follows from the build glibc but was not measured. (9) Byte reproducibility was verified on one machine, with a different path, `CARGO_HOME` and target directory. Across machines it needs the same runner image, because the C compiler and system libraries also shape the bytes. (10) The Go binaries stay static (CGO off, as in v0.1.0), so the prebuilt binaries have no PTY terminals. The caveats say so.
- *Review round 1 (2026-09-28).* (Major) The formula was HEAD-only on macOS Intel and Linux arm64, where `brew install` refuses; the plan's claim that `determine_active_spec` falls back to head was wrong. Fixed as above; SimulateSystem shows stable 0.1.0 on all four pairs. (Minor) The glibc 2.39 floor: release.yml builds the Linux leg on ubuntu-22.04, and Linux x86_64 builds v0.1.0 from source. (Minor) Build paths and non-reproducible archives: the path remap and archive normalization above, verified byte-identical. (Minor) CGO off drops the PTY terminals: stated in the caveats, the Makefile comment and this record, and the binaries stay static. (Minor) The keep-data steps: the packet now stops the processes first and uses a new `mktemp -d` holding directory for each run. In a scratch clone it kept all 82 live files byte-identical, left `git status` clean and did not touch a leftover holding directory. (Minor) The cwd-relative data directory: a caveat line; the real fix belongs to WS-48.
- *Formula bump helper (resumed session).* `sh packaging/homebrew/bump.sh vX.Y.Z [formula]` needs only sh, curl, shasum, sed and awk. It validates the tag with release.yml's anchored regex and reads the current version and the homepage from the formula. It downloads the release's `SHA256SUMS` and the tagged source tarball, and hashes the tarball. Each `url` line that names the current tag moves to the new tag, and the `sha256` line under it gets that url's checksum; the `version` line moves too. The head url names no tag and is left alone. Which platforms take an archive stays as the formula says, and the script names the release archives the formula does not use (for v0.1.0, the Linux x86_64 archive). It refuses, leaving the formula unchanged, when no url names the current tag, a url has no checksum in the release, a url is not followed by its `sha256` line, a checksum line is malformed or a download fails. Otherwise it prints the diff and writes the formula. On the current tag it changes nothing, so rerunning it checks the formula against the release. Tested against the real v0.1.0 release on the Mac (BSD sed and awk) and on the build box (GNU sed 4.9, gawk 5.3.2). On the formula it reports no change. A copy set back to 0.0.9 with zeroed checksums came back identical to the formula below its header comment. It refused each of these: a url renamed to a platform the release lacks, a missing `sha256` line after the first or the third url, a tag without the `v`, a tag with a space or a newline after a valid tag (grep alone matches per line, so a character check runs first), an unknown tag (curl 404), and a formula whose urls had moved past its `version` line. The formula's header comment no longer names a version and points at the helper; `brew style` still reports only the three cops that apply to files outside a tap.
- *`make release-sums`.* The `SHA256SUMS` step now has one definition. `make release` runs it for the host's archive, and the publish job runs it after downloading both platforms' archives. It lists only `xmustard-<VERSION>-*` archives, sorted by name in the C locale, so older builds left in `dist/` stay out and the order does not depend on the runner's locale. Checked with scratch archives on the Mac: it wrote the two v9.9.9 lines and left a v9.9.8 archive out. It refused a tampered archive without writing `SHA256SUMS`, and it refused a version with no archives. The Linux dry run was repeated on the box, on the tree committed as 6a0f6dd, with the same `VERSION=v0.1.0-25-ga9689f8 SOURCE_DATE=202609280734.13`, and the archive came out byte-identical (`bf377ce4...`, as at a9689f8): no shipped Rust or Go source changed in between. `SHA256SUMS` held that one line. actionlint 1.7.12 (`go run` on the box) is clean on both workflows. shellcheck is still not installed, so the `run:` scripts are still not shellchecked.
- *RSS test gate.* `index_query::resident_rss_on_a_100k_symbol_resolved_graph_stays_within_the_line` now carries `#[cfg_attr(not(target_os = "linux"), ignore = ...)]`. Linux is the reference for the RSS lines, and macOS's allocator measures above them, so the test failed only there. The test still compiles everywhere, so its helpers stay in use and raise no dead-code warnings, and `--ignored` still runs it on macOS for measurement. On the build box it is listed as a normal test and passed (index_query: 8 passed, 0 ignored, 319 s in the debug profile). The skip on macOS was not observed, because nothing is compiled on the Mac.
- *Checks.* `xm-remote-check.sh ws-26 all -count=1` gave remote_exit=0 at 6607ea3. The run covered the Rust release build, all cargo tests, clippy at 6 warnings (the base count; none new), `go vet`, and all 14 Go packages. `actionlint` 1.7.12 is clean on the final workflows. Two earlier full runs each failed one test, `index_build::build_of_5000_files_has_no_file_cap_and_peaks_under_25_mib`, whose line is 25.0 MiB; a later run of that binary measured 25.2 MiB. That test already sits at the edge on the base. Five interleaved runs of the index_build binary each gave 24.5–24.8 MiB on base 845868d and 24.5–24.9 MiB on this branch, whose Rust changes are formatting only. The margin (about 0.2–0.5 MiB) belongs to the index-build owner (WS-07/WS-22); this branch does not change it. Review round 1: `xm-remote-check.sh ws-26 all -count=1` gave remote_exit=0 at 1424c27 (the Rust release build, 14 cargo test binaries, clippy at 6 warnings, `go vet`, and all 14 Go packages). The first full run of the round failed the same `index_build` RSS test, and cargo then stopped before the later test binaries. Four runs of that test alone on this branch measured 24.4–24.6 MiB. A second run was killed locally before it printed anything, and the third passed. `gofmt -l api-go` is clean, and actionlint 1.7.12 is clean on both workflows. Resumed session: `xm-remote-check.sh ws-26 all -count=1` gave remote_exit=0 on the tree of 5c6baba (the Rust release build, 14 cargo test binaries all passing, among them index_query with the RSS test run on Linux in 306 s and index_build's RSS test inside its line; clippy at 6 warnings, the base count; `go vet`; all 14 Go packages). An earlier full run in this session was cut off locally after the Rust half had passed (clippy 6) and is not counted. The C-locale sort (c3ee67b) came after it and touches only `release-sums`, which the Mac and the box each ran on scratch archives. `bump.sh` and `release-sums` were rechecked on the box with GNU tools after the hardening. `gofmt -l api-go` and `cargo fmt --check` are clean.

**Owner approval packet (WS-26 untracking).** Approve by merging parity/ws-26, then running the script once in the checkout that will carry the commit (normally main), reviewing `git status`, and committing. The script does not commit. The 80 tracked files; the script checks the sha256 of this exact `git ls-files -- backend/data` output, `69cdc113b9eb9739dc124b364af651ea3d19fe3dde57a856bfab82bb66f0e329`:

```text
backend/data/metrics/run_697b588ec115.json
backend/data/metrics/run_a84ff0803884.json
backend/data/settings.json
backend/data/workspaces.json
backend/data/workspaces/co-titan-0a54108278/activity.jsonl
backend/data/workspaces/co-titan-0a54108278/issue_overrides.json
backend/data/workspaces/co-titan-0a54108278/runs/run_32370bf2aaf8.json
backend/data/workspaces/co-titan-0a54108278/runs/run_32370bf2aaf8.log
backend/data/workspaces/co-titan-0a54108278/runs/run_32370bf2aaf8.out.json
backend/data/workspaces/co-titan-0a54108278/runs/run_40221c2d0946.json
backend/data/workspaces/co-titan-0a54108278/runs/run_40221c2d0946.log
backend/data/workspaces/co-titan-0a54108278/runs/run_40221c2d0946.out.json
backend/data/workspaces/co-titan-0a54108278/runs/run_5d272d8663dd.json
backend/data/workspaces/co-titan-0a54108278/runs/run_5d272d8663dd.log
backend/data/workspaces/co-titan-0a54108278/runs/run_66c68561e0f1.json
backend/data/workspaces/co-titan-0a54108278/runs/run_66c68561e0f1.log
backend/data/workspaces/co-titan-0a54108278/runs/run_66c68561e0f1.out.json
backend/data/workspaces/co-titan-0a54108278/runs/run_67ae394f1c46.json
backend/data/workspaces/co-titan-0a54108278/runs/run_67ae394f1c46.log
backend/data/workspaces/co-titan-0a54108278/runs/run_78afd02e4014.json
backend/data/workspaces/co-titan-0a54108278/runs/run_78afd02e4014.log
backend/data/workspaces/co-titan-0a54108278/runs/run_94383c0c8bb6.json
backend/data/workspaces/co-titan-0a54108278/runs/run_94383c0c8bb6.log
backend/data/workspaces/co-titan-0a54108278/runs/run_94383c0c8bb6.out.json
backend/data/workspaces/co-titan-0a54108278/runs/run_9b4f78ace92d.json
backend/data/workspaces/co-titan-0a54108278/runs/run_9b4f78ace92d.log
backend/data/workspaces/co-titan-0a54108278/runs/run_a19e15e8fd2d.json
backend/data/workspaces/co-titan-0a54108278/runs/run_a19e15e8fd2d.log
backend/data/workspaces/co-titan-0a54108278/runs/run_b1264cb8286c.json
backend/data/workspaces/co-titan-0a54108278/runs/run_b1264cb8286c.log
backend/data/workspaces/co-titan-0a54108278/runs/run_b1264cb8286c.out.json
backend/data/workspaces/co-titan-0a54108278/runs/run_b30bc80f20a7.json
backend/data/workspaces/co-titan-0a54108278/runs/run_b30bc80f20a7.log
backend/data/workspaces/co-titan-0a54108278/runs/run_b874350b2178.json
backend/data/workspaces/co-titan-0a54108278/runs/run_b874350b2178.log
backend/data/workspaces/co-titan-0a54108278/runs/run_cf0f12a1f443.json
backend/data/workspaces/co-titan-0a54108278/runs/run_cf0f12a1f443.log
backend/data/workspaces/co-titan-0a54108278/runs/run_ea4bf722c9b2.json
backend/data/workspaces/co-titan-0a54108278/runs/run_ea4bf722c9b2.json.ba5dfb6a4e2f43488feb1407694db46d.tmp
backend/data/workspaces/co-titan-0a54108278/runs/run_ea4bf722c9b2.log
backend/data/workspaces/co-titan-0a54108278/runs/run_f3d9bfe1d3da.json
backend/data/workspaces/co-titan-0a54108278/runs/run_f3d9bfe1d3da.log
backend/data/workspaces/co-titan-0a54108278/runs/run_f3f97e5aca1c.json
backend/data/workspaces/co-titan-0a54108278/runs/run_f3f97e5aca1c.log
backend/data/workspaces/co-titan-0a54108278/runs/run_f3f97e5aca1c.out.json
backend/data/workspaces/co-titan-0a54108278/runs/run_ff4a98496e93.json
backend/data/workspaces/co-titan-0a54108278/runs/run_ff4a98496e93.log
backend/data/workspaces/co-titan-0a54108278/saved_views.json
backend/data/workspaces/co-titan-0a54108278/snapshot.json
backend/data/workspaces/co-titan-0a54108278/terminals/term_8b530a4ecfc6.log
backend/data/workspaces/co-titan-0a54108278/terminals/term_d3a76fb1df5c.log
backend/data/workspaces/repo-2d5d3dd8af/activity.jsonl
backend/data/workspaces/repo-2d5d3dd8af/fix_records.json
backend/data/workspaces/repo-2d5d3dd8af/issue_overrides.json
backend/data/workspaces/repo-2d5d3dd8af/runs/run_smoke_fix.json
backend/data/workspaces/repo-2d5d3dd8af/runs/run_smoke_fix.log
backend/data/workspaces/repo-2d5d3dd8af/snapshot.json
backend/data/workspaces/repo-2d5d3dd8af/tracker_issues.json
backend/data/workspaces/repo-30ecdc5685/activity.jsonl
backend/data/workspaces/repo-30ecdc5685/fix_records.json
backend/data/workspaces/repo-30ecdc5685/issue_overrides.json
backend/data/workspaces/repo-30ecdc5685/runs/run_smoke_fix.json
backend/data/workspaces/repo-30ecdc5685/runs/run_smoke_fix.log
backend/data/workspaces/repo-30ecdc5685/snapshot.json
backend/data/workspaces/repo-30ecdc5685/tracker_issues.json
backend/data/workspaces/repo-4d73ffdc46/activity.jsonl
backend/data/workspaces/repo-4d73ffdc46/fix_records.json
backend/data/workspaces/repo-4d73ffdc46/issue_overrides.json
backend/data/workspaces/repo-4d73ffdc46/snapshot.json
backend/data/workspaces/repo-4d73ffdc46/tracker_issues.json
backend/data/workspaces/repo-cb6493e598/activity.jsonl
backend/data/workspaces/repo-cb6493e598/snapshot.json
backend/data/workspaces/repo-cb6493e598/tracker_issues.json
backend/data/workspaces/repo-d56ef7ff29/activity.jsonl
backend/data/workspaces/repo-d56ef7ff29/fix_records.json
backend/data/workspaces/repo-d56ef7ff29/issue_overrides.json
backend/data/workspaces/repo-d56ef7ff29/runs/run_smoke_fix.json
backend/data/workspaces/repo-d56ef7ff29/runs/run_smoke_fix.log
backend/data/workspaces/repo-d56ef7ff29/snapshot.json
backend/data/workspaces/repo-d56ef7ff29/tracker_issues.json
```

The script (run it with `sh`; it needs git and shasum):

```sh
#!/bin/sh
# WS-26: stop tracking the runtime files under backend/data. Run it in the checkout that
# will carry the commit, after the owner approves. git rm --cached changes the index only:
# every file stays on disk, and the backend/data/ rule in .gitignore keeps them out of
# later commits. It refuses unless the tracked set is exactly the recorded list.
set -eu
cd "$(git rev-parse --show-toplevel)"

expected_sha=69cdc113b9eb9739dc124b364af651ea3d19fe3dde57a856bfab82bb66f0e329 # of the 80-path list
actual_sha=$(git ls-files -- backend/data | shasum -a 256 | cut -d ' ' -f 1)
if [ "$actual_sha" != "$expected_sha" ]; then
  echo "refusing: the tracked backend/data files differ from the WS-26 list (git ls-files -- backend/data)" >&2
  exit 1
fi
if ! git check-ignore -q --no-index backend/data/settings.json; then
  echo "refusing: .gitignore does not ignore backend/data/ (merge parity/ws-26 first)" >&2
  exit 1
fi
if ! git diff --cached --quiet; then
  echo "refusing: the index already has staged changes; commit or unstage them first" >&2
  exit 1
fi

git rm -r --cached --quiet -- backend/data

# Nothing under backend/data stays tracked, all 80 paths are staged as deletions, and no
# runtime file shows up as untracked.
test -z "$(git ls-files -- backend/data)"
test "$(git diff --cached --name-only --diff-filter=D -- backend/data | wc -l | tr -d ' ')" = 80
if git status --porcelain --untracked-files=all -- backend/data | grep -q '^??'; then
  echo "unexpected: untracked files under backend/data; check .gitignore" >&2
  exit 1
fi
echo "80 files leave the index and stay on disk. Review 'git status', then commit:"
echo "  git commit -m 'chore(data): stop tracking backend/data runtime files (WS-26)'"
```

In every other checkout or worktree whose `backend/data` holds runtime data you want to keep, do this before pulling the untrack commit. Git deletes files that a pulled commit stops tracking, and it refuses the pull while tracked copies differ from HEAD. First stop `xmustard-api`, `xmustard-mcp`, `xmustard-ops` and anything else that writes to this checkout's `backend/data`: the API writes by path and creates missing directories, so a write after the `mv` lands in the restored copy and is lost. Then run this with `sh` from the checkout root:

```sh
set -eu
keep=$(mktemp -d ../xmustard-backend-data.XXXXXX)   # a new directory for each run
echo "live data held in $keep/data until this finishes"
mv backend/data "$keep/data"                        # move the live data out of git's way
git checkout HEAD -- backend/data                   # restore the tracked copies, unmodified
git pull                                            # the untrack commit deletes those copies
rm -rf backend/data && mv "$keep/data" backend/data && rmdir "$keep"
```

`mktemp -d` makes a new holding directory beside the checkout (normally the same filesystem, so `mv` renames), and worktrees that share a parent directory never reuse one. If a step fails, `set -e` stops the run and the data stays in the printed directory.

### WS-27 — Dedupe, code anchors, tiered conflicts and structured claims

**Goal.** At propose time:
- Normalize, then exact hash, then an entropy gate, then MinHash/LSH (a Graphiti dedup_helpers port with the Apache NOTICE). Duplicates become corroboration or possible_duplicates.
- Extract anchors: paths, identifiers resolved through the index, commands, config keys, error codes. Maintain an anchor inverted index with a damped boost.
- Tiered conflict candidates: claim, suspected contradiction, path overlap.
- Optional structured claims with a fixed predicate vocabulary and typed memory relations.
- Return suggested_supersedes so the agent can act.

**Requirements.** PAR-GOV-03, PAR-RCL-02, PAR-GOV-10, PAR-GOV-11, PAR-GOV-21

**Files.** `api-go/internal/workspaceops/memory_dedupe.go`, `api-go/internal/workspaceops/memory_anchors.go`, `api-go/internal/workspaceops/memory_conflicts.go`, `api-go/internal/workspaceops/memory_claims.go`, `api-go/internal/workspaceops/memory_propose.go`, `api-go/internal/govstore/anchors.go`, `api-go/internal/mcpserver/tool_remember.go`, `api-go/internal/workspaceops/memory_dedupe_test.go`, `api-go/internal/workspaceops/memory_anchors_test.go`  
**New modules.** `api-go/internal/workspaceops/memory_dedupe.go`, `api-go/internal/workspaceops/memory_anchors.go`, `api-go/internal/workspaceops/memory_conflicts.go`, `api-go/internal/workspaceops/memory_claims.go`

**Tests required.**
- An exact duplicate of a promoted memory becomes corroborated; near duplicates are returned with similarity
- The entropy gate prevents false merges of short generic text
- Anchors resolve symbols through the worker; a high-fan-out anchor is damped
- Claim conflict: same subject and predicate with a different object is flagged
- T2 contradiction on numbers, versions and commands; T3 path overlap retained and labeled
- Dedupe signatures stay under 1 KB per memory

**Acceptance.** remember returns created or corroborated, possible_duplicates, possible_conflicts, anchors and suggested_supersedes. The lifecycle suite fixtures for duplicates and contradictions pass.

**Collision risk.** memory_propose.go is owned by WS-19, so this is a sequential follow-up. The per-tool mcpserver file tool_remember.go is shared with WS-19.

**Correction.** Critic: MinHash/LSH bands and anchor postings stay in SQLite; per-principal pending quotas; RSS test at 50k memories.

### WS-28 — Change-driven invalidation, as_of, graph-proximity recall and memories on explain/impact

**Goal.** Watcher and HEAD changes intersect the anchor index. The system re-hashes only affected anchors, at symbol-body granularity, and persists stale_observed events and needs_reverify. ground shows at_risk_memories. Add bi-temporal valid_from/invalidated_at and recall as_of. Add recall seed=<symbol> ranking by graph distance, with bounded MMR diversity. Attach anchored memories to explain and impact results. Add refs_stale.

**Requirements.** PAR-PROV-03, PAR-PROV-02, PAR-RCL-03, PAR-SYM-04, PAR-RCL-09

**Files.** `api-go/internal/workspaceops/memory_invalidation.go`, `api-go/internal/workspaceops/memory_temporal.go`, `api-go/internal/workspaceops/memory_recall.go`, `api-go/internal/workspaceops/grounding_memory.go`, `api-go/internal/workspaceops/workspace_reads.go`, `api-go/internal/rustcore/worker.go`, `api-go/internal/mcpserver/tool_recall.go`, `api-go/internal/workspaceops/memory_invalidation_test.go`  
**New modules.** `api-go/internal/workspaceops/memory_invalidation.go`, `api-go/internal/workspaceops/memory_temporal.go`

**Tests required.**
- An edit to an anchored symbol body marks the memory at risk; an unrelated edit in the same file does not
- stale_observed events are persisted with commit and anchors
- as_of at an older commit returns the facts valid then (ancestry check cached)
- seed ranking follows graph distance; MMR runs over ≤100 candidates
- explain and impact include memories with trust and stale labels

**Acceptance.** ground lists memories affected by the caller's changes before they act, and recall supports as_of and seed. Tests are green.

**Collision risk.** memory_recall.go (WS-20) and grounding_memory.go (WS-20, WS-31, WS-33); sequence after WS-20. workspace_reads.go holds the explain wiring.

### WS-29 — Memory scopes and shared collections with grants

**Goal.** Add private, dir, run and global scopes derived from the principal, and shared collections with grants: source, target, permission (rw or ro), granted_by, and a per-collection policy. Recall gives per-target applicability (verified_here, foreign_unchecked, stale) and merges scopes with labels. Add xmustard-ops shared list/create/grant/revoke/history.

**Requirements.** PAR-SHARE-01, PAR-SHARE-02

**Files.** `api-go/internal/workspaceops/memory_scopes.go`, `api-go/internal/workspaceops/memory_grants.go`, `api-go/internal/govstore/grants.go`, `api-go/cmd/xmustard-api/grant_routes.go`, `api-go/cmd/xmustard-api/main.go`, `api-go/cmd/xmustard-ops/main.go`, `api-go/internal/mcpserver/tool_recall.go`, `api-go/internal/mcpserver/tool_remember.go`, `api-go/internal/workspaceops/memory_scopes_test.go`  
**New modules.** `api-go/internal/workspaceops/memory_scopes.go`, `api-go/internal/workspaceops/memory_grants.go`, `api-go/cmd/xmustard-api/grant_routes.go`

**Tests required.**
- A private memory is visible to others only as unverified and self-promotes for its owner
- Scope can never be set from metadata (identity smuggling test)
- A granted collection appears in the target with applicability foreign_unchecked until rechecked there
- A read-only grant rejects writes; revoking a grant hides the collection
- Scope leakage is 0 across the fixture suite

**Acceptance.** Cross-repository authorized memory sharing (vision workflow 3) works end to end with labels, and the lifecycle suite scope-leak metric is 0.

**Collision risk.** main.go gets one line. xmustard-ops main.go is shared with WS-32, WS-34 and WS-44 (add subcommand files, not inline code). The per-tool mcpserver files are shared.

### WS-30 — Cross-workspace code query, residency LRU, worktree identity and path rewriting

**Goal.** search, explain and impact accept workspaces or a group. The resident service holds at most K hot indexes (default 1) with LRU and idle eviction; cold repos are served by pread or loaded on demand in the heavy slot. Detect linked worktrees and record branch/worktree identity. Store anchors repo-relative, with a longest-prefix path rewrite map for foreign absolute paths.

**Requirements.** PAR-SHARE-03, PAR-RT-10, PAR-SHARE-04, PAR-SHARE-05

**Files.** `rust-core/src/serve.rs`, `rust-core/src/index/residency.rs`, `api-go/internal/workspaceops/workspace_registry.go`, `api-go/internal/workspaceops/path_rewrite.go`, `api-go/internal/mcpserver/tool_search.go`, `api-go/internal/mcpserver/tool_explain.go`, `api-go/internal/mcpserver/tool_impact.go`, `rust-core/tests/residency.rs`, `api-go/internal/workspaceops/path_rewrite_test.go`  
**New modules.** `rust-core/src/index/residency.rs`, `api-go/internal/workspaceops/path_rewrite.go`

**Tests required.**
- A second hot repo evicts the least recent one under K=1; the RSS bound holds (gate v2)
- Cross-workspace search applies per-repo candidate caps and reports per-repo freshness
- Worktree detection gives distinct identity per worktree
- Foreign absolute paths rewrite correctly; unmatched paths error clearly

**Acceptance.** Multi-repo queries work within budget under the default residency, and gate v2 with 2 repos is attached.

**Collision risk.** serve.rs (WS-02, WS-14, WS-15). workspace_registry.go (WS-03).

### WS-31 — Tiered memory, write policy, revision-pinned injection, guidance artifacts and onboarding

**Goal.** Deliver the following:
- Core and deferred tiers, with a core character budget and governed tier changes.
- Write-policy validation: schema, size caps, protected fields and patterns that need human-approver, and an admin-only policy config.
- initialize.instructions with a budgeted core projection plus a deferred index, compiled from promoted state and cached by revision.
- ground(since_revision) deltas and the SessionStart renderer.
- Marker-bounded AGENTS.md/CLAUDE.md block writer with original prose.
- MCP prompts (pre_commit_check, orient).
- Onboarding checklist for empty workspaces.
- Memory token report.

**Requirements.** PAR-GOV-08, PAR-GOV-07, PAR-HAR-03, PAR-HAR-11, PAR-GOV-15, PAR-CTX-13, PAR-SEC-02, PAR-SEC-06

**Files.** `api-go/internal/workspaceops/memory_tiers.go`, `api-go/internal/workspaceops/memory_policy.go`, `api-go/internal/workspaceops/grounding_memory.go`, `api-go/internal/mcpserver/instructions.go`, `api-go/internal/mcpserver/prompts.go`, `api-go/internal/hooks/claude.go`, `api-go/cmd/xmustard-ops/guidance.go`, `api-go/cmd/xmustard-ops/policy.go`, `api-go/internal/workspaceops/memory_tiers_test.go`  
**New modules.** `api-go/internal/workspaceops/memory_tiers.go`, `api-go/internal/workspaceops/memory_policy.go`, `api-go/internal/mcpserver/prompts.go`, `api-go/cmd/xmustard-ops/guidance.go`

**Tests required.**
- The core projection respects the character budget and uses only promoted entries
- since_revision returns only added, changed, superseded and stale entries
- A policy violation returns structured errors; a protected memory requires human-approver
- Policy config changes require admin plus human approval
- The AGENTS.md block writer is idempotent with line-anchored markers and never matches inline mentions
- Onboarding appears only when there are no promoted memories

**Acceptance.** Agents receive standing conventions without calling recall. Injection is revision-pinned and budgeted. Policy is enforced server-side.

**Collision risk.** grounding_memory.go (WS-20, WS-28, WS-33). hooks/claude.go (WS-23). mcpserver/instructions.go (WS-04).

**Correction.** Critic: SEC-02 stays in WS-09 and SEC-06 in WS-44; add path-scoped client rule-file export (WS-62).

### WS-32 — Consolidation work queue, memory health, skills export and Markdown/git export-import

**Goal.** Deliver the following:
- Deterministic consolidation jobs: dedupe clusters, stale and orphan anchors, supersession chains, ageing proposals, session digests, init seeds from AGENTS.md. Each has a per-source watermark (advanced on success) and CAS leases with a conflict signature.
- ground consolidation_jobs and a memory_health block with an advisory next_action.
- Procedures exported as skills with a provenance header.
- Markdown/JSONL export and import, plus an optional git mirror. Imports land pending/foreign.

**Requirements.** PAR-GOV-13, PAR-SHARE-08, PAR-GOV-17, PAR-GOV-18, PAR-GOV-14

**Files.** `api-go/internal/workspaceops/memory_jobs.go`, `api-go/internal/workspaceops/memory_health.go`, `api-go/internal/govstore/jobs.go`, `api-go/internal/workspaceops/grounding_memory.go`, `api-go/cmd/xmustard-ops/memories.go`, `api-go/cmd/xmustard-ops/skills.go`, `api-go/internal/mcpserver/tool_remember.go`, `api-go/internal/workspaceops/memory_jobs_test.go`  
**New modules.** `api-go/internal/workspaceops/memory_jobs.go`, `api-go/internal/workspaceops/memory_health.go`, `api-go/cmd/xmustard-ops/memories.go`, `api-go/cmd/xmustard-ops/skills.go`

**Tests required.**
- Jobs are idempotent; the watermark advances only on success; a crashed lease is retried and a finished-failed lease is reported
- remember(job_id) attributes proposals; promotion still requires distinct-principal verify
- Export/import round-trips with provenance; imports land pending
- The git mirror refuses reset when dirty and keeps a backup ref
- Skills export includes path hashes and flags stale procedures

**Acceptance.** The consolidation loop runs end to end with an external agent doing the reasoning, and no memory is auto-promoted.

**Collision risk.** grounding_memory.go, the xmustard-ops subcommand files, and tool_remember.go (WS-19, WS-27).

### WS-33 — Session ledger, compaction snapshot and restore, handoff capsule and cross-client handoff

**Goal.** Deliver the following:
- A durable session ledger per workspace, client, session and principal: tool calls with digest, handle and status; files read and modified; decisions; errors; compactions.
- A PreCompact ≤2 KB priority-tiered snapshot, re-injected via SessionStart source=compact.
- remember(kind=handoff) with a schema-checked capsule and path hashes.
- ground session_handoff, continuity and handoffs.
- Deterministic candidate-memory mining at Stop/SessionEnd.

**Requirements.** PAR-PROV-06, PAR-HAR-04, PAR-HAR-05, PAR-PROV-08, PAR-PROV-07, PAR-GOV-14

**Files.** `api-go/internal/workspaceops/session_ledger.go`, `api-go/internal/workspaceops/session_snapshot.go`, `api-go/internal/workspaceops/handoff_capsule.go`, `api-go/internal/workspaceops/grounding_session.go`, `api-go/internal/govstore/sessions.go`, `api-go/internal/hooks/claude.go`, `api-go/internal/mcpserver/tool_remember.go`, `api-go/internal/workspaceops/session_ledger_test.go`  
**New modules.** `api-go/internal/workspaceops/session_ledger.go`, `api-go/internal/workspaceops/session_snapshot.go`, `api-go/internal/workspaceops/handoff_capsule.go` (`grounding_session.go` already exists: WS-03 created it with GroundingUnknown and the summary; modify it)

**Tests required.**
- The snapshot is ≤2 KB and prioritized; handles in it are expandable
- SessionStart source=compact re-injects the snapshot
- The handoff capsule schema is validated; stale paths are flagged on read
- A new session in a different client receives the prior snapshot and pending proposals
- Candidate mining produces pending candidates only
- Ledger retention and compaction bounds hold

**Acceptance.** Continuity survives client compaction and client switches, backed by evidence.

**Collision risk.** hooks/claude.go (WS-23, WS-31) and tool_remember.go. grounding_session.go was created by WS-03 (GroundingUnknown, summarize).

**Correction.** Critic: SessionEnd/Stop work is async (Claude Code gives SessionEnd hooks a shared 1.5 s budget).

### WS-34 — Session transcript store, session search, new-session bootstrap and cross-harness import

**Goal.** Deliver the following:
- An immutable transcript/session event store with FTS5, labeled as raw observations.
- recall(source=sessions) with date, agent and conversation filters and cursor expansion.
- ground recent and relevant sessions, filtered by RRF ≥0.012 and ≥0.93 of the top score, labeled not confirmed.
- Streaming importers for Claude Code, Codex, OpenCode and Pi session stores, via xmustard-ops import sessions.

**Requirements.** PAR-RCL-07, PAR-RCL-08, PAR-ADP-12, PAR-SEC-04

**Files.** `api-go/internal/sessionimport/claude.go`, `api-go/internal/sessionimport/codex.go`, `api-go/internal/sessionimport/opencode.go`, `api-go/internal/sessionimport/pi.go`, `api-go/internal/sessionimport/import_test.go`, `api-go/internal/workspaceops/transcripts.go`, `api-go/internal/workspaceops/grounding_session.go`, `api-go/internal/mcpserver/tool_recall.go`, `api-go/cmd/xmustard-ops/import_sessions.go`  
**New modules.** `api-go/internal/sessionimport`, `api-go/internal/workspaceops/transcripts.go`, `api-go/cmd/xmustard-ops/import_sessions.go`

**Tests required.**
- Importers stream line by line with bounded memory (a 100 MB fixture stays under a fixed allocation)
- Redaction is applied to imported content
- Search returns excerpts with handles; cursor expansion works
- The bootstrap thresholds and label are applied
- Imported content never appears as verified memory

**Acceptance.** Past sessions from existing agents are searchable and cited by consolidation jobs. Transient import RSS is recorded.

**Collision risk.** grounding_session.go (created by WS-03, extended by WS-33) and tool_recall.go (WS-20, WS-28, WS-29).

### WS-35 — Impact v2: tiers, risk, epistemic envelope, typed filters, trace caps, diff-to-symbol and import cycles

**Goal.** Over the resolved graph, add:
- direction, max_depth, relation_types, min_confidence and include_tests; class seeding.
- by_depth tiers, risk scoring (CRITICAL/HIGH/MEDIUM/LOW/UNKNOWN with risk_note) and per-depth pagination.
- The epistemic envelope, trace with per-hop metadata and caps, and the furthest-node diagnosis.
- detect_changes scopes (unstaged/staged/all/compare, base/head, worktree) with pinned git flags and re-parsed current spans including untracked files.
- Bounded Tarjan+Johnson import-cycle checks surfaced in impact/ground until diagnostics is handed off.

**Requirements.** PAR-IMP-04, PAR-IMP-05, PAR-IMP-06, PAR-IMP-07, PAR-IMP-08, PAR-FRESH-10

**Files.** `rust-core/src/index/impact.rs`, `rust-core/src/index/trace.rs`, `rust-core/src/index/detect_changes.rs`, `rust-core/src/index/cycles.rs`, `rust-core/src/serve.rs`, `api-go/internal/mcpserver/tool_impact.go`, `api-go/internal/rustcore/worker.go`, `rust-core/tests/impact_v2.rs`  
**New modules.** `rust-core/src/index/impact.rs`, `rust-core/src/index/trace.rs`, `rust-core/src/index/detect_changes.rs`, `rust-core/src/index/cycles.rs`

**Tests required.**
- Risk thresholds are covered by table tests; UNKNOWN is returned on zero upstream callers
- Pagination per depth is stable; summary_only counts match
- Pinned git flags produce parsed files under a hostile user git config (quotePath, external diff, color)
- Untracked files are included; spans are current after edits
- Trace caps and the furthest node are reported; cycle enumeration truncates to one representative per SCC with componentCount stable
- contract_break is preserved

**Acceptance.** impact reaches parity with GitNexus impact, detect_changes and trace on fixtures, with honest completeness labels.

**Collision risk.** serve.rs (shared) and tool_impact.go (WS-36 adds refs ops; coordinate). Does not touch diagnostics*.go.

### WS-36 — Symbol navigation surfaces and staged degradation contract

**Goal.** Deliver the following:
- search mode=symbol with the name-path grammar (absolute, relative, overload index), include_body sliced from disk, include_info, depth children, max_matches with a refine map, and the ambiguity envelope with uids.
- explain file/dir overview with depth and staged fallback, plus symbol 360 context with chain_depth.
- impact op=refs|definition|implementations with the containing symbol and snippet.
- The max_chars/limit/cursor degradation ladders on search, explain and impact.

**Requirements.** PAR-SYM-02, PAR-SYM-03, PAR-SYM-04, PAR-IMP-03, PAR-CTX-05

**Files.** `rust-core/src/index/namepath.rs`, `rust-core/src/index/overview.rs`, `rust-core/src/index/context.rs`, `rust-core/src/index/refs.rs`, `rust-core/src/serve.rs`, `api-go/internal/mcpserver/tool_search.go`, `api-go/internal/mcpserver/tool_explain.go`, `api-go/internal/mcpserver/tool_impact.go`, `api-go/internal/mcpserver/degrade.go`, `rust-core/tests/navigation.rs`  
**New modules.** `rust-core/src/index/namepath.rs`, `rust-core/src/index/overview.rs`, `rust-core/src/index/context.rs`, `rust-core/src/index/refs.rs`, `api-go/internal/mcpserver/degrade.go`

**Tests required.**
- Name-path grammar fixtures including overloads and substring matching
- Bodies are sliced from the working tree, not stored
- Ambiguous names return ranked candidates with totalCandidates
- Each degradation stage is reached and named at shrinking max_chars
- refs include the containing symbol and a ±1 snippet with provenance

**Acceptance.** Serena-style symbolic reads are available through the existing tools with bounded outputs.

**Collision risk.** The per-tool mcpserver files search, explain and impact are shared with WS-18, WS-35 and WS-30. serve.rs.

### WS-37 — Static-embedding semantic lane with binary/int8 vector index

**Goal.** Deliver the following:
- A lean static-embedding loader: mmap safetensors, int8, compact WordPiece in a sorted array or FST.
- Vectors precomputed at index time for changed chunks.
- A 1-bit prefilter plus int8 rerank via pread, with HNSW only below a threshold.
- Fusion as a semantic lane with a semantic_model label.
- An optional dense lane for memory recall.
- Default-on only if the retrieval gate improves concept queries without regressions.

**Requirements.** PAR-RET-03, PAR-RET-04, PAR-RCL-01

**Files.** `rust-core/Cargo.toml`, `rust-core/src/index/embed_static.rs`, `rust-core/src/index/tokenizer_wordpiece.rs`, `rust-core/src/index/vectors.rs`, `rust-core/src/search.rs`, `rust-core/src/semantic.rs`, `rust-core/tests/embed_static.rs`, `scripts/bench/gold`  
**New modules.** `rust-core/src/index/embed_static.rs`, `rust-core/src/index/tokenizer_wordpiece.rs`, `rust-core/src/index/vectors.rs`

**Tests required.**
- Tokenizer parity with the reference tokenizer on a fixture vocabulary
- Embeddings match the model2vec reference within tolerance
- Binary prefilter recall@200 then int8 rerank quality on a fixture
- RSS: loaded lane ≤10 MiB peak (gate v2); no whole-file read into f32
- Retrieval gate on concept queries (authenticate vs login)

**Acceptance.** An honest semantic lane is available within budget, and the default-on decision is recorded with gate evidence.

**Collision risk.** search.rs (WS-14, WS-18) and Cargo.toml (WS-15, WS-16).

### WS-38 — Functional areas, execution flows, orientation packet and flow-grouped search

**Goal.** Deliver the following:
- Seeded deterministic communities over resolved edges, with large-graph mode, folder labels and sampled cohesion.
- Entry-point scoring and bounded DFS flows with truncation stats.
- ground orientation from the scanner.
- search group_by=flow.
- MCP resources for areas and flows.
- explain area and flow membership.

**Requirements.** PAR-SYM-08, PAR-SYM-09, PAR-SYM-10, PAR-RET-06, PAR-HAR-11, PAR-GOV-20

**Files.** `rust-core/src/index/areas.rs`, `rust-core/src/index/flows.rs`, `rust-core/src/index/orientation.rs`, `rust-core/src/serve.rs`, `api-go/internal/mcpserver/resources.go`, `api-go/internal/mcpserver/tool_explain.go`, `api-go/internal/mcpserver/tool_search.go`, `api-go/internal/workspaceops/grounding_index.go`, `rust-core/tests/areas_flows.rs`  
**New modules.** `rust-core/src/index/areas.rs`, `rust-core/src/index/flows.rs`, `rust-core/src/index/orientation.rs`, `api-go/internal/mcpserver/resources.go`

**Tests required.**
- Deterministic output across runs (seeded RNG); incremental results equal a full rebuild
- Large-graph mode triggers above 10k symbols
- Flow caps and truncation stats
- Orientation lists are bounded
- Resources list and read correctly

**Acceptance.** Orientation and flow context are available through explain, ground and resources, with worker transients inside the heavy slot.

**Collision risk.** The per-tool mcpserver files explain and search, and grounding_index.go (WS-03, WS-22).

### WS-39 — Resolvers for Rust, Python and Java

**Goal.** Extend scope/import resolution to Rust (use paths, impl blocks, traits), Python (namespace imports, MRO c3) and Java (packages, inheritance, interfaces), with the same confidence tiers, provenance and drop counters.

**Requirements.** PAR-IMP-01, PAR-IMP-05

**Files.** `rust-core/src/index/resolve/imports_rust.rs`, `rust-core/src/index/resolve/imports_python.rs`, `rust-core/src/index/resolve/imports_java.rs`, `rust-core/src/index/resolve/mro.rs`, `rust-core/src/index/resolve/mod.rs`, `rust-core/tests/resolve_more_langs.rs`  
**New modules.** `rust-core/src/index/resolve/imports_rust.rs`, `rust-core/src/index/resolve/imports_python.rs`, `rust-core/src/index/resolve/imports_java.rs`, `rust-core/src/index/resolve/mro.rs`

**Tests required.**
- Per-language resolution fixtures including trait impls, relative imports and overloads
- MRO c3 fixtures
- Precision against rust-analyzer, pyright or jdtls ground truth on small fixtures, recorded

**Acceptance.** Resolved edges are available for 5 languages, with precision numbers recorded for EVAL-09.

**Collision risk.** resolve/mod.rs is from WS-17. Otherwise none.

### WS-40 — Codex, OpenCode and Cursor adapters, Letta recipe and adapter capability registry

**Goal.** Deliver the following:
- Codex hooks: PostToolUse decision:block projection feedback; version-gated PreToolUse updatedInput; mcp_tool handlers; SessionStart and UserPromptSubmit; PreCompact and PostCompact.
- An OpenCode TS plugin: tool.execute.after output mutation, messages.transform masking, session.compacting, system.transform, tool.definition.
- Cursor hooks: updated_mcp_tool_output; preToolUse updated_input command wrapping; sessionStart; preCompact.
- A Letta Code recipe with a letta profile.
- A capability registry surfaced in ground delivery_capabilities.

**Requirements.** PAR-ADP-07, PAR-ADP-08, PAR-ADP-10, PAR-ADP-11, PAR-ADP-14, PAR-CTX-03, PAR-CTX-07

**Files.** `api-go/internal/hooks/codex.go`, `api-go/internal/hooks/cursor.go`, `api-go/internal/hooks/opencode.go`, `api-go/cmd/xmustard-api/hooks_routes.go`, `api-go/internal/adapters/capabilities.go`, `integrations/codex/hooks.json`, `integrations/codex/README.md`, `integrations/opencode/package.json`, `integrations/opencode/src/index.ts`, `integrations/opencode/test/plugin.test.ts`, `integrations/cursor/hooks.json`, `integrations/cursor/README.md`, `integrations/letta/README.md`, `api-go/internal/hooks/adapters_test.go`  
**New modules.** `integrations/codex`, `integrations/opencode`, `integrations/cursor`, `integrations/letta`, `api-go/internal/adapters`

**Tests required.**
- Recorded payload fixtures per client and version; golden outputs
- Codex block-feedback carries the projection plus handle; the updatedInput version gate is honored
- OpenCode mutated output is returned; masking stubs carry handles
- Cursor MCP output replacement; shell wrapping via updated_input
- The capability registry returns explicit unsupported errors

**Acceptance.** The four clients are supported with documented seams and pinned versions. Capability truth is exposed in ground.

**Collision risk.** hooks_routes.go (WS-23) and the hooks package files (WS-31, WS-33 edit claude.go only).

**Correction.** Critic: split into WS-40a Codex, WS-40b OpenCode (TS), WS-40c Cursor (Rust command hook client <=2 MiB), WS-40d Letta recipe + capability registry (records Codex additionalContextLimit 2,500 tokens, Cursor failClosed).

### WS-41 — Command-wrapper reducer, retention classes and upstream references

**Goal.** Add `xmustard-core run -- <argv>` (RTK pattern): it streams child output to the spool with a bounded tail, preserves exit code and signals, and prints the projection plus a handle. Add retention classes (hook-captured, own outputs, snapshots) with separate TTLs and quotas, and hashed upstream references when the client already persisted the original. A full quota means bounded passthrough, never eviction of a promised original.

**Requirements.** PAR-CTX-10, PAR-CTX-09, PAR-STORE-04

**Files.** `rust-core/src/run_wrapper.rs`, `rust-core/src/bin/xmustard-core.rs`, `api-go/internal/evidence/retention.go`, `api-go/internal/evidence/upstream_ref.go`, `api-go/internal/evidence/store.go`, `api-go/cmd/xmustard-api/evidence_routes.go`, `rust-core/tests/run_wrapper.rs`, `api-go/internal/evidence/retention_test.go`  
**New modules.** `rust-core/src/run_wrapper.rs`, `api-go/internal/evidence/retention.go`, `api-go/internal/evidence/upstream_ref.go`

**Tests required.**
- The wrapper preserves the exit code, signals and stdout/stderr ordering with bounded memory on 100 MB output
- The wrapper fails open if the API is unreachable (passthrough with a notice)
- Retention classes expire independently; upstream-reference hash verification streams
- Quota-full behavior never evicts unexpired originals

**Acceptance.** Shell reduction works in clients without result replacement, and evidence disk use stays bounded under universal capture.

**Collision risk.** evidence/store.go and evidence_routes.go (WS-03). bin/xmustard-core.rs (dispatch table).

### WS-42 — Repeat guard, revision-keyed result cache and recoverable history compaction library

**Goal.** Deliver the following:
- An identity-bound per-session repeat guard keyed by (tool, canonical args, raw sha, repo revision), returning 'unchanged since call X; handle H', with error-class-aware repeat annotations and exemptions for side-effecting calls.
- A byte-capped revision-keyed read-only result cache for search, explain, impact and diagnostics.
- A deterministic history compaction library over structured message arrays: keep-recent-N, per-kind summaries, todo deltas, guidance dedupe; each replaced tool_result keeps tool_use_id/is_error plus a handle.

**Requirements.** PAR-CTX-06, PAR-CTX-14, PAR-CTX-08

**Files.** `api-go/internal/evidence/ledger.go`, `api-go/internal/evidence/result_cache.go`, `api-go/internal/compaction/compact.go`, `api-go/internal/compaction/summarize.go`, `api-go/internal/compaction/compact_test.go`, `api-go/internal/evidence/ledger_test.go`, `api-go/internal/evidence/result_cache_test.go`  
**New modules.** `api-go/internal/evidence/ledger.go`, `api-go/internal/evidence/result_cache.go`, `api-go/internal/compaction`

**Tests required.**
- Repeat detection is invalidated by content or revision change; side-effecting tools are never deduped
- Error classes classify validation/not_found/permission/timeout/tool_error
- The cache is capped by bytes and scoped per principal; a revision change misses
- Compaction keeps tool exchange pairs atomic and never byte-slices; handles recover the originals
- Ports of cursor-bridge compaction tests (bridge_orchestrator_test.go:809-891) adapted to structured input, with provenance

**Acceptance.** The libraries are ready for the gateway and context hooks, with measured context savings on fixtures.

**Collision risk.** New files only in the evidence package (store.go is not edited).

### WS-43 — Opt-in upstream MCP gateway, progress event bus and memory notifications

**Goal.** Deliver the following:
- A gateway mode with a namespaced catalog, BM25 tool_search, schema fetch and call. It preserves ids, cancellation, progress, pagination, list_changed and structured results; enforces per-principal allowlists and explicit profiles; projects downstream results; and has idle eviction and a max-live limit (HTTP downstreams preferred).
- A typed event hub (128-event ring, non-blocking fan-out) feeding MCP notifications/progress, a workspace events SSE route and MCP resource subscriptions on memory revision.
- A cursor-paginated memory event feed.

**Requirements.** PAR-HAR-08, PAR-SHARE-07, PAR-SHARE-06

**Files.** `api-go/internal/gateway/catalog.go`, `api-go/internal/gateway/client.go`, `api-go/internal/gateway/search.go`, `api-go/internal/gateway/gateway_test.go`, `api-go/internal/events/hub.go`, `api-go/internal/events/hub_test.go`, `api-go/cmd/xmustard-api/events_routes.go`, `api-go/cmd/xmustard-api/mcp_http.go`, `api-go/cmd/xmustard-api/main.go`, `api-go/internal/mcpserver/tool_recall.go`  
**New modules.** `api-go/internal/gateway`, `api-go/internal/events`, `api-go/cmd/xmustard-api/events_routes.go`

**Tests required.**
- Gateway conformance: id preservation, cancel, progress and pagination passthrough
- Allowlist enforcement; discovery is never treated as authorization
- Idle eviction and max-live limits; spawned stdio servers are reported as external in gate v2
- The hub drops on full without blocking publishers
- Resource subscription notifies on memory revision change
- recall events_since pagination

**Acceptance.** Third-party MCP output is reducible through xMustard when opted in. Progress and notifications work for long calls.

**Collision risk.** mcp_http.go (WS-13), main.go (1 line) and tool_recall.go (shared memory tool file).

### WS-44 — Install/doctor/uninstall per client, conformance suite, ops CLI, ignore/retention config and fixture capture

**Goal.** Deliver the following:
- xmustard-ops setup/uninstall/doctor --client with version detection, preflight (health plus evidence round-trip), backup, atomic swap, automatic rollback and a savings report.
- A pinned client conformance suite.
- status/clean/index --prewarm/is-ignored/lsp status.
- .xmustardignore single-sourced with .gitignore, content_retention config, and a repository-trust gate for repo-supplied config.
- Opt-in sanitized fixture capture (0600, quotas).

**Requirements.** PAR-ADP-13, PAR-EVAL-07, PAR-OPS-02, PAR-FRESH-08, PAR-HAR-12, PAR-SEC-06

**Files.** `api-go/cmd/xmustard-ops/setup.go`, `api-go/cmd/xmustard-ops/doctor.go`, `api-go/cmd/xmustard-ops/status.go`, `api-go/cmd/xmustard-ops/capture_fixture.go`, `api-go/cmd/xmustard-ops/main.go`, `api-go/internal/workspaceops/repo_config.go`, `api-go/internal/conformance/suite.go`, `api-go/internal/conformance/suite_test.go`  
**New modules.** `api-go/cmd/xmustard-ops/setup.go`, `api-go/cmd/xmustard-ops/doctor.go`, `api-go/internal/conformance`

**Tests required.**
- Setup is idempotent; uninstall fully reverses it; rollback on a failed preflight
- Keys are never read from plists or exported into environments
- Conformance: the replacement reaches the next model request, shape match, error preservation, abort, and denial of expired/foreign handles, on recorded fixtures
- is-ignored agrees with the indexer on a fixture tree
- Untrusted repo config that could run commands is ignored

**Acceptance.** One-command install for Claude Code, Codex, OpenCode, Pi and Cursor, with pinned conformance results.

**Collision risk.** xmustard-ops main.go (add subcommand files only). repo_config_routes.go exists in cmd/xmustard-api and must be reconciled.

### WS-45 — Opt-in LSP pool wiring into explain, impact and diagnostics

**Goal.** Route explain(symbol), impact(op=refs|definition) and diagnostics(path) through the existing Go LSP session pool (lsp_definition.go) when a server is installed and precise=true or config enables it. Limit to at most one live server, idle eviction, crash restart with a single retry, restart on RSS runaway and memory caps (maxTsServerMemory). Upgrade edges to provenance=lsp. LSP RSS is reported on an external line.

**Requirements.** PAR-SYM-06

**Files.** `api-go/internal/workspaceops/lsp_definition.go`, `api-go/internal/workspaceops/lsp_precise.go`, `api-go/internal/mcpserver/tool_explain.go`, `api-go/internal/mcpserver/tool_impact.go`, `api-go/internal/workspaceops/lsp_precise_test.go`, `scripts/bench/rss_bench_v2.py`  
**New modules.** `api-go/internal/workspaceops/lsp_precise.go`

**Tests required.**
- Falls back to tree-sitter when no server is installed
- Only one live server; idle eviction; a crash triggers restart and a single retry
- Provenance=lsp set on upgraded edges
- Gate v2 reports LSP RSS on a separate external line

**Acceptance.** Serena-grade precision is available opt-in, with honest external accounting.

**Collision risk.** lsp_definition.go (platform routes use it; keep behavior). Per-tool files explain and impact. Does not touch diagnostics*.go; the diagnostics wiring waits for WS-47.

### WS-46 — SCIP precise-lane ingestion

**Goal.** Ingest scip-go, scip-typescript and rust-analyzer scip output, produced by scheduled external jobs, into the occurrence/edge tables stamped with commit and provenance=scip. Report precision_by_lang coverage. Check indexer licenses.

**Requirements.** PAR-SYM-07

**Files.** `rust-core/Cargo.toml`, `rust-core/src/index/scip_ingest.rs`, `rust-core/src/bin/xmustard-core.rs`, `rust-core/tests/scip_ingest.rs`  
**New modules.** `rust-core/src/index/scip_ingest.rs`

**Tests required.**
- Ingest a small SCIP fixture; occurrences map to symbols
- The commit stamp mismatch with HEAD is labeled stale
- Steady RSS unaffected (disk-backed)

**Acceptance.** A precise lane exists without a resident language server. Indexer runs are external and measured.

**Collision risk.** Cargo.toml and bin/xmustard-core.rs (dispatch table).

### WS-47 — Diagnostics tool deepening after the diagnostics session hands off

**Goal.** Once the in-flight local diagnostics store lands, which is owned by another session, add: path, symbol, min_severity, range, with_references and since=checkpoint|baseline (new and resolved sets), grouping by path, severity and owner name path, structural import-cycle findings from WS-35, and precision provenance.

**Requirements.** PAR-HAR-07, PAR-IMP-08

**Files.** `api-go/internal/workspaceops/diagnostics.go`, `api-go/internal/workspaceops/diagnostics_query.go`, `api-go/internal/mcpserver/tool_diagnostics.go`, `api-go/internal/workspaceops/diagnostics_query_test.go`  
**New modules.** `api-go/internal/workspaceops/diagnostics_query.go`

**Tests required.**
- Filters and grouping on local-store fixtures
- since=checkpoint returns new and resolved sets correctly
- Structural findings merged with provenance

**Acceptance.** diagnostics works without Postgres and offers scoped and delta queries.

**Collision risk.** HIGH. It touches diagnostics*.go, which the diagnostics session owns (diagnostics_input/local_store/prepared/read_stream, diagnostics.go and tests, profile_hook*, zz_profile_gate_test.go). Start only after that session hands off and its branch (wip/diagnostics-live-set or the opus worktree) has landed.

### WS-48 — Single config struct replacing repeated env lookups

**Goal.** Replace the 186 envDefault("XMUSTARD_DATA_DIR"...) calls and other scattered env reads in main.go with one config struct passed to handlers. This is a mechanical change.

**Requirements.** PAR-RT-07

**Files.** `api-go/cmd/xmustard-api/config.go`, `api-go/cmd/xmustard-api/main.go`, `api-go/cmd/xmustard-api/config_test.go`  
**New modules.** `api-go/cmd/xmustard-api/config.go`

**Tests required.**
- Config is loaded once; env precedence documented and tested
- All cmd/xmustard-api tests are green

**Acceptance.** main.go has no repeated envDefault data-dir lookups, and behavior is unchanged.

**Collision risk.** Very high on main.go. Schedule in a quiet window after wave-2 route work lands. Coordinate with the diagnostics session's main.go wiring.

### WS-49 — Kernel package extraction and platform behind a build tag

**Goal.** Extract governance, grounding, evidence, budget, the nine handlers and the MCP server into a kernel package with an explicit Store interface. Move issues, runs, terminals, providers and Postgres behind a build tag so the default binary does not link pgx or provider code.

**Requirements.** PAR-RT-07

**Files.** `api-go/internal/kernel`, `api-go/internal/workspaceops`, `api-go/cmd/xmustard-api/main.go`, `api-go/cmd/xmustard-api/platform_routes.go`, `Makefile`  
**New modules.** `api-go/internal/kernel`, `api-go/cmd/xmustard-api/platform_routes.go`

**Tests required.**
- The default build excludes pgx (go list -deps check)
- The platform build tag restores all routes; both builds pass tests
- The binary size and RSS delta are recorded with gate v2

**Acceptance.** A smaller default binary with measured savings, and the platform available only with the tag.

**Collision risk.** Very high: broad file moves in workspaceops. Run it last in wave 3 with no concurrent workstreams on workspaceops, and coordinate with the diagnostics session.

**Correction.** Critic: gate Rust wiki and goal subcommands behind the same platform feature.

### WS-50 — Parity evaluation suite and final parity-scale gate

**Goal.** Deliver the following:
- The coding-memory lifecycle suite: duplicate, supersession, contradiction, stale, expiry and scope-leak fixtures, plus a consolidation arm.
- The context-reduction quality evaluation: raw, projection, masking and compaction, with rare-failure fixtures.
- Component benches: retrieval against peers, impact precision against LSP/SCIP truth, and freshness latency after edit, rename and delete.
- A footprint comparison against OpenHands, Pi and a bare agent.
- The final EVAL-05 gate at parity scale on macOS and Linux.
Update the docs with measured results only.

**Requirements.** PAR-EVAL-02, PAR-EVAL-03, PAR-EVAL-06, PAR-EVAL-09, PAR-EVAL-05

**Files.** `eval/tasks/memory_lifecycle.yaml`, `eval/tasks/reduction_quality.yaml`, `api-go/cmd/xmustard-eval/arms.go`, `scripts/bench/component_benches.py`, `scripts/bench/footprint_compare.py`, `docs/benchmarks`  
**New modules.** `eval/tasks/memory_lifecycle.yaml`, `eval/tasks/reduction_quality.yaml`, `scripts/bench/component_benches.py`, `scripts/bench/footprint_compare.py`

**Tests required.**
- Fixture validity checks: oracles fail on the untouched baseline and pass on the reference solutions
- Scope leakage metric is 0
- Gate v2 run at parity scale with all default lanes on; externals on separate lines
- Linux run alongside macOS

**Acceptance.** A published, reproducible parity report. Budget and parity claims in docs are updated only from these measurements, and a failed gate is reported honestly with the offending components.

**Collision risk.** docs/benchmarks only. Do not edit README.md or docs/STATUS.md until the diagnostics session has landed, then do a coordinated docs pass.

### WS-51 — Helper-model adapter contract, optional ONNX sidecar and Needle3 measurement

**Goal.** Deliver the following:
- The off-by-default helper contract: an isolated serialized worker with bounded input, time and output and telemetry disabled. It returns IDs or an abstention, validated against the deterministic shortlist and snapshot. Parsing fails closed (cursor-bridge extractJSONObjectCandidates pattern).
- An optional ONNX embedding sidecar for index time only, in the heavy slot.
- Needle3 measured RSS and a tool-selection A/B against BM25 before any enablement.

**Requirements.** PAR-CTX-11, PAR-CTX-12, PAR-RET-05, PAR-GOV-19

**Files.** `rust-core/src/helper/mod.rs`, `rust-core/src/helper/contract.rs`, `rust-core/src/helper/onnx_sidecar.rs`, `api-go/internal/helper/client.go`, `api-go/internal/helper/parse.go`, `api-go/internal/helper/parse_test.go`, `rust-core/Cargo.toml`, `scripts/bench/helper_rss.py`  
**New modules.** `rust-core/src/helper`, `api-go/internal/helper`

**Tests required.**
- Parser handles fenced output, concatenated objects and junk, and fails closed
- Validation rejects IDs outside the shortlist; falls back on abstention
- The sidecar is admitted only through the heavy slot and is reaped when idle
- Needle3 RSS measured and recorded; FunctionGemma/LFM excluded by policy

**Acceptance.** The contract is shipped disabled. Measurements are recorded, and there is no default-profile RSS change.

**Collision risk.** Cargo.toml (feature-gated dependency).

### WS-52 — Revision-aware reads, commit/diff history search and task-shaped retrieval

**Goal.** Deliver the following:
- rev= on search and explain via the git object database without checkout; impact base/head compare (building on WS-35).
- search mode=commits|diff, bounded by git log --grep/-S/-G with caps; stale memories get the commits that invalidated them attached.
- search mode=task, with deterministic decomposition into identifiers, paths, concepts and memories naming paths, returning a few ranges with a why.

**Requirements.** PAR-FRESH-10, PAR-RET-08, PAR-RET-07

**Files.** `rust-core/src/index/revision.rs`, `api-go/internal/workspaceops/history_search.go`, `api-go/internal/workspaceops/task_search.go`, `api-go/internal/mcpserver/tool_search.go`, `api-go/internal/workspaceops/history_search_test.go`, `api-go/internal/workspaceops/task_search_test.go`  
**New modules.** `rust-core/src/index/revision.rs`, `api-go/internal/workspaceops/history_search.go`, `api-go/internal/workspaceops/task_search.go`

**Tests required.**
- A rev= read returns historical content without touching the working tree
- Commit and diff search are capped by lines and bytes
- Stale memory links to its invalidating commit
- Task decomposition fixtures return ≤limit ranges with reasons

**Acceptance.** Sourcegraph and Augment-style revision, history and task retrieval are available through search, with at most one extra revision graph resident.

**Collision risk.** The per-tool mcpserver file tool_search.go is shared with WS-18, WS-36 and WS-38.

### WS-53 — Bounded verification and run-lineage runner

**Goal.** Add a small state machine in the daemon for verification runs (tests and checks) with lineage, outcome records in govstore, and restart honesty (running becomes interrupted and failed). verify reuses an in-flight run for the same claim and revision. Child tools are external, bounded by CaptureWriter and ChildLimit, and reported separately. There is no agent loop.

**Requirements.** PAR-HAR-10, PAR-SHARE-09

**Files.** `api-go/internal/workspaceops/run_lineage.go`, `api-go/internal/workspaceops/run_lineage_test.go`, `api-go/internal/govstore/outcomes.go`, `api-go/internal/workspaceops/memory_verify.go`, `api-go/internal/workspaceops/grounding_runs.go`  
**New modules.** `api-go/internal/workspaceops/run_lineage.go`

**Tests required.**
- Restart marks in-flight runs interrupted and failed
- A duplicate verification request reuses the in-flight run
- Child output is capped; RSS of child processes appears on external lines in gate v2
- Lineage links run, outcome, memory and evidence

**Acceptance.** The verification lineage is honest and deduplicated, with no agent-runtime scope creep.

**Collision risk.** memory_verify.go (WS-19) and grounding_runs.go (WS-03, WS-21).

### WS-54 — Output-budget contract for ground and tools/list (critic addition)

ground(sections, max_chars) with per-section caps and a degradation ladder; snapshot test that tools/list stays <= N bytes per profile (baseline 5,177 B); advanced args accepted but not advertised, documented via an MCP resource.

**Implementation record (branch parity/ws-54, 2026-09-25).** These notes record what was built and measured, and where it differs from the text above.
- *Files.* New package `api-go/internal/groundbudget/` (`sections.go` section table and argument parsing, `apply.go` the ladder, `doc.go` generated documentation; standard library only, so the stdio shim links it without `workspaceops`). The route moved from `main.go` to `api-go/cmd/xmustard-api/ground_routes.go` (`registerGroundRoutes`, a one-line `main.go` diff). In `internal/mcpserver`: `Advanced` and `List` arguments and schema profiles (`schema.go`, `server.go`), `docs_resource.go`, ground's advanced arguments (`tool_ground.go`), one instructions line, and the byte-cap test with `testdata/tools_list_budget.json`. No `grounding*.go` file changed, so the WS-03 split merged without conflict.
- *ground contract.* Sections, most important first: `summary` (pinned: workspace id, summary line, both blocked flags, `unknown`, generated_at; cap 2,048), `runs` (1,536), `index` (3,072), `memory` (768), `drift` (1,536), `principal` (512); undeclared members form `other` (1,024), reduced first and reported by name. `max_chars` defaults to 6,000 and accepts 2,000 to 65,536; out-of-range values are rejected (MCP -32602, HTTP 400 naming the argument and bounds), never clamped. Caps apply first and are lifted for a section requested alone, which is how a caller pages one section in full. Then the least important section goes trimmed (newest items kept), counts (lists counted, objects keep their flags), omitted; a more important section is reduced for max_chars only after every less important one is omitted, and `summary` stops at counts. Signals (failed runs, blocked flags, change and contract counts, stale memory, verification modes, drift flags, `unknown`) are never dropped silently: they survive as counts or flags, or move to `output_budget.signals`, also for sections not requested. Every result ends with `output_budget {max_chars, used_chars, degraded_stage, over_budget?, sections{state, reason, cap, full_chars, kept, total, recover}, not_requested, signals, docs}`.
- *Measured, ground.* On a local clone of research/pi-mono (MIT; 1,929 files) with 60 files given a new leading parameter after baselining, ground returned 19,544 B before and 4,550 B with the default budget (index trimmed to its cap: 37 of 200 contract breaks kept, 200 counted); `sections=index&max_chars=65536` returns all 200 (19,114 B); `max_chars=2000` returns 1,941 B. The same numbers hold after merging WS-03, and a ground call through the real stdio shim returns the budgeted result.
- *Measured, tools/list.* Caps are the compact wire bytes of the `tools/list` result, per schema profile, protocol version and role profile, set at today's measurement on the merged base (WS-03 had removed nullable ground outputSchema members, 238 B per 2025-06-18 list): lean 2024-11-05 reader 3,999, proposer 4,715, verifier 4,630, agent 5,346; lean 2025-06-18 6,646, 7,768, 7,680, 8,802; full (advanced arguments listed, `XMUSTARD_MCP_SCHEMA=full`) +231 B each. "agent" is also admin and open mode; "reader" is also read-only mode, and a `cmd/xmustard-api` test keeps the profiles equal to the route gates. The critic's 5,177 B baseline was an older 2024-11-05 build; the current 2024-11-05 agent list is 5,346 B. The description budget (1,786 of 1,794 B) and the lean golden snapshot are unchanged, so `integrations/pi/src/tools.ts` needed no edit (its unit test passes). The instructions stay within 1,400 B (1,396 B).
- *Advanced arguments.* Accepted on tools/call with the same strict validation as listed ones and never listed in the default lean profile. `xmustard://docs/tools` (4,029 B, generated from the tool and section tables) documents them, the ground contract and the hidden aliases. Clients that validate calls against the closed schema (Pi's mirror is one) cannot send unlisted arguments; the full profile exists for them. Pi keeps the lean schema, so Pi agents do not get `sections` or `max_chars` yet.
- *Budget.* The budgeting itself is transient: `BenchmarkApply` on a 20 KB result allocates 275 KB per call. API max RSS over load, index and about 50 ground calls was 30,113,792 B against 30,081,024 B for the base build; the process-tree peak under repeated ground calls was 39,616 KiB against 38,880 KiB, within run-to-run noise. Binaries grow by about 35 KB each. Shim max RSS for initialize and tools/list over 8 runs was 12,274 KiB mean (range 11,936 to 12,432) against 12,102 KiB (11,744 to 12,544) for the base: the ranges overlap.
- *Not done here.* Ground still computes every section and then budgets. The signals of an unrequested section are part of the contract, so skipping its work would need cheaper signal sources. The README tool table was left alone because the diagnostics session is editing README.md. The frontend `SessionGrounding` type has no `output_budget` field; the UI reads only `summary` and the pinned blocked flags. PAR-CTX-05 is only partly met, for ground only: ground has `max_chars`, `degraded_stage` and `recover_handle`, but no `limit`, `cursor`, `has_more` or `next_cursor`; a list past 65,536 characters is reached through the retained original's evidence pages instead. WS-36 owns the ladders for search, explain and impact. CTX-05 for recall, remember, verify, diagnostics and why_failed, and cursor fields on ground, have no owner yet (WS-04 is merged without them) and need a workstream.
- *Review follow-up (2026-09-26).* (1) Over plain HTTP the budget is opt-in: `GET .../session-grounding` with neither `sections` nor `max_chars` returns the unbudgeted result with no `output_budget`, so Pi's closed mirror and the UI keep the full result. The MCP ground tool always sends `max_chars` (default 6,000). (2) On a delivered (MCP) call, a result that leaves out anything requested retains the unbudgeted original as evidence (`CaptureRequest.Retain`, record mode `retained`) and names it in `output_budget.recover_uri` and `recover_handle`, or says why not in `recover_unavailable`. This is the path to items past 65,536 characters. (3) Every boolean of a signal object survives the counts and omitted stages (drift `sibling_clone` is also in Keep), and nested list lengths such as `drift.reasons` move to signals. `principal` is a signal, so `principal.open_mode` is never dropped. (4) Undeclared members are signals and are named under `sections.other.members`, also on a narrow request. (5) `sections=summary` alone lifts the summary cap, and every recover hint names a call that returns more. (6) Apply parses the result in place (byte windows plus 4 bytes per list element, no copy of members or elements), and the route admits `WorkingSet` (the result, the element tables, the output and 256 KiB of ladder slack) in the request's transient scope before budgeting, answering 503 with Retry-After when the pool refuses. (7) The docs say trimmed lists keep their first items in result order, with failed runs newest first; broken contracts follow the index's order.

### WS-55 — Worktree overlay index (critic addition)

One base index per repo plus per-worktree delta segments for dirty and branch-diff files; acceptance: gate v2 with 4 agents in 4 worktrees of one repo stays under 95.4 MiB.

### WS-56 — Injection-safety policy (critic addition)

Instruction-pattern scan and data framing on everything injected; core tier and hook-injected memories need human-approver or stronger policy; untrusted-capture-derived content stays quarantined; adversarial fixtures in EVAL-02.

**Implementation record (branch parity/ws-56, 2026-09-28).** The section above is one line, so the workstream direction was the spec. It asked for:

- an instruction-pattern scan and data framing on everything xMustard injects;
- a small library that WS-23 calls;
- human-approver-or-stronger verification for core-tier and hook-injected memory, reusing the WS-09 roles and WS-19B provenance;
- quarantine for content derived from untrusted captures;
- adversarial EVAL-02 fixtures run as unit tests;
- a bounded, measured scan cost;
- patterns and framing as data tables, with quarantine and approval checks explicit and fail-closed.

- *Library (`api-go/internal/injection`).* Four parts:
  - **Scan.** `rules.go` holds a table of 13 rules, each with an id, trigger literals and an RE2 pattern: `override_instructions`, `new_instructions`, `role_reassignment`, `authority_claim`, `secrecy`, `prompt_leak`, `exfiltration`, `remote_exec`, `chat_template`, `turn_marker`, `frame_spoof`, `hook_spoof` and `hidden_text` (zero-width characters, bidi controls, BOM and Unicode tag characters). `Scan(parts...)` folds each part once: lowercase, every whitespace run one space, a paragraph break `\n\n`, and the text starting after one. It then makes one pass over the folded bytes. A byte inside a word is skipped. At any other byte, the triggers indexed by that byte (a `[256]` table) are compared, and a matching trigger runs its rule's pattern anchored there, over one byte more than the pattern's longest match. Every match must start with a trigger, and the scan is capped at 128 KiB (`scan_truncated` past the cap).
  - **Frame.** `frame.go` has `Frame`, `FrameAll` and the `Notice`, and a JSON `DataNotice`. A frame tag inside framed text, in any case or spacing, is written with `&lt;`, so the block's own closing tag is the only one. Attribute values keep only id characters.
  - **Policy.** `policy.go` holds the surface table: `recall` and `evidence` are pulled; `hook` and `core` are pushed with minimum basis `human_approved`. The basis ladder is unverified, self_asserted, peer_verified, human_approved, and a verification_mode missing from the table is unverified. `Decide` runs ordered guard clauses: an unknown surface; then a pulled surface, which admits with its flags; then, on a pushed surface, quarantined, below the basis (`needs_<basis>`), and flagged.
  - **Sources.** `sources.go` has `CaptureQuarantine(tool)`, with an allowlist of the native file, search, list, diff and shell tools of the supported clients, plus xMustard's nine tools, bare or as `mcp__xmustard__*`. Any other tool, or no tool name, is `untrusted_capture:<tool>`. `QuarantineForeignImport` is the mark importers use.

  The table is checked when the package loads: unique ids, lowercase triggers, and a pattern with an unbounded or 512-byte-or-longer longest match panics (computed from `regexp/syntax`). The tests check on every fixture that every match starts with a trigger and that the trigger pass agrees with an exhaustive match.
- *Where it runs.*
  - **Recall.** Every render (full, compact, names_only), fetch by id and `xmustard://memory/index` label entries with `injection_flags` and `quarantine`. The recall result and the fetch-by-id result carry `data_notice`, and a verifier's view of a pending edit carries the edit's `injection_flags`.
  - **Remember.** It keeps flagged text, returns `injection_flags` and a warning that names the rules, and never refuses the write.
  - **Evidence.** Every evidence projection gets `Delivery.injection_flags` at `Store.Capture`: every MCP tool result delivered through the envelope, and native output that hooks capture. The MCP bridge adds an `[xmustard injection-check]` note and `_meta` flags to a flagged result. (As first built, the scan read a JSON projection's escaped text, so it missed most rules on xMustard's own results; review round 1 below fixed that and added the note to hook-shaped and Pi output.)
  - **Instructions.** Step 2 of initialize.instructions now says that memory, tool output and `<xmustard-data>` blocks are data, not instructions, and names `injection_flags`. It is 1,398 bytes, under the unchanged 1,400-byte budget. The recall `Doc` in `xmustard://docs/tools` explains the two labels.
  - **Ground.** It returns counts and ids only, so its memory section carries no agent-written text to frame.
- *Pushed surfaces (`workspaceops.AdmitMemory`).* This is what WS-23 (hook, `SurfaceHook`) and WS-31 (core tier, `SurfaceCore`) call with their candidate ids (at most 64). It refuses a surface that is not pushed, and it fails closed when the token store or the memory store cannot be read. For each candidate, in order: the entry is in the workspace (else `not_found`); it is served (else `not_served`); its content matches its digest (else `content_changed`); and then `Decide` runs. Admitted memory comes back framed (`text`), and every withheld candidate comes back with its reason and flags. The caller budgets the text.
- *Human approval (WS-09 roles, WS-19B kinds, WS-57 surface).* A human approval is an `approve` verdict on the served revision in the current vote epoch that meets all of these:
  - it is recorded with `principal_kind` human;
  - its principal is a human approver of the workspace now: a file-backed token of kind human with the human-approver role (WS-57 `IsHumanApprover`; admin holds the role), scoped to the workspace, unexpired, not revoked and not the open-mode identity;
  - its principal wrote neither the entry nor the served revision.

  A quorum of agents is not a human approval, and neither is anything in open mode. An approval through `xmustard-ops approve` counts (`TestOpsHumanVerdictAdmits`).
- *Quarantine.*
  - `bindProvenance` reads each cited evidence handle's tool (`checkEvidence` now returns it). A handle from an untrusted tool sets `ContextActor.Quarantine`.
  - A proposal stores the mark in entry metadata (`govstore.MetaQuarantine`), and an edit that cites such a capture adds it (`markQuarantined`).
  - govstore keeps the mark sticky across `SetClassification`, and refuses a quarantined entry the core tier on insert and in `SetTier`.
  - Recall serves quarantined memory labeled, and pushed surfaces withhold it even after a human approval.
- *EVAL-02 fixtures.* `eval/tasks/memory_lifecycle/injection_safety.json` (schema `xmustard.eval.injection/v1`) holds:
  - 39 scan cases: 25 adversarial, including stacked, zero-width, bidi and tag-character cases, and 14 benign near misses (Java `@Override`, `ignore whitespace`, `curl` without a pipe, typography, `security alert`, prose about hook fields, and others);
  - 13 policy cases;
  - 3 frame-escape cases;
  - 11 capture sources.

  `TestEval02InjectionFixtures` runs them with no model. The end-to-end cases are in `workspaceops/memory_injection_test.go`, and `eval/tasks/README.md` documents the file.
- *Measured (build box: Linux, Ryzen 5 3500X, `-cpu 1`, load average 2 to 3).*
  - `BenchmarkScan`: a 1 KiB memory in 7.1 to 8.0 µs (128 to 144 MB/s); 4 KiB of prose in 27 µs (150 MB/s); 64 KiB of Go source in 0.54 to 0.55 ms (119 to 121 MB/s); and 128 KiB of Go source, the cap, in 1.09 to 1.12 ms. Clean text allocates nothing per scan (the fold buffer is pooled). The "64 KiB of dense adversarial text in 0.50 ms" measured then was a text that matches every rule early, which ends the scan; it was not the worst case (review round 1 below).
  - `BenchmarkFrame` frames 4 KiB in 2.1 µs.
  - `BenchmarkAdmitMemory8`, one hook-sized admission of eight 400-byte human-approved memories, takes 1.47 to 1.53 ms, 155 KB and 2,720 allocations, nearly all of it store reads.
  - `BenchmarkRecall1k` stays at 16.4 to 16.7 ms.
  - The first design ran each rule's pattern unanchored over the whole text, at 2.4 to 15 MB/s (0.3 ms for 1 KiB, 7.6 ms for 64 KiB). The trigger index made the scan 10 to 40 times faster.
  - What the scan adds per injection: recall scans the returned entries (about 30 µs for eight 500-byte entries); a capture scans its projection (about 0.14 ms at the 16 KiB hook targets and about 0.55 ms at the 64 KiB MCP target).
- *Checks.* The full Linux gate (`xm-remote-check.sh all -count=1`: Rust release build, tests and clippy, then go vet and every Go package) passes after merging `feat/parity-v2` at `144123c` (WS-57), with 6 clippy warnings, the same as the base `fd083c2`. No Rust code changed. The merge conflict was only in `ContextActor`: WS-57's `Approval` and WS-56's `Quarantine` are both kept, and `humanApprovers` reuses WS-57's `IsHumanApprover`.
- *Deviations.*
  - The scan is a pattern check, not a classifier. A paraphrase with no rule passes, and a legitimate memory that quotes an injection is flagged, so it is served labeled and never pushed. Remember never refuses flagged text. Rules target injection shapes, not imperative guidance, and the benign fixtures pin the main false positives that were avoided. Fullwidth and other lookalike letters are not normalized: there is no NFKC without x/text.
  - Quarantine sees only what a write cites. Content an agent copies from a web page without citing the capture is not marked, and output that Bash fetched (for example with `curl`) is trusted like any shell output, because the capture records the tool, not the command. The tool name is the one the capturing client recorded. The mark is sticky: an edit that cited an untrusted capture keeps the entry quarantined even if the edit is later rejected. Nothing clears the mark; human-vetted text is proposed afresh.
  - "Stronger than human-approver" is not a separate level. Any human approver's approval counts, whatever its WS-57 assurance (advisory or user_presence). A policy that requires user_presence would need the vote's `provenance.approval` label read per candidate.
  - No foreign importer exists yet (WS-34 and the PAR-REV-08 rule import). They mark their writes with `ContextActor.Quarantine = injection.QuarantineForeignImport`.
  - AdmitMemory has no caller yet, because WS-23 (hooks) and WS-31 (the core projection and tier changes) are not built. Its tests and the govstore tier invariant stand in. Stale memory is not a gate: D-21 labels stale memory, and WS-23 computes it.
  - The evidence `resources/read` pages and search-in-original return original bytes unscanned. They are pages the agent asks for of its own tool output, and the projection that led to them carries the flags.

**Review round 1 (2026-09-28).** Two major and six minor findings; all eight are fixed.

- *JSON tool results were scanned escaped (major).* `Store.Capture` scanned the projection text, which for xMustard's tools is JSON: a newline is `\n`, a quote `\"`, and Go's encoder writes `<` and `>` as `\u003c` and `\u003e`. A phrase wrapping across a line, `turn_marker`, `hook_spoof`, and on Go-encoded results `chat_template` and `frame_spoof`, could not match. `injection.ScanText` now reads a projection that is a JSON document, or a stream of them, as the text it encodes. It validates the JSON (`json.Valid`, then a decoder for streams), then walks the strings once: each member name keeps its quotes and colon (so a forged hook key still reads as one), each string value is decoded (`appendUnquoted`, differential-tested against `encoding/json`, surrogate pairs included), and each is its own paragraph. Other text, and JSON over 256 KiB, is scanned raw. The 128 KiB cap applies to the decoded text. EVAL-02 gains a `tool_result` section of 9 cases (wrapped override, `\u003c|im_start|\u003e`, `\n\nHuman:`, wrapped secrecy, an escaped frame tag, an escaped hook key, a stream, a benign result, plain text), and the evidence tests capture the reviewer's four probes as Go-encoded results. SECURITY.md no longer claims more coverage than this.
- *Markdown underscore emphasis evaded the word rules (major).* `fold` now writes a run of `_` that is not between two letters or digits as whitespace, so `_ignore previous instructions_` and `__Ignore all previous instructions__` start a word; `snake_case`, `im_start` and `tool_result` keep theirs. Fixtures: `underscore-emphasis`, `underscore-bold`, `underscore-secrecy`, `benign-snake-case`.
- *Adversarial scan cost (minor).* A failed try re-ran its pattern over a window of up to about 150 bytes, and nothing bounded the tries. Two changes:
  - **Prefilter.** A rule may name a byte every match holds (`Rule.Needs`): `|` for `remote_exec`, `>` for `frame_spoof`. The pattern runs only when the window holds it.
  - **Match budget.** A scan may try 128 patterns plus one per 64 bytes scanned; a scan that spends the budget stops and reports `scan_saturated`, which a pushed surface treats like a match. A byte budget was tried first and dropped: a try's cost does not follow its window (`ignore all all all all ...` costs about 1.9 µs a try over a 61-byte window, and was not saturated by a budget of 4 window bytes per byte, at 11 ms for 128 KiB).

  Measured on the build box (`-cpu 1`, 3 runs of 50) at 128 KiB, before and after: repeated `curl ` 64.5 → 1.03 ms; repeated `<system` 56.8 → 2.65 ms; repeated `you are now a ` 11.9 → 3.05 ms; `curl ` ×19 then `| x` 37.8 → 3.5 ms. The costliest inputs now are the new benchmarks: `curl_far_pipe_128KiB` (the pipe just out of the pattern's reach) 5.77 ms, `tag_far_close_128KiB` 6.1 ms and `ignore_all_128KiB` 4.3 ms, all saturated. Clean text is unchanged: 1 KiB 7.5 to 7.6 µs, 4 KiB 30 µs, 64 KiB of Go source 0.62 ms, 128 KiB 1.22 to 1.24 ms. Legitimate text stays well under the budget: across 581 of the repository's docs and sources (over 1 KiB), the densest tries one pattern per 163 bytes (a quarter of its budget, `docs/prompts/...-sixth-hundred-tranche-pass.md`), and the median one per 2,234 bytes; `TestMatchBudgetHeadroom` keeps a sample under a quarter. `BenchmarkScanText` reads a 64 KiB Go-encoded search result in 1.08 to 1.11 ms with 2 allocations.
- *Invisible characters (minor).* `hidden_text` adds the soft hyphen, the combining grapheme joiner, the Arabic letter mark, the Hangul, Khmer and Mongolian fillers, and the variation selectors: U+E0100 to E01EF always, U+FE00 to FE0F only two in a row, since an emoji takes one (`benign-emoji` pins that). Each has its UTF-8 encoding or lead bytes as a trigger. Fixtures: `soft-hyphen`, `variation-selector-supplement`, `variation-selector-run`, `hangul-filler`, `grapheme-joiner`. A soft hyphen inside `ignore` is flagged `hidden_text`, not `override_instructions`: fold does not strip the invisible characters.
- *No note on hook-shaped output (minor).* `injection.Note` is now the one note text. `Observe` appends it to the footer when the projection is flagged, so the output a hook puts in place of a native tool's carries it; the Pi adapter mirrors it in `renderDelivery` (and treats it as an adapter line for stubs and for reading a remember result). A native output that is not reduced passes through unchanged and carries no note (recorded in SECURITY.md).
- *A core entry could take the quarantine mark (minor).* `SetClassification` now refuses a quarantine mark on a core-tier entry, so an edit of a core entry that cites an untrusted capture fails and changes nothing (`TestCoreEntryRefusesQuarantinedEdit`).
- *No audit of the mark (minor).* The classify event that adds the mark records `quarantine` in its data, and the history shows it.
- *Quarantine did not carry through xMustard's tools (minor).* A capture of one of xMustard's nine tools now records the first quarantined memory its original carries: a `"quarantine": "<reason>"` member, which text quoted inside a JSON string cannot form (its quotes are escaped). The digest pass finds it (keeping 160 bytes across its 256 KiB chunks; 3.9 GB/s, `BenchmarkCarriedQuarantine`). The capture's record and its pages carry `quarantine`, and `checkEvidence` returns it, so a write that cites a recall result which served a quarantined memory is quarantined for the same reason (`TestQuarantineCarriesThroughRecall`). A capture from before this records nothing and is judged by its tool name. A native tool's output is not searched for the member.
- *Fixtures now.* `injection_safety.json` holds 49 scan cases (33 adversarial, 16 benign), 9 tool results (8 adversarial, 1 benign), 13 policy cases, 3 frame cases and 11 capture sources.
- *Checks.* `xm-remote-check.sh all -count=1` passes (6 clippy warnings, as on the base; no Rust change); the Pi unit tests pass (44).
  - The frontend `MemoryEntry` type does not gain `quarantine`, `injection_flags` or `data_notice` (the UI is out of focus; the fields are optional additions).
- *Files.*
  - New: `api-go/internal/injection/` (`rules.go`, `scan.go`, `frame.go`, `policy.go`, `sources.go`, `injection_test.go`), `workspaceops/memory_injection.go` and its test, `govstore/quarantine_test.go`, `evidence/injection_flags_test.go`, `mcpserver/injection_note_test.go`, and `eval/tasks/memory_lifecycle/injection_safety.json`.
  - Changed: `govstore/entries.go` (`TierCore`, `MetaQuarantine`, the core and sticky invariants); `evidence/store.go` (`Delivery.injection_flags`); `mcpserver/evidence.go`, `instructions.go` and `tool_recall.go`; `workspaceops/memory_propose.go`, `memory_provenance.go`, `memory_edit.go`, `memory_store.go`, `memory_render.go`, `memory_history.go` and `grounding_memory.go`; `docs/SECURITY.md` (Injection safety), `docs/ARCHITECTURE.md` and `eval/tasks/README.md`.

### WS-57 — Human-approval surface (critic addition)

xmustard-ops approve|reject|queue bound to a human-approver token, optional MCP elicitation; precondition for protected paths in WS-19 and WS-31.

**Implementation record (branch parity/ws-57, 2026-09-28).** These notes record what was built and measured, and where it differs from the text above. The section above is one line, so the workstream direction was the spec: approve, reject and queue under a human-approver token (WS-09 roles), WS-71 folded in, optional elicitation behind capability negotiation, wiring to govstore's pending and verification queue, distinct-principal rules, fail-closed trust checks, no new MCP tool and no rise in the lean tools/list caps.
- *Human approver.* A human approver is a principal of kind human (WS-19B's token kinds) that holds the human-approver role; admin holds that role too. `AuthorizeHumanApprover` runs these checks in order and fails closed:
  - a token was given;
  - it resolves, so it is known, not revoked and not expired;
  - it is not the open-mode identity;
  - it is of kind human;
  - it holds human-approver;
  - it is scoped to the workspace.

  A refusal is audited as `denied` with the detail `human approval: <why>`. Open mode has no human approver.
- *Token and assurance.* The ops CLI reads the token from `--token-file`, then from `XMUSTARD_APPROVER_TOKEN`, and otherwise prompts on the controlling terminal with echo off (`/dev/tty`, termios through `golang.org/x/sys/unix` on darwin and linux; other platforms have no prompt). An interrupt, quit, hangup or terminate signal while the prompt is open restores the terminal before the process exits (130). The assurance belongs to the token, not to the channel it arrived on (review round 1):
  - A *presence-only* token (`"presence_only": true` at `POST /api/auth/tokens`, kind human only; rotation keeps it) is accepted only when typed at the prompt. The auth middleware refuses it as a bearer token (401, reason `presence_only_token`, audited), which covers HTTP, the stdio MCP shim and `/mcp`. The ops CLI refuses it from `--token-file` or the environment (audited). So no configuration an agent process reads has to hold it.
  - Every other human-approver token is advisory wherever it is used, typed or not, because the same token is accepted from files, the environment, HTTP and MCP client configurations that agent processes can read.

  Every write a human approver makes records `provenance.approval = "<surface>/<assurance>"`:
  - `ops/user_presence` only for a presence-only token typed at the prompt;
  - `ops/advisory` for any other token at the ops CLI, typed or read from a file or the environment;
  - `mcp_elicitation/advisory` for a write the human confirmed through MCP (the issuer header must name the bridge as well as the approval header);
  - `http/advisory` for a direct API call.

  `user_presence` is not proof of presence. A process that learns a presence-only token can still type it through a pseudo-terminal; the label says only that the token is refused, and audited, on every channel agents are expected to read. Git children never inherit `XMUSTARD_*` variables.
- *approve and reject.* `xmustard-ops approve|reject <ws> <entry_id> [--revision N] [--note]` calls `HumanVerdict`, which calls `VerifyContextOutcome`. The verdict is one more distinct principal's: it counts toward the quorum like a peer's, it never promotes alone past a multi-peer gate, and the owner-distinct policy applies. On top of that, a human approver never votes on memory they proposed or on an edit they authored (`ErrSelfApproval`), in any quorum mode. Revision 0 is pinned to the served revision the check read, so a revision served after the check conflicts instead of being voted on unchecked. Only approve and reject are accepted.
- *queue (WS-19A/B lifecycle, WS-20 queue view, WS-22 events).* `xmustard-ops queue <ws> [--limit 50] [--baselines 10]` lists pending proposals (the served revision of an unpromoted entry) and pending edits (the head revision), ordered by when the pending revision was written (entry id and revision break ties), so a recent edit of an old entry sorts after older proposals and `--limit` keeps the oldest. Each item carries:
  - the text, cut at 2,000 bytes on a rune boundary and withheld when it does not match its digest or was purged;
  - an edit's reason and diff;
  - approvals, rejections and votes_needed;
  - the human approvals already cast.

  The awaiting_me rule of WS-20 applies: writes by the approver, writes the approver already voted on, and writes the owner-distinct policy blocks are left out and counted under `skipped.own`, `skipped.voted` and `skipped.same_owner`. `total` counts every eligible write, and `--limit` bounds only the items rendered. The queue also lists the newest `index_baseline` events (WS-22) with seq, time, principal, reason, auto, head, replaced and previous head. They need no approval; they are listed so a human sees every baseline reset, automatic or not.
- *MCP elicitation.* The session records `capabilities.elicitation` at initialize, and every tool call now sends `X-Xmustard-Issuer: mcp`, with or without evidence delivery. The API holds a remember or verify made under a human approver's token through the bridge. The hold is a 403 with reason `human_presence_required`, an `action` text and its SHA-256 `action_digest`, and it is audited.
  - The text (`workspaceops.DescribeRemember` and `DescribeVerify`) names the write and every field that changes what is stored, as the write runs it. The op or outcome is normalized exactly as `Remember` and `VerifyContextOutcome` normalize it (shared `NormalizeRememberOp` and `NormalizeVerifyOutcome`); an unknown op or outcome, an unsafe id or a missing entry answers the write's own 400 or 404 and asks no one. Only the fields the op reads are shown: title, content, permission, paths, supersedes, WS-20's kind, topic and tags, require_verification and expires for a proposal; base_revision, old_string, new_string, content, description, expires and reason for an edit; the entry's served revision, status, lifecycle and title plus the reason for retire and restore. A verdict names its outcome, the revision it is cast on (the served one when the call names none), that revision's title and text or, for a pending edit, its diff, the target, the evidence handle and the note. Free text is redacted as the write would store it, cut at 300 bytes, and quoted with `strconv.Quote`, so it cannot fake a line of its own; a cut value carries its size and SHA-256.
  - The bridge asks the human only when the client declared elicitation under protocol 2025-06-18 and finished initialization. It sends `elicitation/create` with the text (refused, never cut, past 32 KiB) and one required boolean, `confirm`, that has no default, waits up to 10 minutes, and repeats the call once with `X-Xmustard-Approval: elicitation` and `X-Xmustard-Approval-Digest` only on `accept` with `confirm: true`.
  - The API runs the repeated call only when the digest equals the digest of the text it describes for that call, so what runs is what the human read. A verdict is pinned to the revision shown (feedback outcomes take no revision and are bound by the digest alone). When the memory moved in between, the API holds the call again and the bridge reports that the write changed after the human read it.
  - In every other case nothing is recorded and the tool result says why: a decline, a cancel, a missing or false confirm, a malformed answer, a client error or timeout, or a client without the capability. Other refusals pass through unchanged.
  - Both MCP entry points deliver tool results as evidence envelopes. The hold is a protocol answer the bridge acts on, so the delivery middleware passes it through as the 403, like an admission refusal, and never captures it (review round 1: the envelope used to swallow it, so the human was never asked). No tool, argument or tools/list byte was added (`tools_list_budget_test` passes unchanged in both profiles). The `xmustard://docs/tools` resource gained a short "Human approvers" paragraph.
- *Merge attestation (WS-71, PAR-REV-14).*
  - `xmustard-ops review approve <ws> --base REF [--head HEAD] [--review ID]... [--note]` appends a govstore `merge_approval` event (a new event type; no migration). It binds the workspace, the repository (its canonical root), the base ref, the merge base, the head, `diff_sha256` and `diff_bytes`, and the review record ids (at most 32 safe ids). It also records the assurance and the label `attestation only: xMustard never merges, never changes branch protection and never posts to a pull request; enforce merges with branch protection`. A head already contained in the base is refused.
  - `review revoke --approval SEQ --reason TEXT` appends `merge_approval_revoked`. Any human approver may revoke, since revoking only removes trust. The reason is required, and revoking twice is a conflict.
  - `review gate --base REF [--head REF]` reads only and takes no token. `--base` is required: it names the merge the gate is asked about, such as the remote branch a pre-push hook updates. It prints the state and exits 0 when current, 3 when there is none and 4 when stale. Any error, including an unknown base or a state this build does not know, exits 1.
  - An attestation is current only when it binds the change from the merge base of the requested base and the head: the same repository, merge base, head and digest. An attestation made against another base covers another diff, so it is stale for this merge (review round 1: an approval of `dev..feature` used to report current for merging into `main`, which also brought dev's unreviewed commits). Another head is stale without running git again. The change is diffed at most once per gate. A record that does not decode is an error, never an approval.
  - The gate re-checks the attesting principal. An attestation counts only when it is unrevoked, its recorded approver kind is human, and its approver is still a human approver scoped to the workspace in the token store. One whose token was revoked, or whose kind is not human, is counted under `untrusted` and is never current.
  - The digested diff is hardened. System and global git config are ignored, every `GIT_*` and `XMUSTARD_*` variable is removed, and hooks and the fsmonitor are off. Every option repository config could change is pinned on the command line: myers, indent heuristic, `-U3`, inter-hunk context 0, `a/` and `b/` prefixes, no renames, no relative paths, full index, binary, `--submodule=short`, `-O/dev/null`, no external diff, no textconv and no color. A test sets each of these in the repository config, plus `.gitattributes` textconv, `GIT_DIR` and `GIT_EXTERNAL_DIFF`, and the digest does not change. The diff streams into SHA-256, so memory does not grow with its size.
- *Measured (build box: Linux, 6 cores, git 2.53.0; first round).* `BenchmarkMergeApprovalStateLargeDiff` gates a current attestation over 500 new files of 4 KiB each, a 2,174,928-byte diff: 34.8 to 37.8 ms per gate (three runs of 20; two `rev-parse` runs, one `merge-base` and one `diff`), 228 to 230 KB and about 660 allocations per call. The diff streams into the digest, so the Go heap does not hold it. The lean and full tools/list caps did not change: no schema was touched and `tools_list_budget_test` passes unchanged. A tool call without evidence delivery now also sends the 22-byte issuer header, which evidence delivery already sent.
- *Measured (round 2, same box).* The same benchmark gates with `--base main`: 31.7 to 35.6 ms per gate (three runs of 20; still two `rev-parse` runs, one `merge-base` and one `diff`, since the base is resolved once and passed to the diff), 231 to 234 KB and about 700 allocations per call; the extra 40 allocations are the token-store read that re-checks the attesting principal. A held write costs the describer one store view (and, for a verdict on a pending edit, one revision diff) and only for a human approver's call through the bridge; an agent's call does no extra work. The delivery middleware adds one context value per delivered call. The lean and full tools/list caps did not change.
- *Checks.* The full Linux gate (`xm-remote-check.sh all -count=1`: Rust release build, tests and clippy, then go vet and every Go package) passes, with 6 clippy warnings, the same as the base `fd083c2`. No Rust code changed. Round 2: the gate passed again at `096bf14` (the review fixes plus the WS-20 merge) with `remote_exit=0`, every Rust test binary and Go package green and 6 clippy warnings. The last change after it (restore is the only op whose confirmation reads an inactive entry) passed go vet and the focused `Human|Merge|Confirmation` tests on the box. Removing the delivery slot makes `TestHeldWriteReachesTheBridgeThroughEvidenceDelivery` fail with the swallowed hold, which is the finding it covers.
- *Deviations.*
  - The records are advisory by default. The governance store is a file the user's processes can write, so an attestation or a human verdict is evidence of a decision, not an enforcement; `user_presence` means only that the token was typed at the terminal (there is no keychain integration). Enforcement stays with branch protection, and no signed export (such as a git-notes ref) is written, so `review gate` serves the human's own pre-push hook, not CI.
  - PAR-REV-14's ground member `review.human_approval` is not added. It would run git on every ground, and the critic orders it after the workstreams that create `grounding_review.go` (WS-67 and WS-68). Until then an agent reads the state with `xmustard-ops review gate`, which reads only and takes no token.
  - Merge attestations bind review record ids as given. The review records store (WS-66) does not exist yet, so their authors cannot be checked for distinctness from the approver.
  - The MCP presence rule covers remember and verify, the two governed-memory write tools. The approver's lifecycle routes (DELETE and restore) are HTTP only and not reachable over MCP.
  - The digest binds the text, and the text binds the revision a verdict is cast on. A remember write reads the entry's state when it runs, so a change between the confirmed retry's check and its store transaction is not held again; the window is one request.
  - There is no keychain or OS-level secret store: a presence-only token keeps agents from being handed the token, not from finding it where the human put it. A human who stores it in a file an agent reads is back to advisory in fact, though the label cannot tell.
  - A human approval is one distinct verdict and does not override the quorum. WS-31 decides which protected paths also require one; it can recognize one as a counting approve vote with `principal_kind` human, and audit its surface and assurance under `provenance.approval`.
  - WS-20 landed on `feat/parity-v2` after the first round (merged here at `3ab0b17`) with recall `status=awaiting_me`, ranked by relevance for an agent. The ops queue keeps its own view and the same awaiting_me rule (not written by the approver, not voted on, not blocked by the owner-distinct policy): a human reviews oldest pending revision first, and the queue renders an edit's diff and the human approvals already cast, which recall does not.
  - The hardened diff lives in `merge_approval.go`; WS-64 (the shared hardened diff module, folded into WS-35) should absorb it.
  - Human tokens are minted through `POST /api/auth/tokens` with `"kind":"human"`. `xmustard-api mint-token` still mints agent tokens only.
- *Files.* New: `api-go/internal/workspaceops/human_approval.go`, `merge_approval.go` and their tests; `api-go/internal/mcpserver/elicitation.go` and its test; `api-go/cmd/xmustard-api/human_presence.go` and its test; `api-go/cmd/xmustard-ops/approval.go`, `approval_tty_unix.go`, `approval_tty_darwin.go`, `approval_tty_linux.go`, `approval_tty_other.go` and `approval_test.go`. Changed:
  - `govstore/events.go` (two event types) and `govstore/store.go` (`Actor.Approval` in the provenance);
  - `workspaceops/memory_propose.go` and `memory_provenance.go` (`ContextActor.Approval`);
  - `xmustard-api/main.go` and `memory_routes.go` (the label and the two presence checks);
  - `mcpserver/server.go`, `dispatch.go`, `evidence.go` and `docs_resource.go`;
  - `xmustard-ops/main.go`, three additive lines that dispatch to `approval.go`.
  - Review round 1: `workspaceops/auth.go` (the presence-only flag), `human_confirm.go` and its test (new), `memory_verify.go` (the shared normalizers), `xmustard-api/evidence_routes.go` (the hold passes through delivery), `health_budget.go` (a presence-only token gets the public view).
- *Review round 1 (4 majors, 5 minors; all fixed).*
  - Major: elicitation never ran under evidence delivery. Fixed with a request-context slot the delivery middleware reads (`withHeldWriteSlot`); `TestHeldWriteReachesTheBridgeThroughEvidenceDelivery` drives a real `mcpserver.NewEvidence` session against the route server, and fails with the slot removed.
  - Major: `review gate` did not bind the base. Fixed with a required `--base`; `TestMergeGateBindsTheRequestedBase` is the dev/main case.
  - Major: `user_presence` could be forged by typing a file-held token into a pty. Fixed by binding the assurance to presence-only tokens, which are refused elsewhere; an ordinary typed token is now advisory. The residual pty replay of a presence-only token is documented above and in the code.
  - Major: the confirmation hid what the write does. Fixed with `DescribeRemember`, `DescribeVerify`, the digest binding and the pinned revision.
  - Minor: the shown op or outcome could differ from what runs. Fixed with the shared normalizers; an invalid value is a 400 before anything is asked.
  - Minor: `mcp_elicitation` was chosen from the approval header alone. It now also needs the MCP issuer header.
  - Minor: the gate trusted historical attestations without re-checking the principal. It now re-checks kind and token (see above).
  - Minor: Ctrl-C at the prompt left echo off. The signal handler restores the terminal.
  - Minor: the queue was in entry cursor order. It is now in pending-revision age order before the limit.

### WS-58 — Daemon lifecycle (critic addition)

launchd/systemd unit or socket activation via xmustard-ops setup, health-checked auto-start, crash restart, log rotation, schema migration with connected clients; govstore backup (VACUUM INTO), quick_check on open, restore.

### WS-59 — impact op=rename|delete plans and search mode=exact|regex (critic addition)

Read-only rename/delete impact plans (IMP-09) and exact/regex search (RET-09) matching GitNexus rename and grep-class surfaces.

### WS-60 — Framework entry-point extractors and extra language packs (critic addition)

Route/DI extractors (Django, FastAPI, Next.js, Nest, Spring, Laravel, tRPC, Expo) seeding flow entry points; Vue, Dart, Objective-C packs; reimplemented from design (GitNexus is PolyForm NC).

### WS-61 — Personalized PageRank repo map fitted to a token budget (critic addition)

aider-style ranked map over the resident CSR, personalized by files in play and mentioned identifiers, binary-searched to max_map_tokens; surfaced via ground orientation or explain(path=".").

### WS-62 — Path-scoped client rule-file export (critic addition)

Export verified memories to .clinerules paths:, Cursor rules, .claude/rules, AGENTS.md sections; marker-bounded, revision-stamped; zero RSS delivery path.

### WS-63 — Evaluation corpus authoring (critic addition)

Tasks with hidden oracles validated to fail on baseline and pass on the reference, on Apache/MIT repos; budgeted real-model run as an operator step.


## Open Code Review adoption (added 2026-09-25)

Adopted per the critic's reduced plan in requirements §13.6. No tenth MCP tool. Review evidence informs the human merge decision and never constitutes approval.

| ID | Disposition | Title / change |
|---|---|---|
| WS-64 | **Folded into WS-35** as its first deliverable | Shared hardened diff and scope module (OCR's three diff modes plus full git isolation: GIT_CONFIG_NOSYSTEM/GLOBAL, env scrub, pinned diff.algorithm/indentHeuristic/interHunkContext/orderFile, core.fsmonitor=false, --no-ext-diff, --no-textconv, --end-of-options). One reconciled `scope` vocabulary for impact. |
| WS-65 | **New, wave 2 (should)** | Deterministic finding anchoring (port of OCR resolver/hunk/relocation, Apache-2.0 with NOTICE and kept SPDX headers): sliding-window snippet anchoring to new/old side, re-filing, partial Ground-A check; bounded existing_code (≤40 lines / 4 KB); exposed as a library for WS-27 quoted-code anchors and WS-28 re-anchoring. Findings arrive via evidence_handle or a findings file, not nested MCP arrays. |
| WS-66 | **New, wave 2, shrunk (should)** | Review findings store on existing govstore events/outcomes/jobs/anchors tables with a subject_kind column; OCR's exact dedupe (same path, strict IoU > 0.6, single-line never equals multi-line) plus normalized existing_code hash; positional-only overlap is `possible_duplicate`, never corroboration. Coverage denominator counts all changed files; excluded files are listed as not_reviewed(reason). |
| WS-67 | **Split** | Structured-argument transport stays scalar (evidence_handle / findings_file); diagnostics `source=review` becomes a WS-47 sub-deliverable; verify gets a subject-conditional `approve` default and rejects approve for subject=change. |
| WS-68 | **Kept, wave 3 (should)** | impact(op='review') LLM-free review packet; depends on WS-52 and WS-55 (or v1 limited to workspace and commit-at-HEAD); token acceptance: default packet ≤ 1.5× raw -U3 diff bytes on fixtures, expansion on demand. |
| WS-69 | Deferred (could) | Review rules as governed path-scoped memory; checklist pack only after editing the 7 OCR docs that name OCR-only tools, with modification notices. |
| WS-70 | Kept, wave 3 (could) | SARIF 2.1.0 export; OCR JSON import only from `--format json --output`, deferred until WS-65/66 prove out. |
| WS-71 | **Folded into WS-57** | Human merge-approval attestation bound to the reviewed diff; labelled attestation (enforcement is branch protection); approver token unreadable by agent processes or the record is advisory. |
| WS-72 | **Folded into WS-05** | Secret-path denylist in the redact package (Go list plus Rust golden copy) across read and injection surfaces. |
| WS-73 | Deferred | Recurring findings to consolidation; needs a defined normalized snippet shape and distinct token principals. |
| WS-74 | **Folded into WS-04 / WS-42** (must) | Cache-stable deterministic rendering keyed by (tool, canonical args, repo revision, memory-store revision, renderer version); live-session sections keyed separately; covers search's volatile fields. |
| WS-75 | **Folded into WS-23 / WS-40 / WS-44** | Review workflow packaging and OCR interop recipe; fake-driver e2e; 8-concurrent-client admission and RSS test for external MCP clients such as OCR. |
| WS-76 | **Folded into WS-63** (optional corpus) | AACR-Bench pilot (5–10 PRs), PR-cluster bootstrap, judge agreement, numeric spend cap, licence gate; not part of the WS-50 parity gate. |

Budget: review features sit behind a profile/build tag that is off in the lean default until gate v2 (WS-10) shows headroom.


### WS-65 — Deterministic finding anchoring (OCR port)

Port open-code-review's resolver, hunk parser and re-filing (Apache-2.0, with a NOTICE and upstream's SPDX lines kept). It anchors a quoted snippet to the new or old side of a hunk, re-files a snippet to another changed file, runs a partial Ground-A check and bounds existing_code at 40 lines or 4 KB. It ships as a library for WS-27 (quoted-code anchors) and WS-28 (re-anchoring). Findings arrive through an evidence_handle or a findings file, never as nested MCP arrays. There is no tenth tool, and review features sit behind a profile or build tag that is off in the lean default. Requirements: PAR-REV-04 and PAR-REV-05, as corrected by the critic in requirements §13.6 (the WS-65 line).

**Implementation record (branch parity/ws-65, 2026-09-28).** These notes record what was built and measured, and where it differs from the text above. The source was the local clone `research/open-code-review` at 486022d (`internal/diff/hunk.go`, `resolver.go`, `relocation.go`, `parser.go`, `internal/llmloop/loop.go:708-768` and their tests).
- *Library (`api-go/internal/anchor`).*
  - `NewSnippet` normalizes quoted code as upstream: each line trimmed, one leading `+` and one leading `-` stripped, blank lines dropped. It refuses code over 40 lines or 4,096 bytes (`snippet_too_large`) and code with no non-blank line (`no_snippet`).
  - `NewFile(old, new, diff, head)` parses one file's hunks once. `File.Resolve` walks a table of three tiers: the hunks' new side (`exact_new`), their old side (`exact_old`), then the whole file at head (`file`), whose content is loaded on first need.
  - The first tier holding the snippet decides. One match anchors it; several are `unanchored` with reason `ambiguous` and a candidate count.
  - Matching is Knuth-Morris-Pratt over normalized lines, so one search costs at most two line comparisons per line searched, whatever the snippet repeats. A randomized test checks it against upstream's sliding window.
  - A head the caller did not read (over a bound, binary) was never searched. A snippet no hunk holds is then `unanchored` with reason `head_unread`, never `not_found`, and `Anchor.Unchecked` reports it.
  - `Set.PlaceAll` (and `Set.Place` for one snippet) follows upstream's order. It tries each finding's own file, then re-files a snippet that file does not hold (`relocated`, with `refiled_from`) only when exactly one other changed file holds it. Zero gives `not_found`; two or more give `ambiguous_across_files`. A snippet whose own file is `head_unread` is not re-filed, and while another file's head is unread no hit is unique, so the result is `head_unread`.
  - `PlaceAll` tries every finding's own file before it re-files any, and re-filing first reads each changed file's head in diff order. Under a shared head budget, the files the findings name are read first, and re-filing sees the same heads whatever order the findings come in.
  - A finding filed against a file the change does not touch is looked for in that file at head first, through the caller's `Set.Outside` (the whole-file tier, as `Locate`), and only then re-filed. It anchors with status `file`, and `in_scope` and `in_changed_hunk` are `no`. `Outside` is called once per path, and the file's normalized lines are kept for the next finding on it.
  - A `Set` looks a path up at head before it looks it up as an old path. When a renamed file's old name is a new file's path in the same change, a finding on that name is the new file's in either diff order, and `Touches` finds an old-side anchor's file by its side.
  - `Set.Touches` answers `in_changed_hunk`: does the anchored range hold an added line on the new side, or a deleted line on the old side (blank lines included)?
  - `Locate(path, content, snippet)` is the memory quoted-code anchor for WS-27. `Reanchor(prev, content, snippet)` is refs_stale re-anchoring for WS-28: one match moves the anchor, and among several matches the one still at the previous line keeps it.
  - Every anchor carries a status, a side, a reason, a candidate count and `refiled_from`. The package reads no file and runs no git.
- *Changes from upstream.* Each ported file's header lists its changes.
  - Several matches anchor nothing. Upstream takes the first match, so its two "first match wins" tests now expect `ambiguous`.
  - Hunk sides skip blank lines, as the whole-file pass already did. A snippet that spans a blank line inside a hunk now anchors there instead of falling through to the file tier.
  - A hunk ends when its header's line counts are used up. A trailing newline no longer adds an empty context line (upstream's CRLF test expected one), and text after a hunk is not read as part of it.
  - A file outside the change is searched at head before re-filing. Upstream holds no diff for such a file and re-files at once, so code that is in the file the comment names is reported absent; PAR-REV-05's `code_present` asks about the subject file at head.
  - An old-side anchor names a renamed file's old path.
  - During re-filing, each other file places the snippet with `Resolve`, as if the finding had been filed there. The first tier holding it decides, so a hunk match counts once even when the same code also sits elsewhere in that file at head, which is the rule a finding's own file follows. A file where the deciding tier holds the snippet several times counts all of them toward the cross-file total, so it cannot make a hit elsewhere look unique.
  - Matching is Knuth-Morris-Pratt instead of a sliding window, with the same matches.
  - An unread head is `head_unread`, not an empty file, and re-filing counts it as a possible home.
  - `PlaceAll` anchors a batch in two passes, own files first.
  - A path at head wins over another file's old path. Upstream's `diffByPath` lets whichever file comes later in the diff take a shared name.
  - The model re-location step (`relocation.go`) is not ported, as PAR-REV-04 says.
- *Findings (`api-go/internal/review`).*
  - `Decode` accepts three shapes: a JSON array, `{"findings": [...]}`, or open-code-review's `--format json --output` file, from which it takes `comments` and ignores the rest of the envelope. Either array may arrive as a JSON string; it is decoded once and the repair is recorded.
  - The item schema is closed: xMustard's fields plus OCR's `start_line`, `end_line` and `thinking`. `thinking` is dropped and counted.
  - Member names are read exactly. Go's `encoding/json` matches a name in any case (Unicode folding included) and lets the last repeat win, so an item member, or an envelope's `findings` or `comments`, written in another case or repeated refuses the input. Another reader of the same bytes, which `source.sha256` names, then sees the same findings. The names come from the item type's tags.
  - The whole input is refused when it is over 4 MiB, holds more than 50 findings, or any finding has an unknown member, a wrong type, no content, or a path that is empty, absolute, escaping or contains a NUL.
  - Other problems are recorded on the finding, which is kept:
    - an unknown category or severity becomes `other` or `low`, and case is folded;
    - content over 2,000 characters is cut;
    - an existing_code or suggestion_code over the snippet bound is dropped, and the finding is `unanchored` with `snippet_too_large`;
    - when the producer's own line range differs from the anchor, the note says the anchor replaced it.
  - `SplitDiff` splits git diff output into files. A path comes from the `---`/`+++` lines (C-quoted names decoded, `/dev/null` for the missing side, git's trailing tab after a name with a space dropped). A binary or mode-only file takes its path from its `diff --git` line.
  - `Anchor` places every finding with `PlaceAll` and computes its checks. Each check is `yes`, `no` or `unknown`:
    - `code_present`, a partial Ground A: the quoted code is in the file the finding is filed or re-filed against, at head or on the old side. OCR's Ground A, judged by a model, asks whether the code is absent from the subject file's diff.
    - `in_changed_hunk`.
    - `in_scope`: the file is in the diff.
    - `symbol_resolved`, always `unknown`.

    A finding that fails `code_present` is labeled `unsupported` and kept. When a head the answer needed was not read (`head_unread`), `code_present` and `in_changed_hunk` are `unknown` and the finding is `unchecked`, never `unsupported`. Counts are given per status (`by_status`) and per support label (`by_support`).
- *Surface: the `review` build tag, off by default.*
  - `workspaceops.AnchorReviewFindings(ctx, dataDir, ws, base, head, batch)` anchors against the change merge approval digests. `observeChange` tees the hardened WS-57 diff from the merge base of `base` to `head` into the anchoring, so the result's `change.diff_sha256` equals what `review approve` binds. The tests check that equality.
  - Head content for the whole-file tier comes from one `git cat-file --batch`. It runs under the same git isolation (now `reviewGitCommand`, shared with `reviewGit`) and starts only when a snippet reaches that tier. The same reader is `Set.Outside`: a path a finding names is resolved in head's tree (`<head>:<path>`, a cleaned repository-relative path), never on disk, and a path head does not hold is counted in `head_reads.missing`.
  - Bounds: the diff text at 8 MiB and 262,144 lines (over either the anchoring is refused, since this command takes no heavy slot), one file at head at 1 MiB, and all head content at 16 MiB and 524,288 lines. A file over a head bound, a binary file, or a name the batch protocol cannot carry is searched in its hunks only, `head_reads` lists it, and a finding it could decide is `head_unread` and `unchecked`. The line bounds exist because memory and search time grow with lines, not bytes: a parsed line costs 24 bytes, or 48 for a context line, which is on both sides of its hunk.
  - A failed head read fails the anchoring rather than return anchors that needed it.
  - `close` stops the batch reader before it waits. After a malformed answer, the reader may still be writing output nobody reads, and the wait would otherwise last until the 2-minute timeout.
  - `xmustard-ops review anchor <ws> --base REF [--head REF] (--findings FILE | --evidence HANDLE [--token-file PATH])` prints the change, source, files, findings with anchors and checks, counts, normalizations, head reads and the label `evidence only: ... no review result approves a change`. It stores nothing.
  - An evidence handle is read the way the evidence store checks it:
    - in open mode (no token store), at workspace scope;
    - otherwise a valid token is required (`--token-file`, then `XMUSTARD_API_TOKEN`), and it must not be presence-only and must be scoped to the workspace. The read is then that principal's, so only its own captures are readable.

    An original over 4 MiB is refused.
- *Off by default.*
  - `go list -deps` finds neither package in the default `xmustard-api`, `xmustard-mcp` or `xmustard-ops`.
  - With `-tags review`, `xmustard-ops` grows by 123,800 bytes (25,069,968 to 25,193,768). `xmustard-api` grows by 11,136 bytes, because it links workspaceops' tagged file, which nothing in the API calls. Re-measured on the merged base in the third session: `xmustard-ops` grows by 123,904 bytes (25,241,496 to 25,365,400) and `xmustard-api` by 7,472 bytes (31,075,608 to 31,083,080).
  - `review` usage now lists the registered subcommands. An untagged test asserts that `review anchor` is absent.
  - `make check-backend` (and AGENTS.md) now also runs `go vet -tags review` and the tagged `Review|Merge` tests on `cmd/xmustard-ops` and `internal/workspaceops`.
  - No MCP tool, argument or tools/list byte changed.
- *Licensing.* This is the repository's first Apache-2.0 code:
  - `NOTICE` is new. It states that the repository is mixed-licence and that a file without an SPDX header is MIT, and names the translated files and upstream commit.
  - `third_party/open-code-review/LICENSE` is upstream's licence text.
  - Each ported file, tests included, keeps `SPDX-License-Identifier: Apache-2.0` and upstream's copyright line, adds xMustard's, and lists its changes. The package's own files carry `SPDX-License-Identifier: MIT`.
  - README gains a License section.
- *Fixtures.* `anchor/testdata/fixtures.json` holds 26 deterministic cases, with no model, git or clock:
  - CRLF in the diff, the snippet and the head content;
  - ambiguity across two hunks, in the whole file and across files;
  - every tier, a hunk match beating the same code elsewhere, a blank line inside a hunk;
  - deleted and added files, re-filing (including from a file outside the change, and by the deciding tier of the other file);
  - heads that were not read: `head_unread` in the file itself, a hunk match without the head, and no re-filing while another file's head is unread;
  - indentation versus interior spacing, the 40-line bound either side, a blank snippet.

  Upstream's hunk, resolver and relocation tests are ported in table form.
- *Measured (build box: Linux, 6 cores).*
  - `BenchmarkPlace` parses a 200-file change (300 lines at head each) and anchors 50 snippets: most in a hunk, a fifth only at head, and a tenth in no file, so re-filing reads every head. It took 4.6 to 5.9 ms per op at `eadf3dd` and 4.8 to 6.2 ms at `448f718`, with 3.6 MB and 7,620 allocations either way (3 runs of 20 each).
  - `BenchmarkReviewAnchor` runs end to end over 200 files of 300 lines with two lines edited in each. Per anchoring it runs two `rev-parse`, one `merge-base`, one diff of 113,708 bytes, and one `cat-file` that reads 1,631,460 bytes, which is every head, because 5 of the 50 findings are found nowhere. It took 46.3 to 48.3 ms per op at `eadf3dd` and 41.3 to 42.5 ms at `448f718`, with 8.4 MB and about 14,400 allocations either way (3 runs of 10 each). Every finding there names a changed file, so `Set.Outside` is never called.
  - Third session: `BenchmarkPlace` took 5.4 to 6.5 ms per op, with 3.65 MB and about 7,645 allocations (3 runs of 20). `BenchmarkReviewAnchor` took 40.8 to 43.5 ms per op, with 8.37 MB and about 14,420 allocations (4 runs of 10), at a box load average of about 7 on 6 cores. An earlier run at a higher load measured 55 ms, with the same bytes and allocations.
  - Fourth session (review round 1), the worst case at the bounds. `BenchmarkPlaceAtTheBounds` builds 16 files at every bound: a diff of 262,144 lines and 8 MiB, all context, and 524,288 head lines and 16 MiB. Every line is the same 31 bytes, and 50 findings of 39 such lines and one other are found nowhere, so each searches every side and head. A sliding window would compare 40 lines at every line. It took 861 to 872 ms per op, allocated 83 MB and 1,455 objects, and kept 50.3 MiB live once the findings were placed (3 runs of 10, box load about 10). Before the fix, the reviewer's probe, with the byte bounds only and 2-byte lines, used about 280 MB of heap and 1.3 s for the first finding and 52 s for 49 more. Preallocating each hunk side, capped by the lines the hunk holds, took the allocation from 126 MB to 83 MB. `BenchmarkPlace` took 4.8 to 6.5 ms with 2.1 MB and about 5,895 allocations (3 runs of 10). `BenchmarkReviewAnchor` took 47.5 to 50.4 ms with 6.7 MB and about 10,620 allocations, against 46.7 to 48.6 ms with 8.35 MB and about 14,420 for the previous commit run back to back under the same load (load about 15). Runs of one version at a time at that load measured 98 to 154 ms for this commit and 103 to 113 ms for the previous one, so the time is git under contention.
  - The anchoring runs in the short-lived ops process, not the daemon. The ledger line is 0, with a note that records the worst case above, which WS-67 must re-measure before it moves anchoring into the daemon.
- *Checks.*
  - The full Linux gate passes at `eadf3dd` with `remote_exit=0`: `xm-remote-check.sh all -count=1` runs the Rust release build, tests and clippy, then go vet and every Go package. Clippy reports 6 warnings, the same as the base; no Rust code changed.
  - `go vet -tags review` and the tagged `Review|Merge` tests pass on `cmd/xmustard-ops` and `internal/workspaceops`.
  - The bench unit tests pass (74 run, 3 skipped). The ledger consistency test now expects WS-65 after WS-63.
  - Second session (`d0267cd`, `448f718`): `feat/parity-v2` at `845868d` (the WS-15 watcher, the v0.1.0 release notes and the Homebrew formula) was merged. It touched the architecture map, this plan, the budget ledger and its test, and merged cleanly: the ledger keeps WS-15's note and WS-65's line. The full Linux gate then passed at `448f718` with `remote_exit=0`: every Rust test binary and Go package green, 6 clippy warnings (the base's count). `go vet -tags review`, the tagged `Review|Merge` tests, the untagged `Review|Approval` ops tests and the bench unit tests (76 run, 3 skipped) pass, and `go list -deps` still finds neither package in the three default binaries.
  - Third session (`09d2692`): a pre-review pass fixed three defects. First, a renamed file's old name that a new file reuses in the same change found whichever file came later in the diff. Second, repeated findings on one file outside the change read it at head each time, spending the 16 MiB head bound, so later findings on a large file became unsupported. Third, `close` could wait out the 2-minute timeout after a malformed batch answer. New tests: the reused name in both orders, `Outside` asked once per path, three findings on one outside file under a head bound that fits it once, and a presence-only token refused for an evidence read. `feat/parity-v2` had not moved from `845868d`. The full Linux gate passed with `remote_exit=0`: every Rust test binary and Go package green, including `internal/anchor` and `internal/review`, and 6 clippy warnings (the base's count). `go vet -tags review`, the tagged `Review|Merge` tests, the untagged `Review|Approval` ops tests and the bench unit tests (76 run, 3 skipped) pass, and `go list -deps` finds neither package in `xmustard-api`, `xmustard-mcp` or `xmustard-ops`.
  - Fourth session, review round 1 (`75b251c`, `8674508`, merge `bb5185c`): the four findings were fixed as recorded above. Mutation runs on the build box show each fix is guarded by a test. These fail at least one test each: placing in one pass (the end-to-end order test included), re-filing past an unread head, reading an unread head as not found, decoding without exact member names, and dropping either line bound. `feat/parity-v2` had moved from `845868d` to `db04d40` (WS-26 release packaging, WS-FIX-04 client adapters). It touched the Makefile, README, the architecture map and this plan beside WS-65's lines, and merged cleanly. The full Linux gate then passed with `remote_exit=0`: every Rust test binary and Go package green, 6 clippy warnings (the base's count). Two earlier gate runs, at a box load of about 15, each failed one Rust memory test that this branch does not touch; its Rust tree is identical to `feat/parity-v2`. The tests were the snapshot-swap generations check in `index_query` and the 25 MiB build peak in `index_build`. Run alone, both passed (peak 17.1 MiB). `go vet -tags review`, the tagged `Review|Merge` tests, the untagged `Review|Approval` ops tests and the bench unit tests (76 run, 3 skipped) pass, and `go list -deps` finds neither package in `xmustard-api`, `xmustard-mcp` or `xmustard-ops`.
- *Files.*
  - New:
    - `api-go/internal/anchor/`: `hunk.go`, `resolve.go` and `relocate.go` and their tests (Apache-2.0), plus `locate.go`, `doc.go`, `anchor_test.go` and `testdata/fixtures.json`;
    - `api-go/internal/review/`: `findings.go`, `diffsplit.go`, `check.go` and `review_test.go`;
    - `workspaceops/review_anchor.go` and its test, and `xmustard-ops/review_anchor.go` and its test, all under the review tag, plus `xmustard-ops/review_default_test.go`;
    - `NOTICE` and `third_party/open-code-review/LICENSE`.
  - Changed:
    - `workspaceops/merge_approval.go`: `observeChange` and `reviewGitCommand`, with no behaviour change;
    - `xmustard-ops/approval.go`: the usage lists the registered review subcommands;
    - `Makefile`, `AGENTS.md`, `README.md`, `docs/ARCHITECTURE.md`, the PAR-REV-04 and PAR-REV-05 rows of the requirements, `scripts/bench/budget_ledger.json` and its test.
- *Deviations.*
  - **Go, not Rust.** The integrator's §13.6 proposal put the anchoring in `rust-core/src/review/`. The critic's binding WS-65 correction exposes it as a library for WS-27 and WS-28, which are Go. A Go-to-Go port keeps upstream's SPDX lines and structure, and anchoring is pure string work that needs no index. A Rust diff module (WS-64 folded into WS-35) can pass its diff text to it, or port the fixtures.
  - **One diff mode.** Only range mode is supported: the merge base of `--base` to `--head`, the change merge approval binds. Workspace, staged and commit modes belong to WS-64/WS-35. A deleted file is anchored on its old side only.
  - **Evidence, not stored.** Nothing is persisted, and `verify` does not take findings yet. WS-66 owns the store; WS-67 owns the scalar `verify(subject='change', evidence_handle|findings_file)` transport, and must confine a findings-file path, which the operator CLI takes as given.
  - **`symbol_resolved` stays `unknown`.** The library runs no index query, so WS-67/WS-68 fill it from the resident index. `in_scope` means "in the diff": the REV-02 selection and its exclusion reasons do not exist yet.
  - **Kept, not refused.** Content over 2,000 characters is cut rather than refused, and an oversized existing_code drops the snippet but keeps the finding. Both are recorded, so one long OCR comment does not reject a batch.
  - **Operator reads.** The ops CLI reads the evidence store directly, as every ops command reads the data directory. It authorizes the read as a token's principal only when auth is configured.
  - **Checks can be unknown.** PAR-REV-05 defines `code_present` as present or absent. A head over a bound was never searched, so for a finding it could decide `code_present` and `in_changed_hunk` are `unknown` and the support label is `unchecked`. All four checks are now `yes`, `no` or `unknown` strings, where `code_present`, `in_changed_hunk` and `in_scope` were booleans; nothing consumes them yet (WS-66 and WS-67 do).
  - **Order still decides which named heads fit.** Every finding's own file is read before any re-filing, in finding order. When the heads the findings name pass the 16 MiB or 524,288-line budget by themselves, the later ones are `head_unread`, which is unknown, never a false fact.
  - **Re-filing keeps the tier rule.** The review asked either to count every occurrence in a candidate file or to correct the text. The text is corrected: a candidate file places the snippet as a finding filed against it would, so a hunk match beats the same code elsewhere in that file, the rule the fixture `a hunk match wins over the same code elsewhere in the file` sets for a finding's own file. A new fixture pins the re-filing case.
