# Phase 2 Fourth Hundred-Tranche Pass

Date: 2026-05-01

Use this file as the next execution prompt for a separate implementation chat.

This pass is for the remaining live-validation and completion work after the earlier Phase 2 passes. Do not restart broad feature discovery. Work the blockers that still prevent an honest “done.”

## Objective

Finish Phase 2 live, not just structurally.

The final bar is:

- Postgres-backed semantic indexing runs live
- stored semantic rows can be read back and preferred when fresh
- blocked vs stale vs fresh states are proven in real CLI use
- delta, impact, repo-context, and retrieval surfaces are validated against fresh semantic state
- `sg`-backed semantic match persistence is enabled if the environment permits
- the final status call is blunt and evidence-backed

## Current Truth Before This Pass

Already landed:

- `semantic-index plan`
- `semantic-index run`
- `semantic-index status`
- `path-symbols`
- `code-explainer`
- `changed-symbols`
- `changed-since-last-run`
- `changed-since-last-accepted-fix`
- `impact`
- `repo-context`
- `retrieval-search`
- retrieval-ledger output in the major CLI context surfaces
- plan/file/run/verification linkage in repo-context
- impact derivation metadata

Current live blockers:

- MustardCoPilot workspace id: `mustardcopilot-71f90466e7`
- Postgres DSN is still not configured for live semantic baseline readback
- `sg` / `ast-grep` is still unavailable on this machine
- live semantic state has been `blocked`
- dirty file count has remained `189` in live checks

## What Is Left For Phase 2

This next pass should focus only on these remaining gaps:

1. configure or confirm live Postgres DSN path for the target workspace
2. run live semantic indexing into Postgres
3. verify rows exist and read back as fresh semantic truth
4. validate `path-symbols`, `code-explainer`, `changed-symbols`, `impact`, `repo-context`, and `retrieval-search` against fresh stored rows
5. install and enable `sg` if environment permits
6. prove semantic match persistence and replay if `sg` becomes available
7. run the final MustardCoPilot acceptance sweep
8. state plainly whether Phase 2 is done or still blocked

## Hard Rules

Do not drift:

- no web UI work
- no Tauri
- no Phase 3 LSP work
- no tracker-first reshaping
- no fake “done”
- no reverting unrelated dirty work
- keep xMustard separate from MustardCoPilot
- answer from live truth, not prompt optimism

## Priority Order

Work in this order:

1. live Postgres setup and semantic-index run
2. fresh-row readback validation
3. live CLI acceptance across all semantic consumers
4. `sg` enablement and semantic-match persistence if possible
5. final status audit and next prompt only if still needed

## Execution Instructions

Run continuously in tranche order unless a real blocker appears.

After each tranche:

- update the ledger
- run targeted backend tests when code changes
- run compileall when backend logic changes
- run live CLI checks against `mustardcopilot-71f90466e7` for every changed command surface

Default verification:

```bash
cd backend
pytest -q tests/test_cli.py tests/test_tracker_service.py tests/test_postgres.py
PYTHONPYCACHEPREFIX=/tmp/pycache .venv/bin/python -m compileall app
```

## Fourth Hundred Ledger

Status legend:

- `done`
- `in_progress`
- `pending`
- `blocked`

