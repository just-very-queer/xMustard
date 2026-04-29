# Changelog

All notable changes to xMustard will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/).

## [Unreleased]

### Changed
- `docs/RESEARCH_FINDINGS.md` and `docs/RESEARCH_MATRIX.md` now reflect shipped guidance, eval, verification, vulnerability, and Go/Rust migration work instead of treating those lanes as still missing.
- `docs/PLANNING.md` now tracks the next strategic lanes more explicitly:
  - threat modeling and security review
  - confidence and ticket compliance
  - semantic retrieval and issue intelligence
  - agent operations, insights, and governance
  - incremental backend migration away from Python toward a Rust-based core
- `docs/FEATURES.md`, `docs/ARCHITECTURE.md`, and `README.md` now reflect that xMustard is moving toward stronger trust, retrieval, governance, and backend-platform planning instead of simply expanding UI surface area
- backend migration planning now explicitly tracks a Rust core plus a possible Go API shell instead of assuming the HTTP layer must stay in Python until the very end
- the no-Python migration target now has a durable architecture contract, explicit three-surface agent model, and a completed `external_integrations_gateway` cutover on the FastAPI request path
- FastAPI no longer registers the integration config/test/sync endpoints, leaving those existing route paths under Go ownership while Python retains only non-request-path compatibility helpers

