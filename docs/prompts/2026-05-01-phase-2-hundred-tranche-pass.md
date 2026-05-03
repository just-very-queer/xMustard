# Phase 2 Hundred-Tranche Pass

Date: 2026-05-01

Use this file as the durable execution prompt and tranche ledger for Phase 2 work on xMustard.

This is for a separate execution chat. This chat remains the continuity and product-truth thread.

## Objective

Finish Phase 2: Structural Intelligence as a CLI-first repo-intelligence tool.

Do not drift into web UI polish, tracker CRUD, or Phase 3 LSP implementation. The current mission is to make xMustard excellent at:

- what changed
- what matters
- what symbols/files are relevant
- whether semantic state is fresh or stale
- how to run and verify the repo
- how plans, runs, and changes connect

## Product Rules

Do not violate these:

- xMustard stays separate from MustardCoPilot
- MustardCoPilot consumes xMustard as a tool/service
- current focus is CLI, not web UI
- semantic repo intelligence plus ops memory is the product center
- markdown is for exports and handoff, not the control plane
- no tracker-first drift
- no fake “meaningful impact” slicing
- do not revert unrelated work in the dirty tree
- prefer typed tool surfaces and durable storage over prompt-only cleverness

## Current Reality

Already landed in this checkout:

- Postgres schema planning, render, and bootstrap surfaces
- semantic storage tables for file symbol summaries, semantic queries, and semantic matches
- plan tracking with ownership, versioning, attached files, and revision history
- `repo-state`, `changes`, `run-targets`, `verify-targets`, `code-explainer`, `path-symbols`
- `semantic-search` contract with graceful fallback when `sg` is unavailable
- workspace-batch symbol materialization into Postgres
- `semantic-index plan`
- `semantic-index run`
- CLI-first path selection with `--surface cli`
- selection details with path reason, score, file hash, head SHA, dirty count, and `index_fingerprint`
- backend dependency line moved to current versions:
  - FastAPI `0.136.1`
  - Uvicorn `0.46.0`
  - Pydantic `2.13.3`
  - Typer `0.25.1`
  - Click `8.3.3`
  - Psycopg `3.3.3`

Current known truth:

- `tree-sitter-language-pack` is available in the backend venv
- `sg` / `ast-grep` is still unavailable on this machine
- MustardCoPilot workspace id is `mustardcopilot-71f90466e7`
- live `semantic-index plan` works
- tests currently pass in the backend targeted suite

## What Phase 2 Still Needs

This is the real remaining list:

1. persist semantic index run metadata and stored baseline fingerprints
2. add `semantic-index status` to compare current repo state to last indexed baseline
3. add Postgres read path so semantic rows are not write-only
4. make context packet and CLI surfaces prefer fresh stored semantic rows
5. upgrade retrieval ledger so every selected symbol/file/artifact has an explicit why
6. add dirty symbol tracking instead of only dirty file tracking
7. wire semantic matches into durable replayable storage when `sg` becomes available
8. add hybrid retrieval floor over lexical plus structural evidence
9. add impact floor:
   callers, affected files, likely tests, likely plans/runs
10. add CLI repo-context surface that answers what changed, what matters, how to run it, and whether the semantic state is trustworthy

## Non-Goals For This Pass

Do not do these unless absolutely required to unblock current tranche work:

- full LSP manager
- graph UI
- embeddings pipeline
- wiki generation
- cross-repo orchestration
- frontend redesign
- route-for-route parity work that does not improve repo understanding

## Execution Style

Run this as a nonstop multi-agent pass.

You are allowed and encouraged to use multiple agents aggressively, but keep ownership boundaries clean.

Rules for the execution chat:

- read the listed files before editing
- update this file as tranches are completed
- keep one tranche in progress at a time in the ledger
- when a tranche is done, mark it complete and add a one-line result note
- if a tranche reveals a better next tranche, update the ledger honestly
- do not pause to over-explain; implement, test, update ledger, continue
- test at natural checkpoints, not only at the very end

## Suggested Multi-Agent Pattern

Use agents in parallel when useful:

- one agent on service/model/storage contracts
- one agent on CLI and API surfacing
- one agent on tests and validation
- one agent on research/plan alignment if the execution drifts

