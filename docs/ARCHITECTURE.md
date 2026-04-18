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

## Agent Surfaces

xMustard should keep three explicit agent surfaces and avoid collapsing them into one generic "agent platform" abstraction:

- works with agents: plugin manifests, webhooks, chat-app callbacks, export bundles, and event sinks for external systems
- works within agents: repo guidance, issue context packets, browser/context evidence, and replay packets that can travel inside an agent session
- commands agents: governed run launch, plan approval, runtime process control, verification execution, and review artifacts

The no-Python target architecture now treats those surfaces as stable protocol seams:

- Go owns the control-plane and API shell for all three surfaces
- Rust owns retrieval, repo-map, process/runtime, verification, and store-critical helpers behind those surfaces
- Python is not part of the steady-state request path once migration is complete

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
- report workspace guidance health
- generate starter guidance files for common instruction surfaces
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
- browser dumps for browser-based repros and UI state capture
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
- browser dumps captured from MCP/manual browser inspection

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

The current backend is Python-first, but the no-Python target architecture is now explicit: Go is the control-plane shell and Rust is the long-lived core for runtime/retrieval/store-critical logic.

Why:

- repo indexing and retrieval will become more compute-heavy
- verification execution and process control benefit from stronger systems primitives
- a compiled backend is a better fit for long-term portability and packaging

Recommended migration path:

1. keep the HTTP and frontend contracts stable
2. isolate scanning, repo-map generation, retrieval, and verification execution behind clear service boundaries
3. reimplement those subsystems in Rust first, either as sidecars or library-backed services
4. move control-plane and agent/plugin protocol ownership into the Go API shell
5. remove Python from the steady-state request path only after route and artifact parity are proven

This should be an incremental replacement strategy, not a rewrite that pauses product work.

The repo now includes concrete scaffolding under `rust-core/` and `api-go/`, including a Rust-owned architecture contract and Go-served agent-surface inventory, so this migration direction has a real control-plane seam instead of only roadmap prose.
