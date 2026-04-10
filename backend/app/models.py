from __future__ import annotations

from datetime import datetime, timezone
from typing import Literal, Optional

from pydantic import BaseModel, Field, model_validator


IssueStatus = Literal[
    "open",
    "triaged",
    "investigating",
    "in_progress",
    "verification",
    "resolved",
    "partial",
]
IssueSource = Literal["ledger", "verdict", "manual", "signal", "tracker"]
RunStatus = Literal["queued", "planning", "running", "completed", "failed", "cancelled"]
PlanPhase = Literal["pending", "generating", "awaiting_approval", "approved", "modified", "rejected", "executing"]
SignalKind = Literal["annotation", "exception_swallow", "not_implemented", "test_marker"]
RuntimeKind = Literal["codex", "opencode"]
FixStatus = Literal["proposed", "in_review", "applied", "verified", "rejected"]
RunReviewDisposition = Literal["dismissed", "investigation_only"]
RunbookScope = Literal["issue", "workspace"]
VerificationState = Literal["yes", "no", "unknown"]


def utc_now() -> str:
    return datetime.now(timezone.utc).isoformat()


def activity_actor_key(
    kind: Literal["operator", "agent", "system"],
    name: str,
    runtime: Optional[RuntimeKind] = None,
    model: Optional[str] = None,
) -> str:
    parts = [kind, runtime or name]
    return ":".join(part for part in parts if part)


def activity_actor_label(
    kind: Literal["operator", "agent", "system"],
    name: str,
    runtime: Optional[RuntimeKind] = None,
    model: Optional[str] = None,
) -> str:
    if runtime and model:
        return f"{runtime}:{model}"
    if runtime:
        return runtime
    return name


def build_activity_actor(
    kind: Literal["operator", "agent", "system"],
    name: str,
    runtime: Optional[RuntimeKind] = None,
    model: Optional[str] = None,
) -> "ActivityActor":
    return ActivityActor(
        kind=kind,
        name=name,
        runtime=runtime,
        model=model,
        key=activity_actor_key(kind, name, runtime, model),
        label=activity_actor_label(kind, name, runtime, model),
    )


class EvidenceRef(BaseModel):
    path: str
    line: Optional[int] = None
    excerpt: Optional[str] = None
    normalized_path: Optional[str] = None
    path_exists: Optional[bool] = None
    path_scope: Optional[str] = None


class IssueRecord(BaseModel):
    bug_id: str
    title: str
    severity: str
    issue_status: IssueStatus = "open"
    source: IssueSource = "ledger"
    source_doc: Optional[str] = None
    doc_status: str = "open"
    code_status: str = "unknown"
    summary: Optional[str] = None
    impact: Optional[str] = None
    evidence: list[EvidenceRef] = Field(default_factory=list)
    verification_evidence: list[EvidenceRef] = Field(default_factory=list)
    tests_added: list[str] = Field(default_factory=list)
    tests_passed: list[str] = Field(default_factory=list)
    drift_flags: list[str] = Field(default_factory=list)
    labels: list[str] = Field(default_factory=list)
    notes: Optional[str] = None
    verified_at: Optional[str] = None
    verified_by: Optional[str] = None
    needs_followup: bool = False
    review_ready_count: int = 0
    review_ready_runs: list[str] = Field(default_factory=list)
    fingerprint: Optional[str] = None
    updated_at: str = Field(default_factory=utc_now)


class DiscoverySignal(BaseModel):
    signal_id: str
    kind: SignalKind
    severity: str
    title: str
    summary: str
    file_path: str
    line: int
    evidence: list[EvidenceRef] = Field(default_factory=list)
    tags: list[str] = Field(default_factory=list)
    fingerprint: str
    promoted_bug_id: Optional[str] = None
    created_at: str = Field(default_factory=utc_now)


class TreeNode(BaseModel):
    path: str
    name: str
    node_type: Literal["directory", "file"]
    has_children: bool = False
    size_bytes: Optional[int] = None


class WorkspaceRecord(BaseModel):
    workspace_id: str
    name: str
    root_path: str
    latest_scan_at: Optional[str] = None
    created_at: str = Field(default_factory=utc_now)
    updated_at: str = Field(default_factory=utc_now)


class WorktreeStatus(BaseModel):
    available: bool = False
    is_git_repo: bool = False
    branch: Optional[str] = None
    head_sha: Optional[str] = None
    dirty_files: int = 0
    staged_files: int = 0
    untracked_files: int = 0
    ahead: int = 0
    behind: int = 0
    dirty_paths: list[str] = Field(default_factory=list)


