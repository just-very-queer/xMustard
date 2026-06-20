> **Superseded — read [`RETHINK.md`](RETHINK.md) and [`STATUS.md`](STATUS.md) first.**
> The product is now a tiny governed-runtime-memory MCP server (8 tools), not the
> three-surface cockpit this file describes. The percentages and tool counts below
> reflect the older vision and are kept for history. Current status lives in
> `STATUS.md`; current direction in `RETHINK.md`.

# xMustard — Full Planned Feature List (Three-Surface Vision)

Consolidated from `REPO_COCKPIT_TOOL_ARCHITECTURE.md`, `GITNEXUS_EXTRACTION_MAP.md`,
`PLANNING.md`, `FRONTIER.md`, `MIGRATION_RUST_GO.md`. Status is grounded in what is
actually implemented (verified June 2026). As of this pass all three surfaces are
built and verified end-to-end: the tool (semantic core incl. tree-sitter, **typed
graph edges**, **live LSP sessions**, change tracking, ownership), the MCP context
engine (27 tools), the web cockpit/kanban, and a Postgres store holding both the
semantic index and the ops layer with **RRF hybrid search + a model-free embedding
lane**. The four depth upgrades — typed graph edges, embeddings/RRF fusion, LSP
live sessions, ops-tables→Postgres — are now done. Remaining work is finer depth
(neural embeddings, graph-proximity retrieval, persistent LSP sessions with
impl/type/rename, making Postgres the write path for run_plans/verification_*).

The product is **not** a tracker. It is a **repo cockpit + runtime memory engine**
with three delivery surfaces over a shared semantic + operational core:

1. **The tool** — connects to codex/opencode/others, indexes the repo, and keeps
   durable *runtime memory* (runs, plans, fixes, verification, review, issues).
2. **The MCP context engine** — exposes that memory + repo intelligence to agents
   so they share state across agents/modules instead of markdown; an agent that
   reconnects re-checks state through it. Preferences come from markdown.
3. **The web UI** — a Linear-like kanban tracker for *runtime* state.

All of it sits on **gitnexus-style change tracking**: every file/symbol change is
hashed, fingerprinted, and incorporation-tracked, with staleness + sibling-clone
drift detection.

Legend: ✅ built · 🟡 partial/foundation · ⬜ planned (little/no code)

---

## Surface 1 — The Tool: runtime memory + repo intelligence engine

### A. Operational memory (xMustard's moat — mostly BUILT)
- ✅ Issue context packets (tree focus, related paths, evidence, guidance, repo map, symbol context, prompt)
- ✅ Verification profiles + replayable runs + checklists + confidence + per-runtime/model/branch reports
- ✅ Run records, metrics, workspace cost aggregation
- ✅ Planning-gated runs (generate/approve/reject); 🟡 full plan ownership + versioned revisions + git-context-per-revision
- ✅ Activity ledger
- ✅ Eval/replay harness: scenarios, baseline comparison, variant rollups, fresh-replay rankings, **multi-batch timelines** with movement
- ✅ Ticket context + acceptance criteria; 🟡 deeper inbound Jira/Linear ingestion (basic ingest built)
- ✅ Threat models; ✅ browser dumps; ✅ vulnerability records + **security disposition** (exploitability/suppression/risk-acceptance) + **security acceptance criteria**
- ✅ Audit log; ✅ policy gates (allowed runtimes / required profiles / budget / sensitive)
- ✅ Agent identity registry + ownership history; ✅ run confidence + run briefs; ✅ review packets

### B. Runtime layer (connects codex/opencode/others)
- ✅ Runtime abstraction: codex + opencode probe, model validation, capabilities
- ✅ **OpenAI-compatible providers** (`openai_providers.go`): Ollama / vLLM / LM Studio / OpenAI and **vision (VLM)** models via the OpenAI `/v1` schema — provider CRUD, `/models`, probe, `/chat/completions` with image content parts. Secrets never stored (only the env-var NAME). `/api/providers*` + MCP `provider_chat`. Verified live against a running Ollama (`/v1/models`, probe ok).
- ✅ **Task-typed model routing** (`provider_router.go`): classifies a request into a coding sub-task (locate / code_edit_patch / multi_step_debug_reason / repo_qa_explain / test_gen_validate / vision_ui_diagnose — taxonomy grounded in SWE-agent/Agentless/RepoGraph/CodeRAG-Bench) → resolves to a provider+model by explicit rule or capability (vision→supports_vision provider, code→coder model). `/api/route`, `/api/route/chat`, `/api/routes` + MCP `route_model`. Verified live (code→coder, vision→vision provider). ⬜ wire as a first-class run-execution runtime (currently runs still dispatch to codex/opencode CLIs)
- ✅ Terminal transport (PTY)
- 🟡 Managed execution: structured run state, bounded output capture, failure provenance, durable summaries, cancellation/timeout (Rust process runner is the next Python cut)

