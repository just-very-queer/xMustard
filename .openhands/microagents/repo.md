---
name: repo
type: repo
---

# xMustard Repo Instructions

xMustard is a local bug-operations product, not a general-purpose chat shell. The backend lives in `backend/` and the React frontend lives in `frontend/`.

## Product Intent

- keep bugs, runs, fixes, and verification evidence as durable records
- use repository guidance to shape runs
- prefer review and verification artifacts over opaque agent behavior

## Working Defaults

- keep backend and frontend APIs aligned
- prefer issue-first workflows over adding new generic chat surfaces
- run targeted checks after edits:
  - backend: `pytest -q`
  - backend: `PYTHONPYCACHEPREFIX=/tmp/pycache python3 -m compileall app`
  - frontend: `npm run lint`
  - frontend: `npm run build`

## Repo Layout

- `backend/app/`: service, runtimes, scanners, models, API
- `backend/tests/`: regression tests
- `frontend/src/`: app state, panes, API client, styles
- `docs/`: current product docs
- `research/`: reference repos that inform the roadmap
