from __future__ import annotations

from datetime import datetime, timezone
import uuid
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
PlanOwnershipMode = Literal["agent", "operator", "shared"]
SignalKind = Literal["annotation", "exception_swallow", "not_implemented", "test_marker"]
RuntimeKind = Literal["codex", "opencode"]
FixStatus = Literal["proposed", "in_review", "applied", "verified", "rejected"]
RunReviewDisposition = Literal["dismissed", "investigation_only"]
RunbookScope = Literal["issue", "workspace"]
VerificationState = Literal["yes", "no", "unknown"]
GuidanceKind = Literal["agent_instructions", "conventions", "repo_index", "skill", "workspace_overview"]
GuidanceHealthStatus = Literal["healthy", "partial", "missing", "stale"]
GuidanceStarterTemplate = Literal["agents", "openhands_repo", "conventions"]
CoverageFormat = Literal["unknown", "cobertura", "jacoco", "lcov", "go"]
TicketProvider = Literal["github", "jira", "linear", "manual", "incident", "other"]
ThreatModelMethodology = Literal["manual", "stride", "threat_dragon", "pytm", "threagile", "attack_path"]
ThreatModelStatus = Literal["draft", "reviewed", "accepted"]
BrowserDumpSource = Literal["mcp-chrome", "manual", "playwright", "other"]
VulnerabilityFindingSource = Literal["manual", "nessus-json", "sarif", "semgrep-json", "trivy-json", "other"]
VulnerabilitySeverity = Literal["critical", "high", "medium", "low", "info"]
VulnerabilityStatus = Literal["open", "triaged", "fixing", "fixed", "verified", "closed"]
IngestionPhaseId = Literal[
    "repo_scan",
    "repo_map",
    "runtime_discovery",
    "tree_sitter_index",
    "ast_grep_rules",
    "lsp_enrichment",
    "search_materialization",
]
IngestionDeliveryState = Literal["complete", "ready", "blocked"]
IngestionImplementationState = Literal["implemented", "partial", "planned"]


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
    kind: Literal["ledger", "verdict_bundle", "scanner", "tracker_issue", "fix_record", "ticket_context", "threat_model", "repo_map"]
    label: str
    path: str
    record_count: int
    modified_at: Optional[str] = None
    notes: Optional[str] = None


class RepoMapDirectoryRecord(BaseModel):
    path: str
    file_count: int = 0
    source_file_count: int = 0
    test_file_count: int = 0


class RepoMapFileRecord(BaseModel):
    path: str
    role: Literal["guide", "config", "entry", "test", "source"] = "source"
    size_bytes: Optional[int] = None


class RepoMapSummary(BaseModel):
    workspace_id: str
    root_path: str
    total_files: int = 0
    source_files: int = 0
    test_files: int = 0
    top_extensions: dict[str, int] = Field(default_factory=dict)
    top_directories: list[RepoMapDirectoryRecord] = Field(default_factory=list)
    key_files: list[RepoMapFileRecord] = Field(default_factory=list)
    generated_at: str = Field(default_factory=utc_now)


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
    postgres_dsn: Optional[str] = None
    postgres_schema: str = "xmustard"


class PostgresSchemaPlan(BaseModel):
    configured: bool = False
    dsn_redacted: Optional[str] = None
    schema_name: str = "xmustard"
    sql_path: str
    statement_count: int = 0
    table_names: list[str] = Field(default_factory=list)
    semantic_table_names: list[str] = Field(default_factory=list)
    ops_memory_table_names: list[str] = Field(default_factory=list)
    search_document_tables: list[str] = Field(default_factory=list)
    generated_at: str = Field(default_factory=utc_now)


class PostgresBootstrapRequest(BaseModel):
    dsn: Optional[str] = None
    schema_name: Optional[str] = None


class PostgresBootstrapResult(BaseModel):
    applied: bool = False
    dsn_redacted: Optional[str] = None
    schema_name: str = "xmustard"
    sql_path: str
    statement_count: int = 0
    table_names: list[str] = Field(default_factory=list)
    semantic_table_names: list[str] = Field(default_factory=list)
    search_document_tables: list[str] = Field(default_factory=list)
    message: str
    generated_at: str = Field(default_factory=utc_now)


class PostgresPathMaterializationRequest(BaseModel):
    path: str
    dsn: Optional[str] = None
    schema_name: Optional[str] = None


class PostgresSemanticSearchMaterializationRequest(BaseModel):
    pattern: str
    language: Optional[str] = None
    path_glob: Optional[str] = None
    limit: int = 50
    dsn: Optional[str] = None
    schema_name: Optional[str] = None


class PostgresWorkspaceSemanticMaterializationRequest(BaseModel):
    strategy: Literal["key_files", "paths"] = "key_files"
    paths: list[str] = Field(default_factory=list)
    limit: int = 12
    dsn: Optional[str] = None
    schema_name: Optional[str] = None


class SemanticIndexPathSelection(BaseModel):
    path: str
    role: Literal["guide", "config", "entry", "test", "source", "doc", "asset", "unknown"] = "unknown"
    score: int = 0
    reason: str = ""
    sha256: Optional[str] = None


class SemanticIndexPlan(BaseModel):
    workspace_id: str
    root_path: str
    surface: Literal["cli", "web", "all"] = "cli"
    strategy: Literal["key_files", "paths"] = "key_files"
    requested_paths: list[str] = Field(default_factory=list)
    selected_paths: list[str] = Field(default_factory=list)
    selected_path_details: list[SemanticIndexPathSelection] = Field(default_factory=list)
    head_sha: Optional[str] = None
    dirty_files: int = 0
    worktree_dirty: bool = False
    index_fingerprint: Optional[str] = None
    postgres_configured: bool = False
    postgres_schema: str = "xmustard"
    tree_sitter_available: bool = False
    ast_grep_available: bool = False
    run_target_count: int = 0
    verify_target_count: int = 0
    retrieval_ledger: list["ContextRetrievalLedgerEntry"] = Field(default_factory=list)
    blockers: list[str] = Field(default_factory=list)
    warnings: list[str] = Field(default_factory=list)
    next_actions: list[str] = Field(default_factory=list)
    can_run: bool = False
    generated_at: str = Field(default_factory=utc_now)


class PostgresSemanticMaterializationResult(BaseModel):
    applied: bool = False
    dsn_redacted: Optional[str] = None
    schema_name: str = "xmustard"
    workspace_id: str
    source: Literal["path_symbols", "semantic_search"] = "path_symbols"
    target: str
    materialized_paths: list[str] = Field(default_factory=list)
    file_rows: int = 0
    symbol_rows: int = 0
    summary_rows: int = 0
    query_rows: int = 0
    match_rows: int = 0
    message: str
    generated_at: str = Field(default_factory=utc_now)


class PostgresWorkspaceSemanticMaterializationResult(BaseModel):
    applied: bool = False
    dsn_redacted: Optional[str] = None
    schema_name: str = "xmustard"
    workspace_id: str
    strategy: Literal["key_files", "paths"] = "key_files"
    requested_paths: list[str] = Field(default_factory=list)
    materialized_paths: list[str] = Field(default_factory=list)
    skipped_paths: list[str] = Field(default_factory=list)
    file_rows: int = 0
    symbol_rows: int = 0
    summary_rows: int = 0
    message: str
    generated_at: str = Field(default_factory=utc_now)


class SemanticIndexRunResult(BaseModel):
    workspace_id: str
    surface: Literal["cli", "web", "all"] = "cli"
    dry_run: bool = False
    plan: SemanticIndexPlan
    materialization: Optional[PostgresWorkspaceSemanticMaterializationResult] = None
    message: str
    generated_at: str = Field(default_factory=utc_now)


