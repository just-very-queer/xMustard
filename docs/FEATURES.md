# Feature Specifications

This document contains detailed specifications for xMustard features.

## F1: Planning Checkpoint System

### Description

Before an agent executes a fix, it must generate a plan that the user can review and approve. This ensures human oversight and prevents costly mistakes.

### User Flow

1. User clicks "Start Run" on an issue
2. System shows "Generating plan..." with live output
3. Plan appears in preview panel
4. User can: Approve, Modify Plan, or Cancel
5. If approved, execution begins
6. If modified, agent re-generates plan with feedback

### Data Model

```python
class PlanPhase(str, Enum):
    PENDING = "pending"
    GENERATING = "generating"
    AWAITING_APPROVAL = "awaiting_approval"
    APPROVED = "approved"
    MODIFIED = "modified"
    REJECTED = "rejected"
    EXECUTING = "executing"

class PlanStep(BaseModel):
    step_id: str
    description: str
    estimated_impact: str  # "low", "medium", "high"
    files_affected: list[str]
    risks: list[str]

class RunPlan(BaseModel):
    plan_id: str
    run_id: str
    phase: PlanPhase
    steps: list[PlanStep]
    summary: str
    created_at: str
    approved_at: Optional[str]
    approver: Optional[str]
    feedback: Optional[str]
```

### API Endpoints

```
POST /api/workspaces/{id}/runs/{run_id}/plan
  → Triggers plan generation
  → Returns: { plan_id, phase, steps, summary }

GET /api/workspaces/{id}/runs/{run_id}/plan
  → Returns current plan status and content

POST /api/workspaces/{id}/runs/{run_id}/plan/approve
  Body: { feedback?: string }
  → Approves plan or provides modification feedback

POST /api/workspaces/{id}/runs/{run_id}/plan/reject
  Body: { reason: string }
  → Cancels the run
```

### UI Components

**PlanPreview** (in ExecutionPane):
- Step list with expandable details
- Risk indicators per step
- Files affected badges
- Approve/Modify/Cancel buttons

### Implementation Notes

- Plan generation uses same context packet as execution
- Planning prompt includes: issue details, codebase snapshot, similar past fixes
- Agent outputs structured JSON plan via tool call
- Timeout for plan generation: 60 seconds
- Max plan steps: 10

---

## F2: Cost Tracking

### Description

Track token usage and cost for each agent run, with per-workspace and per-session aggregations.

### Data Model

```python
class RunMetrics(BaseModel):
    tokens_used: int = 0
    prompt_tokens: int = 0
    completion_tokens: int = 0
    cost_usd: float = 0.0
    duration_ms: int = 0
    model: str
    priced_at: str  # Model pricing used

# Pricing (example - configurable)
MODEL_PRICING = {
    "gpt-5.4": {"prompt": 0.000015, "completion": 0.00006},
    "gpt-5.4-mini": {"prompt": 0.000003, "completion": 0.000012},
    "o3": {"prompt": 0.000015, "completion": 0.00006},
}
```

### API Endpoints

```
GET /api/workspaces/{id}/runs/{run_id}/metrics
  → Returns RunMetrics for specific run

GET /api/workspaces/{id}/costs?period=session|week|month
  → Returns aggregated costs:
  {
    total_usd: float,
    by_issue: { bug_id: cost_usd },
    by_model: { model: { count, cost_usd } },
    trend: [{ date, cost_usd }]
  }
```

### UI Components

**CostSummary** (dashboard):
- Total cost display (large number)
- Pie chart by model
- Bar chart by issue (top 10)
- Trend line over time

**RunList** (in QueuePane):
- Cost column showing run cost
- Color coding: green (<$0.50), yellow ($0.50-$2), red (>$2)

### Implementation Notes

- Token counting done via API response metadata
- Cost calculated on run completion
- Historical runs not retroactively priced (uses pricing at time of run)
- Workspace cost limit configurable (warn at 80%, block at 100%)

---

## F3: Issue Quality Scoring

### Description

Automatically assess issue quality to help triage and prioritize.

