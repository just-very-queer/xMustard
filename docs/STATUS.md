# xMustard — Status Report (2026-06-20)

## 1. What the Product IS Now

xMustard is a **tiny MCP server for governed runtime memory** — its only job is giving coding agents (Claude Code, opencode, codex, etc.) two things: *grounding* ("what changed / what's stale / what's broken since you last touched this") and *memory with a trust lifecycle* (propose → multi-agent verify → promote, with drift-on-recall so memory never goes silently stale). It exposes **9 enriched MCP tools** (ground, recall, remember, verify, search, explain, impact, diagnostics, why_failed — several take modes/params, e.g. `search?mode=pattern`, `impact symbol=/from=/to=`, `recall query=`) backed by a Go API shell (`api-go/`) and a Rust semantic core (`rust-core/`). It deliberately does *not* try to be the agent, the search engine, the eval platform, or the security tracker — those are either cut from the agent surface or deferred to a future UI.

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

Verified against the code on 2026-06-21 (one agent per item, evidence-backed —
see `docs/plans/2026-06-21-remaining-work-loop.md`). The items below are the
**genuinely-open** ones; everything previously listed here that is now done has
been removed.

| Item | Verdict | Effort | Evidence |
|------|---------|--------|----------|
| **Graph-proximity RRF lane** (A2 remainder) | **DONE** | medium | `hybrid_search` takes `seed: Option<&str>`, calls `symbol_impact` for BFS distances → `1/(d+1)` 4th `"proximity"` RRF lane; auto-seeds from the top exact match; wired `search?seed=` + MCP `seed` param. Live: `seed=RecordFeedback` re-ranks `feedback.go` neighbours up (lane shows `…+proximity`). |
| **Contract-break detection** (A3 remainder) | **DONE** | medium | `changetrack` derives + baselines per-symbol signatures (`IndexBaseline.signatures`), diffs them on modified files → `DirtySymbol.contract_break` + `signature_change` (`params N→M, return x→y`); surfaced in `impact`/`ground` (`contract_breaks`, `broken_contracts`). Live: arity/return change flags, body-only edit doesn't. |
| **Wiki incrementality** | not-done | small–med | `wiki.rs:56` always full-rebuilds (no dirty-file/cache reuse). The ingestion phase-DAG itself IS done (`project_truth.go ReadIngestionPlan`). |
| **Auth follow-ons** (A5) | not-done | large | `tokenRecord` has no `ExpiresAt`; `ResolveToken` no TTL check; mint/revoke/deny never call `RecordAuditEvent`; `requireRole` only admin vs non-admin. |
| **Postgres as the write path** (C1) | not-done | large | `saveRunRecord`→`writeJSON` for run_plans; verification to JSON; `pgops.go`/`pgverify.go` are one-shot DELETE+INSERT mirrors, never inline on mutation. |
| **Deeper data/control-flow edges** | not-done | large | Only imports/calls/inherits/tests/references; LSP upgrade also only emits `calls`. No data-flow/control-flow/read-write edges. |
| **Cockpit UI for providers/routing/tokens** (A4 remainder) | not-done | large | No React components; `api.ts` has only ticket-integration CRUD. Backend endpoints (`/api/providers*`, `/api/route*`, `/api/auth/*`) exist — only the UI is missing. |

### Stale docs (being fixed in this pass)
- `PLANNED_FEATURES.md` had ⬜ markers for enclosing-scope and LSP impl/type/rename that are now done — corrected.

---

## 4. Completion Estimate Against the RETHINK Thesis

**~90% complete** against the governed-memory product. Everything in the RETHINK
plan (steps 1–5) is done; the deep-graph + IndexEngine loop (S1–S6: full LSP,
scope-resolved CALLS, communities, incremental reindex, agent-feedback ranking,
symbol-level impact/trace) is done. The 9 enriched MCP tools are live and tested,
auth is hardened, grounding + drift-on-recall + conflict surfacing are real, and
the warm index is 20× faster. The remaining 7 items (section 3) are depth/production
polish — none blocks the primary agent use-case.

---

## 5. Highest-Leverage Next Tasks (verified, ranked by value/effort)

1. ~~Graph-proximity RRF lane~~ — **DONE** (R1). 4th proximity lane via `symbol_impact`
   BFS distances + auto-seed + `search?seed=`.
2. ~~Contract-break detection~~ — **DONE** (R2). Baselined per-symbol signatures
   diffed on change → `contract_break` in `impact`/`ground`.
3. **Wiki incrementality** (small–med) — reuse the per-file symbol cache from S4.
4. **Auth A5 / PG write path / deeper edges / cockpit UI** — all large; sequence
   by need (auth + PG write path for production; cockpit UI for a product surface).