class SemanticIndexBaselineRecord(BaseModel):
    index_run_id: str
    workspace_id: str
    surface: Literal["cli", "web", "all"] = "cli"
    strategy: Literal["key_files", "paths"] = "key_files"
    index_fingerprint: str
    head_sha: Optional[str] = None
    dirty_files: int = 0
    worktree_dirty: bool = False
    selected_paths: list[str] = Field(default_factory=list)
    selected_path_details: list[SemanticIndexPathSelection] = Field(default_factory=list)
    materialized_paths: list[str] = Field(default_factory=list)
    file_rows: int = 0
    symbol_rows: int = 0
    summary_rows: int = 0
    postgres_schema: str = "xmustard"
    tree_sitter_available: bool = False
    ast_grep_available: bool = False
    created_at: str = Field(default_factory=utc_now)


class SemanticIndexStatus(BaseModel):
    workspace_id: str
    surface: Literal["cli", "web", "all"] = "cli"
    status: Literal["fresh", "stale", "dirty_provisional", "no_baseline", "blocked"] = "no_baseline"
    postgres_configured: bool = False
    postgres_schema: str = "xmustard"
    current_fingerprint: Optional[str] = None
    current_head_sha: Optional[str] = None
    current_dirty_files: int = 0
    baseline: Optional[SemanticIndexBaselineRecord] = None
    fingerprint_match: bool = False
    stale_reasons: list[str] = Field(default_factory=list)
    warnings: list[str] = Field(default_factory=list)
    generated_at: str = Field(default_factory=utc_now)


class IngestionDependencyRecord(BaseModel):
    dependency_id: str
    kind: Literal["workspace", "artifact", "setting", "tool", "implementation"] = "artifact"
    label: str
    satisfied: bool = False
    detail: Optional[str] = None


class IngestionPhaseRecord(BaseModel):
    phase_id: IngestionPhaseId
    label: str
    description: str
    implementation_state: IngestionImplementationState = "planned"
    delivery_state: IngestionDeliveryState = "blocked"
    dependencies: list[IngestionDependencyRecord] = Field(default_factory=list)
    blockers: list[str] = Field(default_factory=list)
    outputs: list[str] = Field(default_factory=list)
    evidence: list[str] = Field(default_factory=list)


class IngestionPipelinePlan(BaseModel):
    workspace_id: str
    root_path: str
    postgres_configured: bool = False
    postgres_schema: str = "xmustard"
    phases: list[IngestionPhaseRecord] = Field(default_factory=list)
    completed_phase_count: int = 0
    ready_phase_ids: list[IngestionPhaseId] = Field(default_factory=list)
    blocked_phase_ids: list[IngestionPhaseId] = Field(default_factory=list)
    next_phase_id: Optional[IngestionPhaseId] = None
    generated_at: str = Field(default_factory=utc_now)


class RepoChangeRecord(BaseModel):
    path: str
    status: Literal["modified", "added", "deleted", "renamed", "copied", "untracked", "unknown"] = "unknown"
    scope: Literal["since_ref", "working_tree"] = "working_tree"
    previous_path: Optional[str] = None
    staged: bool = False
    unstaged: bool = False


class RepoChangeSummary(BaseModel):
    workspace_id: str
    base_ref: str = "HEAD"
    is_git_repo: bool = False
    branch: Optional[str] = None
    head_sha: Optional[str] = None
    dirty_files: int = 0
    changed_files: list[RepoChangeRecord] = Field(default_factory=list)
    generated_at: str = Field(default_factory=utc_now)


class ChangedSymbolRecord(BaseModel):
    path: str
    symbol: str
    kind: Literal["function", "class", "method", "type", "module"] = "function"
    line_start: Optional[int] = None
    line_end: Optional[int] = None
    evidence_source: Literal["stored_semantic", "on_demand_parser"] = "on_demand_parser"
    semantic_status: Optional[Literal["fresh", "stale", "dirty_provisional", "no_baseline", "blocked"]] = None
    selection_reason: str = ""
    change_scopes: list[Literal["since_ref", "working_tree"]] = Field(default_factory=list)
    change_statuses: list[str] = Field(default_factory=list)


class ImpactPathRecord(BaseModel):
    path: str
    reason: str
    derivation_source: Literal["lexical", "structural", "hybrid"] = "lexical"
    score: int = 0


class ImpactReport(BaseModel):
    workspace_id: str
    base_ref: str = "HEAD"
    semantic_status: Optional["SemanticIndexStatus"] = None
    changed_files: list[RepoChangeRecord] = Field(default_factory=list)
    changed_symbols: list[ChangedSymbolRecord] = Field(default_factory=list)
    likely_affected_files: list[ImpactPathRecord] = Field(default_factory=list)
    likely_affected_tests: list[ImpactPathRecord] = Field(default_factory=list)
    derivation_summary: str = ""
    confidence: Literal["low", "medium", "high"] = "low"
    warnings: list[str] = Field(default_factory=list)
    generated_at: str = Field(default_factory=utc_now)


class RepoTargetRecord(BaseModel):
    target_id: str
    kind: Literal["dev", "run", "build", "test", "lint", "verify", "service", "other"] = "other"
    label: str
    command: str
    source: Literal["package_json", "makefile", "docker_compose", "verification_profile", "heuristic"] = "heuristic"
    source_path: str
    confidence: int = 50


class CodeExplainerResult(BaseModel):
    workspace_id: str
    path: str
    role: Literal["guide", "config", "entry", "test", "source", "doc", "asset", "unknown"] = "unknown"
    line_count: int = 0
    import_count: int = 0
    detected_symbols: list[str] = Field(default_factory=list)
    symbol_source: Literal["tree_sitter", "regex", "none"] = "none"
    parser_language: Optional[str] = None
    evidence_source: Literal["stored_semantic", "on_demand_parser"] = "on_demand_parser"
    selection_reason: str = ""
    semantic_status: Optional["SemanticIndexStatus"] = None
    summary: str
    hints: list[str] = Field(default_factory=list)
    warnings: list[str] = Field(default_factory=list)
    generated_at: str = Field(default_factory=utc_now)


class PathSymbolsResult(BaseModel):
    workspace_id: str
    path: str
    symbol_source: Literal["tree_sitter", "regex", "none"] = "none"
    parser_language: Optional[str] = None
    evidence_source: Literal["stored_semantic", "on_demand_parser"] = "on_demand_parser"
    selection_reason: str = ""
    semantic_status: Optional["SemanticIndexStatus"] = None
    symbols: list["RepoMapSymbolRecord"] = Field(default_factory=list)
    file_summary_row: Optional["FileSymbolSummaryMaterializationRecord"] = None
    symbol_rows: list["SymbolMaterializationRecord"] = Field(default_factory=list)
    warnings: list[str] = Field(default_factory=list)
    generated_at: str = Field(default_factory=utc_now)


class SemanticPatternMatchRecord(BaseModel):
    path: str
    language: Optional[str] = None
    line_start: Optional[int] = None
    line_end: Optional[int] = None
    column_start: Optional[int] = None
    column_end: Optional[int] = None
    matched_text: str
    context_lines: Optional[str] = None
    meta_variables: list[str] = Field(default_factory=list)
    reason: Optional[str] = None
    score: int = 0


class FileSymbolSummaryMaterializationRecord(BaseModel):
    workspace_id: str
    path: str
    language: Optional[str] = None
    parser_language: Optional[str] = None
    symbol_source: Literal["tree_sitter", "regex", "none"] = "none"
    symbol_count: int = 0
    summary_json: dict = Field(default_factory=dict)


class SymbolMaterializationRecord(BaseModel):
    workspace_id: str
    path: str
    symbol: str
    kind: Literal["function", "class", "method", "type", "module"] = "function"
    language: Optional[str] = None
    line_start: Optional[int] = None
    line_end: Optional[int] = None
    enclosing_scope: Optional[str] = None
    signature_text: Optional[str] = None
    symbol_text: Optional[str] = None


