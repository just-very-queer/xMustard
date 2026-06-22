# xMustard post-hardening verification and expansion audit

**Code state inspected:** uploaded `xmustard-codeReview.zip`, repo HEAD `422bbaf` on `feat/product-v1`.  
**Important package caveat:** the prompt calls `FIXES_APPLIED.jsonl` and `GIT_HISTORY.md` authoritative, but those files were not present in the zip. I used code inspection, `goal/xmustard_new_findings.jsonl`, and the uploaded `RESEARCH_NOTE_FOR_GPT.md` / `RETRIEVAL_MEMORY_NOTE.md` as the audit base.  
**Tests attempted:** Go tests were blocked because the repo requires Go 1.26 and the container has Go 1.23.2 with no network access to download the toolchain. Rust tests were blocked because `cargo` is not installed in the container.

## 1. Executive verdict

The hardening pass genuinely closed a lot of the original prototype-era danger: governed-memory transactions now have full load→mutate→save locks, Rust LSP `Drop` kills and waits on its child, Go-side LSP children have an autonomous close path, Postgres uses a shared pool, MCP stdio input is bounded, and Rust cache writes are now atomic. The project is no longer the same fragile prototype reviewed in the first pass.

However, the new post-hardening boundary is clear: **runtime control and protocol ergonomics are now the risky layer.** The biggest remaining risks are terminal route authorization, duplicate run starts, unbounded managed-run output, process-local PG mirror ordering after restart, partially bounded Go→Rust bridges, and recall’s O(history) drift check.

The moat still holds conceptually: governed, drift-checked, cross-agent memory is the strongest differentiator. But to keep that moat useful under real agent load, recall needs to become metadata-first/top-K drift-checked, and the execution plane needs a real supervisor.

## 2. Done vs. left — code-verified inventory

### Done and verified in code

| Area | Verification |
|---|---|
| Memory/context write locks | `storelock.go:25-50` implements path-keyed locks. `ProposeContext` wraps the full transaction at `context_governance.go:265-300`; `VerifyContext` wraps verify/promote at `context_governance.go:320-360`; feedback is locked at `feedback.go:65-90`. |
| Memory drift baseline update | `VerifyContext` captures hashes on first promotion at `context_governance.go:354-358`; `UpdateContextContent` clears stale baseline before re-promotion at `context_governance.go:390-432` (inspected separately). |
| Rust LSP direct child cleanup | `rust-core/src/lsp_session.rs:198-202` kills and waits in `Drop`. |
| Go LSP child cleanup exists | `lsp_definition.go:775-788` sends shutdown/exit, kills, waits, and closes stdin via `closeOnce`. The reaper starts lazily in `lsp_definition.go:275-276`. |
| PG pool exists | `pgpool.go` uses a shared capped `pgxpool`; inline mirror uses `pgPool(ctx)` at `pg_inline.go:128-135`. |
| MCP newline-less OOM fixed | MCP stdio now reads bounded lines: `xmustard-mcp/main.go:258-280`. |
| Rust cache atomic write fixed | `indexcache.rs:18-42` writes temp + sync + rename instead of truncating the target. |
| Search coverage honesty exists | Search returns `coverage` from the graph in `search.rs:151-158` and `search.rs:367-373`. |

### Still left / overstated by docs or status narrative