| Tranche | Status | Goal |
|---|---|---|
| 301 | done | Confirm current Postgres DSN storage path and workspace config for live semantic indexing |
| 302 | done | Configure live Postgres DSN for MustardCoPilot if available in local project settings |
| 303 | done | Re-run `semantic-index plan` against MustardCoPilot and confirm selected paths still make sense |
| 304 | done | Run live `semantic-index run` against configured Postgres |
| 305 | done | Verify semantic baseline row exists after live run |
| 306 | done | Verify file summary rows exist after live run |
| 307 | done | Verify symbol rows exist after live run |
| 308 | done | Verify semantic query rows exist after live run where expected |
| 309 | done | Re-run live `semantic-index status` and confirm it is no longer `blocked` if DSN is configured |
| 310 | done | Validate `path-symbols` prefers fresh stored rows live |
| 311 | done | Validate `code-explainer` prefers fresh stored rows live |
| 312 | done | Validate `changed-symbols` against fresh stored semantic state live |
| 313 | done | Validate `changed-since-last-run` live |
| 314 | done | Validate `changed-since-last-accepted-fix` live |
| 315 | done | Validate `impact` live against fresh stored rows |
| 316 | done | Validate `repo-context` live against fresh stored rows |
| 317 | done | Validate `retrieval-search` live against fresh stored rows |
| 318 | done | Confirm retrieval-ledger output remains honest in fresh state |
| 319 | done | Confirm blocked and stale warnings still behave correctly after fresh baseline exists |
| 320 | done | Install `sg` / `ast-grep` if environment permits |
| 321 | done | Enable live semantic match persistence when `sg` is available |
| 322 | done | Verify semantic match replay from stored rows live |
| 323 | done | Add or adjust tests for semantic match persistence only if code changed |
| 324 | done | Audit whether repo-context explanations are strong enough in live output |
| 325 | done | Audit whether impact derivation is credible in live output |
| 326 | done | Audit whether plan/file/run linkage is populated enough to matter live |
| 327 | done | Tighten docs note for live semantic-index usage if current behavior changed |
| 328 | done | Tighten docs note for delta, impact, repo-context, and retrieval-search if needed |
| 329 | done | Update plan docs with actual live Phase 2 status |
| 330 | done | Re-run targeted backend suite if code changed |
| 331 | done | Re-run compileall if code changed |
| 332 | done | Run final MustardCoPilot acceptance sweep across all Phase 2 CLI surfaces |
| 333 | done | Produce blunt Phase 2 status read: done live or not done live |
| 334 | done | Document blockers honestly if Postgres or `sg` still prevent completion |
| 335 | blocked | Write final Phase 2 closeout note if complete |
| 336 | done | Write next operator prompt only if Phase 2 is still incomplete |
| 337 | pending | Keep tranche reserve for necessary follow-up |
| 338 | pending | Keep tranche reserve for necessary follow-up |
| 339 | pending | Keep tranche reserve for necessary follow-up |
| 340 | pending | Keep tranche reserve for necessary follow-up |
| 341 | pending | Keep tranche reserve for necessary follow-up |
| 342 | pending | Keep tranche reserve for necessary follow-up |
| 343 | pending | Keep tranche reserve for necessary follow-up |
| 344 | pending | Keep tranche reserve for necessary follow-up |
| 345 | pending | Keep tranche reserve for necessary follow-up |
| 346 | pending | Keep tranche reserve for necessary follow-up |
| 347 | pending | Keep tranche reserve for necessary follow-up |
| 348 | pending | Keep tranche reserve for necessary follow-up |
| 349 | pending | Keep tranche reserve for necessary follow-up |
| 350 | pending | Keep tranche reserve for necessary follow-up |
| 351 | pending | Keep tranche reserve for necessary follow-up |
| 352 | pending | Keep tranche reserve for necessary follow-up |
| 353 | pending | Keep tranche reserve for necessary follow-up |
| 354 | pending | Keep tranche reserve for necessary follow-up |
| 355 | pending | Keep tranche reserve for necessary follow-up |
| 356 | pending | Keep tranche reserve for necessary follow-up |
| 357 | pending | Keep tranche reserve for necessary follow-up |
| 358 | pending | Keep tranche reserve for necessary follow-up |
| 359 | pending | Keep tranche reserve for necessary follow-up |
| 360 | pending | Keep tranche reserve for necessary follow-up |
| 361 | pending | Keep tranche reserve for necessary follow-up |
| 362 | pending | Keep tranche reserve for necessary follow-up |
| 363 | pending | Keep tranche reserve for necessary follow-up |
| 364 | pending | Keep tranche reserve for necessary follow-up |
| 365 | pending | Keep tranche reserve for necessary follow-up |
| 366 | pending | Keep tranche reserve for necessary follow-up |
| 367 | pending | Keep tranche reserve for necessary follow-up |
| 368 | pending | Keep tranche reserve for necessary follow-up |
| 369 | pending | Keep tranche reserve for necessary follow-up |
| 370 | pending | Keep tranche reserve for necessary follow-up |
| 371 | pending | Keep tranche reserve for necessary follow-up |
| 372 | pending | Keep tranche reserve for necessary follow-up |
| 373 | pending | Keep tranche reserve for necessary follow-up |
| 374 | pending | Keep tranche reserve for necessary follow-up |
| 375 | pending | Keep tranche reserve for necessary follow-up |
| 376 | pending | Keep tranche reserve for necessary follow-up |
| 377 | pending | Keep tranche reserve for necessary follow-up |
| 378 | pending | Keep tranche reserve for necessary follow-up |
| 379 | pending | Keep tranche reserve for necessary follow-up |
| 380 | pending | Keep tranche reserve for necessary follow-up |
| 381 | pending | Keep tranche reserve for necessary follow-up |
| 382 | pending | Keep tranche reserve for necessary follow-up |
| 383 | pending | Keep tranche reserve for necessary follow-up |
| 384 | pending | Keep tranche reserve for necessary follow-up |
| 385 | pending | Keep tranche reserve for necessary follow-up |
| 386 | pending | Keep tranche reserve for necessary follow-up |
| 387 | pending | Keep tranche reserve for necessary follow-up |
| 388 | pending | Keep tranche reserve for necessary follow-up |
| 389 | pending | Keep tranche reserve for necessary follow-up |
| 390 | pending | Keep tranche reserve for necessary follow-up |
| 391 | pending | Keep tranche reserve for necessary follow-up |
| 392 | pending | Keep tranche reserve for necessary follow-up |
| 393 | pending | Keep tranche reserve for necessary follow-up |
| 394 | pending | Keep tranche reserve for necessary follow-up |
| 395 | pending | Keep tranche reserve for necessary follow-up |
| 396 | pending | Keep tranche reserve for necessary follow-up |
| 397 | pending | Keep tranche reserve for necessary follow-up |
| 398 | pending | Keep tranche reserve for necessary follow-up |
| 399 | pending | Keep tranche reserve for necessary follow-up |
| 400 | done | Final live truth check with no bullshit |