class SemanticQueryMaterializationRecord(BaseModel):
    query_ref: str
    workspace_id: str
    issue_id: Optional[str] = None
    run_id: Optional[str] = None
    source: Literal["adhoc_tool", "issue_context"] = "adhoc_tool"
    reason: Optional[str] = None
    pattern: str
    language: Optional[str] = None
    path_glob: Optional[str] = None
    engine: Literal["ast_grep", "none"] = "none"
    match_count: int = 0
    truncated: bool = False
    error: Optional[str] = None


class SemanticMatchMaterializationRecord(BaseModel):
    query_ref: str
    workspace_id: str
    path: str
    language: Optional[str] = None
    line_start: Optional[int] = None
    line_end: Optional[int] = None
    column_start: Optional[int] = None
    column_end: Optional[int] = None
    matched_text: str
    context_lines: Optional[str] = None
    meta_variables: list[str] = Field(default_factory=list)
    reason: Optional[str] = None
    score: int = 0


class SemanticPatternQueryResult(BaseModel):
    workspace_id: str
    pattern: str
    language: Optional[str] = None
    path_glob: Optional[str] = None
    engine: Literal["ast_grep", "none"] = "none"
    binary_path: Optional[str] = None
    match_count: int = 0
    truncated: bool = False
    matches: list[SemanticPatternMatchRecord] = Field(default_factory=list)
    query_row: Optional[SemanticQueryMaterializationRecord] = None
    match_rows: list[SemanticMatchMaterializationRecord] = Field(default_factory=list)
    error: Optional[str] = None
    generated_at: str = Field(default_factory=utc_now)


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


class RepoToolState(BaseModel):
    workspace: WorkspaceRecord
    snapshot_summary: dict[str, int] = Field(default_factory=dict)
    worktree: WorktreeStatus = Field(default_factory=WorktreeStatus)
    repo_map: Optional[RepoMapSummary] = None
    activity_overview: Optional[ActivityOverview] = None
    recent_activity: list[ActivityRecord] = Field(default_factory=list)
    repo_config_health: Optional["RepoConfigHealth"] = None
    guidance_health: Optional["RepoGuidanceHealth"] = None
    generated_at: str = Field(default_factory=utc_now)


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
    eval_scenario_id: Optional[str] = None
    eval_replay_batch_id: Optional[str] = None
    worktree: Optional[WorktreeStatus] = None
    guidance_paths: list[str] = Field(default_factory=list)
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


class VerificationProfileRecord(BaseModel):
    profile_id: str
    workspace_id: str
    name: str
    description: str = ""
    test_command: str
    coverage_command: Optional[str] = None
    coverage_report_path: Optional[str] = None
    coverage_format: CoverageFormat = "unknown"
    max_runtime_seconds: int = 30
    retry_count: int = 1
    source_paths: list[str] = Field(default_factory=list)
    checklist_items: list[str] = Field(default_factory=list)
    built_in: bool = False
    created_at: str = Field(default_factory=utc_now)
    updated_at: str = Field(default_factory=utc_now)


class VerificationCommandResult(BaseModel):
    command: str
    cwd: str
    exit_code: Optional[int] = None
    success: bool = False
    timed_out: bool = False
    duration_ms: int = 0
    stdout_excerpt: str = ""
    stderr_excerpt: str = ""
    created_at: str = Field(default_factory=utc_now)


class VerificationChecklistResult(BaseModel):
    item_id: str
    title: str
    kind: Literal["system", "custom"] = "custom"
    passed: bool = False
    details: Optional[str] = None


class VerificationProfileExecutionResult(BaseModel):
    execution_id: str = Field(default_factory=lambda: f"vpr_{uuid.uuid4().hex[:12]}")
    profile_id: str
    workspace_id: str
    profile_name: str = ""
    issue_id: Optional[str] = None
    run_id: Optional[str] = None
    attempts: list[VerificationCommandResult] = Field(default_factory=list)
    attempt_count: int = 0
    success: bool = False
    coverage_command_result: Optional[VerificationCommandResult] = None
    coverage_result: Optional["CoverageResult"] = None
    checklist_results: list[VerificationChecklistResult] = Field(default_factory=list)
    confidence: Literal["high", "medium", "low"] = "low"
    coverage_report_path: Optional[str] = None
    created_at: str = Field(default_factory=utc_now)


class VerificationProfileReport(BaseModel):
    profile_id: str
    workspace_id: str
    profile_name: str
    built_in: bool = False
    issue_id: Optional[str] = None
    total_runs: int = 0
    success_runs: int = 0
    failed_runs: int = 0
    success_rate: float = 0.0
    confidence_counts: dict[str, int] = Field(default_factory=dict)
    avg_attempt_count: float = 0.0
    checklist_pass_rate: float = 0.0
    last_run_at: Optional[str] = None
    last_issue_id: Optional[str] = None
    last_run_id: Optional[str] = None
    last_confidence: Optional[Literal["high", "medium", "low"]] = None
    last_success: Optional[bool] = None
    runtime_breakdown: list["VerificationProfileDimensionSummary"] = Field(default_factory=list)
    model_breakdown: list["VerificationProfileDimensionSummary"] = Field(default_factory=list)
    branch_breakdown: list["VerificationProfileDimensionSummary"] = Field(default_factory=list)


class VerificationProfileDimensionSummary(BaseModel):
    key: str
    label: str
    total_runs: int = 0
    success_runs: int = 0
    failed_runs: int = 0
    success_rate: float = 0.0
    last_run_at: Optional[str] = None


class TicketContextRecord(BaseModel):
    context_id: str
    workspace_id: str
    issue_id: str
    provider: TicketProvider = "manual"
    external_id: Optional[str] = None
    title: str
    summary: str = ""
    acceptance_criteria: list[str] = Field(default_factory=list)
    links: list[str] = Field(default_factory=list)
    labels: list[str] = Field(default_factory=list)
    status: Optional[str] = None
    source_excerpt: Optional[str] = None
    created_at: str = Field(default_factory=utc_now)
    updated_at: str = Field(default_factory=utc_now)


class ThreatModelRecord(BaseModel):
    threat_model_id: str
    workspace_id: str
    issue_id: str
    title: str
    methodology: ThreatModelMethodology = "manual"
    summary: str = ""
    assets: list[str] = Field(default_factory=list)
    entry_points: list[str] = Field(default_factory=list)
    trust_boundaries: list[str] = Field(default_factory=list)
    abuse_cases: list[str] = Field(default_factory=list)
    mitigations: list[str] = Field(default_factory=list)
    references: list[str] = Field(default_factory=list)
    status: ThreatModelStatus = "draft"
    created_at: str = Field(default_factory=utc_now)
    updated_at: str = Field(default_factory=utc_now)


class IssueContextReplayRecord(BaseModel):
    replay_id: str
    workspace_id: str
    issue_id: str
    label: str
    prompt: str
    tree_focus: list[str] = Field(default_factory=list)
    guidance_paths: list[str] = Field(default_factory=list)
    verification_profile_ids: list[str] = Field(default_factory=list)
    ticket_context_ids: list[str] = Field(default_factory=list)
    browser_dump_ids: list[str] = Field(default_factory=list)
    created_at: str = Field(default_factory=utc_now)


