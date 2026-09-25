# xMustard Repository Guide

This repository contains xMustard: shared, verified memory and repository intelligence for existing coding agents. Go (`api-go`, HTTP on :8042 and stdio MCP) calls a Rust semantic core (`rust-core`). The React frontend is a secondary consumer; UI work is outside the current focus. The Python backend was retired to the local `archive/2026-06-16-python-backend/`.

## Start Here

- Product scope or a new capability: read `docs/VISION.md` and `docs/ROADMAP.md`.
- Tool interception, compression, or local helper models: read `docs/CONTEXT_LAYER.md` and its dated research; distinguish proposed modules from implemented behavior.
- Status or completion claims: read `docs/STATUS.md`, then verify against source and current checks.
- Module ownership or restructuring: read `docs/ARCHITECTURE.md`.
- Previous decisions or session context: read `docs/history/README.md`; historical closeout claims are scoped to their dated workstream.
- Competitor parity: read `docs/research/COMPETITOR_PARITY_2026-09-24.md` and refresh first-party evidence before treating it as current.

## What This Repo Is For

- ground agents in current repository changes, diagnostics, and evidence
- propose, independently verify, recall, and recheck shared memory
- retrieve narrow code context, explanations, and impact through nine MCP tools
- preserve issue, plan, run, fix, and verification lineage as supporting evidence

## Working Style

- keep the agent-facing product evidence-first, with traceable issue/run context
- preserve the local, no-Docker default and roughly 50–100 MB process-tree target; measure before claiming the target is met
- prefer repo guidance and durable artifacts over chat-only behavior
- avoid adding noisy heuristic scanning when guidance or verification can solve the same problem better
- preserve user changes and avoid destructive git actions unless explicitly requested
- preserve human final merge authority; agent votes, run-plan approval, and successful tests do not authorize a Git merge

## Repository Structure

- `api-go/`: Go HTTP backend (routes, request shaping, persistence) + `xmustard-ops` CLI
- `rust-core/`: Rust core — scanner, repo map, verification, diagnostics, lsp, goal/swarm runtime, data models, semantic search
- `backend/`: runtime `data/` + `sql/` only (Python retired to `archive/`)
- `frontend/src/`: React app, queue views, detail panes, and shared client types
- `docs/`: current vision, architecture, status, roadmap, audits, and indexed history
- `archive/`: local retired code and conversation transcripts; historical context
- `research/`: cloned reference repos used to shape the product roadmap

## Preferred Checks

Backend work:

`make check-backend` runs these checks:

- `cd api-go && go test ./...`
- `cd api-go && go build ./...`
- `cd rust-core && cargo test && cargo clippy`

Frontend work:

Only when a task includes the optional UI (`make check-frontend`):

- `cd frontend && npm run lint`
- `cd frontend && npm run build`

## Guidance For Agents

- keep backend and frontend contracts in sync
- deepen the existing agent tools before expanding the interface
- favor concise, inspectable markdown guidance files
- when updating docs, keep them aligned with the code that actually ships
