# Repo Cockpit Tool Plan

Date: 2026-04-29

Status: accepted planning direction for the next xMustard phase.

## Purpose

This plan turns xMustard into a standalone repo intelligence and agent grounding tool that
can be consumed by MustardCoPilot without merging codebases.

The key decision in this plan:

- xMustard remains separate
- MustardCoPilot uses xMustard as a tool or service
- PostgreSQL becomes the primary persistent store
- semantic intelligence becomes the top build priority

## Product Goal

Build a tool that gives agents and operators a durable, queryable answer to:

- what is open
- what changed
- what is happening
- how to run it
- how to verify it
- what code matters
- what is risky

## Main Technology Decisions

### PostgreSQL

Use PostgreSQL as the primary store for:

- repo metadata
- symbols
- files
- graph edges
- runs
- verification history
- diagnostics
- tickets
- artifacts
- retrieval indexes

Postgres is chosen because this phase needs:

- indexed joins across repo and run state
- durable history
- text ranking support including BM25-capable search through the stack we adopt
- headroom for hybrid retrieval and graph-style queries

### LSP

Use LSP for live code intelligence:

- definitions
- references
- hover
- document symbols
- workspace symbols
- diagnostics
- implementation/type definition where available

### `tree-sitter`

Use `tree-sitter` for:

- parser-controlled symbol extraction
- enclosing-scope recovery
- syntax-aware chunking
- fallback structure when LSP is absent or weak

### `ast-grep`

Use `ast-grep` for:

- semantic pattern queries
- issue-shaped code matching
- contract/rule checks
- change and impact heuristics

### Semantic search

Use hybrid retrieval across:

- BM25 text ranking
- structural graph relevance
- lexical ranking
- optional embedding-based similarity later

The first slice does not need embeddings to be useful.

### Shared contracts

Use a narrow shared contract layer for:

- tool request and response shapes
- schema constants
- graph/storage identifiers
- cross-surface DTOs that must stay aligned

Do not create broad package sprawl just to imitate a monorepo. The lesson from GitNexus is to share the authoritative contracts, not to inherit manual packaging complexity.

### Wiki generation

Keep wiki generation in scope for this product direction.

It should be built as a downstream consumer of:

- semantic repo structure
- subsystem maps
- runbooks
- verification knowledge
- operational memory where that history improves the docs

This is not a first-tranche feature, but it is a real roadmap item.

### Product moat

xMustard should preserve a clear advantage:

- semantic repo intelligence tells the agent what the code means
- operational memory tells the agent what happened, what was tried, what passed, what failed, and what was accepted

That second half is not decoration. It is the moat.

Operational memory should explicitly include:

- plan ownership and status
- plan versions and revision history
- attached repo-relative files for each plan
- branch, head SHA, and dirty-path snapshots at plan revision time
- accepted fixes, verification outcomes, and review decisions

## Delivery Phases

## Phase 0: Reframe And Freeze

Goal:

- stop growing the tracker-first center
- freeze the useful contracts that already exist

Build:

- adopt the repo cockpit tool architecture
- preserve the current packet and artifact model
- stop treating queue-first tracker flows as the main product

Done when:

- planning docs and architecture docs reflect the standalone tool boundary
- new work no longer defaults to tracker-heavy UI expansion

## Phase 1: PostgreSQL Foundation

Goal:

- replace JSON-first persistence as the long-term core

Build:

- Postgres schema for workspaces, files, symbols, graph edges, runs, verifications, activity, diagnostics, artifacts
- migration and compatibility layer from current JSON artifacts
- repo state snapshot tables
- text and ranking indexes
- contract tables and DTO alignment surfaces where needed
- durable plan-tracking tables for ownership, attached files, revision history, and git-state snapshots

First tables:

- `workspaces`
- `workspace_snapshots`
- `files`
- `symbols`
- `file_symbol_summaries`
- `symbol_edges`
- `semantic_queries`
- `semantic_matches`
- `activity_events`
- `run_records`
- `run_plans`
- `run_plan_revisions`
- `verification_profiles`
- `verification_runs`
- `issue_artifacts`
- `diagnostics`

