import { startTransition, useDeferredValue, useEffect, useEffectEvent, useMemo, useRef, useState } from 'react'
import './index.css'
import {
  approvePlan,
  cancelRun,
  captureIssueContextReplay,
  compareIssueContextReplay,
  createIssue,
  closeTerminal,
  createSavedView,
  deleteBrowserDump,
  deleteSavedView,
  deleteRunbook,
  deleteThreatModel,
  deleteTicketContext,
  deleteVerificationProfile,
  exportWorkspace,
  acceptRunReview,
  dismissImprovement,
  getEvalReport,
  findDuplicates,
  generateGuidanceStarter,
  generatePlan,
  generatePatchCritique,
  generateTestSuggestions,
  getHealth,
  getWorkspaceGuidanceHealth,
  getCoverageDelta,
  getPlan,
  getPatchCritique,
  getRunInsights,
  getIssueQuality,
  getTestSuggestions,
  getWorkspaceCostSummary,
  getRunImprovements,
  getCapabilities,
  getSettings,
  issueWork,
  listIssueContextReplays,
  listVerificationProfileHistory,
  listVerificationProfileReports,
  listWorkspaceMetrics,
  listActivity,
  listEvalScenarios,
  listIssues,
  listRuns,
  listSavedViews,
  listSignals,
  listSources,
  listTree,
  listVerificationProfiles,
  listWorkspaceGuidance,
  listWorkspaces,
  loadWorkspace,
  openTerminal,
  probeRuntime,
  promoteSignal,
  queryAgent,
  readActivityOverview,
  readRepoMap,
  readDrift,
  readIssueDrift,
  readSnapshot,
  readRun,
  readRunLog,
  readTerminal,
  readWorktree,
  recordFix,
  replayEvalScenarios,
  rejectPlan,
  reviewRun,
  retryRun,
  scanWorkspace,
  scoreAllIssues,
  scoreIssueQuality,
  saveRunbook,
  saveBrowserDump,
  saveThreatModel,
  saveTicketContext,
  saveVerificationProfile,
  startRun,
  suggestFixDraft,
  triageIssue,
  updateIssue,
  updateSavedView,
  updateSettings,
  writeTerminal,
} from './lib/api'
import { AppTopbar } from './components/AppTopbar'
import { AgentDock } from './components/AgentDock'
import { DetailPane } from './components/DetailPane'
import { ExecutionPane } from './components/ExecutionPane'
import { QueuePane } from './components/QueuePane'
import type { QueuePreset } from './components/QueuePresetStrip'
import { WorkspaceSidebar } from './components/WorkspaceSidebar'
import type {
  ActivityRecord,
  ActivityOverview,
  AppSettings,
  BrowserDumpRecord,
  CostSummary,
  CoverageDelta,
  DiscoverySignal,
  DuplicateMatch,
  EvalScenarioRecord,
  EvalWorkspaceReport,
  ImprovementSuggestion,
  IssueContextReplayComparison,
  IssueContextReplayRecord,
  IssueDriftDetail,
  IssueContextPacket,
  IssueQueueFilters,
  IssueQualityScore,
  IssueRecord,
  LocalAgentCapabilities,
  PatchCritique,
  RepoMapSummary,
  RepoGuidanceRecord,
  RepoGuidanceHealth,
  RunPlan,
  RunRecord,
  RunSessionInsight,
  RuntimeProbeResult,
  RunMetrics,
  SavedIssueView,
  SourceRecord,
  TestSuggestion,
  ThreatModelRecord,
  TicketContextRecord,
  TreeNode,
  TriageSuggestion,
  VerificationProfileExecutionResult,
  VerificationProfileRecord,
  VerificationProfileReport,
  ViewMode,
  WorktreeStatus,
  WorkspaceRecord,
  WorkspaceSnapshot,
} from './lib/types'

const DEFAULT_PATH = '/Users/for_home/Developer/CoTitanMigration/Co_Titan'
const EMPTY_FILTERS: IssueQueueFilters = {
  query: '',
  severities: [],
  statuses: [],
  sources: [],
  labels: [],
  drift_only: false,
  needs_followup: null,
  review_ready_only: false,
}

type ActivityEntityTypeFilter = 'all' | ActivityRecord['entity_type']
type ActivityActorKindFilter = 'all' | ActivityRecord['actor']['kind']

function preferredModelForRuntime(runtime: 'codex' | 'opencode', settings: AppSettings | null) {
  if (!settings) return ''
  return runtime === 'codex' ? settings.codex_model ?? '' : settings.opencode_model ?? ''
}

function parseLabelDraft(value: string) {
  return value
    .split(',')
    .map((label) => label.trim())
    .filter(Boolean)
}

function parseLineDraft(value: string) {
  return value
    .split('\n')
    .map((line) => line.trim())
    .filter(Boolean)
}

function buildInstructionFromRunbook(mode: 'verify' | 'fix' | 'drift', packet: IssueContextPacket | null, issue: IssueRecord | null) {
  const heading =
    mode === 'verify'
      ? 'Verification pass only. Confirm current behavior, run targeted validation, and report remaining evidence gaps.'
      : mode === 'fix'
        ? 'Fix pass. Implement the smallest safe change, run verification, and summarize changed files and tests.'
        : 'Drift audit pass. Compare tracker state, code, and evidence. Report mismatches only.'
  const scope = issue ? `Focus issue: ${issue.bug_id} ${issue.title}` : 'No issue is currently selected.'
  const runbook = packet?.runbook ?? []
  if (!runbook.length) {
    return `${heading}\n${scope}`
  }
  return `${heading}\n${scope}\n\nRunbook:\n${runbook.map((step, index) => `${index + 1}. ${step}`).join('\n')}`
}

function buildInstructionFromSelectedRunbook(packet: IssueContextPacket | null, issue: IssueRecord | null, runbookId: string) {
  const runbook = packet?.available_runbooks.find((item) => item.runbook_id === runbookId)
  if (!runbook) return buildInstructionFromRunbook('fix', packet, issue)
  const scope = issue ? `Focus issue: ${issue.bug_id} ${issue.title}` : 'No issue is currently selected.'
  return `${runbook.name}\n${scope}\n\nRunbook:\n${runbook.template.trim()}`
}

function buildInstructionFromVerificationProfile(profile: VerificationProfileRecord | null, issue: IssueRecord | null) {
  if (!profile) return buildInstructionFromRunbook('verify', null, issue)
  const scope = issue ? `Focus issue: ${issue.bug_id} ${issue.title}` : 'No issue is currently selected.'
  const lines = [
    `Verification profile: ${profile.name}`,
    scope,
    '',
    'Run the saved verification workflow and report pass/fail, coverage movement, and any remaining evidence gaps.',
    `Test command: ${profile.test_command}`,
  ]
  if (profile.coverage_command) {
    lines.push(`Coverage command: ${profile.coverage_command}`)
  }
  if (profile.coverage_report_path) {
    lines.push(`Coverage report path: ${profile.coverage_report_path}`)
  }
  if (profile.source_paths.length) {
    lines.push(`Focus paths: ${profile.source_paths.join(', ')}`)
  }
  lines.push(`Retries: ${profile.retry_count}`)
  lines.push(`Max runtime: ${profile.max_runtime_seconds}s`)
  return lines.join('\n')
}

