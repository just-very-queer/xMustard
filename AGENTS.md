# xMustard Repository Guide

This repository contains xMustard: shared, verified memory and repository intelligence for existing coding agents. The Go API (`api-go`, on `127.0.0.1:8042`) serves the HTTP routes and the MCP endpoint at `/mcp`, and calls a Rust semantic core (`rust-core`). Stdio MCP clients connect through the Rust `xmustard-relay`; the older Go stdio shim `xmustard-mcp` still works. The React frontend is a secondary consumer; UI work is outside the current focus. The Python backend was retired to the local `archive/2026-06-16-python-backend/`.

## Start Here

- Product scope or a new capability: read `docs/VISION.md` and the active plan, `docs/plans/2026-09-25-parity-build-plan.md`.
- Tool interception, compression, or local helper models: read `docs/CONTEXT_LAYER.md` and its dated research; distinguish proposed modules from implemented behavior. In v0.1.0 release builds, native-output capture answers `503 redaction_unavailable` until a production redactor is wired into `captureRedactor` (planned for v0.1.1).
- Status or completion claims: read `docs/STATUS.md` and `docs/releases/v0.1.0.md`, then verify against source and current checks.
- Roles, route gates, exposure posture, or approvals: read `docs/SECURITY.md`.
- Module ownership or restructuring: read `docs/ARCHITECTURE.md`.
- Previous decisions or session context: read `docs/history/README.md`; historical closeout claims are scoped to their dated workstream.
- Competitor parity: read `docs/research/COMPETITOR_PARITY_2026-09-24.md` and `docs/research/PARITY_REQUIREMENTS_2026-09-25.md`, and refresh first-party evidence before treating them as current.

## What This Repo Is For

- ground agents in current repository changes, diagnostics, and evidence
- propose, independently verify, recall, and recheck shared memory
- retrieve narrow code context, explanations, and impact through nine MCP tools: `ground`, `recall`, `remember`, `verify`, `search`, `explain`, `impact`, `diagnostics`, `why_failed`
- preserve issue, plan, run, fix, and verification lineage as supporting evidence

## Working Style

- keep the agent-facing product evidence-first, with traceable issue/run context
- preserve the local, no-Docker default and roughly 50–100 MB process-tree target; measure before claiming the target is met
- prefer repo guidance and durable artifacts over chat-only behavior
- avoid adding noisy heuristic scanning when guidance or verification can solve the same problem better
- preserve user changes and avoid destructive git actions unless explicitly requested
- preserve human final merge authority; agent votes, run-plan approval, and successful tests do not authorize a Git merge

## Repository Structure

- `api-go/`: Go module. `cmd/` holds `xmustard-api` (HTTP API and `/mcp`), `xmustard-ops` (operator CLI), `xmustard-mcp` (older stdio shim) and `xmustard-eval` (outcome executor). `internal/` holds `mcpserver`, `govstore` (SQLite governance store), `workspaceops`, `evidence`, `injection`, `redact`, `rustcore` (core bridge and worker supervisor), `budget`, `groundbudget` and `toolcompat`
- `rust-core/`: Rust core — scanner, repo map, verification, diagnostics, lsp, goal runtime, the code index (`src/index/`: `index.db`, language packs, resolver, BM25 search), the resident worker (`serve.rs`), and the binaries `xmustard-core` and `xmustard-relay`
- `integrations/pi/`: Pi extension, pinned to its runtime version
- `scripts/bench/`: retrieval gate and budget gate v2; `scripts/e2e/`: end-to-end harnesses
- `eval/tasks/`: the task corpus schema, seed tasks and fixtures for `xmustard-eval`
- `packaging/homebrew/`: Homebrew formula
- `.github/workflows/check.yml`: CI for backend checks, the Pi check and both gates
- `backend/`: runtime `data/` + `sql/` only (Python retired to `archive/`)
- `frontend/src/`: React app, queue views, detail panes, and shared client types
- `docs/`: vision, status, architecture, security, release notes, plans, benchmarks, audits, and indexed history (map: `docs/README.md`)
- `archive/`: local retired code and conversation transcripts; historical context (not tracked)
- `research/`: cloned reference repos used to shape the product roadmap (not tracked)

## Preferred Checks

Every check below runs on your own machine, macOS or Linux, with no Docker and no remote host. CI (`.github/workflows/check.yml`) runs the backend checks, both gates and the Pi check on Linux for every pull request.

Prerequisites: Go 1.26, stable Rust with clippy, and `python3`. `make check-backend` also needs pytest, because one `xmustard-ops` test runs `python3 -m pytest`. The Pi check needs Node 22.19 or newer, the floor of the pinned Pi runtime.

Backend work:

`make check-backend` runs these checks:

- `cd api-go && go test ./...`
- `cd api-go && go build ./...`
- `cd rust-core && cargo test && cargo clippy`

After any Rust change, rebuild the release core (`make build`) or point `XMUSTARD_CORE_BIN` at a fresh `xmustard-core`. The Go side runs the first core it finds (`XMUSTARD_CORE_BIN`, then `PATH`, then `rust-core/target/release/xmustard-core`, then `cargo run`), so a stale binary gives stale results. CI runs `make check-backend` with `XMUSTARD_CORE_BIN` set to the release build of the same revision.

Linux is the reference platform for memory tests. `resident_rss_on_a_100k_symbol_resolved_graph_stays_within_the_line` in `rust-core/tests/index_query.rs` exceeds its line on macOS and is load-sensitive on Linux too; rerun it alone before treating a failure as a regression.

Gates and adapters:

- `make bench-test`: gate v2 unit tests, no binaries needed
- `make bench-retrieval`: retrieval gate, when search or indexing changes
- `make bench-gate`: budget gate v2 (CI suite), when a change can move memory or add processes
- `cd integrations/pi && npm ci && npm run check`: when the Pi extension or the tool schemas change

Frontend work:

Only when a task includes the optional UI (`make check-frontend`):

- `cd frontend && npm run lint`
- `cd frontend && npm run build`

## Guidance For Agents

- keep backend and frontend contracts in sync
- deepen the existing agent tools before expanding the interface
- favor concise, inspectable markdown guidance files
- when updating docs, keep them aligned with the code that actually ships
