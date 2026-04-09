# xMustard

A standalone bug operations system for local engineering codebases. It is issue-first and run-first: no chat surface, no dependency on legacy Co_Titan UI.

**Project xMustard** is the evolution of Co_Titan_Bug_Tracker, enhanced with insights from 11 leading AI coding agent projects.

## Stack

- `backend/`: Python FastAPI API + Typer CLI + file-backed JSON store
- `frontend/`: Vite + React + TypeScript UI
- Runtime bridges for `codex`, `opencode`, and extensible agent runtimes

## Core Workflow

1. Load any repository tree into the tracker
2. Scan bug ledgers, verdict JSON, and codebase heuristics
3. Triage canonical issues and auto-discovery signals
4. Generate issue context packets for agent runtimes
5. Start terminal-backed agent runs with planning checkpoints
6. Three-pass verification with coverage tracking
7. Export full workspace snapshots as JSON

## Key Features (Inspired by Research)

- **Planning checkpoints**: Agent plans are shown for approval before execution (inspired by SWE-agent, AutoCodeRover)
- **Cost tracking**: Per-run token usage and cost metrics (inspired by OpenHands, SWE-agent)
- **Multi-phase verification**: Search → Plan → Fix → Review → Verify pipeline (inspired by AutoCodeRover)
- **Triage automation**: Issue quality scoring, duplicate detection (inspired by trIAge, PR-Agent)
- **Post-run artifacts**: Patch critique, suggested improvements (inspired by PR-Agent)
- **Coverage-driven verification**: Test generation and coverage delta tracking (inspired by Qodo Cover)
- **Extensible runtimes**: Plug in any agent runtime via configuration (inspired by OpenHands)

## Backend

```bash
cd backend
python3 -m pip install .
uvicorn app.main:app --reload --port 8042
```

CLI examples:

```bash
cd backend
python3 -m app.cli health
python3 -m app.cli capabilities
python3 -m app.cli runtimes
python3 -m app.cli models codex
python3 -m app.cli models opencode
python3 -m app.cli settings-get
python3 -m app.cli load-workspace /path/to/repo
python3 -m app.cli issue-context <workspace-id> P0_25M03_001
python3 -m app.cli agent-probe <workspace-id> --runtime codex --model gpt-5.4
python3 -m app.cli agent-query <workspace-id> --runtime codex --model gpt-5.4-mini --prompt "Summarize the top 5 open P0/P1 bugs."
python3 -m app.cli run-start <workspace-id> P0_25M03_001 --runtime codex --model gpt-5.4-mini --instruction "Validate first, then fix if reproducible."
```

Root shortcuts:

```bash
make backend
make frontend
make build-ui
```

## Frontend

```bash
cd frontend
npm install
npm run dev
```

The Vite app expects the backend at `http://127.0.0.1:8042`.

## Research

Reference implementations are cloned under `/research/`:

| Repo | Key Inspiration |
|------|----------------|
| OpenHands | Multi-agent orchestration, WebSocket events, cost tracking |
| SWE-agent | YAML config, template system, retry loops, trajectory persistence |
| AutoCodeRover | Two-stage workflow, AST search, multi-patch voting |
| pr-agent | Command dispatch, PR compression, post-run review |
| qodo-cover | Coverage-driven verification, record/replay caching |
| trIAge | Issue triage, duplicate detection, quality scoring |
| cline | Approval gates, token tracking, MCP support |
| aider | Terminal-first UX, auto git commits |
| vulnhuntr | Vulnerability discovery, exploit path tracing |
| openhands-resolver | Auto-issue resolution patterns |
| auto-code-rover | Project-structure-aware planning |

## Architecture

See [ARCHITECTURE.md](docs/ARCHITECTURE.md) for detailed architecture.

## Planning

See [PLANNING.md](docs/PLANNING.md) for implementation roadmap.