1. **Workspace ACLs are not universal.** Auth scopes only check `/api/workspaces/{id}/...` paths (`main.go:4044-4075`). Terminal routes are `/api/terminal/...` and carry `workspace_id` in body/query (`main.go:3735-3838`), so scoped tokens can bypass workspace isolation for terminal verbs.
2. **PG ordering is not restart-safe.** The mirror sequence is an in-process `atomic.Int64` (`pg_inline.go:101-125`) but rows persist `mirror_seq`; after restart a lower sequence can be skipped forever by `storedSeq >= seq` (`pg_inline.go:154-164`).
3. **Binary-first bridge is only partial.** Some wrappers use `coreCommand`, but repo-map/impact/explain helpers still hard-code `cargo run` (`repomap.go:142-164`, `188-210`, `219-242`, `251-264`).
4. **Bridge timeouts/caps are not universal.** RunSearch/RunChangetrack/RunSymbolgraph still use no-context commands, unbounded buffers, and raw stderr (`knowledge.go:8-18`, `changetrack.go:10-18`, `symbolgraph.go:10-18`).
5. **Run supervisor is still thin.** `ApproveRunPlan` can race into duplicate starts (`run_control.go:258-289`), and `runManagedProcess` still has full-output buffering/no timeout (`run_control.go:361-409`).
6. **Terminal lifecycle is partial.** Explicit close cleans up, but terminal sessions are globally keyed (`terminal.go:60`), overwritten on collision (`terminal.go:118`), and natural exit only marks closed (`terminal.go:120-125`, `234-238`).
7. **LSP leak is fixed, but LSP robustness is not.** Some close paths still run under the global lock (`lsp_definition.go:275-305`), and Go LSP frame parsing has no header/payload caps (`lsp_definition.go:804-833`).
8. **Recall still does O(history) drift work.** `RecallContext` computes staleness for every promoted entry before ranking/limit (`context_governance.go:504-515`).
9. **MCP schemas remain too weak.** `tools/list` omits optional parameters and `tools/call` string-coerces arbitrary JSON (`xmustard-mcp/main.go:176-202`, `240-250`). `remember` still sends durable content in the URL (`xmustard-mcp/main.go:63-73`).

## 3. Adversarial runtime bug and memory-leak report

The JSONL appendix contains 16 confirmed post-hardening findings. The highest-priority ones are:

| ID | Priority | Finding | Concrete trigger | Fix |
|---|---:|---|---|---|
| XM-POST-001 | P0 | Terminal workspace-scope bypass | Scoped token for ws-a opens terminal in ws-b via `/api/terminal/open` body. | Workspace-shaped terminal routes or body/query workspace extraction before auth decision. |
| XM-POST-002 | P0 | terminal_id collision overwrites live session | Open two terminals with same caller-supplied ID. | Composite key `{workspace, terminal}` and reject live duplicates. |
| XM-POST-004 | P0 | Concurrent approvals start duplicate workers | Two approval requests race before phase transition is visible. | Per-run lock/actor + `LoadOrStore` active process guard. |
| XM-POST-005 | P1 | Managed run output/no-timeout DoS | Worker prints unbounded output or never exits. | Run supervisor, output ring, timeout/kill, concurrency bound. |
| XM-POST-006 | P1 | PG mirror stale after restart | Stored `mirror_seq=100`, process restarts, new seq=1 gets skipped. | DB sequence or initialize process sequence from PG max. |
| XM-POST-007 | P1 | Go→Rust hot wrappers no timeout/cap | Hung `xmustard-core search` wedges handler. | Shared bounded bridge helper. |
| XM-POST-010 | P1 | Go LSP frame OOM | Fake LSP sends huge `Content-Length`. | Header/payload caps and close-on-violation. |
| XM-POST-011 | P1 | Recall O(history) drift check | `recall limit=1` over 10k memories hashes all paths. | Metadata-first rank, drift-check only top-K candidates. |
| XM-POST-012 | P1 | MCP schema/query transport weakness | `remember` content in URL; wrong types coerced to strings. | Per-tool schema validation and POST body channel. |

## 4. Updated A→B→C cascade map

### Cascade A — terminal routes outside workspace path → ACL bypass → host shell exposure

A: Workspace ACLs inspect only `/api/workspaces/{id}` path segments (`main.go:4044-4075`).  
B: Terminal routes are `/api/terminal/...` and take workspace id from body/query (`main.go:3735-3838`).  
C: Scoped worker tokens can address another workspace’s shell.  
D: The “per-worker token scoping” guarantee becomes route-shape dependent.  
E: A worker delegated implementation in one repo can interact with another repo’s terminal.

