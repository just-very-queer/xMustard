# Co_Titan_Bug_Tracker

A standalone bug operations system for local engineering codebases. It is issue-first and run-first: no chat surface, no dependency on the legacy Co_Titan UI.

## Stack

- `backend/`: Python FastAPI API + Typer CLI + file-backed JSON store
- `frontend/`: Vite + React + TypeScript UI
- Runtime bridges for `codex` and `opencode`

## Core workflow

1. Load any repository tree into the tracker.
2. Scan bug ledgers, verdict JSON, and codebase heuristics.
3. Triage canonical issues and auto-discovery signals.
4. Generate issue context packets for Codex/OpenCode.
5. Start terminal-backed runs against an issue and watch logs.
6. Export the full workspace snapshot as JSON.

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
python3 -m app.cli load-workspace /Users/for_home/Developer/CoTitanMigration/Co_Titan
python3 -m app.cli issue-context co-titan-0a54108278 P0_25M03_001
python3 -m app.cli agent-probe co-titan-0a54108278 --runtime codex --model gpt-5.4
python3 -m app.cli agent-query co-titan-0a54108278 --runtime codex --model gpt-5.4-mini --prompt "Summarize the top 5 open P0/P1 bugs."
python3 -m app.cli run-start co-titan-0a54108278 P0_25M03_001 --runtime codex --model gpt-5.4-mini --instruction "Validate first, then fix if reproducible."
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
