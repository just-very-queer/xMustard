# xMustard Full-Parity Requirements and Build Plan

As of 2026-09-25. Repository HEAD `7ab61a0` (`feat/product-v1`). This is a planning document, not a status claim. Every "fits the budget" statement below is an engineering estimate built from component measurements. The whole-system number stays unproven until the parity-scale gate (PAR-EVAL-05) runs.

---

## 1. Purpose

The owner wants xMustard to reach full capability parity with the researched peers: GitNexus, Serena, Augment Context Engine, Sourcegraph, Mem0, Letta/Letta Code, and Zep/Graphiti. The owner also wants the proposed harness and context-layer work: tool-context reduction, recoverable projections, client adapters, an optional local helper model, and a lighter-than-OpenHands harness modeled on Pi. It must all stay local, with no Docker, and inside the 50–100 MB process-tree target. The owner believes it fits the memory budget.

This document is the canonical requirements set for that goal. It covers:

1. Every source-evidenced requirement from the eight research readers, deduplicated into one matrix with stable IDs (`PAR-<category>-NN`).
2. The processes (pipelines and lifecycles) that implement those requirements.
3. A resident memory-budget allocation, with the conditions under which parity fits.
4. What cannot fit in the default budget, and how each such item becomes optional or external.
5. Open decisions.
6. The build plan: waves, collision map, and workstream conventions. The machine-readable workstream list is delivered alongside this document.

"Parity" means matching capability on representative agent tasks: localization, impact, memory lifecycle, freshness, context cost, and footprint. It does not mean copying UIs, hosted services, or peer storage engines.

## 2. Method and sources

### 2.1 Readers

| Code | Scope | Evidence base |
|---|---|---|
| GNX | GitNexus | `research/gitnexus` @ b92c14c (npm gitnexus 1.6.12), ~324k TS lines read at source level. License PolyForm Noncommercial 1.0.0. |
| SAS | Serena + Augment Context Engine + Sourcegraph MCP | `research/serena` @ 7a29683 (application GPL-3.0-or-later, SolidLSP MIT); first-party Augment and Sourcegraph docs (no source). One local measurement: typescript-language-server 5.3.0 tree at 669.4 MB. |
| MEM | Mem0 + Zep/Graphiti | `research/mem0` @ 8d6c001, `research/graphiti` @ 47f6482, both Apache-2.0. |
| LET | Letta Code + MemGPT | `research/letta-code` @ a9ee041 (Apache-2.0); `docs/research/papers/memgpt-2310.08560.pdf`; docs.letta.com. |
| HCL | Harness and context layer | `research/pi-mono` @ 5fd446c (pinned adapter runtime 0.87.1); OpenHands @ 38f7b1b plus software-agent-sdk @ 1612a78; SWE-agent; cline; first-party docs for Claude Code hooks/MCP, Codex hooks, OpenCode plugin source, and Cursor hooks; Anthropic tool-search; LiteLLM MCP tool search; RTK, context-mode, and Headroom READMEs; Needle3 and Model2Vec model cards; arXiv 2508.21433. |
| CB | Owner's Cursor bridge | `research/cursor-bridge` (Go module `cursorproxy`), pulled from `just-mini:/Users/for_home/Developer/cursor`. The owner's code, with no LICENSE file. |
| XM | xMustard current | HEAD 7ab61a0 plus the in-flight worktrees surveyed read-only. |
| MB | Memory-budget engineering | Probes under `/private/tmp/claude-501/budget-probe` on an M1 MacBook Air (8 GB, macOS 27): release core d644743b, Go API/MCP binaries, SQLite, tantivy, usearch, fastembed, model2vec, tree-sitter, and LSP probes. |

### 2.2 Rules used for this synthesis

- **Dedupe.** Requirements describing the same capability were merged. The Origin column lists every contributing reader ID, and §11 maps every reader ID to a PAR ID. Where readers rated priority differently, the higher rating wins unless a reader gave a budget, license, or scope reason. The reason is then recorded.
- **Priority.**
  - `must`: core to the product thesis (governed, verified, drift-checked shared memory; grounding; narrow context; reduction) or table stakes in the category.
  - `should`: a clear parity gain at reasonable cost.
  - `could`: niche, or dependent on later precision.
  - `skip`: hosted-only, UI-only, out of scope, license-blocked, or budget-violating. The reason is always given.
- **MiB column.** Resident cost if implemented natively in Go/Rust, on a ps-RSS MiB basis for the xMustard-owned tree, written `steady / peak`. `T` means transient inside the single heavy slot (PAR-RT-04). `ext` means an external process that is reported separately and never counted in the gate. Values are design estimates unless marked *measured*.
- **Surface rule** (VISION: "deepen existing tools first"). Every requirement names which of the nine tools it deepens (ground, recall, remember, verify, search, explain, impact, diagnostics, why_failed). A new surface is allowed only when:
  - (a) MCP structurally cannot reach the seam, such as native tool results or compaction boundaries;
  - (b) the budget requires it, as with the transport change; or
  - (c) it is operator-only (CLI or evaluation).
  Each new surface carries its justification in-row.

### 2.3 Corrections and reader disagreements, resolved against source

1. **`wiki.rs` and `goalruntime.rs` are not dead.** `api-go/internal/rustcore/knowledge.go:16` calls `wiki`, and `api-go/internal/rustcore/goals.go:27` calls `goal`, which is the /goal contract owner. GNX-27 ("possibly remove dead wiki.rs") and XM-10 ("goalruntime duplicates Go") do not justify removal. The only code with zero Go callers is `models.rs` (removal already on `parity/w0-cleanup` 4780e7e), the `swarm` and `bench` subcommands, the symbolgraph `build-lsp` and `flow` subcommands, and `lsp-references/definition/implementation/type-definition/rename`. The Go side calls only `lsp-hover` and `lsp-document-symbols`.
2. **Evidence identity sampling.** The evidence middleware samples identity once always, plus a second time only when output is reduced (`evidence_routes.go:158`, `store.go:447-458`). It does not sample twice on every call. Each `resources/read` page still spawns `repo-key`, with p50 around 52 ms.
3. **Symbolgraph clusters, hotspots, and blast-radius have Go callers.** `explain` calls PathCluster, and platform routes cover the rest. The assessment's "dead" claim is partially wrong.
4. **Claude Code built-in tool output.** HCL cites first-party hook docs: PostToolUse `updatedToolOutput` works for all tools as long as it is shape-matched, and a shape mismatch is silently ignored. CB-05 says a model gateway is the only seam. Resolution: hooks are the primary seam, and the model gateway is an opt-in `could` (PAR-HAR-09).
5. **Default read mode for superseded memories.** Mem0 shows superseded memories with a badge by default. xMustard hides them by default, because agents act on recall output.
6. **Harness runner.** MB-21 rates it `should` at 5–8 MiB, and LET-28 says to skip a full agent runtime. Resolution: only a bounded verification/run lineage runner (tests and checks, lineage, outcomes). There is no agent loop (PAR-HAR-10).
7. **Precision lane.** SCIP (SAS-04) and a warm LSP pool (SAS-05, XM-18) are both `should`. A Go LSP pool already exists (`api-go/internal/workspaceops/lsp_definition.go`), so wiring it comes first and SCIP is scheduled later.
8. **Memory feature cost.** The memory readers estimate 15–25 MiB for all deterministic memory parity. That fits only inside the daemon and SQLite lines of §7, and only once per-call snapshot parsing, full-array JSON rewrites, and search-path feedback writes are removed first.
9. **Token counting.** CB-27 estimates exact BPE (tiktoken o200k) at 10–20 MiB. The default uses a bytes/runes heuristic, and exact BPE loads only for evaluation.

### 2.4 Licensing rules (binding for all workstreams)

| Source | License | Rule |
|---|---|---|
| GitNexus | PolyForm Noncommercial 1.0.0 | Reimplement from designs and public algorithms only. Do not copy code, tree-sitter query files, prompts, skill or AGENTS.md prose, or fixtures. Take vendored permissive code (graphology Leiden MIT, grammars MIT) from the original upstream. |
| Serena application | GPL-3.0-or-later | Design reference only. |
| SolidLSP | MIT | Portable with attribution. |
| Mem0, Graphiti, Letta Code | Apache-2.0 | Port with NOTICE, attribution, and a changed-file notice. |
| context-mode | Elastic License 2.0 | Design reference only. |
| RTK | Apache-2.0 | Portable with NOTICE. |
| Pi, OpenHands SDK, SWE-agent | MIT | Portable. OpenHands `enterprise/` is separately licensed. |
| cline | Apache-2.0 | Portable with NOTICE. |
| cursor-bridge | Owner's code, no LICENSE file | Reusable. Record source path and commit in commit messages. Never copy the security-sensitive items in PAR-SEC-03 and PAR-SEC-04. |
| model2vec-rs, potion weights | MIT | Verify weight terms before bundling. |
| Needle3, arctic-embed-xs | Apache-2.0 | |
| `@letta-ai/trajectory` | not in clone | Check before any reuse. |

## 3. Constraints that shape parity

- **Local, no Docker by default.** Docker is acceptable only in an eval-only lane (SWE-bench).
- **The gate is on the xMustard-owned process tree.** The limit is `1e8` bytes, which is **95.4 MiB**, not 100 MiB. The current measured peak on the fixed 501-file workload is 72.3–84.9 MB, with 15–28 MB of headroom. External processes (agents, LSP servers, helper models, compilers, and downstream MCP servers the client launches) are reported on separate lines and never hidden.
- **Nine tools.** Their count is a surface-design choice, not a cap on routed external tools (CONTEXT_LAYER). Schema growth costs `tools/list` tokens, so descriptions must stay terse and detail must be progressively disclosed.
- **Human final merge authority.** Agent votes, run-plan approval, and passing tests never authorize a Git merge. Model output never promotes memory.
- **Evidence first.** Every lossy output carries a recovery handle. Every heuristic output carries provenance (lexical, scope, SCIP, or LSP) and a completeness label.

---

## 4. Unified requirements matrix

Column key:
- **Pri**: priority.
- **MiB**: steady / peak (`T` = heavy-slot transient, `ext` = external).
- **Current**: state at HEAD 7ab61a0, plus in-flight branches where noted.
- **Target surface**: which of the nine tools it deepens, or the new surface and its justification.

### 4.1 PAR-RT: Runtime topology and process model

| ID | Requirement | Origin | Pri | MiB | Current | Target surface |
|---|---|---|---|---|---|---|
| PAR-RT-01 | **Resident Rust index service.** One long-lived `xmustard-core serve` process supervised by Go.<br>Protocol: JSON-RPC framed over stdio or a Unix socket, with a pending-id map, read loop, and `failAllPending` on exit. It restarts lazily with backoff, applies a per-request deadline and a cancel message, trims caches when idle, and exits after idle.<br>It holds read-only index snapshots (graph/postings/vector segments via pread with bounded caches). Snapshots are swapped atomically, and all agents share one snapshot.<br>Go keeps the TrackChild/KillProcessTree kill boundary, ChildLimit admission, and an RSS watchdog. | GNX-09, SAS-08, CB-07, XM-02, MB-02 | must | 3 base (segments counted in IMP-02, RET-01, RET-04, FRESH-01); removes 20–40 T per call | `api-go/internal/rustcore/root.go:39-94` execs `xmustard-core` per call (120 s timeout, 64 MiB stdout cap). One `ground` spawns 4 cores and 8–18 git processes. The graph JSON is deserialized per call (`search.rs:226`). The persistent session type in `lsp_session.rs` is unused. *Measured* per-call core: 10–16 MB warm, 34–47 MB cold. | Internal. Backs all nine tools, whose results gain `snapshot_id`. |
| PAR-RT-02 | **Transient parse/index worker.** `xmustard-core index build/update` runs only inside the single heavy slot and exits after each batch, so grammar pages and parse heaps leave with it.<br>It streams one file at a time with 1–2 threads, drops each tree right after fact extraction, loads only the grammars present in the change set, and writes batched rows. | MB-07, MB-02, GNX budget risk (parse parallelism), SAS-33 (prewarm) | must | 0 / ≤25 T | The cold build holds all trees and symbols: *measured* 35.5 MB footprint / 47.3 MB RSS on the gitnexus clone at the 800-file cap. | Internal. `xmustard-ops index --prewarm`. |
| PAR-RT-03 | **MCP over Streamable HTTP served by the API**, with per-connection bearer principal, cancellation, and progress. A native stdio relay of ≤3 MiB replaces the per-agent Go shim for stdio-only clients. | SAS-27, HCL-23, XM-20, MB-03, GNX-28 (HTTP transport) | must | −8 to −12 per agent (relay ≤3) | *Measured* shim at 7.9–12.6 MiB each; it is a pure HTTP proxy and does not start the API. A minimal Go relay *measured* 13–17 MiB, so the relay must be native (Rust or C). | Transport only; same nine tools. New `/mcp` endpoint justified by budget. |
| PAR-RT-04 | **RSS-aware admission governor.**<br>- Per-component static reservations.<br>- One **heavy slot** shared by the parse worker, large captures/projections (≥4 MiB), SQLite bulk import, index writer, helper model, and ONNX sidecar.<br>- RSS watchdog on the own tree using both ps-RSS and phys_footprint/PSS; it refuses new heavy work near a soft ceiling.<br>- Transient pool lowered from 64 to 16–24 MiB.<br>- FreeOSMemory and cache trim after heavy work is released. | MB-04, MB-19, XM budget risk, CB-18 (bounded fan-out) | must | <1 | `api-go/internal/budget/budget.go` has a 64 MiB pool, 4 child slots, and a 10 s wait, and is explicitly not an RSS ceiling. *Measured*: four 16 MiB captures held the whole pool. | `/api/health` budget block; existing 503/-32000 overload semantics. |
| PAR-RT-05 | **Runtime memory hygiene.** GOMEMLIMIT soft limit around 40 MiB with a GOGC floor; fixed small Rust thread pools with no per-core rayon default; `MALLOC_ARENA_MAX=2` or mimalloc purge on Linux; madvise return after peaks. | MB-22 | should | 0 (saves 3–10 at peak) | The API varies 16–31 MB between runs. One fixed workload allocated 1.08 GB total in Go. | None; health reports GC and heap stats. |
| PAR-RT-06 | **Resident workspace registry** (id → canonical root, invalidated by mtime). Never parse `snapshot.json` on tool paths. | XM-05 | must | 0.2 | `loadSnapshot` parses the full snapshot (3.8 MB on this machine) 5–6 times per `ground` via `changes.go:16-26`, `coverage.go:195-203`, `grounding.go`, `run_review_reads.go`, `workspace_reads.go`, and `repo_identity.go`. | None. |
| PAR-RT-07 | **Core-only by default.** Kernel package (governance, grounding, evidence, budget, nine handlers, MCP) behind an explicit Store interface. The platform half (issues, runs, terminals, providers, Postgres) moves behind a build tag or opt-in. One config struct replaces the 186 `envDefault("XMUSTARD_DATA_DIR"...)` calls. | XM-12 | must | −3 to −8 | `XMUSTARD_CORE_ONLY=1` is opt-in, so 204 routes are served by default. `workspaceops` has 170 files (~47.7k lines). `main.go` has 4,333 lines. | None. |
| PAR-RT-08 | **Remove unreachable Rust code.** Remove `models.rs`, the `swarm` and `bench` subcommands, symbolgraph `build-lsp`/`flow`, and unused `lsp-*` subcommands. Keep `upgrade_graph_with_lsp`, `LspWorkspaceSession`, `wiki.rs`, and `goalruntime.rs` (see §2.3). | XM-10, GNX-27 | should | −2 (binary text pages) | `models.rs` removal is on `parity/w0-cleanup` 4780e7e; the rest is present. | None. |
| PAR-RT-09 | **Declared scale envelope with graceful degradation**, replacing the silent 800-file cap. Proposed envelope: ≤10k source files, ≤100k symbols, ≤300 MB tracked bytes, ≤4 agents, ≤2 hot repos. Beyond it: partial index with coverage losses, cold-repo mode, optional lanes off, and no RSS growth. | MB-23, XM-15 (cap) | must | 0 | `symbolgraph.rs:21` sets MAX_FILES=800 and `:38` sets MAX_GRAPH_SYMBOLS=100,000. *Measured* loss: 3,377 of 4,177 eligible gitnexus files and 1,832 of 2,632 cline files fall to `file_cap`. | ground `index_coverage {files_indexed, eligible, languages, losses}`; coverage on search, explain, and impact. |
| PAR-RT-10 | **Multi-repo residency policy.** One index per repo on disk. Only K hot repos have resident caches (default K=1, K=2 only with optional lanes off), with LRU by query recency and idle eviction. Cold repos serve by pread with a tiny cache or load on demand in the heavy slot. | GNX-21 (eviction), SAS-14 (cap), MB-17 | should | 0 cold; +20–25 per extra hot repo | No residency and no multi-repo concept. | Internal; `workspaces=` arguments (PAR-SHARE-03). |
| PAR-RT-11 | **Bound residual unbounded or redundant per-call work:**<br>- changed-since with no baseline lists every tracked file and extracts symbols with a `usize::MAX` cap (`changetrack.rs:466-528`);<br>- `symbol_signature` re-reads the file per symbol (`:154-185`);<br>- `why_failed` uses unbounded `os.ReadFile` (`failure_explainer.go:50`);<br>- `explain` runs label-propagation clustering on a second spawn with `context.Background` (`symbolgraph.go:100-122`, `main.go:2629-2634`);<br>- the docs lane re-reads all docs on every search (`search.rs:471-520`);<br>- the regex fallback compiles 8 regexes per file (`repomap.rs:852`);<br>- ground swallows decode errors and reports 0 (`grounding.go:112,121`). | XM-21 | must | 0 (cuts peaks) | All present. | Truncation and `unknown` fields where results are capped. |
| PAR-RT-12 | **Spawn-free hook and capture hot path.** Hooks never spawn Rust or git per call. Repository identity is cached, keyed by HEAD plus a status/stat fingerprint, and invalidated by watcher and edit events. Hook-delivered observations are `captured_identity=unknown` by contract. | HCL-24 | must | 1 | The current per-call model would exhaust the 4 child slots under parallel tool batches. | None. |

### 4.2 PAR-STORE: Persistence

| ID | Requirement | Origin | Pri | MiB | Current | Target surface |
|---|---|---|---|---|---|---|
| PAR-STORE-01 | **Governance store on SQLite WAL, in the Go daemon** (modernc pure-Go driver, recommended; v1.59.0 cached locally).<br>Tables: entries, revisions, votes, events (append-only), anchors, claims, relations, sessions, session_events, outcomes, grants, feedback, evidence_meta, jobs, plus FTS5 over title/body/anchors and transcripts.<br>Pragmas: `journal_mode=WAL`, `cache_size` capped at 2–4 MiB, `mmap_size=0` (so pages are not file-backed RSS), `busy_timeout`, and `synchronous=FULL` for governance commits.<br>There is one writer in the daemon. The ops CLI goes through the daemon or relies on SQLite multi-process locking. Edits use compare-and-set on `base_revision`, and writes are O(change). | XM-01, MEM-24, LET-08, MB-12, LET-01 (store), HCL-12 (store) | must | 3 / 12 T (bulk import) — *measured* modernc +2.3 MiB at open, 23.4 MiB maxrss for 100k rows plus FTS5 | `context_entries.json` is one array, rewritten in full per mutation along with `context_meta.json` and hash-named content files (old revision pruned). `storelock.go:26-50` is an in-process mutex only. `verification.go:392-414` writeJSON does not fsync. Only 9 of 47 writeJSON sites hold a lock, and the ops CLI or a second API process loses updates. | None directly. Enables the GOV/RCL/PROV/SHARE rows. |
| PAR-STORE-02 | **Interim cross-process hardening** (flock, fsync, atomic rename, directory fsync) for JSON stores that remain until cut over: runs, tokens, and feedback before cutover. Reuse the pattern of the in-flight `diagnostics_local_store.go` without editing that file. | XM-01 (interim), LET-08 (lease design) | should | 0 | None. | None. |
| PAR-STORE-03 | **Code index store, Rust-owned, per workspace** under `.git/xmustard-cache/index-v3/`.<br>Contents: files (path, lang, size, mtime, hash, parse status); symbols (uid, nested qualified path, kind, byte and line ranges, container, signature hash); references and occurrences (site, containing symbol); edges (kind, confidence, provenance); imports; chunks with contentless FTS5 postings; vectors; the per-file fact cache; and meta (analyzer version, schema fingerprint, `incremental_in_progress` dirty flag, last_commit, coverage).<br>With content retention `none|symbol`, the index never stores source text. Source is read from the working tree on demand. | MB-06, MB-08, GNX-08, SAS-07 | must | counted in IMP-02 / RET-01 | Serde JSON graph snapshots (3 kept) and the per-file feature cache in `indexcache.rs`. | Internal. |
| PAR-STORE-04 | **Retention and compaction policies** for memory history, session events, transcripts, evidence originals, and fixture captures. Tombstones and digests are kept. No path reads the full log. | MEM budget risk, LET-11 risk, CB-21, HCL-07 | should | 0 RSS (disk) | Evidence has 24 h retention and a 256 MiB quota. Everything else is unbounded or absent. | Operator config; `expires_at` and retention class in envelopes. |

### 4.3 PAR-FRESH: Indexing and freshness

| ID | Requirement | Origin | Pri | MiB | Current | Target surface |
|---|---|---|---|---|---|---|
| PAR-FRESH-01 | **Stat-keyed hash cache and one repository-identity scheme.** The cache is keyed by (dev, ino, size, mtime_ns, ctime), taken with fstat on the held fd, and maps to sha256; only stat-changed files are hashed. It short-circuits on HEAD plus porcelain. `changetrack` reuses `indexcache::source_identity`, and the duplicate `content_hash` scheme is retired. Periodic or forced full verification remains, because same-size edits and coarse mtimes exist. | XM-03, SAS-07, CB-15, MB-05, GNX-07 (dirty identity) | must | 1–3 (disk-backed; ~10 at 100k files if heap) | `changetrack.rs:88-127,323` SHA-256s every tracked file on every `ground`, and `:516` does it again for changed-since. `indexcache.rs:425-601` is a second scheme. `parity/w0-drift` (8b81ef8) adds `hashcache.rs`. *Measured* on the gitnexus clone (5,652 files, 270 MB): ground 14.8 s, impact 12.3 s, index 45 s; `git --no-optional-locks status` 139 MB RSS and 9.3 s before the index refresh. | ground/recall/impact return `drift_checked {hashed, stat_skipped}`. |
| PAR-FRESH-02 | **Identity sampled once per request** and passed to the handler and to Rust. Write tools (remember/verify) skip it. The identity is cached per evidence page window, keyed by HEAD plus the porcelain/stat digest. | XM-04, HCL-24, MB-19 | must | 0 (saves 7–13 T and 4 git per call) | `evidence_routes.go:158` samples unconditionally, including for remember/verify. `store.go:447-458` samples a second time when reduced. `resources/read` spawns `repo-key` per 64 KiB page (p50 ~52 ms; 13.4 s to expand a 16 MiB original). | None; freshness labels unchanged. |
| PAR-FRESH-03 | **Watcher with a serialized refresh queue** (FSEvents, kqueue, or inotify; directory-level, not per-file fds). 300 ms debounce with a max-wait bound. Pending overflow triggers a full refresh. Refreshes never overlap and retry with exponential backoff. Changes to `.gitignore`/`.xmustardignore` force a full refresh. Files over 32 MB are ignored. git runs only on overflow or HEAD/index change. | GNX-09, MB-05 | must | 1–2 | None. | `watcher_state` (ok, overflow, degraded) in the freshness envelope. |
| PAR-FRESH-04 | **Incremental re-index with correctness invariants.**<br>- Exit early when unchanged.<br>- Per-file fact cache, content-addressed and gated by analyzer version and schema bump.<br>- Re-resolve only the changed files plus dependents of changed exports (reverse import index), not the whole program as GitNexus does (*measured* 31.7 s for a one-file edit at 2.2k files).<br>- Escalation gate: a full rebuild when the write set exceeds 50% of files and at least 50 files.<br>- A crash dirty flag, or a change in runner identity or schema fingerprint, forces a full rebuild.<br>- Counters: reparsed, reresolved, escalated. | GNX-08, SAS-07 | must | 0 / 5–15 T | Per-file feature cache exists. All edges are re-resolved from word sets on a cache miss. | ground and index status show incremental counters. |
| PAR-FRESH-05 | **Freshness envelope on every hot read** (search, explain, impact, ground): `{indexed_commit, head, status: current|behind(N)|diverged|unknown, dirty_paths_touching_result, index_version, identity_age_ms, watcher_state}`. Fails open with a 5 s timeout, a TTL cache, and in-flight dedupe. | GNX-07, SAS-07, MB-05 | must | 0.5 | ground reports index trust and drift; the other tools report nothing. | Field on search, explain, impact, and ground. |
| PAR-FRESH-06 | **Governed automatic index baseline.** Created at workspace registration or first ground. Rebaselined on HEAD change or by admin action. Baseline history is kept. Agents cannot silently reset it. `POST /index` is role-gated and in the core allowlist. | XM-22 | must | 0 | `main.go:3762-3765` exposes POST /index with no role check, outside `coreWorkspaceSubpaths`, and unreachable from MCP. With no baseline, ground reports stale and impact returns the whole repo. | ground `baseline {head, indexed_at, auto}`. |
| PAR-FRESH-07 | **Adapter edit events feed the dirty set.** Sources: Claude Code FileChanged/watchPaths, OpenCode `file.edited`/`file.watcher.updated`, Pi edit/write results, Cursor afterFileEdit. A periodic full check is kept for unobserved edits. | HCL-25 | should | 0.5 | None. | Hook routes (PAR-HAR-01). |
| PAR-FRESH-08 | **Index configuration.** One ignore source (`.gitignore` plus `.xmustardignore`), `max_file_size`, allow-non-git, `content_retention = full|symbol|none`, and per-workspace detection budgets. The index never stores source text per node. | GNX-31, SAS-33 | should | negative (reduces) | Index and ignore handling disagree (per the assessment). | Repo config and `xmustard-ops is-ignored`. |
| PAR-FRESH-09 | **Portable index snapshot export/import**, keyed by content hash and validated against local file hashes on import. Lets CI build the index. | SAS-35 | could | 0 | None. | `xmustard-ops index export|import`. |
| PAR-FRESH-10 | **Revision-aware reads.** `rev=` on search and explain reads the git object database without a checkout. `impact(base, head)` compares revisions. At most one extra revision's graph is resident; others stay on disk. | SAS-12 | should | ≤10 when loaded | Working tree only, compared against the index baseline. | search, explain, impact args. |