Done when:

- a workspace can be ingested into Postgres
- repo state and activity can be queried from DB instead of only flat files

## Phase 2: Structural Intelligence

Goal:

- add parser-backed code structure before full LSP coverage lands

Build:

- `tree-sitter` indexing pipeline
- symbol extraction
- enclosing scope extraction
- file-to-symbol summaries
- path-to-symbol explanations
- `ast-grep` query integration
- semantic query and match materialization
- indexing boundaries tuned for large real repos
- explicit separation between indexed source, key context files, and ignored/generated output

Done when:

- the repo map can show symbol-aware context
- issue packets can attach symbol-level context without file dumping
- semantic matches can be replayed and explained from durable storage instead of only on-demand assembly
- large repos do not report noisy totals dominated by build output or generated artifacts

Current CLI-first tranche:

1. expose `semantic-index plan` as the inspectable command that shows selected paths, surface, blockers, warnings, and next actions before writing anything
2. expose `semantic-index run` as the applying command that uses the same plan and materializes parser-backed symbol rows into Postgres
3. keep `--surface cli` as a positive selection signal for command/runtime/server files, not a hardcoded UI exclusion
4. preserve exact `--path` selection for agents that already know the files they want indexed
5. make the next follow-up the durable read path: context packets and CLI queries should prefer stored semantic rows when they are fresh

Phase 2 live status on 2026-05-01:

- `semantic-index plan`, `run`, and `status` exist, and plan output now carries a retrieval ledger for selected paths.
- Local Postgres is configured for xMustard, the schema is bootstrapped, and MustardCoPilot semantic index runs now persist and read back Postgres rows.
- `path-symbols` and `code-explainer` prefer stored semantic rows when Postgres has a matching baseline; scoped path reads can use a wider stored baseline when the indexed path hash and HEAD still match.
- `changed-symbols`, `impact`, and `repo-context` expose freshness, derivation sources, confidence, plan/file/run linkage, accepted-fix linkage, and retrieval-ledger entries.
- `changed-since-last-run`, `changed-since-last-accepted-fix`, and `retrieval-search` provide CLI-first floors for deltas and hybrid lexical/structural retrieval.
- `sg` / `ast-grep` is installed locally; semantic-search returns ast-grep matches, and Postgres semantic query/match persistence has been proven live.
- Final closeout validation is still not a clean `fresh` proof: the target MustardCoPilot worktree has 189 dirty files, so scoped semantic status is `dirty_provisional`, not clean `fresh`. Postgres and `sg` are no longer blockers.
- Sixth-hundred closeout audit update: broad key-files `semantic-index status` is still `stale` because the stored key-files fingerprint no longer matches current selected path/hash inputs while MustardCoPilot remains dirty. Stored-path and changed-code surfaces still return `dirty_provisional` where their scoped baseline matches.
- Missing richer run anchors, accepted-fix anchors, and plan links are absent MustardCoPilot workspace history, not proven product gaps: existing run records have no saved plans or ownership files, and the workspace has no accepted fix records.
- Blunt status: Phase 2 is closed as live-validated but dirty/stale provisional. It is not a clean `fresh` closeout until MustardCoPilot is clean and `fresh` can be proven.

## Phase 3: LSP And Diagnostics

Goal:

- give agents live language-server intelligence

Build:

- per-workspace LSP session manager
- definitions/references/symbol queries
- diagnostics ingestion and persistence
- symbol-to-diagnostic linking
- context packet enrichment from LSP answers

Done when:

- agents can ask for definitions, references, and diagnostics through xMustard
- diagnostics persist and can be linked to repo state and runs

## Phase 4: Runtime And Project Discovery

Goal:

- make "how to run it" and "how to verify it" first-class answers

Build:

- manifest discovery
- run/build/test/lint target discovery
- entrypoint discovery
- env/config discovery
- service graph discovery from Docker, compose, and common manifests
- verification target registry

Done when:

