# xMustard — Status Report (2026-06-20)

## 1. What the Product IS Now

xMustard is a **tiny MCP server for governed runtime memory** — its only job is giving coding agents (Claude Code, opencode, codex, etc.) two things: *grounding* ("what changed / what's stale / what's broken since you last touched this") and *memory with a trust lifecycle* (propose → multi-agent verify → promote, with drift-on-recall so memory never goes silently stale). It exposes exactly **8 MCP tools** (ground, recall, remember, verify, search, explain, impact, diagnostics) backed by a Go API shell (`api-go/`) and a Rust semantic core (`rust-core/`). It deliberately does *not* try to be the agent, the search engine, the eval platform, or the security tracker — those are either cut from the agent surface or deferred to a future UI.

---

## 2. DONE — Major Capabilities Built & Verified

### Governed Memory (the moat — all on the 8-tool MCP surface)
- **`remember` / `verify` / `recall`** — propose→pending→multi-agent-verified→promoted pipeline (`context_governance.go`). Verified live: 2-distinct-agent promotion, duplicate-vote rejection, readonly enforcement.
- **Drift-on-recall** — at recall time, each memory's referenced-path hashes are re-checked against the live tree; `stale_count` / `stale_paths` / `stale_memory` flags emitted. Never serves silently-stale memory.
- **Memory conflict surfacing** — `recall` emits `conflicts` (files ≥2 active memories claim) so agents reconcile before trusting.
- **Auth** (`auth.go`) — per-principal bearer tokens (admin/agent/readonly), sha256-hashed at rest, constant-time compare; agent identity in the verification gate comes from the authenticated principal (one token ≠ N fake agents). 7 adversarial issues found and fixed.

### Session Grounding (`ground` — MCP surface)
- `grounding.go` `BuildSessionGrounding` — changed/dirty-symbols/failed-runs, stale-index + sibling-clone drift flag, blocked-by-dirty/failing flags. Backed by `rust-core/changetrack.rs` (317 incorporation events recorded live).

### Semantic Core (partial MCP surface via `search` / `explain` / `impact`)
- **`search`** — hybrid BM25 + hashing-trick embedding + char-trigram fuzzy + Postgres ts_rank RRF. Lexical + structural, narrow, not a dump.
- **`explain`** — file/directory explainer via `rust-core` (purpose, key symbols, run/verify info).
- **`impact`** — dirty symbols → callers/tests blast radius (`rust-core/semantic.rs`).
- **`diagnostics`** — normalized diagnostics for the workspace.
- tree-sitter symbol extraction (Rust/Go/TS/TSX/JS/JSX; regex fallback) backing search + impact.
- ast-grep semantic pattern search (`rust-core/semantic.rs`).
- LSP live sessions (`rust-core/lsp_session.rs`): rust-analyzer/gopls/typescript-language-server/clangd; documentSymbol + hover verified live.
- Semantic repo graph (`rust-core/symbolgraph.rs`): typed edges (imports/calls/inherits/tests/refs), verified live (820 calls / 466 refs / 420 tests).
- Ownership & subsystem model (`rust-core/ownership.rs`): cohesion, likely owners from git history, blast radius.

### Change Tracking (feeds `ground` — MCP surface)
- File/symbol fingerprinting + incorporation-lineage chain (`rust-core/changetrack.rs`). Verified: 317 events.
- Changed-since-baseline + working-tree dirty symbols (not only dirty files).
- Stale-index + sibling-clone drift detection in `/changes/drift` and session-grounding.

### MCP Server (the delivery surface)
- `api-go/cmd/xmustard-mcp` — stdio JSON-RPC 2.0, exactly 8 tools, tested (`main_test.go` asserts the 8-tool set and validates required-param enforcement). Bridges to the Go HTTP API.
- Verified-context injected into agent run prompts (`applyActiveContextToPrompt` on `StartIssueRun` + `StartAgentQuery`).

### Providers & Routing (HTTP-only, NOT on the 8-tool MCP surface)
- OpenAI-compatible provider layer (`openai_providers.go`): Ollama/vLLM/LM Studio/OpenAI + vision VLM. Secrets never stored (env-var name only). SSRF guard, redirect-follow blocked. Verified live against Ollama.
- Task-typed model routing (`provider_router.go`): 6-type taxonomy (locate/code_edit_patch/multi_step_debug_reason/repo_qa_explain/test_gen_validate/vision_ui_diagnose) → provider+model. Verified live.
- `/api/providers*`, `/api/route*` — HTTP-only; no MCP tool for these (correctly not on the agent surface per RETHINK).

### Storage & Persistence
- Postgres store: semantic index (`xm_files`/`xm_symbols`/`xm_edges`) + ops layer (`xm_runs`/`xm_activity`/`xm_issues`) materialized and queryable.
- JSON remains the durable write source; PG is the queryable index.
- Full operational memory layer (issues, runs, plans, verification profiles, eval timelines, threat models, vuln records, browser dumps) built and persisted — but these are HTTP/CLI-only, not on the 8-tool MCP surface (correctly).

### Auth (HTTP-only; feeds MCP gate identity)
- Bearer-token auth with role-based access (admin/agent/readonly). Non-loopback bind requires TLS or explicit override. Auth events flow into the multi-agent verification gate.

### UI / Cockpit (HTTP-only, secondary consumer)
- `frontend/` React: cockpit (`Cockpit.tsx`), kanban (`KanbanBoard.tsx`), intelligence inspector. Exists but not the primary product surface per RETHINK.