Smallest fix: move terminal APIs under `/api/workspaces/{id}` or add a pre-auth workspace extractor for these routes and test all verbs.

### Cascade B — per-run JSON state + no run actor → duplicate workers → unkillable/uncosted work

A: `ApproveRunPlan` does read/check/save/start with no per-run transaction lock (`run_control.go:258-289`).  
B: Two approvals can both pass the phase check.  
C: Both start `runManagedProcess`; `activeRunProcesses.Store` overwrites the handle (`run_control.go:373`).  
D: Cancel may kill one process while the other keeps running.  
E: Verification evidence, logs, costs and worktree mutations become nondeterministic.

Smallest fix: per-run mutex/actor and `LoadOrStore` active-process guard.

### Cascade C — process-local PG mirror order → restart → persistent mirror drift

A: `pgMirrorSeq` is in-process only (`pg_inline.go:101-125`).  
B: PG rows survive restarts with old, higher `mirror_seq`.  
C: New-process snapshots with lower seq are skipped (`pg_inline.go:154-164`).  
D: PG can trail JSON indefinitely after restart.  
E: UI/queries can present stale state while JSON is correct.

Smallest fix: durable DB sequence or initialize the process counter from `max(mirror_seq)` before mirroring.

### Cascade D — drift-on-recall is eager → O(history) hot path → moat gets expensive

A: Recall loads all promoted memory and hashes all referenced paths before ranking (`context_governance.go:504-515`).  
B: Latency grows with total memory history, not requested limit.  
C: Multi-agent memory gets slower as it succeeds and accumulates verified claims.  
D: Operators may trim/disable drift checks to keep recall usable.  
E: The core moat — memory that knows when it is stale — becomes operationally fragile.

Smallest fix: metadata-first ranking, drift-check bounded candidate window, cache file digests, background invalidation.

### Cascade E — per-call Rust bridge + incomplete timeout/cap migration → handler wedges → agent stalls

A: Go still shells out per semantic call.  
B: Some hot wrappers lack timeout/output caps (`knowledge.go:8-18`, `changetrack.go:10-18`, `symbolgraph.go:10-18`).  
C: Hung/noisy child processes wedge HTTP/MCP calls and spike memory.  
D: One agent’s search/ground can degrade controller throughput.  
E: The persistent daemon remains valuable, but a bounded bridge helper is the immediate P1.

### Cascade F — LSP protocol trust → unbounded frame allocation → budget failure when LSP is enabled

A: Go LSP reader trusts `Content-Length` and header lines (`lsp_definition.go:804-833`).  
B: A broken LSP can force large allocation or block a read loop.  
C: The child reaper prevents orphaning but not API OOM/stall.  
D: LSP-on mode violates the 50-100 MB guarantee.  
E: Agents see flaky definition/diagnostic lookups and may retry, compounding load.

## 5. “Write the Rust better” — concrete refactors

1. **One `IndexEngine` daemon:** hold graph/index state in-process, expose unix socket or loopback local transport. Go calls the daemon instead of spawning Rust for each `search`, `ground`, `impact`, `explain`.
2. **Content-addressed cache:** replace `cheap_key` (`indexcache.rs:64-91`) with BLAKE3 per-file hashes and a Merkle tree. Keep mtime only as a skip hint, never as correctness identity.
3. **Persisted query-time metadata:** precompute hotspots, authority, IDF/posting lists at index time. `search.rs:141-180` should not rebuild these structures for every query.
4. **Bound graph build memory:** `symbolgraph.rs:721-760` should avoid keeping every full file in `content_cache`; stream edge extraction and store compact per-file segments.
5. **Intern symbols/paths:** replace cloned `String` names/paths in graph edges with interned IDs. This is the most direct RSS reduction for the daemon.
6. **Typed errors:** standardize `thiserror` for cache/index/LSP/search failures; daemon must degrade/rebuild on corrupt cache rather than panic or exit.
7. **Parallel parse with care:** per-file parse/reference/flow passes are parallelizable; use `rayon` with immutable segment outputs then merge, avoiding shared `name_to_defs` contention.