## Fourth Hundred Closeout

Phase 2 is live-validated but not cleanly closed.

Fifth-hundred closeout audit update on 2026-05-01:

- MustardCoPilot is still dirty with 189 `git status --short` entries.
- Default key-files `semantic-index status` is now `stale`, not `fresh`: Postgres remains configured and the baseline exists, but the current selected path/hash inputs no longer match the stored key-files fingerprint while the target worktree is dirty.
- Scoped indexed-path reads still work: `path-symbols` and `code-explainer` on `src/features/git/utils/pullRequestReviewCommands.ts` use `stored_semantic` and report `dirty_provisional`.
- Changed-code surfaces still work provisionally: `changed-symbols` reports 635 symbols, `impact` reports 321 changed file entries and 635 changed symbols with confidence `high`, and `repo-context` reports `dirty_provisional`.
- Missing run/fix anchors and empty plan links are absent MustardCoPilot workspace history, not a new Phase 2 product gap: run records exist but have no saved plans, and no `fix_records.json` exists for this workspace.
- Blunt status: Phase 2 is not cleanly done. It remains dirty/stale provisional until MustardCoPilot is clean and a fresh semantic index can be proven.

What is now proven live on MustardCoPilot:

- xMustard settings have a local Postgres DSN configured and the `xmustard` schema is bootstrapped.
- `semantic-index run` writes Postgres-backed semantic baselines, file summaries, and symbol rows.
- Postgres readback verified semantic index runs, file summaries, symbols, semantic queries, and semantic matches.
- `path-symbols` and `code-explainer` use `stored_semantic` for indexed paths.
- `changed-symbols`, `impact`, and `repo-context` can use widened stored semantic baselines for dirty changed-code surfaces.
- `sg` / `ast-grep` is installed and `semantic-search` returns live matches.
- semantic query and match persistence is proven with stored match replay available to retrieval surfaces.

Why this is not a clean final closeout:

- MustardCoPilot still has 189 dirty files, so the correct live state is `dirty_provisional`, not clean `fresh`.
- `changed-since-last-run` and `changed-since-last-accepted-fix` still have no run/fix anchors in the target workspace, so their live output is a floor rather than a rich historical delta.
- `repo-context` has retrieval-ledger evidence, stored-symbol evidence, and run targets, but live plan links are still empty for this workspace.

Validation evidence from this pass:

- `semantic-index status`: `dirty_provisional`, key-files baseline present, 20 file rows, 58 symbol rows, `ast_grep_available=true`.
- widened changed-code paths baseline: 100 selected paths, 100 file rows, 747 symbol rows, `ast_grep_available=true`.
- `path-symbols`: `stored_semantic`, `dirty_provisional`.
- `code-explainer`: `stored_semantic`, `dirty_provisional`.
- `changed-symbols`: 635 symbols, `stored_semantic`, `dirty_provisional`.
- `impact`: `dirty_provisional`, 321 changed file entries, 635 changed symbols, confidence `high`.
- `repo-context`: `dirty_provisional`, 32 retrieval-ledger entries with `artifact`, `lexical_hit`, and `stored_symbol` evidence.
- `semantic-search`: engine `ast_grep`, binary `/opt/homebrew/bin/sg`, 5 matches.
- Postgres semantic persistence: 4 semantic index run rows, 102 file summary rows, 748 symbol rows, 2 semantic query rows, and 5 semantic match rows after live materialization.
- Targeted backend verification: `93 passed`.
- Compile verification: `PYTHONPYCACHEPREFIX=/tmp/pycache .venv/bin/python -m compileall app` passed.