Do not send the immediate blocking task away if the main thread needs the result right now.

## Read First

- `backend/app/models.py`
- `backend/app/service.py`
- `backend/app/postgres.py`
- `backend/app/semantic.py`
- `backend/app/cli.py`
- `backend/app/main.py`
- `backend/tests/test_cli.py`
- `backend/tests/test_tracker_service.py`
- `backend/tests/test_postgres.py`
- `docs/REPO_COCKPIT_TOOL_ARCHITECTURE.md`
- `docs/plans/2026-04-29-repo-cockpit-tool-plan.md`
- `docs/GITNEXUS_EXTRACTION_MAP.md`
- `docs/RESEARCH_MATRIX.md`
- `docs/RESEARCH_FINDINGS.md`
- `docs/FRONTIER.md`

## Copy-Paste Prompt For The Other Chat

```md
Continue xMustard Phase 2 from the current checkout.

You are not brainstorming. You are executing a nonstop multi-agent pass and updating the tranche ledger in `docs/prompts/2026-05-01-phase-2-hundred-tranche-pass.md` as you go.

Hard rules:

- xMustard stays separate from MustardCoPilot
- current focus is CLI
- do not drift into web UI work
- do not regress into tracker-first product behavior
- semantic repo intelligence plus ops memory is the center
- do not revert unrelated dirty worktree changes
- use the latest versions already defined in `backend/pyproject.toml`

Current truth:

- `semantic-index plan` and `semantic-index run` exist
- semantic-index plan returns selected paths, reasons, file hashes, head SHA, dirty count, and `index_fingerprint`
- backend current versions are FastAPI `0.136.1`, Uvicorn `0.46.0`, Pydantic `2.13.3`, Typer `0.25.1`, Click `8.3.3`, Psycopg `3.3.3`
- `sg` is unavailable right now
- MustardCoPilot workspace id is `mustardcopilot-71f90466e7`

Your immediate mission is to keep pushing Phase 2 until the semantic DB is not just writable, but queryable and freshness-aware.

Start with tranche 004 unless the ledger has already moved past it.

Expected behavior:

1. read the files listed in the tranche prompt file
2. update the ledger status before editing
3. implement the tranche
4. run targeted tests
5. update the ledger with result notes
6. continue directly into the next tranche

Do not stop after one small patch unless you hit a real blocker.
```

## Tranche Ledger

Status legend:

- `done`
- `in_progress`
- `pending`
- `blocked`

### Completed So Far

| Tranche | Status | Goal | Result note |
|---|---|---|---|
| 001 | done | Add CLI semantic-index plan/run surface | `semantic-index plan` and `semantic-index run` landed |
| 002 | done | Upgrade backend to current package line | backend now uses current FastAPI/Uvicorn/Pydantic/Typer/Click/Psycopg line |
| 003 | done | Add selection details and repo baseline fingerprint | plan now includes reasons, hashes, `head_sha`, dirty count, and `index_fingerprint` |
| 004 | done | Persist semantic index run metadata and baseline fingerprints in durable storage | added `semantic_index_runs` baseline table plus Postgres write helper wired from `semantic-index run` |
| 005 | done | Add `semantic-index status` CLI surface | added JSON CLI status command over the service status contract |
| 006 | done | Compare current repo fingerprint against stored baseline and report freshness | status now reports `fresh`, `stale`, `dirty_provisional`, `no_baseline`, or `blocked` |
| 007 | done | Add schema/storage shape for semantic index sessions or baselines | baseline records store surface, strategy, selected paths, path details, counts, dirty state, and fingerprint |
| 008 | done | Add service contract for reading latest semantic index status | added `read_semantic_index_status` with current-plan comparison against latest stored baseline |
| 009 | done | Add CLI tests for semantic index status reporting | added CLI and service tests for status plumbing and freshness cases |
| 010 | done | Add Postgres read path for file symbol summaries | added typed `read_file_symbol_summary` helper and Postgres mapping test |
| 011 | done | Add Postgres read path for symbols | added typed `read_symbols_for_path` helper and Postgres mapping test |
| 012 | done | Add Postgres read path for semantic queries | added typed `read_semantic_queries` helper and Postgres mapping test |
| 013 | done | Add Postgres read path for semantic matches | added typed `read_semantic_matches` helper and Postgres mapping test |
| 014 | done | Make `path-symbols` prefer fresh stored rows when available | `path-symbols` now reads fresh Postgres summaries/symbols before falling back to on-demand extraction |
| 015 | done | Make `code-explainer` prefer fresh stored rows when available | `code-explainer` now uses fresh stored symbols for detected-symbol evidence before falling back to parser extraction |