Scope roadmap: after the daemon and BLAKE3/Merkle identity land, add optional HNSW/code embeddings behind a feature flag; then type-aware edges and real def-use/taint analysis. Do not build embeddings before the recall/run/protocol P1s.

## 6. MCP tightening + indexing + hashing

Current MCP state: stdio is newline-delimited JSON-RPC in this implementation, with bounded line input. That is now truthful; do not document Content-Length for the current server unless you implement it. The remaining problem is tool contract quality: `inputSchema` is too sparse and dispatcher coercion hides bad calls.

Migration path:

1. **P1 local stdio hardening:** strict `tools/call` param decoding, per-tool JSON Schema, `additionalProperties:false`, typed numeric/string/array validation, structured invalid-params errors, response body cap.
2. **P1 memory transport:** `remember` sends JSON body to HTTP API; API rejects malformed body rather than ignoring decode errors.
3. **P2 shared MCP service:** one streamable-HTTP MCP endpoint for multi-agent/local-remote use, plus a tiny stdio shim for Claude Code/Cursor-style local clients. Keep the same 9 tools.
4. **P2 indexing:** Rust daemon + mmap posting lists + durable segment cache; Go HTTP/MCP becomes a thin policy/auth/gateway layer.
5. **P2 hashing:** BLAKE3+Merkle tree; content-addressed symbol cache; no mtime correctness dependency.

The official MCP tools spec treats a tool as a named capability with an `inputSchema`, and current MCP evolution is moving toward stronger JSON Schema and stateless/routable HTTP deployments. xMustard should match that direction without adding more tools.

## 7. Agent integration

The integration shape is still right: Claude Code/Cursor/Zed connect to local stdio; opencode/codex workers get scoped agent tokens; xMustard/Claude acts as controller and treats worker output as proposals until verified.

What is missing for first-class integration now:

1. **Per-worker token enforcement on every route, not just workspace-shaped routes.** Terminal routes are the proof that route shape matters.
2. **Run streaming and supervision.** Worker output should stream to controller/verification without `strings.Builder` full capture.
3. **Session handoff:** a reconnecting worker should recall promoted memory under a scoped token automatically, but recall must be O(top-K) first.
4. **Codex adapter:** map codex outcomes to the same 9 tools and feedback segment; no new tool surface needed.
5. **Shared MCP service:** one streamable-HTTP service avoids one full MCP process per agent while preserving stdio via shim.

## 8. Runtime budget: 50–100 MB RSS, no Docker

| Component | Current risk | Target RSS | Cuts needed |
|---|---:|---:|---|
| `xmustard-api` Go process | Run buffers, terminal/LSP state, token I/O | 20–35 MB | Run ring buffers, terminal reaper, bounded LSP frames, token cache |
| `xmustard-mcp` | One process per agent; response `ReadAll` | 5–10 MB shim or 15–25 MB per current process | Shared streamable-HTTP MCP service + stdio shim; cap responses |
| Rust core today | Per-call process; fork/deserialize floor | transient | Bounded bridge until daemon lands |
| Rust IndexEngine daemon | Not built | 20–40 MB | Interned graph, mmap posting lists, precomputed query metadata |
| PG pool | Fixed pool but PG external | 5–10 MB client-side | Keep capped; durable mirror sequence |
| LSP children | Reaped but protocol unbounded | 0 MB default; 1 active only when requested | Off by default, one-at-a-time, frame caps, close off-lock |
| Terminals/runs | Shells/workers can persist and buffer | bounded by policy | Idle reaper, max concurrent runs, timeouts |
| Memory/feedback | Recall O(history) I/O | <5 MB hot cache | Metadata-first recall + digest cache |

A guaranteed no-LSP budget is achievable only after the run supervisor, terminal reaper/collision fix, MCP shared service, response caps, and metadata-first recall. Today it is improved but not guaranteed.

## 9. Competitive positioning + papers

Positioning remains strong: **a governed context engine for coding agents, not another coding agent.** The defensible claim is “shared memory that does not silently rot.”

