# xMustard — Remaining Work & Track Split

All three surfaces + the four depth upgrades + OpenAI-compatible providers + task-typed routing +
multi-agent context governance + bearer-token auth are **built and verified** (see `CHANGELOG.md`,
`BUILD_LOG.md`, `PLANNED_FEATURES.md`). What remains are depth/integration items, split into two
tracks that can proceed in parallel.

**Split principle:** codex (`gpt-5.3-codex-spark`, supervised + independently verified) takes
self-contained, pattern-following slices; Claude takes architectural, cross-cutting,
security-sensitive, and integration-heavy work.

> **Status (all closed 2026-06-21, re-verified 2026-06-23):** every track item is now
> done. C2/C3/C4/A1/A3 closed earlier; the items this banner once listed as open all
> shipped in the remaining-work loop — C1 → **R5** (PG write path), A2 graph-proximity
> → **R1**, A3 contract-break → **R2**, A4 provider/routing/token UI → **R7**, A5 auth
> follow-ons → **R4**. See `docs/plans/2026-06-21-remaining-work-loop.md` (R1–R7, all
> committed) and `docs/STATUS.md` §7. No track item remains open.

## Codex track (mechanical, pattern-following — Claude verifies)

- **C1 — Postgres write path for ops** ✅ (**R5**): `pg_inline.go` mirrors `run_plans` +
  `verification_*` into PG inline on mutation (JSON stays source of truth; best-effort,
  gated on `XMUSTARD_PG_DSN`), read-back via `GET /pg/run-plans`.
- **C2 — LSP impl/type/rename** ✅: `lsp_session.rs` has references/definition/implementation/
  typeDefinition/rename + a persistent session (S1).
- **C3 — Enclosing-scope context** ✅: `treesitter.rs` walks the parent chain → enclosing_scope.
- **C4 — Runtime/service-graph discovery** ✅: `project_info.go buildProjectServiceGraph` builds
  service identities/groups/relationships + compose + env handling.

## Claude track (architectural / integration / security)

- **A1 — Routing as a first-class run-execution runtime** ✅: `StartProviderRun` —
  `provider:<name>`/`route` runtimes execute via the model and record a runRecord.
- **A2 — Neural embeddings + graph-proximity RRF lane** ✅: neural-embedding rerank
  (`OpenAIEmbeddings`, `/search?rerank=`) done; graph-proximity lane → **R1** (`hybrid_search`
  `seed=` folds `symbol_impact` distances into a 4th `proximity` RRF lane).
- **A3 — Failure explainers** ✅: `why_failed` correlates run output + error lines + changed files.
  Contract-break detection → **R2** (`changetrack::symbol_signature` + `ChangeSet.contract_breaks`).
- **A4 — Cockpit integration** ✅: governance `MemoryPanel` done; provider/routing/token UI → **R7**
  (`AdminPanel.tsx`, `admin` view).
- **A5 — Auth follow-ons** ✅ (**R4**): capped auth-audit log (mint/revoke/rotate/denied,
  `GET /api/auth/audit`), token expiry + rotation, and a real `roleRank` hierarchy with
  explicit per-endpoint `agent` gates.

## Goal records
The two tracks are also registered as `/goal` runtime goals in the self workspace
(`xmustard-core goal create`) for traceability; codex is driven against the codex-track goal in a
supervised tmux loop, with each slice verified (cargo/go tests + diff review) before it is accepted.