### Added
- Research frontier map in `docs/FRONTIER.md`, turning the local research synthesis into current build lanes for retrieval, symbol-aware context, eval timelines, security review depth, policy records, and review packet export.
- ADR `docs/plans/2026-04-18-no-python-control-plane-adr.md` locking in the no-Python target architecture: Go control-plane shell, Rust runtime/retrieval/store core, sub-500MB steady-state target, and the three agent surfaces (`works with agents`, `works within agents`, `commands agents`)
- Rust-owned architecture contract in `rust-core/src/contracts.rs` plus `xmustard-core describe-architecture`
- Go-served architecture and agent-surface inventory endpoints at `/api/migration/plan`, `/api/migration/agent-surfaces`, and `/api/agent/surfaces`
- Go-owned plugin-manifest registry plus provider integration config/test/sync routes for GitHub, Slack, Linear, and Jira, including durable activity and ticket-context artifacts behind the existing integration endpoints
- issue-level vulnerability import batch persistence via `vulnerability_import_batches.json`, including source/scanner provenance, imported finding ids, payload hash, and lifecycle summary counts for `new` / `existing` / `resolved` / `regressed`
- issue-level vulnerability import batch inspection through Python service, FastAPI route `/api/workspaces/{workspace_id}/issues/{issue_id}/vulnerability-import-batches`, and matching CLI command `vulnerability-import-batches`
- vulnerability import activity now records the durable import batch id alongside SARIF and Nessus imports
- repo-native `.xmustard.yaml` support for path-specific instructions, code-guideline references, MCP/browser-context hints, and issue-context prompt attachment
- Go parity for `.xmustard.yaml` repo-config reads and issue-context prompt attachment, including matched path instructions inside Go-built issue packets
- Issue-level threat model artifacts with backend CRUD, prompt integration, export support, and issue detail editing
- Research notes now include external security/trust references such as OWASP Threat Dragon, OWASP pytm, Semgrep Code, GitHub code scanning, and Vulnhuntr patterns
- Workspace metadata under `backend/data/` now points the self-workspace at `/Users/for_home/Developer/xMustard`
- `.gitignore` coverage for local mirrors, generated run logs, and OS junk
- initial migration working note in `docs/MIGRATION_RUST_GO.md`
- initial `rust-core/` scaffolding for scanner, repo-map, verification, and contract boundaries
- initial `api-go/` scaffolding for the future HTTP/API shell
- route-group migration inventory parity test in `backend/tests/test_api_route_inventory.py`
- Rust scanner CLI plus Python parity and adapter-path tests
- Rust repo-map CLI plus Python parity and adapter-path tests
- Rust coverage parsers for LCOV, Cobertura, and Istanbul plus Python parity and adapter-path tests
- Rust verification command runner plus Python parity and adapter-path tests
- Rust verification profile runner with retries and optional coverage artifact parsing
- Go migration endpoints for scanner, repo-map, and generic coverage parsing
- Go migration endpoint for verification command execution
- Go migration endpoint for verification profile execution
- issue-level verification profile execution endpoint and tracker persistence for coverage/activity artifacts
- Go API shell support for the issue-level verification profile run workflow with snapshot/activity/coverage writes
- Go API shell support for coverage parse, latest coverage lookup, and coverage delta from persisted tracker artifacts
- Go API shell support for verification profile list/save/delete with persisted settings activity
- Go API shell support for ticket-context list/save/delete with persisted issue activity
- Go API shell support for threat-model list/save/delete with persisted issue activity
- Go API shell support for runbook list/save/delete with persisted settings activity
- Go API shell support for browser-dump list/save/delete with persisted issue activity plus issue-context/export inclusion
- Go API shell support for issue-context packet reads, issue-work packet reads with runbook selection, and issue-context replay list/capture against persisted tracker artifacts
- Go API shell support for workspace snapshot reads, activity feeds, activity overview, sources, tree browsing, guidance discovery, and repo-map reads against persisted workspace artifacts
- Go API shell support for issue queue reads, issue drift reads, signal queue reads, and workspace drift summary reads against persisted workspace snapshots
- Go API shell support for issue create/update and saved-view CRUD against persisted tracker artifacts with matching activity records
- Go API shell support for live run cancel/retry, run plan generate/read/approve/reject, run listing/detail/log reads, run review submission, run acceptance, run insights, metrics/cost reads, critique generation/read, improvement dismissal, fix listing/recording, fix-draft generation, verification listing, and review-queue reads against persisted run and tracker artifacts
- Go API shell support for runtime listing, settings reads/writes, local agent capability reads, workspace runtime probe flows, issue-run creation, and workspace query runs against the existing settings and run artifact contracts
- Go API shell support for workspace listing, cached workspace load, worktree reads, export bundle reads, and terminal open/write/resize/read/close transport against the existing workspace registry and terminal log contracts
- Go API shell support for fresh workspace scan/load and explicit `/scan` snapshot rebuilds using Rust-backed signal and repo-map generation plus Go-side tracker artifact assembly
- Python CLI parity expansion for verification profiles, ticket context, threat models, context replays, plan/metrics/coverage/critique flows, integrations, repo inspection, and terminal transport
- Durable browser-dump artifacts for MCP/manual browser debugging context, now attached to issue context packets and export bundles
- Guidance authoring workflow with starter generation for `AGENTS.md`, `.openhands/microagents/repo.md`, and `CONVENTIONS.md`, plus workspace guidance health reporting
- Issue-context replay comparison across prompt content, tree focus, guidance, verification profiles, ticket contexts, and browser dumps, now surfaced in the API, CLI, and issue detail UI
- Verification-profile checklist items, confidence scoring, and durable per-profile execution history, now surfaced in Python, Go, CLI, and the issue detail UI
- Verification-profile reports now break execution history down by runtime, model, and branch, with matching Go API ownership and issue detail UI summaries
- Dynamic issue context now includes symbol ranking and retrieval-backed related artifacts from ticket context, threat models, browser dumps, fixes, and activity
- Eval scenarios and workspace eval reports now correlate replay drift, verification success, and run metrics through the Python API and CLI
- Eval scenarios now persist saved guidance-path and ticket-context variant selections, and eval reports now expose current-vs-saved variant drift summaries across both Python and Go
- Eval workspace reports now group outcomes by saved guidance sets and ticket-context sets with unique-run rollups, deterministic Go parity, and selected-value/cost detail surfaced in the UI
- Eval scenario reports now include baseline-aware comparisons for the same issue, surfacing guidance/ticket/browser/profile deltas and weighted preference reasons without adding new eval routes
- Eval baseline comparisons now include per-profile verification history deltas across saved variants, including runs, success rate, checklist pass rate, attempt count, and confidence-count changes in both Python and Go
- Saved eval scenarios can now launch fresh queued runs through the normal issue-run flow, with scenario overlays applied to prompt context and new run IDs appended back into the scenario record
- Eval scenarios can now be batch replayed per issue, and eval reports now include each scenario's latest fresh run plus fresh execution comparisons back to the issue baseline scenario
- Eval workspace reports now rank fresh replay outcomes across all saved scenarios for an issue, using pairwise fresh-run wins/losses/ties plus deterministic cost and duration tie-breaks in both Python and Go
- Eval workspace reports now show fresh replay rank movement versus the previous fresh replay snapshot for each scenario across Python, Go, and the UI
- Eval replay batches now persist as durable artifacts, and batch-backed trend comparisons now prefer latest-batch vs previous-batch movement before falling back to per-scenario fresh history
- Replay trend views now expose explicit latest-batch and previous-batch ids, making batch history inspectable across Python, Go, and the UI
- The Go API shell now owns eval scenario CRUD and workspace eval-report reads, with route registration moved into a dedicated registrar instead of extending the main HTTP mux tree inline
- Run insights and patch critique now include acceptance-criteria review plus scope/unrelated-change warnings derived from ticket context and worktree state

