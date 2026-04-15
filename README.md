# xMustard

xMustard is a local, issue-first bug operations system for engineering repositories. It treats bugs, runs, review artifacts, and verification evidence as first-class records instead of burying them inside a chat transcript.

This codebase started as `Co_Titan_Bug_Tracker` and has been evolving using concrete patterns from the research repos under `research/`.

## Product Shape

The current product already supports:

- workspace loading and repo scanning
- issue queues, discovery signals, drift tracking, and saved views
- planning-gated agent runs
- run cost and token metrics
- triage analysis: quality scoring, duplicate detection, triage suggestions
- verification artifacts: coverage deltas, test suggestions, patch critique, improvement suggestions
- issue-level threat models with assets, trust boundaries, abuse cases, and mitigations
- issue-level browser dumps for MCP/manual browser-state capture and shared UI debugging context
- repository guidance discovery from files like `AGENTS.md`, `CONVENTIONS.md`, `.devin/wiki.json`, `.openhands/skills/*.md`, and `.openhands/microagents/repo.md`
- run insights that summarize what guidance shaped a run and what risks remain

## Stack

- `backend/`: FastAPI API, Typer CLI, JSON-backed persistence
- `frontend/`: Vite, React, TypeScript
- `rust-core/`: compiled core for scanner, repo-map, coverage, and verification migration work
- `api-go/`: Go API shell that now owns a large share of the live HTTP surface
- runtimes: `codex`, `opencode`, plus room for more runtime adapters

Longer-term platform direction:

- keep the current product contracts stable while incrementally moving backend-heavy subsystems toward a Rust core and evaluating a Go API shell for the HTTP layer

## Migration Status

The migration is active, not hypothetical anymore.

Rust currently owns or supports:

- signal scanning
- repo-map generation
- coverage parsing
- verification command execution
- verification profile execution

The Go shell now owns most day-to-day app flows, including:

- workspace list/load/scan/snapshot/activity/sources/tree/guidance/repo-map/export/worktree
- issue reads and mutations
- issue context/work/replays
- verification profiles, coverage, ticket context, and threat models
- run creation, workspace query runs, run reads, review flows, plans, metrics, critique, and improvements
- runtime/settings routes and terminal transport

The Python CLI now mirrors the tracker and review surface much more closely too, including:

- verification profiles, ticket context, threat models, context replays, and browser dumps
- plan, metrics, coverage, critique, and improvement flows
- integrations, tree/guidance/repo-map inspection, and terminal transport

The main remaining migration slice is provider integrations:

- integration config and test
- GitHub import and PR creation
- Slack notifications
- Linear sync
- Jira sync

## Workflow

1. Load a repo into a workspace snapshot.
2. Ingest canonical issues and lightweight discovery signals.
3. Build an issue context packet with evidence, prior fixes, activity, and repo guidance.
4. Start a planning run or direct run against a runtime.
5. Review logs, plans, costs, critique, improvements, and session insights.
6. Record fixes and verification outcomes with coverage and test suggestions.

## Why The Research Matters

The strongest patterns repeated across the local research repos are:

- repo-specific instructions beat generic prompting
- dynamic context beats dumping the whole repo
- verification loops matter more than raw generation speed
- post-run review artifacts make agent output inspectable
- benchmark and replay infrastructure keeps the system honest

Those themes now shape both the product and the roadmap.

## Next Strategic Lanes

The strongest gaps still on the roadmap are:

- guidance authoring instead of guidance detection alone
- symbol-aware repo maps and dynamic context
- replayable verification and eval harnesses
- threat modeling, security review, and compliance artifacts
- semantic retrieval across issues, runs, tickets, and review data
- governance, insights, and agent operations for team workflows
- incremental backend migration away from the current Python-heavy core

## Development

Backend:

```bash
cd backend
python3 -m pip install .
uvicorn app.main:app --reload --port 8042
```

Frontend:

```bash
cd frontend
npm install
npm run dev
```

Useful checks:

```bash
cd backend
pytest -q
PYTHONPYCACHEPREFIX=/tmp/pycache python3 -m compileall app

cd ../frontend
npm run lint
npm run build
```

The frontend expects the backend at `http://127.0.0.1:8042`.

## Research Repos

Reference implementations are available locally under `research/`.

| Repo | Main lesson pulled into xMustard |
|------|---------------------------------|
| `OpenHands` | repository instructions, microagents, resolver flows |
| `aider` | repo map mindset, git-aware coding loop, lint/test verification |
| `pr-agent` | dynamic context, critique and review summaries |
| `qodo-cover` | coverage-first verification and record/replay testing |
| `trIAge` | issue quality and duplicate triage |
| `cline` | approvals, skills, and repo instruction surfaces |
| `SWE-agent` | trajectories, reproducible task configs, eval mindset |
| `auto-code-rover` | structure-aware planning and staged execution |

See [docs/RESEARCH_FINDINGS.md](docs/RESEARCH_FINDINGS.md), [docs/RESEARCH_MATRIX.md](docs/RESEARCH_MATRIX.md), [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md), and [docs/PLANNING.md](docs/PLANNING.md) for the current synthesis.

Migration work now also has a dedicated working note in [docs/MIGRATION_RUST_GO.md](docs/MIGRATION_RUST_GO.md).
