# Phase 2 Third Hundred-Tranche Pass

Date: 2026-05-01

Use this file as the next execution prompt for a separate implementation chat.

This prompt starts after the work captured in:

- [docs/prompts/2026-05-01-phase-2-hundred-tranche-pass.md](/Users/for_home/Developer/xMustard/docs/prompts/2026-05-01-phase-2-hundred-tranche-pass.md)
- [docs/prompts/2026-05-01-phase-2-second-hundred-tranche-pass.md](/Users/for_home/Developer/xMustard/docs/prompts/2026-05-01-phase-2-second-hundred-tranche-pass.md)

Phase 2 is still not done. The next chat should focus only on what is left.

## Objective

Finish Phase 2: Structural Intelligence as a CLI-first repo-intelligence and ops-memory tool that another runtime can trust.

The finish line is still:

- semantic state is durable and readable
- semantic freshness is explicit and honest
- repo-context answers what changed, what matters, how to run it, and whether the answer is trustworthy
- impact output is credible
- changed-symbol tracking is credible
- plan/run/file/verification linkage is visible enough to matter in real work

## Current Truth Before This Pass

Already landed across the prior passes:

- `semantic-index plan`
- `semantic-index run`
- `semantic-index status`
- durable `semantic_index_runs` baseline storage
- freshness states: `fresh`, `stale`, `dirty_provisional`, `no_baseline`, `blocked`
- Postgres read helpers for file summaries, symbols, semantic queries, and semantic matches
- `path-symbols` prefers fresh stored rows
- `code-explainer` prefers fresh stored rows
- freshness warnings and evidence provenance in `path-symbols` and `code-explainer`
- `changed-symbols`
- `impact`
- `repo-context`
- issue-context semantic freshness read path

Known live truth from MustardCoPilot remains:

- workspace id: `mustardcopilot-71f90466e7`
- live semantic state is still `blocked`
- Postgres DSN is not configured there
- dirty file count seen in live checks: `189`
- `sg` / `ast-grep` is still unavailable on this machine
- `plan_links` were empty in the live repo-context check
- `latest_accepted_fix` was `null` in the live repo-context check

## What Is Actually Left For Phase 2

This next pass should focus on the remaining honest gaps:

1. retrieval-ledger coverage for semantic path selection and stored evidence
2. richer explanation strings in impact and repo-context
3. stronger plan/file/run linkage in repo-context
4. changed-since-last-run and changed-since-last-accepted-fix floors
5. live semantic materialization with configured Postgres
6. semantic match persistence and replay when `sg` becomes available
7. hybrid lexical plus structural retrieval floor
8. final live validation against MustardCoPilot
9. blunt acceptance audit for whether Phase 2 is actually done

## Hard Rules

Do not drift:

- no web UI work
- no Tauri
- no Phase 3 LSP work
- no fake completion language
- no tracker-first reshaping
- no reverting unrelated dirty work
- keep xMustard separate from MustardCoPilot
- keep the output honest when semantic state is blocked or stale

## Priority Order

The next chat should work in this order:

1. retrieval-ledger and explanation gaps
2. plan/run/file linkage gaps
3. changed-since-last-run and accepted-fix delta floors
4. live Postgres-backed semantic-index run and readback
5. `sg` enablement if possible
6. hybrid retrieval floor
7. live acceptance sweep
8. blunt Phase 2 status call

## Execution Instructions

Work continuously in tranche order unless a real blocker appears.

Use multiple agents if helpful, but keep write scopes separate.

After each tranche:

- update the ledger
- run targeted backend tests
- run compileall when backend logic changes
- run live CLI checks against `mustardcopilot-71f90466e7` whenever a CLI contract changes

Default verification:

```bash
cd backend
pytest -q tests/test_cli.py tests/test_tracker_service.py tests/test_postgres.py
PYTHONPYCACHEPREFIX=/tmp/pycache .venv/bin/python -m compileall app
```

## Third Hundred Ledger

Status legend:

- `done`
- `in_progress`
- `pending`
- `blocked`