class SavedIssueView(BaseModel):
    view_id: str
    workspace_id: str
    name: str
    query: str = ""
    severities: list[str] = Field(default_factory=list)
    statuses: list[str] = Field(default_factory=list)
    sources: list[str] = Field(default_factory=list)
    labels: list[str] = Field(default_factory=list)
    drift_only: bool = False
    needs_followup: Optional[bool] = None
    review_ready_only: bool = False
    created_at: str = Field(default_factory=utc_now)
    updated_at: str = Field(default_factory=utc_now)


class SourceRecord(BaseModel):
    source_id: str
    kind: Literal["ledger", "verdict_bundle", "scanner", "tracker_issue", "fix_record"]
    label: str
    path: str
    record_count: int
    modified_at: Optional[str] = None
    notes: Optional[str] = None


class RuntimeModel(BaseModel):
    runtime: RuntimeKind
    id: str


class RuntimeCapabilities(BaseModel):
    runtime: RuntimeKind
    available: bool
    binary_path: Optional[str] = None
    models: list[RuntimeModel] = Field(default_factory=list)
    notes: Optional[str] = None


class AppSettings(BaseModel):
    local_agent_type: RuntimeKind = "codex"
    codex_bin: Optional[str] = None
    opencode_bin: Optional[str] = None
    codex_args: Optional[str] = None
    codex_model: Optional[str] = None
    opencode_model: Optional[str] = None


class ActivityActor(BaseModel):
    kind: Literal["operator", "agent", "system"] = "operator"
    name: str
    runtime: Optional[RuntimeKind] = None
    model: Optional[str] = None
    key: Optional[str] = None
    label: Optional[str] = None

    @model_validator(mode="after")
    def populate_identity(self) -> "ActivityActor":
        if not self.key:
            self.key = activity_actor_key(self.kind, self.name, self.runtime, self.model)
        if not self.label:
            self.label = activity_actor_label(self.kind, self.name, self.runtime, self.model)
        return self


class ActivityRecord(BaseModel):
    activity_id: str
    workspace_id: str
    entity_type: Literal["issue", "run", "view", "signal", "workspace", "settings", "fix"]
    entity_id: str
    action: str
    summary: str
    actor: ActivityActor
    issue_id: Optional[str] = None
    run_id: Optional[str] = None
    details: dict = Field(default_factory=dict)
    created_at: str = Field(default_factory=utc_now)


class ActivityRollupItem(BaseModel):
    key: str
    label: str
    count: int
    actor_key: Optional[str] = None
    action: Optional[str] = None
    entity_type: Optional[str] = None


class ActivityOverview(BaseModel):
    total_events: int = 0
    unique_actors: int = 0
    unique_actions: int = 0
    operator_events: int = 0
    agent_events: int = 0
    system_events: int = 0
    issues_touched: int = 0
    fixes_touched: int = 0
    runs_touched: int = 0
    views_touched: int = 0
    counts_by_entity_type: dict[str, int] = Field(default_factory=dict)
    top_actors: list[ActivityRollupItem] = Field(default_factory=list)
    top_actions: list[ActivityRollupItem] = Field(default_factory=list)
    top_entities: list[ActivityRollupItem] = Field(default_factory=list)
    most_recent_at: Optional[str] = None


class LocalAgentCapabilities(BaseModel):
    selected_runtime: RuntimeKind
    supports_live_subscribe: bool
    supports_terminal: bool = True
    runtimes: list[RuntimeCapabilities] = Field(default_factory=list)


class RuntimeProbeResult(BaseModel):
    runtime: RuntimeKind
    model: str
    ok: bool
    available: bool
    checked_at: str = Field(default_factory=utc_now)
    duration_ms: int = 0
    exit_code: Optional[int] = None
    binary_path: Optional[str] = None
    command_preview: Optional[str] = None
    output_excerpt: Optional[str] = None
    error: Optional[str] = None


class RunRecord(BaseModel):
    run_id: str
    workspace_id: str
    issue_id: str
    runtime: RuntimeKind
    model: str
    status: RunStatus
    title: str
    prompt: str
    command: list[str]
    command_preview: str
    log_path: str
    output_path: str
    created_at: str = Field(default_factory=utc_now)
    started_at: Optional[str] = None
    completed_at: Optional[str] = None
    exit_code: Optional[int] = None
    pid: Optional[int] = None
    error: Optional[str] = None
    runbook_id: Optional[str] = None
    worktree: Optional[WorktreeStatus] = None
    summary: Optional[dict] = None
    plan: Optional[RunPlan] = None