### 4.4 PAR-SYM: Symbols and navigation

| ID | Requirement | Origin | Pri | MiB | Current | Target surface |
|---|---|---|---|---|---|---|
| PAR-SYM-01 | **Nested scope extraction and stable UIDs.** Records the full container path, start and end byte and line ranges, and a UID of the form `Label:path:qualifiedName#arity`, with type-hash or const suffixes added only on collision. | GNX-02, SAS-01 | must | 1 | `treesitter.rs` records only the nearest single container. GraphSymbolNode (`symbolgraph.rs:874-879`) has name, kind, path, and line_start, with no end or container path. | uid on search, explain, and impact results. |
| PAR-SYM-02 | **Name-path symbol lookup** (Serena `find_symbol` parity):<br>- simple, relative suffix, absolute `/A/b`, and overload `[i]` forms;<br>- substring match on the last segment;<br>- file/dir scoping, kind include/exclude, and `depth` children;<br>- `include_body` sliced from disk and `include_info` (signature and doc comment);<br>- exceeding `max_matches` returns a `{file: [name_paths]}` refine map.<br>Ambiguous names return a ranked-candidates envelope with relevance, `totalCandidates`, and `candidatesTruncated`. | SAS-01, GNX-02 | must | in IMP-02 (symbol table ~2–6 touched) | Fuzzy name ranking only. | `search(mode='symbol', query=<name_path>, path?, depth?, include_body?, kinds?, substring?, max_matches?, max_chars?)`. |
| PAR-SYM-03 | **File and directory overview.** Top-level symbols grouped by kind with line ranges, `depth` for children, and locals excluded. Staged fallback: depth 0, then counts by kind. | SAS-02 | must | 1 | explain composes repo-map and key symbols, with no depth and no staged fallback. | `explain(path, depth?, max_chars?)` → `symbols_by_kind`, `degraded_stage`. |
| PAR-SYM-04 | **360-degree symbol context.**<br>- Incoming and outgoing references grouped by relation type (calls, imports, extends, implements, has_method, has_property, accesses read/write, overrides, injects).<br>- Flow participation with step, `is_entry_point`, routes handled, and location.<br>- `include_content` and `chain_depth` 0–3 for layered upstream/downstream BFS.<br>- Epistemic and freshness envelopes.<br>- **Verified memories anchored to the symbol**, which no peer has. | GNX-03, MEM-10 (attached memories) | must | 2 | explain handles files and directories only. | `explain(symbol|uid, include_content?, chain_depth?)`. |
| PAR-SYM-05 | **Language breadth through one language-provider contract.** Each provider supplies extensions, original tree-sitter queries with unified capture tags, import semantics (named, wildcard-leaf, wildcard-transitive, namespace), an import-resolver config, an export checker, a description extractor, and entry-point patterns.<br>Target set: Go, Rust, TS/TSX, JS/JSX, **Python, Java, C, C++, C#, Ruby, PHP, Kotlin, Swift, Bash** (Scala and Lua optional). Regex fallbacks are precompiled once. Coverage is honest per language. | GNX-32, SAS-06, XM-15, MB-07 | must | 0 steady (worker only); worker ≤20 T — *measured* 17 grammars +20 MiB RSS but ~4–6 MB dirty | 4 grammar crates. Python uses a def/class regex. Java and C get almost no symbols. Kotlin, Swift, C#, PHP, and Scala are unrecognized. Grammar crates for python, java, c, cpp, c-sharp, ruby, php, kotlin-ng, swift, bash, scala, and lua are in the local cargo registry. | Coverage block on search, explain, impact, and ground. |
| PAR-SYM-06 | **Optional warm LSP session pool.**<br>- Reuse the Go pool (`lsp_definition.go:81-84`: `sync.Map` keyed by workspace and server, 2-minute idle reaper, definition, references, document and workspace symbols, live diagnostics).<br>- At most one server live by default, with memory caps (for example `maxTsServerMemory`).<br>- Crash restarts with a single retry; restart on RSS runaway.<br>- Results upgrade edges to `provenance=lsp`.<br>- LSP RSS is always reported separately. | SAS-05, XM-18, MB-16 | should | 1–2 in-tree; ext 200 MiB–GBs (*measured* 534 MiB tree for one TS hover; 669 MB typescript-language-server on a 12k-line frontend) | The Go pool is used by platform routes only. The Rust `lsp-hover` and `lsp-document-symbols` are one-shot per call (`lsp_session.rs:333-406`). | `precise=true` or `provenance=lsp` on explain, impact, and diagnostics; `xmustard-ops lsp status|restart`. |
| PAR-SYM-07 | **SCIP precise lane.** Run scip-go, scip-typescript, or rust-analyzer scip as a scheduled transient external job. Ingest into the occurrence table stamped with its commit, with provenance `scip`. | SAS-04 | should (scheduled late) | 10–15 disk-backed; indexer ext (hundreds of MB) | None. | Provenance on impact and search; `precision_by_lang` coverage. |
| PAR-SYM-08 | **Functional areas (communities).** Seeded deterministic Leiden or label propagation over the resolved CALLS/EXTENDS/IMPLEMENTS projection. Large-graph mode above 10k symbols (resolution 2.0, 3 iterations, drop low-confidence edges and nodes of degree <2). Folder-based labels with generic folders skipped, then name prefix, then `Cluster_N`. Cohesion sampled over 50 members, plus keywords. Revives `compute_clusters` at symbol granularity. | GNX-12, MB-26, MEM-29 (memory clusters reuse) | should | 1 / 10–20 T | File-level `compute_clusters` feeds explain PathCluster. | explain area and cohesion; `xmustard://workspace/{id}/areas`. |
| PAR-SYM-09 | **Execution flows.** Entry-point scoring: calleeCount/(callerCount+1), ×2 if exported, times a name multiplier. Top 200 candidates. DFS over CALLS with depth 10, branching 4, and at least 3 steps; deduped, labeled, and classified intra- or cross-community. Truncation stats are persisted. Only built on resolved edges. | GNX-13, MB-26 | should | 2 / 5 T | The `flow` subcommand is a lexical data-flow edge list with no Go caller. | impact and ground `affected_flows`; `xmustard://workspace/{id}/flows`. |
| PAR-SYM-10 | **Repository orientation packet.** Language histogram, top directories, entrypoints, tests, docs, and hotspots with role labels, computed from the scanner and cached by the stat mark. Generic classification only; the bridge's project-specific regexes are not copied. | CB-16 | should | 0.3 | None. | ground `orientation` on the first call per session. |
| PAR-SYM-11 | **Bounded structured graph filter**: kind, edge type, path glob, depth, and limit. This is the Cypher alternative. | GNX-25 (alternative) | could | 0 | None. | `impact(query={edge_types, kinds, path})`. |

### 4.5 PAR-IMP: Impact and graph

| ID | Requirement | Origin | Pri | MiB | Current | Target surface |
|---|---|---|---|---|---|---|
| PAR-IMP-01 | **Resolved, scope- and import-aware symbol graph with typed edges.** Edge types: CALLS, IMPORTS, EXTENDS, IMPLEMENTS, HAS_METHOD, HAS_PROPERTY, ACCESSES (read/write), METHOD_OVERRIDES/IMPLEMENTS, USES, INJECTS.<br>Confidence tiers: same-file 0.95, import-scoped 0.9, global 0.5. Each edge carries provenance `lexical|scope|scip|lsp` and a reason. Strings and comments never create edges.<br>Resolution includes receiver typing, constructor inference, SCC-ordered cross-file return types, and per-language MRO (first-wins, c3). | GNX-01, XM-16, SAS-03, MB-25 | must | in IMP-02; resolver T 10–40 (unmeasured) | `symbolgraph.rs:549-555,1652-1716` emits a file→file edge when any word of ≥4 characters in B (strings and comments included) equals a unique name defined elsewhere. Ambiguous names are dropped. Imports and inheritance come from line-prefix scans. Memory note: distance ≥1 edges are leads, not proof. | impact, explain, search (proximity); per-edge `edge_kind`, `confidence`, `provenance`. |
| PAR-IMP-02 | **Compact graph representation.** Fixed-width symbol rows, an interned string heap, forward and reverse CSR (u32 ids, 12-byte edge records), per-file segment ids for incremental replacement, and a bounded block cache. Target ≤150 B per symbol including edges. Trees are never kept resident. | MB-06 | must | 12 / 15 | *Measured* serde graph at ~1.35 KB/symbol above a 2 MB base (6,538 symbols → 10.8 MB warm), so 100k symbols would need ~135 MB. The 800-file cap hides this. | Internal. |
| PAR-IMP-03 | **Symbol-level occurrences.** Operations: `refs`, `definition`, `implementations`. Each hit returns the containing symbol name path, kind, line, and a ±1-line snippet, with per-edge provenance and confidence. Pagination and staged degradation: no snippets, then per-file counts, then a total. | SAS-03, GNX-03 | must | occurrence table disk-backed; +2 T | GraphEdge is file-to-file (`symbolgraph.rs:882-886`), with no reference lines. | `impact(symbol|uid, op='refs'|'definition'|'implementations', limit?, cursor?, max_chars?)`. |
| PAR-IMP-04 | **Blast radius v2.**<br>- Inputs: direction, `max_depth` (1–32, default 3), `relation_types` (default CALLS/IMPORTS/EXTENDS/IMPLEMENTS; ACCESSES opt-in), `min_confidence`, `include_tests`. Class and interface targets are seeded with constructors and the defining file.<br>- Output by depth tier: d1 WILL BREAK, d2 LIKELY AFFECTED, d3 MAY NEED TESTING. Per-depth limit/offset, `summary_only`, `timeoutMs`, affected flows and modules.<br>- Risk: CRITICAL if direct ≥30, flows ≥5, modules ≥5, or impacted ≥200; HIGH at 15/3/3/100; MEDIUM if direct ≥5 or impacted ≥30; else LOW. UNKNOWN when an upstream walk finds no callers, with a `risk_note`.<br>- Keep xMustard `contract_break`. | GNX-04 | must | 4 T | BFS with from/to and dirty symbols. No typed filters, tiers, risk, or pagination. | `impact(symbol|uid, direction, max_depth, relation_types, min_confidence, include_tests, limit, offset, summary_only)`. |
| PAR-IMP-05 | **Epistemic completeness envelope.** `completeness: exact|lower_bound`, `boundaries[]`, and machine-readable `causes`: extraction failure files, receiver-typing drops, dispatch boundary, external boundary, undecided interface satisfaction, callable-value refs. A lower-bound impact weakens memories resting on it. | GNX-05 | must | 1 | None. | Fields on impact, explain, and search; recall uses them for weighting. |
| PAR-IMP-06 | **Diff-to-symbol change impact.**<br>- Scope: unstaged, staged, all, or compare with `base_ref`/`base`+`head`; worktree validated against the canonical root.<br>- Pinned git flags: `-c core.quotePath=false diff --no-ext-diff --color=never --src-prefix=a/ --dst-prefix=b/ --ignore-cr-at-eol -U0`.<br>- Hunks overlap **current** spans: dirty and untracked files are re-parsed, fixing GitNexus's gaps.<br>- Risk by affected flows. `partial` and `truncated` flags (listing capped at 1000). | GNX-06, SAS-12 | must | 3 T | No-arg impact returns dirty symbols with `contract_break`. | `impact()` with scope arguments; ground `what_changed`. |
| PAR-IMP-07 | **Shortest-path trace.** Per-hop edge type and confidence. Returns the furthest reachable node and depth when no path exists. Caps: depth 10 (≤30), per-node fan-out 200, 5,000 rows per level, 50k visited. | GNX-15 | should | 3 T | `impact from/to` exists without per-hop metadata or caps. | `impact(from, to)` → hops, edges, furthest, truncated. |
| PAR-IMP-08 | **Structural checks: import cycles.** Iterative Tarjan SCC plus a bounded Johnson enumeration over initialization-forcing imports (type-only, dynamic, and in-function imports excluded). `componentCount` is the stable metric; `cycleCount` is null when truncated; one representative per SCC on blowup. | GNX-16 | should | 3 T | None. | diagnostics structural findings `{kind:'import_cycle'}`. Surfaced via impact/ground until the diagnostics owner hands off. |
| PAR-IMP-09 | **Read-only refactor plans.** `op=rename` returns planned `{path, range, old, new}` edits with provenance. `op=delete` returns `{safe, blocking_refs}`. A lexical-only plan is labeled unsafe. xMustard never applies edits. | SAS-31, GNX-24 | could | 2 | None. | `impact(symbol, op='rename'|'delete', new_name?)`. |
| PAR-IMP-10 | **API surface maps**: routes, consumers, response shapes, tool definitions. | GNX-23 | could | 5 | None. | `impact(route=...)` / `explain(route)`, not four new tools. |
| PAR-IMP-11 | **Cross-repo contract bridge** and `@group` routing (HTTP, gRPC, and topic contracts; ≤50 crossings, depth 1). | GNX-22 | could | 15 T | None. | `workspace_id='@group'` on search and impact; ops `group sync`. |
| PAR-IMP-12 | **PDG and taint slices**: statement-level impact, "what guards this line". | GNX-26 | could (deferred) | 40 T at index time | None. | `impact(symbol, line)` if ever built. |

### 4.6 PAR-RET: Code retrieval

