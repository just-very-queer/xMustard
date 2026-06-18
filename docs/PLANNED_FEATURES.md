# xMustard — Full Planned Feature List (Three-Surface Vision)

Consolidated from `REPO_COCKPIT_TOOL_ARCHITECTURE.md`, `GITNEXUS_EXTRACTION_MAP.md`,
`PLANNING.md`, `FRONTIER.md`, `MIGRATION_RUST_GO.md`. Status is grounded in what is
actually implemented (verified June 2026).

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
- ✅ Terminal transport (PTY)
- 🟡 Managed execution: structured run state, bounded output capture, failure provenance, durable summaries, cancellation/timeout (Rust process runner is the next Python cut)

### C. Semantic intelligence core ("Missing Entirely" / Upgrade — PARTIAL)
- ✅ Structural repo map; 🟡 symbol-aware code maps + enclosing-scope context
- 🟡 tree-sitter symbol extraction (`rust-core` path-symbols)
- ✅ **ast-grep semantic pattern search** (`rust-core/semantic.rs`)
- 🟡 LSP layer (`rust-core/lsp.rs` normalizes definitions/references/document+workspace symbols/diagnostics) — needs live session mgmt + hover/impl/type/rename
- 🟡 Impact analysis (`rust-core` semantic-impact: changed symbols → callers/tests); ⬜ contract-break detection
- 🟡 Code/subsystem explainers (`rust-core` explain-path); ⬜ "why a failure happened"
- ⬜ Semantic repo graph (symbols/files/callers/callees/imports/inheritance/tests↔code/issue↔symbol edges)
- ⬜ Session grounding ("what changed since this thread last touched the repo / what's broken / blocked")
- ⬜ Ownership & subsystem model (subsystem map, clusters, hotspots, likely owners, blast radius)

### D. Knowledge layer (storage + retrieval — PLANNED)
- ⬜ **PostgreSQL as primary store** (semantic tables: files/symbols/file_symbol_summaries/semantic_queries/semantic_matches/diagnostics; ops tables: activity_events/run_records/run_plans/run_plan_revisions/verification_*/issue_artifacts). Currently JSON files; `api-go` has postgres plan/bootstrap only.
- ⬜ Hybrid search: BM25/FTS + structural + graph-proximity + optional embeddings, fused (RRF), with explicit exact-scan fallback
- ⬜ Wiki generation (graph-backed, incremental, review-first) — after semantic core is trustworthy

### E. Runtime + project discovery
- ✅ Entrypoints / run / build / test / lint targets, config files, manifests (`api-go` project_info/project_targets); 🟡 env vars, docker/compose services, process/service graph

---

## Surface 2 — MCP context engine (cross-agent memory)

The "better than markdown" shared memory layer. **Largely PLANNED as MCP.**
- ⬜ **MCP server** exposing the core tool surface: `repo_state`, `repo_summary`, `changed_since`, `definitions`, `references`, `diagnostics`, `impact`, `run_targets`, `verify_targets`, `issue_context_packet`, `recent_failures`, `code_explainer`, `subsystem_explainer`
- 🟡 One backend serving CLI + HTTP from the same contracts (CLI = `xmustard-ops`/`xmustard-core`; HTTP = `api-go` with ~150 routes incl. most of the above as REST) — **MCP transport is the missing third delivery**
- 🟡 Durable cross-agent memory store (the goal/swarm runtime + operational memory IS the durable store; needs MCP exposure + an agent "re-check on reconnect" protocol)
- ✅ User preferences from markdown (`.xmustard.yaml` path instructions, `AGENTS.md`/microagent guidance discovery + health + starter generation)

---

## Surface 3 — Web UI (Linear-like kanban for runtime)

- ✅ Current panes: issues / runs / signals / review / execution drawer / goal panel
- ⬜ **Reframe IA** from tracker (issues/runs/signals/review) → cockpit (**repo state / change state / execution state / verification state / risk state / intelligence inspector**)
- ⬜ Kanban-style *runtime* board (today is queue/list, not a Linear-like board)
- 🟡 Intelligence inspector (retrieval ledger "why this symbol/file/test entered context" exists; needs a first-class inspector pane)

---

## Cross-cutting — gitnexus-style change tracking & shared contracts

- 🟡 File/symbol fingerprinting (`rust-core` scanner fingerprints; diagnostics `raw_payload_sha256`) → ⬜ full per-change hashing + incorporation lineage
- ⬜ Change intelligence: changed-since-last-run / since-accepted-fix / since-last-agent-touch; **dirty symbols, not only dirty files**
- ⬜ Index baseline: stored indexed head SHA + repo fingerprint; **stale-index + sibling-clone drift** warnings surfaced in repo-state/context packets
- 🟡 Ingestion pipeline *phases* (repo scan → manifest/runtime → tree-sitter → ast-grep → LSP → search materialization → change/impact materialization) — ingestion-plan exists; explicit phase graph needed
- 🟡 Shared contract layer across Python/Go/Rust/MCP (`rust-core/contracts.rs`); ✅ Python backend retired → Go (api-go) + Rust (rust-core)

---

## External context source (not in this repo)
- `/Users/for_home/Developer/cursor` — a separate project on the home machine used to read this project into agent context **over Tailscale**. Lives on the `for_home` host; not present on this (`for_work`) machine — which is also why every loaded workspace's `root_path` points at `/Users/for_home/...` and won't resolve here.

---

## Honest bottom line
- The **operational-memory half of the moat is largely built and tested** (Surface 1.A + governance/security/eval), and the **Python→Go/Rust migration is done**.
- The **semantic brain (1.C/1.D), the MCP surface (Surface 2), the cockpit UI reframe (Surface 3), and gitnexus-style change-tracking/Postgres are the big remaining build** — they are what turns this from "tracker with good memory" into the repo-cockpit + cross-agent context engine the docs describe.
- Per `PLANNING.md`: ~75–85% backend capability, ~50–60% frontend, ~60–70% overall — but that estimate is for the *tracker* product; against the *full three-surface cockpit* vision, the semantic/MCP/change-tracking core is closer to ~25–35% done.
