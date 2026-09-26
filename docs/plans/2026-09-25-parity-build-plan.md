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

### WS-57 — Human-approval surface (critic addition)

xmustard-ops approve|reject|queue bound to a human-approver token, optional MCP elicitation; precondition for protected paths in WS-19 and WS-31.

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

