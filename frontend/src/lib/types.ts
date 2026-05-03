export type EvidenceRef = {
  path: string
  line?: number | null
  excerpt?: string | null
  normalized_path?: string | null
  path_exists?: boolean | null
  path_scope?: string | null
}

export type IssueRecord = {
  bug_id: string
  title: string
  severity: string
  issue_status: string
  source: string
  source_doc?: string | null
  doc_status: string
  code_status: string
  summary?: string | null
  impact?: string | null
  evidence: EvidenceRef[]
  verification_evidence: EvidenceRef[]
  tests_added: string[]
  tests_passed: string[]
  drift_flags: string[]
  labels: string[]
  notes?: string | null
  verified_at?: string | null
  verified_by?: string | null
  needs_followup: boolean
  review_ready_count: number
  review_ready_runs: string[]
  updated_at?: string | null
}

export type DiscoverySignal = {
  signal_id: string
  kind: string
  severity: string
  title: string
  summary: string
  file_path: string
  line: number
  evidence: EvidenceRef[]
  tags: string[]
  promoted_bug_id?: string | null
}

export type WorkspaceRecord = {
  workspace_id: string
  name: string
  root_path: string
  latest_scan_at?: string | null
}

export type WorktreeStatus = {
  available: boolean
  is_git_repo: boolean
  branch?: string | null
  head_sha?: string | null
  dirty_files: number
  staged_files: number
  untracked_files: number
  ahead: number
  behind: number
  dirty_paths: string[]
}

export type ApiHealth = {
  status: string
}

export type SavedIssueView = {
  view_id: string
  workspace_id: string
  name: string
  query: string
  severities: string[]
  statuses: string[]
  sources: string[]
  labels: string[]
  drift_only: boolean
  needs_followup?: boolean | null
  review_ready_only: boolean
  created_at: string
  updated_at: string
}

export type SavedIssueViewRequest = {
  name: string
  query?: string
  severities?: string[]
  statuses?: string[]
  sources?: string[]
  labels?: string[]
  drift_only?: boolean
  needs_followup?: boolean | null
  review_ready_only?: boolean
}

export type IssueQueueFilters = {
  query: string
  severities: string[]
  statuses: string[]
  sources: string[]
  labels: string[]
  drift_only: boolean
  needs_followup?: boolean | null
  review_ready_only: boolean
}

export type ViewMode = 'issues' | 'review' | 'signals' | 'runs' | 'sources' | 'drift' | 'tree' | 'activity'

export type SourceRecord = {
  source_id: string
  kind: 'ledger' | 'verdict_bundle' | 'scanner' | 'tracker_issue' | 'fix_record' | 'ticket_context' | 'threat_model' | 'repo_map'
  label: string
  path: string
  record_count: number
  modified_at?: string | null
  notes?: string | null
}

export type RepoMapDirectoryRecord = {
  path: string
  file_count: number
  source_file_count: number
  test_file_count: number
}

export type RepoMapFileRecord = {
  path: string
  role: 'guide' | 'config' | 'entry' | 'test' | 'source'
  size_bytes?: number | null
}

export type RepoMapSummary = {
  workspace_id: string
  root_path: string
  total_files: number
  source_files: number
  test_files: number
  top_extensions: Record<string, number>
  top_directories: RepoMapDirectoryRecord[]
  key_files: RepoMapFileRecord[]
  generated_at: string
}

export type RuntimeModel = {
  runtime: 'codex' | 'opencode'
  id: string
}

export type RuntimeCapabilities = {
  runtime: 'codex' | 'opencode'
  available: boolean
  binary_path?: string | null
  models: RuntimeModel[]
  notes?: string | null
}

export type ActivityActor = {
  kind: 'operator' | 'agent' | 'system'
  name: string
  runtime?: 'codex' | 'opencode' | null
  model?: string | null
  key: string
  label: string
}

export type ActivityRecord = {
  activity_id: string
  workspace_id: string
  entity_type: 'issue' | 'run' | 'view' | 'signal' | 'workspace' | 'settings' | 'fix'
  entity_id: string
  action: string
  summary: string
  actor: ActivityActor
  issue_id?: string | null
  run_id?: string | null
  details: Record<string, unknown>
  created_at: string
}

export type ActivityRollupItem = {
  key: string
  label: string
  count: number
  actor_key?: string | null
  action?: string | null
  entity_type?: string | null
}

