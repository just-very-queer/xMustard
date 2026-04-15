# Planning

This roadmap reflects the current state of xMustard, not the original alpha sketch.

## Current Status

Estimated implementation progress:

- backend capability: `75-85%`
- frontend surfacing: `50-60%`
- overall roadmap: `60-70%`

The main gap is no longer raw backend functionality. The biggest remaining work is productizing the guidance, verification, and evaluation layers more completely.

## Strategic Direction

xMustard should compete as an issue-operations system with strong evidence, governance, and workflow fit, not as a generic agent shell.

That means the roadmap now optimizes for:

- better context quality instead of broader heuristic scanning
- stronger verification and replay instead of one-off runs
- clearer trust, security, and compliance artifacts
- better workflow embedding for ticket systems and team operations
- an incremental backend migration path away from the current Python-heavy core

## Implemented Foundations

### Done or largely done

- project rename and xMustard product framing
- workspace loading, snapshots, JSON-backed persistence
- issue queue, signals, drift, activity, saved views
- runtime abstraction for local coding agents
- plan generation, approval, rejection, and planning-gated execution
- run metrics and workspace cost aggregation
- issue quality scoring, duplicate detection, and triage suggestions
- coverage delta, test suggestions, patch critique, and improvement suggestions
- repo guidance discovery and run insights

### Recently added

- detected guidance files now surface in the UI
- missing guidance now gets onboarding prompts
- OpenHands-style `.openhands/microagents/repo.md` files are recognized as repo guidance
- saved workspace verification profiles now surface in the execution drawer and issue detail
- issue-level ticket context now attaches upstream links, summaries, and acceptance criteria to issue packets
- issue-context replay snapshots now capture prompt state for later eval and comparison
- issue-level browser dumps now capture MCP/manual browser state for shared UI debugging context
- workspace repo maps now persist structural summaries and ranked related paths for issue packets

## Research-Driven Next Phases

## Phase A: Guidance Authoring

Inspired by `OpenHands`, `cline`, and `OpenHands Resolver`.

Goal:

- turn repo guidance from passive discovery into an active setup workflow

Build:

- generate starter `AGENTS.md`
- generate starter `.openhands/microagents/repo.md`
- optionally generate `CONVENTIONS.md`
- show missing or stale guidance health in the workspace

Success looks like:

- a new workspace can become guidance-ready in one or two clicks

## Phase B: Repo Map And Dynamic Context

Inspired by `aider` and `pr-agent`.

Goal:

- reduce prompt noise by attaching structural context instead of raw file volume

Build:

- workspace repo map summary
- top symbols and directories for a bug or run
- file and symbol relevance ranking for issue context packets
- dynamic context expansion around touched functions or classes

Current state:

- workspace repo-map summaries now persist top directories, extension mix, and notable files
- issue packets now carry ranked related paths and structural context in the prompt
- the issue detail pane now shows repo-map summaries and related paths

Success looks like:

- lower token use and stronger run quality for the same issues

## Phase C: Ticket Context And Acceptance Criteria

Inspired by `pr-agent` and `OpenHands Resolver`.

Goal:

- make issue context richer than the local tracker alone

Build:

- import or attach ticket acceptance criteria
- capture linked PR, issue, or incident references
- show external context in issue packets and run briefs
- keep imported ticket context inspectable and durable

Current state:

- durable ticket-context records now exist per issue
- GitHub imports seed ticket context automatically
- issue packets and prompts now carry acceptance criteria and links
- the issue detail pane now supports manual curation and editing

Success looks like:

- xMustard can explain not just the bug, but the product expectation the fix is supposed to satisfy

## Phase D: Verification Replay And Checklists

Inspired by `qodo-cover` and `aider`.

Goal:

- make verification reproducible and auditable

Build:

- verification checklist on runs and fixes
- optional replay mode for verification runs
- stored pass or fail outcomes per verification profile
- profile-level reporting for validation confidence

Current state:

- issue-context replay capture now exists as the first stored replay artifact
- prompt snapshots can be saved from the issue detail pane for later comparison
- browser dumps can now be saved as durable issue artifacts and included in issue context packets

Success looks like:

- xMustard can reliably answer whether a fix was merely generated or actually validated

## Phase E: Eval And Replay Harness

Inspired by `SWE-agent`, `aider`, and `qodo-cover`.

Goal:

- measure whether xMustard is getting better

Build:

