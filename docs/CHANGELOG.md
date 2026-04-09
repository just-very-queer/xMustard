# Changelog

All notable changes to xMustard will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/).

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