What to steal/beat:

- **Serena:** steal LSP breadth and protocol discipline; beat it with governed memory and drift.
- **Aider:** steal the ranked repo-map insight; beat per-turn maps with persistent warm graph plus memory trust lifecycle.
- **Sourcegraph/Cody:** steal scalable search/index instincts; beat them on agent-memory provenance and verification.
- **SWE-agent/OpenHands:** keep the lean tool surface and disciplined execution loop.
- **MemGPT/RAPTOR/HippoRAG/GraphRAG:** use memory tiers, graph authority and community summaries, but bind them to code-state drift and verifier provenance.
- **CodeRAG-Bench/RRF literature:** keep hybrid retrieval; later add real embeddings only after the default provider-free path is tight.

The best expansion is not more tools. It is deeper guarantees: typed memory, O(top-K) drift honesty, durable execution evidence, and a daemonized graph.

## 10. Explicit answers to the six questions

1. **Which cascade bites first now?** Under production-like agent load, duplicate/unbounded managed runs are most likely to hurt first because they combine correctness, process lifecycle, RAM, and cost. If terminal is exposed, the workspace-scope bypass is the highest security P0. Smallest defusers: per-run lock/`LoadOrStore` active process guard and route-shaped terminal ACL enforcement.

2. **Daemon now, or higher priority P0?** Not yet. The daemon is the top P2 after P0/P1. First fix terminal auth, run supervisor, PG restart ordering, bounded bridge, LSP frame caps, MCP schemas, and recall O(top-K). The daemon will make UX fast; those fixes keep it safe.

3. **Does LSP `Drop` kill children?** Yes for Rust: `rust-core/src/lsp_session.rs:198-202` calls `child.kill()` and `child.wait()`. Go-side LSP also now has `closeOnce` kill/wait at `lsp_definition.go:775-788`. Remaining Go issues are frame bounds and close-under-global-lock paths, not direct orphaning.

4. **Smallest set for guaranteed 50–100 MB?** Disable LSP by default; cap Go LSP frames; terminal idle reaper and collision-safe session keys; run concurrency bound + timeout + output ring; shared MCP HTTP service with stdio shim; metadata-first recall; PG pool kept capped; Rust daemon with interned/mmap segments. Without those, the budget is typical, not guaranteed.

5. **Where do docs/status overstate code?** Workspace ACLs are not universal; PG mirror ordering is not restart-safe; binary-first bridge is partial; no-Docker deployment still has cargo-run paths; LSP cleanup fixed direct orphaning but not protocol/mutex robustness; runtime budget still has run/terminal/MCP scaling caveats; LSP-resolved calls are still not the default agent path.

6. **P0→P3 queue and top moat-per-effort changes?** See the fix queue artifact. Top three moat-per-effort changes: metadata-first top-K drift recall, typed memory/MCP schema with body transport, and persistent IndexEngine daemon with content-addressed cache.

## 11. Checks run

- `git rev-parse --short HEAD` → `422bbaf`.
- `git status --short` → only `?? goal/`.
- JSONL appendix validation → passed via Python `json.loads` for every line.
- `cd api-go && go test ./internal/workspaceops ./cmd/xmustard-mcp` → blocked: Go tried to download `go1.26.0`; container has no network access to `proxy.golang.org`.
- `GOTOOLCHAIN=local go test ...` → blocked: local Go is `go1.23.2`, repo requires `go >= 1.26.0`.
- `cd rust-core && cargo test --quiet` → blocked: `cargo` not installed in the container.

## 12. Current production risk

The project is materially safer than the first audit suggested. State-integrity for governed memory and feedback is much better, cache tearing is fixed, LSP orphaning is mostly closed, and PG connection exhaustion is addressed. But I would not call it production-safe for sustained concurrent agents until terminal route ACLs, run supervision, PG mirror restart ordering, bridge bounds, LSP frame bounds, MCP schemas/body transport, and recall O(top-K) are fixed and tested.