function App() {
  const [workspacePath, setWorkspacePath] = useState(DEFAULT_PATH)
  const [workspaces, setWorkspaces] = useState<WorkspaceRecord[]>([])
  const [snapshot, setSnapshot] = useState<WorkspaceSnapshot | null>(null)
  const [issueQueue, setIssueQueue] = useState<IssueRecord[]>([])
  const [signalQueue, setSignalQueue] = useState<DiscoverySignal[]>([])
  const [savedViews, setSavedViews] = useState<SavedIssueView[]>([])
  const [activeSavedViewId, setActiveSavedViewId] = useState<string | null>(null)
  const [activePresetId, setActivePresetId] = useState<string | null>(null)
  const [savedViewName, setSavedViewName] = useState('')
  const [issueFilters, setIssueFilters] = useState<IssueQueueFilters>(EMPTY_FILTERS)
  const [issueLabelFilterDraft, setIssueLabelFilterDraft] = useState('')
  const [signalQuery, setSignalQuery] = useState('')
  const [activityQuery, setActivityQuery] = useState('')
  const [activityActionFilter, setActivityActionFilter] = useState('all')
  const [activityEntityTypeFilter, setActivityEntityTypeFilter] = useState<ActivityEntityTypeFilter>('all')
  const [activityActorKindFilter, setActivityActorKindFilter] = useState<ActivityActorKindFilter>('all')
  const [selectedIssueId, setSelectedIssueId] = useState<string | null>(null)
  const [selectedSignalId, setSelectedSignalId] = useState<string | null>(null)
  const [selectedRunId, setSelectedRunId] = useState<string | null>(null)
  const [selectedSourceId, setSelectedSourceId] = useState<string | null>(null)
  const [selectedActivityId, setSelectedActivityId] = useState<string | null>(null)
  const [issueContextPacket, setIssueContextPacket] = useState<IssueContextPacket | null>(null)
  const [runs, setRuns] = useState<RunRecord[]>([])
  const [treeNodes, setTreeNodes] = useState<TreeNode[]>([])
  const [treePath, setTreePath] = useState('')
  const [activeView, setActiveView] = useState<ViewMode>('issues')
  const [runtime, setRuntime] = useState<'codex' | 'opencode'>('codex')
  const [model, setModel] = useState('')
  const [instruction, setInstruction] = useState('')
  const [planningMode, setPlanningMode] = useState(false)
  const [queryPrompt, setQueryPrompt] = useState('Summarize the current workspace focus and next actions in 5 bullets.')
  const [executionOpen, setExecutionOpen] = useState(false)
  const [selectedRunbookId, setSelectedRunbookId] = useState('fix')
  const [runbookNameDraft, setRunbookNameDraft] = useState('')
  const [runbookDescriptionDraft, setRunbookDescriptionDraft] = useState('')
  const [runbookTemplateDraft, setRunbookTemplateDraft] = useState('')
  const [verificationProfiles, setVerificationProfiles] = useState<VerificationProfileRecord[]>([])
  const [selectedVerificationProfileId, setSelectedVerificationProfileId] = useState('')
  const [verificationProfileNameDraft, setVerificationProfileNameDraft] = useState('')
  const [verificationProfileDescriptionDraft, setVerificationProfileDescriptionDraft] = useState('')
  const [verificationProfileTestCommandDraft, setVerificationProfileTestCommandDraft] = useState('')
  const [verificationProfileCoverageCommandDraft, setVerificationProfileCoverageCommandDraft] = useState('')
  const [verificationProfileCoveragePathDraft, setVerificationProfileCoveragePathDraft] = useState('')
  const [verificationProfileCoverageFormatDraft, setVerificationProfileCoverageFormatDraft] = useState<VerificationProfileRecord['coverage_format']>('unknown')
  const [verificationProfileRuntimeDraft, setVerificationProfileRuntimeDraft] = useState('30')
  const [verificationProfileRetryDraft, setVerificationProfileRetryDraft] = useState('1')
  const [verificationProfileSourcePathsDraft, setVerificationProfileSourcePathsDraft] = useState('')
  const [verificationProfileChecklistDraft, setVerificationProfileChecklistDraft] = useState('')
  const [verificationProfileHistory, setVerificationProfileHistory] = useState<VerificationProfileExecutionResult[]>([])
  const [verificationProfileReports, setVerificationProfileReports] = useState<VerificationProfileReport[]>([])
  const [evalScenarios, setEvalScenarios] = useState<EvalScenarioRecord[]>([])
  const [evalReport, setEvalReport] = useState<EvalWorkspaceReport | null>(null)
  const [selectedTicketContextId, setSelectedTicketContextId] = useState('')
  const [ticketContextProviderDraft, setTicketContextProviderDraft] = useState<TicketContextRecord['provider']>('manual')
  const [ticketContextExternalIdDraft, setTicketContextExternalIdDraft] = useState('')
  const [ticketContextTitleDraft, setTicketContextTitleDraft] = useState('')
  const [ticketContextSummaryDraft, setTicketContextSummaryDraft] = useState('')
  const [ticketContextCriteriaDraft, setTicketContextCriteriaDraft] = useState('')
  const [ticketContextLinksDraft, setTicketContextLinksDraft] = useState('')
  const [ticketContextLabelsDraft, setTicketContextLabelsDraft] = useState('')
  const [ticketContextStatusDraft, setTicketContextStatusDraft] = useState('')
  const [ticketContextSourceExcerptDraft, setTicketContextSourceExcerptDraft] = useState('')
  const [selectedThreatModelId, setSelectedThreatModelId] = useState('')
  const [threatModelTitleDraft, setThreatModelTitleDraft] = useState('')
  const [threatModelMethodologyDraft, setThreatModelMethodologyDraft] = useState<ThreatModelRecord['methodology']>('manual')
  const [threatModelSummaryDraft, setThreatModelSummaryDraft] = useState('')
  const [threatModelAssetsDraft, setThreatModelAssetsDraft] = useState('')
  const [threatModelEntryPointsDraft, setThreatModelEntryPointsDraft] = useState('')
  const [threatModelTrustBoundariesDraft, setThreatModelTrustBoundariesDraft] = useState('')
  const [threatModelAbuseCasesDraft, setThreatModelAbuseCasesDraft] = useState('')
  const [threatModelMitigationsDraft, setThreatModelMitigationsDraft] = useState('')
  const [threatModelReferencesDraft, setThreatModelReferencesDraft] = useState('')
  const [threatModelStatusDraft, setThreatModelStatusDraft] = useState<ThreatModelRecord['status']>('draft')
  const [selectedBrowserDumpId, setSelectedBrowserDumpId] = useState('')
  const [browserDumpSourceDraft, setBrowserDumpSourceDraft] = useState<BrowserDumpRecord['source']>('manual')
  const [browserDumpLabelDraft, setBrowserDumpLabelDraft] = useState('')
  const [browserDumpPageUrlDraft, setBrowserDumpPageUrlDraft] = useState('')
  const [browserDumpPageTitleDraft, setBrowserDumpPageTitleDraft] = useState('')
  const [browserDumpSummaryDraft, setBrowserDumpSummaryDraft] = useState('')
  const [browserDumpDomSnapshotDraft, setBrowserDumpDomSnapshotDraft] = useState('')
  const [browserDumpConsoleDraft, setBrowserDumpConsoleDraft] = useState('')
  const [browserDumpNetworkDraft, setBrowserDumpNetworkDraft] = useState('')
  const [browserDumpScreenshotPathDraft, setBrowserDumpScreenshotPathDraft] = useState('')
  const [browserDumpNotesDraft, setBrowserDumpNotesDraft] = useState('')
  const [issueContextReplays, setIssueContextReplays] = useState<IssueContextReplayRecord[]>([])
  const [contextReplayLabelDraft, setContextReplayLabelDraft] = useState('')
  const [selectedContextReplayId, setSelectedContextReplayId] = useState('')
  const [contextReplayComparison, setContextReplayComparison] = useState<IssueContextReplayComparison | null>(null)
  const [logContent, setLogContent] = useState('')
  const [selectedRunPlan, setSelectedRunPlan] = useState<RunPlan | null>(null)
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const logTimerRef = useRef<number | null>(null)
  const runRefreshTimerRef = useRef<number | null>(null)
  const logOffsetRef = useRef(0)
  const terminalTimerRef = useRef<number | null>(null)
  const terminalOffsetRef = useRef(0)
  const [settings, setSettings] = useState<AppSettings | null>(null)
  const [capabilities, setCapabilities] = useState<LocalAgentCapabilities | null>(null)
  const [terminalId, setTerminalId] = useState<string | null>(null)
  const [terminalInput, setTerminalInput] = useState('pwd')
  const [terminalOutput, setTerminalOutput] = useState('')
  const [runtimeProbe, setRuntimeProbe] = useState<RuntimeProbeResult | null>(null)
  const [runtimeProbeLoading, setRuntimeProbeLoading] = useState(false)
  const [backendHealthy, setBackendHealthy] = useState<boolean | null>(null)
  const [sources, setSources] = useState<SourceRecord[]>([])
  const [workspaceGuidance, setWorkspaceGuidance] = useState<RepoGuidanceRecord[]>([])
  const [guidanceHealth, setGuidanceHealth] = useState<RepoGuidanceHealth | null>(null)
  const [repoMap, setRepoMap] = useState<RepoMapSummary | null>(null)
  const [activity, setActivity] = useState<ActivityRecord[]>([])
  const [activityOverview, setActivityOverview] = useState<ActivityOverview | null>(null)
  const [worktree, setWorktree] = useState<WorktreeStatus | null>(null)
  const [driftSummary, setDriftSummary] = useState<Record<string, number>>({})
  const [issueDrift, setIssueDrift] = useState<IssueDriftDetail | null>(null)
  const [issueQuality, setIssueQuality] = useState<IssueQualityScore | null>(null)
  const [issueQualityById, setIssueQualityById] = useState<Record<string, IssueQualityScore>>({})
  const [duplicateMatches, setDuplicateMatches] = useState<DuplicateMatch[]>([])
  const [triageSuggestion, setTriageSuggestion] = useState<TriageSuggestion | null>(null)
  const [costSummary, setCostSummary] = useState<CostSummary | null>(null)
  const [runMetricsById, setRunMetricsById] = useState<Record<string, RunMetrics>>({})
  const [coverageDelta, setCoverageDelta] = useState<CoverageDelta | null>(null)
  const [testSuggestions, setTestSuggestions] = useState<TestSuggestion[]>([])
  const [patchCritique, setPatchCritique] = useState<PatchCritique | null>(null)
  const [runImprovements, setRunImprovements] = useState<ImprovementSuggestion[]>([])
  const [runInsight, setRunInsight] = useState<RunSessionInsight | null>(null)
  const [issueStatusDraft, setIssueStatusDraft] = useState('open')
  const [issueSeverityDraft, setIssueSeverityDraft] = useState('P2')
  const [issueDocStatusDraft, setIssueDocStatusDraft] = useState('open')
  const [issueCodeStatusDraft, setIssueCodeStatusDraft] = useState('unknown')
  const [issueLabelsDraft, setIssueLabelsDraft] = useState('')
  const [issueNotesDraft, setIssueNotesDraft] = useState('')
  const [issueFollowupDraft, setIssueFollowupDraft] = useState(false)
  const [newIssueTitleDraft, setNewIssueTitleDraft] = useState('')
  const [newIssueSeverityDraft, setNewIssueSeverityDraft] = useState('P2')
  const [newIssueSummaryDraft, setNewIssueSummaryDraft] = useState('')
  const [newIssueLabelsDraft, setNewIssueLabelsDraft] = useState('')
  const [fixSummaryDraft, setFixSummaryDraft] = useState('')
  const [fixChangedFilesDraft, setFixChangedFilesDraft] = useState('')
  const [fixTestsDraft, setFixTestsDraft] = useState('')
  const [fixHowDraft, setFixHowDraft] = useState('')
  const [fixIssueStatusDraft, setFixIssueStatusDraft] = useState('verification')
  const [fixRunIdDraft, setFixRunIdDraft] = useState<string | null>(null)
  const deferredActivityQuery = useDeferredValue(activityQuery)

  const runtimeModels = useMemo(
    () =>
      capabilities?.runtimes.find((entry) => entry.runtime === runtime)?.models ??
      snapshot?.runtimes.find((entry) => entry.runtime === runtime)?.models ??
      [],
    [capabilities, snapshot, runtime],
  )
  const reviewRunIds = useMemo(
    () => new Set((snapshot?.issues ?? []).flatMap((issue) => issue.review_ready_runs)),
    [snapshot],
  )
  const reviewRuns = useMemo(
    () => runs.filter((run) => reviewRunIds.has(run.run_id)),
    [reviewRunIds, runs],
  )
  const selectedIssue = useMemo(
    () =>
      snapshot?.issues.find((issue) => issue.bug_id === selectedIssueId) ??
      issueQueue.find((issue) => issue.bug_id === selectedIssueId) ??
      issueQueue[0] ??
      null,
    [snapshot, issueQueue, selectedIssueId],
  )
  const selectedSignal = useMemo(
    () =>
      signalQueue.find((signal) => signal.signal_id === selectedSignalId) ??
      snapshot?.signals.find((signal) => signal.signal_id === selectedSignalId) ??
      signalQueue[0] ??
      null,
    [signalQueue, snapshot, selectedSignalId],
  )
  const selectedRun = useMemo(
    () => {
      if (selectedRunId) {
        return runs.find((run) => run.run_id === selectedRunId) ?? null
      }
      if (activeView === 'runs') return runs[0] ?? null
      if (activeView === 'review') return reviewRuns[0] ?? null
      return null
    },
    [activeView, reviewRuns, runs, selectedRunId],
  )
  const selectedSource = useMemo(
    () => sources.find((source) => source.source_id === selectedSourceId) ?? sources[0] ?? null,
    [sources, selectedSourceId],
  )
  const selectedSavedView = useMemo(
    () => savedViews.find((view) => view.view_id === activeSavedViewId) ?? null,
    [savedViews, activeSavedViewId],
  )
  const filteredActivity = useMemo(() => {
    const normalized = deferredActivityQuery.trim().toLowerCase()
    return activity.filter((item) => {
      if (activityActionFilter !== 'all' && item.action !== activityActionFilter) return false
      if (activityEntityTypeFilter !== 'all' && item.entity_type !== activityEntityTypeFilter) return false
      if (activityActorKindFilter !== 'all' && item.actor.kind !== activityActorKindFilter) return false
      if (!normalized) return true
      return (
        item.summary.toLowerCase().includes(normalized) ||
        item.action.toLowerCase().includes(normalized) ||
        item.actor.name.toLowerCase().includes(normalized) ||
        item.actor.label.toLowerCase().includes(normalized) ||
        (item.issue_id ?? '').toLowerCase().includes(normalized) ||
        (item.run_id ?? '').toLowerCase().includes(normalized)
      )
    })
  }, [activity, activityActionFilter, activityActorKindFilter, activityEntityTypeFilter, deferredActivityQuery])
  const selectedActivity = useMemo(
    () => filteredActivity.find((item) => item.activity_id === selectedActivityId) ?? filteredActivity[0] ?? null,
    [filteredActivity, selectedActivityId],
  )
  const workspaceActivity = useMemo(() => filteredActivity.slice(0, 20), [filteredActivity])
  const issueActivity = useMemo(
    () => (selectedIssue ? activity.filter((item) => item.issue_id === selectedIssue.bug_id).slice(0, 12) : []),
    [activity, selectedIssue],
  )
  const runActivity = useMemo(
    () => (selectedRun ? activity.filter((item) => item.run_id === selectedRun.run_id).slice(0, 12) : []),
    [activity, selectedRun],
  )
  const queuePresets = useMemo<QueuePreset[]>(() => {
    const issues = snapshot?.issues ?? []
    const count = (predicate: (issue: IssueRecord) => boolean) => issues.filter(predicate).length
    return [
      {
        presetId: 'critical-open',
        name: 'Critical Open',
        description: 'P0 and P1 work still not moved to verification',
        count: count((issue) => ['P0', 'P1'].includes(issue.severity) && ['open', 'triaged', 'investigating', 'in_progress'].includes(issue.issue_status)),
        mode: 'issues',
        filters: {
          ...EMPTY_FILTERS,
          severities: ['P0', 'P1'],
          statuses: ['open', 'triaged', 'investigating', 'in_progress'],
        },
      },
      {
        presetId: 'verification',
        name: 'Verification Queue',
        description: 'Bugs waiting for proof, tests, or final closeout',
        count: count((issue) => issue.issue_status === 'verification'),
        mode: 'issues',
        filters: {
          ...EMPTY_FILTERS,
          statuses: ['verification'],
        },
      },
      {
        presetId: 'promoted-signals',
        name: 'Promoted Signals',
        description: 'Discovery issues that still need operator triage',
        count: count((issue) => issue.source === 'signal' && ['open', 'triaged', 'investigating'].includes(issue.issue_status)),
        mode: 'issues',
        filters: {
          ...EMPTY_FILTERS,
          sources: ['signal'],
          statuses: ['open', 'triaged', 'investigating'],
        },
      },
      {
        presetId: 'agent-review',
        name: 'Agent Review',
        description: 'Completed agent runs that still need a recorded fix or verification',
        count: count((issue) => issue.review_ready_count > 0),
        mode: 'review',
        filters: {
          ...EMPTY_FILTERS,
          review_ready_only: true,
        },
      },
      {
        presetId: 'followup-drift',
        name: 'Follow-Up Drift',
        description: 'Drifting issues that still require manual action',
        count: count((issue) => issue.drift_flags.length > 0 && issue.needs_followup),
        mode: 'drift',
        filters: {
          ...EMPTY_FILTERS,
          drift_only: true,
          needs_followup: true,
        },
      },
      {
        presetId: 'ledger-drift',
        name: 'Ledger Drift Audit',
        description: 'Doc-driven bugs where tracker evidence has started to drift',
        count: count((issue) => issue.drift_flags.length > 0 && ['ledger', 'verdict'].includes(issue.source)),
        mode: 'drift',
        filters: {
          ...EMPTY_FILTERS,
          drift_only: true,
          sources: ['ledger', 'verdict'],
        },
      },
    ]
  }, [snapshot])

  const activeLiveRun = useMemo(
    () => runs.find((run) => run.status === 'planning' || run.status === 'queued' || run.status === 'running') ?? null,
    [runs],
  )
  const workspaceId = snapshot?.workspace.workspace_id ?? null
  const selectedIssueBugId = selectedIssue?.bug_id ?? null
  const selectedIssueUpdatedAt = selectedIssue?.updated_at ?? null
  const selectedRunRecordId = selectedRun?.run_id ?? null
  const selectedRunStatus = selectedRun?.status ?? null
  const selectedRunPlanRecord = selectedRun?.plan ?? null
  const activeLiveRunId = activeLiveRun?.run_id ?? null
  const selectedRunMetrics = useMemo(
    () => (selectedRun ? runMetricsById[selectedRun.run_id] ?? null : null),
    [runMetricsById, selectedRun],
  )
  const selectedVerificationProfile = useMemo(
    () => verificationProfiles.find((item) => item.profile_id === selectedVerificationProfileId) ?? null,
    [verificationProfiles, selectedVerificationProfileId],
  )
  const selectedTicketContext = useMemo(
    () => issueContextPacket?.ticket_contexts.find((item) => item.context_id === selectedTicketContextId) ?? null,
    [issueContextPacket?.ticket_contexts, selectedTicketContextId],
  )
  const selectedThreatModel = useMemo(
    () => issueContextPacket?.threat_models.find((item) => item.threat_model_id === selectedThreatModelId) ?? null,
    [issueContextPacket?.threat_models, selectedThreatModelId],
  )
  const selectedBrowserDump = useMemo(
    () => issueContextPacket?.browser_dumps.find((item) => item.dump_id === selectedBrowserDumpId) ?? null,
    [issueContextPacket?.browser_dumps, selectedBrowserDumpId],
  )

  useEffect(() => {
    setSelectedContextReplayId('')
    setContextReplayComparison(null)
  }, [selectedIssueBugId, workspaceId])

  async function refreshHealth() {
    try {
      const result = await getHealth()
      setBackendHealthy(result.status === 'ok')
    } catch {
      setBackendHealthy(false)
    }
  }

  async function refreshIssueQueue(nextFilters = issueFilters, nextView = activeView) {
    if (!snapshot?.workspace.workspace_id) return
    const effectiveDrift = nextView === 'drift' || nextFilters.drift_only
    const issues = await listIssues(snapshot.workspace.workspace_id, {
      q: nextFilters.query,
      severity: nextFilters.severities,
      issue_status: nextFilters.statuses,
      source: nextFilters.sources,
      label: nextFilters.labels,
      drift_only: effectiveDrift,
      needs_followup: nextFilters.needs_followup,
      review_ready_only: nextFilters.review_ready_only,
    })
    setIssueQueue(issues)
  }

  async function refreshSignalQueue(nextQuery = signalQuery) {
    if (!snapshot?.workspace.workspace_id) return
    setSignalQueue(await listSignals(snapshot.workspace.workspace_id, { q: nextQuery }))
  }

  async function refreshActivityData(workspaceId: string) {
    const [nextActivity, nextOverview] = await Promise.all([listActivity(workspaceId, { limit: 120 }), readActivityOverview(workspaceId, 120)])
    startTransition(() => {
      setActivity(nextActivity)
      setActivityOverview(nextOverview)
    })
  }

  async function refreshCostData(workspaceId: string) {
    const [nextCostSummary, metrics] = await Promise.all([
      getWorkspaceCostSummary(workspaceId),
      listWorkspaceMetrics(workspaceId),
    ])
    startTransition(() => {
      setCostSummary(nextCostSummary)
      setRunMetricsById(Object.fromEntries(metrics.map((metric) => [metric.run_id, metric])))
    })
  }

  async function refreshWorkspaceData(workspaceId: string) {
    const [nextRuns, nextSources, nextGuidance, nextGuidanceHealth, nextRepoMap, nextVerificationProfiles, nextDriftSummary, nextViews, nextActivity, nextOverview, nextWorktree, nextCostSummary, metrics] = await Promise.all([
      listRuns(workspaceId),
      listSources(workspaceId),
      listWorkspaceGuidance(workspaceId),
      getWorkspaceGuidanceHealth(workspaceId),
      readRepoMap(workspaceId),
      listVerificationProfiles(workspaceId),
      readDrift(workspaceId),
      listSavedViews(workspaceId),
      listActivity(workspaceId, { limit: 120 }),
      readActivityOverview(workspaceId, 120),
      readWorktree(workspaceId),
      getWorkspaceCostSummary(workspaceId),
      listWorkspaceMetrics(workspaceId),
    ])
    startTransition(() => {
      setRuns(nextRuns)
      setSources(nextSources)
      setWorkspaceGuidance(nextGuidance)
      setGuidanceHealth(nextGuidanceHealth)
      setRepoMap(nextRepoMap)
      setVerificationProfiles(nextVerificationProfiles)
      setDriftSummary(nextDriftSummary)
      setSavedViews(nextViews)
      setActivity(nextActivity)
      setActivityOverview(nextOverview)
      setWorktree(nextWorktree)
      setCostSummary(nextCostSummary)
      setRunMetricsById(Object.fromEntries(metrics.map((metric) => [metric.run_id, metric])))
    })
  }

  function setIssueFiltersFromView(view: SavedIssueView) {
    setIssueFilters({
      query: view.query,
      severities: view.severities,
      statuses: view.statuses,
      sources: view.sources,
      labels: view.labels,
      drift_only: view.drift_only,
      needs_followup: view.needs_followup ?? null,
      review_ready_only: view.review_ready_only ?? false,
    })
    setIssueLabelFilterDraft(view.labels.join(', '))
    setSavedViewName(view.name)
    setActiveSavedViewId(view.view_id)
    setActivePresetId(null)
    setActiveView(view.drift_only ? 'drift' : 'issues')
  }

  function clearIssueQueueState(nextView: ViewMode = 'issues') {
    setActiveSavedViewId(null)
    setActivePresetId(null)
    setSavedViewName('')
    setIssueFilters(EMPTY_FILTERS)
    setIssueLabelFilterDraft('')
    setActiveView(nextView)
  }

  function applyQueuePreset(preset: QueuePreset) {
    setIssueFilters(preset.filters)
    setIssueLabelFilterDraft(preset.filters.labels.join(', '))
    setActivePresetId(preset.presetId)
    setActiveSavedViewId(null)
    setSavedViewName('')
    setActiveView(preset.mode)
  }

  const refreshIssueQueueEffect = useEffectEvent(async () => {
    await refreshIssueQueue()
  })

  const refreshSignalQueueEffect = useEffectEvent(async () => {
    await refreshSignalQueue()
  })

  useEffect(() => {
    void (async () => {
      try {
        const [workspaceList, nextSettings, nextCapabilities] = await Promise.all([
          listWorkspaces(),
          getSettings(),
          getCapabilities(),
          refreshHealth(),
        ])
        setWorkspaces(workspaceList)
        setSettings(nextSettings)
        setCapabilities(nextCapabilities)
        setRuntime(nextSettings.local_agent_type)
      } catch {
        // Backend may not be running yet.
      }
    })()
  }, [])

  useEffect(() => {
    if (!settings?.local_agent_type) return
    if (settings.local_agent_type !== runtime) {
      setRuntime(settings.local_agent_type)
    }
  }, [settings?.local_agent_type, runtime])

  useEffect(() => {
    if (!runtimeModels.length) return
    if (!runtimeModels.some((entry) => entry.id === model)) {
      const preferred = preferredModelForRuntime(runtime, settings)
      const nextModel = runtimeModels.find((entry) => entry.id === preferred)?.id ?? runtimeModels[0].id
      setModel(nextModel)
    }
  }, [runtimeModels, model, runtime, settings])

  useEffect(() => {
    setRuntimeProbe(null)
  }, [runtime, model, snapshot?.workspace.workspace_id])

  useEffect(() => {
    if (!workspaceId) return
    void refreshWorkspaceData(workspaceId).catch((nextError) =>
      setError(nextError instanceof Error ? nextError.message : String(nextError)),
    )
  }, [workspaceId, snapshot?.generated_at])

  useEffect(() => {
    if (!workspaceId) return
    if (activeView !== 'tree') return
    void listTree(workspaceId, treePath)
      .then(setTreeNodes)
      .catch((nextError) => setError(nextError instanceof Error ? nextError.message : String(nextError)))
  }, [workspaceId, treePath, activeView])

  useEffect(() => {
    if (!workspaceId) return
    void refreshIssueQueueEffect().catch((nextError) =>
      setError(nextError instanceof Error ? nextError.message : String(nextError)),
    )
  }, [workspaceId, snapshot?.generated_at, issueFilters, activeView])

  useEffect(() => {
    if (!workspaceId) return
    if (activeView !== 'signals') return
    void refreshSignalQueueEffect().catch((nextError) =>
      setError(nextError instanceof Error ? nextError.message : String(nextError)),
    )
  }, [workspaceId, snapshot?.generated_at, signalQuery, activeView])

  useEffect(() => {
    setSelectedIssueId((current) => {
      if (!issueQueue.length) return null
      return issueQueue.some((issue) => issue.bug_id === current) ? current : issueQueue[0].bug_id
    })
  }, [issueQueue])

  useEffect(() => {
    setSelectedSignalId((current) => {
      if (!signalQueue.length) return null
      return signalQueue.some((signal) => signal.signal_id === current) ? current : signalQueue[0].signal_id
    })
  }, [signalQueue])

  useEffect(() => {
    setSelectedRunId((current) => {
      if (current && runs.some((run) => run.run_id === current)) return current
      if (activeView === 'runs') return runs[0]?.run_id ?? null
      if (activeView === 'review') return reviewRuns[0]?.run_id ?? null
      return current
    })
  }, [activeView, reviewRuns, runs])

  useEffect(() => {
    setSelectedSourceId((current) =>
      sources.some((source) => source.source_id === current) ? current : sources[0]?.source_id ?? null,
    )
  }, [sources])

  useEffect(() => {
    setSelectedActivityId((current) =>
      filteredActivity.some((item) => item.activity_id === current) ? current : filteredActivity[0]?.activity_id ?? null,
    )
  }, [filteredActivity])

  useEffect(() => {
    if (!workspaceId || !selectedIssueBugId) {
      setIssueContextPacket(null)
      setIssueDrift(null)
      return
    }

    let cancelled = false
    void issueWork(workspaceId, selectedIssueBugId, selectedRunbookId || undefined)
      .then((packet) => {
        if (!cancelled) {
          setIssueContextPacket(packet)
        }
      })
      .catch((nextError) => {
        if (!cancelled) {
          setError(nextError instanceof Error ? nextError.message : String(nextError))
        }
      })
    void readIssueDrift(workspaceId, selectedIssueBugId)
      .then((drift) => {
        if (!cancelled) {
          setIssueDrift(drift)
        }
      })
      .catch((nextError) => {
        if (!cancelled) {
          setError(nextError instanceof Error ? nextError.message : String(nextError))
        }
      })

    return () => {
      cancelled = true
    }
  }, [workspaceId, selectedIssueBugId, selectedRunbookId])

  useEffect(() => {
    if (!workspaceId || !selectedIssueBugId) {
      setIssueQuality(null)
      setDuplicateMatches([])
      setTriageSuggestion(null)
      setCoverageDelta(null)
      setTestSuggestions([])
      setIssueContextReplays([])
      setVerificationProfileHistory([])
      setVerificationProfileReports([])
      setEvalScenarios([])
      setEvalReport(null)
      return
    }

    let cancelled = false
    void (async () => {
      try {
        const [
          qualityResult,
          duplicatesResult,
          triageResult,
          coverageResult,
          testSuggestionsResult,
          contextReplaysResult,
          verificationHistoryResult,
          verificationReportsResult,
          evalScenariosResult,
          evalReportResult,
        ] = await Promise.allSettled([
          getIssueQuality(workspaceId, selectedIssueBugId),
          findDuplicates(workspaceId, selectedIssueBugId),
          triageIssue(workspaceId, selectedIssueBugId),
          getCoverageDelta(workspaceId, selectedIssueBugId),
          getTestSuggestions(workspaceId, selectedIssueBugId),
          listIssueContextReplays(workspaceId, selectedIssueBugId),
          listVerificationProfileHistory(workspaceId, { issue_id: selectedIssueBugId }),
          listVerificationProfileReports(workspaceId, { issue_id: selectedIssueBugId }),
          listEvalScenarios(workspaceId, { issue_id: selectedIssueBugId }),
          getEvalReport(workspaceId),
        ])
        if (cancelled) return
        if (qualityResult.status === 'fulfilled') {
          setIssueQuality(qualityResult.value)
          setIssueQualityById((current) => ({ ...current, [qualityResult.value.issue_id]: qualityResult.value }))
        } else {
          setIssueQuality(null)
        }
        if (duplicatesResult.status === 'fulfilled') {
          setDuplicateMatches(duplicatesResult.value)
        } else {
          setDuplicateMatches([])
        }
        if (triageResult.status === 'fulfilled') {
          setTriageSuggestion(triageResult.value)
        } else {
          setTriageSuggestion(null)
        }
        if (coverageResult.status === 'fulfilled') {
          setCoverageDelta(coverageResult.value)
        } else {
          setCoverageDelta(null)
        }
        if (testSuggestionsResult.status === 'fulfilled') {
          setTestSuggestions(testSuggestionsResult.value)
        } else {
          setTestSuggestions([])
        }
        if (contextReplaysResult.status === 'fulfilled') {
          setIssueContextReplays(contextReplaysResult.value)
        } else {
          setIssueContextReplays([])
        }
        if (verificationHistoryResult.status === 'fulfilled') {
          setVerificationProfileHistory(verificationHistoryResult.value)
        } else {
          setVerificationProfileHistory([])
        }
        if (verificationReportsResult.status === 'fulfilled') {
          setVerificationProfileReports(verificationReportsResult.value)
        } else {
          setVerificationProfileReports([])
        }
        if (evalScenariosResult.status === 'fulfilled') {
          setEvalScenarios(evalScenariosResult.value)
        } else {
          setEvalScenarios([])
        }
        if (evalReportResult.status === 'fulfilled') {
          setEvalReport(evalReportResult.value)
        } else {
          setEvalReport(null)
        }
      } catch (nextError) {
        if (!cancelled) {
          setError(nextError instanceof Error ? nextError.message : String(nextError))
        }
      }
    })()

    return () => {
      cancelled = true
    }
  }, [workspaceId, selectedIssueBugId, selectedIssueUpdatedAt])

  useEffect(() => {
    if (!workspaceId || !selectedRunRecordId || !selectedRunStatus) {
      setPatchCritique(null)
      setRunImprovements([])
      setRunInsight(null)
      return
    }
    if (!['completed', 'failed', 'cancelled', 'running', 'queued', 'planning'].includes(selectedRunStatus)) {
      setPatchCritique(null)
      setRunImprovements([])
      setRunInsight(null)
      return
    }

    let cancelled = false
    void (async () => {
      const [critiqueResult, improvementsResult, insightResult] = await Promise.allSettled([
        getPatchCritique(workspaceId, selectedRunRecordId),
        getRunImprovements(workspaceId, selectedRunRecordId),
        getRunInsights(workspaceId, selectedRunRecordId),
      ])
      if (cancelled) return
      if (critiqueResult.status === 'fulfilled') {
        setPatchCritique(critiqueResult.value)
      } else {
        setPatchCritique(null)
      }
      if (improvementsResult.status === 'fulfilled') {
        setRunImprovements(improvementsResult.value)
      } else {
        setRunImprovements([])
      }
      if (insightResult.status === 'fulfilled') {
        setRunInsight(insightResult.value)
      } else {
        setRunInsight(null)
      }
    })().catch((nextError) => {
      if (!cancelled) {
        setError(nextError instanceof Error ? nextError.message : String(nextError))
      }
    })

    return () => {
      cancelled = true
    }
  }, [workspaceId, selectedRunRecordId, selectedRunStatus])

  useEffect(() => {
    if (!workspaceId) {
      setIssueQualityById({})
      return
    }

    let cancelled = false
    void scoreAllIssues(workspaceId)
      .then((scores) => {
        if (cancelled) return
        setIssueQualityById(
          Object.fromEntries(scores.map((score) => [score.issue_id, score])),
        )
      })
      .catch((nextError) => {
        if (!cancelled) {
          setError(nextError instanceof Error ? nextError.message : String(nextError))
        }
      })

    return () => {
      cancelled = true
    }
  }, [workspaceId, snapshot?.generated_at])

  useEffect(() => {
    if (!selectedIssue) return
    setIssueSeverityDraft(selectedIssue.severity)
    setIssueStatusDraft(selectedIssue.issue_status)
    setIssueDocStatusDraft(selectedIssue.doc_status)
    setIssueCodeStatusDraft(selectedIssue.code_status)
    setIssueLabelsDraft(selectedIssue.labels.join(', '))
    setIssueNotesDraft(selectedIssue.notes ?? '')
    setIssueFollowupDraft(selectedIssue.needs_followup)
    setFixSummaryDraft('')
    setFixChangedFilesDraft('')
    setFixTestsDraft('')
    setFixHowDraft('')
    setFixIssueStatusDraft(selectedIssue.issue_status === 'resolved' ? 'resolved' : 'verification')
    setFixRunIdDraft(null)
  }, [selectedIssue])

  useEffect(() => {
    const runbooks = issueContextPacket?.available_runbooks ?? []
    setSelectedRunbookId((current) => {
      if (runbooks.some((item) => item.runbook_id === current)) return current
      return runbooks[0]?.runbook_id ?? 'fix'
    })
  }, [issueContextPacket?.available_runbooks])

  useEffect(() => {
    const selectedRunbook = issueContextPacket?.available_runbooks.find((item) => item.runbook_id === selectedRunbookId) ?? null
    if (!selectedRunbook) {
      setRunbookNameDraft('')
      setRunbookDescriptionDraft('')
      setRunbookTemplateDraft('')
      return
    }
    setRunbookNameDraft(selectedRunbook.name)
    setRunbookDescriptionDraft(selectedRunbook.description)
    setRunbookTemplateDraft(selectedRunbook.template)
  }, [issueContextPacket?.available_runbooks, selectedRunbookId])

  useEffect(() => {
    setSelectedVerificationProfileId((current) =>
      verificationProfiles.some((item) => item.profile_id === current) ? current : verificationProfiles[0]?.profile_id ?? '',
    )
  }, [verificationProfiles])

  useEffect(() => {
    if (!selectedVerificationProfile) {
      setVerificationProfileNameDraft('')
      setVerificationProfileDescriptionDraft('')
      setVerificationProfileTestCommandDraft('')
      setVerificationProfileCoverageCommandDraft('')
      setVerificationProfileCoveragePathDraft('')
      setVerificationProfileCoverageFormatDraft('unknown')
      setVerificationProfileRuntimeDraft('30')
      setVerificationProfileRetryDraft('1')
      setVerificationProfileSourcePathsDraft('')
      setVerificationProfileChecklistDraft('')
      return
    }
    setVerificationProfileNameDraft(selectedVerificationProfile.name)
    setVerificationProfileDescriptionDraft(selectedVerificationProfile.description)
    setVerificationProfileTestCommandDraft(selectedVerificationProfile.test_command)
    setVerificationProfileCoverageCommandDraft(selectedVerificationProfile.coverage_command ?? '')
    setVerificationProfileCoveragePathDraft(selectedVerificationProfile.coverage_report_path ?? '')
    setVerificationProfileCoverageFormatDraft(selectedVerificationProfile.coverage_format)
    setVerificationProfileRuntimeDraft(String(selectedVerificationProfile.max_runtime_seconds))
    setVerificationProfileRetryDraft(String(selectedVerificationProfile.retry_count))
    setVerificationProfileSourcePathsDraft(selectedVerificationProfile.source_paths.join(', '))
    setVerificationProfileChecklistDraft(selectedVerificationProfile.checklist_items.join('\n'))
  }, [selectedVerificationProfile])

  useEffect(() => {
    const contexts = issueContextPacket?.ticket_contexts ?? []
    setSelectedTicketContextId((current) =>
      contexts.some((item) => item.context_id === current) ? current : '',
    )
  }, [issueContextPacket?.ticket_contexts])

  useEffect(() => {
    if (!selectedTicketContext) {
      setTicketContextProviderDraft('manual')
      setTicketContextExternalIdDraft('')
      setTicketContextTitleDraft('')
      setTicketContextSummaryDraft('')
      setTicketContextCriteriaDraft('')
      setTicketContextLinksDraft('')
      setTicketContextLabelsDraft('')
      setTicketContextStatusDraft('')
      setTicketContextSourceExcerptDraft('')
      return
    }
    setTicketContextProviderDraft(selectedTicketContext.provider)
    setTicketContextExternalIdDraft(selectedTicketContext.external_id ?? '')
    setTicketContextTitleDraft(selectedTicketContext.title)
    setTicketContextSummaryDraft(selectedTicketContext.summary)
    setTicketContextCriteriaDraft(selectedTicketContext.acceptance_criteria.join('\n'))
    setTicketContextLinksDraft(selectedTicketContext.links.join('\n'))
    setTicketContextLabelsDraft(selectedTicketContext.labels.join(', '))
    setTicketContextStatusDraft(selectedTicketContext.status ?? '')
    setTicketContextSourceExcerptDraft(selectedTicketContext.source_excerpt ?? '')
  }, [selectedTicketContext])

  useEffect(() => {
    const threatModels = issueContextPacket?.threat_models ?? []
    setSelectedThreatModelId((current) =>
      threatModels.some((item) => item.threat_model_id === current) ? current : '',
    )
  }, [issueContextPacket?.threat_models])

  useEffect(() => {
    if (!selectedThreatModel) {
      setThreatModelTitleDraft('')
      setThreatModelMethodologyDraft('manual')
      setThreatModelSummaryDraft('')
      setThreatModelAssetsDraft('')
      setThreatModelEntryPointsDraft('')
      setThreatModelTrustBoundariesDraft('')
      setThreatModelAbuseCasesDraft('')
      setThreatModelMitigationsDraft('')
      setThreatModelReferencesDraft('')
      setThreatModelStatusDraft('draft')
      return
    }
    setThreatModelTitleDraft(selectedThreatModel.title)
    setThreatModelMethodologyDraft(selectedThreatModel.methodology)
    setThreatModelSummaryDraft(selectedThreatModel.summary)
    setThreatModelAssetsDraft(selectedThreatModel.assets.join('\n'))
    setThreatModelEntryPointsDraft(selectedThreatModel.entry_points.join('\n'))
    setThreatModelTrustBoundariesDraft(selectedThreatModel.trust_boundaries.join('\n'))
    setThreatModelAbuseCasesDraft(selectedThreatModel.abuse_cases.join('\n'))
    setThreatModelMitigationsDraft(selectedThreatModel.mitigations.join('\n'))
    setThreatModelReferencesDraft(selectedThreatModel.references.join('\n'))
    setThreatModelStatusDraft(selectedThreatModel.status)
  }, [selectedThreatModel])

  useEffect(() => {
    const browserDumps = issueContextPacket?.browser_dumps ?? []
    setSelectedBrowserDumpId((current) =>
      browserDumps.some((item) => item.dump_id === current) ? current : '',
    )
  }, [issueContextPacket?.browser_dumps])

  useEffect(() => {
    if (!selectedBrowserDump) {
      setBrowserDumpSourceDraft('manual')
      setBrowserDumpLabelDraft('')
      setBrowserDumpPageUrlDraft('')
      setBrowserDumpPageTitleDraft('')
      setBrowserDumpSummaryDraft('')
      setBrowserDumpDomSnapshotDraft('')
      setBrowserDumpConsoleDraft('')
      setBrowserDumpNetworkDraft('')
      setBrowserDumpScreenshotPathDraft('')
      setBrowserDumpNotesDraft('')
      return
    }
    setBrowserDumpSourceDraft(selectedBrowserDump.source)
    setBrowserDumpLabelDraft(selectedBrowserDump.label)
    setBrowserDumpPageUrlDraft(selectedBrowserDump.page_url ?? '')
    setBrowserDumpPageTitleDraft(selectedBrowserDump.page_title ?? '')
    setBrowserDumpSummaryDraft(selectedBrowserDump.summary)
    setBrowserDumpDomSnapshotDraft(selectedBrowserDump.dom_snapshot)
    setBrowserDumpConsoleDraft(selectedBrowserDump.console_messages.join('\n'))
    setBrowserDumpNetworkDraft(selectedBrowserDump.network_requests.join('\n'))
    setBrowserDumpScreenshotPathDraft(selectedBrowserDump.screenshot_path ?? '')
    setBrowserDumpNotesDraft(selectedBrowserDump.notes ?? '')
  }, [selectedBrowserDump])

  useEffect(() => {
    if (!workspaceId || !selectedRunRecordId) {
      setSelectedRunPlan(null)
      return
    }
    if (selectedRunPlanRecord) {
      setSelectedRunPlan(selectedRunPlanRecord)
      return
    }
    if (selectedRunStatus !== 'planning') {
      setSelectedRunPlan(null)
      return
    }

    let cancelled = false
    void getPlan(workspaceId, selectedRunRecordId)
      .then((plan) => {
        if (!cancelled) {
          setSelectedRunPlan(plan)
        }
      })
      .catch(() => {
        if (!cancelled) {
          setSelectedRunPlan(null)
        }
      })

    return () => {
      cancelled = true
    }
  }, [workspaceId, selectedRunRecordId, selectedRunStatus, selectedRunPlanRecord])

  useEffect(() => {
    if (activeView !== 'runs' && activeView !== 'review') return
    if (!workspaceId || !selectedRunRecordId) return
    setLogContent('')
    logOffsetRef.current = 0

    const poll = async () => {
      try {
        const [runRecord, log] = await Promise.all([
          readRun(workspaceId, selectedRunRecordId),
          readRunLog(workspaceId, selectedRunRecordId, logOffsetRef.current),
        ])
        setRuns((currentRuns) => {
          const next = currentRuns.filter((item) => item.run_id !== runRecord.run_id)
          return [runRecord, ...next]
        })
        if (log.content) {
          setLogContent((current) => `${current}${log.content}`)
        }
        logOffsetRef.current = log.offset
        if (runRecord.status === 'completed' || runRecord.status === 'failed' || runRecord.status === 'cancelled') {
          await refreshActivityData(workspaceId)
          await refreshCostData(workspaceId)
        }
        if (!log.eof) {
          logTimerRef.current = window.setTimeout(poll, 1200)
        }
      } catch (nextError) {
        setError(nextError instanceof Error ? nextError.message : String(nextError))
      }
    }

    void poll()
    return () => {
      if (logTimerRef.current) {
        window.clearTimeout(logTimerRef.current)
      }
    }
  }, [activeView, workspaceId, selectedRunRecordId])

  useEffect(() => {
    if (!workspaceId || !activeLiveRunId) return

    const poll = async () => {
      try {
        const runRecord = await readRun(workspaceId, activeLiveRunId)
        setRuns((currentRuns) => {
          const next = currentRuns.filter((item) => item.run_id !== runRecord.run_id)
          return [runRecord, ...next]
        })
        if (runRecord.status === 'completed' || runRecord.status === 'failed' || runRecord.status === 'cancelled') {
          await refreshActivityData(workspaceId)
          await refreshCostData(workspaceId)
          return
        }
        runRefreshTimerRef.current = window.setTimeout(poll, 1500)
      } catch (nextError) {
        setError(nextError instanceof Error ? nextError.message : String(nextError))
      }
    }

    runRefreshTimerRef.current = window.setTimeout(poll, 900)
    return () => {
      if (runRefreshTimerRef.current) {
        window.clearTimeout(runRefreshTimerRef.current)
      }
    }
  }, [workspaceId, activeLiveRunId])

  useEffect(() => {
    if (!workspaceId || !terminalId) return

    const poll = async () => {
      try {
        const log = await readTerminal(terminalId, workspaceId, terminalOffsetRef.current)
        if (log.content) {
          setTerminalOutput((current) => `${current}${log.content}`)
          terminalOffsetRef.current = log.offset
        }
        if (!log.eof) {
          terminalTimerRef.current = window.setTimeout(poll, 1200)
        }
      } catch (nextError) {
        setError(nextError instanceof Error ? nextError.message : String(nextError))
      }
    }

    void poll()
    return () => {
      if (terminalTimerRef.current) {
        window.clearTimeout(terminalTimerRef.current)
      }
    }
  }, [workspaceId, terminalId])

  async function loadWorkspaceState(rootPath: string) {
    setLoading(true)
    setError(null)
    try {
      const nextSnapshot = await loadWorkspace(rootPath)
      clearIssueQueueState('issues')
      startTransition(() => {
        setSnapshot(nextSnapshot)
        setIssueQueue(nextSnapshot.issues)
        setSignalQueue(nextSnapshot.signals)
      })
      setWorkspacePath(rootPath)
      setWorkspaces(await listWorkspaces())
      setCapabilities(await getCapabilities())
      await refreshHealth()
      setTreePath('')
      setExecutionOpen(false)
      setSelectedRunId(null)
      setSelectedVerificationProfileId('')
      setSelectedIssueId(nextSnapshot.issues[0]?.bug_id ?? null)
      setSelectedSignalId(nextSnapshot.signals[0]?.signal_id ?? null)
      setActivityQuery('')
      setActivityActionFilter('all')
      setActivityEntityTypeFilter('all')
      setActivityActorKindFilter('all')
      setSelectedActivityId(null)
    } catch (nextError) {
      setError(nextError instanceof Error ? nextError.message : String(nextError))
    } finally {
      setLoading(false)
    }
  }

  async function handleScan() {
    if (!snapshot) return
    setLoading(true)
    try {
      const nextSnapshot = await scanWorkspace(snapshot.workspace.workspace_id)
      startTransition(() => setSnapshot(nextSnapshot))
      setCapabilities(await getCapabilities())
      await refreshHealth()
    } catch (nextError) {
      setError(nextError instanceof Error ? nextError.message : String(nextError))
    } finally {
      setLoading(false)
    }
  }

  async function handleGenerateGuidanceStarter(templateId: 'agents' | 'openhands_repo' | 'conventions') {
    if (!snapshot) return
    const candidate = guidanceHealth?.starters.find((item) => item.template_id === templateId)
    const shouldOverwrite = candidate?.exists
      ? window.confirm(`${candidate.path} already exists. Replace it with a fresh starter?`)
      : false
    if (candidate?.exists && !shouldOverwrite) return
    setLoading(true)
    try {
      await generateGuidanceStarter(snapshot.workspace.workspace_id, {
        template_id: templateId,
        overwrite: shouldOverwrite,
      })
      await refreshWorkspaceData(snapshot.workspace.workspace_id)
    } catch (nextError) {
      setError(nextError instanceof Error ? nextError.message : String(nextError))
    } finally {
      setLoading(false)
    }
  }

  async function handleRunIssue() {
    if (!snapshot || !selectedIssue || !model) return
    setLoading(true)
    try {
      const run = await startRun(snapshot.workspace.workspace_id, selectedIssue.bug_id, {
        runtime,
        model,
        instruction,
        runbook_id: selectedRunbookId || undefined,
        planning: planningMode,
      })
      setRuns((current) => [run, ...current])
      setSelectedRunId(run.run_id)
      setSelectedRunPlan(run.plan ?? null)
      setActiveView('runs')
      setExecutionOpen(true)
      setInstruction('')
      await refreshActivityData(snapshot.workspace.workspace_id)
    } catch (nextError) {
      setError(nextError instanceof Error ? nextError.message : String(nextError))
    } finally {
      setLoading(false)
    }
  }

  async function handleRunEvalScenario(scenarioId: string) {
    if (!snapshot || !selectedIssue || !model) return
    setLoading(true)
    try {
      const run = await startRun(snapshot.workspace.workspace_id, selectedIssue.bug_id, {
        runtime,
        model,
        instruction,
        runbook_id: selectedRunbookId || undefined,
        eval_scenario_id: scenarioId,
        planning: planningMode,
      })
      setRuns((current) => [run, ...current])
      setSelectedRunId(run.run_id)
      setSelectedRunPlan(run.plan ?? null)
      setActiveView('runs')
      setExecutionOpen(true)
      setInstruction('')
      await refreshActivityData(snapshot.workspace.workspace_id)
      setEvalScenarios(await listEvalScenarios(snapshot.workspace.workspace_id, { issue_id: selectedIssue.bug_id }))
      setEvalReport(await getEvalReport(snapshot.workspace.workspace_id))
    } catch (nextError) {
      setError(nextError instanceof Error ? nextError.message : String(nextError))
    } finally {
      setLoading(false)
    }
  }

  async function handleReplayAllEvalScenarios() {
    if (!snapshot || !selectedIssue || !model) return
    setLoading(true)
    try {
      const result = await replayEvalScenarios(snapshot.workspace.workspace_id, selectedIssue.bug_id, {
        runtime,
        model,
        instruction,
        runbook_id: selectedRunbookId || undefined,
        planning: planningMode,
      })
      setRuns((current) => [...result.queued_runs, ...current])
      if (result.queued_runs[0]) {
        setSelectedRunId(result.queued_runs[0].run_id)
        setSelectedRunPlan(result.queued_runs[0].plan ?? null)
      }
      setActiveView('runs')
      setExecutionOpen(true)
      setInstruction('')
      await refreshActivityData(snapshot.workspace.workspace_id)
      setEvalScenarios(await listEvalScenarios(snapshot.workspace.workspace_id, { issue_id: selectedIssue.bug_id }))
      setEvalReport(await getEvalReport(snapshot.workspace.workspace_id))
    } catch (nextError) {
      setError(nextError instanceof Error ? nextError.message : String(nextError))
    } finally {
      setLoading(false)
    }
  }

  async function handleQueryAgent() {
    if (!snapshot || !model || !queryPrompt.trim()) return
    setLoading(true)
    try {
      const run = await queryAgent(snapshot.workspace.workspace_id, {
        runtime,
        model,
        prompt: queryPrompt.trim(),
      })
      setRuns((current) => [run, ...current])
      setSelectedRunId(run.run_id)
      setSelectedRunPlan(null)
      setActiveView('runs')
      setExecutionOpen(true)
      await refreshActivityData(snapshot.workspace.workspace_id)
    } catch (nextError) {
      setError(nextError instanceof Error ? nextError.message : String(nextError))
    } finally {
      setLoading(false)
    }
  }

  async function handlePromoteSignal(signal: DiscoverySignal) {
    if (!snapshot) return
    setLoading(true)
    try {
      const nextSnapshot = await promoteSignal(snapshot.workspace.workspace_id, signal.signal_id, signal.severity)
      startTransition(() => setSnapshot(nextSnapshot))
      setActiveView('issues')
      await refreshActivityData(snapshot.workspace.workspace_id)
    } catch (nextError) {
      setError(nextError instanceof Error ? nextError.message : String(nextError))
    } finally {
      setLoading(false)
    }
  }

  async function handleSaveIssue() {
    if (!snapshot || !selectedIssue) return
    setLoading(true)
    try {
      const updated = await updateIssue(snapshot.workspace.workspace_id, selectedIssue.bug_id, {
        severity: issueSeverityDraft,
        issue_status: issueStatusDraft,
        doc_status: issueDocStatusDraft,
        code_status: issueCodeStatusDraft,
        labels: parseLabelDraft(issueLabelsDraft),
        notes: issueNotesDraft,
        needs_followup: issueFollowupDraft,
      })
      setSnapshot((current) =>
        current
          ? {
              ...current,
              issues: current.issues.map((issue) => (issue.bug_id === updated.bug_id ? updated : issue)),
            }
          : current,
      )
      await refreshIssueQueue()
      setDriftSummary(await readDrift(snapshot.workspace.workspace_id))
      await refreshActivityData(snapshot.workspace.workspace_id)
    } catch (nextError) {
      setError(nextError instanceof Error ? nextError.message : String(nextError))
    } finally {
      setLoading(false)
    }
  }

  async function handleRefreshIssueAnalysis() {
    if (!snapshot || !selectedIssue) return
    setLoading(true)
    try {
      const [quality, duplicates, triage] = await Promise.all([
        scoreIssueQuality(snapshot.workspace.workspace_id, selectedIssue.bug_id),
        findDuplicates(snapshot.workspace.workspace_id, selectedIssue.bug_id),
        triageIssue(snapshot.workspace.workspace_id, selectedIssue.bug_id),
      ])
      setIssueQuality(quality)
      setIssueQualityById((current) => ({ ...current, [quality.issue_id]: quality }))
      setDuplicateMatches(duplicates)
      setTriageSuggestion(triage)
    } catch (nextError) {
      setError(nextError instanceof Error ? nextError.message : String(nextError))
    } finally {
      setLoading(false)
    }
  }

  async function handleGenerateTestSuggestions() {
    if (!snapshot || !selectedIssue) return
    setLoading(true)
    try {
      const suggestions = await generateTestSuggestions(snapshot.workspace.workspace_id, selectedIssue.bug_id)
      setTestSuggestions(suggestions)
    } catch (nextError) {
      setError(nextError instanceof Error ? nextError.message : String(nextError))
    } finally {
      setLoading(false)
    }
  }

  async function handleGeneratePatchCritique() {
    if (!snapshot || !selectedRun) return
    setLoading(true)
    try {
      const critique = await generatePatchCritique(snapshot.workspace.workspace_id, selectedRun.run_id)
      setPatchCritique(critique)
      setRunImprovements(critique.improvements.filter((item) => !item.dismissed))
      try {
        setRunInsight(await getRunInsights(snapshot.workspace.workspace_id, selectedRun.run_id))
      } catch {
        setRunInsight(null)
      }
      await refreshActivityData(snapshot.workspace.workspace_id)
    } catch (nextError) {
      setError(nextError instanceof Error ? nextError.message : String(nextError))
    } finally {
      setLoading(false)
    }
  }

  async function handleDismissImprovement(suggestionId: string) {
    if (!snapshot || !selectedRun) return
    const reason = window.prompt('Dismiss reason (optional):') ?? undefined
    setLoading(true)
    try {
      await dismissImprovement(snapshot.workspace.workspace_id, selectedRun.run_id, suggestionId, reason)
      const [critiqueResult, improvementsResult] = await Promise.allSettled([
        getPatchCritique(snapshot.workspace.workspace_id, selectedRun.run_id),
        getRunImprovements(snapshot.workspace.workspace_id, selectedRun.run_id),
      ])
      if (critiqueResult.status === 'fulfilled') {
        setPatchCritique(critiqueResult.value)
      }
      if (improvementsResult.status === 'fulfilled') {
        setRunImprovements(improvementsResult.value)
      } else {
        setRunImprovements([])
      }
      try {
        setRunInsight(await getRunInsights(snapshot.workspace.workspace_id, selectedRun.run_id))
      } catch {
        setRunInsight(null)
      }
    } catch (nextError) {
      setError(nextError instanceof Error ? nextError.message : String(nextError))
    } finally {
      setLoading(false)
    }
  }

  async function handleCreateTrackerIssue() {
    if (!snapshot || !newIssueTitleDraft.trim()) return
    setLoading(true)
    try {
      const created = await createIssue(snapshot.workspace.workspace_id, {
        title: newIssueTitleDraft.trim(),
        severity: newIssueSeverityDraft,
        summary: newIssueSummaryDraft.trim() || undefined,
        labels: parseLabelDraft(newIssueLabelsDraft),
      })
      const nextSnapshot = await readSnapshot(snapshot.workspace.workspace_id)
      startTransition(() => {
        setSnapshot(nextSnapshot)
        setIssueQueue(nextSnapshot.issues)
      })
      setSelectedIssueId(created.bug_id)
      setActiveView('issues')
      setNewIssueTitleDraft('')
      setNewIssueSummaryDraft('')
      setNewIssueLabelsDraft('')
      await refreshActivityData(snapshot.workspace.workspace_id)
    } catch (nextError) {
      setError(nextError instanceof Error ? nextError.message : String(nextError))
    } finally {
      setLoading(false)
    }
  }

  async function handleRecordFix() {
    if (!snapshot || !selectedIssue || !fixSummaryDraft.trim()) return
    setLoading(true)
    try {
      await recordFix(snapshot.workspace.workspace_id, selectedIssue.bug_id, {
        summary: fixSummaryDraft.trim(),
        how: fixHowDraft.trim() || undefined,
        run_id: fixRunIdDraft ?? undefined,
        changed_files: parseLabelDraft(fixChangedFilesDraft),
        tests_run: parseLabelDraft(fixTestsDraft),
        issue_status: fixIssueStatusDraft || undefined,
      })
      const nextSnapshot = await readSnapshot(snapshot.workspace.workspace_id)
      startTransition(() => {
        setSnapshot(nextSnapshot)
        setIssueQueue(nextSnapshot.issues)
      })
      void issueWork(snapshot.workspace.workspace_id, selectedIssue.bug_id, selectedRunbookId || undefined).then(setIssueContextPacket)
      await refreshActivityData(snapshot.workspace.workspace_id)
      setFixSummaryDraft('')
      setFixChangedFilesDraft('')
      setFixTestsDraft('')
      setFixHowDraft('')
      setFixRunIdDraft(null)
    } catch (nextError) {
      setError(nextError instanceof Error ? nextError.message : String(nextError))
    } finally {
      setLoading(false)
    }
  }

  async function handleReviewRun(disposition: 'dismissed' | 'investigation_only') {
    if (!snapshot || !selectedRun) return
    setLoading(true)
    try {
      await reviewRun(snapshot.workspace.workspace_id, selectedRun.run_id, { disposition })
      const [nextSnapshot, nextRuns] = await Promise.all([
        readSnapshot(snapshot.workspace.workspace_id),
        listRuns(snapshot.workspace.workspace_id),
      ])
      startTransition(() => {
        setSnapshot(nextSnapshot)
        setIssueQueue(nextSnapshot.issues)
        setRuns(nextRuns)
      })
      if (selectedRun.issue_id !== 'workspace-query') {
        if (selectedIssue?.bug_id === selectedRun.issue_id) {
          setIssueContextPacket(await issueWork(snapshot.workspace.workspace_id, selectedRun.issue_id, selectedRunbookId || undefined))
        }
        setIssueDrift(await readIssueDrift(snapshot.workspace.workspace_id, selectedRun.issue_id))
      }
      await refreshActivityData(snapshot.workspace.workspace_id)
    } catch (nextError) {
      setError(nextError instanceof Error ? nextError.message : String(nextError))
    } finally {
      setLoading(false)
    }
  }

  async function handleAcceptRunDraft() {
    if (!snapshot || !selectedRun || selectedRun.issue_id === 'workspace-query') return
    setLoading(true)
    try {
      await acceptRunReview(snapshot.workspace.workspace_id, selectedRun.run_id, {
        issue_status: 'verification',
      })
      const [nextSnapshot, nextRuns] = await Promise.all([
        readSnapshot(snapshot.workspace.workspace_id),
        listRuns(snapshot.workspace.workspace_id),
      ])
      startTransition(() => {
        setSnapshot(nextSnapshot)
        setIssueQueue(nextSnapshot.issues)
        setRuns(nextRuns)
      })
      if (selectedIssue?.bug_id === selectedRun.issue_id) {
        setIssueContextPacket(await issueWork(snapshot.workspace.workspace_id, selectedRun.issue_id, selectedRunbookId || undefined))
      }
      await refreshActivityData(snapshot.workspace.workspace_id)
    } catch (nextError) {
      setError(nextError instanceof Error ? nextError.message : String(nextError))
    } finally {
      setLoading(false)
    }
  }

  function handleLoadRunbook(mode: 'verify' | 'fix' | 'drift') {
    setSelectedRunbookId(mode === 'drift' ? 'drift-audit' : mode)
    setInstruction(buildInstructionFromRunbook(mode, issueContextPacket, selectedIssue))
    setExecutionOpen(true)
  }

  function handleApplySelectedRunbook() {
    setInstruction(buildInstructionFromSelectedRunbook(issueContextPacket, selectedIssue, selectedRunbookId))
    setExecutionOpen(true)
  }

  async function handleSaveRunbook() {
    if (!snapshot || !runbookNameDraft.trim() || !runbookTemplateDraft.trim()) return
    setLoading(true)
    try {
      const existing = issueContextPacket?.available_runbooks.find((item) => item.runbook_id === selectedRunbookId)
      const saved = await saveRunbook(snapshot.workspace.workspace_id, {
        runbook_id: existing && !existing.built_in ? existing.runbook_id : undefined,
        name: runbookNameDraft.trim(),
        description: runbookDescriptionDraft.trim(),
        scope: 'issue',
        template: runbookTemplateDraft.trim(),
      })
      setSelectedRunbookId(saved.runbook_id)
      if (selectedIssue) {
        setIssueContextPacket(await issueWork(snapshot.workspace.workspace_id, selectedIssue.bug_id, saved.runbook_id))
      }
      await refreshActivityData(snapshot.workspace.workspace_id)
    } catch (nextError) {
      setError(nextError instanceof Error ? nextError.message : String(nextError))
    } finally {
      setLoading(false)
    }
  }

  async function handleDeleteRunbook() {
    if (!snapshot) return
    const selectedRunbook = issueContextPacket?.available_runbooks.find((item) => item.runbook_id === selectedRunbookId)
    if (!selectedRunbook || selectedRunbook.built_in) return
    setLoading(true)
    try {
      await deleteRunbook(snapshot.workspace.workspace_id, selectedRunbook.runbook_id)
      const fallbackRunbookId = 'fix'
      setSelectedRunbookId(fallbackRunbookId)
      if (selectedIssue) {
        setIssueContextPacket(await issueWork(snapshot.workspace.workspace_id, selectedIssue.bug_id, fallbackRunbookId))
      }
      await refreshActivityData(snapshot.workspace.workspace_id)
    } catch (nextError) {
      setError(nextError instanceof Error ? nextError.message : String(nextError))
    } finally {
      setLoading(false)
    }
  }

  function handleApplyVerificationProfile() {
    setInstruction(buildInstructionFromVerificationProfile(selectedVerificationProfile, selectedIssue))
    setExecutionOpen(true)
  }

  async function handleSaveVerificationProfile() {
    if (!snapshot || !verificationProfileNameDraft.trim() || !verificationProfileTestCommandDraft.trim()) return
    setLoading(true)
    try {
      const saved = await saveVerificationProfile(snapshot.workspace.workspace_id, {
        profile_id: selectedVerificationProfile?.profile_id || undefined,
        name: verificationProfileNameDraft.trim(),
        description: verificationProfileDescriptionDraft.trim(),
        test_command: verificationProfileTestCommandDraft.trim(),
        coverage_command: verificationProfileCoverageCommandDraft.trim() || undefined,
        coverage_report_path: verificationProfileCoveragePathDraft.trim() || undefined,
        coverage_format: verificationProfileCoverageFormatDraft,
        max_runtime_seconds: Number.parseInt(verificationProfileRuntimeDraft, 10) || 30,
        retry_count: Number.parseInt(verificationProfileRetryDraft, 10) || 0,
        source_paths: parseLabelDraft(verificationProfileSourcePathsDraft),
        checklist_items: parseLineDraft(verificationProfileChecklistDraft),
      })
      setVerificationProfiles(await listVerificationProfiles(snapshot.workspace.workspace_id))
      setSelectedVerificationProfileId(saved.profile_id)
      if (selectedIssue) {
        setIssueContextPacket(await issueWork(snapshot.workspace.workspace_id, selectedIssue.bug_id, selectedRunbookId || undefined))
      }
      await refreshActivityData(snapshot.workspace.workspace_id)
    } catch (nextError) {
      setError(nextError instanceof Error ? nextError.message : String(nextError))
    } finally {
      setLoading(false)
    }
  }

  async function handleDeleteVerificationProfile() {
    if (!snapshot || !selectedVerificationProfile || selectedVerificationProfile.built_in) return
    setLoading(true)
    try {
      await deleteVerificationProfile(snapshot.workspace.workspace_id, selectedVerificationProfile.profile_id)
      const nextProfiles = await listVerificationProfiles(snapshot.workspace.workspace_id)
      setVerificationProfiles(nextProfiles)
      setSelectedVerificationProfileId(nextProfiles[0]?.profile_id ?? '')
      if (selectedIssue) {
        setIssueContextPacket(await issueWork(snapshot.workspace.workspace_id, selectedIssue.bug_id, selectedRunbookId || undefined))
      }
      await refreshActivityData(snapshot.workspace.workspace_id)
    } catch (nextError) {
      setError(nextError instanceof Error ? nextError.message : String(nextError))
    } finally {
      setLoading(false)
    }
  }

  async function handleSaveTicketContext() {
    if (!snapshot || !selectedIssue || !ticketContextTitleDraft.trim()) return
    setLoading(true)
    try {
      const saved = await saveTicketContext(snapshot.workspace.workspace_id, selectedIssue.bug_id, {
        context_id: selectedTicketContext?.context_id || undefined,
        provider: ticketContextProviderDraft,
        external_id: ticketContextExternalIdDraft.trim() || undefined,
        title: ticketContextTitleDraft.trim(),
        summary: ticketContextSummaryDraft.trim(),
        acceptance_criteria: parseLineDraft(ticketContextCriteriaDraft),
        links: parseLineDraft(ticketContextLinksDraft),
        labels: parseLabelDraft(ticketContextLabelsDraft),
        status: ticketContextStatusDraft.trim() || undefined,
        source_excerpt: ticketContextSourceExcerptDraft.trim() || undefined,
      })
      setSelectedTicketContextId(saved.context_id)
      setIssueContextPacket(await issueWork(snapshot.workspace.workspace_id, selectedIssue.bug_id, selectedRunbookId || undefined))
      await refreshActivityData(snapshot.workspace.workspace_id)
    } catch (nextError) {
      setError(nextError instanceof Error ? nextError.message : String(nextError))
    } finally {
      setLoading(false)
    }
  }

  async function handleDeleteTicketContext() {
    if (!snapshot || !selectedIssue || !selectedTicketContext) return
    setLoading(true)
    try {
      await deleteTicketContext(snapshot.workspace.workspace_id, selectedIssue.bug_id, selectedTicketContext.context_id)
      setSelectedTicketContextId('')
      setIssueContextPacket(await issueWork(snapshot.workspace.workspace_id, selectedIssue.bug_id, selectedRunbookId || undefined))
      await refreshActivityData(snapshot.workspace.workspace_id)
    } catch (nextError) {
      setError(nextError instanceof Error ? nextError.message : String(nextError))
    } finally {
      setLoading(false)
    }
  }

  async function handleSaveThreatModel() {
    if (!snapshot || !selectedIssue || !threatModelTitleDraft.trim()) return
    setLoading(true)
    try {
      const saved = await saveThreatModel(snapshot.workspace.workspace_id, selectedIssue.bug_id, {
        threat_model_id: selectedThreatModel?.threat_model_id || undefined,
        title: threatModelTitleDraft.trim(),
        methodology: threatModelMethodologyDraft,
        summary: threatModelSummaryDraft.trim(),
        assets: parseLineDraft(threatModelAssetsDraft),
        entry_points: parseLineDraft(threatModelEntryPointsDraft),
        trust_boundaries: parseLineDraft(threatModelTrustBoundariesDraft),
        abuse_cases: parseLineDraft(threatModelAbuseCasesDraft),
        mitigations: parseLineDraft(threatModelMitigationsDraft),
        references: parseLineDraft(threatModelReferencesDraft),
        status: threatModelStatusDraft,
      })
      setSelectedThreatModelId(saved.threat_model_id)
      setIssueContextPacket(await issueWork(snapshot.workspace.workspace_id, selectedIssue.bug_id, selectedRunbookId || undefined))
      await refreshActivityData(snapshot.workspace.workspace_id)
    } catch (nextError) {
      setError(nextError instanceof Error ? nextError.message : String(nextError))
    } finally {
      setLoading(false)
    }
  }

  async function handleDeleteThreatModel() {
    if (!snapshot || !selectedIssue || !selectedThreatModel) return
    setLoading(true)
    try {
      await deleteThreatModel(snapshot.workspace.workspace_id, selectedIssue.bug_id, selectedThreatModel.threat_model_id)
      setSelectedThreatModelId('')
      setIssueContextPacket(await issueWork(snapshot.workspace.workspace_id, selectedIssue.bug_id, selectedRunbookId || undefined))
      await refreshActivityData(snapshot.workspace.workspace_id)
    } catch (nextError) {
      setError(nextError instanceof Error ? nextError.message : String(nextError))
    } finally {
      setLoading(false)
    }
  }

  async function handleSaveBrowserDump() {
    if (!snapshot || !selectedIssue || !browserDumpLabelDraft.trim()) return
    setLoading(true)
    try {
      const saved = await saveBrowserDump(snapshot.workspace.workspace_id, selectedIssue.bug_id, {
        dump_id: selectedBrowserDump?.dump_id || undefined,
        source: browserDumpSourceDraft,
        label: browserDumpLabelDraft.trim(),
        page_url: browserDumpPageUrlDraft.trim() || undefined,
        page_title: browserDumpPageTitleDraft.trim() || undefined,
        summary: browserDumpSummaryDraft.trim() || undefined,
        dom_snapshot: browserDumpDomSnapshotDraft,
        console_messages: parseLineDraft(browserDumpConsoleDraft),
        network_requests: parseLineDraft(browserDumpNetworkDraft),
        screenshot_path: browserDumpScreenshotPathDraft.trim() || undefined,
        notes: browserDumpNotesDraft.trim() || undefined,
      })
      setSelectedBrowserDumpId(saved.dump_id)
      setIssueContextPacket(await issueWork(snapshot.workspace.workspace_id, selectedIssue.bug_id, selectedRunbookId || undefined))
      await refreshActivityData(snapshot.workspace.workspace_id)
    } catch (nextError) {
      setError(nextError instanceof Error ? nextError.message : String(nextError))
    } finally {
      setLoading(false)
    }
  }

  async function handleDeleteBrowserDump() {
    if (!snapshot || !selectedIssue || !selectedBrowserDump) return
    setLoading(true)
    try {
      await deleteBrowserDump(snapshot.workspace.workspace_id, selectedIssue.bug_id, selectedBrowserDump.dump_id)
      setSelectedBrowserDumpId('')
      setIssueContextPacket(await issueWork(snapshot.workspace.workspace_id, selectedIssue.bug_id, selectedRunbookId || undefined))
      await refreshActivityData(snapshot.workspace.workspace_id)
    } catch (nextError) {
      setError(nextError instanceof Error ? nextError.message : String(nextError))
    } finally {
      setLoading(false)
    }
  }

  async function handleCaptureIssueContextReplay() {
    if (!snapshot || !selectedIssue) return
    setLoading(true)
    try {
      const replay = await captureIssueContextReplay(
        snapshot.workspace.workspace_id,
        selectedIssue.bug_id,
        contextReplayLabelDraft.trim() || undefined,
      )
      setIssueContextReplays(await listIssueContextReplays(snapshot.workspace.workspace_id, selectedIssue.bug_id))
      setSelectedContextReplayId(replay.replay_id)
      setContextReplayComparison(
        await compareIssueContextReplay(snapshot.workspace.workspace_id, selectedIssue.bug_id, replay.replay_id),
      )
      setContextReplayLabelDraft('')
      await refreshActivityData(snapshot.workspace.workspace_id)
    } catch (nextError) {
      setError(nextError instanceof Error ? nextError.message : String(nextError))
    } finally {
      setLoading(false)
    }
  }

  async function handleCompareIssueContextReplay(replayId: string) {
    if (!snapshot || !selectedIssue) return
    setLoading(true)
    try {
      setSelectedContextReplayId(replayId)
      setContextReplayComparison(
        await compareIssueContextReplay(snapshot.workspace.workspace_id, selectedIssue.bug_id, replayId),
      )
    } catch (nextError) {
      setError(nextError instanceof Error ? nextError.message : String(nextError))
    } finally {
      setLoading(false)
    }
  }

  function handleUseSelectedRunForFix() {
    if (!snapshot || !selectedIssue || !selectedRun || selectedRun.issue_id !== selectedIssue.bug_id) return
    setLoading(true)
    void suggestFixDraft(snapshot.workspace.workspace_id, selectedIssue.bug_id, selectedRun.run_id)
      .then((draft) => {
        setFixRunIdDraft(draft.run_id)
        setFixSummaryDraft(draft.summary)
        setFixHowDraft(draft.how ?? '')
        setFixChangedFilesDraft(draft.changed_files.join(', '))
        setFixTestsDraft(draft.tests_run.join(', '))
        if (draft.suggested_issue_status) {
          setFixIssueStatusDraft(draft.suggested_issue_status)
        }
      })
      .catch((nextError) => setError(nextError instanceof Error ? nextError.message : String(nextError)))
      .finally(() => setLoading(false))
  }

  async function handleSaveView() {
    if (!snapshot || !savedViewName.trim()) return
    setLoading(true)
    try {
      const created = await createSavedView(snapshot.workspace.workspace_id, {
        name: savedViewName.trim(),
        query: issueFilters.query,
        severities: issueFilters.severities,
        statuses: issueFilters.statuses,
        sources: issueFilters.sources,
        labels: issueFilters.labels,
        drift_only: activeView === 'drift' || issueFilters.drift_only,
        needs_followup: issueFilters.needs_followup ?? null,
        review_ready_only: issueFilters.review_ready_only,
      })
      setActiveSavedViewId(created.view_id)
      setActivePresetId(null)
      setSavedViews(await listSavedViews(snapshot.workspace.workspace_id))
      await refreshActivityData(snapshot.workspace.workspace_id)
    } catch (nextError) {
      setError(nextError instanceof Error ? nextError.message : String(nextError))
    } finally {
      setLoading(false)
    }
  }

  async function handleUpdateView() {
    if (!snapshot || !selectedSavedView) return
    setLoading(true)
    try {
      const updated = await updateSavedView(snapshot.workspace.workspace_id, selectedSavedView.view_id, {
        name: savedViewName.trim() || selectedSavedView.name,
        query: issueFilters.query,
        severities: issueFilters.severities,
        statuses: issueFilters.statuses,
        sources: issueFilters.sources,
        labels: issueFilters.labels,
        drift_only: activeView === 'drift' || issueFilters.drift_only,
        needs_followup: issueFilters.needs_followup ?? null,
        review_ready_only: issueFilters.review_ready_only,
      })
      setActiveSavedViewId(updated.view_id)
      setActivePresetId(null)
      setSavedViewName(updated.name)
      setSavedViews(await listSavedViews(snapshot.workspace.workspace_id))
      await refreshActivityData(snapshot.workspace.workspace_id)
    } catch (nextError) {
      setError(nextError instanceof Error ? nextError.message : String(nextError))
    } finally {
      setLoading(false)
    }
  }

  async function handleDeleteView() {
    if (!snapshot || !selectedSavedView) return
    setLoading(true)
    try {
      await deleteSavedView(snapshot.workspace.workspace_id, selectedSavedView.view_id)
      clearIssueQueueState(activeView)
      setSavedViews(await listSavedViews(snapshot.workspace.workspace_id))
      await refreshActivityData(snapshot.workspace.workspace_id)
    } catch (nextError) {
      setError(nextError instanceof Error ? nextError.message : String(nextError))
    } finally {
      setLoading(false)
    }
  }

  async function handleCancelRun() {
    if (!snapshot || !selectedRun) return
    setLoading(true)
    try {
      const updatedRun = await cancelRun(snapshot.workspace.workspace_id, selectedRun.run_id)
      setRuns((current) => [updatedRun, ...current.filter((item) => item.run_id !== updatedRun.run_id)])
      await refreshActivityData(snapshot.workspace.workspace_id)
    } catch (nextError) {
      setError(nextError instanceof Error ? nextError.message : String(nextError))
    } finally {
      setLoading(false)
    }
  }

  async function handleRetryRun() {
    if (!snapshot || !selectedRun) return
    setLoading(true)
    try {
      const retriedRun = await retryRun(snapshot.workspace.workspace_id, selectedRun.run_id)
      setRuns((current) => [retriedRun, ...current])
      setSelectedRunId(retriedRun.run_id)
      setSelectedRunPlan(retriedRun.plan ?? null)
      await refreshActivityData(snapshot.workspace.workspace_id)
    } catch (nextError) {
      setError(nextError instanceof Error ? nextError.message : String(nextError))
    } finally {
      setLoading(false)
    }
  }

  async function handleGeneratePlan() {
    if (!snapshot || !selectedRun) return
    setLoading(true)
    try {
      const plan = await generatePlan(snapshot.workspace.workspace_id, selectedRun.run_id)
      setSelectedRunPlan(plan)
      setRuns((currentRuns) =>
        currentRuns.map((run) =>
          run.run_id === selectedRun.run_id ? { ...run, status: 'planning', plan } : run,
        ),
      )
      await refreshActivityData(snapshot.workspace.workspace_id)
    } catch (nextError) {
      setError(nextError instanceof Error ? nextError.message : String(nextError))
    } finally {
      setLoading(false)
    }
  }

  async function handleApprovePlan(feedback?: string) {
    if (!snapshot || !selectedRun) return
    setLoading(true)
    try {
      const approvedPlan = await approvePlan(snapshot.workspace.workspace_id, selectedRun.run_id, { feedback })
      setSelectedRunPlan(approvedPlan)
      const updatedRuns = await listRuns(snapshot.workspace.workspace_id)
      setRuns(updatedRuns)
      await refreshActivityData(snapshot.workspace.workspace_id)
    } catch (nextError) {
      setError(nextError instanceof Error ? nextError.message : String(nextError))
    } finally {
      setLoading(false)
    }
  }

  async function handleRejectPlan(reason: string) {
    if (!snapshot || !selectedRun) return
    setLoading(true)
    try {
      const rejectedPlan = await rejectPlan(snapshot.workspace.workspace_id, selectedRun.run_id, { reason })
      setSelectedRunPlan(rejectedPlan)
      const updatedRuns = await listRuns(snapshot.workspace.workspace_id)
      setRuns(updatedRuns)
      await refreshActivityData(snapshot.workspace.workspace_id)
    } catch (nextError) {
      setError(nextError instanceof Error ? nextError.message : String(nextError))
    } finally {
      setLoading(false)
    }
  }

  async function handleExport() {
    if (!snapshot) return
    try {
      const payload = await exportWorkspace(snapshot.workspace.workspace_id)
      await navigator.clipboard.writeText(JSON.stringify(payload, null, 2))
    } catch (nextError) {
      setError(nextError instanceof Error ? nextError.message : String(nextError))
    }
  }

  async function handleSaveSettings() {
    if (!settings) return
    setLoading(true)
    try {
      const next = await updateSettings(settings)
      setSettings(next)
      setCapabilities(await getCapabilities())
      await refreshHealth()
      setRuntime(next.local_agent_type)
    } catch (nextError) {
      setError(nextError instanceof Error ? nextError.message : String(nextError))
    } finally {
      setLoading(false)
    }
  }

  async function handleRefreshCapabilities() {
    try {
      setCapabilities(await getCapabilities())
      await refreshHealth()
    } catch (nextError) {
      setError(nextError instanceof Error ? nextError.message : String(nextError))
    }
  }

  async function handleProbeRuntime() {
    if (!snapshot || !model) return
    setRuntimeProbeLoading(true)
    try {
      const result = await probeRuntime(snapshot.workspace.workspace_id, { runtime, model })
      setRuntimeProbe(result)
      setCapabilities(await getCapabilities())
      await refreshHealth()
    } catch (nextError) {
      setError(nextError instanceof Error ? nextError.message : String(nextError))
    } finally {
      setRuntimeProbeLoading(false)
    }
  }

  async function handleOpenTerminal() {
    if (!snapshot) return
    try {
      const terminal = await openTerminal(snapshot.workspace.workspace_id)
      setTerminalId(terminal.terminal_id)
      setTerminalOutput('')
      terminalOffsetRef.current = 0
    } catch (nextError) {
      setError(nextError instanceof Error ? nextError.message : String(nextError))
    }
  }

  async function handleSendTerminal() {
    if (!terminalId) return
    try {
      await writeTerminal(terminalId, `${terminalInput}\n`)
      setTerminalInput('')
    } catch (nextError) {
      setError(nextError instanceof Error ? nextError.message : String(nextError))
    }
  }

  async function handleCloseTerminal() {
    if (!terminalId) return
    try {
      await closeTerminal(terminalId)
      setTerminalId(null)
    } catch (nextError) {
      setError(nextError instanceof Error ? nextError.message : String(nextError))
    }
  }

  return (
    <div className="app-shell">
      <WorkspaceSidebar
        workspacePath={workspacePath}
        workspaces={workspaces}
        selectedWorkspaceId={snapshot?.workspace.workspace_id}
        activeView={activeView}
        loading={loading}
        workspaceGuidance={workspaceGuidance}
        guidanceHealth={guidanceHealth}
        verificationProfileCount={verificationProfiles.length}
        onWorkspacePathChange={setWorkspacePath}
        onLoadWorkspace={() => void loadWorkspaceState(workspacePath)}
        onScanWorkspace={() => void handleScan()}
        onGenerateGuidanceStarter={(templateId) => void handleGenerateGuidanceStarter(templateId)}
        onSelectWorkspace={(rootPath) => void loadWorkspaceState(rootPath)}
        onChangeView={setActiveView}
        canScan={Boolean(snapshot)}
      />

      <main className="main-surface">
        <AppTopbar
          workspaceName={snapshot?.workspace.name}
          workspacePath={snapshot?.workspace.root_path}
          latestScanAt={snapshot?.workspace.latest_scan_at}
          worktree={worktree}
          costSummary={costSummary}
          guidanceCount={workspaceGuidance.length}
          hasRepoGuidance={workspaceGuidance.length > 0}
          guidanceHealth={guidanceHealth}
          backendHealthy={backendHealthy}
          canExport={Boolean(snapshot)}
          canToggleExecution={Boolean(snapshot && selectedIssue)}
          executionOpen={executionOpen}
          onExport={() => void handleExport()}
          onToggleExecution={() => setExecutionOpen((current) => !current)}
        />

        {error ? <div className="error-banner">{error}</div> : null}

        {!snapshot ? (
          <section className="empty-workspace-panel panel">
            <div className="detail-section">
              <p className="eyebrow">Workspace</p>
              <h3>Load a repository from the sidebar.</h3>
              <p className="subtle">This surface stays minimal until a tree is loaded. Then it becomes queue, inspector, and optional agent drawer.</p>
            </div>
            <div className="toolbar-row">
              <button type="button" onClick={() => void loadWorkspaceState(workspacePath)} disabled={loading}>
                Load current tree
              </button>
            </div>
            <div className="recent-workspace-list">
              {workspaces.length ? (
                workspaces.slice(0, 6).map((workspace) => (
                  <button
                    key={workspace.workspace_id}
                    type="button"
                    className="workspace-chip"
                    onClick={() => void loadWorkspaceState(workspace.root_path)}
                  >
                    <span>{workspace.name}</span>
                    <small>{workspace.root_path}</small>
                  </button>
                ))
              ) : (
                <p className="subtle">No cached workspaces yet.</p>
              )}
            </div>
          </section>
        ) : (
          <>
            {!workspaceGuidance.length ? (
              <section className="guidance-empty-state panel">
                <div className="detail-section">
                  <p className="eyebrow">Repo guidance</p>
                  <h3>This workspace has no repo instructions yet.</h3>
                  <p className="subtle">
                    Add an `AGENTS.md` or `CONVENTIONS.md` file so issue prompts and runs inherit project-specific rules instead of generic defaults.
                  </p>
                </div>
                <div className="tag-row">
                  <span className="tag">Recommended: `AGENTS.md`</span>
                  <span className="tag">Useful sections: setup, commands, code style, review expectations</span>
                  <span className="tag">Optional: `.openhands/skills/*.md` or `.devin/wiki.json`</span>
                </div>
                <div className="toolbar-row">
                  <button type="button" onClick={() => void handleGenerateGuidanceStarter('agents')} disabled={loading}>
                    Generate AGENTS.md
                  </button>
                  <button type="button" className="ghost-button" onClick={() => void handleGenerateGuidanceStarter('openhands_repo')} disabled={loading}>
                    Generate repo microagent
                  </button>
                  <button type="button" className="ghost-button" onClick={() => void handleGenerateGuidanceStarter('conventions')} disabled={loading}>
                    Generate CONVENTIONS.md
                  </button>
                </div>
              </section>
            ) : null}

            <section className="workspace-focus">
              <div>
                <p className="eyebrow">Current focus</p>
                <h3>
                  {selectedIssue?.bug_id ??
                    selectedRun?.issue_id ??
                    selectedSignal?.title ??
                    selectedSource?.label ??
                    selectedActivity?.summary ??
                    'Choose an item from the queue'}
                </h3>
              </div>
              <div className="tag-row">
                <span className="tag">View: {activeView}</span>
                <span className="tag">Issues: {snapshot.summary.issues_total}</span>
                <span className="tag">Open: {snapshot.summary.issues_open}</span>
                <span className="tag">Drift: {snapshot.summary.drift_total}</span>
                <span className="tag">Guidance: {workspaceGuidance.length}</span>
                {selectedIssue ? <span className="tag">Issue: {selectedIssue.issue_status}</span> : null}
                {selectedIssue?.drift_flags.length ? <span className="tag">Drift: {selectedIssue.drift_flags.length}</span> : null}
                {worktree?.is_git_repo ? <span className="tag">Branch: {worktree.branch ?? 'detached'}</span> : null}
              </div>
            </section>

            {executionOpen || activeLiveRun ? (
              <AgentDock
                runtime={runtime}
                model={model}
                runtimeModels={runtimeModels}
                capabilities={capabilities}
                selectedIssue={selectedIssue}
                queryPrompt={queryPrompt}
                runtimeProbe={runtimeProbe}
                runtimeProbeLoading={runtimeProbeLoading}
                backendHealthy={backendHealthy}
                loading={loading}
                onRuntimeChange={(nextRuntime) => {
                  setRuntime(nextRuntime)
                  setSettings((current) => ({ ...(current ?? { local_agent_type: nextRuntime }), local_agent_type: nextRuntime }))
                }}
                onModelChange={(nextModel) => {
                  setModel(nextModel)
                  setSettings((current) => ({
                    ...(current ?? { local_agent_type: runtime }),
                    [runtime === 'codex' ? 'codex_model' : 'opencode_model']: nextModel,
                  }))
                }}
                onQueryPromptChange={setQueryPrompt}
                onProbeRuntime={() => void handleProbeRuntime()}
                onQueryAgent={() => void handleQueryAgent()}
                onRunIssue={() => void handleRunIssue()}
                onOpenRuns={() => setActiveView('runs')}
                onToggleExecution={() => setExecutionOpen((current) => !current)}
                executionOpen={executionOpen}
              />
            ) : null}

            <section className={`board-grid ${executionOpen ? 'board-grid-with-execution' : 'board-grid-focus'}`}>
              <QueuePane
                activeView={activeView}
                issueQueue={issueQueue}
                signalQueue={signalQueue}
                runs={runs}
                reviewRuns={reviewRuns}
                sources={sources}
                treeNodes={treeNodes}
                treePath={treePath}
                activity={filteredActivity}
                costSummary={costSummary}
                issueQualityById={issueQualityById}
                runMetricsById={runMetricsById}
                selectedIssueId={selectedIssueId}
                selectedSignalId={selectedSignalId}
                selectedRunId={selectedRunId}
                selectedSourceId={selectedSourceId}
                selectedActivityId={selectedActivityId}
                savedViews={savedViews}
                selectedSavedViewName={selectedSavedView?.name}
                activeSavedViewId={activeSavedViewId}
                activePresetId={activePresetId}
                presets={queuePresets}
                issueFilters={issueFilters}
                issueLabelFilterDraft={issueLabelFilterDraft}
                savedViewName={savedViewName}
                signalQuery={signalQuery}
                activityQuery={activityQuery}
                activityActionFilter={activityActionFilter}
                activityEntityTypeFilter={activityEntityTypeFilter}
                activityActorKindFilter={activityActorKindFilter}
                onSelectIssue={setSelectedIssueId}
                onSelectSignal={setSelectedSignalId}
                onSelectRun={(runId) => {
                  setSelectedRunId(runId)
                  const run = runs.find((entry) => entry.run_id === runId)
                  if (run && run.issue_id !== 'workspace-query') {
                    setSelectedIssueId(run.issue_id)
                  }
                }}
                onSelectSource={setSelectedSourceId}
                onSelectActivity={setSelectedActivityId}
                onSelectPreset={applyQueuePreset}
                onClearPresetAndViews={() => clearIssueQueueState(activeView)}
                onSelectSavedView={setIssueFiltersFromView}
                onFiltersChange={(nextFilters) => {
                  setActivePresetId(null)
                  setActiveSavedViewId(null)
                  setSavedViewName('')
                  setIssueFilters(nextFilters)
                }}
                onIssueLabelsDraftChange={(value) => {
                  setActivePresetId(null)
                  setActiveSavedViewId(null)
                  setSavedViewName('')
                  setIssueLabelFilterDraft(value)
                  setIssueFilters((current) => ({ ...current, labels: parseLabelDraft(value) }))
                }}
                onSavedViewNameChange={setSavedViewName}
                onSaveView={() => void handleSaveView()}
                onUpdateView={() => void handleUpdateView()}
                onDeleteView={() => void handleDeleteView()}
                onSignalQueryChange={setSignalQuery}
                onActivityQueryChange={setActivityQuery}
                onActivityActionFilterChange={setActivityActionFilter}
                onActivityEntityTypeFilterChange={setActivityEntityTypeFilter}
                onActivityActorKindFilterChange={setActivityActorKindFilter}
                onNavigateTree={setTreePath}
              />

              <DetailPane
                activeView={activeView}
                selectedIssue={selectedIssue}
                selectedSignal={selectedSignal}
                selectedRun={selectedRun}
                selectedSource={selectedSource}
                selectedActivity={selectedActivity}
                activityOverview={activityOverview}
                workspaceActivity={workspaceActivity}
                issueActivity={issueActivity}
                runActivity={runActivity}
                issueDrift={issueDrift}
                issueQuality={issueQuality}
                duplicateMatches={duplicateMatches}
                triageSuggestion={triageSuggestion}
                runMetrics={selectedRunMetrics}
                costSummary={costSummary}
                coverageDelta={coverageDelta}
                testSuggestions={testSuggestions}
                patchCritique={patchCritique}
                runImprovements={runImprovements}
                runInsight={runInsight}
                issueContextPacket={issueContextPacket}
                workspaceGuidance={workspaceGuidance}
                repoMap={repoMap}
                verificationProfiles={verificationProfiles}
                verificationProfileHistory={verificationProfileHistory}
                verificationProfileReports={verificationProfileReports}
                evalScenarios={evalScenarios}
                evalReport={evalReport}
                issueContextReplays={issueContextReplays}
                contextReplayLabelDraft={contextReplayLabelDraft}
                selectedContextReplayId={selectedContextReplayId}
                contextReplayComparison={contextReplayComparison}
                selectedTicketContextId={selectedTicketContextId}
                ticketContextProviderDraft={ticketContextProviderDraft}
                ticketContextExternalIdDraft={ticketContextExternalIdDraft}
                ticketContextTitleDraft={ticketContextTitleDraft}
                ticketContextSummaryDraft={ticketContextSummaryDraft}
                ticketContextCriteriaDraft={ticketContextCriteriaDraft}
                ticketContextLinksDraft={ticketContextLinksDraft}
                ticketContextLabelsDraft={ticketContextLabelsDraft}
                ticketContextStatusDraft={ticketContextStatusDraft}
                ticketContextSourceExcerptDraft={ticketContextSourceExcerptDraft}
                selectedThreatModelId={selectedThreatModelId}
                threatModelTitleDraft={threatModelTitleDraft}
                threatModelMethodologyDraft={threatModelMethodologyDraft}
                threatModelSummaryDraft={threatModelSummaryDraft}
                threatModelAssetsDraft={threatModelAssetsDraft}
                threatModelEntryPointsDraft={threatModelEntryPointsDraft}
                threatModelTrustBoundariesDraft={threatModelTrustBoundariesDraft}
                threatModelAbuseCasesDraft={threatModelAbuseCasesDraft}
                threatModelMitigationsDraft={threatModelMitigationsDraft}
                threatModelReferencesDraft={threatModelReferencesDraft}
                threatModelStatusDraft={threatModelStatusDraft}
                selectedBrowserDumpId={selectedBrowserDumpId}
                browserDumpSourceDraft={browserDumpSourceDraft}
                browserDumpLabelDraft={browserDumpLabelDraft}
                browserDumpPageUrlDraft={browserDumpPageUrlDraft}
                browserDumpPageTitleDraft={browserDumpPageTitleDraft}
                browserDumpSummaryDraft={browserDumpSummaryDraft}
                browserDumpDomSnapshotDraft={browserDumpDomSnapshotDraft}
                browserDumpConsoleDraft={browserDumpConsoleDraft}
                browserDumpNetworkDraft={browserDumpNetworkDraft}
                browserDumpScreenshotPathDraft={browserDumpScreenshotPathDraft}
                browserDumpNotesDraft={browserDumpNotesDraft}
                issueSeverityDraft={issueSeverityDraft}
                issueStatusDraft={issueStatusDraft}
                issueDocStatusDraft={issueDocStatusDraft}
                issueCodeStatusDraft={issueCodeStatusDraft}
                issueLabelsDraft={issueLabelsDraft}
                issueNotesDraft={issueNotesDraft}
                issueFollowupDraft={issueFollowupDraft}
                newIssueTitleDraft={newIssueTitleDraft}
                newIssueSeverityDraft={newIssueSeverityDraft}
                newIssueSummaryDraft={newIssueSummaryDraft}
                newIssueLabelsDraft={newIssueLabelsDraft}
                fixSummaryDraft={fixSummaryDraft}
                fixChangedFilesDraft={fixChangedFilesDraft}
                fixTestsDraft={fixTestsDraft}
                fixHowDraft={fixHowDraft}
                fixIssueStatusDraft={fixIssueStatusDraft}
                fixRunIdDraft={fixRunIdDraft}
                driftSummary={driftSummary}
                snapshotRootPath={snapshot.workspace.root_path}
                latestLedger={snapshot.latest_ledger}
                latestVerdicts={snapshot.latest_verdicts}
                loading={loading}
                logContent={logContent}
                onIssueSeverityChange={setIssueSeverityDraft}
                onIssueStatusChange={setIssueStatusDraft}
                onIssueDocStatusChange={setIssueDocStatusDraft}
                onIssueCodeStatusChange={setIssueCodeStatusDraft}
                onSelectedTicketContextChange={setSelectedTicketContextId}
                onTicketContextProviderChange={setTicketContextProviderDraft}
                onTicketContextExternalIdChange={setTicketContextExternalIdDraft}
                onTicketContextTitleChange={setTicketContextTitleDraft}
                onTicketContextSummaryChange={setTicketContextSummaryDraft}
                onTicketContextCriteriaChange={setTicketContextCriteriaDraft}
                onTicketContextLinksChange={setTicketContextLinksDraft}
                onTicketContextLabelsChange={setTicketContextLabelsDraft}
                onTicketContextStatusChange={setTicketContextStatusDraft}
                onTicketContextSourceExcerptChange={setTicketContextSourceExcerptDraft}
                onSelectedThreatModelChange={setSelectedThreatModelId}
                onThreatModelTitleChange={setThreatModelTitleDraft}
                onThreatModelMethodologyChange={setThreatModelMethodologyDraft}
                onThreatModelSummaryChange={setThreatModelSummaryDraft}
                onThreatModelAssetsChange={setThreatModelAssetsDraft}
                onThreatModelEntryPointsChange={setThreatModelEntryPointsDraft}
                onThreatModelTrustBoundariesChange={setThreatModelTrustBoundariesDraft}
                onThreatModelAbuseCasesChange={setThreatModelAbuseCasesDraft}
                onThreatModelMitigationsChange={setThreatModelMitigationsDraft}
                onThreatModelReferencesChange={setThreatModelReferencesDraft}
                onThreatModelStatusChange={setThreatModelStatusDraft}
                onSelectedBrowserDumpChange={setSelectedBrowserDumpId}
                onBrowserDumpSourceChange={setBrowserDumpSourceDraft}
                onBrowserDumpLabelChange={setBrowserDumpLabelDraft}
                onBrowserDumpPageUrlChange={setBrowserDumpPageUrlDraft}
                onBrowserDumpPageTitleChange={setBrowserDumpPageTitleDraft}
                onBrowserDumpSummaryChange={setBrowserDumpSummaryDraft}
                onBrowserDumpDomSnapshotChange={setBrowserDumpDomSnapshotDraft}
                onBrowserDumpConsoleChange={setBrowserDumpConsoleDraft}
                onBrowserDumpNetworkChange={setBrowserDumpNetworkDraft}
                onBrowserDumpScreenshotPathChange={setBrowserDumpScreenshotPathDraft}
                onBrowserDumpNotesChange={setBrowserDumpNotesDraft}
                onContextReplayLabelChange={setContextReplayLabelDraft}
                onSelectContextReplay={setSelectedContextReplayId}
                onIssueLabelsChange={setIssueLabelsDraft}
                onIssueNotesChange={setIssueNotesDraft}
                onIssueFollowupChange={setIssueFollowupDraft}
                onNewIssueTitleChange={setNewIssueTitleDraft}
                onNewIssueSeverityChange={setNewIssueSeverityDraft}
                onNewIssueSummaryChange={setNewIssueSummaryDraft}
                onNewIssueLabelsChange={setNewIssueLabelsDraft}
                onFixSummaryChange={setFixSummaryDraft}
                onFixChangedFilesChange={setFixChangedFilesDraft}
                onFixTestsChange={setFixTestsDraft}
                onFixHowChange={setFixHowDraft}
                onFixIssueStatusChange={setFixIssueStatusDraft}
                onUseSelectedRunForFix={handleUseSelectedRunForFix}
                onClearFixRun={() => setFixRunIdDraft(null)}
                onSaveIssue={() => void handleSaveIssue()}
                onCreateIssue={() => void handleCreateTrackerIssue()}
                onRecordFix={() => void handleRecordFix()}
                onAcceptRunDraft={() => void handleAcceptRunDraft()}
                onDismissRunReview={() => void handleReviewRun('dismissed')}
                onMarkRunInvestigationOnly={() => void handleReviewRun('investigation_only')}
                onPromoteSignal={() => selectedSignal && void handlePromoteSignal(selectedSignal)}
                onRefreshIssueAnalysis={() => void handleRefreshIssueAnalysis()}
                onGenerateTestSuggestions={() => void handleGenerateTestSuggestions()}
                onGeneratePatchCritique={() => void handleGeneratePatchCritique()}
                onDismissImprovement={(suggestionId) => void handleDismissImprovement(suggestionId)}
                onSaveTicketContext={() => void handleSaveTicketContext()}
                onDeleteTicketContext={() => void handleDeleteTicketContext()}
                onSaveThreatModel={() => void handleSaveThreatModel()}
                onDeleteThreatModel={() => void handleDeleteThreatModel()}
                onSaveBrowserDump={() => void handleSaveBrowserDump()}
                onDeleteBrowserDump={() => void handleDeleteBrowserDump()}
                onCaptureIssueContextReplay={() => void handleCaptureIssueContextReplay()}
                onCompareIssueContextReplay={(replayId) => void handleCompareIssueContextReplay(replayId)}
                onRunEvalScenario={(scenarioId) => void handleRunEvalScenario(scenarioId)}
                onReplayAllEvalScenarios={() => void handleReplayAllEvalScenarios()}
                onRetryRun={() => void handleRetryRun()}
                onCancelRun={() => void handleCancelRun()}
                selectedRunPlan={selectedRunPlan}
                onGeneratePlan={() => void handleGeneratePlan()}
                onApprovePlan={(feedback) => void handleApprovePlan(feedback)}
                onRejectPlan={(reason) => void handleRejectPlan(reason)}
              />

              {executionOpen ? (
                <ExecutionPane
                  runtime={runtime}
                  model={model}
                  runtimeModels={runtimeModels}
                  capabilities={capabilities}
                  instruction={instruction}
                  planningMode={planningMode}
                  queryPrompt={queryPrompt}
                  issueContextPacket={issueContextPacket}
                  selectedRunbookId={selectedRunbookId}
                  runbookNameDraft={runbookNameDraft}
                  runbookDescriptionDraft={runbookDescriptionDraft}
                  runbookTemplateDraft={runbookTemplateDraft}
                  verificationProfiles={verificationProfiles}
                  selectedVerificationProfileId={selectedVerificationProfileId}
                  verificationProfileNameDraft={verificationProfileNameDraft}
                  verificationProfileDescriptionDraft={verificationProfileDescriptionDraft}
                  verificationProfileTestCommandDraft={verificationProfileTestCommandDraft}
                  verificationProfileCoverageCommandDraft={verificationProfileCoverageCommandDraft}
                  verificationProfileCoveragePathDraft={verificationProfileCoveragePathDraft}
                  verificationProfileCoverageFormatDraft={verificationProfileCoverageFormatDraft}
                  verificationProfileRuntimeDraft={verificationProfileRuntimeDraft}
                  verificationProfileRetryDraft={verificationProfileRetryDraft}
                  verificationProfileSourcePathsDraft={verificationProfileSourcePathsDraft}
                  verificationProfileChecklistDraft={verificationProfileChecklistDraft}
                  settings={settings}
                  terminalId={terminalId}
                  terminalInput={terminalInput}
                  terminalOutput={terminalOutput}
                  runtimeProbe={runtimeProbe}
                  runtimeProbeLoading={runtimeProbeLoading}
                  selectedIssue={selectedIssue}
                  loading={loading}
                  onRuntimeChange={(nextRuntime) => {
                    setRuntime(nextRuntime)
                    setSettings((current) => ({ ...(current ?? { local_agent_type: nextRuntime }), local_agent_type: nextRuntime }))
                  }}
                  onModelChange={(nextModel) => {
                    setModel(nextModel)
                    setSettings((current) => ({
                      ...(current ?? { local_agent_type: runtime }),
                      [runtime === 'codex' ? 'codex_model' : 'opencode_model']: nextModel,
                    }))
                  }}
                  onInstructionChange={setInstruction}
                  onPlanningModeChange={setPlanningMode}
                  onQueryPromptChange={setQueryPrompt}
                  onSelectedRunbookChange={setSelectedRunbookId}
                  onRunbookNameDraftChange={setRunbookNameDraft}
                  onRunbookDescriptionDraftChange={setRunbookDescriptionDraft}
                  onRunbookTemplateDraftChange={setRunbookTemplateDraft}
                  onLoadRunbookInstruction={handleLoadRunbook}
                  onApplySelectedRunbook={handleApplySelectedRunbook}
                  onSaveRunbook={() => void handleSaveRunbook()}
                  onDeleteRunbook={() => void handleDeleteRunbook()}
                  onSelectedVerificationProfileChange={setSelectedVerificationProfileId}
                  onVerificationProfileNameDraftChange={setVerificationProfileNameDraft}
                  onVerificationProfileDescriptionDraftChange={setVerificationProfileDescriptionDraft}
                  onVerificationProfileTestCommandDraftChange={setVerificationProfileTestCommandDraft}
                  onVerificationProfileCoverageCommandDraftChange={setVerificationProfileCoverageCommandDraft}
                  onVerificationProfileCoveragePathDraftChange={setVerificationProfileCoveragePathDraft}
                  onVerificationProfileCoverageFormatDraftChange={setVerificationProfileCoverageFormatDraft}
                  onVerificationProfileRuntimeDraftChange={setVerificationProfileRuntimeDraft}
                  onVerificationProfileRetryDraftChange={setVerificationProfileRetryDraft}
                  onVerificationProfileSourcePathsDraftChange={setVerificationProfileSourcePathsDraft}
                  onVerificationProfileChecklistDraftChange={setVerificationProfileChecklistDraft}
                  onApplySelectedVerificationProfile={handleApplyVerificationProfile}
                  onSaveVerificationProfile={() => void handleSaveVerificationProfile()}
                  onDeleteVerificationProfile={() => void handleDeleteVerificationProfile()}
                  onSettingsChange={setSettings}
                  onSaveSettings={() => void handleSaveSettings()}
                  onRefreshCapabilities={() => void handleRefreshCapabilities()}
                  onProbeRuntime={() => void handleProbeRuntime()}
                  onOpenTerminal={() => void handleOpenTerminal()}
                  onCloseTerminal={() => void handleCloseTerminal()}
                  onTerminalInputChange={setTerminalInput}
                  onSendTerminal={() => void handleSendTerminal()}
                  onQueryAgent={() => void handleQueryAgent()}
                  onRunIssue={() => void handleRunIssue()}
                  onOpenRuns={() => setActiveView('runs')}
                  workspaceLoaded
                />
              ) : null}
            </section>
          </>
        )}
      </main>
    </div>
  )
}

export default App
