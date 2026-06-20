# Deep-graph + IndexEngine completion loop

The remaining "hard" work after the moat (governed memory + drift) and the 20× warm
index. Each slice is independently shippable: build → `cargo test` / `go test` →
live-verify on this repo → commit → push → next. One slice per iteration; never
batch unverified work. Keep the 8–9-tool MCP surface — enrich tools, don't add them.

Checklist (in order; check off as committed):

- [ ] **S1 — Full LSP requests.** Extend `rust-core/src/lsp_session.rs` with
  `textDocument/references`, `definition`, `implementation`, `typeDefinition`,
  `rename`, following the existing documentSymbol/hover pattern (CLI subcommands +
  Go delegators). Graceful when the server lacks a capability.
  *DoD:* live `references` on a real symbol via rust-analyzer/tsserver; tests pass.

- [ ] **S2 — Scope-resolved CALLS edges.** Replace lexical name-matching in
  `symbolgraph.rs` with LSP-backed references where a server is available (batch
  `references` per defined symbol → real cross-file CALLS edges), heuristic
  fallback otherwise. Fix first-definer-wins so overloaded names don't misroute.
  *DoD:* a known caller→callee edge that the lexical graph got wrong is now correct;
  edges tagged with resolution source (`lsp` vs `lexical`); live-verified.

- [ ] **S3 — Communities / clusters.** Add modularity-based clustering (Leiden, or
  greedy modularity to start) over the reference-edge graph → `cluster_id` per file,
  exposed through `explain` (a dir/file's cluster + sibling files) and a clusters
  view. Generalize `ownership.rs` subsystem grouping beyond top-dir.
  *DoD:* two tightly-coupled files in different directories land in one cluster;
  test on a synthetic graph; live cluster summary on this repo.

- [ ] **S4 — IndexEngine Phase 1: incremental reindex.** On `ground`/`search`, diff
  `file_hashes` vs `index_baseline.json` (changetrack already has per-file SHA) →
  reindex only dirty paths into the warm graph cache instead of a full rebuild.
  Precompute authority (inbound-degree/PageRank) into the cached graph.
  *DoD:* editing one file reindexes in ≪ full-build time; warm==full correctness.

- [ ] **S5 — IndexEngine Phase 2: agent-feedback layer.** A feedback segment
  (`agent_feedback.json` or an `xm_feedback` table) appended from MCP tool calls
  (search query+returned paths, remember/verify path boosts, run success/fail →
  boost/suppress touched paths). Fuse a feedback term into search ranking
  (`…+ 0.10·feedback`); decay by recency. Seed from the retrieval ledger.
  *DoD:* a path that was verified/used ranks higher on a later search; test the
  fusion; live before/after on a seeded workspace.

- [ ] **S6 — IndexEngine Phase 3: symbol-level adjacency for impact.** Precompute
  symbol adjacency from S2's resolved edges; `impact?symbol=` traverses the
  precomputed adjacency (bounded BFS) for true blast radius (callers/callees/tests),
  not just dirty symbols. Add `from=/to=` trace on `impact` (shortest path between
  two symbols), HTTP-first.
  *DoD:* blast radius for an arbitrary symbol matches hand-traced callers; trace
  finds a known path; live-verified.

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
