# Phase 2 Fifth Hundred-Tranche Pass

Date: 2026-05-01

Use this file as the next execution prompt for a separate implementation chat.

This is not another broad build pass. It is a closeout audit and last-mile cleanup pass for Phase 2.

## Objective

Determine whether Phase 2 is actually done live, or whether it remains `dirty_provisional`.

This pass should only work the remaining truth gaps:

- clean-vs-dirty closeout status
- richer historical anchors for delta commands if they can be added honestly
- live plan-link population if it can be improved honestly
- final acceptance wording and evidence

## Current Truth Before This Pass

Already proven live:

- local Postgres is configured for xMustard
- `xmustard` schema is bootstrapped
- `sg` / `ast-grep` is installed
- `semantic-index run` persists live Postgres-backed baselines, file summaries, symbols, semantic queries, and semantic matches
- `path-symbols` and `code-explainer` use `stored_semantic`
- `changed-symbols`, `impact`, and `repo-context` can use widened stored semantic baselines
- `semantic-search` returns live `ast_grep` matches

Current honest limitations:

- MustardCoPilot still had `189` dirty files in the last live check
- the correct semantic state was `dirty_provisional`, not clean `fresh`
- `changed-since-last-run` and `changed-since-last-accepted-fix` still lacked richer run/fix anchors in the target workspace
- `repo-context` still had empty live plan links for that target workspace

## What Is Left For Phase 2

This pass should focus only on these:

1. check whether MustardCoPilot is still dirty
2. if it is clean, rerun semantic indexing and prove `fresh`
3. if it is still dirty, keep the final status `dirty_provisional`
4. inspect whether richer run/fix anchors can now be populated honestly
5. inspect whether plan links can now be populated honestly
6. rerun the full acceptance sweep across all Phase 2 CLI surfaces
7. update the docs/ledger with the exact live status
8. state plainly whether Phase 2 is done or not done

## Hard Rules

Do not drift:

- no web UI work
- no Tauri
- no Phase 3 LSP work
- no fake fresh status if the target repo is dirty
- no fake historical linkage if the workspace does not contain the anchors
- no reverting unrelated dirty work
- keep xMustard separate from MustardCoPilot

## Priority Order

Work in this order:

1. worktree-status truth
2. fresh-vs-dirty semantic status truth
3. delta-anchor truth
4. plan-link truth
5. final acceptance sweep
6. blunt status call

## Execution Instructions

Run continuously in tranche order unless a real blocker appears.

Only change code if the acceptance sweep shows a real product gap. If the remaining truth is just “the target workspace is dirty,” do not invent more feature work.

After each tranche:

- update the ledger
- run targeted backend tests if code changed
- run compileall if backend logic changed
- rerun live CLI checks for any surface whose behavior changed

Default verification when code changes:

```bash
cd backend
pytest -q tests/test_cli.py tests/test_tracker_service.py tests/test_postgres.py
PYTHONPYCACHEPREFIX=/tmp/pycache .venv/bin/python -m compileall app
```

## Fifth Hundred Ledger

Status legend:

- `done`
- `in_progress`
- `pending`
- `blocked`

