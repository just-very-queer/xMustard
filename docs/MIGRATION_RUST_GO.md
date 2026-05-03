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

- Rust now owns a durable no-Python architecture contract in `rust-core/src/contracts.rs`, including the three agent surfaces:
  - works with agents
  - works within agents
  - commands agents
- Rust exposes that contract through `xmustard-core describe-architecture`
- Go now serves the same contract through:
  - `/api/migration/plan`
  - `/api/migration/agent-surfaces`
  - `/api/agent/surfaces`
- The first completed Python request-path cutline is now explicit:
  - `external_integrations_gateway`
  - former Python request-path owner: `backend/app/main.py`
  - current owner: Go plugin registry + webhook/event sink surfaces
  - compatibility logic can remain in `backend/app/service.py` and `backend/app/cli.py` until later non-request-path cleanup
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
- The Go API shell now owns runbook list/save/delete against `runbooks.json`, including settings activity records.
- The Go API shell now owns browser-dump list/save/delete against `browser_dumps.json`, including issue-scoped activity records and export/context inclusion.
- The Go API shell now owns issue-context packet reads at `/api/workspaces/{workspace_id}/issues/{issue_id}/context`.
- The Go API shell now owns issue-work packet reads at `/api/workspaces/{workspace_id}/issues/{issue_id}/work`, including runbook selection.
- The Go API shell now owns issue-context replay list/capture against `context_replays.json`, including replay activity records.
- The Go API shell now owns eval scenario list/save/delete plus workspace eval-report reads against `eval_scenarios.json`, reusing replay comparison, verification profile reports, and persisted run metrics from the existing tracker artifacts.
- The Go API shell now matches the richer eval-report contract for saved guidance/ticket-context variants and workspace-level variant rollups, including runtime/model breakdowns per guidance set or ticket-context set.
- The Go API shell now owns issue-scoped eval-scenario replay requests too, so batch replays and fresh-vs-baseline eval reporting stay on the Go side once scenarios are launched.
- The Go API shell now owns workspace snapshot reads, activity feeds, activity overview, sources, tree browsing, guidance discovery, and repo-map reads against the existing workspace artifacts.
- The Go API shell now owns issue queue reads, issue drift reads, signal queue reads, and workspace drift summary reads from the persisted workspace snapshot.
- The Go API shell now owns issue create/update plus saved-view list/create/update/delete against `snapshot.json`, `tracker_issues.json`, `issue_overrides.json`, and `saved_views.json`, including matching activity ledger entries.
- The Go API shell now owns runtime listing, settings reads/writes, local agent capability reads, and workspace runtime probe flows while preserving the existing `settings.json` contract and runtime model validation behavior.
- The Go API shell now owns Postgres schema plan/render/bootstrap too, including the persisted `postgres_dsn` and `postgres_schema` settings fields needed to drive those endpoints without re-entering Python for foundation delivery.
- The Go API shell now owns issue-run creation, workspace query runs, run listing, run detail reads, run log reads, live run cancel/retry, run plan generate/read/approve/reject, run review submission, run acceptance, run insights, run metrics, workspace metrics, cost summaries, critique generation/read, improvement listing/dismissal, fix listing, fix recording, fix-draft generation, verification listing, and review-queue reads against the persisted run and tracker artifacts.
- Verification profile checklist items and durable verification-profile history now persist through both Python and Go API paths, with confidence scoring attached to each saved execution record.
- The Go API shell now owns verification-profile report reads, including runtime, model, and branch breakdowns that match the richer Python report shape.
- The Go API shell now owns workspace listing, fresh and cached workspace load, explicit workspace scan, worktree reads, export bundle reads, and terminal open/write/resize/read/close transport against the existing workspace registry and terminal log layout.
- Runtime probes now execute through the Rust `run-managed-command` boundary from Go by default, with Go-local subprocess execution retained only as a bridge-unavailable fallback for that bounded probe slice.
- Go-owned run cancellation and terminal close now terminate process groups/known child processes more reliably while the long-lived Rust managed-run/session contract is still missing.
- The Go API shell now owns the first repo-intelligence read tranche for `/api/workspaces/{workspace_id}/impact`, `/api/workspaces/{workspace_id}/repo-context`, and `/api/workspaces/{workspace_id}/retrieval-search`, deriving delivery payloads from workspace artifacts, git state, repo-map records, activity, run plans, and fix records without calling Python service methods.
- Rust now owns the first semantic-impact contract in `rust-core/src/repomap.rs`: changed-symbol extraction, symbol evidence provenance, and likely affected file/test ranking are emitted through `xmustard-core semantic-impact`.
- Go repo-intelligence reads now consume the Rust semantic-impact contract for impact reports and structural retrieval hits; Go remains the delivery shell and no longer carries the parser-lite changed-symbol or impact-ranking authority for these paths.
- Rust now owns the first on-demand path-symbol contract in `rust-core/src/repomap.rs`, exposed through `xmustard-core path-symbols`.
- Go now serves `path-symbols` from Rust semantic-core output, moving single-path symbol meaning out of Python.
- Rust now owns the first code-explainer semantic substrate in `rust-core/src/repomap.rs`, exposed through `xmustard-core explain-path`: path role, line/import counts, detected symbols, summary, hints, evidence source, and selection reason are emitted by Rust.
- Go now serves `explain-path` by forwarding the Rust typed contract, so Go stays the delivery shell while code-explainer meaning moves out of Python and out of Go-local assembly.
- Rust `path-symbols` now also emits storage-ready `file_summary_row` and `symbol_rows` payloads, so on-demand semantic row shaping is no longer a Python-only responsibility when stored semantic baselines are absent.
- Python compatibility `path-symbols` and `code-explainer` now prefer Rust on-demand contracts first and only fall back to local parser shaping if the Rust command is unavailable, keeping Python on a compatibility-only seam instead of a default meaning-owner seam.
- Rust now defines the first normalized diagnostics contract boundary in `rust-core/src/contracts.rs`: Rust owns diagnostic meaning/normalization, Go owns delivery, and Postgres owns durable `diagnostics` rows.
- The Go API shell now owns the `external_integrations_gateway` cutline too: plugin-manifest-backed integration config/test routes, GitHub issue import + PR creation, Slack notifications, and Linear/Jira issue sync now persist through Go while keeping ticket-context/activity artifacts and the existing `backend/data/integrations/` compatibility layout.
- The Go API shell now owns repo-config reads at `/api/workspaces/{workspace_id}/repo-config` and `/api/workspaces/{workspace_id}/repo-config/health`, and issue-context packets built in Go now include `.xmustard.yaml` path instructions and MCP/browser-context hints.
- FastAPI no longer registers the integration config/test/sync HTTP handlers in `backend/app/main.py`, so the external integrations gateway now leaves Python on the live request path while preserving the same route paths through Go.
- The Go API shell now also owns the live semantic-search and Postgres semantic materialization request paths:
  - `/api/workspaces/{workspace_id}/path-symbols/materialize`
  - `/api/workspaces/{workspace_id}/semantic-index/materialize`
  - `/api/workspaces/{workspace_id}/semantic-search`
  - `/api/workspaces/{workspace_id}/semantic-search/materialize`