### Data Model

```python
class IssueQualityScore(BaseModel):
    completeness: float  # 0.0 - 1.0
    clarity: float       # 0.0 - 1.0
    duplication_score: float  # 0.0 - 1.0 (higher = more likely duplicate)
    priority_score: float  # 0.0 - 1.0
    
    overall: float  # Weighted average
    
    issues: list[str]  # IDs of potential duplicates

class QualityDimensions(BaseModel):
    has_repro_steps: bool
    has_severity: bool
    has_evidence: bool
    has_impact: bool
    title_length_ok: bool  # 10-100 chars
    description_length_ok: bool  # >50 chars
```

### Scoring Rules

**Completeness (40% weight):**
- +0.25: Has reproduction steps
- +0.25: Has severity (P0-P3)
- +0.25: Has evidence (file/line refs)
- +0.25: Has impact statement

**Clarity (30% weight):**
- Title length 10-100 chars: +0.5
- Description >50 chars: +0.5

**Duplication (30% weight):**
- TF-IDF similarity to existing issues
- >0.8 similarity: high duplicate risk
- >0.6 similarity: medium duplicate risk

### API Endpoints

```
GET /api/workspaces/{id}/issues/{issue_id}/quality
  → Returns IssueQualityScore

POST /api/workspaces/{id}/issues/{issue_id}/quality/recalculate
  → Force recalculation (e.g., after edit)

GET /api/workspaces/{id}/issues/quality?min_quality=0.5
  → List issues filtered by quality score
```

### UI Components

**QualityBadge** (in QueuePane):
- Green: overall >= 0.7
- Yellow: overall 0.4-0.7
- Red: overall < 0.4

**QualityPanel** (in DetailPane):
- Radar chart showing dimensions
- List of missing fields with "Add" buttons
- Similar issues with similarity scores

---

## F4: Coverage Verification

### Description

Track test coverage before and after fixes to verify verification quality.

### Data Model

```python
class CoverageResult(BaseModel):
    run_id: str
    issue_id: str
    coverage_type: Literal["line", "branch", "statement"]
    
    baseline: CoverageSnapshot
    after_fix: CoverageSnapshot
    delta: float  # Percentage point change
    
    tests_added: list[str]
    tests_passed: list[str]
    tests_failed: list[str]

class CoverageSnapshot(BaseModel):
    percent: float
    covered_lines: int
    total_lines: int
    covered_branches: int
    total_branches: int
```

### Coverage Parsers

Supported formats:
- Cobertura (XML)
- JaCoCo (XML)
- lcov (text)
- go coverage (text)
- Simple percentage input

### API Endpoints

```
POST /api/workspaces/{id}/runs/{run_id}/coverage
  Body: { coverage_file: string, format: string }
  → Upload coverage file, parse, store result

GET /api/workspaces/{id}/issues/{issue_id}/coverage
  → Returns latest CoverageResult

GET /api/workspaces/{id}/coverage?baseline=true
  → Returns baseline coverage for workspace
```

### UI Components

**CoverageDelta** (in DetailPane):
- Before/after bar comparison
- Delta with +/- indicator
- List of new tests

**CoverageDashboard** (new tab):
- Per-directory coverage map
- Trend over time
- Coverage goals with progress bars

---

## F5: Post-Run Review

### Description

After a run completes, generate automated review of the proposed fix.

### Data Model

```python
class PatchReview(BaseModel):
    review_id: str
    run_id: str
    
    correctness: float  # 0-1
    style_score: float
    security_score: float
    
    issues: list[ReviewIssue]
    suggestions: list[ReviewSuggestion]
    
    summary: str
    verdict: Literal["approve", "request_changes", "reject"]

class ReviewIssue(BaseModel):
    severity: Literal["critical", "major", "minor"]
    location: str
    description: str
    line: Optional[int]

class ReviewSuggestion(BaseModel):
    category: Literal["refactor", "optimize", "document", "test"]
    description: str
    effort: Literal["low", "medium", "high"]
```

### API Endpoints

