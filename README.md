# xMustard

xMustard is a local repo-intelligence and operational-memory tool for coding agents.

The point is not to become another tracker. The point is to give an agent a trustworthy answer to:

- what changed
- what matters
- what is already known
- how to run and verify the repo
- what plan or prior run owns the current work

The project is still in active development. The current engineering focus is the CLI and backend surface, with the web UI kept as a secondary consumer rather than the main product trench.

## Public vs Private Docs

This `README.md` is the public GitHub-facing overview.

Deeper migration notes, tranche prompts, private closeout logs, and working architecture handoff material live in local docs that are not part of the public repository surface. The public README should explain what xMustard is, where it is headed, and how to run it without reading like an internal rollout diary.

## What xMustard Is

xMustard combines two things that usually live in separate products:

- semantic repo intelligence: repo state, file and symbol context, semantic indexing, structural search, and runtime discovery
- ops memory: issues, plans, runs, verification profiles, threat models, ticket context, activity history, and review artifacts

That combination is the moat. Repo-only intelligence without durable memory is too shallow. Tracker-only memory without repo truth is too noisy.

## What Exists Today

The current codebase already has working surfaces for:

- workspace loading and repo scanning
- worktree state: branch, head, dirty paths, staged and untracked state
- issue records, signals, critiques, improvements, and review artifacts
- plan tracking with ownership, revision history, and attached files
- verification profiles and verification history
- ticket context, threat models, vulnerability records, and browser dumps
- terminal transport and runtime launching
- issue-context packets and replay artifacts
- repo guidance discovery from files like `AGENTS.md` and repo-native config
- CLI surfaces for repo state, changes, run targets, verify targets, changed symbols, impact, repo context, code explainer, path symbols, and semantic index planning

The semantic/runtime layer also includes:

- semantic index planning, execution, and status
- durable semantic baseline storage in Postgres
- stored symbol and semantic row read paths
- freshness and provenance metadata for semantic context
- changed-symbol and impact reporting
- structural retrieval and semantic search
- live `ast-grep`-backed semantic matching

Under the hood, the project is in the middle of an ownership shift:

- Go is taking over delivery and request shaping
- Rust is taking over semantic meaning and systems-heavy execution boundaries
- Postgres remains the durable semantic state layer
- Python is being reduced toward compatibility-only status

## What This Repo Is Becoming

The near-term target is a CLI-first agent cockpit for a single repo.

The core product test is simple: xMustard should be able to answer, through typed CLI and backend surfaces:

- what changed since the last useful baseline
- which symbols and files matter
- whether semantic state is trustworthy
- what commands run the project
- what verification targets matter
- what plans, fixes, and runs are connected to the current change

We are not treating “feature count” as progress. If the system cannot ground an agent in repo reality, the work is not finished.

## Repo Layout

- `backend/`: FastAPI app, Typer CLI, models, scanners, runtimes, persistence, and semantic-index work
- `backend/tests/`: backend regression coverage
- `frontend/`: React and TypeScript UI surface
- `api-go/`: Go HTTP shell and migration surface
- `rust-core/`: Rust acceleration and parity work for repo intelligence and verification
- `research/`: local reference repos used for product and architecture study; ignored from git
- `docs/`: local planning and handoff notes; ignored from git

## Development

Go API shell for migrated request surfaces:

```bash
cd api-go
XMUSTARD_API_PORT=8042 go run ./cmd/xmustard-api
```

Python compatibility shell:

```bash
cd backend
python3 -m pip install .
uvicorn app.main:app --reload --port 8042
```

The Python shell is compatibility-only for the routes and CLI workflows that have been delegated, but it is not gone. Several FastAPI routes and many Typer commands still call `TrackerService`; treat the repo as mixed-mode until those paths are moved or deleted with replacement proof. In particular, `path-symbols`, `explain-path`, and `changed-symbols` no longer need Python as their shipped delivery owner.

Frontend setup:

```bash
cd frontend
npm install
npm run dev
```

Core checks:

```bash
cd backend
pytest -q
PYTHONPYCACHEPREFIX=/tmp/pycache python3 -m compileall app

cd ../frontend
npm run lint
npm run build
```

The frontend expects the backend at `http://127.0.0.1:8042`.

## Current Status

xMustard is still in development.

The current public direction is:

- strengthen the CLI/runtime surface first
- keep repo intelligence and ops memory tightly connected
- move shipped request paths away from Python over time
- keep the migration honest: public behavior first, private rollout notes second

If you are reading this on GitHub, treat the README as the public product and architecture overview. The private migration ledger, tranche prompts, and closeout notes are intentionally kept out of the public repo surface.

## Architecture Direction

The intended steady-state shape is:

- Go for request delivery and operator-facing control surfaces
- Rust for semantic meaning, diagnostics normalization, and systems-heavy runtime/process boundaries
- Postgres for durable semantic and operational state
- Python reduced to temporary compatibility shims until it can be deleted
