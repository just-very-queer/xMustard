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
  kind: 'ledger' | 'verdict_bundle' | 'scanner' | 'tracker_issue' | 'fix_record'
  label: string
  path: string
  record_count: number
  modified_at?: string | null
  notes?: string | null
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

export type IssueContextPacket = {
  issue: IssueRecord
  workspace: WorkspaceRecord
  tree_focus: string[]
  evidence_bundle: EvidenceRef[]
  recent_fixes: FixRecord[]
  recent_activity: ActivityRecord[]
  runbook: string[]
  available_runbooks: RunbookRecord[]
  worktree?: WorktreeStatus | null
  prompt: string
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

export type TreeNode = {
  path: string
  name: string
  node_type: 'directory' | 'file'
  has_children: boolean
  size_bytes?: number | null
}
