# Remaining-work completion loop (verified 2026-06-21)

The genuinely-open items after the deep-graph + IndexEngine loop, each verified
against the real code (one agent per item, evidence-backed). Ordered by
value-per-effort. Same discipline as prior loops: one verified slice per iteration —
read the real code → minimal change → build → `cargo test`/`go test` + a new unit
test → rebuild the release binary if rust changed → live-verify on this repo →
adversarial review workflow on risky slices → commit one slice + push to
`feat/product-v1` → check the box here → next. Keep the 9-tool MCP surface (enrich,
don't add tools). No Python.

- [ ] **R1 — Graph-proximity RRF lane** *(medium, best value/effort)*. Thread an
  optional seed symbol into `rust-core/src/search.rs hybrid_search`; call
  `symbolgraph::symbol_impact(graph, seed, max_depth)` for BFS distances; map
  distance→score (e.g. `1/(depth+1)`), store on `Cand`, add a 4th `rank_by`
  "proximity" lane to the RRF loop (`search.rs:260-268`). Wire `search?seed=` (and/or
  auto-seed from the top exact hit).
  *DoD:* a query with a seed re-ranks nearby symbols up; test the lane; live before/after.
  *Evidence it's open:* `search.rs:253-264` fuses only 3 lanes; `symbol_impact` never called from search.

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
