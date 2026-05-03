# Semantic Core Next Tranche Prompt

Date: 2026-04-30

Use this prompt when continuing xMustard's repo-intelligence buildout from the current checkout.

## Objective

Continue xMustard as a standalone repo-intelligence and agent-grounding tool.

The next tranche should deepen the semantic core without losing the current product shape:

- xMustard stays separate from MustardCoPilot
- MustardCoPilot consumes xMustard as a tool/service
- repo intelligence and ops memory remain the center
- markdown stays an export layer, not the operating substrate

## Current Reality In This Checkout

Already implemented:

- Postgres planning/render/bootstrap surfaces
- Postgres foundation schema now includes semantic tables (`file_symbol_summaries`, `semantic_queries`, `semantic_matches`) and durable plan-memory tables (`run_plans`, `run_plan_revisions`)
- plan ownership, versioning, attached files, and revision history
- repo-state, change summary, run-targets, verify-targets, and code-explainer surfaces
- ingestion-plan surface with explicit semantic phase readiness/blockers
- repo-map cleanup for large repos so totals reflect repo-understanding-relevant files
- parser-backed symbol extraction via `tree-sitter-language-pack` when available
- `path-symbols` surface
- `semantic-search` surface contract for ast-grep-backed pattern queries with graceful fallback when the binary is absent
- issue-context prompt sections, dynamic context, and retrieval ledger support for semantic matches when ast-grep is available
- ingestion-plan now distinguishes partially implemented semantic phases from fully unstarted ones
- `path-symbols`, `semantic-search`, and issue-context semantic matches now emit storage-ready row previews for Postgres materialization
- Postgres materialization helpers and API/CLI surfaces now exist for path-symbol rows and semantic-search rows
- workspace-batch Postgres materialization exists for parser-backed symbol indexing

Important local facts:

- backend venv has `tree-sitter` and `tree-sitter-language-pack` installed
- `ast-grep` / `sg` binary is not installed on this machine at the time of writing
- MustardCoPilot is loaded as workspace `mustardcopilot-71f90466e7`
- live `path-symbols` works against MustardCoPilot and returns parser-backed symbols for `src/main.tsx`
- live ingestion plan reports `tree_sitter_index` and `ast_grep_rules` as partial implementations, with durable materialization still gated by Postgres config and `sg` availability

## Current Core Files

Read these first:

- `backend/app/models.py`
- `backend/app/service.py`
- `backend/app/semantic.py`
- `backend/app/main.py`
- `backend/app/cli.py`
- `backend/tests/test_tracker_service.py`
- `backend/tests/test_cli.py`
- `backend/tests/test_activity_ledger.py`
- `docs/REPO_COCKPIT_TOOL_ARCHITECTURE.md`
- `docs/plans/2026-04-29-repo-cockpit-tool-plan.md`

## Product Rules

Do not drift from these:

- keep the system issue-first and evidence-first
- preserve durable operational memory as a moat
- do not turn xMustard back into a tracker-first app
- do not merge xMustard into MustardCoPilot
- backend/tool contracts come before UI polish
- prefer structured state and typed surfaces over markdown summaries

## Next Tranche Goal

Move from file-level semantic helpers toward reusable semantic context and durable indexing.

That means this tranche should do three things:

1. make semantic signals reusable across multiple surfaces
2. prepare Postgres materialization for symbols and semantic matches
3. start feeding semantic matches into issue-context retrieval instead of leaving them isolated tools

Those are no longer greenfield asks:

- semantic matches already enrich issue context when ast-grep is available
- the schema now has explicit storage shapes for semantic queries, semantic matches, parser-backed file summaries, and plan revisions
- storage-ready row builders now exist for file symbol summaries, symbol rows, semantic queries, and semantic matches
- actual Postgres write helpers now exist for ad hoc path-symbol and semantic-search materialization
- a first workspace-batch symbol materialization job now exists, but candidate-path selection still needs to get smarter on real repos
- the next step is to evolve those single-surface writes into reusable batch/indexing flows

