# Changelog

All notable changes to xMustard will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/).

## [Unreleased]

### Changed
- `docs/PLANNING.md` now tracks the next strategic lanes more explicitly:
  - threat modeling and security review
  - confidence and ticket compliance
  - semantic retrieval and issue intelligence
  - agent operations, insights, and governance
  - incremental backend migration away from Python toward a Rust-based core
- `docs/FEATURES.md`, `docs/ARCHITECTURE.md`, and `README.md` now reflect that xMustard is moving toward stronger trust, retrieval, governance, and backend-platform planning instead of simply expanding UI surface area

### Added
- Issue-level threat model artifacts with backend CRUD, prompt integration, export support, and issue detail editing
- Research notes now include external security/trust references such as OWASP Threat Dragon, OWASP pytm, Semgrep Code, GitHub code scanning, and Vulnhuntr patterns
- Workspace metadata under `backend/data/` now points the self-workspace at `/Users/for_home/Developer/xMustard`

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