export type ActivityOverview = {
  total_events: number
  unique_actors: number
  unique_actions: number
  operator_events: number
  agent_events: number
  system_events: number
  issues_touched: number
  fixes_touched: number
  runs_touched: number
  views_touched: number
  top_actors: ActivityRollupItem[]
  top_actions: ActivityRollupItem[]
  top_entities: ActivityRollupItem[]
  most_recent_at?: string | null
}

export type WorkspaceSnapshot = {
  workspace: WorkspaceRecord
  summary: Record<string, number>
  issues: IssueRecord[]
  signals: DiscoverySignal[]
  sources: SourceRecord[]
  drift_summary: Record<string, number>
  runtimes: RuntimeCapabilities[]
  latest_ledger?: string | null
  latest_verdicts?: string | null
  generated_at: string
}

export type AppSettings = {
  local_agent_type: 'codex' | 'opencode'
  codex_bin?: string | null
  opencode_bin?: string | null
  codex_args?: string | null
  codex_model?: string | null
  opencode_model?: string | null
  postgres_dsn?: string | null
  postgres_schema?: string
}

export type LocalAgentCapabilities = {
  selected_runtime: 'codex' | 'opencode'
  supports_live_subscribe: boolean
  supports_terminal: boolean
  runtimes: RuntimeCapabilities[]
}

export type RuntimeProbeResult = {
  runtime: 'codex' | 'opencode'
  model: string
  ok: boolean
  available: boolean
  checked_at: string
  duration_ms: number
  exit_code?: number | null
  binary_path?: string | null
  command_preview?: string | null
  output_excerpt?: string | null
  error?: string | null
}

export type FixRecord = {
  fix_id: string
  workspace_id: string
  issue_id: string
  status: string
  summary: string
  how?: string | null
  actor: ActivityActor
  run_id?: string | null
  session_id?: string | null
  changed_files: string[]
  tests_run: string[]
  evidence: EvidenceRef[]
  worktree?: WorktreeStatus | null
  notes?: string | null
  updated_at: string
  recorded_at: string
}

export type RepoMapSymbolRecord = {
  path: string
  symbol: string
  kind: 'function' | 'class' | 'method' | 'type' | 'module'
  line_start?: number | null
  line_end?: number | null
  enclosing_scope?: string | null
  reason?: string | null
  score: number
}

export type RelatedContextRecord = {
  artifact_type: 'ticket_context' | 'threat_model' | 'browser_dump' | 'vulnerability_finding' | 'fix_record' | 'activity' | 'run'
  artifact_id: string
  title: string
  path?: string | null
  reason?: string | null
  matched_terms: string[]
  score: number
}

export type ContextRetrievalLedgerEntry = {
  entry_id: string
  source_type: 'evidence' | 'related_path' | 'symbol' | 'semantic_match' | 'artifact' | 'guidance' | 'path_instruction'
  source_id: string
  title: string
  path?: string | null
  reason: string
  matched_terms: string[]
  score: number
}

export type SemanticPatternMatchRecord = {
  path: string
  language?: string | null
  line_start?: number | null
  line_end?: number | null
  column_start?: number | null
  column_end?: number | null
  matched_text: string
  context_lines?: string | null
  meta_variables: string[]
  reason?: string | null
  score: number
}

export type SemanticQueryMaterializationRecord = {
  query_ref: string
  workspace_id: string
  issue_id?: string | null
  run_id?: string | null
  source: 'adhoc_tool' | 'issue_context'
  reason?: string | null
  pattern: string
  language?: string | null
  path_glob?: string | null
  engine: 'ast_grep' | 'none'
  match_count: number
  truncated: boolean
  error?: string | null
}

export type SemanticMatchMaterializationRecord = {
  query_ref: string
  workspace_id: string
  path: string
  language?: string | null
  line_start?: number | null
  line_end?: number | null
  column_start?: number | null
  column_end?: number | null
  matched_text: string
  context_lines?: string | null
  meta_variables: string[]
  reason?: string | null
  score: number
}

export type DynamicContextBundle = {
  symbol_context: RepoMapSymbolRecord[]
  semantic_matches: SemanticPatternMatchRecord[]
  semantic_queries: SemanticQueryMaterializationRecord[]
  semantic_match_rows: SemanticMatchMaterializationRecord[]
  related_context: RelatedContextRecord[]
}

export type RepoMCPServerRecord = {
  name: string
  description: string
  usage: string
}

export type RepoPathInstructionRecord = {
  instruction_id: string
  path: string
  instructions: string
  title?: string | null
  source_path: string
}

export type RepoPathInstructionMatch = {
  instruction_id: string
  path: string
  title?: string | null
  instructions: string
  source_path: string
  matched_paths: string[]
}

