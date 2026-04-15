# Rust Core And Go API Migration

This document turns the existing "move backend-heavy subsystems toward Rust" roadmap into a concrete implementation lane for this repo.

## Current State

Today xMustard is a Python FastAPI backend with a React frontend:

- `backend/app/service.py` owns most orchestration
- `backend/app/scanners.py` owns repo scanning and repo-map summary building
- `backend/app/runtimes.py` owns runtime launch, metrics, and process control
- `backend/app/store.py` owns file-backed persistence
- `backend/app/main.py` exposes the full HTTP surface

That structure is productive, but the service layer is carrying too many responsibilities at once.

## Target Shape

The migration direction in this repo is now:

1. Keep the frontend contracts stable.
2. Move heavy backend subsystems into a Rust core library first.
3. Move the HTTP/API shell into Go once the Rust boundaries are stable.
4. Keep persistence and contract shapes inspectable while we transition.

That gives us:

- Rust where indexing, repo maps, retrieval, verification orchestration, and process-heavy logic benefit most
- Go where a small operational API shell can stay simple, portable, and easy to deploy
- React unchanged for as long as possible

## Do Not Do A Blind Flag-Day Rewrite

A true one-shot rewrite from Python to Rust plus a simultaneous API rewrite to Go would be very high risk because it combines:

- domain migration
- storage migration
- process-control migration
- API migration
- product regression risk

The safer interpretation of "one-shot" for this repo is:

- define the end-state now
- scaffold the new lanes now
- move ownership by subsystem instead of by file

## Boundary Plan

### Rust core ownership

The Rust lane should own:

- scanner signals
- ledger and verdict ingestion
- repo-map generation
- structural retrieval and ranking
- verification execution helpers
- runtime-safe process orchestration helpers
- durable contract structs once stabilized

### Go API ownership

The Go lane should own:

- HTTP routing
- request validation and response shaping
- health and readiness
- workspace-level orchestration
- auth, policy, and future team-facing middleware
- calling into Rust services or binaries

### Keep in Python only until replaced

The current Python backend should remain the source of truth for:

- the live API surface
- existing workspace persistence behavior
- existing UI integrations

## Current Python To Target Mapping

| Current module | Near-term owner | End-state note |
|---|---|---|
| `backend/app/scanners.py` | Rust core | first migration target |
| `backend/app/runtimes.py` | Rust core | move process-heavy execution here |
| `backend/app/service.py` context/retrieval code | Rust core | split by subsystem before full replacement |
| `backend/app/store.py` | Go API first, Rust-aware later | keep file formats stable during migration |
| `backend/app/main.py` | Go API | replace route-by-route once parity exists |
| `frontend/src/lib/api.ts` | unchanged contract surface | switch backend target behind the same calls |

## Aider Takeaways Worth Copying

Reading the local `research/aider` repo reinforces a few decisions:

### 1. Repo map is a first-class subsystem

Useful files:

- `research/aider/aider/repomap.py`
- `research/aider/aider/coders/base_coder.py`

Takeaway:

- repo structure should not be an incidental scan result
- it should become a dedicated service with its own refresh and ranking logic

### 2. Git hygiene matters operationally

Useful files:

- `research/aider/aider/repo.py`
- `research/aider/aider/commands.py`
- `research/aider/aider/main.py`

Takeaway:

- commit checkpoints, dirty-worktree awareness, and `.gitignore` hygiene are part of the product, not just local developer habits

### 3. Lint/test loops belong near edit execution

Useful files:

- `research/aider/aider/linter.py`
- `research/aider/aider/commands.py`

Takeaway:

- verification orchestration should sit close to runtime/process control, which is a strong argument for the Rust core lane

## First Migration Deliverables In This Repo

This repo now carries:

- `rust-core/` for the future compiled core
- `api-go/` for the future HTTP/API shell
- `api-go/internal/migration/api_route_groups.json` as the first explicit route-group inventory
- `.gitignore` to keep local mirrors and generated runtime noise out of git

The first implementation goal is not parity. It is boundary clarity.

That boundary work is now live for three concrete slices:

