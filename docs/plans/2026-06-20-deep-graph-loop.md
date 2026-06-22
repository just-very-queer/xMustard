# Deep-graph + IndexEngine completion loop

The remaining "hard" work after the moat (governed memory + drift) and the 20× warm
index. Each slice is independently shippable: build → `cargo test` / `go test` →
live-verify on this repo → commit → push → next. One slice per iteration; never
batch unverified work. Keep the 8–9-tool MCP surface — enrich tools, don't add them.

Checklist (in order; check off as committed):

- [x] **S1 — Full LSP requests.** Extended `rust-core/src/lsp_session.rs` with
  `references`/`definition`/`implementation`/`typeDefinition`/`rename` (shared
  `position_request` helper; CLI arms `lsp-references|definition|implementation|
  type-definition|rename`). Graceful Unavailable when no server. Verified live via
  tsserver: references found 3, definition → LocationLink, rename → WorkspaceEdit.

- [x] **S2 — Scope-resolved CALLS edges.** Fixed first-definer-wins (`name_to_defs`
  Vec + `unique_definer`: an ambiguous name no longer misroutes — tested). Every edge
  carries `resolution` ("lexical"/"lsp"). New `LspWorkspaceSession` (persistent
  spawn, lazy didOpen, batched references) + `upgrade_graph_with_lsp` (budgeted,
  hotspots-first, pre-opens the project, path-canonicalized) produces real `calls`
  edges tagged `lsp` via `symbolgraph build-lsp`. Default search path is untouched
  (opt-in). Verified live (tsserver): `a.ts→util.ts [calls] via computeTotal`,
  `b.ts→util.ts`, both resolution=lsp.

- [x] **S3 — Communities / clusters.** `compute_clusters` does label-propagation
  over the reference-edge graph → file communities that cross directory boundaries
  (deterministic; `FileCluster{cluster_id,label,files,size}`). `symbolgraph clusters`
  CLI + Go `WorkspaceClusters`/`PathCluster`; `/clusters` endpoint and `explain` is
  enriched with the file's cluster. Tested (clusters_span_directories: cross-dir
  coupled files cluster, unrelated stays out). Live on this repo: 4 clusters; explain
  attaches `cluster #0 [api-go]`.

- [x] **S4 — IndexEngine Phase 1: incremental reindex.** Per-file symbol cache
  (`symbols-{ws}.json`, keyed by content sha256) so a rebuild re-parses ONLY files
  whose hash changed and reuses cached symbols for the rest (tree-sitter parse is
  the dominant cost). Authority (inbound reference weight) precomputed onto each
  `GraphFileNode` at index time. Measured: cold 1.32s → 1-file-edit reindex 0.08s
  (16×); cached build == fresh (tested); authority verification.go=220 live.

- [x] **S5 — IndexEngine Phase 2: agent-feedback layer.** `feedback.go`:
  `agent_feedback.json` segment (`path → retrieval/verify/run_success/run_fail +
  last_used`). Written from: search (records the returned paths), verify/propose
  (boosts a memory's paths on promotion), failure explainer (suppresses a failed
  run's implicated paths). `feedbackBoosts` fuses with recency decay (~30-day
  half-life, tanh-squashed); `WorkspaceSearchWithFeedback` re-ranks the default
  search (+0.1·boost). Tested + verified live: verify a memory about auth.go →
  later "auth" search boosts auth.go symbols (reason `· feedback`).

- [x] **S6 — IndexEngine Phase 3: symbol-level adjacency for impact.** `symbol_impact`
  does bounded BFS over the precomputed reference graph from a symbol's defining
  file(s) → every transitively-dependent file with its distance (true blast radius,
  not just dirty symbols). `trace_symbols` does multi-source BFS for the shortest
  dependency path between two symbols. The `impact` MCP tool now takes `symbol=`
  (blast radius) and `from=&to=` (trace) — same tool, enriched. Verified live via
  MCP: `impact symbol=RecordFeedback` → 109 impacted files; `impact from=ProposeContext
  to=WorkspaceSearch` → path of length 2. 91 rust tests; clippy clean.

**All six slices done.** The deep-graph gap vs GitNexus is closed at the agent
surface (real CALLS via LSP, communities, incremental index, agent-feedback ranking,
symbol-level impact + trace) while keeping the 9-tool MCP surface (enriched, not
expanded) and the unique governed-memory + drift-honesty moat.

Working method per slice (the loop):
1. Read the real code; if scope is unclear, run a short scout workflow.
2. Implement the minimal change; build.
3. `cargo test` (rust-core) and/or `go test ./...`; add a unit test for the slice.
4. Rebuild the release binary if rust changed; live-verify on this repo.
5. For risky slices, run an adversarial review workflow and fix confirmed findings.
6. Commit (one slice) + push to `feat/product-v1`; check the box here.
7. Continue to the next slice until all are done.

Honest framing to preserve: xMustard wins on governed memory + drift honesty + the
20× warm index; these slices close the deep-graph gap vs GitNexus (real CALLS,
clusters, incremental index, feedback, symbol-level impact) without becoming a
16-tool graph platform.