export type RepoConfigRecord = {
  workspace_id: string
  source_path?: string | null
  description: string
  path_filters: string[]
  path_instructions: RepoPathInstructionRecord[]
  code_guidelines: string[]
  mcp_servers: RepoMCPServerRecord[]
  loaded_at: string
}

export type RepoConfigHealth = {
  workspace_id: string
  status: 'missing' | 'configured'
  source_path?: string | null
  summary: string
  path_instruction_count: number
  path_filter_count: number
  code_guideline_count: number
  mcp_server_count: number
  loaded_at: string
}

export type IssueContextPacket = {
  issue: IssueRecord
  workspace: WorkspaceRecord
  tree_focus: string[]
  related_paths: string[]
  evidence_bundle: EvidenceRef[]
  recent_fixes: FixRecord[]
  recent_activity: ActivityRecord[]
  guidance: RepoGuidanceRecord[]
  runbook: string[]
  available_runbooks: RunbookRecord[]
  available_verification_profiles: VerificationProfileRecord[]
  ticket_contexts: TicketContextRecord[]
  threat_models: ThreatModelRecord[]
  browser_dumps: BrowserDumpRecord[]
  vulnerability_findings: VulnerabilityFindingRecord[]
  repo_map?: RepoMapSummary | null
  dynamic_context?: DynamicContextBundle | null
  retrieval_ledger: ContextRetrievalLedgerEntry[]
  repo_config?: RepoConfigRecord | null
  matched_path_instructions: RepoPathInstructionMatch[]
  worktree?: WorktreeStatus | null
  prompt: string
}

export type RepoGuidanceRecord = {
  guidance_id: string
  workspace_id: string
  kind: 'agent_instructions' | 'conventions' | 'repo_index' | 'skill' | 'workspace_overview'
  title: string
  path: string
  always_on: boolean
  priority: number
  summary: string
  excerpt?: string | null
  trigger_keywords: string[]
  updated_at?: string | null
}

export type GuidanceStarterRecord = {
  template_id: 'agents' | 'openhands_repo' | 'conventions'
  title: string
  path: string
  description: string
  recommended: boolean
  exists: boolean
  stale: boolean
}

export type RepoGuidanceHealth = {
  workspace_id: string
  status: 'healthy' | 'partial' | 'missing' | 'stale'
  summary: string
  guidance_count: number
  always_on_count: number
  instruction_count: number
  present_files: string[]
  missing_files: string[]
  stale_files: string[]
  recommended_files: string[]
  starters: GuidanceStarterRecord[]
  generated_at: string
}

export type GuidanceStarterRequest = {
  template_id: 'agents' | 'openhands_repo' | 'conventions'
  overwrite?: boolean
}

export type GuidanceStarterResult = {
  workspace_id: string
  template_id: 'agents' | 'openhands_repo' | 'conventions'
  path: string
  created: boolean
  overwritten: boolean
  generated_at: string
}

export type IssueUpdateRequest = {
  severity?: string
  issue_status?: string
  doc_status?: string
  code_status?: string
  labels?: string[]
  notes?: string | null
  needs_followup?: boolean
}

export type IssueCreateRequest = {
  bug_id?: string
  title: string
  severity: string
  summary?: string | null
  impact?: string | null
  issue_status?: string
  doc_status?: string
  code_status?: string
  labels?: string[]
  notes?: string | null
  source_doc?: string | null
  needs_followup?: boolean
}

export type FixRecordRequest = {
  status?: string
  summary: string
  how?: string | null
  run_id?: string | null
  runtime?: 'codex' | 'opencode' | null
  model?: string | null
  changed_files?: string[]
  tests_run?: string[]
  notes?: string | null
  issue_status?: string | null
}

export type FixDraftSuggestion = {
  workspace_id: string
  issue_id: string
  run_id: string
  summary: string
  how?: string | null
  changed_files: string[]
  tests_run: string[]
  suggested_issue_status?: string | null
  source_excerpt?: string | null
}

export type RunbookRecord = {
  runbook_id: string
  workspace_id: string
  name: string
  description: string
  scope: 'issue' | 'workspace'
  template: string
  built_in: boolean
  created_at: string
  updated_at: string
}

export type RunbookUpsertRequest = {
  runbook_id?: string
  name: string
  description?: string
  scope?: 'issue' | 'workspace'
  template: string
}

export type CoverageFormat = 'unknown' | 'cobertura' | 'jacoco' | 'lcov' | 'go'

