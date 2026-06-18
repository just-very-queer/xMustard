# xMustard — Remaining Work & Track Split

All three surfaces + the four depth upgrades + OpenAI-compatible providers + task-typed routing +
multi-agent context governance + bearer-token auth are **built and verified** (see `CHANGELOG.md`,
`BUILD_LOG.md`, `PLANNED_FEATURES.md`). What remains are depth/integration items, split into two
tracks that can proceed in parallel.

**Split principle:** codex (`gpt-5.3-codex-spark`, supervised + independently verified) takes
self-contained, pattern-following slices; Claude takes architectural, cross-cutting,
security-sensitive, and integration-heavy work.

## Codex track (mechanical, pattern-following — Claude verifies)

- **C1 — Postgres write path for ops** *(in progress)*: make `run_plans` + `verification_*` live in
  Postgres (currently JSON), mirroring the `pgops.go` pattern (schema + materialize + read-back +
  FTS where useful). Endpoints under `/pg/*`. `PLANNED_FEATURES.md` line "make PG the write path".
- **C2 — LSP impl/type/rename**: extend `rust-core/src/lsp_session.rs` with `textDocument/
  implementation`, `typeDefinition`, and `rename` request types, following the existing
  `documentSymbol`/`hover` pattern (CLI subcommands + Go delegators + endpoints). Graceful when the
  server lacks the capability.
- **C3 — Enclosing-scope context**: in `rust-core/src/treesitter.rs`, walk each symbol's tree-sitter
  parent chain to record its enclosing scope (module/impl/class/fn) on `RustPathSymbolRecord.
  enclosing_scope` (currently always `None`). Bounded, additive, unit-tested.
- **C4 — Runtime/service-graph discovery**: enrich `project_info` with env-var references,
  docker/compose services, and the process/service graph (`PLANNED_FEATURES.md` line 71), evidence-
  gated like the existing target detection.

## Claude track (architectural / integration / security)

- **A1 — Routing as a first-class run-execution runtime**: let a run dispatch to an OpenAI-compatible
  provider (not just the codex/opencode CLIs) — a `provider:<name>` runtime branch in
  `validateRuntimeModel`/`buildRuntimeCommand` + a managed-run execution path that records a
  `runRecord` from `RouteAndChat`. Closes "wire routing as a run-execution runtime".
- **A2 — Neural embeddings + graph-proximity RRF lane**: add an embeddings lane sourced from a
  provider's `/embeddings` endpoint (reusing the provider layer — no Python), plus a graph-proximity
  lane (symbol-graph distance) fused into RRF in `search.rs`/`SearchPostgres`.
- **A3 — Failure explainers**: "why a failure happened" (correlate run output + diagnostics +
  changed symbols) and contract-break detection in impact analysis.
- **A4 — Cockpit integration**: surface providers, task-typed routing, context governance
  (propose/verify/active), and auth (token mgmt, whoami) in the React cockpit.
- **A5 — Auth follow-ons**: an audit log of auth events (mint/revoke/denied), token expiry/rotation,
  and finer per-endpoint authz beyond the admin/agent/readonly split.

## Goal records
The two tracks are also registered as `/goal` runtime goals in the self workspace
(`xmustard-core goal create`) for traceability; codex is driven against the codex-track goal in a
supervised tmux loop, with each slice verified (cargo/go tests + diff review) before it is accepted.