### C. Semantic intelligence core (Upgrade — MOSTLY BUILT)
- ✅ Structural repo map; ✅ symbol-aware code maps (tree-sitter backed) + ✅ enclosing-scope context (`treesitter.rs` walks the parent chain → impl/class/trait/mod)
- ✅ tree-sitter symbol extraction (`rust-core/treesitter.rs`, primary engine for Rust/Go/TS/TSX/JS/JSX; regex fallback) — feeds path-symbols, the symbol graph, ownership edges, and the Postgres index
- ✅ **ast-grep semantic pattern search** (`rust-core/semantic.rs`)
- ✅ LSP **live sessions** (`rust-core/lsp_session.rs`: spawns rust-analyzer/gopls/typescript-language-server/clangd, runs the initialize/didOpen handshake over stdio JSON-RPC, returns documentSymbol/hover/references/definition/implementation/typeDefinition/rename; persistent `LspWorkspaceSession` for batched references; normalized via `lsp.rs`; graceful when a server isn't installed). `/lsp/document-symbols` + MCP `lsp_document_symbols`; verified live (tsserver, clangd). LSP-resolved CALLS edges feed the symbol graph (`symbolgraph build-lsp`).
- 🟡 Impact analysis (`rust-core` semantic-impact: changed symbols → callers/tests); ⬜ contract-break detection
- 🟡 Code/subsystem explainers (`rust-core` explain-path); ⬜ "why a failure happened"
- ✅ Semantic repo graph (`rust-core/symbolgraph.rs`: files/symbols + **typed edges** imports/calls/inherits/tests/references, tree-sitter backed, + hotspots + blast radius; `issue_symbol_edges.go` adds issue↔symbol mentions/evidence edges via MCP `issue_symbol_edges`). Verified live: calls 820 / refs 466 / tests 420 / imports 107. ⬜ deeper data/control-flow edges
- ✅ Session grounding (`grounding.go` BuildSessionGrounding + MCP `session_grounding` + cockpit banner: changed/dirty-symbols/failed-runs, blocked-by-dirty/failing flags)
- ✅ Ownership & subsystem model (`rust-core/ownership.rs`: subsystem clusters + cohesion, likely owners from git history, blast radius; surfaced in cockpit + MCP `subsystems`/`owners`)

### D. Knowledge layer (storage + retrieval — PARTIAL)
- ✅ **PostgreSQL store** — semantic index (`xm_files`/`xm_symbols`/`xm_edges`, `pgindex.go`) AND the ops layer (`xm_runs`/`xm_activity`/`xm_issues`, `pgops.go`: `/pg/ops/materialize`, `/pg/runs`, `/pg/issues/search`, MCP `pg_runs`/`pg_issue_search`) are materialized + queried in Postgres. JSON remains the durable write source; PG is the queryable index. ⬜ make PG the write path (run_plans/verification_* still JSON-only)
- ✅ Hybrid search with **Reciprocal Rank Fusion**: in-process `rust-core/search.rs` fuses lexical (BM25 idf) + semantic (model-free hashing-trick embedding, char-trigram fuzzy) + structural lanes; Postgres `SearchPostgres` fuses ts_rank + inbound-edge lanes via window-function RRF. ⬜ graph-proximity lane + neural embeddings
- ✅ Wiki generation (`rust-core/wiki.rs` generate_wiki: overview + per-subsystem pages from the symbol graph; MCP `wiki`); ⬜ incremental/review-first refinement

### E. Runtime + project discovery
- ✅ Entrypoints / run / build / test / lint targets, config files, manifests (`api-go` project_info/project_targets); 🟡 env vars, docker/compose services, process/service graph

---

## Surface 2 — MCP context engine (cross-agent memory)

The "better than markdown" shared memory layer. **BUILT — MCP stdio server live.**
- ✅ **MCP server** (`api-go/cmd/xmustard-mcp`, stdio JSON-RPC 2.0 bridging to the HTTP API) exposing 31 typed tools — the 27 above plus the context-governance + provider tools: `context_active`, `context_propose`, `context_verify`, `provider_chat` (all verified end-to-end live)
- ✅ One backend serving CLI + HTTP + **MCP** from the same contracts (CLI = `xmustard-ops`/`xmustard-core`; HTTP = `api-go`; MCP = `xmustard-mcp` over the same REST surface) — the third delivery is now built
- ✅ Durable cross-agent memory store (goal/swarm runtime + operational memory) exposed over MCP; `session_grounding` provides the agent "re-check on reconnect" signal
- ✅ **Context governance** (`context_governance.go`): the trust layer for shared context. Agents PROPOSE entries (permission `readonly`/`readwrite`); an entry is promoted into the active shared context only once ≥N **distinct** agents verify it; a `require_multi_agent_verification` toggle (+ per-proposal override) runs it through multiple agents or not; readonly+verified entries reject edits. `/context*` + MCP `context_propose`/`context_verify`/`context_active`. Verified live (HTTP+MCP): 2-distinct-agent promotion, duplicate-vote rejection, readonly enforcement. The verified active context is now **injected into agent run prompts** (`applyActiveContextToPrompt` on both `StartIssueRun` and `StartAgentQuery`), so every run grounds on the same multi-agent-approved facts — the "context consensus before action" layer the market scout found unaddressed elsewhere (see `docs/research/`).
- ✅ User preferences from markdown (`.xmustard.yaml` path instructions, `AGENTS.md`/microagent guidance discovery + health + starter generation)

---

## Surface 3 — Web UI (Linear-like kanban for runtime)

- ✅ Current panes: issues / runs / signals / review / execution drawer / goal panel
- ✅ **Reframe IA** → cockpit (`frontend/src/components/Cockpit.tsx`: risk state / change state / hotspots / session-grounding banner / subsystems-cohesion / blast-radius + ownership/lineage inspectors)
- ✅ Kanban-style *runtime* board (`frontend/src/components/KanbanBoard.tsx`, Linear-like columns)
- ✅ Intelligence inspector (first-class cockpit panes: blast radius by symbol, ownership + incorporation lineage by path)

---

## Cross-cutting — gitnexus-style change tracking & shared contracts

- ✅ File/symbol fingerprinting + full per-change hashing + incorporation lineage (`rust-core/changetrack.rs`: RepoFingerprint, IndexBaseline, IncorporationEvent append-only chain; `/incorporate` + `/lineage`; verified live: 317 events recorded, lineage replayable)
- ✅ Change intelligence: changed-since-baseline + working-tree changes with **dirty symbols, not only dirty files** (`changetrack.rs` working_tree_changes / changed_since_baseline; `/changes`, MCP `changed_since`)
- ✅ Index baseline: stored indexed head SHA + repo fingerprint; **stale-index + sibling-clone drift** surfaced in `/changes/drift`, session-grounding, and the cockpit banner (verified live: stale=true detection)
- 🟡 Ingestion pipeline *phases* (repo scan → manifest/runtime → ✅ tree-sitter → ✅ ast-grep → ✅ LSP live → ✅ search materialization → ✅ change/impact materialization) — stages built; explicit orchestrated phase graph still needed
- 🟡 Shared contract layer across Python/Go/Rust/MCP (`rust-core/contracts.rs`); ✅ Python backend retired → Go (api-go) + Rust (rust-core)

---

## External context source (not in this repo)
- `/Users/for_home/Developer/cursor` — a separate project on the home machine used to read this project into agent context **over Tailscale**. Lives on the `for_home` host; not present on this (`for_work`) machine — which is also why every loaded workspace's `root_path` points at `/Users/for_home/...` and won't resolve here.

---

## Honest bottom line
- The **operational-memory half of the moat is largely built and tested** (Surface 1.A + governance/security/eval), and the **Python→Go/Rust migration is done**.
- The **semantic brain (1.C/1.D), the MCP surface (Surface 2), the cockpit UI reframe (Surface 3), and gitnexus-style change-tracking/Postgres are the big remaining build** — they are what turns this from "tracker with good memory" into the repo-cockpit + cross-agent context engine the docs describe.
- Per `PLANNING.md`: ~75–85% backend capability, ~50–60% frontend, ~60–70% overall — but that estimate is for the *tracker* product; against the *full three-surface cockpit* vision, the semantic/MCP/change-tracking core is closer to ~25–35% done.