- FastAPI no longer registers those semantic-search/materialization HTTP handlers in `backend/app/main.py`, so Python is no longer the live request-path owner for this route group.
- The `semantic-index` operator flow now runs through the Go `xmustard-ops` binary for plan/run/status plus baseline freshness/persistence, so Python is no longer the authority path for that shipped CLI slice.
- The Go `xmustard-ops` binary now also exposes shipped workspace and Postgres actions for:
  - `workspace changed-symbols`
  - `postgres plan`
  - `postgres render`
  - `postgres bootstrap`
  - `workspace impact`
  - `workspace repo-context`
  - `workspace retrieval-search`
  - `workspace path-symbols`
  - `workspace explain-path`
  - `workspace semantic-search`
  - `workspace postgres-materialize-path`
  - `workspace postgres-materialize-workspace-symbols`
  - `workspace postgres-materialize-semantic-search`
- The Python Typer CLI keeps the old command names for compatibility, but those commands now delegate to `go run ./cmd/xmustard-ops ...` for the migrated workspace/Postgres/semantic surfaces instead of calling `TrackerService` as the authority.
- FastAPI no longer registers `GET /api/workspaces/{workspace_id}/path-symbols` or `GET /api/workspaces/{workspace_id}/explain-path`; those single-path semantic reads are Go HTTP delivery over Rust semantic-core output.
- Python no longer exposes a public `TrackerService.read_changed_symbols(...)` CLI authority seam; the compatibility CLI delegates `changed-symbols` to Go, which derives it from Rust-backed impact.
- Public `TrackerService` compatibility methods for Postgres foundation, Postgres semantic materialization, semantic-index plan/run/status, repo-intelligence reads, path symbols, explainer, semantic search, and retrieval now delegate to Go `xmustard-ops`; the old Python private helpers for those migrated slices have been removed from `TrackerService`.
- The remaining runtime/session cutline is exact: Go still owns long-lived issue/workspace-query process launch, live log writing, PID tracking, retry/cancel, and run summary persistence; Rust needs a managed-run/session contract before that can move without regressing cancellation and streamed run-log behavior.
- Python still owns broader CLI compatibility, remaining FastAPI routes, `TrackerService` compatibility assembly outside the migrated slices, issue-context semantic pattern derivation, and persistence glue; do not mistake Phase 3 for a full Python exit.
- Python parity tests now compare live Python behavior against the Rust outputs for:
  - signal scanning
  - repo-map summaries
  - LCOV parsing
  - Cobertura parsing
  - Istanbul parsing
  - verification command execution
  - verification profile execution with retries and coverage artifacts

## Recommended Next Build Order

1. Expand the Rust semantic contracts toward stored semantic rows, diagnostics normalization, and later tree-sitter/LSP-backed extraction.
2. Add coverage delta and artifact persistence helpers on the Rust side where it simplifies process control.
3. Extend the Go plugin registry into webhook/event fanout and manifest-first provider callbacks.
4. Migrate FastAPI route groups to Go one surface at a time.
5. Delete Python implementations only after route and artifact parity are verified.
