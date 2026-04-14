import type {
  ApiHealth,
  ActivityOverview,
  ActivityRecord,
  AppSettings,
  CostSummary,
  CoverageDelta,
  CoverageResult,
  DuplicateMatch,
  FixDraftSuggestion,
  FixRecord,
  FixRecordRequest,
  GitHubIssueImport,
  GitHubPRCreate,
  GitHubPRResult,
  ImprovementSuggestion,
  IntegrationConfig,
  IntegrationTestResult,
  IssueCreateRequest,
  IssueContextReplayRecord,
  IssueDriftDetail,
  IssueContextPacket,
  IssueQualityScore,
  IssueUpdateRequest,
  JiraIssueSync,
  LinearIssueSync,
  LocalAgentCapabilities,
  PatchCritique,
  PlanApproveRequest,
  PlanRejectRequest,
  RepoMapSummary,
  RepoGuidanceRecord,
  RuntimeProbeResult,
  RunMetrics,
  RunPlan,
  RunRecord,
  RunSessionInsight,
  RunbookRecord,
  RunbookUpsertRequest,
  RunReviewRecord,
  RunReviewRequest,
  SavedIssueView,
  SavedIssueViewRequest,
  SlackNotification,
  SourceRecord,
  TestSuggestion,
  ThreatModelRecord,
  ThreatModelUpsertRequest,
  TicketContextRecord,
  TicketContextUpsertRequest,
  TriageSuggestion,
  TreeNode,
  VerificationProfileRecord,
  VerificationProfileExecutionResult,
  VerificationProfileRunRequest,
  VerificationProfileUpsertRequest,
  WorktreeStatus,
  WorkspaceSnapshot,
  WorkspaceRecord,
} from './types'

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const response = await fetch(path, {
    headers: {
      'Content-Type': 'application/json',
    },
    ...init,
  })
  if (!response.ok) {
    throw new Error(await response.text())
  }
  return response.json() as Promise<T>
}

export function listWorkspaces() {
  return request<WorkspaceRecord[]>('/api/workspaces')
}

export function getHealth() {
  return request<ApiHealth>('/api/health')
}

export function loadWorkspace(rootPath: string, name?: string) {
  return request<WorkspaceSnapshot>('/api/workspaces/load', {
    method: 'POST',
    body: JSON.stringify({
      root_path: rootPath,
      name,
      auto_scan: true,
      prefer_cached_snapshot: true,
    }),
  })
}

export function scanWorkspace(workspaceId: string) {
  return request<WorkspaceSnapshot>(`/api/workspaces/${workspaceId}/scan`, {
    method: 'POST',
  })
}

export function readSnapshot(workspaceId: string) {
  return request<WorkspaceSnapshot>(`/api/workspaces/${workspaceId}/snapshot`)
}

export function listActivity(
  workspaceId: string,
  params: {
    issue_id?: string
    run_id?: string
    limit?: number
  } = {},
) {
  const query = new URLSearchParams()
  Object.entries(params).forEach(([key, value]) => {
    if (value === undefined || value === '' || value === null) return
    query.set(key, String(value))
  })
  return request<ActivityRecord[]>(`/api/workspaces/${workspaceId}/activity?${query.toString()}`)
}

export function readActivityOverview(workspaceId: string, limit = 200) {
  return request<ActivityOverview>(`/api/workspaces/${workspaceId}/activity/overview?limit=${limit}`)
}

export function listTree(workspaceId: string, relativePath = '') {
  const query = new URLSearchParams()
  if (relativePath) query.set('relative_path', relativePath)
  return request<TreeNode[]>(`/api/workspaces/${workspaceId}/tree?${query.toString()}`)
}

export function listWorkspaceGuidance(workspaceId: string) {
  return request<RepoGuidanceRecord[]>(`/api/workspaces/${workspaceId}/guidance`)
}

export function readRepoMap(workspaceId: string) {
  return request<RepoMapSummary>(`/api/workspaces/${workspaceId}/repo-map`)
}

export function issueContext(workspaceId: string, issueId: string) {
  return request<IssueContextPacket>(`/api/workspaces/${workspaceId}/issues/${issueId}/context`)
}

export function issueWork(workspaceId: string, issueId: string, runbookId?: string) {
  const query = new URLSearchParams()
  if (runbookId) query.set('runbook_id', runbookId)
  return request<IssueContextPacket>(`/api/workspaces/${workspaceId}/issues/${issueId}/work?${query.toString()}`)
}

export function listTicketContext(workspaceId: string, issueId: string) {
  return request<TicketContextRecord[]>(`/api/workspaces/${workspaceId}/issues/${issueId}/ticket-context`)
}