| Tranche | Status | Goal |
|---|---|---|
| 401 | done | Rechecked MustardCoPilot worktree state: still 189 dirty files |
| 402 | done | Re-ran `semantic-index status`: default key-files status is now `stale`, Postgres configured, 189 dirty files |
| 403 | blocked | Did not rerun `semantic-index run` for a clean proof because the target worktree is not clean |
| 404 | done | Kept closeout provisional; no fake `fresh` status |
| 405 | done | `path-symbols` on an indexed path still uses `stored_semantic` with `dirty_provisional` |
| 406 | done | `code-explainer` on an indexed path still uses `stored_semantic` with `dirty_provisional` |
| 407 | done | `changed-symbols` returned 635 symbols with `dirty_provisional` and mixed `stored_semantic` / `on_demand_parser` evidence |
| 408 | done | `changed-since-last-run` uses the latest run worktree head as base and returns 321 changed entries; richer run plan anchors are absent |
| 409 | done | `changed-since-last-accepted-fix` falls back to `HEAD` and returns 321 changed entries because there is no accepted-fix record for this workspace |
| 410 | done | `impact` remains credible: 321 changed entries, 635 symbols, confidence `high`, semantic status `dirty_provisional` |
| 411 | done | `repo-context` returns 8 run targets, 8 verify targets, 32 retrieval-ledger entries, 0 plan links, and no latest accepted fix |
| 412 | done | `retrieval-search` returned lexical and structural hits; `semantic-search` returned ast-grep matches for TypeScript and Rust |
| 413 | done | Missing run/fix anchors are absent workspace history, not a product bug: runs exist but have no saved plans; no fix records file exists |
| 414 | done | Empty plan links are absent workspace plan ownership data, not a product bug |
| 415 | done | No narrow code gap surfaced in the acceptance sweep |
| 416 | done | Skipped targeted backend tests because no code changed |
| 417 | done | Skipped compileall because no backend logic changed |
| 418 | done | Updated the fourth-hundred closeout note with exact current live status |
| 419 | done | Updated the repo-cockpit plan note with exact current live status |
| 420 | done | Final blunt status: Phase 2 is not cleanly done; it is live-validated but still dirty/stale provisional |
| 421 | blocked | Phase 2 is not truly done, so no Phase 3 boundary prompt was written |
| 422 | done | Next operator prompt kept to the real remaining blockers |
| 423 | pending | Keep tranche reserve for necessary follow-up |
| 424 | pending | Keep tranche reserve for necessary follow-up |
| 425 | pending | Keep tranche reserve for necessary follow-up |
| 426 | pending | Keep tranche reserve for necessary follow-up |
| 427 | pending | Keep tranche reserve for necessary follow-up |
| 428 | pending | Keep tranche reserve for necessary follow-up |
| 429 | pending | Keep tranche reserve for necessary follow-up |
| 430 | pending | Keep tranche reserve for necessary follow-up |
| 431 | pending | Keep tranche reserve for necessary follow-up |
| 432 | pending | Keep tranche reserve for necessary follow-up |
| 433 | pending | Keep tranche reserve for necessary follow-up |
| 434 | pending | Keep tranche reserve for necessary follow-up |
| 435 | pending | Keep tranche reserve for necessary follow-up |
| 436 | pending | Keep tranche reserve for necessary follow-up |
| 437 | pending | Keep tranche reserve for necessary follow-up |
| 438 | pending | Keep tranche reserve for necessary follow-up |
| 439 | pending | Keep tranche reserve for necessary follow-up |
| 440 | pending | Keep tranche reserve for necessary follow-up |
| 441 | pending | Keep tranche reserve for necessary follow-up |
| 442 | pending | Keep tranche reserve for necessary follow-up |
| 443 | pending | Keep tranche reserve for necessary follow-up |
| 444 | pending | Keep tranche reserve for necessary follow-up |
| 445 | pending | Keep tranche reserve for necessary follow-up |
| 446 | pending | Keep tranche reserve for necessary follow-up |
| 447 | pending | Keep tranche reserve for necessary follow-up |
| 448 | pending | Keep tranche reserve for necessary follow-up |
| 449 | pending | Keep tranche reserve for necessary follow-up |
| 450 | pending | Keep tranche reserve for necessary follow-up |
| 451 | pending | Keep tranche reserve for necessary follow-up |
| 452 | pending | Keep tranche reserve for necessary follow-up |
| 453 | pending | Keep tranche reserve for necessary follow-up |
| 454 | pending | Keep tranche reserve for necessary follow-up |
| 455 | pending | Keep tranche reserve for necessary follow-up |
| 456 | pending | Keep tranche reserve for necessary follow-up |
| 457 | pending | Keep tranche reserve for necessary follow-up |
| 458 | pending | Keep tranche reserve for necessary follow-up |
| 459 | pending | Keep tranche reserve for necessary follow-up |
| 460 | pending | Keep tranche reserve for necessary follow-up |
| 461 | pending | Keep tranche reserve for necessary follow-up |
| 462 | pending | Keep tranche reserve for necessary follow-up |
| 463 | pending | Keep tranche reserve for necessary follow-up |
| 464 | pending | Keep tranche reserve for necessary follow-up |
| 465 | pending | Keep tranche reserve for necessary follow-up |
| 466 | pending | Keep tranche reserve for necessary follow-up |
| 467 | pending | Keep tranche reserve for necessary follow-up |
| 468 | pending | Keep tranche reserve for necessary follow-up |
| 469 | pending | Keep tranche reserve for necessary follow-up |
| 470 | pending | Keep tranche reserve for necessary follow-up |
| 471 | pending | Keep tranche reserve for necessary follow-up |
| 472 | pending | Keep tranche reserve for necessary follow-up |
| 473 | pending | Keep tranche reserve for necessary follow-up |
| 474 | pending | Keep tranche reserve for necessary follow-up |
| 475 | pending | Keep tranche reserve for necessary follow-up |
| 476 | pending | Keep tranche reserve for necessary follow-up |
| 477 | pending | Keep tranche reserve for necessary follow-up |
| 478 | pending | Keep tranche reserve for necessary follow-up |
| 479 | pending | Keep tranche reserve for necessary follow-up |
| 480 | pending | Keep tranche reserve for necessary follow-up |
| 481 | pending | Keep tranche reserve for necessary follow-up |
| 482 | pending | Keep tranche reserve for necessary follow-up |
| 483 | pending | Keep tranche reserve for necessary follow-up |
| 484 | pending | Keep tranche reserve for necessary follow-up |
| 485 | pending | Keep tranche reserve for necessary follow-up |
| 486 | pending | Keep tranche reserve for necessary follow-up |
| 487 | pending | Keep tranche reserve for necessary follow-up |
| 488 | pending | Keep tranche reserve for necessary follow-up |
| 489 | pending | Keep tranche reserve for necessary follow-up |
| 490 | pending | Keep tranche reserve for necessary follow-up |
| 491 | pending | Keep tranche reserve for necessary follow-up |
| 492 | pending | Keep tranche reserve for necessary follow-up |
| 493 | pending | Keep tranche reserve for necessary follow-up |
| 494 | pending | Keep tranche reserve for necessary follow-up |
| 495 | pending | Keep tranche reserve for necessary follow-up |
| 496 | pending | Keep tranche reserve for necessary follow-up |
| 497 | pending | Keep tranche reserve for necessary follow-up |
| 498 | pending | Keep tranche reserve for necessary follow-up |
| 499 | pending | Keep tranche reserve for necessary follow-up |
| 500 | pending | Final closeout truth check with no bullshit |

