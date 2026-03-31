import { startTransition, useDeferredValue, useEffect, useMemo, useRef, useState } from 'react'
import './index.css'
import {
  cancelRun,
  createIssue,
  closeTerminal,
  createSavedView,
  deleteSavedView,
  deleteRunbook,
  exportWorkspace,
  acceptRunReview,
  getHealth,
  getCapabilities,
  getSettings,
  issueWork,
  listActivity,
  listIssues,
  listRuns,
  listSavedViews,
  listSignals,
  listSources,
  listTree,
  listWorkspaces,
  loadWorkspace,
  openTerminal,
  probeRuntime,
  promoteSignal,
  queryAgent,
  readActivityOverview,
  readDrift,
  readIssueDrift,
  readSnapshot,
  readRun,
  readRunLog,
  readTerminal,
  readWorktree,
  recordFix,
  reviewRun,
  retryRun,
  scanWorkspace,
  saveRunbook,
  startRun,
  suggestFixDraft,
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
  DiscoverySignal,
  IssueDriftDetail,
  IssueContextPacket,
  IssueQueueFilters,
  IssueRecord,
  LocalAgentCapabilities,
  RunRecord,
  RuntimeProbeResult,
  SavedIssueView,
  SourceRecord,
  TreeNode,
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
  const [queryPrompt, setQueryPrompt] = useState('Summarize the current workspace focus and next actions in 5 bullets.')
  const [executionOpen, setExecutionOpen] = useState(false)
  const [selectedRunbookId, setSelectedRunbookId] = useState('fix')
  const [runbookNameDraft, setRunbookNameDraft] = useState('')
  const [runbookDescriptionDraft, setRunbookDescriptionDraft] = useState('')
  const [runbookTemplateDraft, setRunbookTemplateDraft] = useState('')
  const [logContent, setLogContent] = useState('')
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
  const [activity, setActivity] = useState<ActivityRecord[]>([])
  const [activityOverview, setActivityOverview] = useState<ActivityOverview | null>(null)
  const [worktree, setWorktree] = useState<WorktreeStatus | null>(null)
  const [driftSummary, setDriftSummary] = useState<Record<string, number>>({})
  const [issueDrift, setIssueDrift] = useState<IssueDriftDetail | null>(null)
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
    () => runs.find((run) => run.status === 'queued' || run.status === 'running') ?? null,
    [runs],
  )

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
      needs_followup: nextFilters.needs_followup === true ? true : undefined,
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

  async function refreshWorkspaceData(workspaceId: string) {
    const [nextRuns, nextSources, nextDriftSummary, nextViews, nextActivity, nextOverview, nextWorktree] = await Promise.all([
      listRuns(workspaceId),
      listSources(workspaceId),
      readDrift(workspaceId),
      listSavedViews(workspaceId),
      listActivity(workspaceId, { limit: 120 }),
      readActivityOverview(workspaceId, 120),
      readWorktree(workspaceId),
    ])
    startTransition(() => {
      setRuns(nextRuns)
      setSources(nextSources)
      setDriftSummary(nextDriftSummary)
      setSavedViews(nextViews)
      setActivity(nextActivity)
      setActivityOverview(nextOverview)
      setWorktree(nextWorktree)
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
    if (!snapshot?.workspace.workspace_id) return
    void refreshWorkspaceData(snapshot.workspace.workspace_id).catch((nextError) =>
      setError(nextError instanceof Error ? nextError.message : String(nextError)),
    )
  }, [snapshot?.workspace.workspace_id, snapshot?.generated_at])

  useEffect(() => {
    if (!snapshot?.workspace.workspace_id) return
    if (activeView !== 'tree') return
    void listTree(snapshot.workspace.workspace_id, treePath)
      .then(setTreeNodes)
      .catch((nextError) => setError(nextError instanceof Error ? nextError.message : String(nextError)))
  }, [snapshot?.workspace.workspace_id, treePath, activeView])

  useEffect(() => {
    if (!snapshot?.workspace.workspace_id) return
    void refreshIssueQueue().catch((nextError) =>
      setError(nextError instanceof Error ? nextError.message : String(nextError)),
    )
  }, [snapshot?.workspace.workspace_id, snapshot?.generated_at, issueFilters, activeView])

  useEffect(() => {
    if (!snapshot?.workspace.workspace_id) return
    if (activeView !== 'signals') return
    void refreshSignalQueue().catch((nextError) =>
      setError(nextError instanceof Error ? nextError.message : String(nextError)),
    )
  }, [snapshot?.workspace.workspace_id, snapshot?.generated_at, signalQuery, activeView])

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
    if (!snapshot?.workspace.workspace_id || !selectedIssue) return
    void issueWork(snapshot.workspace.workspace_id, selectedIssue.bug_id, selectedRunbookId || undefined)
      .then(setIssueContextPacket)
      .catch((nextError) => setError(nextError instanceof Error ? nextError.message : String(nextError)))
    void readIssueDrift(snapshot.workspace.workspace_id, selectedIssue.bug_id)
      .then(setIssueDrift)
      .catch((nextError) => setError(nextError instanceof Error ? nextError.message : String(nextError)))
  }, [snapshot?.workspace.workspace_id, selectedIssue?.bug_id, selectedRunbookId])

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
  }, [selectedIssue?.bug_id])

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
    if (activeView !== 'runs' && activeView !== 'review') return
    if (!snapshot?.workspace.workspace_id || !selectedRun) return
    setLogContent('')
    logOffsetRef.current = 0

    const poll = async () => {
      try {
        const [runRecord, log] = await Promise.all([
          readRun(snapshot.workspace.workspace_id, selectedRun.run_id),
          readRunLog(snapshot.workspace.workspace_id, selectedRun.run_id, logOffsetRef.current),
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
          await refreshActivityData(snapshot.workspace.workspace_id)
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
  }, [activeView, snapshot?.workspace.workspace_id, selectedRunId])

  useEffect(() => {
    if (!snapshot?.workspace.workspace_id || !activeLiveRun) return

    const poll = async () => {
      try {
        const runRecord = await readRun(snapshot.workspace.workspace_id, activeLiveRun.run_id)
        setRuns((currentRuns) => {
          const next = currentRuns.filter((item) => item.run_id !== runRecord.run_id)
          return [runRecord, ...next]
        })
        if (runRecord.status === 'completed' || runRecord.status === 'failed' || runRecord.status === 'cancelled') {
          await refreshActivityData(snapshot.workspace.workspace_id)
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
  }, [snapshot?.workspace.workspace_id, activeLiveRun?.run_id])

  useEffect(() => {
    if (!snapshot?.workspace.workspace_id || !terminalId) return

    const poll = async () => {
      try {
        const log = await readTerminal(terminalId, snapshot.workspace.workspace_id, terminalOffsetRef.current)
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
  }, [snapshot?.workspace.workspace_id, terminalId])

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

  async function handleRunIssue() {
    if (!snapshot || !selectedIssue || !model) return
    setLoading(true)
    try {
      const run = await startRun(snapshot.workspace.workspace_id, selectedIssue.bug_id, {
        runtime,
        model,
        instruction,
        runbook_id: selectedRunbookId || undefined,
      })
      setRuns((current) => [run, ...current])
      setSelectedRunId(run.run_id)
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
        onWorkspacePathChange={setWorkspacePath}
        onLoadWorkspace={() => void loadWorkspaceState(workspacePath)}
        onScanWorkspace={() => void handleScan()}
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
                sources={sources}
                treeNodes={treeNodes}
                treePath={treePath}
                activity={filteredActivity}
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
                issueContextPacket={issueContextPacket}
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
                onRetryRun={() => void handleRetryRun()}
                onCancelRun={() => void handleCancelRun()}
              />

              {executionOpen ? (
                <ExecutionPane
                  runtime={runtime}
                  model={model}
                  runtimeModels={runtimeModels}
                  capabilities={capabilities}
                  instruction={instruction}
                  queryPrompt={queryPrompt}
                  issueContextPacket={issueContextPacket}
                  selectedRunbookId={selectedRunbookId}
                  runbookNameDraft={runbookNameDraft}
                  runbookDescriptionDraft={runbookDescriptionDraft}
                  runbookTemplateDraft={runbookTemplateDraft}
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
                  onQueryPromptChange={setQueryPrompt}
                  onSelectedRunbookChange={setSelectedRunbookId}
                  onRunbookNameDraftChange={setRunbookNameDraft}
                  onRunbookDescriptionDraftChange={setRunbookDescriptionDraft}
                  onRunbookTemplateDraftChange={setRunbookTemplateDraft}
                  onLoadRunbookInstruction={handleLoadRunbook}
                  onApplySelectedRunbook={handleApplySelectedRunbook}
                  onSaveRunbook={() => void handleSaveRunbook()}
                  onDeleteRunbook={() => void handleDeleteRunbook()}
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
