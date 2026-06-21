# Remaining-work completion loop (verified 2026-06-21)

The genuinely-open items after the deep-graph + IndexEngine loop, each verified
against the real code (one agent per item, evidence-backed). Ordered by
value-per-effort. Same discipline as prior loops: one verified slice per iteration —
read the real code → minimal change → build → `cargo test`/`go test` + a new unit
test → rebuild the release binary if rust changed → live-verify on this repo →
adversarial review workflow on risky slices → commit one slice + push to
`feat/product-v1` → check the box here → next. Keep the 9-tool MCP surface (enrich,
don't add tools). No Python.

- [x] **R1 — Graph-proximity RRF lane** *(medium, best value/effort)*. **DONE.**
  `hybrid_search` now takes `seed: Option<&str>`; resolves an effective seed
  (explicit param, else auto-seed from the top exact query→symbol match), calls
  `symbolgraph::symbol_impact(graph, seed, PROXIMITY_DEPTH=3)`, folds
  distance→`1/(d+1)` onto each `Cand.proximity` (seed's own file = 1.0), and adds a
  4th `"proximity"` RRF lane. Wired `search?seed=` through the Go API
  (`WorkspaceSearch`/`WithFeedback`), the CLI 5th positional arg, and the MCP
  `search` tool's `seed` param (still 9 tools).
  *Tests:* `proximity_lane_reranks_graph_neighbour_up` (seed flips graph-near above
  graph-far), `auto_seed_from_exact_match_activates_proximity` (exact match
  auto-seeds; unrelated symbol gets no proximity credit) — both green.
  *Live (this repo):* query `feedback` + `seed=RecordFeedback` → lane reads
  `lexical+semantic+structural+proximity`, scores 0.048→0.064, `applyFeedbackToHits`
  (same file as seed) rises rank 6→3, distant `WorkspaceSearchWithFeedback` drops out
  of top 6. Pure ranking enrichment; only re-ranks the already-matched pool (never
  widens recall). Committed + pushed.

- [x] **R2 — Contract-break detection** *(medium)*. **DONE.** `repomap.rs:176`'s
  `signature_text` was a declared-but-always-`None` field, so the baseline now
  derives a real signature (`changetrack::symbol_signature`: decl line up to the
  body `{`/`;`/`:`, whitespace-normalized) for every function/method and persists it
  on `IndexBaseline.signatures` (keyed `path\x1fscope\x1fsymbol`, `serde(default)` so
  pre-R2 baselines still load). On a *modified* file, each symbol's current signature
  is diffed vs the baseline; a difference sets `DirtySymbol.contract_break` +
  `signature_change` (`classify_signature_change` → `params N→M, return \`x\`→\`y\``),
  and `ChangeSet.contract_breaks` counts them. Surfaced in `impact` (since-index) and
  `ground` (`working_tree_changes` now loads the baseline; `SessionGrounding`
  exposes `contract_breaks` + `broken_contracts` + the summary line). MCP `impact`
  and `ground` descriptions updated (still 9 tools).
  *Tests:* `contract_break_on_signature_change_not_on_body` (arity change breaks,
  body edit doesn't), `contract_break_surfaces_in_working_tree_changes` (return-type
  change in the grounding view), baseline test asserts a new symbol is *not* a break.
  *Live (temp repo):* `auth(token)->bool` → `auth(token, scope)->Result<bool,String>`
  yields `contract_breaks:1`, `signature_change:"params 1→2, return bool→Result<...>"`;
  a body-only edit yields `0`. Committed + pushed.

- [x] **R3 — Wiki incrementality** *(small–medium)*. **DONE.** Two wins: (1)
  `generate_wiki` now calls `build_symbol_graph_cached` instead of the cold
  `build_symbol_graph` — wiki was the *last* consumer doing a full O(repo) graph
  rebuild on every call; it now rides the S0/S4 warm cache. (2) Per-subsystem page
  cache: each subsystem page is fingerprinted over its sorted files + per-file symbol
  sets (`subsystem_fingerprint`) and cached under `.git/xmustard-cache/wiki-<ws>.json`
  (new `indexcache::{load,store}_wiki_cache_bytes`); on regen, an unchanged
  fingerprint serves the byte-identical cached page, only changed subsystems
  re-render. Render order is sorted so the fingerprint↔bytes mapping is sound; stale
  subsystems drop (cache rebuilt from current slugs). `RepoWiki` now reports
  `regenerated_slugs` / `reused_slugs` for measurability.
  *Tests:* `wiki_regenerates_only_changed_subsystem` (cold→all regen; edit core→
  core regenerates, util reused byte-identical, core page shows the new symbol).
  *Live:* edit only `core/lib.rs` → `regenerated:[overview, subsystem-core]`,
  `reused:[subsystem-util]`; warm no-edit run reuses 4/4 subsystem pages. Committed +
  pushed.

- [x] **R4 — Auth follow-ons (A5)** *(large)*. **DONE.** (a) Global capped
  auth-audit log (`auth_audit.go`): mint/revoke/rotate/denied via `RecordAuthAudit`;
  authMiddleware records 401 + readonly-403 denials, `requireRole` records role-gate
  denials; `GET /api/auth/audit` (admin). (b) Token expiry/rotation: `ExpiresAt` on
  `tokenRecord`, fail-closed `tokenExpired` enforced in `ResolveToken`; `MintTokenTTL`
  + `RotateToken` + `POST /api/auth/tokens/{id}/rotate`. (c) Real role hierarchy
  (`roleRank` admin>agent>readonly) with an explicit `agent` gate on `POST /context`,
  `/context/{id}/verify`, and issue-run launches.
  *Adversarial review (workflow, 27 agents, 16 confirmed/plausible) → fixed:*
  **critical** unsynchronized `agent_tokens.json` writes (added `tokenStoreMu` —
  no more lost-update/revoke-resurrection; `-race` regression test); **high**
  corrupt token file fails *open* (`HasAuthConfigured` now fails closed); **high**
  audit DoS (per-field length clip → bounds file; denied-event throttle → bounds
  write-rate + stops flood evicting mint/revoke history); **medium** huge-TTL int64
  overflow (TTL upper bound); **medium** non-unique EventIDs (atomic counter);
  **low** env-token role typos (normalize unknown→readonly), second-precision expiry
  (now nano). Deferred-as-by-design: env tokens are static (404 on rotate/revoke,
  documented).
  *Tests:* `TestTokenExpiry`, `TestRotateToken`, `TestAuthAuditLog`,
  `TestAuthAuditDeniedThrottle`, `TestAuthAuditFieldClip`,
  `TestConcurrentTokenStoreNoLostUpdate` (`-race`). *Live:* unauth→401, readonly
  POST→403, agent POST→200, rotate works, 30-request flood coalesces to one audit
  entry. Committed + pushed.

- [x] **R5 — Postgres as the write path (C1)** *(large)*. **DONE.** New
  `pg_inline.go`: `saveRunRecord` mirrors the run + its plan into PG (xm_runs + new
  xm_run_plans) and `saveVerificationProfileHistory` re-mirrors verification outcomes,
  both inline on mutation. JSON stays the durable source of truth; the mirror is
  strictly best-effort (gated on `XMUSTARD_PG_DSN`, errors logged+swallowed) so PG can
  never break the JSON write. Read-back via `ListRunPlansPostgres` + `GET /pg/run-plans`.
  *Adversarial review (workflow, 30 agents, 20 confirmed/plausible) → fixed:*
  **async background dispatch** (bounded 8-worker pool + `recover` + `PgInlineFlush`)
  so a slow/black-hole PG never adds latency to the request (was a synchronous 5s/60s
  connect on the handler path); **delete-by-key + insert** instead of
  `ON CONFLICT`/unique-index (a pre-existing duplicate row can no longer permanently
  disable mirroring); **raw nullable pointers** so `completed_at`/`error` are SQL
  `NULL` not `''` (verified live); **schema ensured once per process** (no per-write
  DDL churn); `coalesce(phase)` in the read-back. Accepted-as-design: best-effort
  drift reconciled by `MaterializeOps*`; verification re-materialize is async/off-path.
  *Tests:* `TestPgInlineDisabledByDefault`, `TestSaveRunRecordSucceedsWhenPgDead`,
  `TestSaveRunRecordFastWhenPgBlackHole` (returns <1s while the mirror's connect hangs
  5s), `TestInlineRunPlanQueryableInPg` (live: save run+plan → queryable immediately →
  upsert-in-place on phase change). Live: NULL columns confirmed. Committed + pushed.

- [x] **R6 — Deeper data/control-flow edges** *(large)*. **DONE.** A per-line flow
  pass (`flow_edge_kind` + `identifier_spans` + whole-word `find_word`) classifies
  each referenced symbol's syntactic context into **returns** (`return X`),
  **branches** (`if/while/match/…` condition), and **writes** (`X = …`, `X += …`)
  edges. These land in a NEW `SymbolGraph.flow_edges` field (+ `flow_edge_count`),
  kept SEPARATE from `edges` so authority/impact/proximity (which sum edge weight) are
  unperturbed; tagged `resolution:"lexical"` like S2. Surfaced via `symbolgraph build`
  (full graph) and a focused `symbolgraph flow <root> <ws> [path]` CLI arm.
  *Tests:* `graph_emits_flow_edges` (branches + returns edges on a synthetic file,
  not polluting `edges`), `flow_edge_kind_classifies_contexts` (returns/branches/
  writes/none + whole-word safety). Full rust suite green (98).
  *Live (this repo):* 399 flow edges — branches 239, returns 107, writes 53; e.g.
  `returns: project_info.go → repo_config.go via dedupeStrings`. Committed + pushed.

- [ ] **R7 — Cockpit UI for providers / routing / tokens (A4 remainder)** *(large)*.
  React components + `api.ts` client for: provider management (the backend
  `/api/providers*` exists), task-typed routing rules (`/api/route*`), and auth-token
  management (`/api/auth/*`). tsc + build green.
  *DoD:* an operator can add a provider, set a routing rule, and mint/revoke a token from the UI.

Honest framing to preserve: xMustard already wins on governed memory + drift honesty
+ the 20× warm index + the deep-graph parity (S1–S6). These seven are depth/production
polish; none blocks the agent use-case.