export function saveTicketContext(workspaceId: string, issueId: string, payload: TicketContextUpsertRequest) {
  return request<TicketContextRecord>(`/api/workspaces/${workspaceId}/issues/${issueId}/ticket-context`, {
    method: 'POST',
    body: JSON.stringify(payload),
  })
}

export function deleteTicketContext(workspaceId: string, issueId: string, contextId: string) {
  return request<{ ok: boolean; context_id: string }>(
    `/api/workspaces/${workspaceId}/issues/${issueId}/ticket-context/${contextId}`,
    {
      method: 'DELETE',
    },
  )
}

export function listThreatModels(workspaceId: string, issueId: string) {
  return request<ThreatModelRecord[]>(`/api/workspaces/${workspaceId}/issues/${issueId}/threat-models`)
}

export function saveThreatModel(workspaceId: string, issueId: string, payload: ThreatModelUpsertRequest) {
  return request<ThreatModelRecord>(`/api/workspaces/${workspaceId}/issues/${issueId}/threat-models`, {
    method: 'POST',
    body: JSON.stringify(payload),
  })
}

export function deleteThreatModel(workspaceId: string, issueId: string, threatModelId: string) {
  return request<{ ok: boolean; threat_model_id: string }>(
    `/api/workspaces/${workspaceId}/issues/${issueId}/threat-models/${threatModelId}`,
    {
      method: 'DELETE',
    },
  )
}

export function listIssueContextReplays(workspaceId: string, issueId: string) {
  return request<IssueContextReplayRecord[]>(`/api/workspaces/${workspaceId}/issues/${issueId}/context-replays`)
}

export function captureIssueContextReplay(workspaceId: string, issueId: string, label?: string) {
  return request<IssueContextReplayRecord>(`/api/workspaces/${workspaceId}/issues/${issueId}/context-replays`, {
    method: 'POST',
    body: JSON.stringify({ label }),
  })
}

export function listIssues(
  workspaceId: string,
  params: {
    q?: string
    severity?: string[]
    issue_status?: string[]
    source?: string[]
    label?: string[]
    drift_only?: boolean
    needs_followup?: boolean
    review_ready_only?: boolean
  } = {},
) {
  const query = new URLSearchParams()
  Object.entries(params).forEach(([key, value]) => {
    if (value === undefined || value === '' || value === false) return
    query.set(key, Array.isArray(value) ? value.join(',') : String(value))
  })
  return request<WorkspaceSnapshot['issues']>(`/api/workspaces/${workspaceId}/issues?${query.toString()}`)
}

export function listSavedViews(workspaceId: string) {
  return request<SavedIssueView[]>(`/api/workspaces/${workspaceId}/views`)
}

export function createSavedView(workspaceId: string, payload: SavedIssueViewRequest) {
  return request<SavedIssueView>(`/api/workspaces/${workspaceId}/views`, {
    method: 'POST',
    body: JSON.stringify(payload),
  })
}

export function updateSavedView(workspaceId: string, viewId: string, payload: SavedIssueViewRequest) {
  return request<SavedIssueView>(`/api/workspaces/${workspaceId}/views/${viewId}`, {
    method: 'PUT',
    body: JSON.stringify(payload),
  })
}

export function deleteSavedView(workspaceId: string, viewId: string) {
  return request<{ ok: boolean }>(`/api/workspaces/${workspaceId}/views/${viewId}`, {
    method: 'DELETE',
  })
}

export function updateIssue(workspaceId: string, issueId: string, payload: IssueUpdateRequest) {
  return request<WorkspaceSnapshot['issues'][number]>(`/api/workspaces/${workspaceId}/issues/${issueId}`, {
    method: 'PATCH',
    body: JSON.stringify(payload),
  })
}

export function createIssue(workspaceId: string, payload: IssueCreateRequest) {
  return request<WorkspaceSnapshot['issues'][number]>(`/api/workspaces/${workspaceId}/issues`, {
    method: 'POST',
    body: JSON.stringify(payload),
  })
}

export function readIssueDrift(workspaceId: string, issueId: string) {
  return request<IssueDriftDetail>(`/api/workspaces/${workspaceId}/issues/${issueId}/drift`)
}

export function listSignals(
  workspaceId: string,
  params: {
    q?: string
    severity?: string
    promoted?: boolean
  } = {},
) {
  const query = new URLSearchParams()
  Object.entries(params).forEach(([key, value]) => {
    if (value === undefined || value === '') return
    query.set(key, String(value))
  })
  return request<WorkspaceSnapshot['signals']>(`/api/workspaces/${workspaceId}/signals?${query.toString()}`)
}