class IssueContextReplayComparison(BaseModel):
    replay: IssueContextReplayRecord
    current_prompt: str
    current_tree_focus: list[str] = Field(default_factory=list)
    current_guidance_paths: list[str] = Field(default_factory=list)
    current_verification_profile_ids: list[str] = Field(default_factory=list)
    current_ticket_context_ids: list[str] = Field(default_factory=list)
    current_browser_dump_ids: list[str] = Field(default_factory=list)
    prompt_changed: bool = False
    changed: bool = False
    saved_prompt_length: int = 0
    current_prompt_length: int = 0
    added_tree_focus: list[str] = Field(default_factory=list)
    removed_tree_focus: list[str] = Field(default_factory=list)
    added_guidance_paths: list[str] = Field(default_factory=list)
    removed_guidance_paths: list[str] = Field(default_factory=list)
    added_verification_profile_ids: list[str] = Field(default_factory=list)
    removed_verification_profile_ids: list[str] = Field(default_factory=list)
    added_ticket_context_ids: list[str] = Field(default_factory=list)
    removed_ticket_context_ids: list[str] = Field(default_factory=list)
    added_browser_dump_ids: list[str] = Field(default_factory=list)
    removed_browser_dump_ids: list[str] = Field(default_factory=list)
    summary: str
    compared_at: str = Field(default_factory=utc_now)


class BrowserDumpRecord(BaseModel):
    dump_id: str
    workspace_id: str
    issue_id: str
    source: BrowserDumpSource = "mcp-chrome"
    label: str
    page_url: Optional[str] = None
    page_title: Optional[str] = None
    summary: str = ""
    dom_snapshot: str = ""
    console_messages: list[str] = Field(default_factory=list)
    network_requests: list[str] = Field(default_factory=list)
    screenshot_path: Optional[str] = None
    notes: Optional[str] = None
    created_at: str = Field(default_factory=utc_now)
    updated_at: str = Field(default_factory=utc_now)


class VulnerabilityFindingRecord(BaseModel):
    finding_id: str
    workspace_id: str
    issue_id: str
    scanner: str
    source: VulnerabilityFindingSource = "manual"
    severity: VulnerabilitySeverity = "medium"
    status: VulnerabilityStatus = "open"
    title: str
    summary: str = ""
    rule_id: Optional[str] = None
    location_path: Optional[str] = None
    location_line: Optional[int] = None
    cwe_ids: list[str] = Field(default_factory=list)
    cve_ids: list[str] = Field(default_factory=list)
    references: list[str] = Field(default_factory=list)
    evidence: list[str] = Field(default_factory=list)
    threat_model_ids: list[str] = Field(default_factory=list)
    raw_payload: Optional[str] = None
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


class PlanFileAttachment(BaseModel):
    path: str
    source: Literal["step", "manual", "observed"] = "step"
    note: Optional[str] = None
    exists: Optional[bool] = None


class PlanRevision(BaseModel):
    revision_id: str
    version: int = 1
    phase: PlanPhase = "pending"
    summary: str = ""
    feedback: Optional[str] = None
    ownership_mode: PlanOwnershipMode = "agent"
    owner_label: Optional[str] = None
    branch: Optional[str] = None
    head_sha: Optional[str] = None
    dirty_paths: list[str] = Field(default_factory=list)
    attached_files: list[PlanFileAttachment] = Field(default_factory=list)
    created_at: str = Field(default_factory=utc_now)


class RunPlan(BaseModel):
    plan_id: str
    run_id: str
    phase: PlanPhase = "pending"
    steps: list[PlanStep] = Field(default_factory=list)
    summary: str = ""
    reasoning: Optional[str] = None
    version: int = 1
    ownership_mode: PlanOwnershipMode = "agent"
    owner_label: Optional[str] = None
    branch: Optional[str] = None
    head_sha: Optional[str] = None
    dirty_paths: list[str] = Field(default_factory=list)
    attached_files: list[PlanFileAttachment] = Field(default_factory=list)
    revisions: list[PlanRevision] = Field(default_factory=list)
    created_at: str = Field(default_factory=utc_now)
    updated_at: str = Field(default_factory=utc_now)
    approved_at: Optional[str] = None
    approver: Optional[str] = None
    feedback: Optional[str] = None
    modified_summary: Optional[str] = None


class WorkspaceSnapshot(BaseModel):
    scanner_version: int = 1
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
    eval_scenario_id: Optional[str] = None
    eval_replay_batch_id: Optional[str] = None
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


class GuidanceStarterRequest(BaseModel):
    template_id: GuidanceStarterTemplate
    overwrite: bool = False


class GuidanceStarterResult(BaseModel):
    workspace_id: str
    template_id: GuidanceStarterTemplate
    path: str
    created: bool = True
    overwritten: bool = False
    generated_at: str = Field(default_factory=utc_now)


class VerificationProfileUpsertRequest(BaseModel):
    profile_id: Optional[str] = None
    name: str
    description: str = ""
    test_command: str
    coverage_command: Optional[str] = None
    coverage_report_path: Optional[str] = None
    coverage_format: CoverageFormat = "unknown"
    max_runtime_seconds: int = 30
    retry_count: int = 1
    source_paths: list[str] = Field(default_factory=list)
    checklist_items: list[str] = Field(default_factory=list)


class TicketContextUpsertRequest(BaseModel):
    context_id: Optional[str] = None
    provider: TicketProvider = "manual"
    external_id: Optional[str] = None
    title: str
    summary: str = ""
    acceptance_criteria: list[str] = Field(default_factory=list)
    links: list[str] = Field(default_factory=list)
    labels: list[str] = Field(default_factory=list)
    status: Optional[str] = None
    source_excerpt: Optional[str] = None


class ThreatModelUpsertRequest(BaseModel):
    threat_model_id: Optional[str] = None
    title: str
    methodology: ThreatModelMethodology = "manual"
    summary: str = ""
    assets: list[str] = Field(default_factory=list)
    entry_points: list[str] = Field(default_factory=list)
    trust_boundaries: list[str] = Field(default_factory=list)
    abuse_cases: list[str] = Field(default_factory=list)
    mitigations: list[str] = Field(default_factory=list)
    references: list[str] = Field(default_factory=list)
    status: ThreatModelStatus = "draft"


class IssueContextReplayRequest(BaseModel):
    label: Optional[str] = None


class BrowserDumpUpsertRequest(BaseModel):
    dump_id: Optional[str] = None
    source: BrowserDumpSource = "mcp-chrome"
    label: str
    page_url: Optional[str] = None
    page_title: Optional[str] = None
    summary: str = ""
    dom_snapshot: str = ""
    console_messages: list[str] = Field(default_factory=list)
    network_requests: list[str] = Field(default_factory=list)
    screenshot_path: Optional[str] = None
    notes: Optional[str] = None


class VulnerabilityFindingUpsertRequest(BaseModel):
    finding_id: Optional[str] = None
    scanner: str
    source: VulnerabilityFindingSource = "manual"
    severity: VulnerabilitySeverity = "medium"
    status: VulnerabilityStatus = "open"
    title: str
    summary: str = ""
    rule_id: Optional[str] = None
    location_path: Optional[str] = None
    location_line: Optional[int] = None
    cwe_ids: list[str] = Field(default_factory=list)
    cve_ids: list[str] = Field(default_factory=list)
    references: list[str] = Field(default_factory=list)
    evidence: list[str] = Field(default_factory=list)
    threat_model_ids: list[str] = Field(default_factory=list)
    raw_payload: Optional[str] = None


class VulnerabilityImportRequest(BaseModel):
    source: VulnerabilityFindingSource
    payload: str


class VulnerabilityImportBatchRecord(BaseModel):
    batch_id: str
    workspace_id: str
    issue_id: str
    source: VulnerabilityFindingSource
    scanner: str
    finding_ids: list[str] = Field(default_factory=list)
    total_findings: int = 0
    summary_counts: dict[str, int] = Field(default_factory=dict)
    payload_sha256: Optional[str] = None
    created_at: str = Field(default_factory=utc_now)