class FixRecord(BaseModel):
    fix_id: str
    workspace_id: str
    issue_id: str
    status: FixStatus = "proposed"
    summary: str
    how: Optional[str] = None
    actor: ActivityActor
    run_id: Optional[str] = None
    session_id: Optional[str] = None
    changed_files: list[str] = Field(default_factory=list)
    tests_run: list[str] = Field(default_factory=list)
    evidence: list[EvidenceRef] = Field(default_factory=list)
    worktree: Optional[WorktreeStatus] = None
    notes: Optional[str] = None
    updated_at: str = Field(default_factory=utc_now)
    recorded_at: str = Field(default_factory=utc_now)


class RunReviewRecord(BaseModel):
    review_id: str
    workspace_id: str
    run_id: str
    issue_id: str
    disposition: RunReviewDisposition
    actor: ActivityActor
    notes: Optional[str] = None
    created_at: str = Field(default_factory=utc_now)


class RunbookRecord(BaseModel):
    runbook_id: str
    workspace_id: str
    name: str
    description: str
    scope: RunbookScope = "issue"
    template: str
    built_in: bool = False
    created_at: str = Field(default_factory=utc_now)
    updated_at: str = Field(default_factory=utc_now)


class ReviewQueueItem(BaseModel):
    run: RunRecord
    issue: IssueRecord
    draft: Optional["FixDraftSuggestion"] = None


class VerificationRecord(BaseModel):
    verification_id: str
    workspace_id: str
    issue_id: str
    run_id: str
    runtime: RuntimeKind
    model: str
    code_checked: VerificationState = "unknown"
    fixed: VerificationState = "unknown"
    confidence: Literal["high", "medium", "low"] = "low"
    summary: str = ""
    evidence: list[str] = Field(default_factory=list)
    tests: list[str] = Field(default_factory=list)
    actor: ActivityActor
    raw_excerpt: Optional[str] = None
    created_at: str = Field(default_factory=utc_now)


class VerificationSummary(BaseModel):
    workspace_id: str
    issue_id: str
    records: list[VerificationRecord] = Field(default_factory=list)
    checked_yes: int = 0
    checked_no: int = 0
    checked_unknown: int = 0
    fixed_yes: int = 0
    fixed_no: int = 0
    fixed_unknown: int = 0
    consensus_code_checked: VerificationState = "unknown"
    consensus_fixed: VerificationState = "unknown"


class PlanStep(BaseModel):
    step_id: str
    description: str
    estimated_impact: Literal["low", "medium", "high"] = "medium"
    files_affected: list[str] = Field(default_factory=list)
    risks: list[str] = Field(default_factory=list)


class RunPlan(BaseModel):
    plan_id: str
    run_id: str
    phase: PlanPhase = "pending"
    steps: list[PlanStep] = Field(default_factory=list)
    summary: str = ""
    reasoning: Optional[str] = None
    created_at: str = Field(default_factory=utc_now)
    approved_at: Optional[str] = None
    approver: Optional[str] = None
    feedback: Optional[str] = None
    modified_summary: Optional[str] = None


class WorkspaceSnapshot(BaseModel):
    workspace: WorkspaceRecord
    summary: dict[str, int]
    issues: list[IssueRecord]
    signals: list[DiscoverySignal]
    sources: list[SourceRecord] = Field(default_factory=list)
    drift_summary: dict[str, int] = Field(default_factory=dict)
    runtimes: list[RuntimeCapabilities]
    latest_ledger: Optional[str] = None
    latest_verdicts: Optional[str] = None
    generated_at: str = Field(default_factory=utc_now)


class WorkspaceLoadRequest(BaseModel):
    root_path: str
    name: Optional[str] = None
    auto_scan: bool = True
    prefer_cached_snapshot: bool = True


class RunRequest(BaseModel):
    runtime: RuntimeKind
    model: str
    instruction: Optional[str] = None
    runbook_id: Optional[str] = None
    planning: bool = False


class AgentQueryRequest(BaseModel):
    runtime: RuntimeKind
    model: str
    prompt: str


class RuntimeProbeRequest(BaseModel):
    runtime: RuntimeKind
    model: str


class IssueUpdateRequest(BaseModel):
    severity: Optional[str] = None
    issue_status: Optional[IssueStatus] = None
    doc_status: Optional[str] = None
    code_status: Optional[str] = None
    labels: Optional[list[str]] = None
    notes: Optional[str] = None
    needs_followup: Optional[bool] = None


