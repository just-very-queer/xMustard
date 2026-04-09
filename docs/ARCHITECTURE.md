# Architecture

## Product Model

xMustard treats bugs as structured operational records with a clear lifecycle:

```
Issue Created → Triaged → Investigating → In Progress → Verification → Resolved
                                ↓
                          Partial Fix (drift detected)
```

### Core Entities

- **Issues**: Canonical bugs from ledgers, verdicts, promoted signals, or manual entry
- **Signals**: Unpromoted discovery findings from code scans (TODO/FIXME/BUG annotations, swallowed exceptions)
- **Runs**: Terminal-backed agent jobs attached to an issue
- **Context Packets**: Deterministic issue bundles used to seed agent runs
- **Fix Records**: Proposed/accepted fixes from agent runs
- **Verifications**: Three-pass verification results with coverage data

## Backend Design

### Directory Structure

```
backend/
├── app/
│   ├── main.py          # FastAPI app (535 lines) - all API endpoints
│   ├── models.py        # Pydantic models (568 lines) - all data structures
│   ├── service.py       # TrackerService (1200+ lines) - core business logic
│   ├── store.py         # FileStore (248 lines) - JSON file persistence
│   ├── cli.py           # Typer CLI (695 lines) - command-line interface
│   ├── scanners.py      # Bug ledger/verdict parsing (392 lines)
│   ├── runtimes.py      # RuntimeService (498 lines) - agent execution
│   └── terminal.py      # TerminalService - PTY management
├── data/
│   ├── workspaces.json     # Workspace registry
│   ├── settings.json       # App settings
│   └── workspaces/
│       └── <workspace-id>/
│           ├── snapshot.json
│           ├── tracker_issues.json
│           ├── fix_records.json
│           ├── verifications.json
│           ├── runs/
│           │   ├── run_<id>.json      # Run metadata
│           │   ├── run_<id>.log       # Run output
│           │   └── run_<id>.out.json  # Structured output
│           ├── terminals/
│           └── activity.jsonl          # Audit log
```

### Workspace State

- Workspace state stored as JSON under `backend/data/workspaces/<workspace-id>/`
- Scanners ingest:
  - `docs/bugs/Bugs_*.md`
  - `Bugs_*_verdicts.json`
  - Code annotations and heuristic grep signals
- Drift flags computed from partial fixes, missing evidence, missing verification tests

### Agent Runtime Abstraction

Inspired by OpenHands and SWE-agent, runtimes are abstracted:

```python
RuntimeKind = Literal["codex", "opencode", "openhands", "custom"]

class RuntimeService:
    def detect_runtimes() -> list[RuntimeCapabilities]
    def probe_runtime(workspace_id, runtime, model) -> RuntimeProbeResult
    def start_run(issue_id, runtime, model, instruction) -> RunRecord
    def get_run_log(run_id) -> str
```

Current implementations:
- `codex exec --json`
- `opencode run --format json`

### New: Planning Checkpoint System

Inspired by SWE-agent's trajectory hooks and AutoCodeRover's phase system:

```
User triggers run
    ↓
[PLANNING] Agent generates plan
    ↓
User approves/modifies plan
    ↓
[EXECUTION] Agent executes plan
    ↓
[REVIEW] User reviews changes
    ↓
[VERIFICATION] Tests run, coverage measured
    ↓
[ACCEPT/DISCARD] Fix record created
```

### New: Cost Tracking

Inspired by OpenHands Metrics and SWE-agent InstanceStats:

```python
class RunMetrics(BaseModel):
    tokens_used: int = 0
    prompt_tokens: int = 0
    completion_tokens: int = 0
    cost_usd: float = 0.0
    duration_ms: int = 0
    model: str
```

### New: Triage Automation

Inspired by trIAge and PR-Agent:

```python
class IssueQualityScore(BaseModel):
    completeness: float      # Has repro steps, severity, evidence?
    clarity: float          # Clear title, description?
    duplication_score: float # Similarity to existing issues
    priority_score: float   # Impact vs urgency matrix
```

### New: Coverage Verification

Inspired by Qodo Cover:

```python
class CoverageResult(BaseModel):
    before_coverage: float
    after_coverage: float
    delta: float
    tests_added: list[str]
    tests_passed: list[str]
```

## UI Design

### Layout

- Dense left-nav and queue-first navigation inspired by Linear Next
- Detail panels for issue metadata, evidence, and runtime operations
- Execution pane for agent runs with log streaming
- Activity panel with event history and summaries

### New: Run Progress View

Inspired by SWE-agent's trajectory view:

```
┌─ Run Progress ─────────────────────────────────┐
│ ● Planning (current)                            │
│ ○ Execution                                     │
│ ○ Review                                        │
│ ○ Verification                                  │
├────────────────────────────────────────────────┤
│ Plan Preview:                                   │
│ 1. Search for authenticate_user function        │
│ 2. Add null check for token expiry             │
│ 3. Add test case                               │
├────────────────────────────────────────────────┤
│ [Approve] [Modify] [Cancel]                    │
└────────────────────────────────────────────────┘
```

### New: Cost Dashboard

Inspired by OpenHands metrics dashboard:

```
┌─ Run Costs ────────────────────────────────────┐
│ Total this session: $2.47                      │
│ Tokens: 145,230 (12,450 prompt / 132,780 comp) │
├────────────────────────────────────────────────┤
│ P0_25M03_001    $0.82   ████████████          │
│ P1_25M03_002    $0.54   ████████              │
│ P2_25M03_003    $1.11   ████████████████      │
└────────────────────────────────────────────────┘
```

## Integration Patterns

### Agent Runtime Integration

Inspired by OpenHands plugin system:

```python
# Runtime discovery
runtimes = service.runtime_service.detect_runtimes()
# Returns available runtimes with binary paths and models

# Runtime probe (health check)
probe = service.probe_runtime(workspace_id, "codex", "gpt-5.4")
# Verifies runtime is accessible and model is available
```

### External Tool Integration

Inspired by PR-Agent's git providers:

- **GitHub**: Issue import, PR creation for fixes
- **Slack**: Run completion notifications
- **Linear/Jira**: Issue sync

## Security

### Approval Gates

Inspired by cline's approval system:

- Planning checkpoint requires user approval before execution
- High-cost runs (> $5) require explicit confirmation
- File modification limits configurable per workspace

### Audit Trail

- All operations logged to `activity.jsonl`
- Run trajectories persisted for replay and debugging
- Cost records maintained for budget tracking