export type VerificationProfileRecord = {
  profile_id: string
  workspace_id: string
  name: string
  description: string
  test_command: string
  coverage_command?: string | null
  coverage_report_path?: string | null
  coverage_format: CoverageFormat
  max_runtime_seconds: number
  retry_count: number
  source_paths: string[]
  checklist_items: string[]
  built_in: boolean
  created_at: string
  updated_at: string
}

export type VerificationProfileUpsertRequest = {
  profile_id?: string
  name: string
  description?: string
  test_command: string
  coverage_command?: string | null
  coverage_report_path?: string | null
  coverage_format?: CoverageFormat
  max_runtime_seconds?: number
  retry_count?: number
  source_paths?: string[]
  checklist_items?: string[]
}

export type VerificationProfileRunRequest = {
  run_id?: string | null
}

export type VerificationCommandResult = {
  command: string
  cwd: string
  exit_code?: number | null
  success: boolean
  timed_out: boolean
  duration_ms: number
  stdout_excerpt: string
  stderr_excerpt: string
  created_at: string
}

export type VerificationChecklistResult = {
  item_id: string
  title: string
  kind: 'system' | 'custom'
  passed: boolean
  details?: string | null
}

export type VerificationProfileExecutionResult = {
  execution_id: string
  profile_id: string
  workspace_id: string
  profile_name: string
  issue_id?: string | null
  run_id?: string | null
  attempts: VerificationCommandResult[]
  attempt_count: number
  success: boolean
  coverage_command_result?: VerificationCommandResult | null
  coverage_result?: CoverageResult | null
  checklist_results: VerificationChecklistResult[]
  confidence: 'high' | 'medium' | 'low'
  coverage_report_path?: string | null
  created_at: string
}

export type VerificationProfileDimensionSummary = {
  key: string
  label: string
  total_runs: number
  success_runs: number
  failed_runs: number
  success_rate: number
  last_run_at?: string | null
}

export type VerificationProfileReport = {
  profile_id: string
  workspace_id: string
  profile_name: string
  built_in: boolean
  issue_id?: string | null
  total_runs: number
  success_runs: number
  failed_runs: number
  success_rate: number
  confidence_counts: Record<string, number>
  avg_attempt_count: number
  checklist_pass_rate: number
  last_run_at?: string | null
  last_issue_id?: string | null
  last_run_id?: string | null
  last_confidence?: 'high' | 'medium' | 'low' | null
  last_success?: boolean | null
  runtime_breakdown: VerificationProfileDimensionSummary[]
  model_breakdown: VerificationProfileDimensionSummary[]
  branch_breakdown: VerificationProfileDimensionSummary[]
}

export type TicketContextRecord = {
  context_id: string
  workspace_id: string
  issue_id: string
  provider: 'github' | 'jira' | 'linear' | 'manual' | 'incident' | 'other'
  external_id?: string | null
  title: string
  summary: string
  acceptance_criteria: string[]
  links: string[]
  labels: string[]
  status?: string | null
  source_excerpt?: string | null
  created_at: string
  updated_at: string
}

export type TicketContextUpsertRequest = {
  context_id?: string
  provider?: 'github' | 'jira' | 'linear' | 'manual' | 'incident' | 'other'
  external_id?: string | null
  title: string
  summary?: string
  acceptance_criteria?: string[]
  links?: string[]
  labels?: string[]
  status?: string | null
  source_excerpt?: string | null
}

export type ThreatModelRecord = {
  threat_model_id: string
  workspace_id: string
  issue_id: string
  title: string
  methodology: 'manual' | 'stride' | 'threat_dragon' | 'pytm' | 'threagile' | 'attack_path'
  summary: string
  assets: string[]
  entry_points: string[]
  trust_boundaries: string[]
  abuse_cases: string[]
  mitigations: string[]
  references: string[]
  status: 'draft' | 'reviewed' | 'accepted'
  created_at: string
  updated_at: string
}

export type ThreatModelUpsertRequest = {
  threat_model_id?: string
  title: string
  methodology?: ThreatModelRecord['methodology']
  summary?: string
  assets?: string[]
  entry_points?: string[]
  trust_boundaries?: string[]
  abuse_cases?: string[]
  mitigations?: string[]
  references?: string[]
  status?: ThreatModelRecord['status']
}

export type IssueContextReplayRecord = {
  replay_id: string
  workspace_id: string
  issue_id: string
  label: string
  prompt: string
  tree_focus: string[]
  guidance_paths: string[]
  verification_profile_ids: string[]
  ticket_context_ids: string[]
  browser_dump_ids: string[]
  created_at: string
}

