# Architecture

## Product Model

xMustard is an issue-operations system, not a general-purpose agent chat. The product centers on durable records:

- issues
- signals
- runs
- plans
- fixes
- verifications
- review artifacts
- repository guidance

## Runtime Model

The agent runtime is replaceable. The tracker owns state, evidence, and review history; the runtime only executes work.

Current runtime pattern:

- build an issue context packet
- attach repo guidance
- optionally generate a plan
- stream run logs
- persist metrics and artifacts
- generate a run insight summary

This separation matters for the long-term platform plan:

- xMustard should be able to swap runtime adapters, search/indexing engines, and eventually backend implementation language without changing the product model

## Backend Shape

`backend/app/` contains the core system:

- `models.py`: Pydantic models for issues, runs, guidance, metrics, critique, and verification artifacts
- `service.py`: orchestration logic for workspace loading, context building, triage analysis, runs, and review data
- `runtimes.py`: runtime launch and metric calculation
- `scanners.py`: canonical issue ingestion and low-noise discovery scanning
- `main.py`: FastAPI endpoints
- `cli.py`: Typer CLI
- `store.py`: file-backed persistence

Workspace data lives under `backend/data/workspaces/<workspace-id>/`.

## Guidance Layer

The guidance layer is one of the most important additions to the architecture.

Inputs:

- top-level instruction files such as `AGENTS.md`
- repo conventions files
- repo index files such as `.devin/wiki.json`
- skill and microagent files from OpenHands, Cline, or similar setups

Processing:

- discover supported guidance files
- summarize them into structured `RepoGuidanceRecord` entries
- attach them to issue context packets
- store `guidance_paths` on runs
- surface them in run insights and the UI

Why it exists:

- it shifts the system from generic repo scanning toward repo-specific, inspectable context

## Context Building

Issue context packets are currently composed from:

- issue data
- evidence
- repo-map summary and ranked related paths
- recent fixes
- recent issue activity
- runbooks
- worktree status
- repository guidance

The repo-map layer now adds early structural context such as:

- top directories
- notable files
- ranked related paths for an issue

The next architectural step is to deepen that with:

- symbol summaries
- enclosing function or class context

## Review And Verification Layer

The tracker stores artifacts after execution, not just before it.

Current artifacts:

- run metrics and workspace cost summaries
- patch critique
- improvement suggestions
- coverage deltas
- test suggestions
- run insights
- issue-context replay snapshots

This is the beginning of a review system shaped more like PR review than plain terminal logging.

## Security And Trust Layer

The next architectural layer should make trust explicit instead of implied.

Planned additions:

- threat-model records linked to issues and runs
- security acceptance criteria attached to ticket context
- confidence scoring for fixes and verification outcomes
- audit trails for approvals, execution, sync, and policy decisions

This layer is important if xMustard is going to compete as an operational system rather than a developer toy.

## Retrieval And Intelligence Layer

The next major context upgrade is not "scan more files." It is "retrieve better evidence."

Planned additions:

- symbol-aware repo maps
- hybrid semantic and keyword retrieval across issues, runs, ticket context, and review artifacts
- context ranking that combines issue evidence, ticket intent, and structural repo data

This is the path toward stronger dynamic context and better operator trust.

## Frontend Shape

The frontend is a queue-first operational UI:

- sidebar for workspace selection and guidance health
- topbar for workspace health, cost, and guidance status
- queue panes for issues, runs, review items, signals, activity, and sources
- detail pane for issue context, review artifacts, verification, and repo guidance
- optional execution drawer for live runtime interaction

## Architectural Gaps

The biggest remaining gaps are:

- symbol-aware repo map and dynamic context
- threat modeling, confidence, and security review artifacts
- semantic retrieval across operational records
- agent governance, auditability, and team-level insights
- replay and evaluation harnesses
- export-oriented review packets
- deeper Jira and Linear ticket context ingestion

These gaps matter more than adding more heuristic scanning.

## Backend Migration Strategy

The current backend is Python-first, but the long-term architecture should support a Rust-based core and may move the HTTP shell to Go once the service boundaries are stable.

Why:

- repo indexing and retrieval will become more compute-heavy
- verification execution and process control benefit from stronger systems primitives
- a compiled backend is a better fit for long-term portability and packaging

Recommended migration path:

1. keep the HTTP and frontend contracts stable
2. isolate scanning, repo-map generation, retrieval, and verification execution behind clear service boundaries
3. reimplement those subsystems in Rust first, either as sidecars or library-backed services
4. migrate orchestration last, either into a thin Go API shell or a Rust HTTP service, only after the data model and operational workflows are proven

This should be an incremental replacement strategy, not a rewrite that pauses product work.

The repo now includes early scaffolding under `rust-core/` and `api-go/` so this migration direction has a concrete starting point.
