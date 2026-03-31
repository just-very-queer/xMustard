# Architecture

## Product model

The tracker treats bugs as structured operational records:

- `Issues`: canonical bugs from ledgers, verdicts, or promoted discovery signals
- `Signals`: unpromoted discovery findings from code scans
- `Runs`: terminal-backed Codex or OpenCode jobs attached to an issue
- `Context packets`: deterministic issue bundles used to seed agent runs

## Backend design

- Workspace state is stored as JSON under `backend/data/workspaces/<workspace-id>/`.
- Scanners ingest:
  - `docs/bugs/Bugs_*.md`
  - `Bugs_*_verdicts.json`
  - code annotations and heuristic grep signals
- Drift flags are computed from partial fixes, missing evidence, and missing verification tests.
- Runtime execution uses:
  - `codex exec --json`
  - `opencode run --format json`

## UI design

- Dense left-nav and queue-first navigation inspired by Linear Next.
- Detail panels for issue metadata, evidence, and runtime operations.
- Logs and agent outputs are treated as artifacts, not conversation turns.