- Rust owns `scan-signals` in `rust-core/src/scanner.rs`
- Rust owns `build-repo-map` in `rust-core/src/repomap.rs`
- Rust owns coverage parsing for LCOV, Cobertura, and Istanbul in `rust-core/src/verification.rs`
- Rust now also owns a timeout-aware `run-verification-command` process runner in `rust-core/src/verification.rs`
- Rust now also owns profile-aware verification execution with retries and optional coverage parsing in `rust-core/src/verification.rs`
- Rust exposes those capabilities through `rust-core/src/bin/xmustard-core.rs`
- Go can invoke them through:
  - `/api/migration/scan-signals`
  - `/api/migration/repo-map`
  - `/api/migration/coverage`
  - `/api/migration/coverage/lcov`
  - `/api/migration/verification/run`
  - `/api/migration/verification/profile-run`
- Python can optionally delegate scanner and repo-map work to Rust behind:
  - `XMUSTARD_USE_RUST_SCANNER=1`
  - `XMUSTARD_USE_RUST_REPOMAP=1`
- Python can optionally delegate coverage parsing to Rust behind:
  - `XMUSTARD_USE_RUST_COVERAGE=1`
- Python can optionally delegate verification command execution to Rust behind:
  - `XMUSTARD_USE_RUST_VERIFICATION=1`
- The live Python API can now run saved verification profiles against an issue and persist coverage/activity through the existing tracker artifacts.
- The Go API shell now owns the same issue-level verification profile run route and writes snapshot, activity, and coverage artifacts into the existing backend data layout.
- The Go API shell now also owns coverage parse, latest-coverage lookup, and coverage-delta calculation against the same persisted coverage artifacts.
- The Go API shell now owns verification profile list/save/delete against `verification_profiles.json`, including settings activity records and a built-in fallback profile.
- The Go API shell now owns ticket-context list/save/delete against `ticket_contexts.json`, including issue-scoped activity records.
- The Go API shell now owns threat-model list/save/delete against `threat_models.json`, including issue-scoped activity records.
- The Go API shell now owns issue-context packet reads at `/api/workspaces/{workspace_id}/issues/{issue_id}/context`.
- The Go API shell now owns issue-work packet reads at `/api/workspaces/{workspace_id}/issues/{issue_id}/work`, including runbook selection.
- The Go API shell now owns issue-context replay list/capture against `context_replays.json`, including replay activity records.
- The Go API shell now owns workspace snapshot reads, activity feeds, activity overview, sources, tree browsing, guidance discovery, and repo-map reads against the existing workspace artifacts.
- The Go API shell now owns issue queue reads, issue drift reads, signal queue reads, and workspace drift summary reads from the persisted workspace snapshot.
- The Go API shell now owns issue create/update plus saved-view list/create/update/delete against `snapshot.json`, `tracker_issues.json`, `issue_overrides.json`, and `saved_views.json`, including matching activity ledger entries.
- The Go API shell now owns runtime listing, settings reads/writes, local agent capability reads, and workspace runtime probe flows while preserving the existing `settings.json` contract and runtime model validation behavior.
- The Go API shell now owns issue-run creation, workspace query runs, run listing, run detail reads, run log reads, live run cancel/retry, run plan generate/read/approve/reject, run review submission, run acceptance, run insights, run metrics, workspace metrics, cost summaries, critique generation/read, improvement listing/dismissal, fix listing, fix recording, fix-draft generation, verification listing, and review-queue reads against the persisted run and tracker artifacts.
- The Go API shell now owns workspace listing, fresh and cached workspace load, explicit workspace scan, worktree reads, export bundle reads, and terminal open/write/resize/read/close transport against the existing workspace registry and terminal log layout.
- Python parity tests now compare live Python behavior against the Rust outputs for:
  - signal scanning
  - repo-map summaries
  - LCOV parsing
  - Cobertura parsing
  - Istanbul parsing
  - verification command execution
  - verification profile execution with retries and coverage artifacts

## Recommended Next Build Order

1. Add coverage delta and artifact persistence helpers on the Rust side where it simplifies process control.
2. Migrate the remaining provider integration surfaces to Go.
3. Migrate FastAPI route groups to Go one surface at a time.
4. Delete Python implementations only after route and artifact parity are verified.