### Active 100-Tranche Pass

| Tranche | Status | Goal |
|---|---|---|
| 004 | done | Persist semantic index run metadata and baseline fingerprints in durable storage |
| 005 | done | Add `semantic-index status` CLI surface |
| 006 | done | Compare current repo fingerprint against stored baseline and report `fresh` / `stale` / `dirty_provisional` |
| 007 | done | Add schema/storage shape for semantic index sessions or baselines |
| 008 | done | Add service contract for reading latest semantic index status |
| 009 | done | Add CLI tests for semantic index status reporting |
| 010 | done | Add Postgres read path for file symbol summaries |
| 011 | done | Add Postgres read path for symbols |
| 012 | done | Add Postgres read path for semantic queries |
| 013 | done | Add Postgres read path for semantic matches |
| 014 | done | Make `path-symbols` prefer fresh stored rows when available |
| 015 | done | Make `code-explainer` prefer fresh stored rows when available |
| 016 | pending | Add freshness warning to `path-symbols` when DB state is stale |
| 017 | pending | Add freshness warning to `code-explainer` when DB state is stale |
| 018 | pending | Add retrieval-ledger entries for semantic index path selection |
| 019 | pending | Add retrieval-ledger entries for stored semantic rows |
| 020 | pending | Upgrade context packet to distinguish on-demand vs stored semantic evidence |
| 021 | pending | Add durable context-selection reason strings for symbol entries |
| 022 | pending | Add durable context-selection reason strings for semantic-match entries |
| 023 | pending | Add symbol-level dirty tracking floor from changed files plus stored symbols |
| 024 | pending | Add CLI surface for changed symbols |
| 025 | pending | Add likely affected files from changed symbols |
| 026 | pending | Add likely affected tests from changed symbols |
| 027 | pending | Add impact contract model for symbol/file/test impact |
| 028 | pending | Add CLI `impact` surface |
| 029 | pending | Add repo-context contract model |
| 030 | pending | Add CLI `repo-context` surface |
| 031 | pending | Include semantic freshness in repo-context output |
| 032 | pending | Include changed files and changed symbols in repo-context output |
| 033 | pending | Include run-targets and verify-targets in repo-context output |
| 034 | pending | Include plan ownership links in repo-context output |
| 035 | pending | Include recent activity in repo-context output |
| 036 | pending | Persist semantic index materialization counts and timestamps |
| 037 | pending | Persist semantic index surface (`cli` / `web` / `all`) in baseline records |
| 038 | pending | Persist semantic index selected path list in baseline records |
| 039 | pending | Persist worktree dirty summary in baseline records |
| 040 | pending | Add sibling-clone / repo-root mismatch detection shape |
| 041 | pending | Add remote URL capture for future sibling-clone drift detection |
| 042 | pending | Add explicit stale reason enum or string contract |
| 043 | pending | Add status read path to issue-context packet assembly |
| 044 | pending | Make issue-context use stored symbol context when fresh |
| 045 | pending | Fall back cleanly to on-demand extraction when stored rows are missing or stale |
| 046 | pending | Add tests for fresh stored symbol use in issue context |
| 047 | pending | Add tests for stale fallback behavior in issue context |
| 048 | pending | Add tests for dirty provisional status |
| 049 | pending | Add tests for fingerprint drift |
| 050 | pending | Add materialized semantic-index activity records with fingerprint/status |
| 051 | pending | Add `semantic-index run --dry-run` result consistency checks |
| 052 | pending | Improve CLI path selection reasons so top choices are easier to audit |
| 053 | pending | Improve CLI surface seeding from run-targets and verify-targets |
| 054 | pending | Prevent non-code assets from surfacing in default CLI semantic plans |
| 055 | pending | Tighten test/spec penalties without blacklisting whole surfaces |
| 056 | pending | Add structural ranking note to semantic index plan output |
| 057 | pending | Add lexical ranking note to semantic index plan output where relevant |
| 058 | pending | Add plan ownership linkage from changed files to run plans |
| 059 | pending | Add attached-file linkage from plans into repo-context |
| 060 | pending | Add latest accepted-fix linkage into repo-context |
| 061 | pending | Add changed-since-last-run floor |
| 062 | pending | Add changed-since-last-accepted-fix floor |
| 063 | pending | Add context packet note for blocked semantic freshness when no baseline exists |
| 064 | pending | Install and validate `sg` if environment permits |
| 065 | pending | Enable live semantic match persistence when `sg` is available |
| 066 | pending | Add semantic match replay from stored rows |
| 067 | pending | Add semantic match freshness handling |
| 068 | pending | Add tests for semantic match persistence path |
| 069 | pending | Add tests for semantic match replay path |
| 070 | pending | Add BM25/lexical retrieval foundation in Postgres-backed search docs/contracts |
| 071 | pending | Add search document storage shape for symbols and semantic matches if missing |
| 072 | pending | Add retrieval query contract for lexical plus structural search |
| 073 | pending | Add CLI search surface that can explain result reasons |
| 074 | pending | Add retrieval-ledger source typing for lexical vs structural hits |
| 075 | pending | Add contract for likely owner / hotspot placeholders without fake precision |
| 076 | pending | Add repo-context explanation text for why a symbol/file mattered |
| 077 | pending | Add repo-context explanation text for why a plan/run mattered |
| 078 | pending | Add repo-context explanation text for why verification targets mattered |
| 079 | pending | Add summary command for “what happened since last semantic index” |
| 080 | pending | Add tests around summary command and freshness output |
| 081 | pending | Re-run live semantic-index plan against MustardCoPilot and refine path selection |
| 082 | pending | Re-run live semantic-index run against a configured Postgres target |
| 083 | pending | Verify rows exist and can be read back |
| 084 | pending | Add docs note for current semantic-index CLI usage |
| 085 | pending | Update phase plan doc with actual landed status |
| 086 | pending | Update research-facing docs if direction shifted materially |
| 087 | pending | Add fixture coverage for multi-language symbol materialization if touched |
| 088 | pending | Add fixture coverage for stale index status if touched |
| 089 | pending | Add fixture coverage for changed symbol tracking if touched |
| 090 | pending | Tighten compile/test loop around new CLI surfaces |
| 091 | pending | Run targeted live checks against MustardCoPilot repo-context output |
| 092 | pending | Clean up contract names only if drift is now confusing |
| 093 | pending | Add final tranche-level acceptance checklist for Phase 2 completion |
| 094 | pending | Verify Phase 2 “CLI-first semantic source of truth” bar is actually met |
| 095 | pending | Document remaining blockers honestly |
| 096 | pending | Prepare next handoff prompt if Phase 2 is still incomplete |
| 097 | pending | Prepare LSP-phase boundary note without starting Phase 3 implementation |
| 098 | pending | Re-run full targeted backend verification suite |
| 099 | pending | Refresh ledger notes and completed tranche summaries |
| 100 | pending | Produce final Phase 2 status read: done or not done, with no bullshit |

## Minimum Checks Per Tranche

Run what matches the risk, but default to:

- `cd backend && pytest -q tests/test_cli.py tests/test_postgres.py tests/test_tracker_service.py`
- `cd backend && PYTHONPYCACHEPREFIX=/tmp/pycache .venv/bin/python -m compileall app`

When CLI behavior changes, also run a live command against `mustardcopilot-71f90466e7`.

## Completion Bar

Phase 2 is done only when:

- semantic index baselines are durable
- semantic freshness or staleness is explicit
- stored semantic rows are readable and used when fresh
- repo-context can answer what changed, what matters, how to run it, and whether semantic state is trustworthy
- changed symbol tracking exists at a credible floor
- impact output exists at a credible floor
- tests pass

If those are not true, Phase 2 is not done.