Seventh-hundred final audit update on 2026-05-02:

- MustardCoPilot is still dirty with 189 `git status --short` entries, so no clean `fresh` status was claimed.
- Default key-files `semantic-index status` remains `stale` with Postgres configured, `current_dirty_files: 189`, key-files baseline present, 20 file rows, 58 symbol rows, and `ast_grep_available=true`.
- Scoped stored-path reads still work: `path-symbols` and `code-explainer` on `src/features/git/utils/pullRequestReviewCommands.ts` use `stored_semantic` and report `dirty_provisional`.
- Changed-code surfaces still work provisionally: `changed-symbols` returns 635 stored semantic symbols, `impact` reports 321 changed files and 635 changed symbols with confidence `high`, and `repo-context` reports `dirty_provisional` through its semantic-status payload.
- `changed-since-last-run` and `changed-since-last-accepted-fix` still have no baseline run or accepted-fix anchor in this workspace; both report 321 changed files and 0 changed symbols from the fallback surface.
- `repo-context` still reports 8 run targets, 32 retrieval-ledger entries, and 0 plan links.
- `retrieval-search` for `pull request review` returns 5 hits and 5 retrieval-ledger entries.
- `semantic-search` through live `sg` / `ast-grep` returns 5 TypeScript function matches.
- No product truth bug appeared in the final sweep, so no code patch or backend test rerun was needed.
- Blunt status: Phase 2 is closed as live-validated but dirty/stale provisional. The only remaining blocker to a clean `fresh` claim is the dirty target MustardCoPilot worktree.

## Copy-Paste Prompt

Use this in the next execution chat:

```text
Continue Phase 2 in /Users/for_home/Developer/xMustard using /Users/for_home/Developer/xMustard/docs/prompts/2026-05-01-phase-2-fourth-hundred-tranche-pass.md as the source of truth.

Focus only on what is left for Phase 2. Do not drift into web UI, Tauri, or Phase 3 LSP work.

Current truth:
- semantic-index plan/run/status exist
- path-symbols, code-explainer, changed-symbols, changed-since-last-run, changed-since-last-accepted-fix, impact, repo-context, and retrieval-search all exist
- local Postgres is configured, the xmustard schema is bootstrapped, and MustardCoPilot semantic baselines/readback are proven live
- sg / ast-grep is installed and semantic match persistence has been proven live
- path-symbols and code-explainer use stored_semantic for indexed paths
- changed-symbols, impact, and repo-context use stored semantic rows when the widened changed-code baseline covers their paths
- MustardCoPilot still has 189 dirty files, so the live semantic state is dirty_provisional, not clean fresh
- changed-since-last-run and changed-since-last-accepted-fix still lack rich run/fix anchors in the target workspace
- repo-context has retrieval-ledger and stored-symbol evidence, but live plan links remain empty

Your job is to do the last clean-closeout audit only:
- do not reinstall or reconfigure Postgres or sg unless they regressed
- check whether the MustardCoPilot worktree is still dirty
- if it is still dirty, state that Phase 2 remains dirty_provisional rather than clean fresh
- if the worktree is clean, rerun semantic-index live and verify status is fresh
- rerun the acceptance sweep across semantic-index, path-symbols, code-explainer, changed-symbols, changed-since-last-run, changed-since-last-accepted-fix, impact, repo-context, retrieval-search, and semantic-search
- update this ledger and the plan doc with the exact live status

Run continuously in tranche order. Keep the answer honest: Postgres and sg were unblocked in the fourth pass, but a dirty MustardCoPilot worktree still prevents a clean fresh Phase 2 closeout.

Default verification when code changes:
cd backend
pytest -q tests/test_cli.py tests/test_tracker_service.py tests/test_postgres.py
PYTHONPYCACHEPREFIX=/tmp/pycache .venv/bin/python -m compileall app
```
