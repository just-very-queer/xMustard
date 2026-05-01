# xMustard

xMustard is a local repo-intelligence and operational-memory tool for coding agents.

The point is not to become another tracker. The point is to give an agent a trustworthy answer to:

- what changed
- what matters
- what is already known
- how to run and verify the repo
- what plan or prior run owns the current work

Current development focus is the CLI semantic-runtime layer. The web UI exists, but the active product trench is making the backend and CLI good enough to serve tools like MustardCoPilot.

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

Phase 2 work now also includes:

- `semantic-index plan`
- `semantic-index run`
- `semantic-index status`
- durable semantic index baseline storage in Postgres
- stored symbol and semantic row read paths
- freshness states such as `fresh`, `stale`, `dirty_provisional`, `no_baseline`, and `blocked`
- provenance-aware symbol evidence in `path-symbols` and `code-explainer`
- `changed-symbols`, `changed-since-last-run`, and `changed-since-last-accepted-fix`
- `impact`, `repo-context`, and `retrieval-search`
- retrieval-ledger output and stronger derivation metadata across the CLI context surfaces
- live Postgres-backed semantic indexing and readback
- live `ast-grep` semantic search and semantic match persistence

## What This Repo Is Becoming

The near-term target is a CLI-first agent cockpit for a single repo.

Phase 2 is done only when xMustard can answer, through typed CLI and backend surfaces:

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

Backend setup:

```bash
cd backend
python3 -m pip install .
uvicorn app.main:app --reload --port 8042
```

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

## Research Direction

The strongest patterns pulled from local research work are:

- repo-specific instructions beat generic prompting
- semantic retrieval beats blind file dumps
- verification loops matter more than raw generation speed
- post-run review artifacts make agent output inspectable
- replay and eval infrastructure keep the system honest

The biggest current frontier is making stored semantic truth and ops memory work together cleanly enough that another runtime can consume xMustard as a reliable tool.

Phase 2 final closeout truth on 2026-05-02: the implementation surfaces are live and the platform-side work is complete, but the target MustardCoPilot repo still has exactly `189` dirty files. Broad key-files `semantic-index status` remains `stale` with `current_dirty_files: 189` and a fingerprint mismatch; scoped stored-path, impact, and repo-context surfaces remain `dirty_provisional`. No clean `fresh` index is claimable until that target tree is clean. The final acceptance sweep proves the CLI surfaces still run: `changed-symbols` returns `635` symbols, `impact` reports `321` changed files and `635` changed symbols, `repo-context` has `32` retrieval-ledger entries and `0` plan links, `retrieval-search` returns `5` hits, and `semantic-search` returns `5` live ast-grep matches. Missing run/fix anchors and empty plan links in that workspace currently read as absent history, not missing platform capability.
