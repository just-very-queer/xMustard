import type {
  ApiHealth,
  ActivityOverview,
  ActivityRecord,
  AppSettings,
  CostSummary,
  FixDraftSuggestion,
  FixRecord,
  FixRecordRequest,
  IssueCreateRequest,
  IssueDriftDetail,
  IssueContextPacket,
  IssueUpdateRequest,
  LocalAgentCapabilities,
  PlanApproveRequest,
  PlanRejectRequest,
  RuntimeProbeResult,
  RunMetrics,
  RunPlan,
  RunRecord,
  RunbookRecord,
  RunbookUpsertRequest,
  RunReviewRecord,
  RunReviewRequest,
  SavedIssueView,
  SavedIssueViewRequest,
  SourceRecord,
  TreeNode,
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

export function issueContext(workspaceId: string, issueId: string) {
  return request<IssueContextPacket>(`/api/workspaces/${workspaceId}/issues/${issueId}/context`)
}

export function issueWork(workspaceId: string, issueId: string, runbookId?: string) {
  const query = new URLSearchParams()
  if (runbookId) query.set('runbook_id', runbookId)
  return request<IssueContextPacket>(`/api/workspaces/${workspaceId}/issues/${issueId}/work?${query.toString()}`)
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

export function getWorkspaceCostSummary(workspaceId: string) {
  return request<CostSummary>(`/api/workspaces/${workspaceId}/costs`)
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