export function startRun(
  workspaceId: string,
  issueId: string,
  payload: { runtime: string; model: string; instruction?: string; runbook_id?: string; planning?: boolean },
) {
  return request<RunRecord>(`/api/workspaces/${workspaceId}/issues/${issueId}/runs`, {
    method: 'POST',
    body: JSON.stringify(payload),
  })
}

export function recordFix(workspaceId: string, issueId: string, payload: FixRecordRequest) {
  return request<FixRecord>(`/api/workspaces/${workspaceId}/issues/${issueId}/fixes`, {
    method: 'POST',
    body: JSON.stringify(payload),
  })
}

export function suggestFixDraft(workspaceId: string, issueId: string, runId: string) {
  return request<FixDraftSuggestion>(
    `/api/workspaces/${workspaceId}/issues/${issueId}/fix-draft?run_id=${encodeURIComponent(runId)}`,
  )
}

export function queryAgent(workspaceId: string, payload: { runtime: string; model: string; prompt: string }) {
  return request<RunRecord>(`/api/workspaces/${workspaceId}/agent/query`, {
    method: 'POST',
    body: JSON.stringify(payload),
  })
}

export function listRuns(workspaceId: string) {
  return request<RunRecord[]>(`/api/workspaces/${workspaceId}/runs`)
}

export function readRun(workspaceId: string, runId: string) {
  return request<RunRecord>(`/api/workspaces/${workspaceId}/runs/${runId}`)
}

export function getRunInsights(workspaceId: string, runId: string) {
  return request<RunSessionInsight>(`/api/workspaces/${workspaceId}/runs/${runId}/insights`)
}

export function cancelRun(workspaceId: string, runId: string) {
  return request<RunRecord>(`/api/workspaces/${workspaceId}/runs/${runId}/cancel`, {
    method: 'POST',
  })
}

export function retryRun(workspaceId: string, runId: string) {
  return request<RunRecord>(`/api/workspaces/${workspaceId}/runs/${runId}/retry`, {
    method: 'POST',
  })
}

export function reviewRun(workspaceId: string, runId: string, payload: RunReviewRequest) {
  return request<RunReviewRecord>(`/api/workspaces/${workspaceId}/runs/${runId}/review`, {
    method: 'POST',
    body: JSON.stringify(payload),
  })
}

export function acceptRunReview(workspaceId: string, runId: string, payload: { issue_status?: string; notes?: string }) {
  return request<FixRecord>(`/api/workspaces/${workspaceId}/runs/${runId}/accept`, {
    method: 'POST',
    body: JSON.stringify(payload),
  })
}

export function listRunbooks(workspaceId: string) {
  return request<RunbookRecord[]>(`/api/workspaces/${workspaceId}/runbooks`)
}

export function saveRunbook(workspaceId: string, payload: RunbookUpsertRequest) {
  return request<RunbookRecord>(`/api/workspaces/${workspaceId}/runbooks`, {
    method: 'POST',
    body: JSON.stringify(payload),
  })
}

export function deleteRunbook(workspaceId: string, runbookId: string) {
  return request<{ ok: boolean; runbook_id: string }>(`/api/workspaces/${workspaceId}/runbooks/${runbookId}`, {
    method: 'DELETE',
  })
}

export function listVerificationProfiles(workspaceId: string) {
  return request<VerificationProfileRecord[]>(`/api/workspaces/${workspaceId}/verification-profiles`)
}

export function saveVerificationProfile(workspaceId: string, payload: VerificationProfileUpsertRequest) {
  return request<VerificationProfileRecord>(`/api/workspaces/${workspaceId}/verification-profiles`, {
    method: 'POST',
    body: JSON.stringify(payload),
  })
}

export function deleteVerificationProfile(workspaceId: string, profileId: string) {
  return request<{ ok: boolean; profile_id: string }>(
    `/api/workspaces/${workspaceId}/verification-profiles/${profileId}`,
    {
      method: 'DELETE',
    },
  )
}

export function runVerificationProfileForIssue(
  workspaceId: string,
  issueId: string,
  profileId: string,
  payload: VerificationProfileRunRequest = {},
) {
  return request<VerificationProfileExecutionResult>(
    `/api/workspaces/${workspaceId}/issues/${issueId}/verification-profiles/${profileId}/run`,
    {
      method: 'POST',
      body: JSON.stringify(payload),
    },
  )
}

export function readRunLog(workspaceId: string, runId: string, offset = 0) {
  return request<{ offset: number; content: string; eof: boolean }>(
    `/api/workspaces/${workspaceId}/runs/${runId}/log?offset=${offset}`,
  )
}