---

## 3. NOT DONE / Remaining Gaps

### RETHINK Steps 4–5 (explicitly open)
- **Step 4 — Retire materialized static index as default.** `pgindex.go` + `xm_files`/`xm_symbols` Postgres tables still exist as the primary search path. Agents should explore live; the static index should be a fallback, not the source of truth. No code change yet.
- **Step 5 — Deprecate goal/swarm + ops/eval/security platform surfaces.** The HTTP API still exposes the full ~120-route surface (eval timelines, security disposition, goal/swarm runtime, ops materialization). The agent surface is clean (8 tools), but the HTTP surface is not cleaned up. `RETHINK.md` defers this to "not gutting without explicit call" but it is still open weight.

### Routing as Run-Execution Runtime (ROADMAP A1)
- Provider routing is a decision/dispatch layer but not a first-class run-execution runtime. Full coding runs still dispatch to the codex/opencode CLIs. `provider:<name>` runtime branch and managed-run execution path recording a `runRecord` from `RouteAndChat` not built.

### Neural Embeddings + Graph-Proximity RRF Lane (ROADMAP A2)
- Search uses a model-free hashing-trick embedding. No neural embeddings via provider `/embeddings` endpoint. No graph-proximity lane in RRF fusion. `search.rs`/`SearchPostgres` RRF stops at lexical + structural.

### Failure Explainers (ROADMAP A3)
- "Why a failure happened" (correlate run output + diagnostics + changed symbols) not implemented. Contract-break detection in impact analysis not implemented.

### Cockpit Integration of Governance + Providers (ROADMAP A4)
- React cockpit does not surface context governance (propose/verify/active), provider management, task-typed routing, or token management. HTTP API exists; UI wiring is not done.

### Auth Follow-ons (ROADMAP A5)
- No audit log of auth events (mint/revoke/denied). No token expiry/rotation. No finer per-endpoint authz beyond the admin/agent/readonly split.

### Postgres Write Path for Ops (ROADMAP C1)
- `run_plans` and `verification_*` still JSON-only; PG write path not implemented for these.

### LSP impl/type/rename (ROADMAP C2)
- `lsp_session.rs` supports documentSymbol + hover only. `textDocument/implementation`, `typeDefinition`, `rename` not built.

### Enclosing-Scope Context (ROADMAP C3)
- `RustPathSymbolRecord.enclosing_scope` always `None`. Tree-sitter parent-chain walk not implemented.

### Stale Docs
- `PLANNED_FEATURES.md` still describes the "three-surface cockpit" and "27 tools → 31 tools" framing that predates the RETHINK collapse to 8. It says "75–85% backend capability" against the old vision — that estimate is misleading against the current governed-memory thesis.
- `README.md` still frames xMustard as a "repo cockpit + operational-memory tool" with no mention of the 8-tool MCP surface or the RETHINK direction.
- `docs/ROADMAP.md` splits work into "codex track / Claude track" — accurate but the A1–A5/C1–C4 items are not cross-referenced to the RETHINK steps.

---

## 4. Completion Estimate Against the RETHINK Thesis

**~75% complete** against the governed-memory product.

The core moat (propose → verify → promote → drift-on-recall → conflict-surfacing) is delivered and sharp. The 8-tool MCP surface is live and tested. Auth is hardened. Grounding is real. The gaps are: static index retirement (step 4), HTTP surface cleanup (step 5), neural search upgrade, failure explainers, and cockpit wiring for governance — none of which block the primary agent use-case, but all of which matter for production readiness.

---

## 5. Top 5 Highest-Leverage Next Tasks (Ranked)

1. **Update README.md and PLANNED_FEATURES.md to reflect RETHINK** — Stale docs are the highest-risk item: anyone reading them (including future Claude sessions) gets the wrong picture. `README.md` should describe the 8-tool governed-memory server; `PLANNED_FEATURES.md` should be reconciled or superseded by `RETHINK.md`. No code change required; pure alignment work.

2. **Retire materialized static index as default (RETHINK step 4)** — Make `search` call live tree-sitter/ast-grep exploration first, falling back to the PG index only as a cache. Closes the biggest architectural contradiction in the product: RETHINK says "active exploration beats static indexing" but search still queries a static Postgres index as the primary path. Impact: search quality + eliminates drift between index and live tree.

3. **HTTP surface cleanup (RETHINK step 5)** — The 8-tool MCP surface is clean, but the Go API still wires ~120 routes including eval/swarm/goal/security/ops platform endpoints. Move non-memory, non-grounding routes behind a `X-Feature` flag or a separate `api-go/cmd/xmustard-platform` binary so the MCP-backed paths stay sharp and auditable. Reduces attack surface and cognitive load.

4. **`recall` / `ground` integration test against a live workspace with real file mutations** — The drift-on-recall and conflict-surfacing logic exists and is unit-tested, but there is no end-to-end test that promotes a memory, mutates the referenced file, recalls, and asserts `stale=true`. This is the product's core invariant; it needs an integration test before shipping to real agents.

5. **Routing as a first-class run-execution runtime (ROADMAP A1)** — Provider routing exists as a dispatch decision but runs still shell out to codex/opencode CLIs. Adding a `provider:<name>` runtime branch to `validateRuntimeModel`/`buildRuntimeCommand` lets the tool drive any OpenAI-compatible model directly — making the provider layer actually close the loop and removing the CLI dependency for non-local runtimes.
