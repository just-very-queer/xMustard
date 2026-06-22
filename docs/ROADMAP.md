# xMustard — Remaining Work & Track Split

All three surfaces + the four depth upgrades + OpenAI-compatible providers + task-typed routing +
multi-agent context governance + bearer-token auth are **built and verified** (see `CHANGELOG.md`,
`BUILD_LOG.md`, `PLANNED_FEATURES.md`). What remains are depth/integration items, split into two
tracks that can proceed in parallel.

**Split principle:** codex (`gpt-5.3-codex-spark`, supervised + independently verified) takes
self-contained, pattern-following slices; Claude takes architectural, cross-cutting,
security-sensitive, and integration-heavy work.

> **Status (verified 2026-06-21):** C2, C3, C4, A1, A3 ✅ done; A2 ✅ neural-embedding
> rerank done, ⬜ graph-proximity lane remains; A4 ✅ governance MemoryPanel done,
> ⬜ provider/routing/token UI remains; C1 ⬜ and A5 ⬜ open. The genuinely-open work
> is tracked in `docs/plans/2026-06-21-remaining-work-loop.md`.

## Codex track (mechanical, pattern-following — Claude verifies)

- **C1 — Postgres write path for ops** ⬜: make `run_plans` + `verification_*` live in
  Postgres (currently JSON), mirroring the `pgops.go` pattern (schema + materialize + read-back +
  FTS where useful). Endpoints under `/pg/*`. `PLANNED_FEATURES.md` line "make PG the write path".
- **C2 — LSP impl/type/rename** ✅: `lsp_session.rs` has references/definition/implementation/
  typeDefinition/rename + a persistent session (S1).
- **C3 — Enclosing-scope context** ✅: `treesitter.rs` walks the parent chain → enclosing_scope.
- **C4 — Runtime/service-graph discovery** ✅: `project_info.go buildProjectServiceGraph` builds
  service identities/groups/relationships + compose + env handling.

## Claude track (architectural / integration / security)

- **A1 — Routing as a first-class run-execution runtime** ✅: `StartProviderRun` —
  `provider:<name>`/`route` runtimes execute via the model and record a runRecord.
- **A2 — Neural embeddings + graph-proximity RRF lane** ✅/⬜: neural-embedding rerank
  (`OpenAIEmbeddings`, `/search?rerank=`) done; ⬜ graph-proximity lane (use `symbol_impact`
  distances as a 4th RRF lane) remains.
- **A3 — Failure explainers** ✅: `why_failed` correlates run output + error lines + changed files.
  ⬜ contract-break detection remains (foundation: `signature_text` already extracted).
- **A4 — Cockpit integration** ✅/⬜: governance `MemoryPanel` done; ⬜ provider/routing/token UI remains.
- **A5 — Auth follow-ons** ⬜: audit log of auth events (mint/revoke/denied), token expiry/rotation,
  and finer per-endpoint authz beyond the admin/agent/readonly split.

## Goal records
The two tracks are also registered as `/goal` runtime goals in the self workspace
(`xmustard-core goal create`) for traceability; codex is driven against the codex-track goal in a
supervised tmux loop, with each slice verified (cargo/go tests + diff review) before it is accepted.
