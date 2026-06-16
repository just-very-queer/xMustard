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

Deeper migration notes, tranche prompts, closeout logs, and working architecture handoff material live in internal-facing repo docs. The public README should explain what xMustard is, where it is headed, and how to run it without reading like an internal rollout diary.

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

- `api-go/`: Go HTTP shell — the backend (120 routes), request shaping, persistence, calling the Rust core
- `rust-core/`: Rust core — scanner, repo map, verification, diagnostics, lsp, goal/swarm runtime, data models, semantic search
- `backend/`: runtime data (`data/`) and SQL schema (`sql/`) only; the Python FastAPI/Typer stack was retired to `archive/2026-06-16-python-backend/`
- `frontend/`: React and TypeScript UI surface (proxies `/api` → `:8042`)
- `archive/`: retired implementations, including the legacy Python backend
- `research/`: local reference repos used for product and architecture study; ignored from git
- `docs/`: planning, architecture, handoff notes, prompts, and closeout logs

## Development

The backend is Go (`api-go`) calling the Rust core (`rust-core`):

```bash
cd api-go
XMUSTARD_API_PORT=8042 go run ./cmd/xmustard-api   # or: make backend
```

The Python FastAPI/Typer backend has been **retired**. Its full surface is
shadowed by `api-go` (120 routes ≥ the Python's 115, verified by a passing Go
test suite) plus the Rust core, and `cli.py` already delegated to `xmustard-ops`.
The source is preserved under `archive/2026-06-16-python-backend/` for reference.
See `docs/PYTHON_TO_RUST_MIGRATION.md`.

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

If you are reading this on GitHub, treat the README as the public product and architecture overview. The tracked docs include internal-facing migration notes, tranche prompts, and closeout material for code-truth handoffs.

## Architecture Direction

The intended steady-state shape is:

- Go for request delivery and operator-facing control surfaces
- Rust for semantic meaning, diagnostics normalization, and systems-heavy runtime/process boundaries
- Postgres for durable semantic and operational state
- Python reduced to temporary compatibility shims until it can be deleted