## [0.2.0] - 2026-04-14

### Added
- Repository guidance discovery for:
  - `AGENTS.md`
  - `CONVENTIONS.md`
  - `.devin/wiki.json`
  - `.openhands/skills/*.md`
  - `.agents/skills/*.md`
  - `.cursor/rules/*.mdc`
- OpenHands-style repo microagent support for `.openhands/microagents/repo.md` and related markdown files
- Run insight summaries that capture guidance used, strengths, risks, and recommendations
- Workspace guidance API and run insight API
- Workspace verification profile API for saved test and coverage commands
- Ticket-context API for issue-level upstream references and acceptance criteria
- Issue-context replay API for saved prompt snapshots
- Repo-map API for workspace structural summaries
- UI sections for repository guidance and run insights
- UI sections for verification profiles in the execution drawer and issue detail
- UI section for ticket context editing and inspection in issue detail
- UI section for issue-context replay capture and inspection
- UI section for repo-map summaries and ranked related paths
- Guidance onboarding in the sidebar, topbar, and workspace empty state
- Research synthesis document: `docs/RESEARCH_FINDINGS.md`
- Repo-by-repo research matrix: `docs/RESEARCH_MATRIX.md`
- First-party repo guidance files:
  - `AGENTS.md`
  - `.openhands/microagents/repo.md`

### Changed
- Scanner behavior now avoids noisy self-referential matches from research and generated directories
- Planning, cost, triage, verification, and review features are now reflected in the UI much more completely
- Verification commands are now treated as saved workspace artifacts instead of ad hoc operator text
- Imported issue references are now normalized into durable ticket-context records instead of being lost in sync metadata
- Issue prompts now include structural repo-map context and ranked related paths
- `docs/PLANNING.md`, `docs/FEATURES.md`, `docs/ARCHITECTURE.md`, and `README.md` now reflect current implementation instead of the original sketch roadmap
- Frontend effect wiring in `App.tsx` was tightened to keep lint clean while guidance and insight state updates stay stable

## [0.1.0] - 2026-04-10

### Added
- Project renamed from Co_Titan_Bug_Tracker to **xMustard**
- Branch: `alpha.test.001.ideation.architecture.o1`
- Research folder with 11 cloned reference repositories:
  - OpenHands, SWE-agent, AutoCodeRover, Aider, cline
  - pr-agent, qodo-cover, trIAge, vulnhuntr, openhands-resolver, auto-code-rover
- New documentation:
  - `docs/ARCHITECTURE.md` - Updated architecture with new features
  - `docs/PLANNING.md` - Implementation roadmap
  - `docs/FEATURES.md` - Detailed feature specifications
- **Phase 2: Planning Checkpoint System** (backend complete, UI pending):
  - `PlanPhase`, `PlanStep`, `RunPlan` models
  - `planning` status for runs
  - Plan generation, approval, rejection logic
  - API endpoints: `/runs/{id}/plan`, `/plan/approve`, `/plan/reject`
  - Approval gate in runtimes with `wait_for_approval` support
  - Frontend types and API functions for plan handling

### Changed
- README.md - Updated with new project name, features, and research references
- package.json - Renamed to `xmustard-ui`
- pyproject.toml - Renamed to `xmustard-backend`
- WorkspaceSidebar.tsx - Updated branding to "xMustard Bug operations"
- App title changed to "xMustard"

### Planned Features (Phase 2+)
- [ ] F1: Planning checkpoint system - UI integration (Phase 2.2)
- [ ] F2: Cost tracking
- [ ] F3: Issue quality scoring
- [ ] F4: Coverage verification
- [ ] F5: Post-run review artifacts
- [ ] F6: Multi-agent orchestration
- [ ] F7: GitHub integration
- [ ] F8: Slack integration

---

## Previous (Co_Titan_Bug_Tracker)

See git history for previous changelog entries.