| Tranche | Status | Goal |
|---|---|---|
| 201 | done | Add retrieval-ledger entries for semantic index path selection |
| 202 | done | Add retrieval-ledger entries for stored semantic symbol rows |
| 203 | done | Add retrieval-ledger entries for stored semantic match rows |
| 204 | done | Add explicit selection reason strings for symbol evidence |
| 205 | done | Add explicit selection reason strings for semantic-match evidence |
| 206 | done | Add repo-context explanation text for why a file or symbol mattered |
| 207 | done | Add repo-context explanation text for why a plan or run mattered |
| 208 | done | Add repo-context explanation text for why verification targets mattered |
| 209 | done | Add likely affected files from changed symbols if still too shallow |
| 210 | done | Add likely affected tests from changed symbols if still too shallow |
| 211 | done | Tighten impact contract so it explains confidence and derivation source |
| 212 | done | Improve CLI JSON output coverage for `impact` and `repo-context` |
| 213 | done | Add run-plan ownership linkage from changed files |
| 214 | done | Add plan attached-file linkage into repo-context if weak |
| 215 | done | Add latest accepted-fix linkage into repo-context if weak |
| 216 | done | Add changed-since-last-run floor |
| 217 | done | Add changed-since-last-accepted-fix floor |
| 218 | pending | Add summary command for what happened since last semantic index |
| 219 | pending | Add tests for summary command and freshness output |
| 220 | done | Validate repo-context schema can answer agent questions without ad hoc side channels |
| 221 | pending | Persist repo-root and remote capture for sibling-clone mismatch detection if still missing |
| 222 | done | Tighten stale-reason contract if current wording is too loose |
| 223 | done | Add semantic-index activity records with fingerprint and status |
| 224 | done | Re-run live `semantic-index plan` against MustardCoPilot and refine path selection if needed |
| 225 | blocked | Re-run live `semantic-index run` against configured Postgres; blocked because no Postgres DSN is configured |
| 226 | blocked | Verify stored rows exist and read back correctly after live run; blocked because no Postgres DSN is configured |
| 227 | blocked | Re-run live `semantic-index status` against configured Postgres; blocked because no Postgres DSN is configured |
| 228 | blocked | Run live `path-symbols` against stored fresh rows; live output falls back to parser because semantic state is blocked |
| 229 | blocked | Run live `code-explainer` against stored fresh rows; live output falls back to parser because semantic state is blocked |
| 230 | blocked | Run live `changed-symbols` against MustardCoPilot after fresh index; fresh index is blocked |
| 231 | blocked | Run live `impact` against MustardCoPilot after fresh index; fresh index is blocked |
| 232 | blocked | Run live `repo-context` against MustardCoPilot after fresh index; fresh index is blocked |
| 233 | blocked | Install and validate `sg` if environment permits; `sg` and `ast-grep` are unavailable |
| 234 | done | Enable semantic match persistence when `sg` is available |
| 235 | done | Add semantic match replay from stored rows |
| 236 | done | Add semantic match freshness handling |
| 237 | done | Add tests for semantic match persistence |
| 238 | done | Add tests for semantic match replay |
| 239 | done | Add retrieval query contract for lexical plus structural search |
| 240 | done | Add CLI search surface with explainable reasons |
| 241 | done | Add retrieval-ledger source typing for lexical vs structural vs semantic hits |
| 242 | done | Add search document storage shape if BM25 or lexical floor still needs it |
| 243 | pending | Add placeholder likely-owner and hotspot reporting without fake precision |
| 244 | done | Tighten CLI path selection reasons for auditability |
| 245 | pending | Tighten run-target and verify-target seeding into semantic plans if needed |
| 246 | pending | Keep non-code assets out of default CLI semantic plans |
| 247 | pending | Tighten test/spec penalties without suppressing real signal |
| 248 | done | Add structural ranking note to plan output |
| 249 | done | Add lexical ranking note to plan output where relevant |
| 250 | done | Add docs note for live CLI semantic-index usage |
| 251 | done | Add docs note for `changed-symbols`, `impact`, and `repo-context` |
| 252 | done | Update plan docs with actual landed Phase 2 status |
| 253 | pending | Update research-facing notes only if direction materially changed |
| 254 | done | Add fixture coverage for stale index status if touched |
| 255 | done | Add fixture coverage for changed symbol tracking if touched |
| 256 | done | Add fixture coverage for repo-context if touched |
| 257 | pending | Tighten CLI help text for new agent-facing commands |
| 258 | pending | Tighten contract names only where drift is confusing |
| 259 | pending | Add acceptance checklist output for Phase 2 completion bar |
| 260 | done | Verify Phase 2 completion bar against actual code and live behavior |
| 261 | done | Document blockers honestly if Postgres or `sg` still prevent full completion |
| 262 | done | Re-run targeted backend suite |
| 263 | done | Re-run compileall |
| 264 | done | Re-run any new focused tests added during this pass |
| 265 | done | Do final live MustardCoPilot validation sweep |
| 266 | done | Refresh ledger with completed tranche notes |
| 267 | done | Produce blunt current-phase status read |
| 268 | done | Audit whether repo-context really answers what changed and what matters |
| 269 | done | Audit whether impact output is credible enough |
| 270 | done | Audit whether plan ownership and attached-file linkage are visible enough |
| 271 | done | Audit whether blocked and stale states are honest enough for agent use |
| 272 | done | Audit whether CLI output shape is ready for MustardCoPilot tool consumption |
| 273 | blocked | Write final Phase 2 closeout note if complete; Phase 2 is not complete live |
| 274 | done | Write precise remaining-work note if incomplete |
| 275 | done | Prepare next operator prompt from whichever truth is real |
| 276 | done | State plainly whether Phase 2 is done or not done |
| 277 | pending | Keep tranche reserve for unexpected but necessary follow-up |
| 278 | pending | Keep tranche reserve for unexpected but necessary follow-up |
| 279 | pending | Keep tranche reserve for unexpected but necessary follow-up |
| 280 | pending | Keep tranche reserve for unexpected but necessary follow-up |
| 281 | pending | Keep tranche reserve for unexpected but necessary follow-up |
| 282 | pending | Keep tranche reserve for unexpected but necessary follow-up |
| 283 | pending | Keep tranche reserve for unexpected but necessary follow-up |
| 284 | pending | Keep tranche reserve for unexpected but necessary follow-up |
| 285 | pending | Keep tranche reserve for unexpected but necessary follow-up |
| 286 | pending | Keep tranche reserve for unexpected but necessary follow-up |
| 287 | pending | Keep tranche reserve for unexpected but necessary follow-up |
| 288 | pending | Keep tranche reserve for unexpected but necessary follow-up |
| 289 | pending | Keep tranche reserve for unexpected but necessary follow-up |
| 290 | pending | Keep tranche reserve for unexpected but necessary follow-up |
| 291 | pending | Keep tranche reserve for unexpected but necessary follow-up |
| 292 | pending | Keep tranche reserve for unexpected but necessary follow-up |
| 293 | pending | Keep tranche reserve for unexpected but necessary follow-up |
| 294 | pending | Keep tranche reserve for unexpected but necessary follow-up |
| 295 | pending | Keep tranche reserve for unexpected but necessary follow-up |
| 296 | pending | Keep tranche reserve for unexpected but necessary follow-up |
| 297 | pending | Keep tranche reserve for unexpected but necessary follow-up |
| 298 | pending | Keep tranche reserve for unexpected but necessary follow-up |
| 299 | pending | Keep tranche reserve for unexpected but necessary follow-up |
| 300 | pending | Final hard truth check with no bullshit |