export function generatePlan(workspaceId: string, runId: string) {
  return request<RunPlan>(`/api/workspaces/${workspaceId}/runs/${runId}/plan`, {
    method: 'POST',
  })
}

export function getPlan(workspaceId: string, runId: string) {
  return request<RunPlan>(`/api/workspaces/${workspaceId}/runs/${runId}/plan`)
}

export function approvePlan(workspaceId: string, runId: string, payload: PlanApproveRequest) {
  return request<RunPlan>(`/api/workspaces/${workspaceId}/runs/${runId}/plan/approve`, {
    method: 'POST',
    body: JSON.stringify(payload),
  })
}

export function rejectPlan(workspaceId: string, runId: string, payload: PlanRejectRequest) {
  return request<RunPlan>(`/api/workspaces/${workspaceId}/runs/${runId}/plan/reject`, {
    method: 'POST',
    body: JSON.stringify(payload),
  })
}

export function getRunMetrics(workspaceId: string, runId: string) {
  return request<RunMetrics>(`/api/workspaces/${workspaceId}/runs/${runId}/metrics`)
}

export function listWorkspaceMetrics(workspaceId: string) {
  return request<RunMetrics[]>(`/api/workspaces/${workspaceId}/metrics`)
}

export function getWorkspaceCostSummary(workspaceId: string) {
  return request<CostSummary>(`/api/workspaces/${workspaceId}/costs`)
}

export function getIssueQuality(workspaceId: string, issueId: string) {
  return request<IssueQualityScore>(`/api/workspaces/${workspaceId}/issues/${issueId}/quality`)
}

export function scoreIssueQuality(workspaceId: string, issueId: string) {
  return request<IssueQualityScore>(`/api/workspaces/${workspaceId}/issues/${issueId}/quality`, {
    method: 'POST',
  })
}

export function scoreAllIssues(workspaceId: string) {
  return request<IssueQualityScore[]>(`/api/workspaces/${workspaceId}/quality/score-all`, {
    method: 'POST',
  })
}

export function findDuplicates(workspaceId: string, issueId: string) {
  return request<DuplicateMatch[]>(`/api/workspaces/${workspaceId}/issues/${issueId}/duplicates`)
}

export function triageIssue(workspaceId: string, issueId: string) {
  return request<TriageSuggestion>(`/api/workspaces/${workspaceId}/issues/${issueId}/triage`, {
    method: 'POST',
  })
}

export function triageAllIssues(workspaceId: string) {
  return request<TriageSuggestion[]>(`/api/workspaces/${workspaceId}/triage/all`, {
    method: 'POST',
  })
}

export function parseCoverageReport(workspaceId: string, reportPath: string, runId?: string, issueId?: string) {
  const params = new URLSearchParams({ report_path: reportPath })
  if (runId) params.set('run_id', runId)
  if (issueId) params.set('issue_id', issueId)
  return request<CoverageResult>(`/api/workspaces/${workspaceId}/coverage/parse?${params}`, {
    method: 'POST',
  })
}

export function getCoverage(workspaceId: string, issueId?: string, runId?: string) {
  const params = new URLSearchParams()
  if (issueId) params.set('issue_id', issueId)
  if (runId) params.set('run_id', runId)
  return request<CoverageResult>(`/api/workspaces/${workspaceId}/coverage?${params}`)
}

export function getCoverageDelta(workspaceId: string, issueId: string) {
  return request<CoverageDelta>(`/api/workspaces/${workspaceId}/issues/${issueId}/coverage-delta`)
}

export function generateTestSuggestions(workspaceId: string, issueId: string) {
  return request<TestSuggestion[]>(`/api/workspaces/${workspaceId}/issues/${issueId}/test-suggestions`, {
    method: 'POST',
  })
}

export function getTestSuggestions(workspaceId: string, issueId: string) {
  return request<TestSuggestion[]>(`/api/workspaces/${workspaceId}/issues/${issueId}/test-suggestions`)
}

export function generatePatchCritique(workspaceId: string, runId: string) {
  return request<PatchCritique>(`/api/workspaces/${workspaceId}/runs/${runId}/critique`, {
    method: 'POST',
  })
}

export function getPatchCritique(workspaceId: string, runId: string) {
  return request<PatchCritique>(`/api/workspaces/${workspaceId}/runs/${runId}/critique`)
}

export function getRunImprovements(workspaceId: string, runId: string) {
  return request<ImprovementSuggestion[]>(`/api/workspaces/${workspaceId}/runs/${runId}/improvements`)
}