## Fifth Hundred Closeout Audit

Phase 2 is not cleanly done.

Exact live status from the 2026-05-01 closeout audit:

- MustardCoPilot is still dirty: `git status --short` reports 189 entries.
- Because the target worktree is dirty, no clean `semantic-index run` was executed to prove `fresh`.
- Default `semantic-index status` is currently `stale`, not `fresh`: the stored key-files baseline exists, Postgres is configured, but the current selected path/hash inputs no longer match the stored key-files fingerprint while the worktree remains dirty.
- Scoped stored-path reads still work: `path-symbols` and `code-explainer` on `src/features/git/utils/pullRequestReviewCommands.ts` use `stored_semantic` and report `dirty_provisional`.
- `changed-symbols` returns 635 symbols with `dirty_provisional` across mixed `stored_semantic` and `on_demand_parser` evidence.
- `impact` reports 321 changed file entries, 635 changed symbols, confidence `high`, and `dirty_provisional`.
- `repo-context` reports `dirty_provisional`, 8 run targets, 8 verify targets, 32 retrieval-ledger entries, 0 plan links, and no latest accepted fix.
- `retrieval-search` returns lexical and structural hits with retrieval-ledger entries.
- `semantic-search` returns live ast-grep matches for both TypeScript and Rust patterns.

Anchor/link truth:

- Missing richer run anchors are absent workspace history, not a Phase 2 product gap. MustardCoPilot has run records, but they are cancelled and have no saved plans or plan-owned files.
- Missing accepted-fix anchors are absent workspace history, not a Phase 2 product gap. There is no `fix_records.json` for the MustardCoPilot workspace.
- Empty plan links are absent workspace plan ownership data, not a Phase 2 product gap.

Blunt status:

Sixth-hundred final recheck:

- MustardCoPilot is still dirty with `189` dirty files.
- Broad key-files `semantic-index status` is still `stale`.
- Scoped stored-path and changed-code surfaces still report `dirty_provisional`.
- `changed-since-last-run` and `changed-since-last-accepted-fix` still return `321` changed entries; accepted-fix fallback remains `HEAD` because the workspace has no accepted fix record.
- `changed-symbols` still returns `635` symbols.
- `impact` still reports `321` changed entries, `635` changed symbols, confidence `high`, 8 likely affected files, and 8 likely affected tests.
- `repo-context` still reports `dirty_provisional`, 8 run targets, 8 verify targets, 32 retrieval-ledger entries, 0 plan links, and no latest accepted fix.
- `retrieval-search` returns lexical and structural hits with retrieval-ledger entries.
- `semantic-search` runs through live `sg` / `ast-grep`; a TypeScript function query returned 5 matches and stored query/match rows.

Phase 2 has working live infrastructure and working CLI surfaces, but it is not a clean `fresh` closeout until MustardCoPilot is clean and a fresh semantic index can be proven. The final honest state is dirty/stale provisional: stored-path and changed-code surfaces can still return `dirty_provisional`, but broad key-files `semantic-index status` is `stale`.

## Copy-Paste Prompt

Use this in the next execution chat:

```text
Continue Phase 2 closeout in /Users/for_home/Developer/xMustard using /Users/for_home/Developer/xMustard/docs/prompts/2026-05-01-phase-2-fifth-hundred-tranche-pass.md as the source of truth.

This is only a closeout recheck after MustardCoPilot changes. Do not do broad implementation.

Current truth:
- local Postgres is configured and live semantic indexing/readback has already been proven
- sg / ast-grep is installed and semantic search works
- MustardCoPilot was still dirty in the fifth-hundred audit: 189 dirty files
- broad key-files `semantic-index status` was `stale`
- scoped stored-path and changed-code surfaces still reported `dirty_provisional`
- missing run/fix anchors and empty plan links were absent workspace history, not proven product gaps

Your job:
- check whether MustardCoPilot is clean
- if clean, rerun `semantic-index run` and prove `semantic-index status` is `fresh`
- if still dirty, do not claim Phase 2 is cleanly done
- rerun only the closeout CLI sweep needed to prove current status
- update docs with the exact live state
- state plainly whether Phase 2 is cleanly done

Do not drift into web UI, Tauri, or Phase 3 LSP work.
Do not reinstall Postgres or sg unless they have regressed.
Do not invent feature work unless a new acceptance sweep shows a real product gap.

Default verification only if code changes:
cd backend
pytest -q tests/test_cli.py tests/test_tracker_service.py tests/test_postgres.py
PYTHONPYCACHEPREFIX=/tmp/pycache .venv/bin/python -m compileall app
```