export type IssueContextReplayComparison = {
  replay: IssueContextReplayRecord
  current_prompt: string
  current_tree_focus: string[]
  current_guidance_paths: string[]
  current_verification_profile_ids: string[]
  current_ticket_context_ids: string[]
  current_browser_dump_ids: string[]
  prompt_changed: boolean
  changed: boolean
  saved_prompt_length: number
  current_prompt_length: number
  added_tree_focus: string[]
  removed_tree_focus: string[]
  added_guidance_paths: string[]
  removed_guidance_paths: string[]
  added_verification_profile_ids: string[]
  removed_verification_profile_ids: string[]
  added_ticket_context_ids: string[]
  removed_ticket_context_ids: string[]
  added_browser_dump_ids: string[]
  removed_browser_dump_ids: string[]
  summary: string
  compared_at: string
}

export type BrowserDumpRecord = {
  dump_id: string
  workspace_id: string
  issue_id: string
  source: 'mcp-chrome' | 'manual' | 'playwright' | 'other'
  label: string
  page_url?: string | null
  page_title?: string | null
  summary: string
  dom_snapshot: string
  console_messages: string[]
  network_requests: string[]
  screenshot_path?: string | null
  notes?: string | null
  created_at: string
  updated_at: string
}

export type BrowserDumpUpsertRequest = {
  dump_id?: string
  source?: BrowserDumpRecord['source']
  label: string
  page_url?: string | null
  page_title?: string | null
  summary?: string
  dom_snapshot?: string
  console_messages?: string[]
  network_requests?: string[]
  screenshot_path?: string | null
  notes?: string | null
}

export type VulnerabilityFindingRecord = {
  finding_id: string
  workspace_id: string
  issue_id: string
  scanner: string
  source: 'manual' | 'semgrep' | 'codeql' | 'snyk' | 'osv_scanner' | 'grype' | 'trivy' | 'other'
  severity: 'critical' | 'high' | 'medium' | 'low' | 'info'
  status: 'open' | 'accepted' | 'fixed' | 'dismissed'
  title: string
  summary: string
  rule_id?: string | null
  location_path?: string | null
  location_line?: number | null
  cwe_ids: string[]
  cve_ids: string[]
  references: string[]
  evidence: string[]
  threat_model_ids: string[]
  raw_payload?: string | null
  created_at: string
  updated_at: string
}

export type RunReviewDisposition = 'dismissed' | 'investigation_only'

export type RunReviewRecord = {
  review_id: string
  workspace_id: string
  run_id: string
  issue_id: string
  disposition: RunReviewDisposition
  actor: ActivityActor
  notes?: string | null
  created_at: string
}

export type RunReviewRequest = {
  disposition: RunReviewDisposition
  notes?: string | null
}

export type IssueDriftDetail = {
  bug_id: string
  drift_flags: string[]
  missing_evidence: EvidenceRef[]
  verification_gap: boolean
}

export type RunRecord = {
  run_id: string
  workspace_id: string
  issue_id: string
  runtime: 'codex' | 'opencode'
  model: string
  status: string
  runbook_id?: string | null
  eval_scenario_id?: string | null
  eval_replay_batch_id?: string | null
  title: string
  prompt: string
  command: string[]
  command_preview: string
  log_path: string
  output_path: string
  created_at: string
  started_at?: string | null
  completed_at?: string | null
  exit_code?: number | null
  pid?: number | null
  error?: string | null
  worktree?: WorktreeStatus | null
  guidance_paths: string[]
  summary?: {
    runtime: string
    session_id?: string | null
    event_count: number
    tool_event_count: number
    last_event_type?: string | null
    text_excerpt?: string | null
  } | null
  plan?: RunPlan | null
}

export type RunSessionInsight = {
  workspace_id: string
  run_id: string
  issue_id: string
  status: 'queued' | 'planning' | 'running' | 'completed' | 'failed' | 'cancelled'
  headline: string
  summary: string
  guidance_used: string[]
  strengths: string[]
  risks: string[]
  recommendations: string[]
  acceptance_review?: AcceptanceCriteriaReview | null
  scope_warnings: ScopeWarning[]
  generated_at: string
}

export type AcceptanceCriteriaReview = {
  status: 'met' | 'partial' | 'not_met' | 'unknown'
  criteria: string[]
  matched: string[]
  missing: string[]
  notes: string[]
}

export type ScopeWarning = {
  kind: 'unrelated_change' | 'scope_drift'
  message: string
  paths: string[]
  severity: 'low' | 'medium' | 'high'
}

