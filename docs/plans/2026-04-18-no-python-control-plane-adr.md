# ADR: No-Python Control Plane Cutover

Status: accepted for the migration lane in this worktree.

Date: 2026-04-18

## Why this ADR exists

xMustard already has serious Go and Rust migration lanes, but the end-state shape was still too implicit in code. That invites migration theater: ports without a crisp ownership model, or "agent platform" drift that weakens the issue-first and evidence-first product.

This ADR locks in a bounded steady-state design:

- Go owns the control-plane and API shell.
- Rust owns process/runtime, retrieval, repo-map, scanner, and store-critical logic.
- Python is not part of the steady-state request path.
- External agents and chat apps integrate through explicit protocol surfaces instead of ad hoc Python handlers.

## Non-goals

- Do not turn xMustard into a generic agent shell.
- Do not replace durable issue, run, fix, verification, and review artifacts with transient chat state.
- Do not assume a flag-day rewrite.

## Decision

The no-Python target architecture is:

1. `api-go/` is the only long-lived HTTP/control-plane shell.
2. `rust-core/` owns process-safe execution, retrieval/repo-map, scanner logic, verification helpers, and store-critical contracts.
3. The product keeps three distinct agent surfaces:
   - `works with agents`: plugin and event surfaces for external agents/chat apps
   - `works within agents`: repo guidance, issue context packets, replay packets, and export bundles that can travel inside agent sessions
   - `commands agents`: governed run launch, plan gating, logs, metrics, critique, and verification
4. Steady-state runtime should remain under `500 MB` where practical, with high-water buffers treated as temporary migration debt instead of the target.

## Surface ownership

### 1. Works with agents

Purpose:
- xMustard exchanges durable issue, run, and review state with external agents and chat apps without giving up ownership of evidence.

Go owns:
- plugin manifest and callback registration
- inbound/outbound webhook and event fanout
- auth and policy checks

Rust owns:
- export-ready review bundles
- stable artifact payloads for evidence-heavy integrations

## 2. Works within agents

Purpose:
- xMustard can live inside an agent session through repo-native guidance and structured issue context, not just through chat prompts.

Go owns:
- guidance discovery
- issue/work/context packet assembly
- replay and eval-facing HTTP surfaces

Rust owns:
- repo-map and retrieval-heavy context building
- store-critical packet helpers once stabilized

## 3. Commands agents

Purpose:
- xMustard acts as an issue-first control plane that launches, gates, and audits agent work.

Go owns:
- run creation
- plan approval/rejection
- operator-facing HTTP orchestration

Rust owns:
- runtime-safe process control
- command execution helpers
- verification execution and parsing
- log/event shaping that should survive agent/runtime swaps

## Next removable Python boundary

The next Python boundary to remove is:

- `external_integrations_gateway`

Current owner:
- `backend/app/main.py`
- `backend/app/service.py`

Target owner:
- Go plugin registry and webhook/event sink surfaces

Why this is next:
- it is the main remaining external agent/chat-app seam still modeled as Python-specific routes
- moving it deletes FastAPI-only integration glue without forcing a storage rewrite first
- it aligns with the product promise that xMustard works with agents through explicit protocols, not bespoke handlers

First slice after this ADR:

1. Serve plugin manifest and agent-surface inventory from Go.
2. Re-home provider test/sync routes behind manifest ids instead of Python-only route logic.
3. Keep Rust focused on durable event/review payloads, not chat-app auth fanout.

## Implementation slice landed with this ADR

This worktree now includes:

- a Rust-owned architecture contract in `rust-core/src/contracts.rs`
- a Rust CLI entrypoint at `xmustard-core describe-architecture`
- Go-served architecture and agent-surface endpoints:
  - `/api/migration/plan`
  - `/api/migration/agent-surfaces`
  - `/api/agent/surfaces`
- migration inventory updates in `api-go/internal/migration/api_route_groups.json`

## Consequences

Good:

- the migration target is executable, not just textual
- Go now has a concrete way to advertise agent/plugin surfaces
- the next Python removal target is named and reviewable in both code and docs

Tradeoff:

- some migration metadata now shells from Go into Rust, which is acceptable at this stage because the goal is ownership clarity before optimization

## Follow-on work

1. Add Go-owned plugin manifest persistence and registration records.
2. Re-home GitHub, Slack, Jira, and Linear sync/notify routes behind manifest ids.
3. Move retrieval/store-critical helpers from Python service code into Rust-backed contracts one artifact family at a time.
4. Delete Python request-path ownership only after route and artifact parity are verified.