class VulnerabilityFindingReportItem(BaseModel):
    finding_id: str
    scanner: str
    source: VulnerabilityFindingSource
    severity: VulnerabilitySeverity
    status: VulnerabilityStatus
    title: str
    summary: str = ""
    rule_id: Optional[str] = None
    location_path: Optional[str] = None
    location_line: Optional[int] = None
    cwe_ids: list[str] = Field(default_factory=list)
    cve_ids: list[str] = Field(default_factory=list)
    references: list[str] = Field(default_factory=list)
    evidence: list[str] = Field(default_factory=list)
    threat_model_ids: list[str] = Field(default_factory=list)
    linked_threat_model_titles: list[str] = Field(default_factory=list)
    created_at: str = Field(default_factory=utc_now)
    updated_at: str = Field(default_factory=utc_now)


class VulnerabilityFindingReport(BaseModel):
    workspace_id: str
    issue_id: str
    total_findings: int = 0
    by_severity: dict[str, int] = Field(default_factory=dict)
    by_status: dict[str, int] = Field(default_factory=dict)
    linked_threat_models: list[ThreatModelRecord] = Field(default_factory=list)
    findings: list[VulnerabilityFindingReportItem] = Field(default_factory=list)
    generated_at: str = Field(default_factory=utc_now)


class WorkspaceVulnerabilityIssueRollup(BaseModel):
    issue_id: str
    title: str
    issue_severity: str
    highest_vulnerability_severity: VulnerabilitySeverity = "info"
    total_findings: int = 0
    open_findings: int = 0
    linked_threat_models: int = 0
    scanners: list[str] = Field(default_factory=list)


class WorkspaceVulnerabilityReport(BaseModel):
    workspace_id: str
    total_findings: int = 0
    by_severity: dict[str, int] = Field(default_factory=dict)
    by_status: dict[str, int] = Field(default_factory=dict)
    by_source: dict[str, int] = Field(default_factory=dict)
    linked_threat_models_total: int = 0
    linked_threat_models: list[ThreatModelRecord] = Field(default_factory=list)
    issue_rollups: list[WorkspaceVulnerabilityIssueRollup] = Field(default_factory=list)
    generated_at: str = Field(default_factory=utc_now)


class WorkspaceSecurityReviewBundle(BaseModel):
    workspace_id: str
    total_findings: int = 0
    open_findings: int = 0
    linked_threat_models: list[ThreatModelRecord] = Field(default_factory=list)
    top_findings: list[VulnerabilityFindingReportItem] = Field(default_factory=list)
    issue_rollups: list[WorkspaceVulnerabilityIssueRollup] = Field(default_factory=list)
    recent_activity: list[ActivityRecord] = Field(default_factory=list)
    generated_at: str = Field(default_factory=utc_now)


class VerifyIssueRequest(BaseModel):
    runtime: RuntimeKind = "opencode"
    models: list[str] = Field(default_factory=list)
    runbook_id: Optional[str] = "verify"
    instruction: Optional[str] = None
    timeout_seconds: float = 60.0
    poll_interval: float = 2.0


class VerificationProfileRunRequest(BaseModel):
    run_id: Optional[str] = None


class EvalScenarioUpsertRequest(BaseModel):
    scenario_id: Optional[str] = None
    name: str
    issue_id: str
    description: Optional[str] = None
    baseline_replay_id: Optional[str] = None
    guidance_paths: list[str] = Field(default_factory=list)
    ticket_context_ids: list[str] = Field(default_factory=list)
    verification_profile_ids: list[str] = Field(default_factory=list)
    run_ids: list[str] = Field(default_factory=list)
    browser_dump_ids: list[str] = Field(default_factory=list)
    notes: Optional[str] = None


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
    repo_map: Optional[RepoMapSummary] = None
    runs: list[RunRecord]
    fixes: list[FixRecord] = Field(default_factory=list)
    run_reviews: list[RunReviewRecord] = Field(default_factory=list)
    runbooks: list[RunbookRecord] = Field(default_factory=list)
    verification_profiles: list[VerificationProfileRecord] = Field(default_factory=list)
    verification_profile_history: list[VerificationProfileExecutionResult] = Field(default_factory=list)
    ticket_contexts: list[TicketContextRecord] = Field(default_factory=list)
    threat_models: list[ThreatModelRecord] = Field(default_factory=list)
    context_replays: list[IssueContextReplayRecord] = Field(default_factory=list)
    browser_dumps: list[BrowserDumpRecord] = Field(default_factory=list)
    vulnerability_findings: list[VulnerabilityFindingRecord] = Field(default_factory=list)
    eval_scenarios: list["EvalScenarioRecord"] = Field(default_factory=list)
    verifications: list[VerificationRecord] = Field(default_factory=list)
    activity: list[ActivityRecord] = Field(default_factory=list)
    exported_at: str = Field(default_factory=utc_now)


class RepoMapSymbolRecord(BaseModel):
    path: str
    symbol: str
    kind: Literal["function", "class", "method", "type", "module"] = "function"
    line_start: Optional[int] = None
    line_end: Optional[int] = None
    enclosing_scope: Optional[str] = None
    evidence_source: Literal["stored_semantic", "on_demand_parser"] = "on_demand_parser"
    reason: Optional[str] = None
    score: int = 0


class RelatedContextRecord(BaseModel):
    artifact_type: Literal["ticket_context", "threat_model", "browser_dump", "vulnerability_finding", "fix_record", "activity", "run"]
    artifact_id: str
    title: str
    path: Optional[str] = None
    reason: Optional[str] = None
    matched_terms: list[str] = Field(default_factory=list)
    score: int = 0


class ContextRetrievalLedgerEntry(BaseModel):
    entry_id: str
    source_type: Literal[
        "evidence",
        "related_path",
        "stored_symbol",
        "on_demand_symbol",
        "semantic_match",
        "stored_semantic_match",
        "semantic_index_path",
        "lexical_hit",
        "structural_hit",
        "artifact",
        "guidance",
        "path_instruction",
    ]
    source_id: str
    title: str
    path: Optional[str] = None
    reason: str
    matched_terms: list[str] = Field(default_factory=list)
    score: int = 0


class RetrievalSearchHit(BaseModel):
    path: str
    source_type: Literal["lexical_hit", "structural_hit", "stored_symbol", "stored_semantic_match"] = "lexical_hit"
    title: str
    reason: str
    matched_terms: list[str] = Field(default_factory=list)
    score: int = 0


class RetrievalSearchResult(BaseModel):
    workspace_id: str
    query: str
    hits: list[RetrievalSearchHit] = Field(default_factory=list)
    retrieval_ledger: list[ContextRetrievalLedgerEntry] = Field(default_factory=list)
    warnings: list[str] = Field(default_factory=list)
    generated_at: str = Field(default_factory=utc_now)


class DynamicContextBundle(BaseModel):
    symbol_context: list[RepoMapSymbolRecord] = Field(default_factory=list)
    semantic_matches: list[SemanticPatternMatchRecord] = Field(default_factory=list)
    semantic_queries: list[SemanticQueryMaterializationRecord] = Field(default_factory=list)
    semantic_match_rows: list[SemanticMatchMaterializationRecord] = Field(default_factory=list)
    related_context: list[RelatedContextRecord] = Field(default_factory=list)


class RepoMCPServerRecord(BaseModel):
    name: str
    description: str = ""
    usage: str = ""


class RepoPathInstructionRecord(BaseModel):
    instruction_id: str
    path: str
    instructions: str
    title: Optional[str] = None
    source_path: str


class RepoPathInstructionMatch(BaseModel):
    instruction_id: str
    path: str
    title: Optional[str] = None
    instructions: str
    source_path: str
    matched_paths: list[str] = Field(default_factory=list)


class RepoConfigRecord(BaseModel):
    workspace_id: str
    source_path: Optional[str] = None
    description: str = ""
    path_filters: list[str] = Field(default_factory=list)
    path_instructions: list[RepoPathInstructionRecord] = Field(default_factory=list)
    code_guidelines: list[str] = Field(default_factory=list)
    mcp_servers: list[RepoMCPServerRecord] = Field(default_factory=list)
    loaded_at: str = Field(default_factory=utc_now)