export type PlanStep = {
  step_id: string
  description: string
  estimated_impact: 'low' | 'medium' | 'high'
  files_affected: string[]
  risks: string[]
}

export type RunPlan = {
  plan_id: string
  run_id: string
  phase: 'pending' | 'generating' | 'awaiting_approval' | 'approved' | 'modified' | 'rejected' | 'executing'
  steps: PlanStep[]
  summary: string
  reasoning?: string | null
  created_at: string
  approved_at?: string | null
  approver?: string | null
  feedback?: string | null
  modified_summary?: string | null
}

export type PlanApproveRequest = {
  feedback?: string
}

export type PlanRejectRequest = {
  reason: string
}

export type RunMetrics = {
  run_id: string
  workspace_id: string
  input_tokens: number
  output_tokens: number
  estimated_cost: number
  duration_ms: number
  model: string
  runtime: 'codex' | 'opencode'
  calculated_at: string
}

export type CostSummary = {
  workspace_id: string
  total_runs: number
  total_input_tokens: number
  total_output_tokens: number
  total_estimated_cost: number
  total_duration_ms: number
  runs_by_status: Record<string, number>
  cost_by_runtime: Record<string, number>
  cost_by_model: Record<string, number>
  period_start?: string | null
  period_end?: string | null
}

export type IssueQualityScore = {
  issue_id: string
  workspace_id: string
  overall: number
  completeness: number
  clarity: number
  evidence_quality: number
  has_repro: boolean
  has_severity: boolean
  has_evidence: boolean
  has_impact: boolean
  has_summary: boolean
  title_length: number
  summary_length: number
  evidence_count: number
  suggestions: string[]
  calculated_at: string
}

export type DuplicateMatch = {
  source_id: string
  target_id: string
  similarity: number
  match_type: 'exact' | 'fuzzy' | 'fingerprint'
  shared_fields: string[]
}

export type TriageSuggestion = {
  issue_id: string
  workspace_id: string
  suggested_severity?: string | null
  suggested_labels: string[]
  suggested_owner?: string | null
  confidence: number
  reasoning: string
  calculated_at: string
}

export type CoverageResult = {
  result_id: string
  workspace_id: string
  run_id?: string | null
  issue_id?: string | null
  line_coverage: number
  branch_coverage?: number | null
  function_coverage?: number | null
  lines_covered: number
  lines_total: number
  branches_covered?: number | null
  branches_total?: number | null
  files_covered: number
  files_total: number
  uncovered_files: string[]
  format: string
  raw_report_path?: string | null
  created_at: string
}

export type CoverageDelta = {
  workspace_id: string
  issue_id: string
  baseline?: CoverageResult | null
  current?: CoverageResult | null
  line_delta: number
  branch_delta?: number | null
  lines_added: number
  lines_lost: number
  new_files_covered: string[]
  files_regressed: string[]
  calculated_at: string
}

export type TestSuggestion = {
  suggestion_id: string
  issue_id: string
  workspace_id: string
  test_file: string
  test_description: string
  priority: 'high' | 'medium' | 'low'
  rationale: string
  suggested_code?: string | null
  created_at: string
}

export type TreeNode = {
  path: string
  name: string
  node_type: 'directory' | 'file'
  has_children: boolean
  size_bytes?: number | null
}

export type ImprovementSuggestion = {
  suggestion_id: string
  file_path: string
  line_start?: number | null
  line_end?: number | null
  category: 'bug_risk' | 'style' | 'performance' | 'maintainability' | 'security' | 'testing'
  severity: 'high' | 'medium' | 'low'
  description: string
  suggested_fix?: string | null
  dismissed: boolean
  dismissed_reason?: string | null
}

export type PatchCritique = {
  critique_id: string
  workspace_id: string
  run_id: string
  issue_id: string
  overall_quality: 'excellent' | 'good' | 'acceptable' | 'needs_work' | 'poor'
  correctness: number
  completeness: number
  style: number
  safety: number
  issues_found: string[]
  improvements: ImprovementSuggestion[]
  summary: string
  acceptance_review?: AcceptanceCriteriaReview | null
  scope_warnings: ScopeWarning[]
  generated_at: string
}

export type EvalScenarioRecord = {
  scenario_id: string
  workspace_id: string
  issue_id: string
  name: string
  description?: string | null
  baseline_replay_id?: string | null
  guidance_paths: string[]
  ticket_context_ids: string[]
  verification_profile_ids: string[]
  run_ids: string[]
  browser_dump_ids: string[]
  notes?: string | null
  created_at: string
  updated_at: string
}