| ID | Requirement | Origin | Pri | MiB | Current | Target surface |
|---|---|---|---|---|---|---|
| PAR-RET-01 | **BM25 lexical index** over symbol names (camel and snake subtokens), paths, doc comments, and **function-aligned code-body chunks**. Updated incrementally per file. Readers use pread with a bounded block cache, not mmap-resident. The writer runs only in the worker. Replaces the docs-lane rereads. | GNX-10, SAS-09, XM-17, MB-08, CB-17 | must | 5 / 8 (*measured* tantivy 100k docs: 26 MB on disk, reader 2.5 MB footprint but 23 MiB RSS if mmap'd; writer ≥15 MB/thread) | Only symbol names, path tokens, and a flat 0.05 docs lane that re-reads every doc per query. No body search. | `search(mode='hybrid'|'text', path_glob?, lang?)`. |
| PAR-RET-02 | **Hybrid fusion with explainable reranking.**<br>- RRF with a deliberately chosen constant (K=60, as today).<br>- `lanes_matched`, per-lane scores, and `reasons[]`: declaration identifier match, normalized identifier, window coverage including order, dense line, symbol-like line.<br>- Bounded snippets (up to 3, ±1 line, line-numbered).<br>- Degradations reported.<br>- The rerank runs over the top-K candidates only and never enumerates the repo per query. | GNX-10, CB-17, SAS-09 | must | 1 | RRF over lexical, pseudo-semantic, structural, and proximity lanes. Hits have no snippet. embed() is recomputed per symbol per query (`search.rs:303,329`). | search hits `{path, lines, snippet, lanes_matched, scores, reasons, uid}`. |
| PAR-RET-03 | **Honest semantic lane from static embeddings.** model2vec/potion via a lean loader: mmap safetensors, int8, a compact WordPiece vocab (sorted array or FST, not the HF tokenizers object graph), mean pooling. Vectors are precomputed at index time; a query costs one lookup. Enabled by default only if the retrieval gate shows a gain. The FNV hashing lane is removed or renamed. | SAS-10, GNX-11 (default alt), XM-17, MB-09, HCL-28 | should (the honesty rename is must, in ADP-01) | 6 / 10 (*measured* lean potion-base-8M: 16 MB footprint; model2vec-rs as shipped: 78–90 MiB, which does not fit) | A 256-d FNV trigram "semantic" lane. The semantic-onnx feature is never built. | `search` `lanes:['semantic']` and `semantic_model {id, hash, static|neural}`. |
| PAR-RET-04 | **Budget-safe vector index.** 1-bit sign vectors resident for a Hamming prefilter of the top ~200, then int8 rerank via pread from a disk segment. HNSW only below ~20–50k vectors or when p95 demands it. Vector ids are tied to chunk content hashes. | MB-10 | should | 3 / 4 (binary 100k×256 = 3.1 MiB) — *measured* HNSW 100k×384: i8 61, f16 98, f32 171 MiB (does not fit) | None. | `vector_mode` in search and recall output. |
| PAR-RET-05 | **Optional neural embedding sidecar.** An ONNX transformer run lazily in a forked, reaped process, only for index batches, only in the heavy slot or with an explicitly raised budget. Alternatively a local OpenAI-compatible endpoint. | GNX-11, MB-11 | could | 0 default; ext 92–700 MiB *measured* (fastembed MiniLM/bge) | The `semantic-onnx` feature would reload the model per CLI call. | Admin flag; `semantic_model` label. |
| PAR-RET-06 | **Flow-grouped query output.** Hits attach to flows and communities; flows are ranked by ΣRRF + 0.1×cohesion; definitions outside any flow are listed separately. Task-ranking signals (query plus paths) are used for real, unlike GitNexus's ignored `task_context`. | GNX-14 | should | 1 | None. | `search(group_by='flow')`. |
| PAR-RET-07 | **Task-shaped retrieval.** Multi-sentence input is decomposed deterministically into identifiers, path hints, and concepts, plus verified memories that name paths. Returns a few ranges, each with a `why`. `directory_path` scoping. An optional helper can propose expansions or abstain. | SAS-11 | should | 0 | `search` takes a single query. | `search(mode='task', limit=5)` → `{path, lines, why, lanes, related_memories}`. |
| PAR-RET-08 | **Commit and diff history search.** Bounded `git log --grep/-S/-G --author --since --until -- <paths>` with line and byte caps. When a memory goes stale, attach the commits that touched its paths. | SAS-13 | should | 2 T | None. | `search(mode='commits'|'diff', paths?, author?, since?, until?, limit≤50)`. |
| PAR-RET-09 | **Exact and regex large-corpus lane.** pread positional-trigram or bounded scan with byte and time budgets. Never mmap-resident (zoekt-style needs ~1.2× corpus in RAM). | MB-18, SAS-32 (the pattern mode stays) | could | 6 | ast-grep `mode=pattern` only. | `search(mode='exact'|'regex')` → spans, `bytes_scanned`, truncated. |
| PAR-RET-10 | **Declarative projection parameters** (`fields`, `group_by`, `where` on path glob, kind, or lane) instead of a code-execution REPL. | SAS-22 | could | 0 | None. | search, impact, and recall arguments. |
| PAR-RET-11 | **Outcome-backed feedback, off the hot read path.** A buffered, coalescing recorder. Retrieval counts do not feed back into ranking unless an outcome confirms them, which breaks the popularity loop. Outcome events are idempotent per (run, path), and a GET never writes. | XM-09 | should | 0.5 | Every search does a locked read-modify-write of `agent_feedback.json` (`knowledge.go:62-72`, `feedback.go:57-89`), with 0.5 per retrieval feeding back. `why_failed` increments run_fail on every GET. Fix in flight on `parity/w0-feedback` 5fbc8de. | The search `reason` mentions feedback only when an outcome backs it. |

### 4.7 PAR-GOV: Governed memory lifecycle

| ID | Requirement | Origin | Pri | MiB | Current | Target surface |
|---|---|---|---|---|---|---|
| PAR-GOV-01 | **Honest first run in open mode.** Promote immediately, label the entry `verification_mode=self_asserted_open_mode`, and log a loud startup line. Other modes: `peer_verified`, `single_agent`. Token bootstrap (one token per agent) in the Makefile and packaging. | XM-06 | must | 0 | Open mode records callers as `anonymous` with threshold 2 and author excluded, so `remember` stays pending forever (`main.go:3871-3908`, `context_governance.go:476-541`). Fix in flight on `parity/w0-kernel` 1928a7a. | recall entries carry `verification_mode`. |
| PAR-GOV-02 | **Discoverable verification queue and trust tiers.** ground reports `pending_for_you` (entries the caller did not author and has not voted on). `recall(status='pending'|'awaiting_me')` and `include_pending=true` return entries labeled `trust='unverified'` with `votes_needed`; the default stays verified-only. verify exposes `note` and `evidence_handle`. | XM-08, MEM-07, LET-09 | must | 0 | Pending listing is admin-only (`main.go:3812-3820`). recall and ground expose promoted entries only. verify accepts only `entry_id` and `approve`. | ground, recall, verify. |
| PAR-GOV-03 | **Duplicate detection at propose time.** Normalize, then exact content hash, then Shannon-entropy gate (≥1.5, min length 6 or ≥2 tokens), then 3-gram shingles with 32-permutation MinHash, LSH bands of 4, and Jaccard ≥0.9, within the scope and anchors. A duplicate of a promoted memory becomes **corroboration** (new source or evidence) rather than a new entry. Mid-similarity returns `possible_duplicates`. A merged record is kept and hidden. | MEM-01 | must | 2 (~0.3 KB per memory in bands) | Each ProposeContext appends a new entry whose id includes a timestamp. There is no dedupe. | `remember` → `{status: created|corroborated, duplicate_of?, possible_duplicates}`; `verify(outcome='duplicate_of', target)`; `recall(include_merged=true)`. |
| PAR-GOV-04 | **Governed supersession, retirement, and archive.** `remember(supersedes=...)`; when the new entry is promoted, the old entries atomically become `superseded` with `superseded_by`, `invalidated_at`, and `expired_at`, and are never deleted. `op=retire` is verify-gated, or human-gated for protected memories. Archive tier. Superseded entries are hidden by default; `include_superseded` returns badged history. | MEM-02, LET-02, SAS-16, XM-23 | must | 0.2 | The code comment says readonly entries "can only be superseded by a new proposal", but no field or state exists, so old and new stay promoted together. | remember, recall. |
| PAR-GOV-05 | **Focused edits create pending revisions.** Operations: str_replace, insert, update_description. A non-empty `reason` is required, `base_revision` is compared-and-set, and no-op edits are rejected. verify shows the diff between the current and proposed revision. Free-form regex editing of promoted content is never allowed. | LET-22, LET-08, SAS-16 | should | 0.2 | `UpdateContextContent` overwrites content, wipes verifications and baselines, and prunes the old content file. The route is ungated. | `remember(op='edit', entry_id, base_revision, reason, old_string, new_string)`; the verify payload includes the diff. |
| PAR-GOV-06 | **Forget, retract, and purge.**<br>- Retract is governed (`verify outcome=retract`, or an admin), sets `retracted`, and is recorded in history.<br>- Purge is admin-only hard deletion (secrets or PII) leaving a digest tombstone.<br>- Scope-bounded bulk forget covers a private lane, session candidates, or a workspace with admin.<br>- Shared memory is preserved unless explicitly included. | MEM-17, SAS-16, LET-02 | must | 0 | There is no DELETE route. A promoted wrong or secret-bearing memory cannot be removed. | `verify(outcome='retract')`; admin `DELETE /api/workspaces/{ws}/context/{id}?purge=true`, kept off MCP by default. |
| PAR-GOV-07 | **Write-time policy validation.**<br>- Allowed keys and required fields.<br>- Per-entry character cap (Letta default 20,000), core budget (Letta 65,536 characters), and depth.<br>- Protected fields and protected-memory patterns that require a `human-approver` principal.<br>- The policy config can be changed only by an admin with human approval.<br>- Violations are structured `{field, rule, limit, actual}`. | LET-03, SAS-19 (protected patterns) | must | 0.5 | Safe-ID validation only. | Errors from remember and verify; admin `xmustard-ops policy set`. |
| PAR-GOV-08 | **Tiered memory.** *core* entries are always injected under a hard character budget. *deferred* entries show only in an index (title plus one-line description, render capped at 500 lines, 20k characters, and 50 per group, with a truncation notice). Changing tier is governed. This is MemGPT's fixed-size working context. | LET-04, LET-23 | must | 0.5 | One flat ranked list. | `remember(tier, description)`; `recall(tier)`; ground `core_memory {revision, entries, chars_used, budget}` plus `deferred_index`. |
| PAR-GOV-09 | **Kinds, tags, metadata, and a small filter grammar.**<br>- Kinds: project_knowledge, decision, constraint, workflow/command, procedure, problem_fix/gotcha, result/measurement, convention, handoff, candidate, maintenance.<br>- Free tags and a small metadata map.<br>- Filters: AND across fields, OR within a field.<br>- The grammar stays much smaller than Mem0's DSL. | MEM-08, LET-15, MEM-31 (covered) | must | 0.5 | Title, content, and paths only. | `remember(kind, tags)`; `recall(kind, tags, path_prefix, since, until, by)`. |
| PAR-GOV-10 | **Structured claims and typed relations.** A claim is `{subject anchor, predicate from a fixed coding vocabulary (requires, forbids, runs_with, configured_as, pinned_to, deprecated_by, owned_by, replaces), object}`. Memory-to-memory edges: supersedes, duplicates, refines, contradicts. Same subject and predicate with a different object is a deterministic contradiction. | MEM-12 | should | 0.5 | None. | `remember(claim=...)`; `recall(subject, predicate)`. |
| PAR-GOV-11 | **Tiered deterministic conflict candidates**, advisory only.<br>- T1: claim conflict.<br>- T2: same anchor with high lexical similarity but different numbers, versions, or commands, or negation/deprecation markers.<br>- T3: path overlap (today's behavior).<br>An optional helper can classify T2. Conflicts never auto-invalidate. | MEM-11, MB-14 | should | 1 | Path overlap only (`context_governance.go:1113-1144`), and the name oversells it. | `remember` → `possible_conflicts`; recall `conflicts[{kind, entry_ids, reason}]`. |
| PAR-GOV-12 | **Expiration.** Hide, don't delete. UTC, date-only, inclusive; a malformed date fails open. Expired entries can still be fetched by id and restored. | MEM-26 | should | 0.1 | None. | `remember(expires)`; `recall(show_expired)`. |
| PAR-GOV-13 | **Consolidation work queue.** xMustard prepares candidate jobs deterministically; the connected agent (or an opt-in helper) does the reasoning, and results return as pending proposals that still need distinct-principal verification.<br>- Job kinds: dedupe clusters, stale or orphaned anchors, supersession chains, ageing unverified proposals, session digests, and init seeds from AGENTS.md/CLAUDE.md sections.<br>- Watermark per source, advanced only on success.<br>- Job leases use compare-and-set with a conflict signature; a crashed claim is retried, and a finished-failed claim is reported rather than relaunched.<br>- Idempotent and non-destructive. | LET-10, MEM-22, LET-25, HCL-15, MB-14 | must | 2 / 3 T | None. | ground `consolidation_jobs[{job_id, kind, candidate_ids, evidence_refs, instructions}]`; `remember(job_id=...)`. No tenth tool. |
| PAR-GOV-14 | **Harness capture produces candidate memories, never promoted automatically.** Deterministic templates, for example "command failed with signature X, fixed by editing Y, now passes" or "new workflow command observed". Phrasing comes from the agent or an optional helper. Only accepted or completed changes are saved, not proposals (Mem0 policy). | MEM-15, HCL-15 | should | 0.5 | The Pi adapter captures nothing as memory. | Candidates visible via `recall(include_pending, kind='candidate')` and ground. |
| PAR-GOV-15 | **Onboarding for an empty workspace.** When no memories are promoted, ground returns a checklist of proposal categories: build and test commands, conventions, task completion, module map. | SAS-20, LET-25 | should | 0 | None. | ground `onboarding {needed, propose[]}`. |
| PAR-GOV-16 | **Outcome feedback per memory.** `verify(outcome='helpful'|'misleading'|'stale_harm', note)` per principal. A small, bounded rank bias that never overrides verification or staleness. Primary stale-harm metric. | MEM-21 | should | 0.5 | `RecordFeedback('verify', paths)` boosts code search only. | verify outcomes; feeds EVAL-02. |
| PAR-GOV-17 | **Procedures as governed skills.** `kind=procedure` memories carry path hashes of the scripts they reference. Export to `.agents/skills` or `.claude/skills` with a provenance header, so a stale procedure is flagged. | LET-15 | should | 0.5 | None. | `recall(kind='procedure')`; `xmustard-ops skills export`. |
| PAR-GOV-18 | **Human-readable export and import.**<br>- Markdown with front-matter (id, state, verifiers, file hashes, revision) under `.xmustard/memories/<topic>/<slug>.md`.<br>- An optional git mirror with one commit per revision batch; a dirty repo refuses reset and keeps a backup ref.<br>- JSONL streaming export and import that preserve provenance.<br>- Imports land `pending`/`foreign` and are never auto-verified. | SAS-18, LET-24, MEM-30 | should | 0.5 | Opaque JSON. | `xmustard-ops memories export|import [--git]`. |
| PAR-GOV-19 | **Optional model-based fact extraction** from session episodes. Absolute dates, links to related memories, `origin=model`. Out of process, pending only. | MEM-28 | could | 0 in-tree; ext 300–1500 MiB | None. | Helper adapter (PAR-CTX-11). |
| PAR-GOV-20 | **Memory clustering by subsystem.** Label propagation over anchor co-occurrence plus directories. | MEM-29 | could | 2 T | None. | `explain(path)` → `memory_cluster`. |
| PAR-GOV-21 | **Delegate LLM-dependent memory operations to the calling agent.** `remember` returns deterministic `duplicates[]`, `conflicts[]`, and `suggested_supersedes[]`; verify accepts supersede or merge decisions. xMustard runs no in-process LLM. | MB-14 | should | 0 | None. | remember and verify. |

### 4.8 PAR-RCL: Memory retrieval

| ID | Requirement | Origin | Pri | MiB | Current | Target surface |
|---|---|---|---|---|---|---|
| PAR-RCL-01 | **Hybrid memory ranking.** FTS5 BM25 over title, content, and anchors with light deterministic stemming. Fused with path/anchor overlap, verification strength, recency, and a stale penalty, then thresholded. Optional dense lane from PAR-RET-03. `explain=true` returns `score_details` (bm25, path, anchor, trust, recency, stale_penalty, dense?). | MEM-09, XM-23 | must | 3 | +1 per shared query token with no IDF or stemming (`context_governance.go:900-955`). | `recall(explain=true)`. |
| PAR-RCL-02 | **Code-anchor index.**<br>- Anchors extracted deterministically at propose time: explicit paths, backticked or qualified identifiers (regex `[A-Za-z_][\w-]*(?:\.[A-Za-z_][\w-]*)+`), file-like tokens, CLI commands, env and config keys, error codes.<br>- Symbols resolved against the index.<br>- Inverted index anchor → memories, with the fan-out-damped boost `sim·0.5/(1+0.001(n−1)²)`.<br>- Memories attach to impact and explain.<br>- Drift is checked at symbol granularity (symbol body hash), not whole file. | MEM-10 | must | 3 | Paths only (+1.5 overlap); file-hash drift. | `remember` → `anchors[]`; impact and explain `memories[{id, title, trust, stale}]`. |
| PAR-RCL-03 | **Graph-proximity recall.** `seed=<symbol>` ranks memories whose anchors are within 2–3 hops, weighted 1/(1+d). With no query it seeds from changed symbols. Bounded MMR-style diversity over a candidate set of ≤100, never an O(n²) matrix. | MEM-23 | should | 1 | None. | `recall(seed=...)` → `score_details.graph_distance`. |
| PAR-RCL-04 | **Session-aware recall.** Memories already delivered in this session are suppressed unless their content or stale state changed, with an `already_shown` count. Hard `max_chars` budget (Mem0 default 4,000, clamped 1,000–10,000). Compact one-line render; fetch by id. TTL eviction on seen-sets. | MEM-16 | should | 0.5 | Up to 8 full entries on every recall. | `recall(session_id, max_chars, render='compact'|'full')`. |
| PAR-RCL-05 | **Progressive disclosure.** `names_only` returns ids, titles, topic, state, stale, and paths. `topic` filter using "/" names. The promoted-title index goes into MCP instructions. | SAS-17 | should | 1 | None. | `recall(topic, names_only=true)`. |
| PAR-RCL-06 | **Cursor pagination and memory pressure.** `next_cursor` and `omitted` on recall and search. ground reports `memory_pressure {core_pct, pending_count}`. | LET-23, SAS-21 | should | 0.2 | Single top-N window (max 50). | recall, search, ground. |
| PAR-RCL-07 | **Session transcript store** (MemGPT recall memory). Immutable session events and transcripts, labeled raw observations and never verified. FTS search with date, agent, and conversation filters; cursor expansion around a hit ("needle and expand"). Streamed ingest. | LET-11 | should | 4–6 (FTS cache in the SQLite cap) | Evidence covers xMustard's own outputs only. | `recall(source='sessions', query, since, until, agent, cursor)`; expand via evidence handles. |
| PAR-RCL-08 | **New-session bootstrap.** Up to 5 recent and 5 relevant prior sessions. Relevant sessions must reach RRF ≥0.012 and ≥0.93 of the top score. Labeled "lightweight context, not confirmed facts". Descriptions come from handoff capsules, not an LLM. | LET-12 | should | 0.5 | None. | `ground(query?)` → `recent_sessions`, `relevant_sessions`. |
| PAR-RCL-09 | **Memory reference integrity.** Flags entries that reference retired or superseded entries (`refs_stale`), with fuzzy candidate suggestions. | SAS-19 | could | 1 | None. | recall field. |

### 4.9 PAR-PROV: Provenance and temporal model

| ID | Requirement | Origin | Pri | MiB | Current | Target surface |
|---|---|---|---|---|---|---|
| PAR-PROV-01 | **Append-only memory history.** Event types: propose, vote, reject, edit, promote, supersede, merge, stale_observed, drift_cleared, retract, purge, feedback, tier_change. Each event records principal, time, HEAD sha, old and new content digests, and a note. Every content version stays retrievable, and each verdict binds to a specific revision and digest. | MEM-03, LET-01, XM-23 | must | 1 | No history. Edits overwrite content and prune the previous content file. | `recall(entry_id, history=true)` → `{entry, events, versions}`; `verify(entry_id, revision?)`. |
| PAR-PROV-02 | **Bi-temporal validity.** World time as `valid_from` and `invalidated_at` (commit and time) plus system time (`created_at`, `promoted_at`, `expired_at`). `recall(as_of=<sha|RFC3339>)` uses a cached ancestry check. Intervals are closed, never deleted. | MEM-04, MB-13, XM-23 | should | 0.3 | Only CreatedAt and UpdatedAt. Staleness is computed at read time and never persisted. | `remember(valid_from)`; `recall(as_of, valid_only)`. |
| PAR-PROV-03 | **Change-driven invalidation, persisted.** Changed paths and symbols (watcher or HEAD change) are looked up in the anchor index. Only affected anchors are re-hashed. A mismatch appends `stale_observed(commit, anchors)` and sets `needs_reverify`. ground surfaces `at_risk_memories`. Recall-time drift remains as a backstop. | MEM-05 | must | 1 | Staleness is computed only for the recall window and never stored (`context_governance.go:165-190,958-975`). | ground `memory.at_risk[{id, title, paths, since_commit}]`; recall `stale_since`. |
| PAR-PROV-04 | **Provenance binding.** HEAD, branch, and dirty flag at propose and at promote. Links to evidence handles, runs, issues, sessions, and transcript excerpts, so a verifier can inspect how a claim was derived (the useful residue of Deep Search). Retraction cascade: an invalidated source flags its memories for re-verification. | MEM-13, SAS-34 (residue), LET-01 (trailers) | must | 0.5 | Source principal, paths, and path hashes only (`context_governance.go:32-66`). | `remember(evidence, run_id, session_id)`; recall `provenance {head, branch, dirty, evidence, session}`; why_failed links memories. |
| PAR-PROV-05 | **Evidence-bound verification and principal distinctness.** A vote can cite an evidence handle, test outcome, or file hashes, checked at vote time. Principals record an owner and kind (human or agent, runtime, model). Optional policy: the verifier's owner must differ from the author's owner. | XM-24 | should | 0 | A verification is `{agent, approve, note, at}`. Distinctness is by token id, so one operator can self-verify with two tokens. | `verify(evidence_handle, note)`; recall `verification_basis`. |
| PAR-PROV-06 | **Session ledger and episodes.** A durable log per (workspace, client, session, principal): tool calls (name, args digest, handle, status), files read and modified, prompts and decisions, errors, compactions, and memories proposed and verified. Raw, derived, and verified records stay distinct. There is one coordinated writer. Sessions chain into threads per issue or branch, with deterministic summaries. | HCL-12, MEM-14, CB-11 | must | 1–2 (tail cache; rows in SQLite) | Runs and evidence exist, with no session record tying them together. | ground `continuity {recent_actionable, open_items}` and `last_sessions`; recall `session_id` filter. |
| PAR-PROV-07 | **Session and sub-agent identity for attribution.** Execution key `<client>:<session>:agent:<agent>`, taken from `X-Claude-Code-Session-Id`/`Agent-Id`, `metadata.user_id`, Pi session ids, or `X-Xmustard-Session`/`-Agent`, with a hashed fallback. Audit metadata only, never authorization. | CB-06 | must | 0.05 | The shim uses a random per-process sessionID. | recall and ground show `proposed_by {principal, session, agent}`. |
| PAR-PROV-08 | **Handoff capsule.** A schema-checked `{goals, what_happened, identifiers (verbatim), errors_and_fixes, current_state, next_step, lookup_hints}`, bounded to ≤500 words. Stored as session-scoped evidence with path hashes. ground returns the latest capsules with their stale paths. | LET-14, HCL-14 | should | 0.5 | `versioned_handoff.go` is issue-scoped only. | `remember(kind='handoff', ...)`; ground `handoffs[]`. |

### 4.10 PAR-SHARE: Multi-agent and cross-repo sharing

| ID | Requirement | Origin | Pri | MiB | Current | Target surface |
|---|---|---|---|---|---|---|
| PAR-SHARE-01 | **Scoped namespaces:** shared workspace, directory chain, private per principal (may self-promote; shown to others only as unverified), run/session, and global (user-level across workspaces). Scope and author are derived server-side from the principal. Recall merges scopes with labels. | MEM-06, LET-07, SAS-18 (global) | must | 0.3 | A single workspace scope. | `remember(visibility|scope, dir)`; `recall(scope='repo'|'dir'|'mine'|'run'|'global')`. |
| PAR-SHARE-02 | **Shared collections with explicit grants.** A grant is `(source_ws, target_ws or collection, scope, permission rw|ro, granted_by)`, and each collection has its own policy. Per-target applicability: `verified_here`, `foreign_unchecked`, or `stale`. A fact verified in repo A has no authority in repo B until it is rechecked there. | LET-06, XM-25, SAS-14 | must | 0.5 | No grants. `Principal.Workspaces` only narrows access. | `recall(include_shared|scopes=...)` → `{scope, source_workspace, applicability}`; `xmustard-ops shared list|create|grant|revoke|history`. |
| PAR-SHARE-03 | **Cross-workspace read-only code query.** search, explain, and impact accept `workspaces=a,b` or a group. Indexes load lazily with LRU (PAR-RT-10). Per-repo candidate caps. Writes stay per workspace. | SAS-14, GNX-21, MB-17 | should | 0 cold; T 10–30 | None. | search, explain, and impact args; per-repo coverage and freshness. |
| PAR-SHARE-04 | **Branch- and worktree-scoped index identity.** Linked worktrees are detected. Memories record the worktree and branch they were verified on, which prevents contamination across worktrees. | GNX-21 | should | 2 | Workspace id comes from the root path only. | workspace metadata; recall provenance. |
| PAR-SHARE-05 | **Cross-machine path handling.** Anchors are stored repo-relative plus repo identity. An explicit longest-prefix rewrite map handles inbound absolute paths from other hosts. | CB-23 | should | 0 | Absolute-path assumptions in places. | All tools accept foreign absolute paths. |
| PAR-SHARE-06 | **Memory event feed and notifications.** A cursor-paginated feed over PROV-01 events. MCP `resources/subscribe` on `xmustard://workspace/{id}/memory-revision`. No outbound webhooks. | MEM-20, LET-27 | should | 0.5 | None. | `recall(events_since, types)`; MCP resource notifications. |
| PAR-SHARE-07 | **Typed progress event bus.** A 128-event ring with non-blocking fan-out to 64-buffer subscribers (drop on full). Published by index refresh, verify, evidence capture, and runs. MCP `notifications/progress` when the client sends a `progressToken`. SSE route for operators. | CB-10 | should | 0.3 | None. | MCP progress; `GET /api/workspaces/{id}/events`. |
| PAR-SHARE-08 | **Memory health block with an advisory next action.** Pending for me, conflicts (path_overlap, revision_race, supersede_race), stale entries, consolidation jobs, in-flight and interrupted runs, and a deterministic `next_action {type, title, detail, recommended_tool}`. It never redirects agent calls. | LET-09, CB-13 | should | 0.5 | None. | ground `memory_health`, `next_action`. |
| PAR-SHARE-09 | **Delegated-work dedupe and restart honesty.** verify returns the existing in-flight verification run for the same claim and revision. On restart, pending and running runs are marked failed with "interrupted by restart". | CB-12 | should | 0.3 | None. | verify `{reused_run, status}`; ground `in_flight`, `interrupted`. |

### 4.11 PAR-CTX: Context reduction

| ID | Requirement | Origin | Pri | MiB | Current | Target surface |
|---|---|---|---|---|---|---|
| PAR-CTX-01 | **Universal observation capture** for any tool (native, MCP, xMustard). Streamed body; JSON hook bodies are stream-decoded straight into the spool. Metadata: tool name and version, call id, session id, client, principal, args digest, is_error or exit code, content type, output shape. Redaction applies. Hook-delivered bytes are labeled `captured_identity=unknown`. | HCL-01, XM-29 | must | 2 | `POST /api/workspaces/{ws}/evidence` exists (Pi path). Delivery is limited to the nine routes (`evidence_routes.go:40-80`). The Pi hook skips other tools (`integrations/pi/src/index.ts:80-83`). | HTTP capture API; no new MCP tool. |
| PAR-CTX-02 | **Tool-family deterministic reducers**, versioned per projection.<br>- Shell, test runners, build, git, grep, read, ls/glob, logs, lints.<br>- Keep the exit code, failing tests and assertions, first and last stack frames, error lines, and counts; collapse passing lines, progress output, and repeats.<br>- Head+tail failure-preserving excerpts (Letta 30%+30%) with omission markers.<br>- Grep capped per file with totals; read as a line-numbered requested range.<br>- Per-kind projections: read `{path, line_count, excerpt}`, list ≤40, glob ≤60, search ≤40 matches.<br>- Structured payloads are bounded too, and status fields are never dropped. | HCL-02, CB-04, LET-17 | must | 2 | `reduce.go` ReducerVersion `xm-reduce/1` handles generic JSON and text salience only. | Projection text plus `[xmustard evidence]` footer or `_meta xmustard/evidence`. |
| PAR-CTX-03 | **Per-client budgets and output-shape adapters.**<br>- Claude built-ins must match the tool's output schema or they are ignored (Bash `{stdout, stderr, interrupted, isImage}`).<br>- additionalContext is capped at 10,000 characters; MCP output above 25k tokens is persisted by the client.<br>- Letta's bash clamp is 30k characters. Pi uses TextContent/ImageContent plus details. OpenCode uses `{title, output, metadata}`.<br>- The 64 KiB threshold is lowered where useful. | HCL-03, LET-19, LET-17 | must | 0.3 | Below 64 KiB, output passes through. | Adapter policy table; the nine tools are unchanged. |
| PAR-CTX-04 | **Recovery with search inside the original.** Regex or query plus line-range search over a streaming 1 MiB-chunk scan with a match cap. Available on every client route: `resources/read xmustard://evidence/{handle}?offset&length&pattern&lines`, Pi `xmustard_expand`, or a fallback where resources are unsupported (see D-07). Fails closed on scope, principal, and expiry. | HCL-04 | must | 1 | Byte paging only. | resources/read args; `xmustard_expand(pattern)`. |
| PAR-CTX-05 | **Output budget and pagination contract on all nine tools.** `max_chars`, `limit`, `cursor` → `has_more`, `next_cursor`, `degraded_stage`, `recover_handle`. Each tool declares a staged degradation ladder: symbols → name-path map; overview → depth 0 → counts; refs → no snippets → per-file counts → total. Out-of-range values are rejected, not clamped. | SAS-21, GNX-17, LET-23 | must | 1 | Recoverable projection only above 64 KiB. | All nine tools. |
| PAR-CTX-06 | **Identity-bound dedupe and repeat guard.** A per-session LRU keyed by (tool, canonical args, raw sha256, repo revision) returns "unchanged since call X; handle H". Error-class-aware repeat annotation (validation, not_found, permission, timeout, tool_error; maxRepeat 2 strict, 3× coarse, 6× exploratory). Never applied to side-effecting commands. | HCL-06, CB-02 | should | 1 | None. | Projection text `[xmustard repeat] {...}`. |
| PAR-CTX-07 | **Retroactive masking** of tool results older than N turns in Pi (context_edit at turn_end) and OpenCode (messages.transform). The mask advances in polling windows so the provider prompt cache survives. The latest failure and actively edited files are exempt. Raw entries are kept. | HCL-05 | should | 0 (in client) | None. | Adapter-side stub `[xmustard masked: N lines; handle ...]`. |
| PAR-CTX-08 | **Recoverable deterministic history compaction** over structured message arrays (Anthropic content blocks, Pi messages). Keep the most recent N; summarize by kind; express todo and plan changes as deltas; dedupe guidance. Every replaced tool_result keeps its `tool_use_id` and `is_error` and carries a handle. Tool exchange pairs stay atomic. Never byte-sliced. | CB-03 | must | 0 / 2 T | None. | Library used by the gateway (HAR-09) and the Pi/OpenCode context hooks. |
| PAR-CTX-09 | **Retention classes and upstream references.** Separate TTLs and quotas for hook-captured native outputs, xMustard outputs, and snapshots. When the client already persisted the original, a hashed upstream reference replaces the copy. When the quota is full, return a bounded passthrough or an explicit failure; an unexpired promised original is never evicted. | HCL-07, MB-19 | should | 0.2 | 24 h and 256 MiB only; a full quota rejects. | `expires_at` and `retention_class` in envelopes. |
| PAR-CTX-10 | **Command-wrapper reducer** (RTK pattern). `xmustard-core run -- <argv>` streams child output to the spool with a bounded tail, preserves exit code and signals, and prints the projection plus a handle. Installed through PreToolUse input rewriting where the client cannot replace results. | HCL-21 | should | 0 / 3 T | None. | Wrapped shell output. |
| PAR-CTX-11 | **Helper-model adapter contract**, off by default. An isolated, serialized native worker with bounded input, time, output, and concurrency, and telemetry disabled. It returns candidate IDs, scores, or an abstention. Rust validates results against the deterministic shortlist and current snapshot and falls back on uncertainty. Output parsing is robust and fails closed (strip fences, take the last typed object, balanced-brace extraction). It never promotes or authorizes. | HCL-27, CB-20, MB-15 | should | 0.5 | None. | None directly; projection metadata `helper {model, version, abstained, confidence}`. |
| PAR-CTX-12 | **Local helper models chosen by measured RSS.**<br>- Model2Vec static embeddings are the only resident candidate.<br>- Needle3 runs only as an on-demand serialized worker in the heavy slot, after its RSS is measured.<br>- FunctionGemma (551 MB peak) and LFM2.5-230M (293–375 MB) are excluded.<br>- Jev is hosted-only and needs explicit permission. | HCL-28, MB-15 | could | 0 default; Needle3 ext est 45–90 (unmeasured) | None. | None. |
| PAR-CTX-13 | **Token estimation and budget introspection.** A bytes/runes heuristic by default; exact BPE is loaded lazily and only for evaluation. `delivered_tokens_est` on envelopes. Core-memory token report. | CB-27, LET-21 | should | 0.2 | None. | Envelope field; ground `memory_budget`; `xmustard-ops memory tokens`. |
| PAR-CTX-14 | **Revision-keyed read-only result cache.** Key: tool, canonical args, repo revision key, parser version. Scoped per principal, capped by bytes (not entries), and shares the prior observation's handle. Prompt or final-answer caches are never used. | CB-14 | should | 2 | None. | search, explain, impact, diagnostics `cache {hit, revision_key}`. |

### 4.12 PAR-HAR: Harness integration

| ID | Requirement | Origin | Pri | MiB | Current | Target surface |
|---|---|---|---|---|---|---|
| PAR-HAR-01 | **Hook service in the daemon** at `/api/hooks/<client>/<event>`, plus a static hook client for command-type hooks. The client talks over a Unix socket, fails open within ~200 ms, and never spawns Rust or git. Events: SessionStart, UserPromptSubmit, PreToolUse, PostToolUse (plus Failure and Batch), PreCompact/PostCompact, Stop, SessionEnd, FileChanged. HTTP and `mcp_tool` hook types are preferred because they spawn no process. | GNX-19, SAS-25, LET-18, MB-20, MEM-15 | must | 1–2 in daemon; client 3–10 T per invocation | Pi extension only. | New surface, justified because MCP cannot intercept native tools (CONTEXT_LAYER). |
| PAR-HAR-02 | **Pre-tool injection and steering.**<br>- Grep, Glob, and `Bash rg|grep`: extract the pattern (first non-flag token of ≥3 characters; skip `-e -f -m -A -B -C -g -t --include --exclude`) and inject top BM25 and graph hits **plus verified memories** for it.<br>- Read or Edit of a path: inject verified and stale-flagged memories bound to that path.<br>- A grep-burst counter nudges toward search or impact, with a 2-minute cooldown.<br>- A post-git (commit, merge, rebase, cherry-pick, pull) freshness notice.<br>- The busy fallback is throttled. | GNX-19, SAS-25 | must | 0 | None. | Hook additionalContext, bounded, with a recovery handle. |
| PAR-HAR-03 | **Revision-pinned memory injection and guidance artifacts.**<br>- MCP `initialize.instructions` carries the workflow (ground → recall → search/impact → remember → verify) plus a budgeted core projection and deferred index, compiled only from promoted state and cached by (revision, budget).<br>- `ground(since_revision)` returns deltas (added, changed, superseded, stale) with "newer wins".<br>- SessionStart additionalContext.<br>- A marker-bounded, line-anchored AGENTS.md/CLAUDE.md block with original prose.<br>- `initial_instructions` fallback for clients that drop instructions. | LET-05, SAS-23, GNX-18, HCL-08 | must | 1 | `initialize` returns no instructions (`xmustard-mcp/main.go:464-468`). | MCP initialize; ground; hooks; `xmustard-ops guidance write`. |
| PAR-HAR-04 | **Compaction snapshot and restore.**<br>- At the client's compaction boundary, build a ≤2 KB priority-tiered snapshot: goal, active files, open failures with handles, pending proposals, decisions.<br>- Re-inject after compaction: Claude Code/Codex SessionStart `source=compact`; OpenCode `experimental.session.compacting` and `system.transform`; Pi a custom CompactionEntry whose `details` carry handles.<br>- The snapshot is derived data, never verified memory. | HCL-13, LET-14 | must | 0.5 | None. | Hooks; ground `session_id`. |
| PAR-HAR-05 | **Cross-client handoff at session start.** A new session in any client gets the prior snapshot, unexpired handles, proposals waiting for a distinct verifier, and current drift. Client transcripts (Claude `transcript_path`, Pi session JSONL, Codex rollouts) are hashed upstream references, streamed with a byte cap. | HCL-14 | should | 1 | Continuity works only for promoted memory. | ground `session_handoff`. |
| PAR-HAR-06 | **why_failed and ground failure signals without platform runs.** why_failed accepts a `command` (run through the bounded `run-managed-command`), an `evidence_handle`, or a pasted log. The outcome record lives in the core store and is read as a bounded tail (last 1 MiB). Captured native failing test and build outputs (`is_error` or nonzero exit) become run-independent failure records. The GET is idempotent. | XM-13, HCL-26 | must | 0.5 | `failure_explainer.go:43-52` needs a run that only platform routes create and reads the full output. ground's `recent_failed_runs` is always empty for pure-MCP users. | `why_failed(run_id | command | evidence_handle | log)`; ground. |
| PAR-HAR-07 | **diagnostics without Postgres, deepened.** A local store (in flight in the diagnostics session). Arguments: path, symbol, min_severity (1–4), range, with_references, `since=checkpoint|baseline`, returning new and resolved sets. Grouped by path → severity → owner name path. Structural findings (import cycles). Precision provenance. | XM-14, SAS-29, GNX-16 | must | 1.5–2 | `diagnostics.go:433-448` requires a Postgres DSN, so the tool always fails on the local default. Local store in flight in `/private/tmp/xmustard-opus-l9b1F4` (not to be touched). | `diagnostics(path?, symbol?, min_severity?, range?, with_references?, since?)`. |
| PAR-HAR-08 | **Opt-in upstream MCP gateway.**<br>- Namespaced catalog `server__tool` with deferred discovery (`tool_search` returning `tool_reference` blocks or schemas, schema fetch, call), ranked by BM25 over names, descriptions, and argument names.<br>- Preserves request ids, cancellation, progress, pagination, `list_changed`, and structured results.<br>- Per-principal allowlists. Downstream results are projected through CTX-01..04.<br>- Idle eviction and a max number of live servers. HTTP downstreams are preferred; stdio downstreams count toward the tree.<br>- Explicit profiles (analysis, full); permissions are never gated by keywords. | HCL-09, HCL-10, CB-08, LET-16, CB-19 | should | 5 (+ext downstream servers 40–100 each) | None. | New surface: a gateway catalog separate from the nine tools. |
| PAR-HAR-09 | **Opt-in model/API gateway.** Anthropic Messages pass-through that rewrites only fresh tool_result content. Preserves `tool_use`/`tool_result` pairing, role order, signed thinking, and SSE streaming. No 64 MiB buffering. Loopback only. Version-tested. Does not change billing or auth routes. | CB-05, HCL-22 | could | 6 idle; must stream | None. The owner's cursor-bridge `anthropic_gateway.go` is a reference. | New optional binary; justified only for Cursor built-in read/grep and history reduction. |
| PAR-HAR-10 | **Bounded verification and run-lineage runner.** A state machine for runs of tests, checks, and verifications, with outcome records, lineage, and interrupted-run honesty. Child tools are external, bounded by CaptureWriter and ChildLimit, and reported separately. No agent loop. | MB-21 | should | 1 steady / 6 while running | The goal runtime exists as a CLI (`goalruntime.rs`, Go shell). | why_failed; ground; admin run control. |
| PAR-HAR-11 | **MCP prompts, resources, and hints.** Prompts `pre_commit_check` and `orient`. Resources for areas, flows, workspaces, and memory revision. Terse next-step hints, off by default for token cost. Optional generated per-area skills. | GNX-18 | should | 0.5 | `resources/list` serves evidence originals only. | MCP prompts and resources. |
| PAR-HAR-12 | **Sanitized adapter traffic capture and fixture corpus.** Redacted request/response and hook payloads per client version, with 0600 files and retention quotas, backing conformance tests. | CB-21 | should | 0.5 | None. | `xmustard-ops capture-fixture` (operator). |

### 4.13 PAR-ADP: Client adapters and MCP protocol

| ID | Requirement | Origin | Pri | MiB | Current | Target surface |
|---|---|---|---|---|---|---|
| PAR-ADP-01 | **MCP protocol modernization and honest descriptions.**<br>- Current protocol version negotiation.<br>- Server `instructions`.<br>- ToolAnnotations: `readOnlyHint` on the seven read tools; destructive and idempotent hints on remember and verify; titles.<br>- `outputSchema` plus `structuredContent`, keeping text content.<br>- `_meta['anthropic/maxResultSizeChars']` where a larger bounded output is intended.<br>- Closed schemas; out-of-range bounds rejected; error codes.<br>- OpenAI/Codex schema sanitization.<br>- Hidden compatibility aliases (Claude Code drops an argument named `query`).<br>- WHEN TO USE / AFTER THIS descriptions.<br>- **Honest names**: "identifier search with typo tolerance"; "lexical reference graph; distance ≥1 edges are leads"; conflicts = path overlap. | GNX-29, SAS-23, HCL-08, XM-11, XM-19, CB-18, MEM-25 | must | 0 | protocolVersion 2024-11-05 (`main.go:28`). No instructions, annotations, or structured output. Descriptions say "semantic" and "graph BFS" (`main.go:119,138`); fix in flight on `parity/w0-cleanup` 0d30698. | `tools/list` and `initialize` of the nine tools. |
| PAR-ADP-02 | **Workspace auto-resolution.** `workspace_id` is optional on all nine tools. It resolves from `XMUSTARD_WORKSPACE_ID`, MCP `roots/list`, shim cwd, or a `path` argument through the deterministic `workspaceIDForPath`, and a lazily indexed workspace is auto-registered. Results echo `{workspace_id, root}`. | SAS-15, XM-19, GNX-21 | must | 0.5 | Required on all nine tools, with no listing tool. | All nine tools. |
| PAR-ADP-03 | **Tool-argument alias normalization and validation.** A ~70-entry alias table plus canonical keys (lowercase, strip `_ - ` and spaces). Per-tool repair: `tool@server` and `server/tool` splitting, glob `.go` → `*.go`, `file://` stripping. Required-field validation with structured errors naming the canonical field. Normalizations are recorded in evidence. Mutating arguments are never silently changed. | CB-01 | must | 0.1 | Strict argSpec `known` map (`xmustard-mcp/main.go:402`). | All nine tools and the adapter HTTP paths. |
| PAR-ADP-04 | **Deepened argument surface of the nine tools**: the MEM-25 mapping (§5) plus bounds the API already supports but MCP hides (recall `limit`, search `limit`, impact `depth`, verify `note`). | MEM-25, XM-19 | must | 0 | recall `query, paths`; remember `content, title, paths`; verify `entry_id, approve`. | §5 surface map. |
| PAR-ADP-05 | **Per-client profiles and read-only mode.** `--client=claude-code|codex|cursor|opencode|pi|letta` and `--mode=readonly|full` choose the tool subset and description variants. Read-only mode filters `tools/list` and rejects writes. Workspace allowlist. | SAS-24, GNX-28, HCL-11 | should | 0 | None. | Relay flags and HTTP endpoint query parameters. |
| PAR-ADP-06 | **Claude Code adapter plugin** (`hooks.json` plus `.mcp.json`). PostToolUse HTTP hook returns shape-matched `updatedToolOutput` for Bash, Read, Grep, Glob, WebFetch, and MCP. PostToolUseFailure adds additionalContext only. PostToolBatch notes. PreToolUse `updatedInput` wraps commands (optional). SessionStart via `mcp_tool` → ground. PreCompact/PostCompact. `watchPaths`/FileChanged. Fails open to the original within budget. | HCL-16 | must | 0 | None. | Packaging on HAR-01. |
| PAR-ADP-07 | **Codex adapter.** PostToolUse `decision:"block"` plus a reason substitutes the projection and handle as feedback. PreToolUse `updatedInput` for shell, apply_patch, and MCP, version-gated and conformance-tested. SessionStart and UserPromptSubmit context. PreCompact/PostCompact. `mcp_tool` handlers so no process is spawned. | HCL-17 | must | 0 | None. | `.codex/hooks.json` packaging. |
| PAR-ADP-08 | **OpenCode adapter plugin.** `tool.execute.after` mutates `output.output`, `title`, and `metadata`, and the mutated object is what the session returns. `experimental.chat.messages.transform` for masking, `experimental.session.compacting` for the snapshot, `experimental.chat.system.transform` as a SessionStart substitute, and `tool.definition` for compact descriptions. v2 equivalents. | HCL-18 | must | 0 (runs in Bun, external) | None. | TypeScript plugin package. |
| PAR-ADP-09 | **Pi adapter extended to built-in tools.** Projects bash, read, grep, find, ls, edit, and write through CTX-01, preserving `isError` and `details`. turn_end `context_edit` masking. `session_before_compact` custom summary carrying handles. `tool_call` input mutation for command wrapping. | HCL-19, XM-29 | must | 0 (runs in Pi Node, external ~130 MB) | Only the nine tools are projected. | `integrations/pi`. |
| PAR-ADP-10 | **Cursor adapter.** postToolUse `updated_mcp_tool_output` covers MCP only. preToolUse `updated_input` wraps shell commands. sessionStart `additional_context`. preCompact snapshot. failClosed handling. Built-in read/grep can be reduced only through HAR-09. | HCL-20 | should | 0 | None. | `.cursor/hooks.json` packaging. |
| PAR-ADP-11 | **Letta Code recipe.** An `mcpServers` entry, tool descriptions tuned for `letta mcp search`, responses ≤30k characters with recovery handles, and `client_profile=letta` to lower the projection threshold. Letta's hosted agent-messaging MCP is not copied. | LET-19 | should | 0 | None. | Docs, profile, and a contract test. |
| PAR-ADP-12 | **Cross-harness session import.** Streaming Go readers for Claude Code, Codex, OpenCode, and Pi session stores, filtered by project path, normalized into the session store. Imported spans are what consolidation jobs cite. | LET-13 | should | 1 / 10–20 T (transient process) | None. | `xmustard-ops import sessions --source ... --project ...`. |
| PAR-ADP-13 | **One-command install, uninstall, and doctor per client.** Version detection, preflight (health plus evidence round-trip), backup, atomic swap, automatic rollback, and a savings report (raw versus delivered bytes, expansions). Never extracts keys from plists and never exports proxy keys. | GNX-20, HCL-31, CB-26 | should | 0 | HEAD-only Homebrew formula. | `xmustard-ops setup|uninstall|doctor --client ...`. |
| PAR-ADP-14 | **Adapter capability registry** with explicit unsupported errors: expand route (resources_read, xmustard_expand, or none), result replacement, progress, and images, per client and version. | CB-22 | should | 0 | None. | ground `delivery_capabilities`; `xmustard-ops status`. |

### 4.14 PAR-SEC: Security and auth

| ID | Requirement | Origin | Pri | MiB | Current | Target surface |
|---|---|---|---|---|---|---|
| PAR-SEC-01 | **Author-bound, role-gated memory edits.** Strict JSON decode (400/413 on a bad body). Denials are audited. | XM-07, MEM-19 | must | 0 | `PUT /context/{id}` is ungated (`main.go:3912-3922`), and decode errors are discarded (`:3853,:3889`). Fix in flight on `parity/w0-kernel`. | HTTP 403 for non-author edits. |
| PAR-SEC-02 | **Principal roles bound to tokens**: reader, proposer, verifier, human-approver, admin. verify requires the verifier role. Protected memories and policy changes require human-approver. ground reports the caller's principal and roles. Errors name the missing role. | SAS-28, LET-03, SAS-19 | must | 0.5 | admin > agent > readonly; distinctness by token id. | Errors; ground `principal`. |
| PAR-SEC-03 | **Transport and exposure posture.**<br>- Loopback by default; non-loopback requires a token and TLS.<br>- Host/Origin allowlist on localhost; constant-time compare everywhere.<br>- No keys in query strings; no remote-execution endpoint.<br>- Read-only mode; per-deployment tool disable; workspace allowlist.<br>- **Negative requirements from cursor-bridge (do not copy):** env-value leak in MCP discovery, `?api_key=` browser auth, non-constant-time compare, full-header CDP logging, plist key extraction, Tailscale Funnel exposure, and browser-header spoofing. | GNX-28, SAS-28, CB-25 | must | 1 | Modes auto, required, and off (loopback), and non-loopback requires TLS. No Host allowlist or read-only mode. | Config; filtered `tools/list`. |
| PAR-SEC-04 | **Secret and PII redaction everywhere.**<br>- Covers memory ingest, render, capture, transcripts, imports, handoff capsules, overflow, diagnostics, and fixtures.<br>- An RE2 pattern set: bearer, AWS, GitHub, Slack, OpenAI/Anthropic keys, PEM, JSON secret fields; plus an entropy check.<br>- Env values replaced by names; header allow-list; files written 0600.<br>- Returns a structured reject or `redacted:true` with a count. | MEM-18, LET-20, CB-09 | must | 0.5–1 | Only `redactPostgresDSN`. | remember errors; envelope `redactions`. |
| PAR-SEC-05 | **Scope integrity and path confinement.** Scope and author come from the principal, never from metadata (Mem0 #6655). Wildcards and delimiters are rejected in ids and tags. Anchors are confined to the repo root with EvalSymlinks (reuse `safepath.go`). Absolute paths and symlinks cannot escape (cursor-bridge `resolvePath` is the counter-example). | MEM-19, CB-24 | must | 0 | `safepath*.go` exists for some routes. | Error `path escapes workspace`. |
| PAR-SEC-06 | **Repository-trust gate.** Repository-supplied `.xmustard` config that could run commands is ignored until the repository is trusted. | SAS-28 | should | 0 | None. | Config. |
| PAR-SEC-07 | **Token store cache.** Cached by mtime and size, invalidated on mint, rotate, or revoke. | XM-26 | could | 0 | Parsed on every request, twice in open mode. | None. |

### 4.15 PAR-EVAL: Evaluation and measurement

| ID | Requirement | Origin | Pri | MiB | Current | Target surface |
|---|---|---|---|---|---|---|
| PAR-EVAL-01 | **Paired outcome harness** (workflow_bench design).<br>- Arms: baseline, baseline_nomcp, xmustard_mcp, xmustard_mcp+hooks, xmustard+memory, and peer comparators (GitNexus and Serena installed separately, never vendored).<br>- A fresh detached worktree per run; hidden harness-owned oracles in addition to the model-visible verify step.<br>- Tokens and cost from each client's final events (`claude -p` stream-json, `codex exec --json`, `pi --mode rpc`); diff churn; task classes; fixed model and seed.<br>- McNemar and bootstrap statistics.<br>- No Docker in the default lane; SWE-bench only in an eval-only lane. | GNX-30, SAS-37, XM-27 | must | 0 (outside tree) | `memory_harness.go` has the statistics for four arms but no corpus and no executor. | `xmustard-eval` (operator). |
| PAR-EVAL-02 | **Coding-memory lifecycle suite.** Fixtures for duplicate, supersession, contradiction, stale drift, expiry, and scope leak, plus a consolidation arm. Metrics: current-fact recall@k, stale-served rate, superseded-served rate, duplicate rate, contradiction precision and recall, scope leakage (must be 0), tokens per recall, promotion errors, stale-memory harm. | MEM-27, LET-26 | must | 0 | None. | Eval corpus. |
| PAR-EVAL-03 | **Context-reduction quality evaluation.** Compares raw, deterministic projection, masking, and client LLM compaction on the same tasks. Metrics: evidence recall, false omissions, expansion rate, solve rate, tokens. Includes rare-failure fixtures (one failing test among thousands; a single contradictory source). | HCL-30 | must | 0 | A fixed MCP fixture passes; there is no paired task comparison. | Eval corpus. |
| PAR-EVAL-04 | **Per-tool usage and data-movement accounting.** Calls, argument bytes, response bytes, estimated tokens, projection savings, latency, process spawns, git spawns, bytes hashed, read, and parsed per call. Persisted per session and workspace. | SAS-26, XM-27, MB-24 | must | 1 | The benchmark marks Rust and shim allocations, byte copies, and memory bandwidth as "unmeasured". | `/api/health` counters; `xmustard-ops stats`; optional `_meta.usage`. |
| PAR-EVAL-05 | **Parity-scale budget gate with dual metrics.**<br>- ps-RSS stays the gate (≤95.4 MiB). Also reports phys_footprint (macOS) or PSS/USS (Linux), anon versus file-backed per process, and component attribution; external processes on separate lines.<br>- Fixtures: a real multi-language 5–10k-file repo (gitnexus and cline clones at pinned shas), 17 grammars, static embeddings on, 1, 2, and 4 agents over HTTP MCP, watcher on, 2 hot repos, captures during indexing.<br>- `rss_bench.py` is frozen at 6411f391, so this is a new v2. | MB-24, HCL-29 | must | 0 | 501 generated files with syntax errors; ps-RSS at 100 ms; one machine. | CI and report artifacts. |
| PAR-EVAL-06 | **Footprint and outcome comparison against OpenHands, Pi, and a bare agent** on the same tasks and model. The xMustard-owned tree is measured separately from external agent trees (Pi ~130 MB per Node process; *measured* 263.6 MiB for two). OpenHands is measured, not cited from its 4 GB guidance. | HCL-29 | must | 0 | Only the working-set size has a number. | Eval artifacts. |
| PAR-EVAL-07 | **Pinned client conformance.** Per client version: the replacement reaches the next model request, shape matches, errors are preserved, abort and cancel work, and expired or foreign handles are denied. Discrepancies are resolved by test (for example Codex `updatedInput`). | HCL-31, CB-21 | should | 0 | Pi e2e with a scripted faux provider exists. | Test suite. |
| PAR-EVAL-08 | **CI**: `make check-backend`, the retrieval gate, and the budget gate on PRs; tagged releases. | XM-27, XM-28 | must | 0 | No `.github` workflows and no tags. | `.github/workflows`. |
| PAR-EVAL-09 | **Component benches**: localization versus peers (retrieval gate), impact precision against LSP or SCIP ground truth, freshness latency after edit, rename, or delete (CONTEXT_LAYER acceptance), and incremental-work counters. | GNX-30, SAS-37, GNX-08 | must | 0 | Retrieval gate only. | Bench scripts. |

### 4.16 PAR-OPS: Ops and packaging

| ID | Requirement | Origin | Pri | MiB | Current | Target surface |
|---|---|---|---|---|---|---|
| PAR-OPS-01 | **Budget and health reporting.** Per-component reserved and used, heavy-slot owner and queue, GC and heap stats, and the counters from EVAL-04. | MB-01, MB-22 | must | <0.5 | Basic health only. | `/api/health`. |
| PAR-OPS-02 | **Ops CLI**: `status [--json]`, `doctor` (languages, precision lanes, index age, store health, RSS snapshot, DB versus in-memory counts), `clean [--stale]`, `index --prewarm`, `is-ignored <path>`, `lsp status|restart`. | GNX-31, SAS-33 | should | 0 | Partial `xmustard-ops`. | `xmustard-ops`. |
| PAR-OPS-03 | **Release hygiene.** Untrack the 80 `backend/data` runtime files (git rm --cached; human confirms), reconcile `.gitignore`, tag releases with a prebuilt `xmustard-core`, fix the Homebrew formula. | XM-28 | should | 0 | `backend/data` tracked; HEAD-only formula. | Packaging. |
| PAR-OPS-04 | **Licensing and provenance rules** (§2.4), enforced in review. Every ported file carries attribution and NOTICE. | all readers | must | 0 | n/a | Review checklist. |

### 4.17 PAR-X: Explicitly out of scope (skip), with reasons

| ID | Item | Origin | Reason |
|---|---|---|---|
| PAR-X-01 | Raw Cypher and an embedded graph DB (LadybugDB/Kuzu, Neo4j, FalkorDB, Neptune); an external vector DB (Qdrant, pgvector) in the default. | GNX-25, MB-27 | The LadybugDB buffer pool floor is 64–256 MiB, the default is 80% of RAM up to 2 GiB, and one issue reports 19.5 GiB RSS. Neo4j is a JVM at ≥500 MB. SQLite plus custom segments gives functional parity. The Postgres mirror stays a platform-only option. Alternative: PAR-SYM-11. |
| PAR-X-02 | LLM wiki generation, the web graph explorer, the `serve` HTTP API, and remote auto-sync. | GNX-27 | Needs a hosted LLM; UI is out of focus; hosted operations. `wiki.rs` stays because it has a Go caller. |
| PAR-X-03 | Symbolic and line editing tools (replace_symbol_body, insert_*, replace_in_files, and so on) and bridge runtime pieces (terminal sessions, apply_patch, subagents, computer_use). | SAS-30, CB-30 | The outer agent owns writes. Serena's own evaluation finds about 4.5× more payload than native Edit for 1–3-line edits. Owning writes violates the read-and-verify boundary. The design reference kept: per-edit outcome schema. |
| PAR-X-04 | Agent-native duplicate tools (read_file, list_dir, find_file, shell). | SAS-32 | Serena removes these itself for Claude Code and Codex. |
| PAR-X-05 | Deep Search hosted agentic research. | SAS-34 | Hosted and metered; a local LLM would be 1–8 GB. The residue lives in PAR-PROV-04. |
| PAR-X-06 | Web dashboard and JetBrains IDE backend. | SAS-36 | UI out of focus; requires an IDE. |
| PAR-X-07 | Mem0 procedural memory type (deprecated upstream) and image memory. | MEM-31 | Covered by `kind=workflow|procedure`; images are out of scope. |
| PAR-X-08 | Hosted platform management (orgs, dashboard, graph view, webhooks, batch), managed graph DBs, Augment remote index, Zep managed, Mem0 platform, Jev-by-default. | MEM-32, MB-28 | Hosted-only; moves RSS off-box instead of reducing it. At most an opt-in remote scorer with explicit data permission. |
| PAR-X-09 | Full agent runtime (subagents, host compaction, channels, crons, remote computers, desktop app, personalities, mods, provider routing, sandbox, agent-messaging MCP). | LET-28 | xMustard augments existing agents. The Node/ink stack is estimated at 150–350 MB. |
| PAR-X-10 | Sandboxed execute and index tools (ctx_execute style). | HCL-32 | Makes xMustard an execution runtime; spawns node/python runtimes (30–100+ MB each); ELv2. Covered by PAR-CTX-10. |
| PAR-X-11 | Container sandbox runtime and hosted conversation server (OpenHands). | HCL-33 | Docker and gigabytes. |
| PAR-X-12 | An LLM summarizing condenser run by xMustard. | HCL-34 | Clients own compaction. Masking matches summarization (arXiv 2508.21433). No in-budget local model. Replaced by CTX-07/08 and HAR-04. |
| PAR-X-13 | Output-verbosity shaping. | HCL-35 | Changes model behavior, not the evidence supplied. |
| PAR-X-14 | Native Cursor ConnectRPC/protobuf shim, CDP observer, VM MITM and Frida capture. | CB-28 | Private drifting protocol; TLS interception; credentials; needs a public URL. |
| PAR-X-15 | Multi-provider model router, autoroute, and upstream fallback. | CB-29 | Billing-route changes are out of scope; OpenCode header spoofing is a ToS risk. |
| PAR-X-16 | Prompt and final-answer response cache. | CB-14 (part) | A semantic-cache replay hazard. |
| PAR-X-17 | Keyword-derived permission and tool gating. | CB-19 (part) | Discovery is not authorization. |
| PAR-X-18 | Arbitrary code REPL or Lua composition on the server. | SAS-22 (part) | Security posture. Declarative alternative: PAR-RET-10. |
| PAR-X-19 | Outbound webhooks. | MEM-20 (part) | Local-first. |
| PAR-X-20 | Porting GitNexus code, queries, prompts, or skills; Serena GPL application code; context-mode code. | license | §2.4. |

---

## 5. Target surface map for the nine tools

This section lists every argument and field the parity work adds. Tools stay at nine. Descriptions stay terse, and detail is disclosed progressively through `initialize.instructions` and resources.

- **ground**`(workspace_id?, session_id?, since_revision?, query?)` adds:
  - freshness envelope, `index_coverage`, `baseline {head, indexed_at, auto}`, incremental counters, `drift_checked`;
  - `pending_for_you`, `at_risk_memories`, `memory_health`, `next_action` (advisory), `consolidation_jobs`;
  - `core_memory` (budgeted, revision-pinned) plus `deferred_index`, `memory_update` delta, `memory_budget`, `memory_pressure`, `onboarding`;
  - `recent_sessions`, `relevant_sessions`, `session_handoff`, `continuity`, `handoffs`;
  - `orientation` (first call), `recent_failed_runs` (core outcomes), `in_flight`, `interrupted`;
  - `principal {id, roles}`, `delivery_capabilities`.
- **recall**`(query?, paths?, entry_id?, history?, status?, include_pending?, include_superseded?, include_merged?, show_expired?, as_of?, scope|scopes?, include_shared?, kind?, tags?, topic?, path_prefix?, since?, until?, by?, seed?, session_id?, source=memories|sessions, names_only?, render?, max_chars?, explain?, limit?, cursor?, events_since?, types?)` returns:
  - per entry: `trust`, `verification_mode`, `verification_basis`, `provenance`, `valid_from`/`invalidated_at`, `superseded_by`, `anchors`, `score_details`, `stale_since`, `refs_stale`, `applicability`, `scope`;
  - plus `conflicts[kind]`, `already_shown`, `next_cursor`.
- **remember**`(content, title?, paths?, kind?, tags?, description?, tier?, visibility|scope?, dir?, op=propose|edit|retire|supersede?, supersedes?, entry_id?, base_revision?, reason?, old_string?, new_string?, expires?, valid_from?, evidence?, run_id?, session_id?, claim?, job_id?)` returns:
  - `status created|corroborated`, `entry_id`, `revision`, `content_digest`;
  - `duplicate_of`, `possible_duplicates`, `possible_conflicts`, `suggested_supersedes`, `anchors`;
  - `redacted`, `violations`.
- **verify**`(entry_id, revision?, outcome=approve|reject|duplicate_of|retract|helpful|misleading|stale_harm, target?, note?, evidence_handle?)` returns the state transition, a diff for edit revisions, and `reused_run`.
- **search**`(query, mode=hybrid|symbol|text|task|commits|diff|pattern|exact|regex, path?|path_glob?, lang?, kinds?, depth?, include_body?, substring?, max_matches?, group_by?, rev?, workspaces?|group?, fields?, limit?, cursor?, max_chars?)` returns:
  - hits `{path, lines, snippet, uid, lanes_matched, scores, reasons, why, related_memories}`;
  - `flows`, `coverage`, `freshness`, `semantic_model`, `vector_mode`, `cache`.
- **explain**`(path|symbol|uid, depth?, include_content?, chain_depth?, rev?, max_chars?)` returns:
  - `symbols_by_kind` / `degraded_stage`, symbol context (categorized refs, flows, entry point), `area`/`cohesion`;
  - `memories`, `memory_cluster`, `epistemic`, `freshness`.
- **impact**`(symbol?|uid?, op=blast|refs|definition|implementations|rename|delete, from?, to?, direction?, max_depth?, relation_types?, min_confidence?, include_tests?, scope=unstaged|staged|all|compare?, base_ref?|base?/head?, worktree?, new_name?, precise?, limit?, offset?, cursor?, summary_only?, max_chars?)` returns:
  - `by_depth`, `counts`, `risk`, `risk_note`, `affected_flows`, `affected_modules`, `contract_break`;
  - `hops`/`edges {type, confidence, provenance}`, `furthest`;
  - `epistemic`, `memories`, `freshness`, `partial`, `truncated`.
- **diagnostics**`(path?, symbol?, min_severity?, range?, with_references?, since=checkpoint|baseline?)` returns `by_path{severity{owner_name_path[]}}`, `new`, `resolved`, structural findings (`import_cycle`), and provenance.
- **why_failed**`(run_id?|command?|evidence_handle?|log?)` returns failure signals, implicated paths and memories, a bounded tail, and the outcome record id.

Other surfaces:
- **Resources**: `xmustard://evidence/{handle}?offset&length&pattern&lines`; `xmustard://workspace/{id}/areas|flows|memory-revision` (subscribable); `xmustard://workspaces`.
- **Transports**: Streamable HTTP `/mcp` (default) and a native stdio relay.
- **Non-MCP surfaces**, each justified in the matrix:
  - `/api/hooks/<client>/<event>` and `xmustard-hook`;
  - evidence capture API;
  - `xmustard-ops` (setup, uninstall, doctor, status, clean, index, memories export/import, skills export, shared, policy, import sessions, stats, guidance, capture-fixture, lsp);
  - `xmustard-eval`;
  - opt-in gateway catalog;
  - opt-in model gateway.

---

## 6. Processes catalogue

### 6.1 Indexing pipeline (target)

1. **Trigger.** A watcher batch (PAR-FRESH-03), adapter edit events (FRESH-07), a HEAD change, first registration, or an explicit prewarm.
2. **Identity.** Diff the stat cache (FRESH-01) and hash only changed files. Compute the source identity once for the batch.
3. **Admission.** Go acquires the single heavy slot (RT-04). If it is busy, the batch waits up to a bound, and queries meanwhile keep serving the previous snapshot with a `behind` freshness label.
4. **Worker** (`xmustard-core index update --paths ...`, RT-02). Set `incremental_in_progress=1` in index meta. For each file, streaming and 1–2 threads:
   - read;
   - pick the language provider (SYM-05);
   - parse;
   - extract facts: symbols with nested qualified paths and ranges, UIDs, references excluding strings and comments, imports, doc comments, function-aligned chunks;
   - drop the tree;
   - write rows in batched transactions.
   The per-file fact cache is keyed by (content hash, analyzer version, schema fingerprint), so unchanged files are skipped.
5. **Resolution** (IMP-01). Re-resolve the changed files plus dependents of changed exports (reverse import index), as in the GitNexus write-set algebra: changed ∪ one-hop boundary ∪ importer BFS. Emit typed edges with confidence and provenance, and drop counters for the epistemic envelope (IMP-05).
6. **Escalation gate.** If the write set exceeds 50% of files and at least 50 files, do a full rebuild. A leftover dirty flag, or an analyzer identity or schema fingerprint change, also forces a full rebuild.
7. **Graph-wide layers**, only when edges changed and bounded: areas (SYM-08), flows (SYM-09), import cycles (IMP-08). Recompute with a deterministic seed.
8. **Postings and vectors.** Update FTS5 chunk postings for the changed chunks (RET-01). Optionally embed the changed chunks statically into binary and int8 segments (RET-03/04).
9. **Finalize.** Write last_commit, coverage, and counters (reparsed, reresolved, escalated). Clear the dirty flag, release the heavy slot, and trigger FreeOSMemory.
10. **Swap.** The resident service rebuilds the CSR delta and name index, then swaps the snapshot atomically. Readers drain from the old snapshot. The progress bus publishes `index.refreshed`.

Peer references:
- GitNexus 19-phase DAG: scan → structure → markdown → parse (worker pool, ~20 MB byte-budget chunks, content-addressed parse cache) → routes/tools/orm → crossFile → scopeResolution → mro → di → communities → processes → CSV into LadybugDB → FTS → embeddings → meta and registry → AGENTS.md block.
- GitNexus incremental writeback: dirty flag, subgraph extract, 1-hop plus importer BFS, escalation gate.
- GitNexus measured cost: a one-file edit takes 31.7 s at 2.2k files, because the in-memory graph and all emit passes are rebuilt. xMustard must not repeat this.
- Serena: stat-poll → didChangeWatchedFiles, and md5-keyed document-symbol caches saved after every call.
- Augment connectors: hash → diff against stored state → re-embed only changed files.

### 6.2 Freshness and identity loop

1. The watcher (a directory-level FSEvents, kqueue, or inotify stream) feeds a debounced queue: 300 ms debounce, a max-wait bound, and serial refreshes that never overlap.
2. Queue overflow or a change to an ignore file becomes a `*` full refresh. A failure retries with exponential backoff.
3. The identity cache (HEAD, porcelain digest, stat marks) is updated per batch. `identity_age_ms` is exposed.
4. Each request samples identity once, as a cached read (FRESH-02), and passes it to the handler and to Rust.
5. git runs only on overflow or a HEAD/index change. It never runs in a form that re-hashes racy index entries per call; the measured `git status` spike was 139 MB.
6. Each hot read returns the freshness envelope (FRESH-05). The status is `current`, `behind(N)` via rev-list, `diverged`, or `unknown`, and fails open with a 5 s timeout.
7. Periodic full verification (for example hourly, or on doctor) catches same-size edits and preserved mtimes.

### 6.3 Query path (search, explain, impact, ground)

1. The MCP call arrives over HTTP (RT-03) or through the relay.
2. Toolcompat normalizes and validates arguments (ADP-03), and the workspace is auto-resolved (ADP-02).
3. The request is admitted through the byte pool and child slots.
4. The current snapshot id and identity are attached.
5. IPC goes to the resident service (RT-01). Lanes run over pread segments: exact symbol and name path, BM25, static-embedding binary prefilter then int8 rerank, and graph proximity. They are fused by RRF and reranked with the explainable features over the top K.
6. The result is assembled bounded (`max_chars`, `limit`, cursor, staged degradation), with the epistemic and freshness envelopes, and verified memories are attached by anchor (RCL-02).
7. If the result exceeds the client budget, it is projected with a handle (CTX-02/03).
8. Accounting counters are recorded (EVAL-04).
9. No per-call process spawns and no full-tree hashing happen on this path.

### 6.4 Admission and heavy-slot scheduling

1. Resident components reserve their steady allocation at startup.
2. Heavy work acquires the single heavy slot plus a byte reservation. Heavy work means: a reindex batch, a capture ≥4 MiB or one above the pool, a SQLite bulk import, the index writer, the helper model, or the ONNX sidecar. If the slot is busy, the request waits up to a bound, then returns 503 or `-32000` with Retry-After.
3. The RSS watchdog samples the own tree (ps-RSS plus phys_footprint or PSS) every 250 ms while heavy work is active, and on demand otherwise. Near the soft ceiling it refuses new heavy work.
4. When heavy work is released: `debug.FreeOSMemory` in Go and cache trimming in the service.
5. The transient byte pool drops to 16–24 MiB. Captures above the pool stream straight to the spool under the heavy slot.
6. The cancellation chain is kept: shim or HTTP → `exec.CommandContext` / IPC cancel → process-group kill.

### 6.5 Governed memory lifecycle v2

States: `proposed → promoted → (at_risk | stale) → reverified | superseded | retracted | expired | purged(tombstone)`. There is also a `private` lane that self-promotes and shows to others only as unverified, and open mode labels promotions `self_asserted_open_mode`.

**remember**
1. Authenticate the principal and derive scope and author server-side (SEC-05).
2. Validate policy (GOV-07) and redact (SEC-04).
3. Normalize, then extract anchors (RCL-02; symbols resolved through the service) and the optional claim (GOV-10).
4. Dedupe (GOV-03): exact hash, then MinHash/LSH within scope and anchors. A duplicate of a promoted memory returns `corroborated`; otherwise `possible_duplicates`.
5. Detect conflicts (GOV-11): T1 claim, T2 suspected contradiction, T3 path overlap.
6. Bind provenance (PROV-04): HEAD, branch, dirty, evidence handles, session, job id.
7. Append a `propose` event (PROV-01) and create revision 1. The state is `pending`, or `promoted` for the private lane or open mode.
8. Return candidates for the agent to act on (GOV-21).

**verify**
1. Enforce the verifier role (SEC-02) and principal distinctness (policy PROV-05).
2. Validate the evidence handle if one is given.
3. The outcome is one of approve, reject, duplicate_of, retract, helpful, misleading, or stale_harm. The latest verdict per principal replaces earlier ones.
4. Reconcile. On first promotion: snapshot path hashes and symbol-body hashes, set `valid_from=HEAD`, apply `supersedes` atomically (old entries become `superseded` with `invalidated_at` and `expired_at`), and append events.

**recall**
1. Filter by scope, kind, tags, status, `as_of`, and expiry.
2. Rank with FTS5 BM25, anchors and graph seed, trust, recency, and a stale penalty, then threshold (RCL-01/03).
3. Run a bounded drift check (window max(4×limit, 16)) using the cached identity and symbol hashes. Persist `stale_observed` events.
4. Apply supersession filtering, session-seen suppression, and the `max_chars` budget.
5. Render with `explain`, provenance, conflicts, and a cursor.
6. Content binds to its full SHA-256 digest, retries up to 3 times, then withholds.

Every transition is replayable from the append-only history. LLM-based consolidation is only ever proposed by the calling agent and never auto-promoted.

### 6.6 Change-driven invalidation loop

1. The watcher or a HEAD change produces changed paths and symbols.
2. Look them up in the anchor inverted index to find affected promoted memories.
3. Re-hash only the affected anchors (the symbol body where an anchor exists).
4. On mismatch, append `stale_observed(commit, anchors)` and set `needs_reverify`.
5. ground surfaces `at_risk_memories` ("memories affected by your changes").
6. An agent re-verifies (clearing drift against a new baseline) or proposes a supersession.
7. When drift persists beyond N commits or days, the consolidation queue proposes retirement, and commit search (RET-08) attaches the invalidating commits.

This mirrors Graphiti's ingest-time invalidation, with the repository as ground truth instead of an LLM contradiction check.

### 6.7 Consolidation (sleep-time analogue)

1. **Triggers:** step count or compaction events reported by hooks (Letta default: 25 steps, or on compaction), session end, a manual command, or a schedule.
2. **Watermark per source** (session or transcript), advanced only on success. The schema follows Letta's `v3_assistant_steps`: reflected-through id, total and reflected counts, last started and succeeded.
3. **Deterministic candidate generation** over the metadata cache and the SQLite index:
   - near-duplicate clusters (MinHash);
   - stale or orphaned anchors (deleted symbols);
   - supersession chains;
   - ageing unverified proposals;
   - session digests (repeated failure fixes, new commands);
   - init seeds from AGENTS.md/CLAUDE.md sections.
4. **Job lease:** compare-and-set with a conflict signature. A crashed `launching` claim is retried; a `done`-failed claim is reported rather than relaunched.
5. **ground exposes jobs with instructions.** The instructions embed Letta's filters: lasting versus ephemeral, already captured, generalizable, absolute dates, fix contradictions at the source, archive versus delete, tier check.
6. **The agent or opt-in helper works** and submits `remember(job_id=...)` proposals.
7. **Distinct-principal verify is mandatory.** This makes Letta's optional "explicit merge" the only mode, which is the differentiator.

### 6.8 Session ledger, compaction continuity, and handoff

1. Hooks record session events into the ledger (PROV-06): tool calls with args digest, handle, and status; files read and modified; prompts; decisions; errors; compactions. There is one writer (the daemon).
2. **Before compaction.** At PreCompact (Claude Code, Codex, Cursor), `session_before_compact` (Pi), or `experimental.session.compacting` (OpenCode), xMustard builds a ≤2 KB priority-tiered snapshot: active files, open failures with handles, decisions, pending proposals, next step.
3. The client compacts.
4. **After compaction.** The snapshot is re-injected through SessionStart `source=compact` additionalContext (Claude Code, Codex) or `system.transform` (OpenCode). For Pi, the snapshot is already inside the custom CompactionEntry `details`. Handles stay expandable.
5. **Handoff capsule.** At Stop or SessionEnd, or through `remember(kind=handoff)`, a capsule is stored with path hashes (PROV-08). Candidate memories are mined deterministically (GOV-14).
6. **Next session, any client.** SessionStart receives ground `session_handoff`: the prior snapshot, unexpired handles, proposals awaiting a distinct verifier, current drift, and recent and relevant sessions (RCL-08). Client transcripts are hashed upstream references (HAR-05), streamed with a byte cap.

### 6.9 Context reduction pipeline

1. **Capture** (CTX-01): stream the body into the spool under quota admission (Expect 100-continue), redacting as it goes. SHA-256 the original and attach metadata.
2. **Reduce** (CTX-02): pick the reducer by (client tool name, argv0), versioned. Keep failure-first salience with head and tail windows and explicit omissions.
3. **Shape** (CTX-03): apply the per-client budget and validate against the output schema. An invalid shape falls back to the original within budget, or to an explicit size error.
4. **Deliver:** the projection plus an `[xmustard evidence]` footer or `_meta` (handle, raw bytes, projected bytes, omissions, reducer, `expires_at`, retention class, `captured_identity`).
5. **Dedupe guard** (CTX-06): if the identity-bound key matches an earlier call, return "unchanged since call X; handle H". Side-effecting commands are exempt.
6. **Recover** (CTX-04): 64 KiB pages or pattern and line search in the original, with the freshness check from the cached identity. Returns explicit 410, 403, or 404 on expiry, revocation, or wrong principal.
7. **Mask** (CTX-07): older results become stubs in polling windows (Pi, OpenCode).
8. **Compact** (CTX-08): structured history compaction with handles, used in the gateway and context hooks.
9. **Retain** (CTX-09): retention classes and upstream references. A full quota means bounded passthrough, never eviction of a promised original.

Peer references:
- OpenHands: `maybe_truncate` (head and tail, `<response clipped>`, full content saved to a file); LLMSummarizingCondenser (`max_size=240`, `keep_first=2`, soft and hard triggers).
- SWE-agent: `LastNObservations` with polling.
- cline: duplicate-file-read notice.
- Pi: OutputAccumulator (rolling tail, temp-file spill, 2000 lines / 50 KB).
- Letta: clamp limits (bash 30k, failure head+tail 10k, grep 10k, read 2,000 lines × 2,000 characters, 32k backstop, overflow file with secret scrubbing).
- cursor-bridge: compaction (18,000-byte trigger, keep 3, summarize 8, todo deltas); lossy, which xMustard fixes.

### 6.10 Harness adapter lifecycles

| Client | Result replacement seam | Context injection | Compaction boundary | History transform | Notes |
|---|---|---|---|---|---|
| Claude Code | PostToolUse `updatedToolOutput`: all tools, shape-matched for built-ins, 10,000-character caps. PostToolUseFailure: additionalContext only. | SessionStart (command or `mcp_tool` only), UserPromptSubmit, PostToolBatch | PreCompact (can block), PostCompact (no decision), SessionStart `source=compact` | none | HTTP and `mcp_tool` hooks spawn no process. Parallel PostToolUse runs concurrently. A non-first-party `ANTHROPIC_BASE_URL` disables tool search. |
| Codex | PostToolUse `decision:block` plus reason (feedback substitution). `updatedMCPToolOutput` is unsupported. | SessionStart, UserPromptSubmit | PreCompact, PostCompact | none | PreToolUse `updatedInput` needs a version gate (context-mode says deny-only; its pretooluse detects ≥0.141.0). |
| OpenCode | `tool.execute.after` mutates the output, and the mutation is returned to the model. | `experimental.chat.system.transform` | `experimental.session.compacting`, `compaction.autocontinue` | `experimental.chat.messages.transform` | Has the most complete seams. `tool.definition` shortens descriptions. |
| Pi | `tool_result` (content, details, isError, usage), composable | `session_start`, `setActiveTools` | `session_before_compact` → custom CompactionEntry | `context_edit` entries at `turn_end` | Reference harness; the pinned 0.87.1 dist includes `turn_end` and `context_edit`. |
| Cursor | postToolUse `updated_mcp_tool_output` (MCP only). Built-in shell and read are observe-only. | sessionStart `additional_context` | preCompact | none | Shell reduction through preToolUse `updated_input` command wrapping. Built-in read/grep only through the model gateway. Cloud agents lack some events. |
| Letta Code | Through MCP only, via `letta mcp call` in Bash (30k bash clamp) | Claude-compatible hooks (additionalContext, 10k cap) | PreCompact | none | Tools are discoverable through `letta mcp search`, which ranks on descriptions. |

**Command-rewrite reduction.** PreToolUse (or Cursor `updated_input`, or Pi `tool_call` mutation) rewrites `cmd args` to `xmustard-core run -- cmd args`. The wrapper streams the child's output to the spool with a bounded tail, passes the exit code through, and prints the projection plus a handle. Used where result replacement is unavailable.

**Deferred tool discovery (gateway).**
1. The full catalog is held server-side; only the search tool plus 3–5 core tools are visible.
2. Search runs (regex, BM25, or token overlap).
3. A `tool_reference` or schema is expanded.
4. The call is routed by namespaced name with allowlist enforcement.
5. The result is projected.
This is cache-safe because deferred tools never enter the system-prompt prefix.

**Install and conformance.**
1. Detect the client and version.
2. Write the hooks and MCP registration with a backup.
3. Preflight: health, evidence round-trip, `count_tokens` where relevant.
4. Run the pinned conformance suite (EVAL-07).
5. Roll back automatically on failure.
6. Record the version pin.

### 6.11 Optional sidecar lifecycle (ONNX embedder, helper model, LSP servers, SCIP indexers)

1. Off by default.
2. First use requests the heavy slot, or an external budget line with explicit user opt-in.
3. Spawn with a pinned artifact and version, telemetry disabled (`NEEDLE_TELEMETRY=0`, `DO_NOT_TRACK=1`), and bounded input and output.
4. Serialize requests. The Needle C header is process-global and not thread-safe.
5. Validate outputs against candidates and the snapshot. On uncertainty, fall back to the deterministic path.
6. Reap when idle. Restart on RSS runaway.
7. RSS is always reported on a separate external or optional line.

Peer reference: the GitNexus embedding sidecar (fork, IPC, batch 16 with sub-batches of 8, reap).

### 6.12 Evaluation processes

1. **Outcome benchmark** (EVAL-01).
   1. For each task (id, class, repo, ref, prompt, visible verify step, hidden oracle) and each arm and run: create a fresh detached worktree at ref.
   2. Launch the headless client (claude -p stream-json, codex exec --json, pi --mode rpc) inside containment.
   3. Record final-event tokens and cost, wall time, diff churn, and the xMustard tree RSS from the v2 sampler.
   4. A run counts as resolved only if the hidden oracle passes.
   5. Report medians per task and arm with McNemar and bootstrap statistics, and name skipped tasks.
2. **Memory lifecycle suite** (EVAL-02). Scripted multi-agent scenarios (propose, conflicting propose, supersede, drift, expire, scope leak) with the metrics listed in §4.15.
3. **Reduction quality** (EVAL-03). The same tasks under raw, projection, masking, and compaction, measuring evidence recall, false omissions, expansion rate, solve rate, and tokens, including rare-failure fixtures.
4. **Budget gate** (EVAL-05).
   1. Register process roots and roles.
   2. Sample ps-RSS plus footprint/PSS every 100 ms.
   3. Attribute peaks to components.
   4. The gate fails above 95.4 MiB or if the workload is incomplete.
   5. Report data-movement counters, with externals on separate lines.
5. **Footprint comparison** (EVAL-06). Bare agent, agent plus xMustard (Pi, Claude Code, Codex, OpenCode), and OpenHands (process sandbox and Docker) on the same tasks and model.
6. **Conformance** (EVAL-07). Per client version, as described in §6.10.
7. **Component benches** (EVAL-09): retrieval gate localization; impact precision against LSP or SCIP truth; freshness latency after edit, rename, and delete; incremental counters.

### 6.13 Peer reference processes (for implementers)

- **Serena symbolic read:**
  1. `apply_ex` (task executor, 240 s tool timeout).
  2. Active-tool and active-project checks.
  3. Stat-poll → `didChangeWatchedFiles`.
  4. Raw and high-level document-symbol caches keyed by md5.
  5. `NamePathMatcher`.
  6. Renderer with staged shortened factories.
  7. Restart and retry once on language-server termination.
  8. Token accounting.
  9. `save_all_caches`.
- **Serena language-server lifecycle:** parallel start, fail-fast, route each file to the first server that does not ignore it, `_ensure_functional_ls`, restart on crash, stop with a 2 s grace. No idle eviction; xMustard adds eviction.
- **Mem0 OSS v3 add:**
  1. Load the last 10 messages.
  2. Embed and fetch the top 10 existing memories in scope, with UUIDs mapped to integers.
  3. One LLM call (ADD-only, `linked_memory_ids`).
  4. Batch-embed.
  5. MD5 dedupe.
  6. Lemmatize for BM25.
  7. Insert vectors and an ADD history row.
  8. spaCy entity linking.
  9. Save messages.
- **Mem0 search:**
  1. Validate that scope ids are present.
  2. Semantic over-fetch max(4k, 60).
  3. Sigmoid-normalized BM25 (query-length-adaptive midpoint and steepness).
  4. Entity boost damped by fan-out.
  5. Threshold 0.1.
  6. Additive fusion divided by max possible.
  7. `score_details`.
- **Mem0 coding plugin:**
  1. Hooks capture without a model.
  2. Checkpoint every 5 exchanges, 10 messages, or 40k characters, with a 5-minute idle flush.
  3. A detached worker builds a redacted coding-session episode (files, commands with status, provenance).
  4. Chunk under 24k tokens → add, scoped to repo, dir, and mine.
  5. First-prompt recall (≥20 characters, top 5) suppresses memories already shown and fits a 4,000-character budget.
- **Graphiti add_episode:**
  1. EpisodicNode.
  2. extract_nodes.
  3. Resolve nodes: exact, then entropy gate, then MinHash/LSH ≥0.9, then LLM.
  4. extract_edges.
  5. Resolve edges: exact fast path, then LLM for duplicate or contradicted facts, then a small LLM for timestamps, then the deterministic interval rule (older contradicted edges get `invalid_at=new.valid_at`, `expired_at=now`; a new edge expires if a newer contradicting fact exists).
  6. Save the episode, MENTIONS, and saga links.
  MCP queues episodes sequentially per `group_id`.
- **Letta MemFS write:**
  1. `memory(command, reason)`.
  2. Clean-repo assert.
  3. Confined op.
  4. Stage explicit paths.
  5. Pre-commit validators (frontmatter, protected `read_only`, size, depth, core budget, protected config).
  6. Commit as the agent.
  7. `memory_updated`.
  8. Post-turn sync state machine (clean, pushed, dirty, conflict, push_failed, skipped) with CAS repair workers.
  9. The prompt is recompiled from HEAD, and a `<memory_update>` delta is sent mid-conversation.
- **Letta reflection:**
  1. Trigger at 25 steps or on compaction.
  2. Reserve the launch.
  3. Payload since the watermark (≤16k startup tokens, 40k-character snapshot).
  4. Worktree branch.
  5. Five phases: investigate, extract, update, review, commit.
  6. Auto or explicit merge.
  7. Advance the watermark only on success.
- **OpenHands condensation:** append-only events with Condensation tombstones, then a View. Soft triggers at 240 events or on token count; hard triggers on context errors (5 retries, scale 0.8). An extra LLM call summarizes the first half.
- **Pi compaction:**
  1. Trigger when `contextTokens > window − 16384`.
  2. Walk back to keep 20,000 recent tokens, cutting only at user, assistant, bash, or custom messages.
  3. Serialize with tool results capped at 2,000 characters.
  4. Summarize into Goal, Constraints, Progress, Decisions, Next, and Critical Context, plus read and modified file lists.
  5. Append a CompactionEntry. Extensions can replace it.
- **cursor-bridge BYOK loop:**
  1. Filter declared tools by intent.
  2. Normalize the workspace.
  3. Build the JSON-only contract prompt.
  4. Up to 6 steps: planner snapshot of ≤6,000 bytes, then normalize, validate, loop-guard, and redirect, then execute (parallel only for read-only calls), then a per-kind projection, then compact at 18,000 bytes.
  5. Sanitize the final answer and reject no-op answers.

---

## 7. Memory budget

### 7.1 Measured baseline (HEAD, fixed 501-file workload)

- Gate peak: 72.3–84.9 MB against a 95.4 MiB limit.
- At the 80.6 MB peak: API 22.6 MiB, two per-call cores 22.0 and 12.8 MiB, two shims 10.1 and 9.4 MiB.
- Probes: API 14–25.5 MiB, shim 7.9–12.6 MiB, tree 38.6–59.5 MiB across the nine tools on self and the gitnexus clone.
- Go allocated 1.08 GB in total over one workload.
- The design stops scaling at parity size: the 800-file cap, ~1.35 KB per symbol in the heap, and per-call full-tree hashing (ground 14.8 s on 270 MB).

### 7.2 Resident design allocation (default profile)

Envelope: one hot repo with ≤100k symbols and ≤10k source files; 2–4 agents over HTTP MCP. MiB on the ps-RSS basis.

| Component | Steady | Peak | Basis |
|---|---|---|---|
| Go daemon (HTTP and MCP, governance, evidence, hooks, ledger, runner, adapters), GOMEMLIMIT ~40 | 22 | 28 | Today's API is 22–30 with per-call snapshot parsing; the platform half is unlinked (−3–8); a minimal Go relay *measured* 13–17. See 7.3. |
| SQLite governance store (modernc; cache 2–4 MiB; `mmap_size=0`) | 3–5 | 12 (bulk import, in heavy slot) | *Measured*: +2.3 at open; 100k rows plus FTS5 → 23.4 maxrss for the whole process. |
| Rust index service base | 3 | 3 | *Measured* Rust baseline 1.9–2.7. |
| Symbol table, CSR graph, and name postings | 12 | 15 | Target ≤150 B/symbol × 100k. Mostly file-backed and reclaimable; dirty memory limited to a 2–4 MiB block cache. |
| Code BM25 index cache (pread) | 5 | 8 | *Measured* tantivy reader footprint 2.5 MB. The pread design avoids 23 MiB of mmap RSS. |
| Static-embedding lane (lean loader, int8, custom tokenizer) | 6 | 10 | Lean potion-base-8M *measured* at 16 MB footprint, of which 13 MB was the HF tokenizer; the custom tokenizer target is ~2. Off unless the retrieval gate passes. |
| Binary vectors and int8 rerank scratch | 3 | 4 | 100k × 256 bits = 3.1 MiB. |
| Watcher and stat cache | 3 | 4 | ~100 B per file; 10k files ≈ 1 MiB. |
| MCP access | 0 over HTTP | ≤3 per stdio relay | Replaces 8–12 per Go shim. |
| **Steady subtotal** | **57–63** | | |
| Heavy slot, exclusive: parse worker, large capture or projection, SQLite bulk import, index writer, or helper | 0 | ≤25 | Worker with 17 grammars *measured* at 22 RSS / 5.7 footprint; tantivy writer ≥15/thread; SQLite bulk +12–14. |
| Rare git child (overflow or HEAD change) | 0 | ≤5 | *Measured* 7.6 MB after the index refresh. |
| Margin | ≥5 | ≥2–8 | |
| **Peak** | | **~87–93** | Against the 95.4 MiB gate. |

### 7.3 Sub-allocation inside the Go daemon (targets, unmeasured)

| Item | MiB |
|---|---|
| Go runtime, binary text, HTTP and MCP server base | 12–14 |
| MCP HTTP sessions (4 agents) | ≤1 |
| Governance caches (meta, trust tiers, LSH bands, anchor hot set, session-seen sets with TTL) | ≤3 |
| Evidence index, handles, repeat-guard LRU (64 sessions × 256 fingerprints), result cache (byte-capped) | ≤2.5 |
| Hook handlers, event hub ring (128), progress | ≤1 |
| Run lineage runner (idle ≤1; up to ~6 while a run is active, inside peak) | ≤1 |
| Redaction RE2 set, toolcompat tables, token cache | ≤1 |
| **Total steady** | **~20–23**; peak 28 covers request churn and small captures (<4 MiB) |

### 7.4 Scenarios

| Scenario | Estimated peak | Fits? |
|---|---|---|
| 1 hot repo, 2 agents over HTTP, idle | 57–63 | yes |
| Plus a reindex batch in the heavy slot | 82–88 | yes |
| Plus a 16 MiB capture during reindex | Capture waits for the slot or is refused (503), so no overlap | yes, by construction |
| 4 agents over HTTP | +≤1 | yes |
| 4 stdio-only agents with a native relay (≤3 each) | steady 69–75, peak ~99–105 | **no**. Needs HTTP, or relays ≤2 MiB with embeddings off. |
| 4 agents with today's Go shims | +32–48 | **no** |
| Second hot repo | +20–25 | **no** in the default profile. LRU keeps K=1. K=2 only with the embedding lane off and the envelope halved. |
| Embedding lane off | −9 | yes |
| LSP server, helper LLM, or ONNX embedder running | ext +92 to GBs | Outside the gate by definition, reported separately |
| Repo beyond the envelope (for example 50k files) | Stat cache +5 heap if not disk-backed; graph grows linearly | Degrades per RT-09. Never grows RSS silently. |

### 7.5 Conditions for the claim (all must hold)

1. One Go daemon serves MCP over HTTP, with no per-agent Go shims (RT-03).
2. No per-call Rust or git spawns on hot paths: the resident index service plus cached identity and a watcher (RT-01, RT-12, FRESH-01/02/03).
3. Indexes are compact and disk-backed with pread and bounded caches. Nothing proportional to repo size lives in a heap (IMP-02, RET-01, RET-04, STORE-01, STORE-03).
4. A single heavy slot serializes the parse worker, big captures, bulk imports, the index writer, and the helper (RT-04).
5. The transient pool is 16–24 MiB, GOMEMLIMIT is set, and allocator hygiene is applied (RT-04, RT-05).
6. The platform half is not linked by default (RT-07).
7. The §8 items are excluded from the default profile and reported separately.
8. The scale envelope is declared and enforced, with one hot repo by default (RT-09, RT-10).
9. The parity-scale gate (EVAL-05) passes with ps-RSS as the gate and footprint/PSS reported alongside. **Until it does, "parity fits the budget" stays unproven.** The owner's belief is plausible for the deterministic parity set. It does not hold for LSP, neural embeddings, a local LLM helper, an embedded graph DB, gateway-spawned stdio MCP servers, or more than one hot repo.

### 7.6 Accounting rules

- ps-RSS stays the gate for continuity with the frozen v1 benchmark. File-backed pages count toward it, which is why pread is preferred over mmap.
- Footprint/PSS and anon versus file-backed are reported alongside, so mmap designs are neither wrongly rejected nor wrongly accepted.
- External processes go on named lines: agents, LSP servers, helper models, the ONNX sidecar, SCIP indexers, compilers and tests run by why_failed or the runner, and downstream MCP servers the client launched.
- Moving work into an external process is never counted as a resource improvement (CONTEXT_LAYER).
- Linux glibc arenas and Go GC headroom can add RSS that macOS runs do not show, so the gate must also run on Linux.

---

## 8. What cannot fit in the default budget, and its disposition

| Item | Measured / estimated | Disposition |
|---|---|---|
| Neural ONNX embeddings (MiniLM, bge-small, arctic-embed-xs) | *measured* 92–700 MiB (fastembed; unbatched quantized peaks 637–687) | Opt-in sidecar at index time in the heavy slot or with a raised budget (RET-05). Default: static embeddings or none. |
| HNSW over 100k × 384 | *measured* i8 61, f16 98, f32 171 MiB resident; build 65–273 | Only below ~20–50k vectors. Otherwise a binary flat scan plus int8 rerank (RET-04). |
| model2vec-rs as shipped; HF tokenizers crate | *measured* 78–90 MiB (potion-base-8M); tokenizers 13–27 MiB | Custom lean loader and tokenizer (RET-03). |
| Live LSP servers | *measured* 534 MiB tree for one TS hover; 669 MB typescript-language-server on a 12k-line frontend; gopls, rust-analyzer, tsserver at GB scale in public reports | Opt-in, one live server, idle-evicted, memory-capped, external line (SYM-06). |
| SCIP indexer runs | Hundreds of MB transient (scip-typescript embeds tsserver) | Scheduled external job; only the ingested table is resident (SYM-07). |
| Local helper LLMs | FunctionGemma 551 MB peak; LFM2.5-230M 293–375 MB; SmolLM2-135M 101 MB file; Needle3 35.3 MB artifact with RSS unmeasured (est. 45–90) | Off by default. Needle3 only as an on-demand serialized worker after measurement, never alongside the parse worker (CTX-12). |
| Model-based fact extraction, NER (GLiNER2 205–340M params) | 0.5–1.5 GB | Optional helper, pending candidates only (GOV-19). |
| Embedded graph DB (LadybugDB/Kuzu), Neo4j, FalkorDB, Qdrant, pgvector | ≥256 MiB pool floor to GBs | Skip (PAR-X-01). SQLite plus segments. |
| Gateway-spawned stdio MCP servers | 40–100 MiB each (Node/Python) | Prefer HTTP downstreams. Otherwise external lines, max live servers, idle eviction (HAR-08). |
| Model/API gateway buffering | cursor-bridge buffers 64 MiB per request | Streaming only, with per-request byte caps (HAR-09, could). |
| Exact BPE tokenizer (o200k) | est. 10–20 MiB | Evaluation only, lazy (CTX-13). |
| Second and further hot repos | +20–25 MiB each | LRU, cold pread mode (RT-10). |
| Zoekt-style mmap trigram index | ~1.2× corpus in RAM (≈325 MB for the gitnexus clone) | pread or bounded scan (RET-09). |
| Per-agent Go MCP shims | 8–12 MiB each (*measured*) | Removed (RT-03). |
| Execution runtimes inside xMustard (ctx_execute, containers, OpenHands) | 30 MiB to GBs | Skip (PAR-X-10, X-11). |
| PDG/taint and cross-repo group bridges | 10–40 MiB index-time transient plus large on-disk graphs | Deferred could (IMP-11, IMP-12). |

---

## 9. Open decisions

| ID | Decision | Options | Recommendation |
|---|---|---|---|
| D-01 | Where the resident index lives | (a) Rust service over stdio/Unix-socket JSON-RPC; (b) tree-sitter in Go via cgo | (a). It keeps the Rust core boundary and a kill boundary, and ROADMAP says to decide after measurement. Measure (a) first behind `XMUSTARD_CORE_WORKER`. |
| D-02 | Code index storage format | (a) rusqlite (bundled) `index.db` for facts and chunk FTS5, with an in-memory CSR built on load; (b) custom segment files plus tantivy with a pread Directory | (a) for simple incremental per-file updates. Revisit if FTS5 ranking or throughput fails the retrieval gate. rusqlite 0.32.1 and tantivy 0.24.2 are both cached locally. **Closed 2026-09-25 as (a) by measurement** (`docs/benchmarks/2026-09-25-d02-fts5-vs-tantivy.md`): FTS5 writer 11–13 MiB versus tantivy 40–44 MiB, equal MRR@10; FTS5 OR queries are slower (p95 28–40 ms). |
| D-03 | Go SQLite driver | modernc (pure Go, v1.59.0 cached) vs mattn (cgo, 1.8× faster inserts) | modernc, to keep the static no-cgo build. |
| D-04 | Budget metric | Keep ps-RSS gate plus report footprint/PSS, vs switch the gate to footprint | Keep ps-RSS as the gate (continuity) and report both. Design with pread and small caches so both pass. |
| D-05 | Static embeddings default | on vs off | Off until the retrieval gate shows a concept-query gain without regressions. Budget reserved. |
| D-06 | First-run trust | Open-mode self-assert (w0-kernel) vs private lane vs both | Both. Open mode is labeled `self_asserted_open_mode`. The private lane is the documented single-agent path. Peer-verified is the default when tokens exist. |
| D-07 | Expansion in clients without `resources/read` (Cursor, some Codex paths) | A tenth `expand` tool vs a client-specific fallback | A client-specific fallback (hook-injected pages, or a gateway-catalog `expand`) first. A tenth core tool needs explicit owner approval, because CONTEXT_LAYER forbids overloading recall. |
| D-08 | Hook transport and client language | http/`mcp_tool` hooks (no process) vs command client; Go (~5–10 MiB) vs Rust (~2 MiB) | Prefer http/`mcp_tool`. The command fallback client is Go first for speed of delivery; switch to Rust if EVAL-05 shows hook bursts matter. |
| D-09 | Model/API gateway | Build (could) vs never | Defer. Hooks cover Claude Code built-ins. Revisit only for Cursor built-in read/grep reduction. |
| D-10 | git as memory store | Primary vs mirror | Mirror only (GOV-18). SQLite is primary. |
| D-11 | Precision lane order | LSP pool first vs SCIP first | LSP pool first (the code already exists in Go), opt-in. SCIP later. |
| D-12 | Default hot repos | K=1 vs K=2 | K=1. K=2 only with optional lanes off and a halved envelope. |
| D-13 | Scale envelope numbers | Proposed ≤10k files / ≤100k symbols / ≤300 MB tracked / ≤4 agents / ≤2 hot repos | Confirm after EVAL-05 at parity scale. |
| D-14 | `goalruntime.rs` | Keep vs retire | Keep. It owns the /goal contract and has a Go caller. Any consolidation with the Go goal runtime is a separate decision. |
| D-15 | Harness runner scope | Bounded verification-run lineage vs agent loop | Verification-run lineage only (HAR-10). |
| D-16 | Principal distinctness policy strength | token-distinct vs owner-distinct | Owner-distinct available as a policy. Default token-distinct, with owner and kind recorded. |
| D-17 | MCP protocol target | 2025-06-18 or newer, with negotiation fallback to 2024-11-05 | Negotiate. Keep text content alongside `structuredContent`. |
| D-18 | Landing order for in-flight branches | w0-cleanup → w0-kernel → w0-feedback → w0-drift, then wave 1 | Agree. The human merges. The diagnostics session lands on its own schedule. |
| D-19 | Hosted scorers (Jev) and remote models | Allowed with explicit permission vs never | Only with explicit per-workspace data permission. Never counted as a budget win. |
| D-20 | Letta-style archive tier vs retire only | Both | Both. Archive keeps content findable at low rank; retire removes it from active recall. |
| D-21 | Hook injection policy (memories shown before native Read/Edit) | Inject verified plus stale-flagged vs verified only | Verified plus stale-flagged with explicit labels, never pending. Serena deliberately never injects; xMustard differs on purpose. |
| D-22 | Language pack binary size (+15–25 MB binary) | Always vs feature-gated packs | Feature-gated packs in the build. The release ships the full pack in the worker binary only. |

---

## 10. Build plan

### 10.1 Waves

- **Wave 1: foundations.** Twelve workstreams (WS-00 to WS-11), startable now and mostly in new modules.
  - WS-00 prepares the in-flight `parity/w0-*` branches for human merge.
  - Fully parallel: WS-01 (govstore), WS-02 (resident worker), WS-05 (toolcompat and redact), WS-06 (governor), WS-07 (index DB and fact extraction), WS-08 (evidence reducers), WS-10 (gate v2 and CI), WS-11 (eval executor).
  - Need WS-00 landed first because they touch hot files: WS-03 (request context), WS-04 (MCP package), WS-09 (security kernel).
- **Wave 2: first capability layer.** WS-12 to WS-26:
  - store cutover;
  - MCP over HTTP;
  - resident query side;
  - watcher and incremental refresh;
  - language packs;
  - Go and TS/JS resolver;
  - BM25 lane;
  - memory lifecycle writes;
  - recall v2;
  - why_failed without platform runs;
  - automatic baseline;
  - Claude Code hooks;
  - Pi extension;
  - dead-code removal;
  - release hygiene.
- **Wave 3: parity depth.** WS-27 to WS-53:
  - dedupe, anchors, claims;
  - invalidation and `as_of`;
  - scopes and grants;
  - cross-workspace query;
  - tiers and injection;
  - consolidation;
  - session ledger and handoff;
  - transcripts;
  - impact v2 and navigation surfaces;
  - embeddings;
  - areas and flows;
  - more resolvers;
  - other client adapters;
  - wrapper and reduction extras;
  - gateway;
  - install and conformance;
  - LSP and SCIP;
  - diagnostics deepening (after the diagnostics session hands off);
  - kernel extraction;
  - parity evaluation and final gate;
  - helper-model contract;
  - revision-aware reads;
  - run-lineage runner.

### 10.2 Collision map and conventions

In-flight work that must not be touched:
- the diagnostics session: `api-go/internal/workspaceops/diagnostics*.go`, `api-go/cmd/xmustard-api/profile_hook*.go`, `api-go/internal/workspaceops/zz_profile_gate_test.go`, `api-go/cmd/xmustard-api/diagnostics_*_test.go`, and its README.md / docs/STATUS.md edits;
- the `parity/w0-*` branches, until WS-00 lands them.

| Hot file | Workstreams touching it | Mitigation |
|---|---|---|
| `api-go/cmd/xmustard-api/main.go` (4,333 lines) | WS-00 (kernel), WS-06, WS-09, WS-12, WS-13, WS-21, WS-22, WS-23, WS-29, WS-48, WS-49. The diagnostics session may add route wiring. | New routes go in `api-go/cmd/xmustard-api/<area>_routes.go` with a `register<Area>Routes(mux, deps)` function. Each workstream's `main.go` diff is 1–3 lines. WS-48 (config struct) runs in a quiet window. |
| `api-go/internal/workspaceops/context_governance.go` (1,144 lines) | WS-00 (kernel, feedback), WS-12 | WS-12 splits it into `memory_propose.go`, `memory_verify.go`, `memory_edit.go`, `memory_recall.go`, `memory_store.go`. Later memory workstreams own disjoint files. |
| `api-go/internal/workspaceops/grounding.go` (188 lines) | WS-00, WS-03, WS-20, WS-21, WS-22, WS-28, WS-31, WS-33 | WS-03 splits it into `grounding_index.go`, `grounding_memory.go`, `grounding_runs.go`, `grounding_session.go`. |
| `api-go/cmd/xmustard-mcp/main.go` and the tool table | WS-00 (cleanup), WS-04, WS-13, and every tool-surface workstream | WS-04 moves the table into `api-go/internal/mcpserver/tool_<name>.go`, one file per tool. |
| `rust-core/src/bin/xmustard-core.rs`, `rust-core/src/lib.rs` | WS-00 (cleanup, drift), WS-02, WS-07, WS-25, WS-41 | WS-02 introduces a subcommand dispatch table. Each workstream adds one entry. |
| `rust-core/Cargo.toml` | WS-07, WS-15, WS-16, WS-37 | Dependency additions go in a separate first commit. |
| `rust-core/src/search.rs`, `symbolgraph.rs`, `changetrack.rs`, `indexcache.rs` | WS-00 (drift), WS-14, WS-15, WS-18, WS-22, WS-37 | Sequence WS-22 before WS-15 on `changetrack.rs`. WS-18 and WS-14 coordinate on `search.rs`, where WS-14 only swaps the graph source. |
| `api-go/internal/evidence/store.go`, `api-go/cmd/xmustard-api/evidence_routes.go` | WS-03, WS-08, WS-41 | WS-08 adds new files and touches only the `reduce.go` registry hook. WS-03 owns the `store.go` page-identity change. |
| `integrations/pi/src/tools.ts` | WS-00 (cleanup), WS-04, WS-24 | WS-04 generates the table, and WS-24 consumes it. |
| `Makefile`, `README.md`, `docs/STATUS.md` | WS-10, WS-13, WS-26; the diagnostics session | Status docs change only after measurement, in a coordinated docs pass. |

Process rules for every workstream:
- Branch per workstream.
- Every PR ends with `make check-backend` (plus TypeScript tests for integrations).
- Resident-affecting workstreams attach a gate v2 (WS-10) measurement.
- The human performs the merge. Agent votes and green tests do not authorize merges.
- Licensing and provenance per §2.4.

---

## 11. Traceability: reader ID → PAR ID

- **GNX**: 01→IMP-01 · 02→SYM-01, SYM-02 · 03→SYM-04 · 04→IMP-04 · 05→IMP-05 · 06→IMP-06 · 07→FRESH-05, FRESH-01 · 08→FRESH-04, STORE-03 · 09→FRESH-03, RT-01 · 10→RET-01, RET-02 · 11→RET-03, RET-05 · 12→SYM-08 · 13→SYM-09 · 14→RET-06 · 15→IMP-07 · 16→IMP-08, HAR-07 · 17→CTX-05 · 18→HAR-11, HAR-03 · 19→HAR-01, HAR-02 · 20→ADP-13 · 21→SHARE-03, SHARE-04, RT-10, ADP-02 · 22→IMP-11 · 23→IMP-10 · 24→IMP-09 · 25→X-01, SYM-11 · 26→IMP-12 · 27→X-02 (wiki.rs kept) · 28→SEC-03, ADP-05, RT-03 · 29→ADP-01 · 30→EVAL-01, EVAL-09 · 31→FRESH-08, OPS-02 · 32→SYM-05.
- **SAS**: 01→SYM-02, SYM-01 · 02→SYM-03 · 03→IMP-03, IMP-01 · 04→SYM-07 · 05→SYM-06 · 06→SYM-05 · 07→FRESH-01, FRESH-04, FRESH-05 · 08→RT-01 · 09→RET-01, RET-02 · 10→RET-03 · 11→RET-07 · 12→FRESH-10, IMP-06 · 13→RET-08 · 14→SHARE-03, SHARE-02, RT-10 · 15→ADP-02 · 16→GOV-04, GOV-05, GOV-06 · 17→RCL-05 · 18→GOV-18, SHARE-01 · 19→SEC-02, GOV-07, RCL-09 · 20→GOV-15 · 21→CTX-05 · 22→RET-10, X-18 · 23→ADP-01, HAR-03 · 24→ADP-05 · 25→HAR-01, HAR-02 · 26→EVAL-04 · 27→RT-03 · 28→SEC-02, SEC-03, SEC-06 · 29→HAR-07 · 30→X-03 · 31→IMP-09 · 32→X-04 · 33→OPS-02, FRESH-08, RT-02 · 34→X-05, PROV-04 · 35→FRESH-09 · 36→X-06 · 37→EVAL-01.
- **MEM**: 01→GOV-03 · 02→GOV-04 · 03→PROV-01 · 04→PROV-02 · 05→PROV-03 · 06→SHARE-01 · 07→GOV-02 · 08→GOV-09 · 09→RCL-01 · 10→RCL-02, SYM-04 · 11→GOV-11 · 12→GOV-10 · 13→PROV-04 · 14→PROV-06 · 15→GOV-14, HAR-01 · 16→RCL-04 · 17→GOV-06 · 18→SEC-04 · 19→SEC-01, SEC-05 · 20→SHARE-06 · 21→GOV-16 · 22→GOV-13 · 23→RCL-03 · 24→STORE-01 · 25→ADP-04, ADP-01 · 26→GOV-12 · 27→EVAL-02 · 28→GOV-19 · 29→GOV-20, SYM-08 · 30→GOV-18 · 31→X-07 · 32→X-08.
- **LET**: 01→PROV-01, STORE-01 · 02→GOV-04 · 03→GOV-07, SEC-02 · 04→GOV-08 · 05→HAR-03 · 06→SHARE-02 · 07→SHARE-01 · 08→STORE-01, GOV-05, STORE-02 · 09→SHARE-08, GOV-02 · 10→GOV-13 · 11→RCL-07 · 12→RCL-08 · 13→ADP-12 · 14→PROV-08, HAR-04 · 15→GOV-17, GOV-09 · 16→HAR-08 · 17→CTX-02, CTX-03 · 18→HAR-01 · 19→ADP-11, CTX-03 · 20→SEC-04 · 21→CTX-13 · 22→GOV-05 · 23→RCL-06, GOV-08 · 24→GOV-18 · 25→GOV-13, GOV-15 · 26→EVAL-02 · 27→SHARE-06 · 28→X-09.
- **HCL**: 01→CTX-01 · 02→CTX-02 · 03→CTX-03 · 04→CTX-04 · 05→CTX-07 · 06→CTX-06 · 07→CTX-09 · 08→ADP-01, HAR-03 · 09→HAR-08 · 10→HAR-08 · 11→ADP-05 · 12→PROV-06, STORE-01 · 13→HAR-04 · 14→HAR-05, PROV-08 · 15→GOV-14, GOV-13 · 16→ADP-06 · 17→ADP-07 · 18→ADP-08 · 19→ADP-09 · 20→ADP-10 · 21→CTX-10 · 22→HAR-09 · 23→RT-03, STORE-01 · 24→RT-12, FRESH-02 · 25→FRESH-07 · 26→HAR-06 · 27→CTX-11 · 28→CTX-12, RET-03 · 29→EVAL-06, EVAL-05 · 30→EVAL-03 · 31→ADP-13, EVAL-07 · 32→X-10 · 33→X-11 · 34→X-12 · 35→X-13.
- **CB**: 01→ADP-03 · 02→CTX-06 · 03→CTX-08 · 04→CTX-02 · 05→HAR-09 · 06→PROV-07 · 07→RT-01 · 08→HAR-08 · 09→SEC-04, SEC-03 · 10→SHARE-07 · 11→PROV-06 · 12→SHARE-09 · 13→SHARE-08 · 14→CTX-14, X-16 · 15→FRESH-01 · 16→SYM-10 · 17→RET-02 · 18→ADP-01, RT-04 · 19→HAR-08, X-17 · 20→CTX-11 · 21→HAR-12, EVAL-07 · 22→ADP-14 · 23→SHARE-05 · 24→SEC-05 · 25→SEC-03 · 26→ADP-13 · 27→CTX-13 · 28→X-14 · 29→X-15 · 30→X-03.
- **XM**: 01→STORE-01, STORE-02 · 02→RT-01 · 03→FRESH-01 · 04→FRESH-02 · 05→RT-06 · 06→GOV-01 · 07→SEC-01 · 08→GOV-02 · 09→RET-11 · 10→RT-08 · 11→ADP-01 · 12→RT-07 · 13→HAR-06 · 14→HAR-07 · 15→SYM-05, RT-09 · 16→IMP-01 · 17→RET-01, RET-02, RET-03 · 18→SYM-06 · 19→ADP-01, ADP-02, ADP-04 · 20→RT-03 · 21→RT-11 · 22→FRESH-06 · 23→GOV-04, PROV-01, PROV-02, RCL-01 · 24→PROV-05 · 25→SHARE-02 · 26→SEC-07 · 27→EVAL-01, EVAL-04, EVAL-08 · 28→OPS-03 · 29→CTX-01, ADP-09.
- **MB**: 01→§7, OPS-01 · 02→RT-01, RT-02 · 03→RT-03 · 04→RT-04 · 05→FRESH-01, FRESH-03 · 06→IMP-02, STORE-03 · 07→SYM-05, RT-02 · 08→RET-01 · 09→RET-03 · 10→RET-04 · 11→RET-05 · 12→STORE-01 · 13→PROV-02 · 14→GOV-21, GOV-13 · 15→CTX-11, CTX-12 · 16→SYM-06 · 17→RT-10, SHARE-03 · 18→RET-09 · 19→CTX-04, RT-04, CTX-09 · 20→HAR-01 · 21→HAR-10 · 22→RT-05 · 23→RT-09 · 24→EVAL-05, EVAL-04 · 25→IMP-01 · 26→SYM-08, SYM-09 · 27→X-01 · 28→X-08.

---

## 12. Completeness critique (adversarial review of this document)

An independent critic cross-checked this synthesis against the reader findings and the peers' source. Its findings are binding corrections for the build plan in `docs/plans/2026-09-25-parity-build-plan.md`.

### 12.1 Verdict

The synthesis is thorough on the eight readers' sources, but it is not yet complete and not yet shown to fit the budget. I spot-checked several claims and they hold:
- Claude Code updatedToolOutput works for all tools with a shape match (hooks.md:2035-2040).
- Codex decision:block replaces the tool result.
- Cursor updated_mcp_tool_output is MCP-only.
- Pi 0.87.1 has context_edit and turn_end.
- wiki and goal have Go callers.

Five problems remain.

1. **Sources not read.** Clones the project already researched were never read: aider (repo map), pr-agent (dynamic context), AutoCodeRover (SBFL), and OpenHands keyword microagents. Cline's post-edit diagnostics delta and harness events (SubagentStart/Stop, CwdChanged, WorktreeCreate) are missing. Four must-level requirements are absent: prompt-injection defense, a human-approval surface, daemon lifecycle, and workspace resolution over HTTP.

2. **The budget chapter contradicts itself.** The per-workstream MiB add up to about 85–90 MiB steady, against 57–63 in §7.2. The peak math leaves out:
   - query peaks that overlap a reindex (+17 MiB);
   - snapshot-swap double-buffering (+12–15 MiB);
   - a resolver transient of up to 40 MiB inside the heavy slot;
   - realistic CSR density, which is 180–380 B/symbol, not 150;
   - worktree-per-agent multiplicity.
   
   A realistic default-envelope peak is about 100–115 MiB unless five things change: graph segments become file-backed, captures leave the heavy slot, worktrees share a base index with overlays, governance structures stay in SQLite, and GOMEMLIMIT is set to the daemon's line.

3. **The plan has sequencing and collision errors.**
   - WS-14 has no edge source.
   - The dependencies WS-15→WS-22, WS-18→WS-14 and WS-23→WS-14/WS-20 are missing from depends_on.
   - The collision map treats the diagnostics WIP as "likely at HEAD". It is not: it changes budget.go, root.go, main.go and xmustard-ops/main.go outside HEAD.
   - The WS-00 file lists are incomplete, and a frontend contract change goes unchecked.

4. **Scope and licensing are unsettled.**
   - "Parity" has no numeric thresholds, so no workstream can pass or fail it.
   - Parity with Augment and Sourcegraph is interface parity only, because they are hosted.
   - Serena-grade precision exists only with the external LSP lane.
   - Several GitNexus parity surfaces are advertised in §5 but never scheduled.

   GitNexus is PolyForm Noncommercial, which puts it at licensing risk as both a benchmark fixture and a comparator.

5. **Context cost has no budget.** tools/list is measured at 5,177 B today and would grow about 6–10× under §5. ground gains about 25 sections with no max_chars.

Fix these before wave 2. The owner's belief that deterministic parity fits in 50–100 MB is still plausible, but only under the tightened design above, and it stays unproven until gate v2 runs at parity scale with the overlap and worktree scenarios.

### 12.2 Missing requirements

| Priority | Capability | Source | Why |
|---|---|---|---|
| must | Wrapper cycle: prompt-injection and memory-poisoning defense on every injection surface (core-tier projection in initialize.instructions, SessionStart/PreToolUse additionalContext, hook-injected path memories, imported sessions, captured WebFetch/Read output that later becomes candidate memories) | Gap across the synthesis (§4.7 GOV-08, §4.12 HAR-02/HAR-03, D-21). Claude Code hooks doc (code.claude.com/docs/en/hooks.md:939-1025) confirms additionalContext goes straight into model context. | The plan pushes promoted memory text into every session automatically (HAR-02/03, GOV-08 core tier) and mines captured tool output into candidates (GOV-14). A malicious or naive agent's proposal, verified by a second agent (two agents verifying each other is weak), becomes standing instructions for every client. Nothing covers this: no instruction-pattern scan on remember, no data-framing of injected text, no human-approver requirement for the core tier or hook-injected tiers, and no quarantine for content derived from untrusted sources (web, foreign imports). |
| must | Human-approval surface for the human-approver role (protected memories, policy changes, retire of protected entries, purge) | Synthesis §4.14 SEC-02, §4.7 GOV-07; code.claude.com/docs/en/hooks.md lifecycle (Elicitation events) | SEC-02, GOV-07 and GOV-04 require a 'human-approver' principal, and UI is out of scope, yet no requirement defines how a human approves. There is no `xmustard-ops approve/queue` CLI, and MCP elicitation (Claude Code lists Elicitation/ElicitationResult hook events) is not used. As written, protected-memory flows cannot be completed. |
| must | Worktree overlay index: one shared base index per repo plus a small per-worktree delta for dirty and branch-changed files, instead of a separate hot index per worktree | research/letta-code/src/tools/enter-worktree.test.ts, exit-worktree.test.ts; code.claude.com/docs/en/hooks.md (WorktreeCreate/WorktreeRemove); GNX-21; synthesis §7.4 'Second hot repo … no' | Running one worktree per agent is now the common multi-agent pattern. Letta Code ships enter/exit-worktree tools, Claude Code has WorktreeCreate/WorktreeRemove hooks, and GitNexus has branch/worktree indexes. Under RT-10/SHARE-04, each worktree is a separate hot repo at +20–25 MiB, and §7.4 already says a second hot repo does not fit. So the headline 'several agents share memory' scenario breaks the budget unless worktrees share a base index. |
| should | Post-edit new-diagnostics delta injected after Edit/Write/apply_patch (syntax errors from tree-sitter first; LSP/compiler delta when available) | research/cline/apps/vscode/src/integrations/diagnostics/index.ts:7-25 (getNewDiagnostics); cline/apps/vscode/src/core/task/tools/handlers/WriteToFileToolHandler.ts:362-412 (newProblemsMessage); research/SWE-agent/tools/edit_anthropic/bin/str_replace_editor:38-151 | Cline diffs diagnostics before and after every file write and returns 'new problems' in the tool result. SWE-agent's edit tool runs flake8 on each edit and reports only errors the edit introduced. Both are direct harness-quality features. The synthesis has diagnostics(since=checkpoint) as a pull tool (HAR-07) but no PostToolUse(Edit\|Write\|apply_patch) push through the hook service. |
| should | Personalized PageRank repo map fitted to a token budget (personalization from files in play and identifiers mentioned; binary search to max_map_tokens) | research/aider/aider/repomap.py:365-529 (get_ranked_tags, personalization, nx.pagerank), :576-629 (get_ranked_tags_map with max_map_tokens); rust-core/src/repomap.rs:248,925 | aider sits in research/ and in the earlier research (docs/RESEARCH_FINDINGS.md §2, MARKET_SCOUT), but no reader read it. SYM-10's 'orientation packet' is a static histogram, not a relevance-ranked, token-fitted map. xMustard already has repomap.rs with no PageRank or token budget (only build_repo_map and rank_affected_paths). This is the cheapest narrow-context win for ground and explain. |
| should | Keyword- and trigger-activated knowledge injection at UserPromptSubmit (memories carrying triggers[]; first-prompt recall) | research/OpenHands/.openhands/microagents/documentation.md (type: knowledge; triggers: documentation, docs, document); synthesis §6.13 Mem0 coding plugin step 5 (listed as a peer process only, never promoted to a PAR requirement) | OpenHands knowledge microagents activate on prompt keywords, and Mem0's coding plugin does first-prompt recall. HAR-01 lists the UserPromptSubmit event but no requirement says what xMustard injects there. PreToolUse-pattern and path injection (HAR-02) never fire for purely conversational prompts. |
| should | Export verified memories as client-native, path-scoped rule files (.clinerules `paths:` conditional rules, Cursor rules, .claude/rules, AGENTS.md sections), marker-bounded and revision-stamped | research/cline/docs/customization/cline-rules.mdx:156-236 (paths conditionals); Cursor hooks doc beforeReadFile input carries attachments of type 'rule' (cursor.com/docs/hooks) | This path costs no RSS and needs no hooks, and it works in clients whose hook seams are weak (Cline has no MCP result replacement; Cursor built-ins are observe-only). HAR-03 writes a single AGENTS.md block and GOV-17 exports skills only. Conditional rule files are the peers' own delivery mechanism for scoped guidance. |
| should | Subagent lifecycle capture and attribution (SubagentStart/SubagentStop in Claude Code, Codex and Cursor; Codex Interrupt; Claude CwdChanged and InstructionsLoaded) | code.claude.com/docs/en/hooks.md lifecycle (SubagentStart/Stop, CwdChanged, InstructionsLoaded, WorktreeCreate); developers.openai.com/codex/hooks.md:20-27 (SubagentStart/SubagentStop, Interrupt); cursor.com/docs/hooks (subagentStart/subagentStop) | PROV-07 derives sub-agent identity from HTTP headers taken from the cursor-bridge model gateway, not from hooks. Delegated subagent results, which carry the most compaction-sensitive evidence, are never captured into the session ledger or handoff. CwdChanged is the only reliable signal to re-resolve workspace_id mid-session. |
| must | tools/list token budget as a tested contract, with per-profile schema variants (advanced arguments accepted but not advertised; details in resources) | Local measurement: api-go/cmd/xmustard-mcp built to /private/tmp/claude-501/crit/xm-mcp, tools/list = 5,177 B; synthesis §5 argument lists | Measured today: 9 tools, 29 arguments, 5,177 bytes (~1.3k tokens), from the built xmustard-mcp tools/list. The §5 target surface has ~130 arguments plus outputSchema per tool, an estimated 25–40 KB (~6–10k tokens) in every session's prompt prefix. That contradicts the tool-context-reduction thesis. ADP-01 says only 'terse descriptions', with no numeric cap or test. |
| must | ground output budget with section selection (sections=[...], max_chars, per-section caps, degradation ladder) | Synthesis §5 ground; PAR-CTX-05 | §5 adds about 25 sections to ground (freshness, coverage, baseline, pending_for_you, at_risk, memory_health, next_action, consolidation_jobs, core_memory, deferred_index, sessions, handoffs, orientation, delivery_capabilities, and more). ground is the first call of every session. CTX-05 names 'all nine tools' but gives ladders only for search, explain and impact. As specified, ground would become the biggest context cost in the product. |
| must | Scale envelope for governance data (active memories, pending per principal, sessions, transcripts, evidence handles) with resident structures disk-backed | Synthesis PAR-GOV-03 MiB column, PAR-RT-09, §7.3 | GOV-03 keeps MinHash/LSH bands resident at ~0.3 KB per memory, so 100k memories is about 30 MiB. Anchor hot sets, session-seen sets and repeat-guard state also grow with use. RT-09's envelope covers code only (files, symbols, agents, repos). Without quotas (pending per principal, proposals per session), one looping agent grows daemon RSS without bound. |
| must | Daemon lifecycle: launchd/systemd service or socket activation, auto-start on first hook or MCP connect, crash restart, log rotation, and upgrade with schema migration while agents are connected | Synthesis PAR-RT-03 current-state note; PAR-ADP-13 | RT-03 moves every client onto one shared daemon (HTTP MCP plus hooks). Today the shim 'does not start the API', and peers such as Serena and GitNexus avoid this problem by launching per client over stdio. If the daemon is down, every hook fails open silently and MCP breaks. ADP-13 covers client config only. |
| must | Workspace resolution over HTTP MCP without a cwd signal (per-project URL parameter or header written by setup; roots/list conformance per client) | Synthesis PAR-ADP-02 vs PAR-RT-03 | ADP-02 resolves the workspace from the shim's cwd. RT-03 removes the shim for HTTP clients, so the only remaining signals are roots/list (support varies by client and is untested) and explicit arguments. Moving to HTTP silently regresses auto-resolution. |
| should | Durable delivery log: which memory revisions were injected or returned to which session and principal, and when | Synthesis PAR-GOV-16, PAR-RCL-04, PAR-EVAL-02; research/mem0 has no openmemory/ directory | GOV-16 stale-harm attribution and EVAL-02's stale-memory-harm metric need to know what an agent was shown. RCL-04's session-seen sets are TTL-evicted, and PROV-01 logs writes only. Mem0's OpenMemory had memory access logs for this purpose, but openmemory/ is absent from the local clone, so that source was not read. |
| should | Non-code text indexing: Markdown headings and sections, YAML/JSON/TOML config keys, Dockerfile, SQL, proto/GraphQL | research/gitnexus/gitnexus/src/core/ingestion/markdown-processor.ts; synthesis PAR-RCL-02, PAR-SYM-05 | RCL-02 extracts 'env and config keys' as memory anchors, and RET-01 replaces the docs lane, but SYM-05 and WS-16 cover programming languages only. GitNexus has a markdown processor phase. Without config and doc indexing, config-key anchors cannot resolve and drift checks cannot find them. |
| should | GitNexus language and framework breadth: Vue SFC, Dart, Objective-C, Zig, COBOL, plus framework route/DI extractors (Django, FastAPI, Next.js, Nest, Spring, Laravel, tRPC, Expo) that seed entry points | research/gitnexus/gitnexus/src/core/ingestion/languages/{vue,dart,objective-c,zig,cobol}.ts; route-extractors/{django,fastapi-router-bindings,nextjs,nest,spring,laravel,trpc,expo}.ts; di-extractors/spring.ts | The SYM-05 target list omits five languages GitNexus ships. Framework route extractors are folded into IMP-10 as 'could' and never scheduled, yet they supply the entry points that SYM-09 flow detection depends on. Flows will be weak on web apps. |
| should | Asymmetric dynamic diff context: expand change hunks to the enclosing function or class, with more context before the change than after | research/pr-agent/docs/docs/core-abilities/dynamic_context.md:1-40 | pr-agent was part of the earlier research but no reader read it. IMP-06 and ground what_changed return symbols and ranges but no bounded, model-friendly hunk context, which is what an agent needs to review its own change. |
| could | Spectrum-based fault localization from test coverage feeding why_failed | research/auto-code-rover/app (sbfl in config.py, task.py, inference.py) | AutoCodeRover in research/ uses SBFL to rank suspicious methods from failing and passing test coverage. why_failed currently only parses logs. This is deterministic, needs no model, and fits the evidence thesis. No reader covered it. |
| could | Corroboration-count ranking signal and repo-configurable typed memory kinds and claim predicates | research/graphiti/graphiti_core/search/search_config.py:53-67; graphiti_core/graphiti.py:619-649 | Graphiti ranks with an episode_mentions reranker and lets users define entity_types and edge_type_map. GOV-10 fixes the predicate vocabulary and RCL-01 fusion ignores corroboration counts, even though GOV-03 records corroboration. |
| should | Governance store backup and integrity (VACUUM INTO or online backup; PRAGMA quick_check on open; restore command) | Synthesis PAR-STORE-01, WS-12 | SQLite becomes the single source of truth for shared verified memory. The only recovery paths offered are Markdown/JSONL export (GOV-18) and a one-time JSON import backup (WS-12). |
| should | Per-client injection limits in the adapter policy table: Codex additionalContextLimit defaults to 2,500 tokens; Cursor supports command and prompt hooks only (no http or mcp_tool) | developers.openai.com/codex/hooks.md (~1411-1427 additionalContextLimit default 2500; line 193 command and mcp_tool handlers only); cursor.com/docs/hooks (command-based default and prompt-based types; failClosed; updated_mcp_tool_output MCP-only) | CTX-03 records Claude's 10,000-character cap but not Codex's token-based limit. D-08's 'Go hook client first' is wrong for Cursor, where every tool call spawns the hook binary. Cursor's failClosed and beforeReadFile semantics also affect fail-open design. |

### 12.3 Plan problems

- Parity is never defined numerically, so it cannot be tested. WS-50 accepts 'a published, reproducible parity report', WS-18 says 'improves by the agreed delta', and WS-11 accepts a fake-driver dry run. Nothing sets thresholds such as localization recall@k within X of the GitNexus/Serena arms, impact precision ≥Y against LSP truth, stale-served rate ≤Z, or token savings ≥N% at evidence recall ≥M%. Without them, 'full parity' cannot be passed or failed.
- Quality parity with hosted engines (Augment Context Engine, Sourcegraph Deep Search/NLS) cannot be verified locally, and they cannot be run as comparator arms without accounts. RET-03/RET-07 (static potion embeddings plus deterministic decomposition) give interface parity at best. The doc should say that, or add an opt-in hosted comparator arm with explicit data permission.
- Serena parity is heuristic by default. Precision comes only from the opt-in external LSP pool (SYM-06: 200 MiB to GBs, outside the gate), and the default resolver in WS-17 is 'receiver typing covers simple cases'. It should be labeled 'Serena-shape surface, heuristic precision unless LSP/SCIP is enabled'.
- The collision map for the diagnostics work is wrong. wip/diagnostics-live-set is NOT in HEAD 7ab61a0 (merge-base fe267dc). Outside the diagnostics files it changes api-go/internal/budget/budget.go (+44), internal/rustcore/root.go (+15), cmd/xmustard-api/main.go (+94), cmd/xmustard-ops/main.go (+21), workspaceops/safepath_unix.go, context_packet.go, integrations/pi/test/e2e/{harness.ts,pi-adapter.e2e.ts} and .gitignore. The synthesis calls budget.go, root.go and treesitter.rs changes 'likely already at HEAD, risk low'. They collide directly with WS-06 (budget.go), WS-02 (root.go), WS-09 (safepath), WS-24 (Pi e2e), WS-26 (.gitignore) and every xmustard-ops workstream. The opus worktree /private/tmp/xmustard-opus-l9b1F4 also has 122 dirty paths at cd13e2b, including rust-core search.rs, symbolgraph.rs, changetrack.rs, indexcache.rs, treesitter.rs and repomap.rs.
- WS-00 is mis-described. All four w0 branches are already based on 7ab61a0 (0 commits behind), so no rebase is needed. Its file list also omits files the branches actually change: w0-kernel touches auth.go, auth_test.go, runtime_settings.go, frontend/src/lib/api.ts (a frontend contract, so make check-frontend is required per AGENTS.md) and README.md; w0-feedback touches failure_explainer.go; w0-cleanup touches AGENTS.md, README.md and docs/ARCHITECTURE.md. README.md is edited by two w0 branches and by the diagnostics session, which conflicts with the 'do not edit README.md' rule.
- WS-14 has no edge source. WS-07's schema has files, symbols, references, imports, chunks and meta, but no edges table. WS-17 adds edges and runs in parallel in wave 2. WS-14's acceptance ('CSR built from index.db equals edges in DB', 'identical results to the legacy graph … while edges are still lexical') cannot be met. Either port the legacy lexical edge builder into index.db (in WS-07 or a new step) or make WS-14 depend on WS-17.
- Dependency edges promised in the collision map are missing from depends_on. WS-15 needs WS-22, since both touch changetrack.rs and the collision map says WS-22 goes first. WS-18 needs WS-14, since both touch search.rs and search-from-index needs the resident reader. WS-23 needs WS-14: PreToolUse injection of 'top BM25 and graph hits' with the acceptance 'zero Rust/git spawns per hook' is impossible without the resident service. WS-23 also needs WS-20 for path-bound verified memories. WS-15's 'reresolved' counter needs WS-17.
- Several workstreams are too large or have mixed owners. WS-19 has 12 requirements (lifecycle states, provenance, evidence-bound votes, owner-distinct policy, redaction, expiry, kinds). WS-31 mixes tiers and injection with SEC-02 roles and SEC-06 repository trust. WS-40 bundles four different client adapters with different languages and runtimes. WS-44 has 6 requirements, WS-35 has 6.
- Requirements have duplicate or ambiguous owners: SEC-02 (WS-09 and WS-31), SEC-05 (WS-09 and WS-19), SEC-06 (WS-31 and WS-44), FRESH-06 (WS-09 and WS-22), CTX-05 (WS-04 and WS-36), IMP-08 (WS-35 and WS-47), GOV-14 (WS-32 and WS-33), CTX-07 (WS-24 and WS-40), SEC-04 (WS-05, WS-19 and WS-34), FRESH-08 (WS-07 and WS-44). The acceptance criteria do not say which workstream closes each requirement.
- Some requirements are unscheduled even though §5 advertises their surface. IMP-09 (impact op=rename|delete), RET-09 (search mode=exact|regex), RET-10 (fields/where), IMP-11 (workspace_id='@group'), IMP-10, IMP-12, SYM-11 and FRESH-09 have no workstream. For GitNexus 'full parity' this leaves out rename, route_map/tool_map/shape_check/api_impact, group_sync and pdg_query/explain(taint), all of which GitNexus exposes (research/gitnexus/gitnexus/src/mcp/tools.ts). Either schedule them or remove them from §5 and state they are not parity.
- Heavy-slot contention defeats context reduction when it is needed most. RT-04 routes captures ≥4 MiB through the single heavy slot. FRESH-03 triggers a reindex 300 ms after every edit. Agents typically run tests right after editing, so large test output hits a busy slot and waits up to the 10 s bound, or gets 503. The PostToolUse hook then either stalls the agent or fails open and delivers the full unreduced output. WS-08 already claims O(window) streaming capture, so captures should not need the heavy slot, and hook paths must never wait on it.
- Claude Code gives all SessionEnd hooks a shared 1.5-second budget (hooks.md timeout row). WS-33's handoff capsule and candidate mining at Stop/SessionEnd must be asynchronous or they will be cut off.
- D-02 (rusqlite FTS5 vs tantivy) is still an open decision, yet WS-07 (wave 1) hard-codes rusqlite and WS-18 hard-codes FTS5. The budget lines are based on tantivy measurements. Resolve D-02 with an FTS5 measurement before WS-07 starts, or make WS-07's first deliverable that measurement.
- Licensing risk in the evaluation plan. EVAL-01 runs GitNexus (PolyForm Noncommercial 1.0.0) as a comparator arm, and EVAL-05 and WS-07 use the gitnexus clone as the benchmark fixture ('indexes every eligible file of the gitnexus clone'). Using NC-licensed software to develop a product may be commercial use. Switch fixtures to Apache/MIT repositories (cline, pi-mono, OpenHands SDK) and get an explicit owner decision before running GitNexus. Separately, research/cursor-bridge has no .git of its own (git log shows the xMustard commit), so 'record source path and commit' is impossible; record a SHA-256 manifest instead. The copy also contains unrelated deploy scripts (pinterest_*, fix_nginx_*, deploy_*) that must be excluded from any port.
- WS-11/WS-50 have no corpus-authoring workstream (tasks with hidden oracles that fail on baseline and pass on the reference) and no funded real-model run. The executor accepts only a fake driver, so no parity claim can be produced within this plan.
- Researched sources that were not read: aider (repomap.py, benchmark), pr-agent (dynamic_context, review/improve), qodo-cover, AutoCodeRover (SBFL, AST search APIs), vulnhuntr, openhands-resolver and trIAge. All are in research/ and cited in docs/RESEARCH_FINDINGS.md as research the project discussed. OpenHands .openhands/microagents (keyword triggers) was not mined. Serena JetBrainsTypeHierarchyTool (type hierarchy) is unmapped. The Mem0 OpenMemory MCP server (access logs, per-app pause) is absent from the clone and unread. OpenCode claims come from the unpinned GitHub dev branch; nothing is cloned locally.
- WS-13's relay must be pinned to std-only Rust (no tokio or reqwest) and must parse Streamable-HTTP SSE responses. Otherwise the ≤3 MiB target fails. For reference, I measured the existing gomin Go relay probe at 13.7–16.4 MB ps-RSS and 6.8–9.7 MB phys_footprint, so the native-relay rationale holds.
- Flipping core-only to the default (WS-09, wave 1) breaks the frontend and platform consumers long before WS-49 builds the kernel. Also, once WS-49 puts the platform behind a build tag, the only callers of wiki (workspaceops/knowledge.go:192) and goal (workspaceops/goals.go) become platform-only, so wiki.rs and goalruntime.rs are dead in the default binary. The Rust side then needs the same feature gate.

### 12.4 Budget problems

- §7.2/§7.3 contradict the workstream budget_mb values. Adding up per-workstream deltas that land in the Go daemon (WS-05, 06, 08, 19, 20, 21, 23, 27, 28, 29, 31, 32, 33, 34, 41, 42, 45, 47, 52, 53) gives about 24 MiB, or about 29 MiB with the WS-43 gateway, on top of today's 14–25.5 MiB API. §7.3 gives the whole daemon 20–23 steady and allocates only ~8 MiB to all of these (governance caches ≤3, evidence ≤2.5, hooks ≤1, runner ≤1, redaction ≤1). The Rust-side workstream deltas (WS-02, 14, 15, 17, 18, 30, 35, 36, 37, 38, 52) add up to about 37 MiB against §7.2's 32. By the workstream numbers, steady state is about 85–90 MiB before any heavy-slot work, not 57–63. One ledger is needed.
- The peak calculation (~87–93 MiB) adds the heavy slot only to steady state. But queries keep running during reindex (§6.1 step 3), so per-component query peaks overlap the slot: daemon +6, CSR +3, BM25 +3, embeddings +4, vectors +1 = +17 MiB. That gives about 104–110 MiB against the 95.4 MiB gate.
- Snapshot swap double-buffering is not counted. §6.1 step 10 rebuilds the CSR delta and name index in the resident service and 'readers drain from the old snapshot', so two graphs are briefly resident (+12–15 MiB). This contradicts the §7.2 note that the graph is 'mostly file-backed and reclaimable', because D-02(a) recommends 'an in-memory CSR built on load'.
- The CSR target of ≤150 B/symbol including edges is implausible for resolved symbol-level edges. Probe data (/private/tmp/claude-501/budget-probe/out-gitnexus-build.json) shows today's graph is 6,538 symbols with 8,245 file-to-file edges. A resolved graph with CALLS, ACCESSES, HAS_METHOD and IMPORTS typically has 5–10 edges per symbol. Forward plus reverse CSR at 8–12 B per edge is 80–240 B per symbol from edges alone, plus about 40 B for the symbol row and 40–80 B for interned qualified names: roughly 180–380 B per symbol, or 18–36 MiB at 100k symbols, against a 12/15 line. WS-14's synthetic 100k-symbol test must use realistic edge density taken from WS-17 output.
- The heavy slot assumes a ≤25 MiB transient, but the same worker also runs resolution (the IMP-01 row says 'resolver T 10–40 (unmeasured)'), areas and flows (SYM-08: 10–20 T), and at index time the static-embedding pass. A full rebuild at the envelope (10k files, ~80–100k symbols, ~0.5–1M references) has not been measured, so the heavy-slot line is unproven and may be 40 MiB or more.
- GOMEMLIMIT of about 40 MiB (PAR-RT-05, §7.2) is above the daemon's own 28 MiB peak line. The Go GC will let the heap grow toward 40 before collecting aggressively, so the limit permits breaking the allocation. Set it to the daemon line, around 26–28, or accept a larger line.
- The BM25 line (5/8) and the '2.5 MB reader footprint' basis come from tantivy probes, but D-02 and WS-18 choose FTS5 in rusqlite, which has no measurement. The design also links two SQLite engines, modernc in Go and bundled rusqlite in Rust, each with its own page cache and text pages. Only the Go one is counted.
- IMP-06 says 'dirty and untracked files are re-parsed' at query time. That needs tree-sitter grammars inside the resident service (the 17-grammar probe measured +20 MiB RSS) or a worker spawn per impact call, which violates the no-spawn hot path. Neither is in §7.2.
- Worktree multiplicity: with one worktree per agent (Letta enter-worktree, Claude Code WorktreeCreate, Codex worktrees), each worktree counts as a hot repo under RT-10 (+20–25 MiB each). The common four-agent case therefore does not fit unless worktrees share a base index with small overlays.
- Hook client processes are not attributed. Cursor supports only command and prompt hook types, so every Cursor tool call spawns the xmustard-hook binary, and parallel calls spawn several. D-08 picks a Go client at ~5–10 MiB each. My measurement of the minimal Go relay probe was 13.7–16.4 MB ps-RSS, which suggests a Go hook client is larger than 5–10 MiB. §7.6's external-process list does not say whether xMustard binaries launched by the agent count toward the gate. Default to a Rust client of ≤2 MiB for Cursor and declare the accounting.
- Governance and session structures grow with use and are not in the envelope: LSH bands at ~0.3 KB per memory (100k memories ≈ 30 MiB), anchor hot set, session-seen sets, and per-session repeat-guard LRU across many sessions. Keep LSH bands and anchor postings in SQLite tables, and add memory, session and pending-queue quotas to RT-09.
- Context budget, which is separate from RSS: tools/list today is 5,177 bytes (~1.3k tokens) for 29 arguments, measured from a scratch build of xmustard-mcp. The §5 target (~130 arguments plus outputSchema) would cost an estimated 6–10k tokens per session, and ground grows by about 25 sections with no max_chars. Neither has a numeric budget or test, which undermines the tool-context-reduction thesis the parity work is meant to strengthen.

### 12.5 Workstream corrections

- WS-00: change the goal from 'rebase' to 'verify and stack'; the branches already sit on 7ab61a0. Add auth.go, auth_test.go, runtime_settings.go, failure_explainer.go, frontend/src/lib/api.ts, README.md, AGENTS.md and docs/ARCHITECTURE.md to files_to_touch. Add make check-frontend to tests_required because w0-kernel changes frontend/src/lib/api.ts. Resolve the README.md three-way edit (w0-cleanup, w0-kernel, diagnostics session) explicitly.
- Add WS-00D, 'reconcile wip/diagnostics-live-set with HEAD'. It is not in HEAD and changes budget.go (+44), rustcore/root.go (+15), main.go (+94), xmustard-ops/main.go (+21), safepath_unix.go, context_packet.go, the Pi e2e harness and .gitignore. Make it a predecessor of WS-02, WS-06, WS-09, WS-24 and WS-26, or have each of those rebase onto it. Replace every 'likely already at HEAD' note with the verified diff.
- WS-07: add an edges table and port the legacy lexical edge builder into index.db, labeled provenance=lexical, so WS-14 can reach legacy parity before WS-17. Alternatively, make WS-14 depend on WS-17 and drop the legacy-equality test.
- Add missing depends_on edges: WS-15→WS-22 (changetrack.rs ordering), WS-15→WS-17 (reresolved counters), WS-18→WS-14 (search.rs and resident reader), WS-23→WS-14 and WS-20 (spawn-free search injection and path-bound memories), WS-36→WS-18 (shared tool_search.go).
- WS-06: remove captures from the heavy slot, since WS-08 streams O(window). Add the acceptance 'no hook or capture path ever waits on the heavy slot'. Set debug.SetMemoryLimit to the daemon's line (~26–28 MiB), not 40.
- WS-10: add a per-component budget ledger file. CI fails when a workstream's measured gate-v2 delta exceeds its line. Reconcile §7.2/§7.3 with the workstream budget_mb values before wave 2. Add scenarios for 'queries during reindex' (overlapping peaks), 'snapshot swap under load' and '4 agents in 4 worktrees'. Replace the gitnexus fixture with Apache/MIT repositories (cline, pi-mono, OpenHands SDK) pending an owner licensing decision.
- WS-14: the RSS acceptance must use a real resolved graph (WS-17 output on cline at a pinned sha, ≥5 edges per symbol) and must include swap double-buffering. Decide file-backed CSR segments (built by the worker and read by the service with pread) versus in-memory CSR from measurements, and update D-02 and the §7.2 wording to match.
- WS-07/WS-18: first deliverable is an FTS5 versus tantivy RSS and ranking measurement that closes D-02 before the schema is fixed.
- New WS (wave 2, after WS-04): output-budget contract for ground and tools/list. Add ground(sections, max_chars) with per-section caps and a degradation ladder. Add a snapshot test asserting tools/list stays at or under N bytes per profile (baseline measured today: 5,177 B). Advanced arguments are accepted but not advertised and are documented through an MCP resource.
- New WS (wave 3, before WS-30): worktree overlay index. One base index per repo, per-worktree delta segments for dirty and branch-diff files, and worktree identity from SHARE-04. Acceptance: gate v2 with 4 agents in 4 worktrees of one repo stays under 95.4 MiB.
- New WS: injection-safety policy. Instruction-pattern scan and data framing on everything injected. Core tier and hook-injected memories require human-approver or a stronger verification policy. Content derived from untrusted captures (WebFetch, foreign imports) stays quarantined and cannot enter core. Adversarial fixtures go into EVAL-02.
- New WS: human-approval surface. `xmustard-ops approve|reject|queue` bound to a human-approver token, plus optional MCP elicitation where the client supports it. It is a precondition for the protected paths in WS-19 and WS-31.
- New WS: daemon lifecycle. A launchd/systemd unit or socket activation installed by `xmustard-ops setup`, health-checked auto-start, crash restart, log rotation, and schema migration while clients are connected. Fold govstore backup (VACUUM INTO or online backup, quick_check on open, restore) into it or into WS-12.
- WS-13: pin the relay to std-only Rust. Add a per-project workspace binding (setup writes `/mcp?workspace=<id>` or an X-Xmustard-Workspace header) and a roots/list conformance test per client, because HTTP removes the cwd signal ADP-02 relies on.
- WS-23: add SubagentStart/SubagentStop, CwdChanged, WorktreeCreate/Remove and UserPromptSubmit keyword-trigger recall (OpenHands microagent parity; add triggers[] to remember in WS-19). Add a PostToolUse(Edit|Write|apply_patch) new-diagnostics delta, tree-sitter syntax errors first, following Cline getNewDiagnostics and SWE-agent lint-on-edit. Set explicit http-hook timeouts in hooks.json.
- WS-33: make SessionEnd/Stop work asynchronous (enqueue and return inside Claude Code's 1.5 s SessionEnd budget). Build capsules from the ledger later.
- D-08 and WS-40: the Cursor command hook client is Rust (≤2 MiB) from the start; Cursor has no http or mcp_tool hook type. Record Codex's additionalContextLimit (default 2,500 tokens) and Cursor's failClosed semantics in the WS-40 capability registry. Split WS-40 into WS-40a Codex, WS-40b OpenCode (TypeScript), WS-40c Cursor, and WS-40d Letta recipe plus the registry.
- Split WS-19 into WS-19a (lifecycle states: supersede, retire, retract, purge, expiry, CAS edit) and WS-19b (provenance binding, evidence-bound votes, owner-distinct policy, ingest redaction). Remove SEC-02 and SEC-06 from WS-31 (keep SEC-02 in WS-09 and SEC-06 in WS-44). Assign one closing owner for each duplicated requirement: CTX-05, IMP-08, GOV-14, CTX-07, SEC-04, SEC-05, FRESH-06, FRESH-08.
- Schedule or delete: add WS-54 (impact op=rename|delete read-only plans, IMP-09; search mode=exact|regex, RET-09) and WS-55 (framework route/DI entry-point extractors plus the Vue, Dart and Objective-C language packs). Alternatively, remove those arguments from §5 and list them explicitly as non-parity with reasons.
- New WS (could/should, wave 3): an aider-style personalized PageRank repo map fitted to a token budget, surfaced as ground orientation or explain(path='.'). It runs inside the resident service over the CSR, so it needs no new resident structure.
- New WS or extend WS-31: export verified memories to path-scoped client rule files (.clinerules paths:, Cursor rules, .claude/rules), marker-bounded and revision-stamped. This is a no-RSS delivery path for clients with weak hooks.
- WS-27: keep MinHash/LSH bands and anchor postings in SQLite, not resident. Add per-principal pending quotas and a per-workspace active-memory envelope, with an RSS test at 50k memories.
- WS-11: add a corpus-authoring workstream (tasks with hidden oracles validated to fail on baseline and pass on the reference) and a budgeted real-model run as an operator step. Define numeric parity thresholds per capability family in WS-50's acceptance.
- WS-49: gate the Rust wiki and goal subcommands behind the same platform feature, because their only Go callers (workspaceops/knowledge.go:192 and goals.go) become platform-only.
- WS-05/WS-42 (cursor-bridge ports): record a SHA-256 manifest of the ported source files instead of a commit, because research/cursor-bridge has no .git of its own. Exclude the unrelated pinterest, nginx and deploy scripts in that copy from any port.