class RepoConfigHealth(BaseModel):
    workspace_id: str
    status: Literal["missing", "configured"] = "missing"
    source_path: Optional[str] = None
    summary: str
    path_instruction_count: int = 0
    path_filter_count: int = 0
    code_guideline_count: int = 0
    mcp_server_count: int = 0
    loaded_at: str = Field(default_factory=utc_now)


class IssueContextPacket(BaseModel):
    issue: IssueRecord
    workspace: WorkspaceRecord
    semantic_status: Optional[SemanticIndexStatus] = None
    tree_focus: list[str] = Field(default_factory=list)
    related_paths: list[str] = Field(default_factory=list)
    evidence_bundle: list[EvidenceRef] = Field(default_factory=list)
    recent_fixes: list[FixRecord] = Field(default_factory=list)
    recent_activity: list[ActivityRecord] = Field(default_factory=list)
    guidance: list["RepoGuidanceRecord"] = Field(default_factory=list)
    runbook: list[str] = Field(default_factory=list)
    available_runbooks: list[RunbookRecord] = Field(default_factory=list)
    available_verification_profiles: list[VerificationProfileRecord] = Field(default_factory=list)
    ticket_contexts: list[TicketContextRecord] = Field(default_factory=list)
    threat_models: list[ThreatModelRecord] = Field(default_factory=list)
    browser_dumps: list[BrowserDumpRecord] = Field(default_factory=list)
    vulnerability_findings: list[VulnerabilityFindingRecord] = Field(default_factory=list)
    repo_map: Optional[RepoMapSummary] = None
    dynamic_context: Optional[DynamicContextBundle] = None
    retrieval_ledger: list[ContextRetrievalLedgerEntry] = Field(default_factory=list)
    repo_config: Optional[RepoConfigRecord] = None
    matched_path_instructions: list[RepoPathInstructionMatch] = Field(default_factory=list)
    worktree: Optional[WorktreeStatus] = None
    prompt: str


class RepoContextTargetLink(BaseModel):
    target: RepoTargetRecord
    reason: str
    score: int = 0


class RepoContextPlanLink(BaseModel):
    run_id: str
    issue_id: str
    status: RunStatus
    phase: Optional[PlanPhase] = None
    ownership_mode: Optional[PlanOwnershipMode] = None
    owner_label: Optional[str] = None
    attached_files: list[str] = Field(default_factory=list)
    reason: str
    score: int = 0


class RepoContextActivityLink(BaseModel):
    action: str
    summary: str
    issue_id: Optional[str] = None
    run_id: Optional[str] = None
    created_at: str
    reason: str
    score: int = 0


class RepoContextFixLink(BaseModel):
    fix_id: str
    issue_id: str
    run_id: Optional[str] = None
    summary: str
    changed_files: list[str] = Field(default_factory=list)
    tests_run: list[str] = Field(default_factory=list)
    recorded_at: str
    reason: str


class RepoContextRecord(BaseModel):
    workspace_id: str
    base_ref: str = "HEAD"
    semantic_status: Optional[SemanticIndexStatus] = None
    impact: ImpactReport
    run_targets: list[RepoContextTargetLink] = Field(default_factory=list)
    verify_targets: list[RepoContextTargetLink] = Field(default_factory=list)
    plan_links: list[RepoContextPlanLink] = Field(default_factory=list)
    recent_activity: list[RepoContextActivityLink] = Field(default_factory=list)
    latest_accepted_fix: Optional[RepoContextFixLink] = None
    retrieval_ledger: list[ContextRetrievalLedgerEntry] = Field(default_factory=list)
    generated_at: str = Field(default_factory=utc_now)


class RepoGuidanceRecord(BaseModel):
    guidance_id: str
    workspace_id: str
    kind: GuidanceKind
    title: str
    path: str
    always_on: bool = False
    priority: int = 100
    summary: str = ""
    excerpt: Optional[str] = None
    trigger_keywords: list[str] = Field(default_factory=list)
    updated_at: Optional[str] = None


class GuidanceStarterRecord(BaseModel):
    template_id: GuidanceStarterTemplate
    title: str
    path: str
    description: str
    recommended: bool = True
    exists: bool = False
    stale: bool = False


class RepoGuidanceHealth(BaseModel):
    workspace_id: str
    status: GuidanceHealthStatus
    summary: str
    guidance_count: int = 0
    always_on_count: int = 0
    instruction_count: int = 0
    present_files: list[str] = Field(default_factory=list)
    missing_files: list[str] = Field(default_factory=list)
    stale_files: list[str] = Field(default_factory=list)
    recommended_files: list[str] = Field(default_factory=list)
    starters: list[GuidanceStarterRecord] = Field(default_factory=list)
    generated_at: str = Field(default_factory=utc_now)


class RunSessionInsight(BaseModel):
    workspace_id: str
    run_id: str
    issue_id: str
    status: RunStatus
    headline: str
    summary: str = ""
    guidance_used: list[str] = Field(default_factory=list)
    strengths: list[str] = Field(default_factory=list)
    risks: list[str] = Field(default_factory=list)
    recommendations: list[str] = Field(default_factory=list)
    acceptance_review: Optional["AcceptanceCriteriaReview"] = None
    scope_warnings: list["ScopeWarning"] = Field(default_factory=list)
    generated_at: str = Field(default_factory=utc_now)


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


class PlanTrackingUpdateRequest(BaseModel):
    ownership_mode: Optional[PlanOwnershipMode] = None
    owner_label: Optional[str] = None
    attached_files: list[str] = Field(default_factory=list)
    replace_attachments: bool = False
    feedback: Optional[str] = None


class RunMetrics(BaseModel):
    run_id: str
    workspace_id: str
    input_tokens: int = 0
    output_tokens: int = 0
    estimated_cost: float = 0.0
    duration_ms: int = 0
    model: str
    runtime: RuntimeKind
    calculated_at: str = Field(default_factory=utc_now)


class CostSummary(BaseModel):
    workspace_id: str
    total_runs: int = 0
    total_input_tokens: int = 0
    total_output_tokens: int = 0
    total_estimated_cost: float = 0.0
    total_duration_ms: int = 0
    runs_by_status: dict[str, int] = Field(default_factory=dict)
    cost_by_runtime: dict[str, float] = Field(default_factory=dict)
    cost_by_model: dict[str, float] = Field(default_factory=dict)
    period_start: Optional[str] = None
    period_end: Optional[str] = None


class IssueQualityScore(BaseModel):
    issue_id: str
    workspace_id: str
    overall: float = 0.0
    completeness: float = 0.0
    clarity: float = 0.0
    evidence_quality: float = 0.0
    has_repro: bool = False
    has_severity: bool = False
    has_evidence: bool = False
    has_impact: bool = False
    has_summary: bool = False
    title_length: int = 0
    summary_length: int = 0
    evidence_count: int = 0
    suggestions: list[str] = Field(default_factory=list)
    calculated_at: str = Field(default_factory=utc_now)


class DuplicateMatch(BaseModel):
    source_id: str
    target_id: str
    similarity: float
    match_type: Literal["exact", "fuzzy", "fingerprint"] = "fuzzy"
    shared_fields: list[str] = Field(default_factory=list)


class TriageSuggestion(BaseModel):
    issue_id: str
    workspace_id: str
    suggested_severity: Optional[str] = None
    suggested_labels: list[str] = Field(default_factory=list)
    suggested_owner: Optional[str] = None
    confidence: float = 0.0
    reasoning: str = ""
    calculated_at: str = Field(default_factory=utc_now)