## Third Hundred Closeout Note

Phase 2 is not done live.

Landed in this pass:

- semantic-index plan output now includes retrieval-ledger entries for selected paths
- repo-context now emits explicit retrieval-ledger entries for changed files, changed symbols, impact candidates, plans, fixes, and recent activity
- impact now reports derivation source per affected path plus a confidence and derivation summary
- repo-context plan links include plan attached files, step-owned files, dirty paths, ownership mode, and owner labels
- changed-since-last-run and changed-since-last-accepted-fix CLI floors exist
- retrieval-search provides a CLI hybrid lexical plus parser-backed structural floor with source-typed ledger entries
- stored semantic match replay is wired for Postgres-backed rows when they exist

Live MustardCoPilot truth from this pass:

- workspace id: `mustardcopilot-71f90466e7`
- Postgres DSN is still not configured in xMustard settings
- semantic status is still `blocked`
- live dirty file count is still `189`
- `sg` / `ast-grep` is still unavailable
- live `path-symbols` and `code-explainer` work, but they use on-demand parser extraction rather than stored fresh rows
- live `changed-symbols`, `impact`, `repo-context`, and `retrieval-search` run and expose blocked freshness honestly

Remaining Phase 2 bar:

- configure Postgres for the MustardCoPilot workspace
- bootstrap the schema if needed
- run `semantic-index run` live and verify stored rows read back through `status`, `path-symbols`, and `code-explainer`
- install or otherwise provide `sg`, then validate semantic match persistence and replay live
- re-run the final MustardCoPilot acceptance sweep after those two blockers are gone

## Copy-Paste Prompt

Use this in the next execution chat:

```text
Continue Phase 2 in /Users/for_home/Developer/xMustard using /Users/for_home/Developer/xMustard/docs/prompts/2026-05-01-phase-2-third-hundred-tranche-pass.md as the source of truth.

Work only on what is left for Phase 2. Do not drift into web UI, Tauri, or Phase 3 LSP work.

Current truth:
- semantic-index plan/run/status exist
- path-symbols and code-explainer already expose freshness and provenance
- changed-symbols, impact, and repo-context already exist
- live MustardCoPilot semantic state is still blocked because Postgres DSN is not configured
- sg is still unavailable

Your job is to finish the remaining Phase 2 gaps:
- retrieval ledger
- better repo-context and impact explanations
- stronger plan/run/file linkage
- changed-since-last-run and accepted-fix deltas
- live Postgres-backed semantic indexing and readback
- semantic match persistence if sg becomes available
- hybrid lexical plus structural retrieval floor
- final live validation against MustardCoPilot

Run continuously in tranche order. Update the ledger as you finish work. Keep the answer honest: if Postgres or sg still block the full bar, say so.

Default verification:
cd backend
pytest -q tests/test_cli.py tests/test_tracker_service.py tests/test_postgres.py
PYTHONPYCACHEPREFIX=/tmp/pycache .venv/bin/python -m compileall app
```
