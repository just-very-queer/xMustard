---
name: repo
type: repo
---

# xMustard Repo Instructions

xMustard provides shared, verified memory and repository intelligence for existing coding agents. Read `AGENTS.md` for working rules and `docs/README.md` for the current documentation map.

## Product Intent

- ground agents in current code and supported shared memory
- preserve source, freshness, verification, and run/fix lineage
- keep the default local, no-Docker, and within the measured resource target

## Working Defaults

- keep backend and frontend APIs aligned
- focus on MCP, CLI, and backend behavior; UI is outside the current focus
- run targeted checks after edits:
  - backend: `make check-backend`
  - frontend, when explicitly in scope: `make check-frontend`

## Repo Layout

- `api-go/`: Go HTTP backend + `xmustard-ops` CLI (calls the Rust core)
- `rust-core/`: Rust core logic (scanner, repo map, verification, goals, models, semantic)
- `backend/`: runtime `data/` + `sql/` only — Python retired to `archive/2026-06-16-python-backend/`
- `frontend/src/`: app state, panes, API client, styles
- `docs/`: current product docs
- `research/`: reference repos that inform the roadmap
