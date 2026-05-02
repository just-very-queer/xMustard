# xMustard

xMustard is a local repo-intelligence and operational-memory tool for coding agents.

The point is not to become another tracker. The point is to give an agent a trustworthy answer to:

- what changed
- what matters
- what is already known
- how to run and verify the repo
- what plan or prior run owns the current work

Current development focus is the CLI semantic-runtime layer. The web UI exists, but the active product trench is making the backend and CLI good enough to serve tools like MustardCoPilot.

Phase 3 is now the Python-exit and ownership-shift phase:

- Go should own API delivery and request shaping
- Rust should own semantic meaning, symbol extraction, impact ranking, and diagnostics normalization
- Postgres should remain the durable semantic state layer
- Python should keep shrinking toward compatibility-only status until it can be deleted slice by slice

The first Phase 3 authority cuts are already landed:

- Go owns API delivery for `impact`, `repo-context`, and `retrieval-search`
- Rust owns semantic impact calculation, changed-symbol extraction, provenance, and affected file/test ranking
- Rust owns on-demand `path-symbols` extraction, the code-explainer semantic substrate, and storage-ready symbol materialization rows consumed by Go delivery and Python compatibility fallback
- Go now also owns the Postgres foundation delivery slice for settings-backed schema plan/render/bootstrap
- Go now owns the live semantic-search plus Postgres semantic materialization HTTP slice for `path-symbols/materialize`, `semantic-index/materialize`, and `semantic-search/materialize`

Completion audit truth on 2026-05-02: xMustard is still mixed-mode, not Python-exited. Go and Rust own real shipped slices, but Python still remains in the authority path through the Typer `semantic-index` and operator CLI surface, `TrackerService` compatibility assembly, and semantic baseline/materialization helpers.

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

Phase 3 work now includes:

- Go-owned repo-intelligence read delivery for `impact`, `repo-context`, and `retrieval-search`
- Rust-owned semantic-impact generation consumed by Go delivery
- Rust-owned on-demand path-symbol extraction consumed by Go `path-symbols` delivery
- Rust-owned code-explainer substrate for `explain-path`, including path role, line/import counts, detected symbols, summary, hints, and provenance
- Rust-owned file-symbol summary and symbol-row shaping for on-demand `path-symbols`, so Python compatibility no longer has to shape those Postgres-ready rows when stored semantic state is absent
- Go-owned semantic-search and semantic materialization delivery for the live HTTP paths that write path-symbol, workspace semantic-index batch, and semantic-search rows into Postgres
- a durable Python exit map to track what still belongs in Python temporarily and what should move next

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

The Python shell is now compatibility-only for the remaining Python routes and CLI workflows. The Go-owned Postgres foundation and semantic materialization/search HTTP surfaces are served from `api-go`, not `backend/app/main.py`.

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

Phase 3 starts from a simpler rule than the earlier closeout loops: external validation workspaces can help prove tool consumption, but they do not define whether xMustard itself is complete. The next real bar is reducing Python authority in shipped request paths and semantic-core logic, not reopening Phase 2 status arguments.

Phase 2 final closeout truth on 2026-05-02: the implementation surfaces are live and Phase 2 is complete for xMustard itself. The CLI semantic-runtime layer is in place: `semantic-index`, `path-symbols`, `code-explainer`, `changed-symbols`, `changed-since-last-run`, `changed-since-last-accepted-fix`, `impact`, `repo-context`, `retrieval-search`, and `semantic-search` all exist, and the Postgres plus `ast-grep` semantic path is proven live.

MustardCoPilot was added to xMustard as a workspace for live validation, not merged into this repo. That external workspace was useful for proving tool consumption, but its dirty worktree is not a gating condition for Phase 2 completion. The real closeout hiccup was procedural: we let external validation status act like a product-completion requirement. That dependency is now dropped. Optional downstream validation can stay dirty, stale, or provisional without changing the fact that xMustard Phase 2 is done.
