# Phase 2 Second Hundred-Tranche Pass

Date: 2026-05-01

Use this file as the overflow execution prompt for a separate implementation chat after the first Phase 2 hundred-tranche pass.

This file exists because Phase 2 is still not done. The first pass established the CLI semantic-index floor and completed tranches `001` through `015`. This second pass keeps pushing until Phase 2 is actually finished, not cosmetically “advanced.”

## Objective

Finish Phase 2: Structural Intelligence as a CLI-first repo-intelligence and ops-memory tool.

The product bar is still:

- tell an agent what changed
- tell an agent what matters
- show whether semantic state is fresh or stale
- connect symbols, files, plans, runs, and verification
- explain how to run and verify the repo
- stay separate from MustardCoPilot while remaining usable by it as a tool

## Hard Rules

Do not drift from these:

- xMustard stays separate from MustardCoPilot
- current focus is CLI, not web UI
- do not reintroduce Tauri or desktop-app framing
- semantic repo intelligence plus ops memory is the product center
- no tracker-first home screen/product logic
- markdown is for exports and handoff, not the control plane
- prefer typed contracts and durable storage over prompt-only behavior
- do not revert unrelated work in the dirty tree
- keep the code path honest about freshness, staleness, and blocked states

## Current Truth Before This Pass

Already landed:

- `semantic-index plan`
- `semantic-index run`
- `semantic-index status`
- durable `semantic_index_runs` baseline storage
- freshness states: `fresh`, `stale`, `dirty_provisional`, `no_baseline`, `blocked`
- Postgres read helpers for symbol summaries, symbols, semantic queries, and semantic matches
- `path-symbols` prefers fresh stored rows
- `code-explainer` prefers fresh stored rows
- plan tracking already has ownership, versioning, and attached files

Known live truth from MustardCoPilot check:

- workspace id: `mustardcopilot-71f90466e7`
- `semantic-index status` currently reports `blocked`
- reason: Postgres DSN is not configured locally
- dirty file count seen in that run: `189`
- `sg` / `ast-grep` is still unavailable on this machine

## What Must Still Be Built

This pass is for the remaining real work:

1. freshness warnings in semantic consumers
2. retrieval-ledger reason strings
3. dirty symbol tracking
4. impact floor
5. repo-context CLI
6. plan/run linkage into repo-context
7. semantic freshness truth inside issue context
8. changed-since-last-run and changed-since-last-accepted-fix floors
9. semantic match persistence and replay when `sg` becomes available
10. live validation against MustardCoPilot with a configured Postgres target when possible

## Execution Instructions

Run continuously in tranche order unless a real blocker appears.

Use multiple agents where helpful, but keep write scopes separated.

After each tranche:

- update the ledger
- run matching backend tests
- run compileall when backend logic changes
- run a live CLI command against `mustardcopilot-71f90466e7` when a CLI surface changes

Minimum default verification:

```bash
cd backend
pytest -q tests/test_cli.py tests/test_postgres.py tests/test_tracker_service.py
PYTHONPYCACHEPREFIX=/tmp/pycache .venv/bin/python -m compileall app
```

## Tranche Ledger

Status legend:

- `done`
- `in_progress`
- `pending`
- `blocked`

### Second Hundred