## Exact Tasks

### 1. Reuse parser-backed symbol records across context assembly

Add a cached or reusable symbol-summary path so:

- `code-explainer`
- `path-symbols`
- issue-context symbol ranking
- future impact analysis

do not each become separate extraction islands.

The first step can still be file-on-demand, but the contract should look ready for storage.

### 2. Deepen semantic-search integration into issue context

Take the current `semantic-search` contract and push it beyond the first additive integration.

Add a narrow heuristic:

- derive a small set of semantic patterns from issue title/summary/evidence
- run ast-grep only when the binary is available
- add resulting matches into:
  - related paths
  - retrieval ledger
  - dynamic context or a sibling semantic-match bundle

Do not let ast-grep dominate context. It should enrich retrieval, not replace it.

Current state:

- semantic matches already flow into dynamic context, related paths, prompt sections, and retrieval ledger when ast-grep is available
- the next step is to make those semantic matches reusable, less heuristic-heavy, and more storage-ready

### 3. Expand from ad hoc writes into reusable semantic ingestion

Extend the Postgres foundation from ad hoc materialization helpers into reusable semantic ingestion paths.

At minimum:

- reuse the existing file-symbol-summary insertion helper in broader indexing flows
- reuse the existing semantic-query insertion helper in issue-context or replay flows
- reuse the existing semantic-match insertion helper in broader indexing flows
- define how run-plan tracking will eventually join the same ops-memory store
- keep it additive and inspectable

You do not need to fully persist every surface in this tranche, but the next step should move beyond one-off writes and toward repeatable semantic ingestion jobs.

### 4. Improve ingestion-plan truthfulness further

The ingestion plan now distinguishes partially implemented semantic phases. Keep going until it can also report when durable storage helpers exist but full indexing jobs are still pending.

Refine it so it can distinguish:

- tool available
- on-demand extraction implemented
- durable materialization still pending

The goal is to tell the truth about progress, not just about the end state.

### 5. Keep the ast-grep path graceful

If `sg` is unavailable:

- return structured `engine=none`
- keep a clear error message
- do not raise noisy exceptions through the tool surface

If `sg` is available later:

- the same surface should work without API changes

### 6. Add verification that matches the risk

Keep tests focused and real:

- service tests for semantic context enrichment
- CLI/API tests for semantic-search and path-symbols
- compile check
- no brittle giant snapshots

## Acceptance Criteria

This tranche is done when:

- issue context can consume semantic matches when ast-grep is available and explain them clearly
- retrieval ledger can explain semantic match contributions
- parser-backed symbol extraction is reusable and not isolated to one endpoint
- ingestion-plan more accurately reflects implemented-vs-materialized semantic work
- Postgres semantic storage shape is explicit in code or schema, not only in docs
- tests pass cleanly

## Non-Goals

Do not do these in the same tranche unless they are absolutely necessary:

- full LSP manager
- graph canvas UI
- embeddings pipeline
- wiki generation
- cross-repo groups
- broad frontend redesign

## Suggested Execution Order

1. read current semantic surfaces and ingestion-plan logic
2. refactor reusable semantic record helpers
3. integrate semantic-search into issue-context retrieval
4. update retrieval ledger and dynamic context contracts as needed
5. extend Postgres semantic planning/materialization contracts
6. refine ingestion-plan truthfulness
7. add tests
8. run targeted verification
9. test against MustardCoPilot workspace again

## Checks

Run at minimum:

- `cd backend && pytest -q tests/test_tracker_service.py tests/test_cli.py tests/test_activity_ledger.py tests/test_scanners.py`
- `cd backend && PYTHONPYCACHEPREFIX=/tmp/pycache python3 -m compileall app`

If semantic storage changes land:

- run the relevant Postgres plan/render tests too

## Notes For The Next Agent

- respect the dirty worktree
- do not revert unrelated frontend, Go, or data changes
- keep xMustard as the repo-intelligence tool boundary
- prefer narrow semantic contracts over giant scanner growth