- xMustard can answer how to run and verify the repo without hand-authored markdown

## Phase 5: Retrieval, Search, And Impact

Goal:

- move from structural context to explainable retrieval and impact analysis

Build:

- BM25-backed retrieval over repo and artifact text
- hybrid ranking over structural and lexical evidence
- changed symbol tracking
- call/import/dependency impact analysis
- affected test estimation
- retrieval ledger upgrades
- stale index and sibling-clone drift detection

Done when:

- context packets can explain why each symbol, file, or artifact was selected
- agents can ask what a change is likely to break

## Phase 6: Explainers And Tool Surface

Goal:

- expose the system as a clean tool for MustardCoPilot and other clients

Build:

- `repo_state`
- `repo_summary`
- `changed_since`
- `definitions`
- `references`
- `diagnostics`
- `impact`
- `run_targets`
- `verify_targets`
- `issue_context_packet`
- `recent_failures`
- `code_explainer`
- `subsystem_explainer`
- `wiki_overview`
- `subsystem_docs`

Done when:

- MustardCoPilot can use xMustard through structured tool calls instead of doc scraping

## Phase 7: Wiki Generation

Goal:

- generate durable repo docs from semantic truth plus operational memory

Build:

- graph-backed overview generation
- subsystem/module pages
- incremental regeneration keyed to repo changes
- links to runbooks, verification surfaces, and recent accepted work
- review-first module map artifact that can be inspected and edited before full generation
- freshness checks so current source and stored graph state cannot silently diverge

Done when:

- xMustard can generate useful repo docs that are anchored to code truth and workflow truth

Non-goals for the first wiki slice:

- do not ship stale source plus stale graph mixes
- do not depend on opaque LLM grouping without an inspectable fallback
- do not treat generated prose as a source of truth

## Phase 8: Cockpit UI Rebuild

Goal:

- reshape the UI around repo state, not tracker queues

Build:

- repo overview
- change state panel
- execution state panel
- verification state panel
- risk state panel
- intelligence inspector

## Quality Bar

The semantic-core phase should match a higher quality bar than the current tracker-style app needed.

Borrowed lessons from GitNexus:

- separate unit, integration, and UI/E2E checks where the surface justifies it
- keep architecture and package-boundary docs current
- typecheck shared contracts and API/tool DTOs
- add fixture-based regression tests for language/indexing behavior

Concrete expectations for xMustard:

1. `tree-sitter` extraction needs fixture repos and golden outputs
2. `ast-grep` rules need regression tests tied to real bug shapes
3. tool/API contract changes need synchronized checks
4. wiki generation needs freshness and invalidation tests before it is trusted

De-emphasize:

- queue-first home
- saved views as main feature
- source inventory as headline surface

Done when:

- the home experience reads as repo cockpit, not local issue tracker

## First Implementation Tranche

Start here before broader UI work:

1. freeze current useful contracts
2. define Postgres schema
3. wire Postgres connection and migration bootstrap
4. add `tree-sitter` indexing
5. add `ast-grep` query support
6. define first structured tool endpoints:
   - `repo_state`
   - `changed_since`
   - `run_targets`
   - `verify_targets`
   - `code_explainer`

Why this first:

- it creates the new foundation without waiting for the full LSP stack
- it gives MustardCoPilot something useful to call early
- it starts moving the product away from tracker gravity immediately

## Success Criteria

The plan is working when xMustard can answer, from structured state:

- what changed
- what is currently broken
- what code is relevant
- how to run the repo
- how to verify the repo
- what symbols are involved
- what diagnostics exist
- what is risky to touch

without depending on manually curated markdown as the primary source of truth.

## Explicit Boundary With MustardCoPilot

MustardCoPilot should consume xMustard through a tool boundary only.

MustardCoPilot should not:

- absorb xMustard persistence internals
- take on xMustard route ownership
- inherit xMustard tracker UI directly

MustardCoPilot should:

- call xMustard for repo grounding
- render xMustard outputs in threads, inspectors, and workspace views
- remain the operator shell and thread orchestrator
