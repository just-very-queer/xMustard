use serde::{Deserialize, Serialize};

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct EvidenceRef {
    pub path: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub line: Option<i64>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub excerpt: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub normalized_path: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub path_exists: Option<bool>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub path_scope: Option<String>,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct IssueRecord {
    pub bug_id: String,
    pub title: String,
    pub severity: String,
    #[serde(default)]
    pub issue_status: String,
    #[serde(default)]
    pub source: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub source_doc: Option<String>,
    #[serde(default)]
    pub doc_status: String,
    #[serde(default)]
    pub code_status: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub summary: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub impact: Option<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub evidence: Vec<EvidenceRef>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub verification_evidence: Vec<EvidenceRef>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub tests_added: Vec<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub tests_passed: Vec<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub drift_flags: Vec<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub labels: Vec<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub notes: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub verified_at: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub verified_by: Option<String>,
    #[serde(default)]
    pub needs_followup: bool,
    #[serde(default)]
    pub review_ready_count: i64,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub review_ready_runs: Vec<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub fingerprint: Option<String>,
    #[serde(default)]
    pub updated_at: String,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct DiscoverySignal {
    pub signal_id: String,
    pub kind: String,
    pub severity: String,
    pub title: String,
    pub summary: String,
    pub file_path: String,
    pub line: i64,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub evidence: Vec<EvidenceRef>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub tags: Vec<String>,
    pub fingerprint: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub promoted_bug_id: Option<String>,
    #[serde(default)]
    pub created_at: String,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct TreeNode {
    pub path: String,
    pub name: String,
    pub node_type: String,
    #[serde(default)]
    pub has_children: bool,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub size_bytes: Option<i64>,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct WorkspaceRecord {
    pub workspace_id: String,
    pub name: String,
    pub root_path: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub latest_scan_at: Option<String>,
    #[serde(default)]
    pub created_at: String,
    #[serde(default)]
    pub updated_at: String,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct WorktreeStatus {
    #[serde(default)]
    pub available: bool,
    #[serde(default)]
    pub is_git_repo: bool,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub branch: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub head_sha: Option<String>,
    #[serde(default)]
    pub dirty_files: i64,
    #[serde(default)]
    pub staged_files: i64,
    #[serde(default)]
    pub untracked_files: i64,
    #[serde(default)]
    pub ahead: i64,
    #[serde(default)]
    pub behind: i64,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub dirty_paths: Vec<String>,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct SavedIssueView {
    pub view_id: String,
    pub workspace_id: String,
    pub name: String,
    #[serde(default)]
    pub query: String,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub severities: Vec<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub statuses: Vec<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub sources: Vec<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub labels: Vec<String>,
    #[serde(default)]
    pub drift_only: bool,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub needs_followup: Option<bool>,
    #[serde(default)]
    pub review_ready_only: bool,
    #[serde(default)]
    pub created_at: String,
    #[serde(default)]
    pub updated_at: String,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct SourceRecord {
    pub source_id: String,
    pub kind: String,
    pub label: String,
    pub path: String,
    pub record_count: i64,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub modified_at: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub notes: Option<String>,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct RepoMapDirectoryRecord {
    pub path: String,
    #[serde(default)]
    pub file_count: i64,
    #[serde(default)]
    pub source_file_count: i64,
    #[serde(default)]
    pub test_file_count: i64,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct RepoMapFileRecord {
    pub path: String,
    #[serde(default)]
    pub role: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub size_bytes: Option<i64>,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct RepoMapSummary {
    pub workspace_id: String,
    pub root_path: String,
    #[serde(default)]
    pub total_files: i64,
    #[serde(default)]
    pub source_files: i64,
    #[serde(default)]
    pub test_files: i64,
    #[serde(default)]
    pub top_extensions: serde_json::Value,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub top_directories: Vec<RepoMapDirectoryRecord>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub key_files: Vec<RepoMapFileRecord>,
    #[serde(default)]
    pub generated_at: String,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct RuntimeModel {
    pub runtime: String,
    pub id: String,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct RuntimeCapabilities {
    pub runtime: String,
    pub available: bool,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub binary_path: Option<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub models: Vec<RuntimeModel>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub notes: Option<String>,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct AppSettings {
    #[serde(default)]
    pub local_agent_type: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub codex_bin: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub opencode_bin: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub codex_args: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub codex_model: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub opencode_model: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub postgres_dsn: Option<String>,
    #[serde(default)]
    pub postgres_schema: String,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct PostgresSchemaPlan {
    #[serde(default)]
    pub configured: bool,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub dsn_redacted: Option<String>,
    #[serde(default)]
    pub schema_name: String,
    pub sql_path: String,
    #[serde(default)]
    pub statement_count: i64,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub table_names: Vec<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub semantic_table_names: Vec<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub ops_memory_table_names: Vec<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub search_document_tables: Vec<String>,
    #[serde(default)]
    pub generated_at: String,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct PostgresBootstrapRequest {
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub dsn: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub schema_name: Option<String>,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct PostgresBootstrapResult {
    #[serde(default)]
    pub applied: bool,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub dsn_redacted: Option<String>,
    #[serde(default)]
    pub schema_name: String,
    pub sql_path: String,
    #[serde(default)]
    pub statement_count: i64,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub table_names: Vec<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub semantic_table_names: Vec<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub search_document_tables: Vec<String>,
    pub message: String,
    #[serde(default)]
    pub generated_at: String,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct PostgresPathMaterializationRequest {
    pub path: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub dsn: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub schema_name: Option<String>,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct PostgresSemanticSearchMaterializationRequest {
    pub pattern: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub language: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub path_glob: Option<String>,
    #[serde(default)]
    pub limit: i64,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub dsn: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub schema_name: Option<String>,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct PostgresWorkspaceSemanticMaterializationRequest {
    #[serde(default)]
    pub strategy: String,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub paths: Vec<String>,
    #[serde(default)]
    pub limit: i64,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub dsn: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub schema_name: Option<String>,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct SemanticIndexPathSelection {
    pub path: String,
    #[serde(default)]
    pub role: String,
    #[serde(default)]
    pub score: i64,
    #[serde(default)]
    pub reason: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub sha256: Option<String>,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct SemanticIndexPlan {
    pub workspace_id: String,
    pub root_path: String,
    #[serde(default)]
    pub surface: String,
    #[serde(default)]
    pub strategy: String,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub requested_paths: Vec<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub selected_paths: Vec<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub selected_path_details: Vec<SemanticIndexPathSelection>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub head_sha: Option<String>,
    #[serde(default)]
    pub dirty_files: i64,
    #[serde(default)]
    pub worktree_dirty: bool,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub index_fingerprint: Option<String>,
    #[serde(default)]
    pub postgres_configured: bool,
    #[serde(default)]
    pub postgres_schema: String,
    #[serde(default)]
    pub tree_sitter_available: bool,
    #[serde(default)]
    pub ast_grep_available: bool,
    #[serde(default)]
    pub run_target_count: i64,
    #[serde(default)]
    pub verify_target_count: i64,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub retrieval_ledger: Vec<ContextRetrievalLedgerEntry>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub blockers: Vec<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub warnings: Vec<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub next_actions: Vec<String>,
    #[serde(default)]
    pub can_run: bool,
    #[serde(default)]
    pub generated_at: String,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct PostgresSemanticMaterializationResult {
    #[serde(default)]
    pub applied: bool,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub dsn_redacted: Option<String>,
    #[serde(default)]
    pub schema_name: String,
    pub workspace_id: String,
    #[serde(default)]
    pub source: String,
    pub target: String,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub materialized_paths: Vec<String>,
    #[serde(default)]
    pub file_rows: i64,
    #[serde(default)]
    pub symbol_rows: i64,
    #[serde(default)]
    pub summary_rows: i64,
    #[serde(default)]
    pub query_rows: i64,
    #[serde(default)]
    pub match_rows: i64,
    pub message: String,
    #[serde(default)]
    pub generated_at: String,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct PostgresWorkspaceSemanticMaterializationResult {
    #[serde(default)]
    pub applied: bool,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub dsn_redacted: Option<String>,
    #[serde(default)]
    pub schema_name: String,
    pub workspace_id: String,
    #[serde(default)]
    pub strategy: String,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub requested_paths: Vec<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub materialized_paths: Vec<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub skipped_paths: Vec<String>,
    #[serde(default)]
    pub file_rows: i64,
    #[serde(default)]
    pub symbol_rows: i64,
    #[serde(default)]
    pub summary_rows: i64,
    pub message: String,
    #[serde(default)]
    pub generated_at: String,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct SemanticIndexRunResult {
    pub workspace_id: String,
    #[serde(default)]
    pub surface: String,
    #[serde(default)]
    pub dry_run: bool,
    pub plan: SemanticIndexPlan,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub materialization: Option<PostgresWorkspaceSemanticMaterializationResult>,
    pub message: String,
    #[serde(default)]
    pub generated_at: String,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct SemanticIndexBaselineRecord {
    pub index_run_id: String,
    pub workspace_id: String,
    #[serde(default)]
    pub surface: String,
    #[serde(default)]
    pub strategy: String,
    pub index_fingerprint: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub head_sha: Option<String>,
    #[serde(default)]
    pub dirty_files: i64,
    #[serde(default)]
    pub worktree_dirty: bool,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub selected_paths: Vec<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub selected_path_details: Vec<SemanticIndexPathSelection>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub materialized_paths: Vec<String>,
    #[serde(default)]
    pub file_rows: i64,
    #[serde(default)]
    pub symbol_rows: i64,
    #[serde(default)]
    pub summary_rows: i64,
    #[serde(default)]
    pub postgres_schema: String,
    #[serde(default)]
    pub tree_sitter_available: bool,
    #[serde(default)]
    pub ast_grep_available: bool,
    #[serde(default)]
    pub created_at: String,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct SemanticIndexStatus {
    pub workspace_id: String,
    #[serde(default)]
    pub surface: String,
    #[serde(default)]
    pub status: String,
    #[serde(default)]
    pub postgres_configured: bool,
    #[serde(default)]
    pub postgres_schema: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub current_fingerprint: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub current_head_sha: Option<String>,
    #[serde(default)]
    pub current_dirty_files: i64,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub baseline: Option<SemanticIndexBaselineRecord>,
    #[serde(default)]
    pub fingerprint_match: bool,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub stale_reasons: Vec<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub warnings: Vec<String>,
    #[serde(default)]
    pub generated_at: String,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct RepoChangeRecord {
    pub path: String,
    #[serde(default)]
    pub status: String,
    #[serde(default)]
    pub scope: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub previous_path: Option<String>,
    #[serde(default)]
    pub staged: bool,
    #[serde(default)]
    pub unstaged: bool,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct RepoChangeSummary {
    pub workspace_id: String,
    #[serde(default)]
    pub base_ref: String,
    #[serde(default)]
    pub is_git_repo: bool,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub branch: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub head_sha: Option<String>,
    #[serde(default)]
    pub dirty_files: i64,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub changed_files: Vec<RepoChangeRecord>,
    #[serde(default)]
    pub generated_at: String,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct ChangedSymbolRecord {
    pub path: String,
    pub symbol: String,
    #[serde(default)]
    pub kind: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub line_start: Option<i64>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub line_end: Option<i64>,
    #[serde(default)]
    pub evidence_source: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub semantic_status: Option<String>,
    #[serde(default)]
    pub selection_reason: String,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub change_scopes: Vec<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub change_statuses: Vec<String>,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct ImpactPathRecord {
    pub path: String,
    pub reason: String,
    #[serde(default)]
    pub derivation_source: String,
    #[serde(default)]
    pub score: i64,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct ImpactReport {
    pub workspace_id: String,
    #[serde(default)]
    pub base_ref: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub semantic_status: Option<SemanticIndexStatus>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub changed_files: Vec<RepoChangeRecord>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub changed_symbols: Vec<ChangedSymbolRecord>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub likely_affected_files: Vec<ImpactPathRecord>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub likely_affected_tests: Vec<ImpactPathRecord>,
    #[serde(default)]
    pub derivation_summary: String,
    #[serde(default)]
    pub confidence: String,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub warnings: Vec<String>,
    #[serde(default)]
    pub generated_at: String,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct RepoTargetRecord {
    pub target_id: String,
    #[serde(default)]
    pub kind: String,
    pub label: String,
    pub command: String,
    #[serde(default)]
    pub source: String,
    pub source_path: String,
    #[serde(default)]
    pub confidence: i64,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub profile_id: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub working_dir: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub entry_path: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub reason: Option<String>,
    #[serde(default)]
    pub truth_source: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub truth_generated_at: Option<String>,
    #[serde(default)]
    pub scan_bound: bool,
    #[serde(default)]
    pub answer_coherence: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub scan_generated_at: Option<String>,
    #[serde(default)]
    pub overlay_applied: bool,
    #[serde(default)]
    pub freshness_status: String,
    #[serde(default)]
    pub freshness_reason: String,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub freshness_evidence_paths: Vec<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub owner_service_id: Option<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub related_target_ids: Vec<String>,
    #[serde(default)]
    pub ownership: ProjectTargetOwnership,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct ProjectInfoProvenance {
    pub source_kind: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub source_file: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub command: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub cwd: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub entry_path: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub declared_command: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub service_name: Option<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub config_files: Vec<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub config_hints: Vec<String>,
    pub evidence_type: String,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub evidence: Vec<EvidenceRef>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub profile_id: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub confidence: Option<i64>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub reason: Option<String>,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct ProjectManifestRecord {
    pub manifest_kind: String,
    pub path: String,
    pub verdict: String,
    pub provenance: ProjectInfoProvenance,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct ProjectRuntimeRecord {
    pub runtime: String,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub source_files: Vec<String>,
    pub verdict: String,
    pub provenance: ProjectInfoProvenance,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct ProjectObservedRuntimeRecord {
    pub runtime: String,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub source_files: Vec<String>,
    #[serde(default)]
    pub available: bool,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub binary_path: Option<String>,
    pub verdict: String,
    pub provenance: ProjectInfoProvenance,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct ProjectEntrypointRecord {
    #[serde(default)]
    pub kind: String,
    pub label: String,
    pub command: String,
    pub verdict: String,
    pub provenance: ProjectInfoProvenance,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct ProjectCommandRecord {
    #[serde(default)]
    pub target_id: String,
    #[serde(default)]
    pub kind: String,
    pub label: String,
    pub command: String,
    pub verdict: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub owner_service_id: Option<String>,
    pub ownership: ProjectTargetOwnership,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub related_target_ids: Vec<String>,
    pub provenance: ProjectInfoProvenance,
}

#[derive(Debug, Clone, Default, Serialize, Deserialize, PartialEq)]
pub struct ProjectTargetOwnership {
    #[serde(default)]
    pub status: String,
    #[serde(default)]
    pub match_basis: String,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub service_ids: Vec<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub scope_kind: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub scope_key: Option<String>,
    #[serde(default)]
    pub reason: String,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct ProjectServiceRecord {
    pub name: String,
    pub command: String,
    pub verdict: String,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub depends_on: Vec<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub profiles: Vec<String>,
    pub provenance: ProjectInfoProvenance,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct ProjectServiceIdentityRecord {
    pub service_id: String,
    pub name: String,
    pub identity_kind: String,
    pub verdict: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub working_dir: Option<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub manifest_paths: Vec<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub entry_paths: Vec<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub run_target_ids: Vec<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub verify_target_ids: Vec<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub run_commands: Vec<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub verify_commands: Vec<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub depends_on: Vec<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub profiles: Vec<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub group_ids: Vec<String>,
    pub provenance: ProjectInfoProvenance,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct ProjectServiceGroupRecord {
    pub group_id: String,
    pub name: String,
    pub group_type: String,
    pub verdict: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub root_dir: Option<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub manifest_paths: Vec<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub member_service_ids: Vec<String>,
    pub provenance: ProjectInfoProvenance,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct ProjectServiceRelationshipRecord {
    pub relationship_id: String,
    pub relationship_type: String,
    pub source_service_id: String,
    pub target_service_id: String,
    pub verdict: String,
    pub provenance: ProjectInfoProvenance,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct ProjectInfoStaticTruth {
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub manifests: Vec<ProjectManifestRecord>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub runtimes: Vec<ProjectRuntimeRecord>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub entrypoints: Vec<ProjectEntrypointRecord>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub run_targets: Vec<ProjectCommandRecord>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub verify_targets: Vec<ProjectCommandRecord>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub services: Vec<ProjectServiceRecord>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub service_identities: Vec<ProjectServiceIdentityRecord>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub service_groups: Vec<ProjectServiceGroupRecord>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub service_relationships: Vec<ProjectServiceRelationshipRecord>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub warnings: Vec<String>,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct ProjectInfoRuntimeTruth {
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub runtimes: Vec<ProjectObservedRuntimeRecord>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub warnings: Vec<String>,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct ProjectInfoRecord {
    pub workspace_id: String,
    pub root_path: String,
    #[serde(default)]
    pub source_mode: String,
    pub static_truth: ProjectInfoStaticTruth,
    pub runtime_truth: ProjectInfoRuntimeTruth,
    #[serde(default)]
    pub generated_at: String,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct PathSymbolsResult {
    pub workspace_id: String,
    pub path: String,
    #[serde(default)]
    pub symbol_source: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub parser_language: Option<String>,
    #[serde(default)]
    pub evidence_source: String,
    #[serde(default)]
    pub selection_reason: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub semantic_status: Option<SemanticIndexStatus>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub symbols: Vec<RepoMapSymbolRecord>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub file_summary_row: Option<FileSymbolSummaryMaterializationRecord>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub symbol_rows: Vec<SymbolMaterializationRecord>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub warnings: Vec<String>,
    #[serde(default)]
    pub generated_at: String,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct SemanticPatternMatchRecord {
    pub path: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub language: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub line_start: Option<i64>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub line_end: Option<i64>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub column_start: Option<i64>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub column_end: Option<i64>,
    pub matched_text: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub context_lines: Option<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub meta_variables: Vec<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub reason: Option<String>,
    #[serde(default)]
    pub score: i64,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct FileSymbolSummaryMaterializationRecord {
    pub workspace_id: String,
    pub path: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub language: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub parser_language: Option<String>,
    #[serde(default)]
    pub symbol_source: String,
    #[serde(default)]
    pub symbol_count: i64,
    #[serde(default)]
    pub summary_json: serde_json::Value,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct SymbolMaterializationRecord {
    pub workspace_id: String,
    pub path: String,
    pub symbol: String,
    #[serde(default)]
    pub kind: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub language: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub line_start: Option<i64>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub line_end: Option<i64>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub enclosing_scope: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub signature_text: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub symbol_text: Option<String>,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct SemanticQueryMaterializationRecord {
    pub query_ref: String,
    pub workspace_id: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub issue_id: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub run_id: Option<String>,
    #[serde(default)]
    pub source: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub reason: Option<String>,
    pub pattern: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub language: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub path_glob: Option<String>,
    #[serde(default)]
    pub engine: String,
    #[serde(default)]
    pub match_count: i64,
    #[serde(default)]
    pub truncated: bool,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub error: Option<String>,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct SemanticMatchMaterializationRecord {
    pub query_ref: String,
    pub workspace_id: String,
    pub path: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub language: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub line_start: Option<i64>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub line_end: Option<i64>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub column_start: Option<i64>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub column_end: Option<i64>,
    pub matched_text: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub context_lines: Option<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub meta_variables: Vec<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub reason: Option<String>,
    #[serde(default)]
    pub score: i64,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct SemanticPatternQueryResult {
    pub workspace_id: String,
    pub pattern: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub language: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub path_glob: Option<String>,
    #[serde(default)]
    pub engine: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub binary_path: Option<String>,
    #[serde(default)]
    pub match_count: i64,
    #[serde(default)]
    pub truncated: bool,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub matches: Vec<SemanticPatternMatchRecord>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub query_row: Option<SemanticQueryMaterializationRecord>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub match_rows: Vec<SemanticMatchMaterializationRecord>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub error: Option<String>,
    #[serde(default)]
    pub generated_at: String,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct ActivityActor {
    #[serde(default)]
    pub kind: String,
    pub name: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub runtime: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub model: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub key: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub label: Option<String>,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct ActivityRecord {
    pub activity_id: String,
    pub workspace_id: String,
    pub entity_type: String,
    pub entity_id: String,
    pub action: String,
    pub summary: String,
    pub actor: ActivityActor,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub issue_id: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub run_id: Option<String>,
    #[serde(default)]
    pub details: serde_json::Value,
    #[serde(default)]
    pub created_at: String,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct ActivityRollupItem {
    pub key: String,
    pub label: String,
    pub count: i64,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub actor_key: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub action: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub entity_type: Option<String>,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct ActivityOverview {
    #[serde(default)]
    pub total_events: i64,
    #[serde(default)]
    pub unique_actors: i64,
    #[serde(default)]
    pub unique_actions: i64,
    #[serde(default)]
    pub operator_events: i64,
    #[serde(default)]
    pub agent_events: i64,
    #[serde(default)]
    pub system_events: i64,
    #[serde(default)]
    pub issues_touched: i64,
    #[serde(default)]
    pub fixes_touched: i64,
    #[serde(default)]
    pub runs_touched: i64,
    #[serde(default)]
    pub views_touched: i64,
    #[serde(default)]
    pub counts_by_entity_type: serde_json::Value,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub top_actors: Vec<ActivityRollupItem>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub top_actions: Vec<ActivityRollupItem>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub top_entities: Vec<ActivityRollupItem>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub most_recent_at: Option<String>,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct LocalAgentCapabilities {
    pub selected_runtime: String,
    pub supports_live_subscribe: bool,
    #[serde(default)]
    pub supports_terminal: bool,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub runtimes: Vec<RuntimeCapabilities>,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct RuntimeProbeResult {
    pub runtime: String,
    pub model: String,
    pub ok: bool,
    pub available: bool,
    #[serde(default)]
    pub checked_at: String,
    #[serde(default)]
    pub duration_ms: i64,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub exit_code: Option<i64>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub binary_path: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub command_preview: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub output_excerpt: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub error: Option<String>,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct RunRecord {
    pub run_id: String,
    pub workspace_id: String,
    pub issue_id: String,
    pub runtime: String,
    pub model: String,
    pub status: String,
    pub title: String,
    pub prompt: String,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub command: Vec<String>,
    pub command_preview: String,
    pub log_path: String,
    pub output_path: String,
    #[serde(default)]
    pub created_at: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub started_at: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub completed_at: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub exit_code: Option<i64>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub pid: Option<i64>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub error: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub runbook_id: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub eval_scenario_id: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub eval_replay_batch_id: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub worktree: Option<WorktreeStatus>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub guidance_paths: Vec<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub summary: Option<serde_json::Value>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub plan: Option<RunPlan>,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct FixRecord {
    pub fix_id: String,
    pub workspace_id: String,
    pub issue_id: String,
    #[serde(default)]
    pub status: String,
    pub summary: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub how: Option<String>,
    pub actor: ActivityActor,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub run_id: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub session_id: Option<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub changed_files: Vec<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub tests_run: Vec<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub evidence: Vec<EvidenceRef>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub worktree: Option<WorktreeStatus>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub notes: Option<String>,
    #[serde(default)]
    pub updated_at: String,
    #[serde(default)]
    pub recorded_at: String,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct RunReviewRecord {
    pub review_id: String,
    pub workspace_id: String,
    pub run_id: String,
    pub issue_id: String,
    pub disposition: String,
    pub actor: ActivityActor,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub notes: Option<String>,
    #[serde(default)]
    pub created_at: String,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct RunbookRecord {
    pub runbook_id: String,
    pub workspace_id: String,
    pub name: String,
    pub description: String,
    #[serde(default)]
    pub scope: String,
    pub template: String,
    #[serde(default)]
    pub built_in: bool,
    #[serde(default)]
    pub created_at: String,
    #[serde(default)]
    pub updated_at: String,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct VerificationProfileRecord {
    pub profile_id: String,
    pub workspace_id: String,
    pub name: String,
    #[serde(default)]
    pub description: String,
    pub test_command: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub coverage_command: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub coverage_report_path: Option<String>,
    #[serde(default)]
    pub coverage_format: String,
    #[serde(default)]
    pub max_runtime_seconds: i64,
    #[serde(default)]
    pub retry_count: i64,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub source_paths: Vec<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub checklist_items: Vec<String>,
    #[serde(default)]
    pub built_in: bool,
    #[serde(default)]
    pub created_at: String,
    #[serde(default)]
    pub updated_at: String,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct VerificationCommandResult {
    pub command: String,
    pub cwd: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub exit_code: Option<i64>,
    #[serde(default)]
    pub success: bool,
    #[serde(default)]
    pub timed_out: bool,
    #[serde(default)]
    pub duration_ms: i64,
    #[serde(default)]
    pub stdout_excerpt: String,
    #[serde(default)]
    pub stderr_excerpt: String,
    #[serde(default)]
    pub created_at: String,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct VerificationChecklistResult {
    pub item_id: String,
    pub title: String,
    #[serde(default)]
    pub kind: String,
    #[serde(default)]
    pub passed: bool,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub details: Option<String>,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct VerificationProfileExecutionResult {
    #[serde(default)]
    pub execution_id: String,
    pub profile_id: String,
    pub workspace_id: String,
    #[serde(default)]
    pub profile_name: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub issue_id: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub run_id: Option<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub attempts: Vec<VerificationCommandResult>,
    #[serde(default)]
    pub attempt_count: i64,
    #[serde(default)]
    pub success: bool,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub coverage_command_result: Option<VerificationCommandResult>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub coverage_result: Option<CoverageResult>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub checklist_results: Vec<VerificationChecklistResult>,
    #[serde(default)]
    pub confidence: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub coverage_report_path: Option<String>,
    #[serde(default)]
    pub created_at: String,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct VerificationProfileReport {
    pub profile_id: String,
    pub workspace_id: String,
    pub profile_name: String,
    #[serde(default)]
    pub built_in: bool,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub issue_id: Option<String>,
    #[serde(default)]
    pub total_runs: i64,
    #[serde(default)]
    pub success_runs: i64,
    #[serde(default)]
    pub failed_runs: i64,
    #[serde(default)]
    pub success_rate: f64,
    #[serde(default)]
    pub confidence_counts: serde_json::Value,
    #[serde(default)]
    pub avg_attempt_count: f64,
    #[serde(default)]
    pub checklist_pass_rate: f64,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub last_run_at: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub last_issue_id: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub last_run_id: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub last_confidence: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub last_success: Option<bool>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub runtime_breakdown: Vec<VerificationProfileDimensionSummary>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub model_breakdown: Vec<VerificationProfileDimensionSummary>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub branch_breakdown: Vec<VerificationProfileDimensionSummary>,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct VerificationProfileDimensionSummary {
    pub key: String,
    pub label: String,
    #[serde(default)]
    pub total_runs: i64,
    #[serde(default)]
    pub success_runs: i64,
    #[serde(default)]
    pub failed_runs: i64,
    #[serde(default)]
    pub success_rate: f64,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub last_run_at: Option<String>,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct TicketContextRecord {
    pub context_id: String,
    pub workspace_id: String,
    pub issue_id: String,
    #[serde(default)]
    pub provider: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub external_id: Option<String>,
    pub title: String,
    #[serde(default)]
    pub summary: String,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub acceptance_criteria: Vec<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub links: Vec<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub labels: Vec<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub status: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub source_excerpt: Option<String>,
    #[serde(default)]
    pub created_at: String,
    #[serde(default)]
    pub updated_at: String,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct ThreatModelRecord {
    pub threat_model_id: String,
    pub workspace_id: String,
    pub issue_id: String,
    pub title: String,
    #[serde(default)]
    pub methodology: String,
    #[serde(default)]
    pub summary: String,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub assets: Vec<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub entry_points: Vec<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub trust_boundaries: Vec<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub abuse_cases: Vec<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub mitigations: Vec<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub references: Vec<String>,
    #[serde(default)]
    pub status: String,
    #[serde(default)]
    pub created_at: String,
    #[serde(default)]
    pub updated_at: String,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct IssueContextReplayRecord {
    pub replay_id: String,
    pub workspace_id: String,
    pub issue_id: String,
    pub label: String,
    pub prompt: String,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub tree_focus: Vec<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub guidance_paths: Vec<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub verification_profile_ids: Vec<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub ticket_context_ids: Vec<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub browser_dump_ids: Vec<String>,
    #[serde(default)]
    pub created_at: String,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct IssueContextReplayComparison {
    pub replay: IssueContextReplayRecord,
    pub current_prompt: String,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub current_tree_focus: Vec<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub current_guidance_paths: Vec<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub current_verification_profile_ids: Vec<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub current_ticket_context_ids: Vec<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub current_browser_dump_ids: Vec<String>,
    #[serde(default)]
    pub prompt_changed: bool,
    #[serde(default)]
    pub changed: bool,
    #[serde(default)]
    pub saved_prompt_length: i64,
    #[serde(default)]
    pub current_prompt_length: i64,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub added_tree_focus: Vec<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub removed_tree_focus: Vec<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub added_guidance_paths: Vec<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub removed_guidance_paths: Vec<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub added_verification_profile_ids: Vec<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub removed_verification_profile_ids: Vec<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub added_ticket_context_ids: Vec<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub removed_ticket_context_ids: Vec<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub added_browser_dump_ids: Vec<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub removed_browser_dump_ids: Vec<String>,
    pub summary: String,
    #[serde(default)]
    pub compared_at: String,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct BrowserDumpRecord {
    pub dump_id: String,
    pub workspace_id: String,
    pub issue_id: String,
    #[serde(default)]
    pub source: String,
    pub label: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub page_url: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub page_title: Option<String>,
    #[serde(default)]
    pub summary: String,
    #[serde(default)]
    pub dom_snapshot: String,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub console_messages: Vec<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub network_requests: Vec<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub screenshot_path: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub notes: Option<String>,
    #[serde(default)]
    pub created_at: String,
    #[serde(default)]
    pub updated_at: String,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct VulnerabilityFindingRecord {
    pub finding_id: String,
    pub workspace_id: String,
    pub issue_id: String,
    pub scanner: String,
    #[serde(default)]
    pub source: String,
    #[serde(default)]
    pub severity: String,
    #[serde(default)]
    pub status: String,
    pub title: String,
    #[serde(default)]
    pub summary: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub rule_id: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub location_path: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub location_line: Option<i64>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub cwe_ids: Vec<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub cve_ids: Vec<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub references: Vec<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub evidence: Vec<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub threat_model_ids: Vec<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub raw_payload: Option<String>,
    #[serde(default)]
    pub created_at: String,
    #[serde(default)]
    pub updated_at: String,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct ReviewQueueItem {
    pub run: RunRecord,
    pub issue: IssueRecord,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub draft: Option<FixDraftSuggestion>,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct VerificationRecord {
    pub verification_id: String,
    pub workspace_id: String,
    pub issue_id: String,
    pub run_id: String,
    pub runtime: String,
    pub model: String,
    #[serde(default)]
    pub code_checked: String,
    #[serde(default)]
    pub fixed: String,
    #[serde(default)]
    pub confidence: String,
    #[serde(default)]
    pub summary: String,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub evidence: Vec<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub tests: Vec<String>,
    pub actor: ActivityActor,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub raw_excerpt: Option<String>,
    #[serde(default)]
    pub created_at: String,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct VerificationSummary {
    pub workspace_id: String,
    pub issue_id: String,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub records: Vec<VerificationRecord>,
    #[serde(default)]
    pub checked_yes: i64,
    #[serde(default)]
    pub checked_no: i64,
    #[serde(default)]
    pub checked_unknown: i64,
    #[serde(default)]
    pub fixed_yes: i64,
    #[serde(default)]
    pub fixed_no: i64,
    #[serde(default)]
    pub fixed_unknown: i64,
    #[serde(default)]
    pub consensus_code_checked: String,
    #[serde(default)]
    pub consensus_fixed: String,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct PlanStep {
    pub step_id: String,
    pub description: String,
    #[serde(default)]
    pub estimated_impact: String,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub files_affected: Vec<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub risks: Vec<String>,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct PlanFileAttachment {
    pub path: String,
    #[serde(default)]
    pub source: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub note: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub exists: Option<bool>,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct PlanRevision {
    pub revision_id: String,
    #[serde(default)]
    pub version: i64,
    #[serde(default)]
    pub phase: String,
    #[serde(default)]
    pub summary: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub feedback: Option<String>,
    #[serde(default)]
    pub ownership_mode: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub owner_label: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub branch: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub head_sha: Option<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub dirty_paths: Vec<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub attached_files: Vec<PlanFileAttachment>,
    #[serde(default)]
    pub created_at: String,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct RunPlan {
    pub plan_id: String,
    pub run_id: String,
    #[serde(default)]
    pub phase: String,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub steps: Vec<PlanStep>,
    #[serde(default)]
    pub summary: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub reasoning: Option<String>,
    #[serde(default)]
    pub version: i64,
    #[serde(default)]
    pub ownership_mode: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub owner_label: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub branch: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub head_sha: Option<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub dirty_paths: Vec<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub attached_files: Vec<PlanFileAttachment>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub revisions: Vec<PlanRevision>,
    #[serde(default)]
    pub created_at: String,
    #[serde(default)]
    pub updated_at: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub approved_at: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub approver: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub feedback: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub modified_summary: Option<String>,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct WorkspaceSnapshot {
    #[serde(default)]
    pub scanner_version: i64,
    pub workspace: WorkspaceRecord,
    pub summary: serde_json::Value,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub issues: Vec<IssueRecord>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub signals: Vec<DiscoverySignal>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub sources: Vec<SourceRecord>,
    #[serde(default)]
    pub drift_summary: serde_json::Value,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub runtimes: Vec<RuntimeCapabilities>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub run_targets: Vec<RepoTargetRecord>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub verify_targets: Vec<RepoTargetRecord>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub project_info: Option<ProjectInfoRecord>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub latest_ledger: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub latest_verdicts: Option<String>,
    #[serde(default)]
    pub generated_at: String,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct WorkspaceLoadRequest {
    pub root_path: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub name: Option<String>,
    #[serde(default)]
    pub auto_scan: bool,
    #[serde(default)]
    pub prefer_cached_snapshot: bool,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct RunRequest {
    pub runtime: String,
    pub model: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub instruction: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub runbook_id: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub eval_scenario_id: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub eval_replay_batch_id: Option<String>,
    #[serde(default)]
    pub planning: bool,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct AgentQueryRequest {
    pub runtime: String,
    pub model: String,
    pub prompt: String,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct RuntimeProbeRequest {
    pub runtime: String,
    pub model: String,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct IssueUpdateRequest {
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub severity: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub issue_status: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub doc_status: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub code_status: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub labels: Option<Vec<String>>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub notes: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub needs_followup: Option<bool>,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct IssueCreateRequest {
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub bug_id: Option<String>,
    pub title: String,
    pub severity: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub summary: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub impact: Option<String>,
    #[serde(default)]
    pub issue_status: String,
    #[serde(default)]
    pub doc_status: String,
    #[serde(default)]
    pub code_status: String,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub labels: Vec<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub notes: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub source_doc: Option<String>,
    #[serde(default)]
    pub needs_followup: bool,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct FixRecordRequest {
    #[serde(default)]
    pub status: String,
    pub summary: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub how: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub run_id: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub runtime: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub model: Option<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub changed_files: Vec<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub tests_run: Vec<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub notes: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub issue_status: Option<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub evidence: Vec<EvidenceRef>,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct FixUpdateRequest {
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub status: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub notes: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub issue_status: Option<String>,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct FixDraftSuggestion {
    pub workspace_id: String,
    pub issue_id: String,
    pub run_id: String,
    pub summary: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub how: Option<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub changed_files: Vec<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub tests_run: Vec<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub suggested_issue_status: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub source_excerpt: Option<String>,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct RunReviewRequest {
    pub disposition: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub notes: Option<String>,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct RunAcceptRequest {
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub issue_status: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub notes: Option<String>,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct RunbookUpsertRequest {
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub runbook_id: Option<String>,
    pub name: String,
    #[serde(default)]
    pub description: String,
    #[serde(default)]
    pub scope: String,
    pub template: String,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct GuidanceStarterRequest {
    pub template_id: String,
    #[serde(default)]
    pub overwrite: bool,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct GuidanceStarterResult {
    pub workspace_id: String,
    pub template_id: String,
    pub path: String,
    #[serde(default)]
    pub created: bool,
    #[serde(default)]
    pub overwritten: bool,
    #[serde(default)]
    pub generated_at: String,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct VerificationProfileUpsertRequest {
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub profile_id: Option<String>,
    pub name: String,
    #[serde(default)]
    pub description: String,
    pub test_command: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub coverage_command: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub coverage_report_path: Option<String>,
    #[serde(default)]
    pub coverage_format: String,
    #[serde(default)]
    pub max_runtime_seconds: i64,
    #[serde(default)]
    pub retry_count: i64,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub source_paths: Vec<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub checklist_items: Vec<String>,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct TicketContextUpsertRequest {
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub context_id: Option<String>,
    #[serde(default)]
    pub provider: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub external_id: Option<String>,
    pub title: String,
    #[serde(default)]
    pub summary: String,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub acceptance_criteria: Vec<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub links: Vec<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub labels: Vec<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub status: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub source_excerpt: Option<String>,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct ThreatModelUpsertRequest {
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub threat_model_id: Option<String>,
    pub title: String,
    #[serde(default)]
    pub methodology: String,
    #[serde(default)]
    pub summary: String,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub assets: Vec<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub entry_points: Vec<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub trust_boundaries: Vec<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub abuse_cases: Vec<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub mitigations: Vec<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub references: Vec<String>,
    #[serde(default)]
    pub status: String,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct IssueContextReplayRequest {
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub label: Option<String>,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct BrowserDumpUpsertRequest {
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub dump_id: Option<String>,
    #[serde(default)]
    pub source: String,
    pub label: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub page_url: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub page_title: Option<String>,
    #[serde(default)]
    pub summary: String,
    #[serde(default)]
    pub dom_snapshot: String,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub console_messages: Vec<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub network_requests: Vec<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub screenshot_path: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub notes: Option<String>,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct VulnerabilityFindingUpsertRequest {
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub finding_id: Option<String>,
    pub scanner: String,
    #[serde(default)]
    pub source: String,
    #[serde(default)]
    pub severity: String,
    #[serde(default)]
    pub status: String,
    pub title: String,
    #[serde(default)]
    pub summary: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub rule_id: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub location_path: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub location_line: Option<i64>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub cwe_ids: Vec<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub cve_ids: Vec<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub references: Vec<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub evidence: Vec<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub threat_model_ids: Vec<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub raw_payload: Option<String>,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct VulnerabilityImportRequest {
    pub source: String,
    pub payload: String,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct VulnerabilityImportBatchRecord {
    pub batch_id: String,
    pub workspace_id: String,
    pub issue_id: String,
    pub source: String,
    pub scanner: String,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub finding_ids: Vec<String>,
    #[serde(default)]
    pub total_findings: i64,
    #[serde(default)]
    pub summary_counts: serde_json::Value,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub payload_sha256: Option<String>,
    #[serde(default)]
    pub created_at: String,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct VulnerabilityFindingReportItem {
    pub finding_id: String,
    pub scanner: String,
    pub source: String,
    pub severity: String,
    pub status: String,
    pub title: String,
    #[serde(default)]
    pub summary: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub rule_id: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub location_path: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub location_line: Option<i64>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub cwe_ids: Vec<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub cve_ids: Vec<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub references: Vec<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub evidence: Vec<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub threat_model_ids: Vec<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub linked_threat_model_titles: Vec<String>,
    #[serde(default)]
    pub created_at: String,
    #[serde(default)]
    pub updated_at: String,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct VulnerabilityFindingReport {
    pub workspace_id: String,
    pub issue_id: String,
    #[serde(default)]
    pub total_findings: i64,
    #[serde(default)]
    pub by_severity: serde_json::Value,
    #[serde(default)]
    pub by_status: serde_json::Value,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub linked_threat_models: Vec<ThreatModelRecord>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub findings: Vec<VulnerabilityFindingReportItem>,
    #[serde(default)]
    pub generated_at: String,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct WorkspaceVulnerabilityIssueRollup {
    pub issue_id: String,
    pub title: String,
    pub issue_severity: String,
    #[serde(default)]
    pub highest_vulnerability_severity: String,
    #[serde(default)]
    pub total_findings: i64,
    #[serde(default)]
    pub open_findings: i64,
    #[serde(default)]
    pub linked_threat_models: i64,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub scanners: Vec<String>,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct WorkspaceVulnerabilityReport {
    pub workspace_id: String,
    #[serde(default)]
    pub total_findings: i64,
    #[serde(default)]
    pub by_severity: serde_json::Value,
    #[serde(default)]
    pub by_status: serde_json::Value,
    #[serde(default)]
    pub by_source: serde_json::Value,
    #[serde(default)]
    pub linked_threat_models_total: i64,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub linked_threat_models: Vec<ThreatModelRecord>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub issue_rollups: Vec<WorkspaceVulnerabilityIssueRollup>,
    #[serde(default)]
    pub generated_at: String,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct WorkspaceSecurityReviewBundle {
    pub workspace_id: String,
    #[serde(default)]
    pub total_findings: i64,
    #[serde(default)]
    pub open_findings: i64,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub linked_threat_models: Vec<ThreatModelRecord>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub top_findings: Vec<VulnerabilityFindingReportItem>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub issue_rollups: Vec<WorkspaceVulnerabilityIssueRollup>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub recent_activity: Vec<ActivityRecord>,
    #[serde(default)]
    pub generated_at: String,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct VerifyIssueRequest {
    #[serde(default)]
    pub runtime: String,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub models: Vec<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub runbook_id: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub instruction: Option<String>,
    #[serde(default)]
    pub timeout_seconds: f64,
    #[serde(default)]
    pub poll_interval: f64,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct VerificationProfileRunRequest {
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub run_id: Option<String>,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct EvalScenarioUpsertRequest {
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub scenario_id: Option<String>,
    pub name: String,
    pub issue_id: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub description: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub baseline_replay_id: Option<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub guidance_paths: Vec<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub ticket_context_ids: Vec<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub verification_profile_ids: Vec<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub run_ids: Vec<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub browser_dump_ids: Vec<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub notes: Option<String>,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct SavedIssueViewRequest {
    pub name: String,
    #[serde(default)]
    pub query: String,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub severities: Vec<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub statuses: Vec<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub sources: Vec<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub labels: Vec<String>,
    #[serde(default)]
    pub drift_only: bool,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub needs_followup: Option<bool>,
    #[serde(default)]
    pub review_ready_only: bool,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct PromoteSignalRequest {
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub title: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub severity: Option<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub labels: Vec<String>,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct ExportBundle {
    pub workspace: WorkspaceRecord,
    pub snapshot: WorkspaceSnapshot,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub repo_map: Option<RepoMapSummary>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub runs: Vec<RunRecord>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub fixes: Vec<FixRecord>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub run_reviews: Vec<RunReviewRecord>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub runbooks: Vec<RunbookRecord>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub verification_profiles: Vec<VerificationProfileRecord>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub verification_profile_history: Vec<VerificationProfileExecutionResult>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub ticket_contexts: Vec<TicketContextRecord>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub threat_models: Vec<ThreatModelRecord>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub context_replays: Vec<IssueContextReplayRecord>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub browser_dumps: Vec<BrowserDumpRecord>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub vulnerability_findings: Vec<VulnerabilityFindingRecord>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub eval_scenarios: Vec<EvalScenarioRecord>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub verifications: Vec<VerificationRecord>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub activity: Vec<ActivityRecord>,
    #[serde(default)]
    pub exported_at: String,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct RepoMapSymbolRecord {
    pub path: String,
    pub symbol: String,
    #[serde(default)]
    pub kind: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub line_start: Option<i64>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub line_end: Option<i64>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub enclosing_scope: Option<String>,
    #[serde(default)]
    pub evidence_source: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub reason: Option<String>,
    #[serde(default)]
    pub score: i64,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct RelatedContextRecord {
    pub artifact_type: String,
    pub artifact_id: String,
    pub title: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub path: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub reason: Option<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub matched_terms: Vec<String>,
    #[serde(default)]
    pub score: i64,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct ContextRetrievalLedgerEntry {
    pub entry_id: String,
    pub source_type: String,
    pub source_id: String,
    pub title: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub path: Option<String>,
    pub reason: String,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub matched_terms: Vec<String>,
    #[serde(default)]
    pub score: i64,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct DynamicContextBundle {
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub symbol_context: Vec<RepoMapSymbolRecord>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub semantic_matches: Vec<SemanticPatternMatchRecord>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub semantic_queries: Vec<SemanticQueryMaterializationRecord>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub semantic_match_rows: Vec<SemanticMatchMaterializationRecord>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub related_context: Vec<RelatedContextRecord>,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct RepoMCPServerRecord {
    pub name: String,
    #[serde(default)]
    pub description: String,
    #[serde(default)]
    pub usage: String,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct RepoPathInstructionRecord {
    pub instruction_id: String,
    pub path: String,
    pub instructions: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub title: Option<String>,
    pub source_path: String,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct RepoPathInstructionMatch {
    pub instruction_id: String,
    pub path: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub title: Option<String>,
    pub instructions: String,
    pub source_path: String,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub matched_paths: Vec<String>,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct RepoConfigRecord {
    pub workspace_id: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub source_path: Option<String>,
    #[serde(default)]
    pub description: String,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub path_filters: Vec<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub path_instructions: Vec<RepoPathInstructionRecord>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub code_guidelines: Vec<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub mcp_servers: Vec<RepoMCPServerRecord>,
    #[serde(default)]
    pub loaded_at: String,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct RepoConfigHealth {
    pub workspace_id: String,
    #[serde(default)]
    pub status: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub source_path: Option<String>,
    pub summary: String,
    #[serde(default)]
    pub path_instruction_count: i64,
    #[serde(default)]
    pub path_filter_count: i64,
    #[serde(default)]
    pub code_guideline_count: i64,
    #[serde(default)]
    pub mcp_server_count: i64,
    #[serde(default)]
    pub loaded_at: String,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct IssueContextPacket {
    pub issue: IssueRecord,
    pub workspace: WorkspaceRecord,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub semantic_status: Option<SemanticIndexStatus>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub tree_focus: Vec<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub related_paths: Vec<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub evidence_bundle: Vec<EvidenceRef>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub recent_fixes: Vec<FixRecord>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub recent_activity: Vec<ActivityRecord>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub guidance: Vec<RepoGuidanceRecord>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub runbook: Vec<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub available_runbooks: Vec<RunbookRecord>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub available_verification_profiles: Vec<VerificationProfileRecord>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub ticket_contexts: Vec<TicketContextRecord>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub threat_models: Vec<ThreatModelRecord>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub browser_dumps: Vec<BrowserDumpRecord>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub vulnerability_findings: Vec<VulnerabilityFindingRecord>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub repo_map: Option<RepoMapSummary>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub dynamic_context: Option<DynamicContextBundle>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub retrieval_ledger: Vec<ContextRetrievalLedgerEntry>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub repo_config: Option<RepoConfigRecord>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub matched_path_instructions: Vec<RepoPathInstructionMatch>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub worktree: Option<WorktreeStatus>,
    pub prompt: String,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct RepoGuidanceRecord {
    pub guidance_id: String,
    pub workspace_id: String,
    pub kind: String,
    pub title: String,
    pub path: String,
    #[serde(default)]
    pub always_on: bool,
    #[serde(default)]
    pub priority: i64,
    #[serde(default)]
    pub summary: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub excerpt: Option<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub trigger_keywords: Vec<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub updated_at: Option<String>,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct GuidanceStarterRecord {
    pub template_id: String,
    pub title: String,
    pub path: String,
    pub description: String,
    #[serde(default)]
    pub recommended: bool,
    #[serde(default)]
    pub exists: bool,
    #[serde(default)]
    pub stale: bool,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct RepoGuidanceHealth {
    pub workspace_id: String,
    pub status: String,
    pub summary: String,
    #[serde(default)]
    pub guidance_count: i64,
    #[serde(default)]
    pub always_on_count: i64,
    #[serde(default)]
    pub instruction_count: i64,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub present_files: Vec<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub missing_files: Vec<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub stale_files: Vec<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub recommended_files: Vec<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub starters: Vec<GuidanceStarterRecord>,
    #[serde(default)]
    pub generated_at: String,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct RunSessionInsight {
    pub workspace_id: String,
    pub run_id: String,
    pub issue_id: String,
    pub status: String,
    pub headline: String,
    #[serde(default)]
    pub summary: String,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub guidance_used: Vec<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub strengths: Vec<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub risks: Vec<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub recommendations: Vec<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub acceptance_review: Option<AcceptanceCriteriaReview>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub scope_warnings: Vec<ScopeWarning>,
    #[serde(default)]
    pub generated_at: String,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct IssueDriftDetail {
    pub bug_id: String,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub drift_flags: Vec<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub missing_evidence: Vec<EvidenceRef>,
    #[serde(default)]
    pub verification_gap: bool,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct TerminalOpenRequest {
    pub workspace_id: String,
    #[serde(default)]
    pub cols: i64,
    #[serde(default)]
    pub rows: i64,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub terminal_id: Option<String>,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct TerminalWriteRequest {
    pub data: String,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct TerminalResizeRequest {
    pub cols: i64,
    pub rows: i64,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct PlanApproveRequest {
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub feedback: Option<String>,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct PlanRejectRequest {
    pub reason: String,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct PlanTrackingUpdateRequest {
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub ownership_mode: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub owner_label: Option<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub attached_files: Vec<String>,
    #[serde(default)]
    pub replace_attachments: bool,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub feedback: Option<String>,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct RunMetrics {
    pub run_id: String,
    pub workspace_id: String,
    #[serde(default)]
    pub input_tokens: i64,
    #[serde(default)]
    pub output_tokens: i64,
    #[serde(default)]
    pub estimated_cost: f64,
    #[serde(default)]
    pub duration_ms: i64,
    pub model: String,
    pub runtime: String,
    #[serde(default)]
    pub calculated_at: String,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct CostSummary {
    pub workspace_id: String,
    #[serde(default)]
    pub total_runs: i64,
    #[serde(default)]
    pub total_input_tokens: i64,
    #[serde(default)]
    pub total_output_tokens: i64,
    #[serde(default)]
    pub total_estimated_cost: f64,
    #[serde(default)]
    pub total_duration_ms: i64,
    #[serde(default)]
    pub runs_by_status: serde_json::Value,
    #[serde(default)]
    pub cost_by_runtime: serde_json::Value,
    #[serde(default)]
    pub cost_by_model: serde_json::Value,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub period_start: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub period_end: Option<String>,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct IssueQualityScore {
    pub issue_id: String,
    pub workspace_id: String,
    #[serde(default)]
    pub overall: f64,
    #[serde(default)]
    pub completeness: f64,
    #[serde(default)]
    pub clarity: f64,
    #[serde(default)]
    pub evidence_quality: f64,
    #[serde(default)]
    pub has_repro: bool,
    #[serde(default)]
    pub has_severity: bool,
    #[serde(default)]
    pub has_evidence: bool,
    #[serde(default)]
    pub has_impact: bool,
    #[serde(default)]
    pub has_summary: bool,
    #[serde(default)]
    pub title_length: i64,
    #[serde(default)]
    pub summary_length: i64,
    #[serde(default)]
    pub evidence_count: i64,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub suggestions: Vec<String>,
    #[serde(default)]
    pub calculated_at: String,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct DuplicateMatch {
    pub source_id: String,
    pub target_id: String,
    pub similarity: f64,
    #[serde(default)]
    pub match_type: String,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub shared_fields: Vec<String>,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct TriageSuggestion {
    pub issue_id: String,
    pub workspace_id: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub suggested_severity: Option<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub suggested_labels: Vec<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub suggested_owner: Option<String>,
    #[serde(default)]
    pub confidence: f64,
    #[serde(default)]
    pub reasoning: String,
    #[serde(default)]
    pub calculated_at: String,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct CoverageResult {
    pub result_id: String,
    pub workspace_id: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub run_id: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub issue_id: Option<String>,
    #[serde(default)]
    pub line_coverage: f64,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub branch_coverage: Option<f64>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub function_coverage: Option<f64>,
    #[serde(default)]
    pub lines_covered: i64,
    #[serde(default)]
    pub lines_total: i64,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub branches_covered: Option<i64>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub branches_total: Option<i64>,
    #[serde(default)]
    pub files_covered: i64,
    #[serde(default)]
    pub files_total: i64,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub uncovered_files: Vec<String>,
    #[serde(default)]
    pub format: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub raw_report_path: Option<String>,
    #[serde(default)]
    pub created_at: String,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct CoverageDelta {
    pub workspace_id: String,
    pub issue_id: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub baseline: Option<CoverageResult>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub current: Option<CoverageResult>,
    #[serde(default)]
    pub line_delta: f64,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub branch_delta: Option<f64>,
    #[serde(default)]
    pub lines_added: i64,
    #[serde(default)]
    pub lines_lost: i64,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub new_files_covered: Vec<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub files_regressed: Vec<String>,
    #[serde(default)]
    pub calculated_at: String,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct TestSuggestion {
    pub suggestion_id: String,
    pub issue_id: String,
    pub workspace_id: String,
    pub test_file: String,
    pub test_description: String,
    #[serde(default)]
    pub priority: String,
    #[serde(default)]
    pub rationale: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub suggested_code: Option<String>,
    #[serde(default)]
    pub created_at: String,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct ImprovementSuggestion {
    pub suggestion_id: String,
    pub file_path: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub line_start: Option<i64>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub line_end: Option<i64>,
    #[serde(default)]
    pub category: String,
    #[serde(default)]
    pub severity: String,
    #[serde(default)]
    pub description: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub suggested_fix: Option<String>,
    #[serde(default)]
    pub dismissed: bool,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub dismissed_reason: Option<String>,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct PatchCritique {
    pub critique_id: String,
    pub workspace_id: String,
    pub run_id: String,
    pub issue_id: String,
    #[serde(default)]
    pub overall_quality: String,
    #[serde(default)]
    pub correctness: f64,
    #[serde(default)]
    pub completeness: f64,
    #[serde(default)]
    pub style: f64,
    #[serde(default)]
    pub safety: f64,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub issues_found: Vec<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub improvements: Vec<ImprovementSuggestion>,
    #[serde(default)]
    pub summary: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub acceptance_review: Option<AcceptanceCriteriaReview>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub scope_warnings: Vec<ScopeWarning>,
    #[serde(default)]
    pub generated_at: String,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct AcceptanceCriteriaReview {
    #[serde(default)]
    pub status: String,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub criteria: Vec<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub matched: Vec<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub missing: Vec<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub notes: Vec<String>,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct ScopeWarning {
    #[serde(default)]
    pub kind: String,
    pub message: String,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub paths: Vec<String>,
    #[serde(default)]
    pub severity: String,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct EvalScenarioRecord {
    pub scenario_id: String,
    pub workspace_id: String,
    pub issue_id: String,
    pub name: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub description: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub baseline_replay_id: Option<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub guidance_paths: Vec<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub ticket_context_ids: Vec<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub verification_profile_ids: Vec<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub run_ids: Vec<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub browser_dump_ids: Vec<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub notes: Option<String>,
    #[serde(default)]
    pub created_at: String,
    #[serde(default)]
    pub updated_at: String,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct EvalReplayBatchRecord {
    pub batch_id: String,
    pub workspace_id: String,
    pub issue_id: String,
    pub runtime: String,
    pub model: String,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub scenario_ids: Vec<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub queued_run_ids: Vec<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub instruction: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub runbook_id: Option<String>,
    #[serde(default)]
    pub planning: bool,
    #[serde(default)]
    pub created_at: String,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct EvalScenarioVariantDiff {
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub selected_guidance_paths: Vec<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub current_guidance_paths: Vec<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub added_guidance_paths: Vec<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub removed_guidance_paths: Vec<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub selected_ticket_context_ids: Vec<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub current_ticket_context_ids: Vec<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub added_ticket_context_ids: Vec<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub removed_ticket_context_ids: Vec<String>,
    #[serde(default)]
    pub changed: bool,
    #[serde(default)]
    pub summary: String,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct EvalVariantRollup {
    #[serde(default)]
    pub variant_kind: String,
    pub variant_key: String,
    pub label: String,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub selected_values: Vec<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub scenario_ids: Vec<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub scenario_names: Vec<String>,
    #[serde(default)]
    pub scenario_count: i64,
    #[serde(default)]
    pub run_count: i64,
    #[serde(default)]
    pub success_runs: i64,
    #[serde(default)]
    pub failed_runs: i64,
    #[serde(default)]
    pub total_estimated_cost: f64,
    #[serde(default)]
    pub avg_duration_ms: i64,
    #[serde(default)]
    pub verification_success_rate: f64,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub runtime_breakdown: Vec<VerificationProfileDimensionSummary>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub model_breakdown: Vec<VerificationProfileDimensionSummary>,
    #[serde(default)]
    pub summary: String,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct EvalScenarioReport {
    pub scenario: EvalScenarioRecord,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub baseline_replay: Option<IssueContextReplayRecord>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub latest_replay_comparison: Option<IssueContextReplayComparison>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub variant_diff: Option<EvalScenarioVariantDiff>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub comparison_to_baseline: Option<EvalScenarioBaselineComparison>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub latest_fresh_run: Option<EvalFreshRunSummary>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub fresh_comparison_to_baseline: Option<EvalFreshExecutionComparison>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub verification_profile_reports: Vec<VerificationProfileReport>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub run_metrics: Vec<RunMetrics>,
    #[serde(default)]
    pub total_estimated_cost: f64,
    #[serde(default)]
    pub avg_duration_ms: i64,
    #[serde(default)]
    pub success_runs: i64,
    #[serde(default)]
    pub failed_runs: i64,
    #[serde(default)]
    pub verification_success_rate: f64,
    #[serde(default)]
    pub summary: String,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct EvalScenarioBaselineComparison {
    pub compared_to_scenario_id: String,
    pub compared_to_name: String,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub guidance_only_in_scenario: Vec<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub guidance_only_in_baseline: Vec<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub ticket_context_only_in_scenario: Vec<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub ticket_context_only_in_baseline: Vec<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub browser_dump_only_in_scenario: Vec<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub browser_dump_only_in_baseline: Vec<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub verification_profile_only_in_scenario: Vec<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub verification_profile_only_in_baseline: Vec<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub verification_profile_deltas: Vec<EvalScenarioVerificationProfileDelta>,
    #[serde(default)]
    pub success_runs_delta: i64,
    #[serde(default)]
    pub failed_runs_delta: i64,
    #[serde(default)]
    pub verification_success_rate_delta: f64,
    #[serde(default)]
    pub avg_duration_ms_delta: i64,
    #[serde(default)]
    pub total_estimated_cost_delta: f64,
    #[serde(default)]
    pub preferred: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub preferred_scenario_id: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub preferred_scenario_name: Option<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub preference_reasons: Vec<String>,
    #[serde(default)]
    pub summary: String,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct EvalScenarioVerificationProfileDelta {
    pub profile_id: String,
    pub profile_name: String,
    #[serde(default)]
    pub present_in_scenario: bool,
    #[serde(default)]
    pub present_in_baseline: bool,
    #[serde(default)]
    pub scenario_total_runs: i64,
    #[serde(default)]
    pub baseline_total_runs: i64,
    #[serde(default)]
    pub total_runs_delta: i64,
    #[serde(default)]
    pub scenario_success_rate: f64,
    #[serde(default)]
    pub baseline_success_rate: f64,
    #[serde(default)]
    pub success_rate_delta: f64,
    #[serde(default)]
    pub scenario_checklist_pass_rate: f64,
    #[serde(default)]
    pub baseline_checklist_pass_rate: f64,
    #[serde(default)]
    pub checklist_pass_rate_delta: f64,
    #[serde(default)]
    pub scenario_avg_attempt_count: f64,
    #[serde(default)]
    pub baseline_avg_attempt_count: f64,
    #[serde(default)]
    pub avg_attempt_count_delta: f64,
    #[serde(default)]
    pub scenario_confidence_counts: serde_json::Value,
    #[serde(default)]
    pub baseline_confidence_counts: serde_json::Value,
    #[serde(default)]
    pub preferred: String,
    #[serde(default)]
    pub summary: String,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct EvalFreshRunSummary {
    pub scenario_id: String,
    pub scenario_name: String,
    pub run_id: String,
    pub status: String,
    pub runtime: String,
    pub model: String,
    pub created_at: String,
    #[serde(default)]
    pub estimated_cost: f64,
    #[serde(default)]
    pub duration_ms: i64,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub command_preview: Option<String>,
    #[serde(default)]
    pub planning: bool,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct EvalFreshExecutionComparison {
    pub compared_to_scenario_id: String,
    pub compared_to_name: String,
    pub scenario_status: String,
    pub baseline_status: String,
    #[serde(default)]
    pub estimated_cost_delta: f64,
    #[serde(default)]
    pub duration_ms_delta: i64,
    #[serde(default)]
    pub preferred: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub preferred_scenario_id: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub preferred_scenario_name: Option<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub preference_reasons: Vec<String>,
    #[serde(default)]
    pub summary: String,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct EvalFreshReplayRankingEntry {
    #[serde(default)]
    pub rank: i64,
    pub scenario_id: String,
    pub scenario_name: String,
    pub latest_fresh_run: EvalFreshRunSummary,
    #[serde(default)]
    pub pairwise_wins: i64,
    #[serde(default)]
    pub pairwise_losses: i64,
    #[serde(default)]
    pub pairwise_ties: i64,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub preference_reasons: Vec<String>,
    #[serde(default)]
    pub summary: String,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct EvalFreshReplayRanking {
    pub issue_id: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub baseline_scenario_id: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub baseline_scenario_name: Option<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub ranked_scenarios: Vec<EvalFreshReplayRankingEntry>,
    #[serde(default)]
    pub summary: String,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct EvalFreshReplayTrendEntry {
    pub scenario_id: String,
    pub scenario_name: String,
    #[serde(default)]
    pub current_rank: i64,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub previous_rank: Option<i64>,
    #[serde(default)]
    pub movement: String,
    pub latest_fresh_run: EvalFreshRunSummary,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub previous_fresh_run: Option<EvalFreshRunSummary>,
    #[serde(default)]
    pub summary: String,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct EvalFreshReplayTrend {
    pub issue_id: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub latest_batch_id: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub previous_batch_id: Option<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub entries: Vec<EvalFreshReplayTrendEntry>,
    #[serde(default)]
    pub summary: String,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct EvalWorkspaceReport {
    pub workspace_id: String,
    #[serde(default)]
    pub scenario_count: i64,
    #[serde(default)]
    pub run_count: i64,
    #[serde(default)]
    pub success_runs: i64,
    #[serde(default)]
    pub failed_runs: i64,
    #[serde(default)]
    pub total_estimated_cost: f64,
    #[serde(default)]
    pub total_duration_ms: i64,
    #[serde(default)]
    pub verification_success_rate: f64,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub cost_summary: Option<CostSummary>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub scenario_reports: Vec<EvalScenarioReport>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub replay_batches: Vec<EvalReplayBatchRecord>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub fresh_replay_rankings: Vec<EvalFreshReplayRanking>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub fresh_replay_trends: Vec<EvalFreshReplayTrend>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub guidance_variant_rollups: Vec<EvalVariantRollup>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub ticket_context_variant_rollups: Vec<EvalVariantRollup>,
    #[serde(default)]
    pub generated_at: String,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct EvalScenarioReplayRequest {
    pub runtime: String,
    pub model: String,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub scenario_ids: Vec<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub instruction: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub runbook_id: Option<String>,
    #[serde(default)]
    pub planning: bool,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct EvalScenarioReplayResult {
    pub workspace_id: String,
    pub issue_id: String,
    pub runtime: String,
    pub model: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub batch_id: Option<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub scenario_ids: Vec<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub queued_runs: Vec<RunRecord>,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct DismissImprovementRequest {
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub reason: Option<String>,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct IntegrationConfig {
    pub config_id: String,
    pub workspace_id: String,
    pub provider: String,
    #[serde(default)]
    pub enabled: bool,
    #[serde(default)]
    pub settings: serde_json::Value,
    #[serde(default)]
    pub created_at: String,
    #[serde(default)]
    pub updated_at: String,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct GitHubIssueImport {
    pub import_id: String,
    pub workspace_id: String,
    pub github_repo: String,
    pub issue_number: i64,
    pub issue_id: String,
    pub title: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub body: Option<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub labels: Vec<String>,
    #[serde(default)]
    pub state: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub html_url: Option<String>,
    #[serde(default)]
    pub imported_at: String,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct GitHubPRCreate {
    pub workspace_id: String,
    pub run_id: String,
    pub issue_id: String,
    pub head_branch: String,
    #[serde(default)]
    pub base_branch: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub title: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub body: Option<String>,
    #[serde(default)]
    pub draft: bool,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct GitHubPRResult {
    pub pr_id: String,
    pub workspace_id: String,
    pub run_id: String,
    pub issue_id: String,
    pub pr_number: i64,
    pub html_url: String,
    #[serde(default)]
    pub state: String,
    #[serde(default)]
    pub created_at: String,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct SlackNotification {
    pub notification_id: String,
    pub workspace_id: String,
    pub event: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub channel: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub webhook_url: Option<String>,
    #[serde(default)]
    pub message: String,
    #[serde(default)]
    pub status: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub error: Option<String>,
    #[serde(default)]
    pub created_at: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub sent_at: Option<String>,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct LinearIssueSync {
    pub sync_id: String,
    pub workspace_id: String,
    pub issue_id: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub linear_id: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub linear_team_id: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub linear_status: Option<String>,
    #[serde(default)]
    pub title: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub description: Option<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub labels: Vec<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub priority: Option<String>,
    #[serde(default)]
    pub sync_direction: String,
    #[serde(default)]
    pub synced_at: String,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct JiraIssueSync {
    pub sync_id: String,
    pub workspace_id: String,
    pub issue_id: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub jira_key: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub jira_project: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub jira_status: Option<String>,
    #[serde(default)]
    pub summary: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub description: Option<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub labels: Vec<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub priority: Option<String>,
    #[serde(default)]
    pub issue_type: String,
    #[serde(default)]
    pub sync_direction: String,
    #[serde(default)]
    pub synced_at: String,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct IntegrationTestRequest {
    pub provider: String,
    #[serde(default)]
    pub settings: serde_json::Value,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct IntegrationTestResult {
    pub provider: String,
    pub ok: bool,
    #[serde(default)]
    pub message: String,
    #[serde(default)]
    pub details: serde_json::Value,
    #[serde(default)]
    pub tested_at: String,
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn workspace_record_round_trip() {
        let record = WorkspaceRecord {
            workspace_id: "ws-1".into(),
            name: "test-workspace".into(),
            root_path: "/tmp/test".into(),
            latest_scan_at: Some("2026-01-01T00:00:00Z".into()),
            created_at: "2026-01-01T00:00:00Z".into(),
            updated_at: "2026-01-01T00:00:00Z".into(),
        };
        let json = serde_json::to_string(&record).unwrap();
        let deserialized: WorkspaceRecord = serde_json::from_str(&json).unwrap();
        assert_eq!(record, deserialized);
    }

    #[test]
    fn app_settings_round_trip() {
        let settings = AppSettings {
            local_agent_type: "opencode".into(),
            codex_bin: Some("/usr/bin/codex".into()),
            opencode_bin: None,
            codex_args: None,
            codex_model: Some("deepseek-v4-pro".into()),
            opencode_model: None,
            postgres_dsn: Some("postgresql://localhost/xmustard".into()),
            postgres_schema: "xmustard".into(),
        };
        let json = serde_json::to_string(&settings).unwrap();
        let deserialized: AppSettings = serde_json::from_str(&json).unwrap();
        assert_eq!(settings, deserialized);
    }

    #[test]
    fn issue_record_round_trip() {
        let record = IssueRecord {
            bug_id: "BUG-001".into(),
            title: "Null pointer in parser".into(),
            severity: "high".into(),
            issue_status: "open".into(),
            source: "ledger".into(),
            source_doc: Some("docs/bugs/BUG-001.md".into()),
            doc_status: "open".into(),
            code_status: "unknown".into(),
            summary: Some("A null pointer dereference in the parser".into()),
            impact: Some("Crashes the service on malformed input".into()),
            evidence: vec![EvidenceRef {
                path: "src/parser.rs".into(),
                line: Some(42),
                excerpt: Some("let x = *ptr;".into()),
                normalized_path: None,
                path_exists: None,
                path_scope: None,
            }],
            verification_evidence: vec![],
            tests_added: vec!["test_parser_null".into()],
            tests_passed: vec!["test_parser_null".into()],
            drift_flags: vec![],
            labels: vec!["bug".into(), "parser".into()],
            notes: Some("Found during fuzzing".into()),
            verified_at: None,
            verified_by: None,
            needs_followup: true,
            review_ready_count: 1,
            review_ready_runs: vec!["run-1".into()],
            fingerprint: Some("abc123".into()),
            updated_at: "2026-01-01T00:00:00Z".into(),
        };
        let json = serde_json::to_string(&record).unwrap();
        let deserialized: IssueRecord = serde_json::from_str(&json).unwrap();
        assert_eq!(record, deserialized);
    }

    #[test]
    fn issue_record_minimal_deserialization() {
        let json = r#"{"bug_id":"BUG-002","title":"Minimal bug","severity":"low"}"#;
        let issue: IssueRecord = serde_json::from_str(json).unwrap();
        assert_eq!(issue.bug_id, "BUG-002");
        assert_eq!(issue.title, "Minimal bug");
        assert_eq!(issue.severity, "low");
        assert_eq!(issue.issue_status, "");
        assert_eq!(issue.source, "");
        assert!(issue.source_doc.is_none());
        assert_eq!(issue.doc_status, "");
        assert_eq!(issue.code_status, "");
        assert!(issue.summary.is_none());
        assert!(issue.impact.is_none());
        assert!(issue.evidence.is_empty());
        assert!(issue.verification_evidence.is_empty());
        assert!(issue.tests_added.is_empty());
        assert!(issue.tests_passed.is_empty());
        assert!(issue.drift_flags.is_empty());
        assert!(issue.labels.is_empty());
        assert!(issue.notes.is_none());
        assert!(issue.verified_at.is_none());
        assert!(issue.verified_by.is_none());
        assert!(!issue.needs_followup);
        assert_eq!(issue.review_ready_count, 0);
        assert!(issue.review_ready_runs.is_empty());
        assert!(issue.fingerprint.is_none());
        assert_eq!(issue.updated_at, "");
    }

    #[test]
    fn activity_record_round_trip() {
        let record = ActivityRecord {
            activity_id: "act-1".into(),
            workspace_id: "ws-1".into(),
            entity_type: "issue".into(),
            entity_id: "bug-1".into(),
            action: "status_change".into(),
            summary: "Changed status to triaged".into(),
            actor: ActivityActor {
                kind: "operator".into(),
                name: "alice".into(),
                runtime: None,
                model: None,
                key: Some("operator:alice".into()),
                label: Some("alice".into()),
            },
            issue_id: Some("bug-1".into()),
            run_id: None,
            details: serde_json::json!({"from": "open", "to": "triaged"}),
            created_at: "2026-01-01T00:00:00Z".into(),
        };
        let json = serde_json::to_string(&record).unwrap();
        let deserialized: ActivityRecord = serde_json::from_str(&json).unwrap();
        assert_eq!(record, deserialized);
    }

    #[test]
    fn runbook_record_round_trip() {
        let record = RunbookRecord {
            runbook_id: "rb-1".into(),
            workspace_id: "ws-1".into(),
            name: "Default Fix".into(),
            description: "Standard fix workflow".into(),
            scope: "issue".into(),
            template: "Fix the bug in {{file}}".into(),
            built_in: false,
            created_at: "2026-01-01T00:00:00Z".into(),
            updated_at: "2026-01-01T00:00:00Z".into(),
        };
        let json = serde_json::to_string(&record).unwrap();
        let deserialized: RunbookRecord = serde_json::from_str(&json).unwrap();
        assert_eq!(record, deserialized);
    }

    #[test]
    fn browser_dump_record_round_trip() {
        let record = BrowserDumpRecord {
            dump_id: "dump-1".into(),
            workspace_id: "ws-1".into(),
            issue_id: "bug-1".into(),
            source: "mcp-chrome".into(),
            label: "Homepage snapshot".into(),
            page_url: Some("http://localhost:3000".into()),
            page_title: Some("My App".into()),
            summary: "Snapshot of homepage".into(),
            dom_snapshot: "<html>...</html>".into(),
            console_messages: vec!["error: Failed to load".into()],
            network_requests: vec!["GET /api/status 200".into()],
            screenshot_path: None,
            notes: None,
            created_at: "2026-01-01T00:00:00Z".into(),
            updated_at: "2026-01-01T00:00:00Z".into(),
        };
        let json = serde_json::to_string(&record).unwrap();
        let deserialized: BrowserDumpRecord = serde_json::from_str(&json).unwrap();
        assert_eq!(record, deserialized);
    }
}