- saved evaluation scenarios
- replay of issue context generation and run insights
- regression suite for guidance discovery, planning quality, and verification outcomes
- workspace-level reporting for cost, success rate, and verification quality

Success looks like:

- product changes can be judged by outcome, not intuition

## Phase F: Threat Modeling And Security Review

Inspired by secure SDLC workflows and enterprise engineering review.

Goal:

- make security reasoning a first-class artifact instead of an afterthought

Build:

- issue-level threat models with assets, trust boundaries, and abuse paths
- security acceptance criteria attached to ticket context
- security review artifacts on runs
- dependency, secret, and auth-risk checkpoints in run review

Current state:

- issue-level threat models can now be saved as durable workspace artifacts
- threat models are attached to issue context packets and operator prompts
- the issue detail pane now supports threat-model editing for assets, entry points, abuse cases, and mitigations

Success looks like:

- xMustard can explain not only whether a change works, but whether it changes the system risk profile safely

## Phase G: Confidence And Ticket Compliance

Inspired by `Devin`, `pr-agent`, and `Qodo Merge`.

Goal:

- make the system explicit about how sure it is and whether a change actually satisfies the upstream request

Build:

- run-level confidence scoring
- verification confidence per saved profile
- acceptance-criteria compliance review
- unrelated-change detection and scope warnings

Success looks like:

- runs produce a durable answer to "is this fixed?" and "does this satisfy the ticket?" instead of leaving that implicit

## Phase H: Semantic Retrieval And Issue Intelligence

Inspired by `Linear`, `aider`, and `pr-agent`.

Goal:

- retrieve the right operational context across issues, runs, comments, tickets, and repo structure

Build:

- hybrid semantic and keyword search
- cross-artifact retrieval across issues, runs, ticket context, and review data
- better duplicate clustering and owner suggestions
- context expansion informed by symbol and ticket relationships

Success looks like:

- operators can find the most relevant prior run, ticket, or file without manually hunting through the workspace

## Phase I: Agent Operations, Insights, And Governance

Inspired by `Linear` and `Devin`.

Goal:

- make agents operate like visible teammates inside a governed system

Build:

- agent identity and ownership history on runs
- success, cost, and verification dashboards by runtime and workflow
- audit trails for approvals, sync, and execution
- policy gates for runtime choice, verification requirements, and sensitive workspaces

Success looks like:

- teams can trust and manage xMustard operationally, not just use it as a clever local tool

## Phase J: Review And Export Flows

Inspired by `pr-agent` and `OpenHands Resolver`.

Goal:

- make run output easier to consume outside the app

Build:

- PR-style review packet export
- human-readable fix brief
- run acceptance checklist
- optional provider integrations for GitHub and Linear once internal flows are solid

Success looks like:

- a successful run can become a clean handoff artifact

## Phase K: Backend Migration Away From Python

Goal:

- move the backend core toward a Rust-based implementation without freezing product work or breaking the current UI

Why this is on the roadmap:

- the current Python backend is productive, but long-term performance, packaging, concurrency, and binary distribution would improve with a compiled core
- repo indexing, semantic retrieval, verification orchestration, and policy-heavy execution are good candidates for Rust services

Migration approach:

- keep the frontend and API contracts stable first
- isolate domain contracts and persistence formats before rewriting internals
- migrate the most compute-heavy or reliability-sensitive subsystems first
- avoid a flag-day rewrite of the entire product

Planned stages:

1. define stable service boundaries for scanning, repo-map generation, search, verification execution, and runtime orchestration
2. move scanners, repo-map building, and search indexing into a Rust sidecar or library-backed service
3. move verification execution and artifact generation into Rust for stronger process control
4. evaluate replacing the FastAPI orchestration layer with a Go API shell or Rust HTTP service once the core subsystems are proven

Success looks like:

- xMustard gains a faster, more portable backend core without losing its existing evidence model or UI velocity

## Priority Order

1. Guidance authoring
2. Repo map and dynamic context
3. Ticket context and acceptance criteria
4. Verification replay and checklists
5. Eval and replay harness
6. Threat modeling and security review
7. Confidence and ticket compliance
8. Semantic retrieval and issue intelligence
9. Agent operations, insights, and governance
10. Review and export flows
11. Backend migration away from Python

## Docs To Keep In Sync

- `README.md`
- `docs/ARCHITECTURE.md`
- `docs/FEATURES.md`
- `docs/CHANGELOG.md`
- `docs/RESEARCH_FINDINGS.md`