export type EvalScenarioUpsertRequest = {
  scenario_id?: string
  name: string
  issue_id: string
  description?: string | null
  baseline_replay_id?: string | null
  guidance_paths?: string[]
  ticket_context_ids?: string[]
  verification_profile_ids?: string[]
  run_ids?: string[]
  browser_dump_ids?: string[]
  notes?: string | null
}

export type EvalScenarioVariantDiff = {
  selected_guidance_paths: string[]
  current_guidance_paths: string[]
  added_guidance_paths: string[]
  removed_guidance_paths: string[]
  selected_ticket_context_ids: string[]
  current_ticket_context_ids: string[]
  added_ticket_context_ids: string[]
  removed_ticket_context_ids: string[]
  changed: boolean
  summary: string
}

export type EvalScenarioReport = {
  scenario: EvalScenarioRecord
  baseline_replay?: IssueContextReplayRecord | null
  latest_replay_comparison?: IssueContextReplayComparison | null
  variant_diff?: EvalScenarioVariantDiff | null
  comparison_to_baseline?: EvalScenarioBaselineComparison | null
  latest_fresh_run?: EvalFreshRunSummary | null
  fresh_comparison_to_baseline?: EvalFreshExecutionComparison | null
  verification_profile_reports: VerificationProfileReport[]
  run_metrics: RunMetrics[]
  total_estimated_cost: number
  avg_duration_ms: number
  success_runs: number
  failed_runs: number
  verification_success_rate: number
  summary: string
}

export type EvalScenarioBaselineComparison = {
  compared_to_scenario_id: string
  compared_to_name: string
  guidance_only_in_scenario: string[]
  guidance_only_in_baseline: string[]
  ticket_context_only_in_scenario: string[]
  ticket_context_only_in_baseline: string[]
  browser_dump_only_in_scenario: string[]
  browser_dump_only_in_baseline: string[]
  verification_profile_only_in_scenario: string[]
  verification_profile_only_in_baseline: string[]
  verification_profile_deltas: EvalScenarioVerificationProfileDelta[]
  success_runs_delta: number
  failed_runs_delta: number
  verification_success_rate_delta: number
  avg_duration_ms_delta: number
  total_estimated_cost_delta: number
  preferred: 'scenario' | 'baseline' | 'tie'
  preferred_scenario_id?: string | null
  preferred_scenario_name?: string | null
  preference_reasons: string[]
  summary: string
}

export type EvalScenarioVerificationProfileDelta = {
  profile_id: string
  profile_name: string
  present_in_scenario: boolean
  present_in_baseline: boolean
  scenario_total_runs: number
  baseline_total_runs: number
  total_runs_delta: number
  scenario_success_rate: number
  baseline_success_rate: number
  success_rate_delta: number
  scenario_checklist_pass_rate: number
  baseline_checklist_pass_rate: number
  checklist_pass_rate_delta: number
  scenario_avg_attempt_count: number
  baseline_avg_attempt_count: number
  avg_attempt_count_delta: number
  scenario_confidence_counts: Record<string, number>
  baseline_confidence_counts: Record<string, number>
  preferred: 'scenario' | 'baseline' | 'tie'
  summary: string
}

export type EvalFreshRunSummary = {
  scenario_id: string
  scenario_name: string
  run_id: string
  status: string
  runtime: 'codex' | 'opencode'
  model: string
  created_at: string
  estimated_cost: number
  duration_ms: number
  command_preview?: string | null
  planning: boolean
}

export type EvalFreshExecutionComparison = {
  compared_to_scenario_id: string
  compared_to_name: string
  scenario_status: string
  baseline_status: string
  estimated_cost_delta: number
  duration_ms_delta: number
  preferred: 'scenario' | 'baseline' | 'tie'
  preferred_scenario_id?: string | null
  preferred_scenario_name?: string | null
  preference_reasons: string[]
  summary: string
}

export type EvalFreshReplayRankingEntry = {
  rank: number
  scenario_id: string
  scenario_name: string
  latest_fresh_run: EvalFreshRunSummary
  pairwise_wins: number
  pairwise_losses: number
  pairwise_ties: number
  preference_reasons: string[]
  summary: string
}

export type EvalFreshReplayRanking = {
  issue_id: string
  baseline_scenario_id?: string | null
  baseline_scenario_name?: string | null
  ranked_scenarios: EvalFreshReplayRankingEntry[]
  summary: string
}