export function dismissImprovement(workspaceId: string, runId: string, suggestionId: string, reason?: string) {
  return request<ImprovementSuggestion>(`/api/workspaces/${workspaceId}/runs/${runId}/improvements/${suggestionId}/dismiss`, {
    method: 'POST',
    body: JSON.stringify({ reason }),
  })
}

export function configureIntegration(workspaceId: string, provider: string, settings: Record<string, unknown> = {}) {
  return request<IntegrationConfig>(`/api/workspaces/${workspaceId}/integrations?provider=${provider}`, {
    method: 'POST',
    body: JSON.stringify(settings),
  })
}

export function getIntegrationConfigs(workspaceId: string) {
  return request<IntegrationConfig[]>(`/api/workspaces/${workspaceId}/integrations`)
}

export function testIntegration(provider: string, settings: Record<string, unknown>) {
  return request<IntegrationTestResult>('/api/integrations/test', {
    method: 'POST',
    body: JSON.stringify({ provider, settings }),
  })
}

export function importGitHubIssues(workspaceId: string, repo: string, state = 'open') {
  return request<GitHubIssueImport[]>(`/api/workspaces/${workspaceId}/integrations/github/import?repo=${encodeURIComponent(repo)}&state=${state}`, {
    method: 'POST',
  })
}

export function createGitHubPR(workspaceId: string, payload: GitHubPRCreate) {
  return request<GitHubPRResult>(`/api/workspaces/${workspaceId}/integrations/github/pr`, {
    method: 'POST',
    body: JSON.stringify(payload),
  })
}

export function sendSlackNotification(workspaceId: string, event: string, message?: string) {
  const params = `event=${encodeURIComponent(event)}${message ? `&message=${encodeURIComponent(message)}` : ''}`
  return request<SlackNotification>(`/api/workspaces/${workspaceId}/integrations/slack/notify?${params}`, {
    method: 'POST',
  })
}

export function syncIssueToLinear(workspaceId: string, issueId: string) {
  return request<LinearIssueSync>(`/api/workspaces/${workspaceId}/integrations/linear/sync/${issueId}`, {
    method: 'POST',
  })
}

export function syncIssueToJira(workspaceId: string, issueId: string) {
  return request<JiraIssueSync>(`/api/workspaces/${workspaceId}/integrations/jira/sync/${issueId}`, {
    method: 'POST',
  })
}

export function promoteSignal(workspaceId: string, signalId: string, severity: string) {
  return request<WorkspaceSnapshot>(`/api/workspaces/${workspaceId}/signals/${signalId}/promote`, {
    method: 'POST',
    body: JSON.stringify({ severity }),
  })
}

export function exportWorkspace(workspaceId: string) {
  return request<unknown>(`/api/workspaces/${workspaceId}/export`)
}

export function listSources(workspaceId: string) {
  return request<SourceRecord[]>(`/api/workspaces/${workspaceId}/sources`)
}

export function readDrift(workspaceId: string) {
  return request<Record<string, number>>(`/api/workspaces/${workspaceId}/drift`)
}

export function readWorktree(workspaceId: string) {
  return request<WorktreeStatus>(`/api/workspaces/${workspaceId}/worktree`)
}

export function getSettings() {
  return request<AppSettings>('/api/settings')
}

export function updateSettings(settings: AppSettings) {
  return request<AppSettings>('/api/settings', {
    method: 'POST',
    body: JSON.stringify(settings),
  })
}

export function getCapabilities() {
  return request<LocalAgentCapabilities>('/api/agent/capabilities')
}

export function probeRuntime(workspaceId: string, payload: { runtime: string; model: string }) {
  return request<RuntimeProbeResult>(`/api/workspaces/${workspaceId}/agent/probe`, {
    method: 'POST',
    body: JSON.stringify(payload),
  })
}

export function openTerminal(workspaceId: string) {
  return request<{ terminal_id: string; pid: number }>('/api/terminal/open', {
    method: 'POST',
    body: JSON.stringify({ workspace_id: workspaceId, cols: 100, rows: 28 }),
  })
}

export function writeTerminal(terminalId: string, data: string) {
  return request<{ ok: boolean }>(`/api/terminal/${terminalId}/write`, {
    method: 'POST',
    body: JSON.stringify({ data }),
  })
}

export function readTerminal(terminalId: string, workspaceId: string, offset: number) {
  return request<{ offset: number; content: string; eof: boolean }>(
    `/api/terminal/${terminalId}/read?workspace_id=${workspaceId}&offset=${offset}`,
  )
}

export function closeTerminal(terminalId: string) {
  return request<{ ok: boolean }>(`/api/terminal/${terminalId}`, {
    method: 'DELETE',
  })
}
