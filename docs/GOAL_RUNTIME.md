# Goal Runtime

`/goal` adds a durable objective layer above individual issue runs and ad hoc agent prompts.

The feature is intentionally small. It does not try to become a second chat thread manager, a full autonomous runtime, or a swarm scheduler. It records what the operator is trying to achieve, what proof is expected, what file surface is allowed, which runtime/model is preferred, and which evidence has been collected.

## What The Prototype Does

- Creates structured goal records for a workspace.
- Stores goal state in JSON under the existing workspace data directory.
- Appends iteration records and evidence.
- Generates a markdown ledger as a readable projection.
- Exports a bounded context packet for Codex, OpenCode, Claude, or manual work.
- Blocks `complete` unless verification evidence exists or an explicit skip reason is recorded.
- Shows the flow in the execution drawer through a compact goal panel.

The source of truth is structured data. Markdown is generated for humans and report screenshots.

## Storage

Goal data is stored under:

```text
backend/data/workspaces/<workspace_id>/goals.json
backend/data/workspaces/<workspace_id>/goal_iterations/<goal_id>.json
backend/data/workspaces/<workspace_id>/goals/<goal_id>.md
```

`goals.json` and `goal_iterations/*.json` are the durable records. The markdown file is regenerated after create, iteration append, and status update.

## API

The Go API shell owns the route family:

```text
GET    /api/workspaces/{workspace_id}/goals
POST   /api/workspaces/{workspace_id}/goals
GET    /api/workspaces/{workspace_id}/goals/{goal_id}
PATCH  /api/workspaces/{workspace_id}/goals/{goal_id}/status
POST   /api/workspaces/{workspace_id}/goals/{goal_id}/iterations
GET    /api/workspaces/{workspace_id}/goals/{goal_id}/ledger
GET    /api/workspaces/{workspace_id}/goals/{goal_id}/context
```

Python does not receive duplicate route ownership for this slice. That keeps `/goal` aligned with the current migration direction: Go owns delivery and request shaping, while heavier runtime/retrieval/process work can move behind stable contracts later.

## Completion Gate

A goal cannot be marked complete only because a model or worker says it is done.

Completion requires one of:

- verification evidence, such as a test, build, lint, or review command with a successful outcome
- an explicit verification skip reason entered by the operator

Skip reasons are persisted as audit evidence so the ledger can distinguish "verified complete" from "human-skipped verification."

## Context Packet

The context packet is a bounded text export for worker runtimes. It includes:

- title, objective, and status
- acceptance criteria
- current tranche
- allowed surface
- verification commands and profile IDs
- runtime preference and model
- recent iterations and evidence
- resumption notes
- the rule that worker output is proposal/evidence, not automatic acceptance

It intentionally does not dump whole source files or secrets.

## Demo Flow

1. Load a workspace.
2. Open the execution drawer.
3. Create a goal with acceptance criteria, allowed files, and verification commands.
4. Copy the generated context packet into a worker runtime.
5. Append the worker result as an iteration.
6. Add verification evidence.
7. Mark the goal complete.
8. Attempting completion without evidence should fail unless a skip reason is provided.

The best demo proof is the blocked completion path because it shows xMustard treats generated code as a proposal until checked.

## Relationship To Codex And OpenCode

Codex and OpenCode are useful worker runtimes. They can inspect code, propose edits, and run commands. xMustard's role is different: it stores the durable objective, allowed surface, runtime choice, evidence, verification state, and resumption trail.

This separation keeps the runtime replaceable. A goal can be worked manually today, with OpenCode tomorrow, and with Codex later, while the proof trail remains in the workspace.

## Implementations

The goal contract has two wire-compatible owners over the same
`goals.json` / `goal_iterations/<id>.json` / `goals/<id>.md` files:

- **Go shell** (`api-go/internal/workspaceops/goals.go`) — owns the HTTP routes.
- **Rust core** (`rust-core/src/goalruntime.rs`, binary `xmustard-core goal …`) —
  the systems-safe owner: closed `GoalStatus` enum, atomic temp+fsync+rename
  writes, `#![forbid(unsafe_code)]`, and an anti-AI-slop linter (`slop` module)
  that refuses empty/refusal objectives and gates completion on verifiable
  evidence. Wire-parity with the Go shell is enforced by
  `TestGoalRustWireParity`. Benchmarks live in `docs/BENCHMARKS.md`.

## `/swarm`

`/swarm` builds on `/goal` as multiple worker ledgers, not as magic automation.

The shape:

```text
goal
  reader worker: read-only summary and candidate files
  builder worker: patch draft inside allowed surface
  critic worker: diff review and risk notes
  verifier worker: deterministic command evidence
  controller gate: accept, block, narrow, or complete
```

The controller remains xMustard. Worker output is recorded as evidence and
reviewed against acceptance criteria and verification commands.

**Status:** the swarm *scaffold* is implemented in `rust-core/src/swarm.rs`
(`xmustard-core swarm plan|status|gate|record`). It provides the role-tagged
lanes and the deterministic controller gate (`accept | block | narrow |
complete`): it blocks on failure outcomes, narrows when a builder edits outside
the allowed surface, and only reaches `complete` when a verifier lane carries
verification evidence *and* a critic lane has reviewed. It is a typed view +
gate over goal iterations — it records and judges worker turns; it does not
itself spawn agents.

## Non-Goals

- no production scheduling claims
- no autonomous swarm execution claims
- no enterprise security hardening claims
- no benchmark superiority claims
- no plagiarism-check claims
- no replacement of Linear, CodeRabbit, Codex, OpenCode, Claude Code, or other tools

The honest claim is that this is a working local prototype of a durable structured goal record with generated ledger export and evidence-gated completion.