class CoverageResult(BaseModel):
    result_id: str
    workspace_id: str
    run_id: Optional[str] = None
    issue_id: Optional[str] = None
    line_coverage: float = 0.0
    branch_coverage: Optional[float] = None
    function_coverage: Optional[float] = None
    lines_covered: int = 0
    lines_total: int = 0
    branches_covered: Optional[int] = None
    branches_total: Optional[int] = None
    files_covered: int = 0
    files_total: int = 0
    uncovered_files: list[str] = Field(default_factory=list)
    format: str = "unknown"
    raw_report_path: Optional[str] = None
    created_at: str = Field(default_factory=utc_now)


class CoverageDelta(BaseModel):
    workspace_id: str
    issue_id: str
    baseline: Optional[CoverageResult] = None
    current: Optional[CoverageResult] = None
    line_delta: float = 0.0
    branch_delta: Optional[float] = None
    lines_added: int = 0
    lines_lost: int = 0
    new_files_covered: list[str] = Field(default_factory=list)
    files_regressed: list[str] = Field(default_factory=list)
    calculated_at: str = Field(default_factory=utc_now)


class TestSuggestion(BaseModel):
    suggestion_id: str
    issue_id: str
    workspace_id: str
    test_file: str
    test_description: str
    priority: Literal["high", "medium", "low"] = "medium"
    rationale: str = ""
    suggested_code: Optional[str] = None
    created_at: str = Field(default_factory=utc_now)


class ImprovementSuggestion(BaseModel):
    suggestion_id: str
    file_path: str
    line_start: Optional[int] = None
    line_end: Optional[int] = None
    category: Literal["bug_risk", "style", "performance", "maintainability", "security", "testing"] = "maintainability"
    severity: Literal["high", "medium", "low"] = "medium"
    description: str = ""
    suggested_fix: Optional[str] = None
    dismissed: bool = False
    dismissed_reason: Optional[str] = None


class PatchCritique(BaseModel):
    critique_id: str
    workspace_id: str
    run_id: str
    issue_id: str
    overall_quality: Literal["excellent", "good", "acceptable", "needs_work", "poor"] = "acceptable"
    correctness: float = 0.0
    completeness: float = 0.0
    style: float = 0.0
    safety: float = 0.0
    issues_found: list[str] = Field(default_factory=list)
    improvements: list[ImprovementSuggestion] = Field(default_factory=list)
    summary: str = ""
    acceptance_review: Optional["AcceptanceCriteriaReview"] = None
    scope_warnings: list["ScopeWarning"] = Field(default_factory=list)
    generated_at: str = Field(default_factory=utc_now)


class AcceptanceCriteriaReview(BaseModel):
    status: Literal["met", "partial", "not_met", "unknown"] = "unknown"
    criteria: list[str] = Field(default_factory=list)
    matched: list[str] = Field(default_factory=list)
    missing: list[str] = Field(default_factory=list)
    notes: list[str] = Field(default_factory=list)


class ScopeWarning(BaseModel):
    kind: Literal["unrelated_change", "scope_drift"] = "scope_drift"
    message: str
    paths: list[str] = Field(default_factory=list)
    severity: Literal["low", "medium", "high"] = "medium"


class EvalScenarioRecord(BaseModel):
    scenario_id: str
    workspace_id: str
    issue_id: str
    name: str
    description: Optional[str] = None
    baseline_replay_id: Optional[str] = None
    guidance_paths: list[str] = Field(default_factory=list)
    ticket_context_ids: list[str] = Field(default_factory=list)
    verification_profile_ids: list[str] = Field(default_factory=list)
    run_ids: list[str] = Field(default_factory=list)
    browser_dump_ids: list[str] = Field(default_factory=list)
    notes: Optional[str] = None
    created_at: str = Field(default_factory=utc_now)
    updated_at: str = Field(default_factory=utc_now)


class EvalReplayBatchRecord(BaseModel):
    batch_id: str
    workspace_id: str
    issue_id: str
    runtime: RuntimeKind
    model: str
    scenario_ids: list[str] = Field(default_factory=list)
    queued_run_ids: list[str] = Field(default_factory=list)
    instruction: Optional[str] = None
    runbook_id: Optional[str] = None
    planning: bool = False
    created_at: str = Field(default_factory=utc_now)


class EvalScenarioVariantDiff(BaseModel):
    selected_guidance_paths: list[str] = Field(default_factory=list)
    current_guidance_paths: list[str] = Field(default_factory=list)
    added_guidance_paths: list[str] = Field(default_factory=list)
    removed_guidance_paths: list[str] = Field(default_factory=list)
    selected_ticket_context_ids: list[str] = Field(default_factory=list)
    current_ticket_context_ids: list[str] = Field(default_factory=list)
    added_ticket_context_ids: list[str] = Field(default_factory=list)
    removed_ticket_context_ids: list[str] = Field(default_factory=list)
    changed: bool = False
    summary: str = ""


class EvalVariantRollup(BaseModel):
    variant_kind: Literal["guidance", "ticket_context"] = "guidance"
    variant_key: str
    label: str
    selected_values: list[str] = Field(default_factory=list)
    scenario_ids: list[str] = Field(default_factory=list)
    scenario_names: list[str] = Field(default_factory=list)
    scenario_count: int = 0
    run_count: int = 0
    success_runs: int = 0
    failed_runs: int = 0
    total_estimated_cost: float = 0.0
    avg_duration_ms: int = 0
    verification_success_rate: float = 0.0
    runtime_breakdown: list["VerificationProfileDimensionSummary"] = Field(default_factory=list)
    model_breakdown: list["VerificationProfileDimensionSummary"] = Field(default_factory=list)
    summary: str = ""


class EvalScenarioReport(BaseModel):
    scenario: EvalScenarioRecord
    baseline_replay: Optional[IssueContextReplayRecord] = None
    latest_replay_comparison: Optional[IssueContextReplayComparison] = None
    variant_diff: Optional[EvalScenarioVariantDiff] = None
    comparison_to_baseline: Optional["EvalScenarioBaselineComparison"] = None
    latest_fresh_run: Optional["EvalFreshRunSummary"] = None
    fresh_comparison_to_baseline: Optional["EvalFreshExecutionComparison"] = None
    verification_profile_reports: list[VerificationProfileReport] = Field(default_factory=list)
    run_metrics: list[RunMetrics] = Field(default_factory=list)
    total_estimated_cost: float = 0.0
    avg_duration_ms: int = 0
    success_runs: int = 0
    failed_runs: int = 0
    verification_success_rate: float = 0.0
    summary: str = ""


class EvalScenarioBaselineComparison(BaseModel):
    compared_to_scenario_id: str
    compared_to_name: str
    guidance_only_in_scenario: list[str] = Field(default_factory=list)
    guidance_only_in_baseline: list[str] = Field(default_factory=list)
    ticket_context_only_in_scenario: list[str] = Field(default_factory=list)
    ticket_context_only_in_baseline: list[str] = Field(default_factory=list)
    browser_dump_only_in_scenario: list[str] = Field(default_factory=list)
    browser_dump_only_in_baseline: list[str] = Field(default_factory=list)
    verification_profile_only_in_scenario: list[str] = Field(default_factory=list)
    verification_profile_only_in_baseline: list[str] = Field(default_factory=list)
    verification_profile_deltas: list["EvalScenarioVerificationProfileDelta"] = Field(default_factory=list)
    success_runs_delta: int = 0
    failed_runs_delta: int = 0
    verification_success_rate_delta: float = 0.0
    avg_duration_ms_delta: int = 0
    total_estimated_cost_delta: float = 0.0
    preferred: Literal["scenario", "baseline", "tie"] = "tie"
    preferred_scenario_id: Optional[str] = None
    preferred_scenario_name: Optional[str] = None
    preference_reasons: list[str] = Field(default_factory=list)
    summary: str = ""


