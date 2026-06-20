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

- [ ] **R2 — Contract-break detection** *(medium)*. Persist `signature_text`
  (already extracted at `repomap.rs:176`) into the change-tracking baseline; on change
  detection, diff baseline vs current signature per symbol; emit a `contract_break`
  flag on changed symbols (added/removed params, changed return type). Surface in
  `impact` (and `ground`).
  *DoD:* changing a function signature flags a contract break; unchanged body does not; tested + live.

- [ ] **R3 — Wiki incrementality** *(small–medium)*. `wiki.rs generate_wiki` always
  full-rebuilds (`wiki.rs:56`). Reuse the per-file symbol cache (S4) / the warm graph
  cache so wiki regenerates only the affected subsystem pages on change.
  *DoD:* editing one file regenerates only its page(s), not the whole wiki; measured.

- [ ] **R4 — Auth follow-ons (A5)** *(large)*. (a) Audit log: have mint/revoke and
  authMiddleware-denial call `RecordAuditEvent` (mint/revoke/denied). (b) Token
  expiry/rotation: add `ExpiresAt` to `tokenRecord`, enforce in `ResolveToken`, add a
  rotate endpoint. (c) Finer authz: a real `agent`-role gate distinct from `readonly`
  on the right routes.
  *DoD:* a denied request appears in the audit log; an expired token is rejected; tested.

- [ ] **R5 — Postgres as the write path (C1)** *(large)*. Make `run_plans` and
  `verification_*` write to Postgres inline on mutation (not just the one-shot
  `pgops.go`/`pgverify.go` mirrors), keeping JSON as a fallback/export. Schema +
  inline INSERT/UPDATE on `saveRunRecord` / verification saves.
  *DoD:* a run plan / verification mutation is queryable in PG immediately, no manual materialize; tested.

- [ ] **R6 — Deeper data/control-flow edges** *(large)*. Add edge kinds beyond
  imports/calls/inherits/tests/references — e.g. returns / reads / writes / branches —
  via tree-sitter (and/or LSP) analysis per symbol, with new classification logic in
  `reference_edge_kind`. Tag resolution like S2.
  *DoD:* at least one new flow edge kind is emitted and verified on a synthetic case.

- [ ] **R7 — Cockpit UI for providers / routing / tokens (A4 remainder)** *(large)*.
  React components + `api.ts` client for: provider management (the backend
  `/api/providers*` exists), task-typed routing rules (`/api/route*`), and auth-token
  management (`/api/auth/*`). tsc + build green.
  *DoD:* an operator can add a provider, set a routing rule, and mint/revoke a token from the UI.

Honest framing to preserve: xMustard already wins on governed memory + drift honesty
+ the 20× warm index + the deep-graph parity (S1–S6). These seven are depth/production
polish; none blocks the agent use-case.