| Tranche | Status | Goal |
|---|---|---|
| 101 | done | Land freshness warning output in `path-symbols` when stored semantic rows are stale |
| 102 | done | Land freshness warning output in `code-explainer` when stored semantic rows are stale |
| 103 | pending | Add retrieval-ledger entries for semantic index path selection |
| 104 | pending | Add retrieval-ledger entries for stored semantic symbol rows |
| 105 | pending | Add retrieval-ledger entries for stored semantic match rows |
| 106 | done | Distinguish stored semantic evidence vs on-demand parser evidence in contracts |
| 107 | done | Add explicit selection reason strings for symbol evidence |
| 108 | pending | Add explicit selection reason strings for semantic-match evidence |
| 109 | done | Build changed-symbol derivation from dirty files plus stored symbol rows |
| 110 | done | Add CLI surface for changed symbols |
| 111 | done | Add likely affected files from changed symbols |
| 112 | done | Add likely affected tests from changed symbols |
| 113 | done | Add impact contract for symbol, file, and test impact |
| 114 | done | Add CLI `impact` command |
| 115 | done | Add repo-context contract |
| 116 | done | Add CLI `repo-context` command |
| 117 | done | Include semantic freshness in repo-context |
| 118 | done | Include changed files and changed symbols in repo-context |
| 119 | done | Include run-targets and verify-targets in repo-context |
| 120 | done | Include plan ownership links in repo-context |
| 121 | done | Include recent activity in repo-context |
| 122 | pending | Include latest accepted-fix linkage in repo-context |
| 123 | done | Include plan attached-file linkage in repo-context |
| 124 | pending | Add changed-since-last-run floor |
| 125 | pending | Add changed-since-last-accepted-fix floor |
| 126 | done | Add semantic freshness status read path into issue-context assembly |
| 127 | pending | Make issue context prefer stored symbol evidence when fresh |
| 128 | pending | Keep clean fallback to on-demand extraction when stored state is stale |
| 129 | pending | Add tests for fresh stored symbol use in issue context |
| 130 | pending | Add tests for stale fallback in issue context |
| 131 | pending | Add tests for dirty provisional status handling in consumers |
| 132 | pending | Add tests for fingerprint drift handling in consumers |
| 133 | pending | Persist materialization counts and timestamps in baseline records if still missing |
| 134 | pending | Persist repo-root and remote capture for sibling-clone mismatch detection |
| 135 | pending | Add explicit stale-reason enum or tighter contract |
| 136 | pending | Add CLI summary command for what happened since last semantic index |
| 137 | pending | Add tests for summary command and freshness output |
| 138 | pending | Improve CLI path selection reasons for auditability |
| 139 | pending | Improve CLI surface seeding from run-targets and verify-targets |
| 140 | pending | Keep non-code assets out of default CLI semantic plans |
| 141 | pending | Tighten test/spec penalties without hiding relevant surfaces |
| 142 | pending | Add structural ranking note to plan output |
| 143 | pending | Add lexical ranking note to plan output where relevant |
| 144 | pending | Add run-plan ownership linkage from changed files |
| 145 | pending | Add placeholder likely-owner and hotspot reporting without fake precision |
| 146 | pending | Add repo-context explanation text for why a file or symbol mattered |
| 147 | pending | Add repo-context explanation text for why a plan or run mattered |
| 148 | pending | Add repo-context explanation text for why verification targets mattered |
| 149 | pending | Validate current Postgres schema can serve repo-context without ad hoc side tables |
| 150 | pending | Add CLI JSON output coverage for repo-context and impact |
| 151 | pending | Install and validate `sg` if environment permits |
| 152 | pending | Enable live semantic match persistence when `sg` is available |
| 153 | pending | Add semantic match replay from stored rows |
| 154 | pending | Add semantic match freshness handling |
| 155 | pending | Add tests for semantic match persistence |
| 156 | pending | Add tests for semantic match replay |
| 157 | pending | Add search document storage shape for lexical plus structural retrieval if needed |
| 158 | pending | Add retrieval query contract for lexical plus structural search |
| 159 | pending | Add CLI search surface with explainable reasons |
| 160 | pending | Add retrieval-ledger source typing for lexical vs structural vs semantic hits |
| 161 | pending | Re-run live `semantic-index plan` against MustardCoPilot and refine path selection |
| 162 | pending | Re-run live `semantic-index run` against configured Postgres |
| 163 | pending | Verify rows exist and can be read back from live materialization |
| 164 | pending | Re-run live `semantic-index status` against configured Postgres |
| 165 | pending | Run live `path-symbols` against stored fresh rows |
| 166 | pending | Run live `code-explainer` against stored fresh rows |
| 167 | pending | Run live `changed-symbols` or `impact` check against MustardCoPilot |
| 168 | pending | Run live `repo-context` check against MustardCoPilot |
| 169 | pending | Add docs note for current CLI semantic-index usage |
| 170 | pending | Add docs note for repo-context and impact commands |
| 171 | pending | Update plan docs with actual landed Phase 2 status |
| 172 | pending | Update research-facing notes only if direction materially changed |
| 173 | pending | Add fixture coverage for multi-language symbol materialization if touched |
| 174 | pending | Add fixture coverage for stale index status |
| 175 | pending | Add fixture coverage for changed symbol tracking |
| 176 | pending | Add fixture coverage for repo-context |
| 177 | pending | Tighten CLI help text so agent-facing commands are self-explanatory |
| 178 | pending | Tighten contract names only where drift is confusing |
| 179 | pending | Add semantic-index activity records with fingerprint and status |
| 180 | pending | Add acceptance checklist output for Phase 2 completion bar |
| 181 | pending | Verify Phase 2 completion bar against code, not aspiration |
| 182 | pending | Document blockers honestly if `sg` or Postgres setup still prevents the full bar |
| 183 | pending | Prepare overflow prompt only if Phase 2 is still not done |
| 184 | pending | Write Phase 3 boundary note without starting LSP implementation |
| 185 | pending | Re-run targeted backend suite |
| 186 | pending | Re-run compileall |
| 187 | pending | Re-run any new focused tests added during this pass |
| 188 | pending | Do final live MustardCoPilot validation sweep |
| 189 | pending | Refresh ledger with completed tranche notes |
| 190 | pending | Produce blunt current-phase status read |
| 191 | pending | Audit whether repo-context actually answers what changed and what matters |
| 192 | pending | Audit whether impact output is credible or still too shallow |
| 193 | pending | Audit whether plan ownership and file attachments are visible enough |
| 194 | pending | Audit whether stored semantic rows are truly the preferred source when fresh |
| 195 | pending | Audit whether blocked and stale states are honest enough for agent use |
| 196 | pending | Audit whether CLI naming and output shape are ready for MustardCoPilot tool consumption |
| 197 | pending | Write final Phase 2 closeout note if complete |
| 198 | pending | Write precise remaining-work note if incomplete |
| 199 | pending | Prepare next operator prompt from whichever truth is real |
| 200 | pending | State plainly whether Phase 2 is done or not done, with no bullshit |

## Completion Bar

Phase 2 is done only when all of this is true:

- semantic baselines are durable
- semantic freshness and staleness are explicit
- stored semantic rows are readable and preferred when fresh
- repo-context answers what changed, what matters, how to run it, and whether semantic state is trustworthy
- changed-symbol tracking exists at a credible floor
- impact output exists at a credible floor
- plan, file, run, and verification linkage is visible
- tests pass

If that is not true, Phase 2 is not done.