class EvalScenarioVerificationProfileDelta(BaseModel):
    profile_id: str
    profile_name: str
    present_in_scenario: bool = False
    present_in_baseline: bool = False
    scenario_total_runs: int = 0
    baseline_total_runs: int = 0
    total_runs_delta: int = 0
    scenario_success_rate: float = 0.0
    baseline_success_rate: float = 0.0
    success_rate_delta: float = 0.0
    scenario_checklist_pass_rate: float = 0.0
    baseline_checklist_pass_rate: float = 0.0
    checklist_pass_rate_delta: float = 0.0
    scenario_avg_attempt_count: float = 0.0
    baseline_avg_attempt_count: float = 0.0
    avg_attempt_count_delta: float = 0.0
    scenario_confidence_counts: dict[str, int] = Field(default_factory=dict)
    baseline_confidence_counts: dict[str, int] = Field(default_factory=dict)
    preferred: Literal["scenario", "baseline", "tie"] = "tie"
    summary: str = ""


class EvalFreshRunSummary(BaseModel):
    scenario_id: str
    scenario_name: str
    run_id: str
    status: RunStatus
    runtime: RuntimeKind
    model: str
    created_at: str
    estimated_cost: float = 0.0
    duration_ms: int = 0
    command_preview: Optional[str] = None
    planning: bool = False


class EvalFreshExecutionComparison(BaseModel):
    compared_to_scenario_id: str
    compared_to_name: str
    scenario_status: RunStatus
    baseline_status: RunStatus
    estimated_cost_delta: float = 0.0
    duration_ms_delta: int = 0
    preferred: Literal["scenario", "baseline", "tie"] = "tie"
    preferred_scenario_id: Optional[str] = None
    preferred_scenario_name: Optional[str] = None
    preference_reasons: list[str] = Field(default_factory=list)
    summary: str = ""


class EvalFreshReplayRankingEntry(BaseModel):
    rank: int = 0
    scenario_id: str
    scenario_name: str
    latest_fresh_run: EvalFreshRunSummary
    pairwise_wins: int = 0
    pairwise_losses: int = 0
    pairwise_ties: int = 0
    preference_reasons: list[str] = Field(default_factory=list)
    summary: str = ""


class EvalFreshReplayRanking(BaseModel):
    issue_id: str
    baseline_scenario_id: Optional[str] = None
    baseline_scenario_name: Optional[str] = None
    ranked_scenarios: list[EvalFreshReplayRankingEntry] = Field(default_factory=list)
    summary: str = ""


class EvalFreshReplayTrendEntry(BaseModel):
    scenario_id: str
    scenario_name: str
    current_rank: int = 0
    previous_rank: Optional[int] = None
    movement: Literal["up", "down", "same", "new"] = "new"
    latest_fresh_run: EvalFreshRunSummary
    previous_fresh_run: Optional[EvalFreshRunSummary] = None
    summary: str = ""


class EvalFreshReplayTrend(BaseModel):
    issue_id: str
    latest_batch_id: Optional[str] = None
    previous_batch_id: Optional[str] = None
    entries: list[EvalFreshReplayTrendEntry] = Field(default_factory=list)
    summary: str = ""


class EvalWorkspaceReport(BaseModel):
    workspace_id: str
    scenario_count: int = 0
    run_count: int = 0
    success_runs: int = 0
    failed_runs: int = 0
    total_estimated_cost: float = 0.0
    total_duration_ms: int = 0
    verification_success_rate: float = 0.0
    cost_summary: Optional[CostSummary] = None
    scenario_reports: list[EvalScenarioReport] = Field(default_factory=list)
    replay_batches: list[EvalReplayBatchRecord] = Field(default_factory=list)
    fresh_replay_rankings: list[EvalFreshReplayRanking] = Field(default_factory=list)
    fresh_replay_trends: list[EvalFreshReplayTrend] = Field(default_factory=list)
    guidance_variant_rollups: list[EvalVariantRollup] = Field(default_factory=list)
    ticket_context_variant_rollups: list[EvalVariantRollup] = Field(default_factory=list)
    generated_at: str = Field(default_factory=utc_now)


class EvalScenarioReplayRequest(BaseModel):
    runtime: RuntimeKind
    model: str
    scenario_ids: list[str] = Field(default_factory=list)
    instruction: Optional[str] = None
    runbook_id: Optional[str] = None
    planning: bool = False


class EvalScenarioReplayResult(BaseModel):
    workspace_id: str
    issue_id: str
    runtime: RuntimeKind
    model: str
    batch_id: Optional[str] = None
    scenario_ids: list[str] = Field(default_factory=list)
    queued_runs: list[RunRecord] = Field(default_factory=list)


class DismissImprovementRequest(BaseModel):
    reason: Optional[str] = None


IntegrationProvider = Literal["github", "slack", "linear", "jira"]
GitHubEventType = Literal["issues", "pull_request", "push"]
NotificationEvent = Literal[
    "run.completed", "run.failed", "run.cancelled",
    "verification.recorded", "fix.applied", "plan.approved", "plan.rejected",
]


class IntegrationConfig(BaseModel):
    config_id: str
    workspace_id: str
    provider: IntegrationProvider
    enabled: bool = True
    settings: dict = Field(default_factory=dict)
    created_at: str = Field(default_factory=utc_now)
    updated_at: str = Field(default_factory=utc_now)


class GitHubIssueImport(BaseModel):
    import_id: str
    workspace_id: str
    github_repo: str
    issue_number: int
    issue_id: str
    title: str
    body: Optional[str] = None
    labels: list[str] = Field(default_factory=list)
    state: str = "open"
    html_url: Optional[str] = None
    imported_at: str = Field(default_factory=utc_now)


class GitHubPRCreate(BaseModel):
    workspace_id: str
    run_id: str
    issue_id: str
    head_branch: str
    base_branch: str = "main"
    title: Optional[str] = None
    body: Optional[str] = None
    draft: bool = False


class GitHubPRResult(BaseModel):
    pr_id: str
    workspace_id: str
    run_id: str
    issue_id: str
    pr_number: int
    html_url: str
    state: str = "open"
    created_at: str = Field(default_factory=utc_now)


class SlackNotification(BaseModel):
    notification_id: str
    workspace_id: str
    event: NotificationEvent
    channel: Optional[str] = None
    webhook_url: Optional[str] = None
    message: str = ""
    status: Literal["pending", "sent", "failed"] = "pending"
    error: Optional[str] = None
    created_at: str = Field(default_factory=utc_now)
    sent_at: Optional[str] = None


class LinearIssueSync(BaseModel):
    sync_id: str
    workspace_id: str
    issue_id: str
    linear_id: Optional[str] = None
    linear_team_id: Optional[str] = None
    linear_status: Optional[str] = None
    title: str = ""
    description: Optional[str] = None
    labels: list[str] = Field(default_factory=list)
    priority: Optional[str] = None
    sync_direction: Literal["push", "pull"] = "push"
    synced_at: str = Field(default_factory=utc_now)


class JiraIssueSync(BaseModel):
    sync_id: str
    workspace_id: str
    issue_id: str
    jira_key: Optional[str] = None
    jira_project: Optional[str] = None
    jira_status: Optional[str] = None
    summary: str = ""
    description: Optional[str] = None
    labels: list[str] = Field(default_factory=list)
    priority: Optional[str] = None
    issue_type: str = "Bug"
    sync_direction: Literal["push", "pull"] = "push"
    synced_at: str = Field(default_factory=utc_now)


class IntegrationTestRequest(BaseModel):
    provider: IntegrationProvider
    settings: dict = Field(default_factory=dict)


class IntegrationTestResult(BaseModel):
    provider: IntegrationProvider
    ok: bool
    message: str = ""
    details: dict = Field(default_factory=dict)
    tested_at: str = Field(default_factory=utc_now)