class IssueCreateRequest(BaseModel):
    bug_id: Optional[str] = None
    title: str
    severity: str
    summary: Optional[str] = None
    impact: Optional[str] = None
    issue_status: IssueStatus = "open"
    doc_status: str = "open"
    code_status: str = "unknown"
    labels: list[str] = Field(default_factory=list)
    notes: Optional[str] = None
    source_doc: Optional[str] = None
    needs_followup: bool = False


class FixRecordRequest(BaseModel):
    status: FixStatus = "proposed"
    summary: str
    how: Optional[str] = None
    run_id: Optional[str] = None
    runtime: Optional[RuntimeKind] = None
    model: Optional[str] = None
    changed_files: list[str] = Field(default_factory=list)
    tests_run: list[str] = Field(default_factory=list)
    notes: Optional[str] = None
    issue_status: Optional[IssueStatus] = None
    evidence: list[EvidenceRef] = Field(default_factory=list)


class FixUpdateRequest(BaseModel):
    status: Optional[FixStatus] = None
    notes: Optional[str] = None
    issue_status: Optional[IssueStatus] = None


class FixDraftSuggestion(BaseModel):
    workspace_id: str
    issue_id: str
    run_id: str
    summary: str
    how: Optional[str] = None
    changed_files: list[str] = Field(default_factory=list)
    tests_run: list[str] = Field(default_factory=list)
    suggested_issue_status: Optional[IssueStatus] = None
    source_excerpt: Optional[str] = None


class RunReviewRequest(BaseModel):
    disposition: RunReviewDisposition
    notes: Optional[str] = None


class RunAcceptRequest(BaseModel):
    issue_status: Optional[IssueStatus] = "verification"
    notes: Optional[str] = None


class RunbookUpsertRequest(BaseModel):
    runbook_id: Optional[str] = None
    name: str
    description: str = ""
    scope: RunbookScope = "issue"
    template: str


class VerifyIssueRequest(BaseModel):
    runtime: RuntimeKind = "opencode"
    models: list[str] = Field(default_factory=list)
    runbook_id: Optional[str] = "verify"
    instruction: Optional[str] = None
    timeout_seconds: float = 60.0
    poll_interval: float = 2.0


class SavedIssueViewRequest(BaseModel):
    name: str
    query: str = ""
    severities: list[str] = Field(default_factory=list)
    statuses: list[str] = Field(default_factory=list)
    sources: list[str] = Field(default_factory=list)
    labels: list[str] = Field(default_factory=list)
    drift_only: bool = False
    needs_followup: Optional[bool] = None
    review_ready_only: bool = False


class PromoteSignalRequest(BaseModel):
    title: Optional[str] = None
    severity: Optional[str] = None
    labels: list[str] = Field(default_factory=list)


class ExportBundle(BaseModel):
    workspace: WorkspaceRecord
    snapshot: WorkspaceSnapshot
    runs: list[RunRecord]
    fixes: list[FixRecord] = Field(default_factory=list)
    run_reviews: list[RunReviewRecord] = Field(default_factory=list)
    runbooks: list[RunbookRecord] = Field(default_factory=list)
    verifications: list[VerificationRecord] = Field(default_factory=list)
    activity: list[ActivityRecord] = Field(default_factory=list)
    exported_at: str = Field(default_factory=utc_now)


class IssueContextPacket(BaseModel):
    issue: IssueRecord
    workspace: WorkspaceRecord
    tree_focus: list[str] = Field(default_factory=list)
    evidence_bundle: list[EvidenceRef] = Field(default_factory=list)
    recent_fixes: list[FixRecord] = Field(default_factory=list)
    recent_activity: list[ActivityRecord] = Field(default_factory=list)
    runbook: list[str] = Field(default_factory=list)
    available_runbooks: list[RunbookRecord] = Field(default_factory=list)
    worktree: Optional[WorktreeStatus] = None
    prompt: str


class IssueDriftDetail(BaseModel):
    bug_id: str
    drift_flags: list[str] = Field(default_factory=list)
    missing_evidence: list[EvidenceRef] = Field(default_factory=list)
    verification_gap: bool = False


class TerminalOpenRequest(BaseModel):
    workspace_id: str
    cols: int = 120
    rows: int = 36
    terminal_id: Optional[str] = None


class TerminalWriteRequest(BaseModel):
    data: str


class TerminalResizeRequest(BaseModel):
    cols: int
    rows: int


class PlanApproveRequest(BaseModel):
    feedback: Optional[str] = None


class PlanRejectRequest(BaseModel):
    reason: str