export type EvalFreshReplayTrendEntry = {
  scenario_id: string
  scenario_name: string
  current_rank: number
  previous_rank?: number | null
  movement: 'up' | 'down' | 'same' | 'new'
  latest_fresh_run: EvalFreshRunSummary
  previous_fresh_run?: EvalFreshRunSummary | null
  summary: string
}

export type EvalFreshReplayTrend = {
  issue_id: string
  latest_batch_id?: string | null
  previous_batch_id?: string | null
  entries: EvalFreshReplayTrendEntry[]
  summary: string
}

export type EvalReplayBatchRecord = {
  batch_id: string
  workspace_id: string
  issue_id: string
  runtime: 'codex' | 'opencode'
  model: string
  scenario_ids: string[]
  queued_run_ids: string[]
  instruction?: string | null
  runbook_id?: string | null
  planning: boolean
  created_at: string
}

export type EvalVariantRollup = {
  variant_kind: 'guidance' | 'ticket_context'
  variant_key: string
  label: string
  selected_values: string[]
  scenario_ids: string[]
  scenario_names: string[]
  scenario_count: number
  run_count: number
  success_runs: number
  failed_runs: number
  total_estimated_cost: number
  avg_duration_ms: number
  verification_success_rate: number
  runtime_breakdown: VerificationProfileDimensionSummary[]
  model_breakdown: VerificationProfileDimensionSummary[]
  summary: string
}

export type EvalWorkspaceReport = {
  workspace_id: string
  scenario_count: number
  run_count: number
  success_runs: number
  failed_runs: number
  total_estimated_cost: number
  total_duration_ms: number
  verification_success_rate: number
  cost_summary?: CostSummary | null
  scenario_reports: EvalScenarioReport[]
  replay_batches: EvalReplayBatchRecord[]
  fresh_replay_rankings: EvalFreshReplayRanking[]
  fresh_replay_trends: EvalFreshReplayTrend[]
  guidance_variant_rollups: EvalVariantRollup[]
  ticket_context_variant_rollups: EvalVariantRollup[]
  generated_at: string
}

export type EvalScenarioReplayRequest = {
  runtime: 'codex' | 'opencode'
  model: string
  scenario_ids?: string[]
  instruction?: string | null
  runbook_id?: string | null
  planning?: boolean
}

export type EvalScenarioReplayResult = {
  workspace_id: string
  issue_id: string
  runtime: 'codex' | 'opencode'
  model: string
  batch_id?: string | null
  scenario_ids: string[]
  queued_runs: RunRecord[]
}

export type IntegrationConfig = {
  config_id: string
  workspace_id: string
  provider: 'github' | 'slack' | 'linear' | 'jira'
  enabled: boolean
  settings: Record<string, unknown>
  created_at: string
  updated_at: string
}

export type GitHubIssueImport = {
  import_id: string
  workspace_id: string
  github_repo: string
  issue_number: number
  issue_id: string
  title: string
  body?: string | null
  labels: string[]
  state: string
  html_url?: string | null
  imported_at: string
}

export type GitHubPRCreate = {
  workspace_id: string
  run_id: string
  issue_id: string
  head_branch: string
  base_branch?: string
  title?: string | null
  body?: string | null
  draft?: boolean
}

export type GitHubPRResult = {
  pr_id: string
  workspace_id: string
  run_id: string
  issue_id: string
  pr_number: number
  html_url: string
  state: string
  created_at: string
}

export type SlackNotification = {
  notification_id: string
  workspace_id: string
  event: string
  channel?: string | null
  webhook_url?: string | null
  message: string
  status: 'pending' | 'sent' | 'failed'
  error?: string | null
  created_at: string
  sent_at?: string | null
}

export type LinearIssueSync = {
  sync_id: string
  workspace_id: string
  issue_id: string
  linear_id?: string | null
  linear_team_id?: string | null
  linear_status?: string | null
  title: string
  description?: string | null
  labels: string[]
  priority?: string | null
  sync_direction: 'push' | 'pull'
  synced_at: string
}

export type JiraIssueSync = {
  sync_id: string
  workspace_id: string
  issue_id: string
  jira_key?: string | null
  jira_project?: string | null
  jira_status?: string | null
  summary: string
  description?: string | null
  labels: string[]
  priority?: string | null
  issue_type: string
  sync_direction: 'push' | 'pull'
  synced_at: string
}

export type IntegrationTestResult = {
  provider: 'github' | 'slack' | 'linear' | 'jira'
  ok: boolean
  message: string
  details: Record<string, unknown>
  tested_at: string
}