```
POST /api/workspaces/{id}/runs/{run_id}/review
  → Triggers patch review generation
  → Returns: { review_id, status: "processing" }

GET /api/workspaces/{id}/runs/{run_id}/review
  → Returns PatchReview if complete
```

### UI Components

**ReviewPanel** (in ExecutionPane, after run):
- Verdict badge (approve/request_changes/reject)
- Issue list with severity colors
- Suggestion cards with accept/dismiss
- Overall scores as gauge charts

---

## F6: Multi-Agent Orchestration

### Description

Allow spawning multiple agent runs in parallel for independent issues.

### Data Model

```python
class AgentTask(BaseModel):
    task_id: str
    issue_id: str
    runtime: RuntimeKind
    model: str
    instruction: str
    status: Literal["queued", "running", "completed", "failed"]
    result: Optional[RunRecord]

class AgentSwarm(BaseModel):
    swarm_id: str
    workspace_id: str
    name: str
    tasks: list[AgentTask]
    strategy: Literal["parallel", "sequential", "priority"]
    max_concurrent: int = 5
    created_at: str
    completed_at: Optional[str]
```

### API Endpoints

```
POST /api/workspaces/{id}/swarm
  Body: { issue_ids: list[str], strategy: string, max_concurrent: int }
  → Creates swarm, starts runs
  → Returns: { swarm_id, tasks }

GET /api/workspaces/{id}/swarm/{swarm_id}
  → Returns swarm status and task results

POST /api/workspaces/{id}/swarm/{swarm_id}/cancel
  → Cancels all pending tasks
```

### UI Components

**SwarmDashboard** (new view):
- Grid of task cards with status
- Progress indicator (X/Y completed)
- Aggregate cost display
- Cancel all / Cancel individual buttons

---

## F7: GitHub Integration

### Description

Import issues from GitHub, sync status, create PRs from accepted fixes.

### Data Model

```python
class GitHubConfig(BaseModel):
    repo: str  # "owner/repo"
    token: str  # encrypted
    import_labels: list[str]  # Label names to import
    sync_enabled: bool

class GitHubIssue(BaseModel):
    number: int
    title: str
    body: str
    labels: list[str]
    state: str
    created_at: str
    updated_at: str
```

### API Endpoints

```
POST /api/workspaces/{id}/github/connect
  Body: { repo: string, token: string }
  → Validate connection, store config

GET /api/workspaces/{id}/github/issues?state=open
  → List GitHub issues

POST /api/workspaces/{id}/github/import/{issue_number}
  → Import GitHub issue as xMustard issue

POST /api/workspaces/{id}/fixes/{fix_id}/create-pr
  → Create GitHub PR from accepted fix
```

---

## F8: Slack Integration

### Description

Send notifications to Slack on run events.

### Data Model

```python
class SlackConfig(BaseModel):
    webhook_url: str  # encrypted
    channel: str
    events: list[Literal["run_complete", "verification_pass", "verification_fail", "drift_detected"]]
    mention_rules: list[dict]  # e.g., P0 → @oncall
```

### API Endpoints

```
POST /api/workspaces/{id}/slack/configure
  Body: SlackConfig
  → Validate webhook, store config

POST /api/workspaces/{id}/slack/test
  → Send test message

GET /api/workspaces/{id}/slack/config
  → Returns config (webhook_url masked)
```

---

## Appendix: Configuration Schema

```python
class AppSettings(BaseModel):
    local_agent_type: RuntimeKind = "codex"
    codex_bin: Optional[str] = None
    opencode_bin: Optional[str] = None
    codex_args: Optional[str] = None
    codex_model: Optional[str] = None
    opencode_model: Optional[str] = None
    
    # New settings
    enable_planning_checkpoint: bool = True
    enable_cost_tracking: bool = True
    cost_limit_usd: float = 100.0
    max_concurrent_runs: int = 3
    
    github_config: Optional[GitHubConfig] = None
    slack_config: Optional[SlackConfig] = None
    
    model_pricing: dict[str, dict[str, float]]  # Override default pricing
```
