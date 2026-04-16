import type {
  ActivityRecord,
  ActivityOverview,
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
  IssueContextPacket,
  IssueDriftDetail,
  IssueQualityScore,
  IssueRecord,
  PatchCritique,
  RepoMapSummary,
  RepoGuidanceRecord,
  RunMetrics,
  RunPlan,
  RunRecord,
  RunSessionInsight,
  SourceRecord,
  TestSuggestion,
  ThreatModelRecord,
  TicketContextRecord,
  TriageSuggestion,
  VerificationProfileRecord,
  VerificationProfileExecutionResult,
  VerificationProfileReport,
  ViewMode,
} from '../lib/types'
import { SummaryCard, StatusPill } from './TrackerPrimitives'
import { formatDate } from '../lib/format'
import { ActivityDigestSummary } from './ActivityDigestSummary'
import { CostSummaryPanel } from './CostSummary'
import { PlanPreview } from './PlanPreview'

function qualityAccent(score?: number | null) {
  if (score === null || score === undefined) return 'sand'
  if (score >= 80) return 'green'
  if (score >= 55) return 'amber'
  return 'red'
}

function replayDeltaBlock(label: string, added: string[], removed: string[]) {
  if (!added.length && !removed.length) return null
  return (
    <div className="field-stack">
      <span className="filter-label">{label}</span>
      {added.length ? <div className="detail-mini-block">Added: {added.join(', ')}</div> : null}
      {removed.length ? <div className="detail-mini-block">Removed: {removed.join(', ')}</div> : null}
    </div>
  )
}

function dimensionSummaryTags(
  label: string,
  items: VerificationProfileReport['runtime_breakdown'],
) {
  if (!items.length) return null
  return (
    <div className="tag-row">
      {items.slice(0, 3).map((item) => (
        <span key={`${label}-${item.key}`} className="tag">
          {label}: {item.label} ({item.success_rate}% / {item.total_runs})
        </span>
      ))}
    </div>
  )
}

function confidenceTone(confidence?: number | null) {
  if (confidence === null || confidence === undefined) return 'unknown'
  if (confidence >= 0.75) return 'fixed'
  if (confidence >= 0.45) return 'running'
  return 'failed'
}

function deltaAccent(delta?: number | null) {
  if (delta === null || delta === undefined) return 'sand'
  if (delta > 0) return 'green'
  if (delta < 0) return 'red'
  return 'sand'
}

function guidanceTone(kind: RepoGuidanceRecord['kind']) {
  if (kind === 'agent_instructions') return 'codex'
  if (kind === 'conventions') return 'blue'
  if (kind === 'skill') return 'green'
  if (kind === 'repo_index') return 'amber'
  return 'sand'
}

function acceptanceTone(status?: 'met' | 'partial' | 'not_met' | 'unknown' | null) {
  if (status === 'met') return 'green'
  if (status === 'partial') return 'amber'
  if (status === 'not_met') return 'red'
  return 'sand'
}

function GuidanceList({
  guidance,
  emptyLabel,
}: {
  guidance: RepoGuidanceRecord[]
  emptyLabel: string
}) {
  if (!guidance.length) {
    return <p className="subtle">{emptyLabel}</p>
  }
  return (
    <div className="activity-list">
      {guidance.map((item) => (
        <div key={item.guidance_id} className="activity-entry">
          <div className="activity-entry-top">
            <strong>{item.title}</strong>
            <small>{item.path}</small>
          </div>
          <div className="row-meta">
            <StatusPill tone={guidanceTone(item.kind)}>{item.kind.replace(/_/g, ' ')}</StatusPill>
            <span className="tag">{item.always_on ? 'always-on' : 'optional'}</span>
            {item.updated_at ? <span className="tag">{formatDate(item.updated_at)}</span> : null}
          </div>
          <p>{item.summary || 'No guidance summary available.'}</p>
          {item.trigger_keywords.length ? (
            <div className="tag-row">
              {item.trigger_keywords.map((keyword) => (
                <span key={`${item.guidance_id}-${keyword}`} className="tag">
                  {keyword}
                </span>
              ))}
            </div>
          ) : null}
          {item.excerpt ? <pre className="detail-mini-block">{item.excerpt}</pre> : null}
        </div>
      ))}
    </div>
  )
}

function WorktreeSnapshot({ label, worktree }: { label: string; worktree?: IssueContextPacket['worktree'] | RunRecord['worktree'] }) {
  if (!worktree?.available) return null
  return (
    <section className="detail-section">
      <h4>{label}</h4>
      <div className="evidence-row">
        <span>Branch</span>
        <small>{worktree.branch ?? (worktree.is_git_repo ? 'detached' : 'not a git repo')}</small>
      </div>
      <div className="evidence-row">
        <span>Head</span>
        <small>{worktree.head_sha ?? 'unknown'}</small>
      </div>
      <div className="evidence-row">
        <span>Dirty files</span>
        <small>
          {worktree.dirty_files}
          {worktree.ahead || worktree.behind ? ` · +${worktree.ahead}/-${worktree.behind}` : ''}
        </small>
      </div>
      {worktree.dirty_paths.length ? (
        <div className="tag-row">
          {worktree.dirty_paths.slice(0, 8).map((path) => (
            <span key={path} className="tag">
              {path}
            </span>
          ))}
        </div>
      ) : (
        <p className="subtle">No dirty paths captured.</p>
      )}
    </section>
  )
}

type Props = {
  activeView: ViewMode
  selectedIssue: IssueRecord | null
  selectedSignal: DiscoverySignal | null
  selectedRun: RunRecord | null
  selectedSource: SourceRecord | null
  selectedActivity: ActivityRecord | null
  activityOverview: ActivityOverview | null
  workspaceActivity: ActivityRecord[]
  issueActivity: ActivityRecord[]
  runActivity: ActivityRecord[]
  issueDrift: IssueDriftDetail | null
  issueQuality: IssueQualityScore | null
  duplicateMatches: DuplicateMatch[]
  triageSuggestion: TriageSuggestion | null
  runMetrics: RunMetrics | null
  costSummary: CostSummary | null
  coverageDelta: CoverageDelta | null
  testSuggestions: TestSuggestion[]
  patchCritique: PatchCritique | null
  runImprovements: ImprovementSuggestion[]
  runInsight: RunSessionInsight | null
  issueContextPacket: IssueContextPacket | null
  workspaceGuidance: RepoGuidanceRecord[]
  repoMap: RepoMapSummary | null
  verificationProfiles: VerificationProfileRecord[]
  verificationProfileHistory: VerificationProfileExecutionResult[]
  verificationProfileReports: VerificationProfileReport[]
  evalScenarios: EvalScenarioRecord[]
  evalReport: EvalWorkspaceReport | null
  issueContextReplays: IssueContextReplayRecord[]
  contextReplayLabelDraft: string
  selectedContextReplayId: string
  contextReplayComparison: IssueContextReplayComparison | null
  selectedTicketContextId: string
  ticketContextProviderDraft: TicketContextRecord['provider']
  ticketContextExternalIdDraft: string
  ticketContextTitleDraft: string
  ticketContextSummaryDraft: string
  ticketContextCriteriaDraft: string
  ticketContextLinksDraft: string
  ticketContextLabelsDraft: string
  ticketContextStatusDraft: string
  ticketContextSourceExcerptDraft: string
  selectedThreatModelId: string
  threatModelTitleDraft: string
  threatModelMethodologyDraft: ThreatModelRecord['methodology']
  threatModelSummaryDraft: string
  threatModelAssetsDraft: string
  threatModelEntryPointsDraft: string
  threatModelTrustBoundariesDraft: string
  threatModelAbuseCasesDraft: string
  threatModelMitigationsDraft: string
  threatModelReferencesDraft: string
  threatModelStatusDraft: ThreatModelRecord['status']
  selectedBrowserDumpId: string
  browserDumpSourceDraft: BrowserDumpRecord['source']
  browserDumpLabelDraft: string
  browserDumpPageUrlDraft: string
  browserDumpPageTitleDraft: string
  browserDumpSummaryDraft: string
  browserDumpDomSnapshotDraft: string
  browserDumpConsoleDraft: string
  browserDumpNetworkDraft: string
  browserDumpScreenshotPathDraft: string
  browserDumpNotesDraft: string
  issueSeverityDraft: string
  issueStatusDraft: string
  issueDocStatusDraft: string
  issueCodeStatusDraft: string
  issueLabelsDraft: string
  issueNotesDraft: string
  issueFollowupDraft: boolean
  newIssueTitleDraft: string
  newIssueSeverityDraft: string
  newIssueSummaryDraft: string
  newIssueLabelsDraft: string
  fixSummaryDraft: string
  fixChangedFilesDraft: string
  fixTestsDraft: string
  fixHowDraft: string
  fixIssueStatusDraft: string
  fixRunIdDraft: string | null
  driftSummary: Record<string, number>
  snapshotRootPath?: string | null
  latestLedger?: string | null
  latestVerdicts?: string | null
  loading: boolean
  logContent: string
  onIssueSeverityChange: (value: string) => void
  onIssueStatusChange: (value: string) => void
  onIssueDocStatusChange: (value: string) => void
  onIssueCodeStatusChange: (value: string) => void
  onSelectedTicketContextChange: (value: string) => void
  onTicketContextProviderChange: (value: TicketContextRecord['provider']) => void
  onTicketContextExternalIdChange: (value: string) => void
  onTicketContextTitleChange: (value: string) => void
  onTicketContextSummaryChange: (value: string) => void
  onTicketContextCriteriaChange: (value: string) => void
  onTicketContextLinksChange: (value: string) => void
  onTicketContextLabelsChange: (value: string) => void
  onTicketContextStatusChange: (value: string) => void
  onTicketContextSourceExcerptChange: (value: string) => void
  onSelectedThreatModelChange: (value: string) => void
  onThreatModelTitleChange: (value: string) => void
  onThreatModelMethodologyChange: (value: ThreatModelRecord['methodology']) => void
  onThreatModelSummaryChange: (value: string) => void
  onThreatModelAssetsChange: (value: string) => void
  onThreatModelEntryPointsChange: (value: string) => void
  onThreatModelTrustBoundariesChange: (value: string) => void
  onThreatModelAbuseCasesChange: (value: string) => void
  onThreatModelMitigationsChange: (value: string) => void
  onThreatModelReferencesChange: (value: string) => void
  onThreatModelStatusChange: (value: ThreatModelRecord['status']) => void
  onSelectedBrowserDumpChange: (value: string) => void
  onBrowserDumpSourceChange: (value: BrowserDumpRecord['source']) => void
  onBrowserDumpLabelChange: (value: string) => void
  onBrowserDumpPageUrlChange: (value: string) => void
  onBrowserDumpPageTitleChange: (value: string) => void
  onBrowserDumpSummaryChange: (value: string) => void
  onBrowserDumpDomSnapshotChange: (value: string) => void
  onBrowserDumpConsoleChange: (value: string) => void
  onBrowserDumpNetworkChange: (value: string) => void
  onBrowserDumpScreenshotPathChange: (value: string) => void
  onBrowserDumpNotesChange: (value: string) => void
  onContextReplayLabelChange: (value: string) => void
  onSelectContextReplay: (value: string) => void
  onIssueLabelsChange: (value: string) => void
  onIssueNotesChange: (value: string) => void
  onIssueFollowupChange: (value: boolean) => void
  onNewIssueTitleChange: (value: string) => void
  onNewIssueSeverityChange: (value: string) => void
  onNewIssueSummaryChange: (value: string) => void
  onNewIssueLabelsChange: (value: string) => void
  onFixSummaryChange: (value: string) => void
  onFixChangedFilesChange: (value: string) => void
  onFixTestsChange: (value: string) => void
  onFixHowChange: (value: string) => void
  onFixIssueStatusChange: (value: string) => void
  onUseSelectedRunForFix: () => void
  onClearFixRun: () => void
  onSaveIssue: () => void
  onCreateIssue: () => void
  onRecordFix: () => void
  onAcceptRunDraft: () => void
  onDismissRunReview: () => void
  onMarkRunInvestigationOnly: () => void
  onPromoteSignal: () => void
  onRefreshIssueAnalysis: () => void
  onGenerateTestSuggestions: () => void
  onGeneratePatchCritique: () => void
  onDismissImprovement: (suggestionId: string) => void
  onSaveTicketContext: () => void
  onDeleteTicketContext: () => void
  onSaveThreatModel: () => void
  onDeleteThreatModel: () => void
  onSaveBrowserDump: () => void
  onDeleteBrowserDump: () => void
  onCaptureIssueContextReplay: () => void
  onCompareIssueContextReplay: (replayId: string) => void
  onRunEvalScenario: (scenarioId: string) => void
  onReplayAllEvalScenarios: () => void
  onRetryRun: () => void
  onCancelRun: () => void
  selectedRunPlan: RunPlan | null
  onGeneratePlan: () => void
  onApprovePlan: (feedback?: string) => void
  onRejectPlan: (reason: string) => void
}

export function DetailPane({
  activeView,
  selectedIssue,
  selectedSignal,
  selectedRun,
  selectedSource,
  selectedActivity,
  activityOverview,
  workspaceActivity,
  issueActivity,
  runActivity,
  issueDrift,
  issueQuality,
  duplicateMatches,
  triageSuggestion,
  runMetrics,
  costSummary,
  coverageDelta,
  testSuggestions,
  patchCritique,
  runImprovements,
  runInsight,
  issueContextPacket,
  workspaceGuidance,
  repoMap,
  verificationProfiles,
  verificationProfileHistory,
  verificationProfileReports,
  evalScenarios,
  evalReport,
  issueContextReplays,
  contextReplayLabelDraft,
  selectedContextReplayId,
  contextReplayComparison,
  selectedTicketContextId,
  ticketContextProviderDraft,
  ticketContextExternalIdDraft,
  ticketContextTitleDraft,
  ticketContextSummaryDraft,
  ticketContextCriteriaDraft,
  ticketContextLinksDraft,
  ticketContextLabelsDraft,
  ticketContextStatusDraft,
  ticketContextSourceExcerptDraft,
  selectedThreatModelId,
  threatModelTitleDraft,
  threatModelMethodologyDraft,
  threatModelSummaryDraft,
  threatModelAssetsDraft,
  threatModelEntryPointsDraft,
  threatModelTrustBoundariesDraft,
  threatModelAbuseCasesDraft,
  threatModelMitigationsDraft,
  threatModelReferencesDraft,
  threatModelStatusDraft,
  selectedBrowserDumpId,
  browserDumpSourceDraft,
  browserDumpLabelDraft,
  browserDumpPageUrlDraft,
  browserDumpPageTitleDraft,
  browserDumpSummaryDraft,
  browserDumpDomSnapshotDraft,
  browserDumpConsoleDraft,
  browserDumpNetworkDraft,
  browserDumpScreenshotPathDraft,
  browserDumpNotesDraft,
  issueSeverityDraft,
  issueStatusDraft,
  issueDocStatusDraft,
  issueCodeStatusDraft,
  issueLabelsDraft,
  issueNotesDraft,
  issueFollowupDraft,
  newIssueTitleDraft,
  newIssueSeverityDraft,
  newIssueSummaryDraft,
  newIssueLabelsDraft,
  fixSummaryDraft,
  fixChangedFilesDraft,
  fixTestsDraft,
  fixHowDraft,
  fixIssueStatusDraft,
  fixRunIdDraft,
  driftSummary,
  snapshotRootPath,
  latestLedger,
  latestVerdicts,
  loading,
  logContent,
  onIssueSeverityChange,
  onIssueStatusChange,
  onIssueDocStatusChange,
  onIssueCodeStatusChange,
  onSelectedTicketContextChange,
  onTicketContextProviderChange,
  onTicketContextExternalIdChange,
  onTicketContextTitleChange,
  onTicketContextSummaryChange,
  onTicketContextCriteriaChange,
  onTicketContextLinksChange,
  onTicketContextLabelsChange,
  onTicketContextStatusChange,
  onTicketContextSourceExcerptChange,
  onSelectedThreatModelChange,
  onThreatModelTitleChange,
  onThreatModelMethodologyChange,
  onThreatModelSummaryChange,
  onThreatModelAssetsChange,
  onThreatModelEntryPointsChange,
  onThreatModelTrustBoundariesChange,
  onThreatModelAbuseCasesChange,
  onThreatModelMitigationsChange,
  onThreatModelReferencesChange,
  onThreatModelStatusChange,
  onSelectedBrowserDumpChange,
  onBrowserDumpSourceChange,
  onBrowserDumpLabelChange,
  onBrowserDumpPageUrlChange,
  onBrowserDumpPageTitleChange,
  onBrowserDumpSummaryChange,
  onBrowserDumpDomSnapshotChange,
  onBrowserDumpConsoleChange,
  onBrowserDumpNetworkChange,
  onBrowserDumpScreenshotPathChange,
  onBrowserDumpNotesChange,
  onContextReplayLabelChange,
  onSelectContextReplay,
  onIssueLabelsChange,
  onIssueNotesChange,
  onIssueFollowupChange,
  onNewIssueTitleChange,
  onNewIssueSeverityChange,
  onNewIssueSummaryChange,
  onNewIssueLabelsChange,
  onFixSummaryChange,
  onFixChangedFilesChange,
  onFixTestsChange,
  onFixHowChange,
  onFixIssueStatusChange,
  onUseSelectedRunForFix,
  onClearFixRun,
  onSaveIssue,
  onCreateIssue,
  onRecordFix,
  onAcceptRunDraft,
  onDismissRunReview,
  onMarkRunInvestigationOnly,
  onPromoteSignal,
  onRefreshIssueAnalysis,
  onGenerateTestSuggestions,
  onGeneratePatchCritique,
  onDismissImprovement,
  onSaveTicketContext,
  onDeleteTicketContext,
  onSaveThreatModel,
  onDeleteThreatModel,
  onSaveBrowserDump,
  onDeleteBrowserDump,
  onCaptureIssueContextReplay,
  onCompareIssueContextReplay,
  onRunEvalScenario,
  onReplayAllEvalScenarios,
  onRetryRun,
  onCancelRun,
  selectedRunPlan,
  onGeneratePlan,
  onApprovePlan,
  onRejectPlan,
}: Props) {
  const selectedTicketContext =
    issueContextPacket?.ticket_contexts.find((item) => item.context_id === selectedTicketContextId) ?? null
  const selectedThreatModel =
    issueContextPacket?.threat_models.find((item) => item.threat_model_id === selectedThreatModelId) ?? null
  const selectedBrowserDump =
    issueContextPacket?.browser_dumps.find((item) => item.dump_id === selectedBrowserDumpId) ?? null

  return (
    <div className="panel detail-panel">
      {(activeView === 'issues' || activeView === 'drift' || activeView === 'review') && selectedIssue ? (
        <>
          <div className="panel-header">
            <div>
              <p className="eyebrow">{activeView === 'drift' ? 'Drift detail' : activeView === 'review' ? 'Review issue' : 'Issue detail'}</p>
              <h3>{activeView === 'drift' ? selectedIssue.bug_id : selectedIssue.title}</h3>
            </div>
            <div className="row-meta">
              <StatusPill tone={selectedIssue.severity.toLowerCase()}>{selectedIssue.severity}</StatusPill>
              <StatusPill tone={selectedIssue.issue_status}>{selectedIssue.issue_status}</StatusPill>
            </div>
          </div>

          {activeView !== 'drift' ? (
            <div className="detail-copy">
              <p>{selectedIssue.summary || 'No summary on record.'}</p>
              <p className="subtle">{selectedIssue.impact || 'No impact statement attached.'}</p>
            </div>
          ) : null}

          {activeView === 'review' && selectedRun ? (
            <section className="detail-section review-banner">
              <div className="panel-header">
                <div>
                  <h4>Review candidate</h4>
                  <p className="subtle">{selectedRun.run_id}</p>
                </div>
                <div className="row-meta">
                  <StatusPill tone={selectedRun.runtime}>{selectedRun.runtime}</StatusPill>
                  <StatusPill tone={selectedRun.status}>{selectedRun.status}</StatusPill>
                </div>
              </div>
              <pre className="detail-mini-block">{selectedRun.summary?.text_excerpt ?? 'No structured excerpt captured yet.'}</pre>
              <div className="toolbar-row">
                <button onClick={onAcceptRunDraft} disabled={loading}>
                  Accept draft as fix
                </button>
                <button className="ghost-button" onClick={onUseSelectedRunForFix} disabled={loading}>
                  Review fix draft
                </button>
                <button className="ghost-button" onClick={onMarkRunInvestigationOnly} disabled={loading}>
                  Mark investigation only
                </button>
                <button className="ghost-button" onClick={onDismissRunReview} disabled={loading}>
                  Dismiss review
                </button>
              </div>
            </section>
          ) : null}

          <section className="detail-section">
            <h4>Evidence</h4>
            {selectedIssue.evidence.concat(selectedIssue.verification_evidence).slice(0, 12).map((item, index) => (
              <div key={`${item.path}-${index}`} className="evidence-row">
                <span>
                  {item.path}
                  {item.line ? `:${item.line}` : ''}
                </span>
                <small>{item.excerpt || item.normalized_path || 'Path reference'}</small>
              </div>
            ))}
          </section>

          <section className="detail-section">
            <h4>Drift</h4>
            <div className="tag-row">
              {selectedIssue.review_ready_count > 0 ? (
                <span className="tag">review-ready: {selectedIssue.review_ready_count}</span>
              ) : null}
              {selectedIssue.drift_flags.length ? (
                selectedIssue.drift_flags.map((flag) => (
                  <span key={flag} className="tag">
                    {flag}
                  </span>
                ))
              ) : (
                <span className="subtle">No drift flags.</span>
              )}
            </div>
          </section>

          <section className="detail-section">
            <div className="panel-header">
              <div>
                <h4>Issue analysis</h4>
                <p className="subtle">Quality, likely duplicates, and triage guidance from the backend analysis endpoints.</p>
              </div>
              <button type="button" className="ghost-button" onClick={onRefreshIssueAnalysis} disabled={loading}>
                Refresh analysis
              </button>
            </div>
            <div className="summary-grid analysis-summary-grid">
              <SummaryCard
                label="Quality"
                value={issueQuality ? `${issueQuality.overall}%` : 'n/a'}
                accent={qualityAccent(issueQuality?.overall)}
              />
              <SummaryCard
                label="Completeness"
                value={issueQuality ? `${issueQuality.completeness}%` : 'n/a'}
                accent={qualityAccent(issueQuality?.completeness)}
              />
              <SummaryCard
                label="Clarity"
                value={issueQuality ? `${issueQuality.clarity}%` : 'n/a'}
                accent={qualityAccent(issueQuality?.clarity)}
              />
              <SummaryCard
                label="Evidence"
                value={issueQuality ? `${issueQuality.evidence_quality}%` : 'n/a'}
                accent={qualityAccent(issueQuality?.evidence_quality)}
              />
            </div>
            {issueQuality ? (
              <>
                <div className="tag-row">
                  <span className="tag">evidence refs: {issueQuality.evidence_count}</span>
                  <span className="tag">title chars: {issueQuality.title_length}</span>
                  <span className="tag">summary chars: {issueQuality.summary_length}</span>
                  <span className="tag">{issueQuality.has_repro ? 'has repro' : 'missing repro'}</span>
                  <span className="tag">{issueQuality.has_impact ? 'has impact' : 'missing impact'}</span>
                </div>
                {issueQuality.suggestions.length ? (
                  <div className="analysis-list">
                    {issueQuality.suggestions.map((suggestion) => (
                      <div key={suggestion} className="evidence-row">
                        <span>{suggestion}</span>
                        <small>quality suggestion</small>
                      </div>
                    ))}
                  </div>
                ) : (
                  <p className="subtle">No quality suggestions right now.</p>
                )}
              </>
            ) : (
              <p className="subtle">Issue analysis has not loaded yet.</p>
            )}
            <div className="toolbar-row">
              <StatusPill
                tone={confidenceTone(triageSuggestion?.confidence)}
              >{`triage confidence ${triageSuggestion ? `${Math.round(triageSuggestion.confidence * 100)}%` : 'n/a'}`}</StatusPill>
              {triageSuggestion?.suggested_severity ? (
                <span className="tag">suggested severity: {triageSuggestion.suggested_severity}</span>
              ) : null}
              {triageSuggestion?.suggested_owner ? (
                <span className="tag">suggested owner: {triageSuggestion.suggested_owner}</span>
              ) : null}
            </div>
            {triageSuggestion ? (
              <>
                <p>{triageSuggestion.reasoning || 'No triage reasoning available.'}</p>
                {triageSuggestion.suggested_labels.length ? (
                  <div className="tag-row">
                    {triageSuggestion.suggested_labels.map((label) => (
                      <span key={label} className="tag">
                        {label}
                      </span>
                    ))}
                  </div>
                ) : (
                  <p className="subtle">No label suggestions were generated.</p>
                )}
              </>
            ) : (
              <p className="subtle">No triage suggestion loaded.</p>
            )}
            <div className="activity-list">
              {duplicateMatches.length ? (
                duplicateMatches.slice(0, 5).map((match) => (
                  <div key={`${match.source_id}-${match.target_id}`} className="activity-entry">
                    <div className="activity-entry-top">
                      <strong>{match.target_id}</strong>
                      <small>{Math.round(match.similarity * 100)}% match</small>
                    </div>
                    <div className="row-meta">
                      <span className="tag">{match.match_type}</span>
                      {match.shared_fields.map((field) => (
                        <span key={field} className="tag">
                          {field}
                        </span>
                      ))}
                    </div>
                  </div>
                ))
              ) : (
                <p className="subtle">No meaningful duplicates detected.</p>
              )}
            </div>
          </section>

          <section className="detail-section">
            <h4>Missing evidence</h4>
            {issueDrift?.missing_evidence?.length ? (
              issueDrift.missing_evidence.map((item, index) => (
                <div key={`${item.path}-${index}`} className="evidence-row">
                  <span>
                    {item.path}
                    {item.line ? `:${item.line}` : ''}
                  </span>
                  <small>{item.normalized_path ?? 'unresolved'}</small>
                </div>
              ))
            ) : (
              <p className="subtle">No missing evidence recorded.</p>
            )}
          </section>

          <section className="detail-section">
            <h4>Workflow state</h4>
            <div className="toolbar-row">
              <label className="detail-section field-stack" htmlFor="issue-severity">
                <span className="filter-label">Severity</span>
                <select
                  id="issue-severity"
                  name="issue-severity"
                  className="text-input"
                  value={issueSeverityDraft}
                  onChange={(event) => onIssueSeverityChange(event.target.value)}
                >
                  {['P0', 'P1', 'P2', 'P3'].map((status) => (
                    <option key={status} value={status}>
                      {status}
                    </option>
                  ))}
                </select>
              </label>
              <label className="detail-section field-stack" htmlFor="issue-status">
                <span className="filter-label">Status</span>
                <select
                  id="issue-status"
                  name="issue-status"
                  className="text-input"
                  value={issueStatusDraft}
                  onChange={(event) => onIssueStatusChange(event.target.value)}
                >
                  {['open', 'triaged', 'investigating', 'in_progress', 'verification', 'resolved', 'partial'].map((status) => (
                    <option key={status} value={status}>
                      {status}
                    </option>
                  ))}
                </select>
              </label>
              <label className="toggle-row" htmlFor="issue-followup">
                <input
                  id="issue-followup"
                  name="issue-followup"
                  type="checkbox"
                  checked={issueFollowupDraft}
                  onChange={(event) => onIssueFollowupChange(event.target.checked)}
                />
                <span>Needs follow-up</span>
              </label>
            </div>
            <div className="toolbar-row">
              <label className="detail-section field-stack" htmlFor="issue-doc-status">
                <span className="filter-label">Doc status</span>
                <select
                  id="issue-doc-status"
                  name="issue-doc-status"
                  className="text-input"
                  value={issueDocStatusDraft}
                  onChange={(event) => onIssueDocStatusChange(event.target.value)}
                >
                  {['open', 'fixed', 'partial', 'unknown'].map((status) => (
                    <option key={status} value={status}>
                      {status}
                    </option>
                  ))}
                </select>
              </label>
              <label className="detail-section field-stack" htmlFor="issue-code-status">
                <span className="filter-label">Code status</span>
                <select
                  id="issue-code-status"
                  name="issue-code-status"
                  className="text-input"
                  value={issueCodeStatusDraft}
                  onChange={(event) => onIssueCodeStatusChange(event.target.value)}
                >
                  {['unknown', 'open', 'fixed', 'partial'].map((status) => (
                    <option key={status} value={status}>
                      {status}
                    </option>
                  ))}
                </select>
              </label>
            </div>
            <label className="detail-section field-stack" htmlFor="issue-label-draft">
              <span className="filter-label">Labels</span>
              <input
                id="issue-label-draft"
                name="issue-label-draft"
                className="text-input"
                placeholder="Comma-separated labels"
                value={issueLabelsDraft}
                onChange={(event) => onIssueLabelsChange(event.target.value)}
              />
            </label>
            <label className="detail-section field-stack" htmlFor="issue-notes">
              <span className="filter-label">Operator notes</span>
              <textarea
                id="issue-notes"
                name="issue-notes"
                className="text-area"
                rows={4}
                placeholder="Operator notes"
                value={issueNotesDraft}
                onChange={(event) => onIssueNotesChange(event.target.value)}
              />
            </label>
            <div className="toolbar-row">
              <button onClick={onSaveIssue} disabled={loading}>
                Save issue state
              </button>
              <span className="subtle">Updated {formatDate(selectedIssue.updated_at)}</span>
            </div>
          </section>

          <section className="detail-section">
            <h4>Verification</h4>
            <div className="evidence-row">
              <span>Verification gap</span>
              <small>{issueDrift?.verification_gap ? 'yes' : 'no'}</small>
            </div>
            <div className="evidence-row">
              <span>Review-ready runs</span>
              <small>{selectedIssue.review_ready_runs.length ? selectedIssue.review_ready_runs.join(', ') : 'none'}</small>
            </div>
          </section>

          <section className="detail-section">
            <div className="panel-header">
              <div>
                <h4>Coverage and tests</h4>
                <p className="subtle">Verification depth for this issue, plus suggested regression tests.</p>
              </div>
              <button type="button" className="ghost-button" onClick={onGenerateTestSuggestions} disabled={loading}>
                Generate tests
              </button>
            </div>
            {coverageDelta ? (
              <>
                <div className="summary-grid analysis-summary-grid">
                  <SummaryCard
                    label="Baseline"
                    value={coverageDelta.baseline ? `${coverageDelta.baseline.line_coverage}%` : 'n/a'}
                    accent="sand"
                  />
                  <SummaryCard
                    label="Current"
                    value={coverageDelta.current ? `${coverageDelta.current.line_coverage}%` : 'n/a'}
                    accent="blue"
                  />
                  <SummaryCard
                    label="Line delta"
                    value={`${coverageDelta.line_delta > 0 ? '+' : ''}${coverageDelta.line_delta}%`}
                    accent={deltaAccent(coverageDelta.line_delta)}
                  />
                  <SummaryCard
                    label="Covered lines"
                    value={`${coverageDelta.lines_added > 0 ? '+' : ''}${coverageDelta.lines_added} / -${coverageDelta.lines_lost}`}
                    accent={deltaAccent(coverageDelta.lines_added - coverageDelta.lines_lost)}
                  />
                </div>
                <div className="tag-row">
                  {coverageDelta.new_files_covered.slice(0, 6).map((file) => (
                    <span key={file} className="tag">
                      covered: {file}
                    </span>
                  ))}
                  {coverageDelta.files_regressed.slice(0, 6).map((file) => (
                    <span key={file} className="tag tag-warning">
                      regressed: {file}
                    </span>
                  ))}
                </div>
              </>
            ) : (
              <p className="subtle">No coverage delta recorded yet for this issue.</p>
            )}
            <div className="activity-list">
              {testSuggestions.length ? (
                testSuggestions.map((suggestion) => (
                  <div key={suggestion.suggestion_id} className="activity-entry">
                    <div className="activity-entry-top">
                      <strong>{suggestion.test_file}</strong>
                      <small>{suggestion.priority}</small>
                    </div>
                    <p>{suggestion.test_description}</p>
                    <div className="row-meta">
                      <span className="tag">{suggestion.priority}</span>
                      <span className="tag">{formatDate(suggestion.created_at)}</span>
                    </div>
                    <small>{suggestion.rationale}</small>
                  </div>
                ))
              ) : (
                <p className="subtle">No saved test suggestions yet.</p>
              )}
            </div>
          </section>

          <section className="detail-section">
            <div className="panel-header">
              <div>
                <h4>Verification profiles</h4>
                <p className="subtle">Saved repo-specific commands for fast, repeatable issue verification.</p>
              </div>
              <span className="tag">{verificationProfiles.length} profiles</span>
            </div>
            <div className="activity-list">
              {verificationProfiles.length ? (
                verificationProfiles.map((profile) => {
                  const profileReport = verificationProfileReports.find((item) => item.profile_id === profile.profile_id)
                  return (
                    <div key={profile.profile_id} className="activity-entry">
                      <div className="activity-entry-top">
                        <strong>{profile.name}</strong>
                        <small>{profile.built_in ? 'built-in' : 'custom'}</small>
                      </div>
                      <p>{profile.description || 'No description set.'}</p>
                      <div className="row-meta">
                        <span className="tag">{profile.coverage_format}</span>
                        <span className="tag">{profile.max_runtime_seconds}s</span>
                        <span className="tag">retries {profile.retry_count}</span>
                        {profileReport ? <span className="tag">success {profileReport.success_rate}%</span> : null}
                      </div>
                      {profileReport ? (
                        <>
                          <div className="tag-row">
                            <span className="tag">runs {profileReport.total_runs}</span>
                            <span className="tag">avg attempts {profileReport.avg_attempt_count}</span>
                            <span className="tag">checklist {profileReport.checklist_pass_rate}%</span>
                            <span className="tag">high {profileReport.confidence_counts.high ?? 0}</span>
                            <span className="tag">medium {profileReport.confidence_counts.medium ?? 0}</span>
                            <span className="tag">low {profileReport.confidence_counts.low ?? 0}</span>
                          </div>
                          {dimensionSummaryTags('runtime', profileReport.runtime_breakdown)}
                          {dimensionSummaryTags('model', profileReport.model_breakdown)}
                          {dimensionSummaryTags('branch', profileReport.branch_breakdown)}
                        </>
                      ) : null}
                      <pre className="detail-mini-block">{profile.test_command}</pre>
                      {profile.coverage_command ? <pre className="detail-mini-block">{profile.coverage_command}</pre> : null}
                      {profile.checklist_items.length ? (
                        <div className="tag-row">
                          {profile.checklist_items.map((item) => (
                            <span key={`${profile.profile_id}-checklist-${item}`} className="tag">
                              checklist: {item}
                            </span>
                          ))}
                        </div>
                      ) : null}
                      {profile.source_paths.length ? (
                        <div className="tag-row">
                          {profile.source_paths.map((path) => (
                            <span key={`${profile.profile_id}-${path}`} className="tag">
                              {path}
                            </span>
                          ))}
                        </div>
                      ) : null}
                      {verificationProfileHistory.filter((item) => item.profile_id === profile.profile_id).length ? (
                        <div className="activity-list">
                          {verificationProfileHistory
                            .filter((item) => item.profile_id === profile.profile_id)
                            .slice(0, 3)
                            .map((item) => (
                              <div key={item.execution_id} className="activity-entry">
                                <div className="activity-entry-top">
                                  <strong>{item.success ? 'Passed' : 'Failed'}</strong>
                                  <small>{formatDate(item.created_at)}</small>
                                </div>
                                <div className="row-meta">
                                  <span className="tag">confidence {item.confidence}</span>
                                  <span className="tag">attempts {item.attempt_count}</span>
                                  {item.run_id ? <span className="tag">run {item.run_id}</span> : null}
                                </div>
                                {item.checklist_results.length ? (
                                  <div className="tag-row">
                                    {item.checklist_results.map((check) => (
                                      <span key={`${item.execution_id}-${check.item_id}`} className={`tag ${check.passed ? '' : 'tag-warning'}`}>
                                        {check.passed ? 'pass' : 'gap'}: {check.title}
                                      </span>
                                    ))}
                                  </div>
                                ) : null}
                              </div>
                            ))}
                        </div>
                      ) : (
                        <p className="subtle">No verification history for this profile yet.</p>
                      )}
                    </div>
                  )
                })
              ) : (
                <p className="subtle">No verification profiles recorded for this workspace yet.</p>
              )}
            </div>
          </section>

          <section className="detail-section">
            <div className="panel-header">
              <div>
                <h4>Structural context</h4>
                <p className="subtle">Repo-map summary and ranked paths that help the agent localize this bug.</p>
              </div>
              {repoMap ? <span className="tag">{repoMap.source_files} source files</span> : null}
            </div>
            {issueContextPacket?.related_paths?.length ? (
              <div className="tag-row">
                {issueContextPacket.related_paths.map((path) => (
                  <span key={`related-${path}`} className="tag">
                    {path}
                  </span>
                ))}
              </div>
            ) : (
              <p className="subtle">No ranked related paths recorded yet.</p>
            )}
            {repoMap ? (
              <>
                <div className="summary-grid analysis-summary-grid">
                  <SummaryCard label="Files" value={repoMap.total_files} accent="sand" />
                  <SummaryCard label="Source" value={repoMap.source_files} accent="blue" />
                  <SummaryCard label="Tests" value={repoMap.test_files} accent="green" />
                  <SummaryCard label="Exts" value={Object.keys(repoMap.top_extensions).length} accent="amber" />
                </div>
                <div className="activity-list">
                  {repoMap.top_directories.map((directory) => (
                    <div key={directory.path} className="activity-entry">
                      <div className="activity-entry-top">
                        <strong>{directory.path}</strong>
                        <small>{directory.file_count} files</small>
                      </div>
                      <div className="row-meta">
                        <span className="tag">source {directory.source_file_count}</span>
                        <span className="tag">tests {directory.test_file_count}</span>
                      </div>
                    </div>
                  ))}
                </div>
                {repoMap.key_files.length ? (
                  <div className="tag-row">
                    {repoMap.key_files.map((file) => (
                      <span key={`key-${file.path}`} className="tag">
                        {file.role}: {file.path}
                      </span>
                    ))}
                  </div>
                ) : null}
              </>
            ) : (
              <p className="subtle">No repo map loaded for this workspace yet.</p>
            )}
          </section>

          <section className="detail-section">
            <div className="panel-header">
              <div>
                <h4>Dynamic context</h4>
                <p className="subtle">Structure-aware symbols and related artifacts ranked from the current issue packet.</p>
              </div>
            </div>
            {issueContextPacket?.dynamic_context?.symbol_context?.length ? (
              <div className="activity-list">
                {issueContextPacket.dynamic_context.symbol_context.map((item) => (
                  <div key={`${item.path}-${item.symbol}-${item.line_start ?? 'na'}`} className="activity-entry">
                    <div className="activity-entry-top">
                      <strong>{item.symbol}</strong>
                      <small>{item.path}</small>
                    </div>
                    <div className="row-meta">
                      <span className="tag">{item.kind}</span>
                      <span className="tag">score {item.score}</span>
                      {item.line_start ? <span className="tag">line {item.line_start}</span> : null}
                      {item.enclosing_scope ? <span className="tag">scope {item.enclosing_scope}</span> : null}
                    </div>
                    <p>{item.reason ?? 'No ranking reason recorded.'}</p>
                  </div>
                ))}
              </div>
            ) : (
              <p className="subtle">No symbol context ranked yet.</p>
            )}
            {issueContextPacket?.dynamic_context?.related_context?.length ? (
              <div className="activity-list">
                {issueContextPacket.dynamic_context.related_context.map((item) => (
                  <div key={`${item.artifact_type}-${item.artifact_id}`} className="activity-entry">
                    <div className="activity-entry-top">
                      <strong>{item.title}</strong>
                      <small>{item.artifact_type}</small>
                    </div>
                    <div className="row-meta">
                      <span className="tag">score {item.score}</span>
                      {item.path ? <span className="tag">{item.path}</span> : null}
                      {item.matched_terms.map((term) => (
                        <span key={`${item.artifact_id}-${term}`} className="tag">
                          {term}
                        </span>
                      ))}
                    </div>
                    <p>{item.reason ?? 'No retrieval reason recorded.'}</p>
                  </div>
                ))}
              </div>
            ) : (
              <p className="subtle">No related artifacts ranked yet.</p>
            )}
          </section>

          <section className="detail-section">
            {(() => {
              const changedVariantCount = evalReport?.scenario_reports.filter((item) => item.variant_diff?.changed).length ?? 0
              const freshReplayRankings = (evalReport?.fresh_replay_rankings ?? []).filter(
                (item) => item.issue_id === selectedIssue?.bug_id,
              )
              const freshReplayTrends = (evalReport?.fresh_replay_trends ?? []).filter(
                (item) => item.issue_id === selectedIssue?.bug_id,
              )
              const guidanceVariantRollups = evalReport?.guidance_variant_rollups ?? []
              const ticketVariantRollups = evalReport?.ticket_context_variant_rollups ?? []
              return (
                <>
            <div className="panel-header">
              <div>
                <h4>Eval scenarios</h4>
                <p className="subtle">Saved replay/evaluation presets and the latest workspace-level report slices for this issue.</p>
              </div>
              <div className="toolbar-row">
                <button type="button" className="ghost-button" onClick={onReplayAllEvalScenarios} disabled={loading || !evalScenarios.length}>
                  Run all scenarios
                </button>
                <div className="tag-row">
                <span className="tag">{evalScenarios.length} scenarios</span>
                <span className="tag">variants {changedVariantCount} changed</span>
                </div>
              </div>
            </div>
            {evalScenarios.length ? (
              <div className="activity-list">
                {evalScenarios.map((scenario) => {
                  const report = evalReport?.scenario_reports.find((item) => item.scenario.scenario_id === scenario.scenario_id)
                  return (
                    <div key={scenario.scenario_id} className="activity-entry">
                      <div className="activity-entry-top">
                        <strong>{scenario.name}</strong>
                        <small>{scenario.baseline_replay_id ?? 'no baseline replay'}</small>
                      </div>
                      <div className="toolbar-row">
                        <button type="button" className="ghost-button" onClick={() => onRunEvalScenario(scenario.scenario_id)} disabled={loading}>
                          Run scenario
                        </button>
                      </div>
                      <p>{scenario.description || scenario.notes || 'No eval scenario notes recorded.'}</p>
                      <div className="row-meta">
                        <span className="tag">guidance {scenario.guidance_paths.length}</span>
                        <span className="tag">ticket context {scenario.ticket_context_ids.length}</span>
                        <span className="tag">runs {scenario.run_ids.length}</span>
                        <span className="tag">profiles {scenario.verification_profile_ids.length}</span>
                        <span className="tag">browser dumps {scenario.browser_dump_ids.length}</span>
                      </div>
                      {report ? (
                        <>
                          <div className="tag-row">
                            <span className="tag">success {report.success_runs}</span>
                            <span className="tag">failed {report.failed_runs}</span>
                            <span className="tag">verification {report.verification_success_rate}%</span>
                            <span className="tag">avg duration {report.avg_duration_ms}ms</span>
                            <span className="tag">cost ${report.total_estimated_cost.toFixed(4)}</span>
                          </div>
                          {report.latest_fresh_run ? (
                            <>
                              <div className="tag-row">
                                <span className="tag">fresh run {report.latest_fresh_run.run_id}</span>
                                <span className="tag">fresh status {report.latest_fresh_run.status}</span>
                                <span className="tag">fresh cost ${report.latest_fresh_run.estimated_cost.toFixed(4)}</span>
                                <span className="tag">fresh duration {report.latest_fresh_run.duration_ms}ms</span>
                              </div>
                              {report.fresh_comparison_to_baseline ? (
                                <>
                                  <div className="tag-row">
                                    <span className="tag">fresh vs {report.fresh_comparison_to_baseline.compared_to_name}</span>
                                    <span className="tag">preferred {report.fresh_comparison_to_baseline.preferred}</span>
                                    <span className="tag">
                                      cost {report.fresh_comparison_to_baseline.estimated_cost_delta >= 0 ? '+' : ''}{report.fresh_comparison_to_baseline.estimated_cost_delta.toFixed(4)}
                                    </span>
                                    <span className="tag">
                                      duration {report.fresh_comparison_to_baseline.duration_ms_delta >= 0 ? '+' : ''}{report.fresh_comparison_to_baseline.duration_ms_delta}ms
                                    </span>
                                  </div>
                                  <p className="subtle">{report.fresh_comparison_to_baseline.summary}</p>
                                </>
                              ) : null}
                            </>
                          ) : null}
                          <p>{report.summary}</p>
                          {report.variant_diff ? (
                            <>
                              <div className="tag-row">
                                <span className="tag">
                                  variant {report.variant_diff.changed ? 'changed' : 'matched'}
                                </span>
                                <span className="tag">
                                  guidance +{report.variant_diff.added_guidance_paths.length} / -{report.variant_diff.removed_guidance_paths.length}
                                </span>
                                <span className="tag">
                                  ticket +{report.variant_diff.added_ticket_context_ids.length} / -{report.variant_diff.removed_ticket_context_ids.length}
                                </span>
                              </div>
                              <p>{report.variant_diff.summary}</p>
                            </>
                          ) : null}
                          {report.comparison_to_baseline ? (
                            <>
                              <div className="tag-row">
                                <span className="tag">vs {report.comparison_to_baseline.compared_to_name}</span>
                                <span className="tag">preferred {report.comparison_to_baseline.preferred}</span>
                                <span className="tag">
                                  success {report.comparison_to_baseline.success_runs_delta >= 0 ? '+' : ''}{report.comparison_to_baseline.success_runs_delta}
                                </span>
                                <span className="tag">
                                  verification {report.comparison_to_baseline.verification_success_rate_delta >= 0 ? '+' : ''}{report.comparison_to_baseline.verification_success_rate_delta}%
                                </span>
                                <span className="tag">
                                  cost {report.comparison_to_baseline.total_estimated_cost_delta >= 0 ? '+' : ''}{report.comparison_to_baseline.total_estimated_cost_delta.toFixed(4)}
                                </span>
                              </div>
                              {report.comparison_to_baseline.preference_reasons.length ? (
                                <p className="subtle">
                                  Preferred reasons: {report.comparison_to_baseline.preference_reasons.join(', ')}
                                </p>
                              ) : null}
                              <p>{report.comparison_to_baseline.summary}</p>
                              {report.comparison_to_baseline.verification_profile_deltas.length ? (
                                <div className="detail-subsection">
                                  <div className="panel-header">
                                    <div>
                                      <h5>Verification profile deltas</h5>
                                      <p className="subtle">How saved verification history differs from the issue baseline scenario.</p>
                                    </div>
                                  </div>
                                  <div className="activity-list">
                                    {report.comparison_to_baseline.verification_profile_deltas.map((item) => (
                                      <div key={`${scenario.scenario_id}-verify-delta-${item.profile_id}`} className="activity-entry">
                                        <div className="activity-entry-top">
                                          <strong>{item.profile_name}</strong>
                                          <small>preferred {item.preferred}</small>
                                        </div>
                                        <div className="tag-row">
                                          <span className="tag">runs {item.total_runs_delta >= 0 ? '+' : ''}{item.total_runs_delta}</span>
                                          <span className="tag">success {item.success_rate_delta >= 0 ? '+' : ''}{item.success_rate_delta}%</span>
                                          <span className="tag">checklist {item.checklist_pass_rate_delta >= 0 ? '+' : ''}{item.checklist_pass_rate_delta}%</span>
                                          <span className="tag">attempts {item.avg_attempt_count_delta >= 0 ? '+' : ''}{item.avg_attempt_count_delta}</span>
                                        </div>
                                        <p className="subtle">
                                          Scenario confidence {Object.entries(item.scenario_confidence_counts).map(([key, value]) => `${key}:${value}`).join(', ') || 'none'}
                                        </p>
                                        <p className="subtle">
                                          Baseline confidence {Object.entries(item.baseline_confidence_counts).map(([key, value]) => `${key}:${value}`).join(', ') || 'none'}
                                        </p>
                                        <p>{item.summary}</p>
                                      </div>
                                    ))}
                                  </div>
                                </div>
                              ) : null}
                            </>
                          ) : null}
                        </>
                      ) : null}
                    </div>
                  )
                })}
              </div>
            ) : (
              <p className="subtle">No eval scenarios saved for this issue yet.</p>
            )}
            {freshReplayRankings.length ? (
              <details className="detail-disclosure">
                <summary>Fresh replay ranking</summary>
                <div className="activity-list">
                  {freshReplayRankings.map((ranking) => (
                    <div key={`fresh-ranking-${ranking.issue_id}`} className="activity-entry">
                      <div className="activity-entry-top">
                        <strong>{ranking.baseline_scenario_name ? `Baseline ${ranking.baseline_scenario_name}` : 'Fresh replay cluster'}</strong>
                        <small>{ranking.ranked_scenarios.length} scenarios ranked</small>
                      </div>
                      <p>{ranking.summary}</p>
                      <div className="activity-list">
                        {ranking.ranked_scenarios.map((entry) => (
                          <div key={`fresh-ranking-entry-${entry.scenario_id}`} className="activity-entry">
                            <div className="activity-entry-top">
                              <strong>#{entry.rank} {entry.scenario_name}</strong>
                              <small>{entry.latest_fresh_run.run_id}</small>
                            </div>
                            <div className="tag-row">
                              <span className="tag">status {entry.latest_fresh_run.status}</span>
                              <span className="tag">wins {entry.pairwise_wins}</span>
                              <span className="tag">losses {entry.pairwise_losses}</span>
                              <span className="tag">ties {entry.pairwise_ties}</span>
                              <span className="tag">cost ${entry.latest_fresh_run.estimated_cost.toFixed(4)}</span>
                              <span className="tag">duration {entry.latest_fresh_run.duration_ms}ms</span>
                            </div>
                            {entry.preference_reasons.length ? (
                              <p className="subtle">Preference reasons: {entry.preference_reasons.join(', ')}</p>
                            ) : null}
                            <p>{entry.summary}</p>
                          </div>
                        ))}
                      </div>
                    </div>
                  ))}
                </div>
              </details>
            ) : null}
            {freshReplayTrends.length ? (
              <details className="detail-disclosure">
                <summary>Fresh replay trend</summary>
                <div className="activity-list">
                  {freshReplayTrends.map((trend) => (
                    <div key={`fresh-trend-${trend.issue_id}`} className="activity-entry">
                      <div className="activity-entry-top">
                        <strong>Replay movement</strong>
                        <small>{trend.entries.length} scenarios tracked</small>
                      </div>
                      <div className="tag-row">
                        {trend.latest_batch_id ? <span className="tag">latest batch {trend.latest_batch_id}</span> : null}
                        {trend.previous_batch_id ? <span className="tag">previous batch {trend.previous_batch_id}</span> : null}
                      </div>
                      <p>{trend.summary}</p>
                      <div className="activity-list">
                        {trend.entries.map((entry) => (
                          <div key={`fresh-trend-entry-${entry.scenario_id}`} className="activity-entry">
                            <div className="activity-entry-top">
                              <strong>{entry.scenario_name}</strong>
                              <small>{entry.latest_fresh_run.run_id}</small>
                            </div>
                            <div className="tag-row">
                              <span className="tag">movement {entry.movement}</span>
                              <span className="tag">current rank {entry.current_rank}</span>
                              {entry.previous_rank != null ? (
                                <span className="tag">previous rank {entry.previous_rank}</span>
                              ) : null}
                              <span className="tag">status {entry.latest_fresh_run.status}</span>
                            </div>
                            {entry.previous_fresh_run ? (
                              <p className="subtle">
                                Previous run {entry.previous_fresh_run.run_id} cost ${entry.previous_fresh_run.estimated_cost.toFixed(4)} duration {entry.previous_fresh_run.duration_ms}ms
                              </p>
                            ) : null}
                            <p>{entry.summary}</p>
                          </div>
                        ))}
                      </div>
                    </div>
                  ))}
                </div>
              </details>
            ) : null}
            {guidanceVariantRollups.length || ticketVariantRollups.length ? (
              <details className="detail-disclosure">
                <summary>Variant comparison</summary>
                {guidanceVariantRollups.length ? (
                  <div className="detail-subsection">
                    <div className="panel-header">
                      <div>
                        <h5>Guidance sets</h5>
                        <p className="subtle">Outcome rollups grouped by saved guidance-path selections.</p>
                      </div>
                    </div>
                    <div className="activity-list">
                      {guidanceVariantRollups.map((item) => (
                        <div key={`guidance-${item.variant_key}`} className="activity-entry">
                          <div className="activity-entry-top">
                            <strong>{item.label}</strong>
                            <small>{item.scenario_count} scenarios</small>
                          </div>
                          <div className="tag-row">
                            <span className="tag">success {item.success_runs}</span>
                            <span className="tag">failed {item.failed_runs}</span>
                            <span className="tag">verification {item.verification_success_rate}%</span>
                            <span className="tag">avg duration {item.avg_duration_ms}ms</span>
                            <span className="tag">cost ${item.total_estimated_cost.toFixed(4)}</span>
                          </div>
                          <p className="subtle">
                            {item.selected_values.length
                              ? `Selected guidance: ${item.selected_values.join(', ')}`
                              : 'Selected guidance: current defaults'}
                          </p>
                          <div className="tag-row">
                            {item.runtime_breakdown.slice(0, 3).map((bucket) => (
                              <span key={`guidance-runtime-${item.variant_key}-${bucket.key}`} className="tag">
                                runtime {bucket.label} {bucket.total_runs}
                              </span>
                            ))}
                            {item.model_breakdown.slice(0, 3).map((bucket) => (
                              <span key={`guidance-model-${item.variant_key}-${bucket.key}`} className="tag">
                                model {bucket.label} {bucket.total_runs}
                              </span>
                            ))}
                          </div>
                          <p>{item.summary}</p>
                        </div>
                      ))}
                    </div>
                  </div>
                ) : null}
                {ticketVariantRollups.length ? (
                  <div className="detail-subsection">
                    <div className="panel-header">
                      <div>
                        <h5>Ticket-context sets</h5>
                        <p className="subtle">Outcome rollups grouped by saved ticket-context selections.</p>
                      </div>
                    </div>
                    <div className="activity-list">
                      {ticketVariantRollups.map((item) => (
                        <div key={`ticket-${item.variant_key}`} className="activity-entry">
                          <div className="activity-entry-top">
                            <strong>{item.label}</strong>
                            <small>{item.scenario_count} scenarios</small>
                          </div>
                          <div className="tag-row">
                            <span className="tag">success {item.success_runs}</span>
                            <span className="tag">failed {item.failed_runs}</span>
                            <span className="tag">verification {item.verification_success_rate}%</span>
                            <span className="tag">avg duration {item.avg_duration_ms}ms</span>
                            <span className="tag">cost ${item.total_estimated_cost.toFixed(4)}</span>
                          </div>
                          <p className="subtle">
                            {item.selected_values.length
                              ? `Selected ticket context: ${item.selected_values.join(', ')}`
                              : 'Selected ticket context: current defaults'}
                          </p>
                          <div className="tag-row">
                            {item.runtime_breakdown.slice(0, 3).map((bucket) => (
                              <span key={`ticket-runtime-${item.variant_key}-${bucket.key}`} className="tag">
                                runtime {bucket.label} {bucket.total_runs}
                              </span>
                            ))}
                            {item.model_breakdown.slice(0, 3).map((bucket) => (
                              <span key={`ticket-model-${item.variant_key}-${bucket.key}`} className="tag">
                                model {bucket.label} {bucket.total_runs}
                              </span>
                            ))}
                          </div>
                          <p>{item.summary}</p>
                        </div>
                      ))}
                    </div>
                  </div>
                ) : null}
              </details>
            ) : null}
                </>
              )
            })()}
          </section>

          <section className="detail-section">
            <div className="panel-header">
              <div>
                <h4>Ticket context</h4>
                <p className="subtle">Acceptance criteria, upstream links, and product intent attached to this issue.</p>
              </div>
              <span className="tag">{issueContextPacket?.ticket_contexts.length ?? 0} linked</span>
            </div>
            <div className="toolbar-row">
              <label className="detail-section field-stack runbook-picker" htmlFor="ticket-context-select">
                <span className="filter-label">Selected context</span>
                <select
                  id="ticket-context-select"
                  name="ticket-context-select"
                  className="text-input"
                  value={selectedTicketContextId}
                  onChange={(event) => onSelectedTicketContextChange(event.target.value)}
                >
                  <option value="">New ticket context</option>
                  {(issueContextPacket?.ticket_contexts ?? []).map((context) => (
                    <option key={context.context_id} value={context.context_id}>
                      {context.title}
                    </option>
                  ))}
                </select>
              </label>
              {selectedTicketContext ? (
                <div className="tag-row">
                  <span className="tag">{selectedTicketContext.provider}</span>
                  {selectedTicketContext.external_id ? <span className="tag">{selectedTicketContext.external_id}</span> : null}
                  {selectedTicketContext.status ? <span className="tag">{selectedTicketContext.status}</span> : null}
                </div>
              ) : (
                <span className="subtle">Use this form to attach an upstream issue, incident, or acceptance brief.</span>
              )}
            </div>
            <div className="detail-section runbook-editor">
              <div className="toolbar-row">
                <label className="detail-section field-stack" htmlFor="ticket-context-provider">
                  <span className="filter-label">Provider</span>
                  <select
                    id="ticket-context-provider"
                    name="ticket-context-provider"
                    className="text-input"
                    value={ticketContextProviderDraft}
                    onChange={(event) => onTicketContextProviderChange(event.target.value as TicketContextRecord['provider'])}
                  >
                    {['manual', 'github', 'jira', 'linear', 'incident', 'other'].map((provider) => (
                      <option key={provider} value={provider}>
                        {provider}
                      </option>
                    ))}
                  </select>
                </label>
                <label className="detail-section field-stack" htmlFor="ticket-context-external-id">
                  <span className="filter-label">External id</span>
                  <input
                    id="ticket-context-external-id"
                    name="ticket-context-external-id"
                    className="text-input"
                    placeholder="PROJ-123 or repo#123"
                    value={ticketContextExternalIdDraft}
                    onChange={(event) => onTicketContextExternalIdChange(event.target.value)}
                  />
                </label>
              </div>
              <label className="detail-section field-stack" htmlFor="ticket-context-title">
                <span className="filter-label">Title</span>
                <input
                  id="ticket-context-title"
                  name="ticket-context-title"
                  className="text-input"
                  placeholder="Customer escalation or upstream bug title"
                  value={ticketContextTitleDraft}
                  onChange={(event) => onTicketContextTitleChange(event.target.value)}
                />
              </label>
              <label className="detail-section field-stack" htmlFor="ticket-context-summary">
                <span className="filter-label">Summary</span>
                <textarea
                  id="ticket-context-summary"
                  name="ticket-context-summary"
                  className="text-area"
                  rows={4}
                  placeholder="What the upstream ticket says this issue should accomplish."
                  value={ticketContextSummaryDraft}
                  onChange={(event) => onTicketContextSummaryChange(event.target.value)}
                />
              </label>
              <label className="detail-section field-stack" htmlFor="ticket-context-criteria">
                <span className="filter-label">Acceptance criteria</span>
                <textarea
                  id="ticket-context-criteria"
                  name="ticket-context-criteria"
                  className="text-area"
                  rows={4}
                  placeholder={'One criterion per line'}
                  value={ticketContextCriteriaDraft}
                  onChange={(event) => onTicketContextCriteriaChange(event.target.value)}
                />
              </label>
              <div className="toolbar-row">
                <label className="detail-section field-stack" htmlFor="ticket-context-links">
                  <span className="filter-label">Links</span>
                  <textarea
                    id="ticket-context-links"
                    name="ticket-context-links"
                    className="text-area"
                    rows={3}
                    placeholder={'One URL per line'}
                    value={ticketContextLinksDraft}
                    onChange={(event) => onTicketContextLinksChange(event.target.value)}
                  />
                </label>
                <label className="detail-section field-stack" htmlFor="ticket-context-status">
                  <span className="filter-label">Status</span>
                  <input
                    id="ticket-context-status"
                    name="ticket-context-status"
                    className="text-input"
                    placeholder="open, synced, verified"
                    value={ticketContextStatusDraft}
                    onChange={(event) => onTicketContextStatusChange(event.target.value)}
                  />
                </label>
              </div>
              <label className="detail-section field-stack" htmlFor="ticket-context-labels">
                <span className="filter-label">Labels</span>
                <input
                  id="ticket-context-labels"
                  name="ticket-context-labels"
                  className="text-input"
                  placeholder="customer, export"
                  value={ticketContextLabelsDraft}
                  onChange={(event) => onTicketContextLabelsChange(event.target.value)}
                />
              </label>
              <label className="detail-section field-stack" htmlFor="ticket-context-excerpt">
                <span className="filter-label">Source excerpt</span>
                <textarea
                  id="ticket-context-excerpt"
                  name="ticket-context-excerpt"
                  className="text-area"
                  rows={3}
                  placeholder="Relevant upstream text to keep with this issue."
                  value={ticketContextSourceExcerptDraft}
                  onChange={(event) => onTicketContextSourceExcerptChange(event.target.value)}
                />
              </label>
              <div className="toolbar-row">
                <button type="button" onClick={onSaveTicketContext} disabled={loading || !ticketContextTitleDraft.trim()}>
                  Save ticket context
                </button>
                <button type="button" className="ghost-button" onClick={onDeleteTicketContext} disabled={loading || !selectedTicketContext}>
                  Delete ticket context
                </button>
              </div>
            </div>
          </section>

          <section className="detail-section">
            <div className="panel-header">
              <div>
                <h4>Threat model</h4>
                <p className="subtle">Assets, trust boundaries, abuse cases, and mitigations attached to this issue.</p>
              </div>
              <span className="tag">{issueContextPacket?.threat_models.length ?? 0} linked</span>
            </div>
            <div className="toolbar-row">
              <label className="detail-section field-stack runbook-picker" htmlFor="threat-model-select">
                <span className="filter-label">Selected model</span>
                <select
                  id="threat-model-select"
                  name="threat-model-select"
                  className="text-input"
                  value={selectedThreatModelId}
                  onChange={(event) => onSelectedThreatModelChange(event.target.value)}
                >
                  <option value="">New threat model</option>
                  {(issueContextPacket?.threat_models ?? []).map((model) => (
                    <option key={model.threat_model_id} value={model.threat_model_id}>
                      {model.title}
                    </option>
                  ))}
                </select>
              </label>
              {selectedThreatModel ? (
                <div className="tag-row">
                  <span className="tag">{selectedThreatModel.methodology}</span>
                  <span className="tag">{selectedThreatModel.status}</span>
                </div>
              ) : (
                <span className="subtle">Use this to capture exploit paths, trust boundaries, and mitigations before or after a run.</span>
              )}
            </div>
            <div className="detail-section runbook-editor">
              <div className="toolbar-row">
                <label className="detail-section field-stack" htmlFor="threat-model-title">
                  <span className="filter-label">Title</span>
                  <input
                    id="threat-model-title"
                    name="threat-model-title"
                    className="text-input"
                    placeholder="Export authorization boundary"
                    value={threatModelTitleDraft}
                    onChange={(event) => onThreatModelTitleChange(event.target.value)}
                  />
                </label>
                <label className="detail-section field-stack" htmlFor="threat-model-methodology">
                  <span className="filter-label">Methodology</span>
                  <select
                    id="threat-model-methodology"
                    name="threat-model-methodology"
                    className="text-input"
                    value={threatModelMethodologyDraft}
                    onChange={(event) => onThreatModelMethodologyChange(event.target.value as ThreatModelRecord['methodology'])}
                  >
                    {['manual', 'stride', 'threat_dragon', 'pytm', 'threagile', 'attack_path'].map((method) => (
                      <option key={method} value={method}>
                        {method}
                      </option>
                    ))}
                  </select>
                </label>
                <label className="detail-section field-stack" htmlFor="threat-model-status">
                  <span className="filter-label">Status</span>
                  <select
                    id="threat-model-status"
                    name="threat-model-status"
                    className="text-input"
                    value={threatModelStatusDraft}
                    onChange={(event) => onThreatModelStatusChange(event.target.value as ThreatModelRecord['status'])}
                  >
                    {['draft', 'reviewed', 'accepted'].map((status) => (
                      <option key={status} value={status}>
                        {status}
                      </option>
                    ))}
                  </select>
                </label>
              </div>
              <label className="detail-section field-stack" htmlFor="threat-model-summary">
                <span className="filter-label">Summary</span>
                <textarea
                  id="threat-model-summary"
                  name="threat-model-summary"
                  className="text-area"
                  rows={3}
                  placeholder="What security-sensitive path this issue touches."
                  value={threatModelSummaryDraft}
                  onChange={(event) => onThreatModelSummaryChange(event.target.value)}
                />
              </label>
              <div className="toolbar-row">
                <label className="detail-section field-stack" htmlFor="threat-model-assets">
                  <span className="filter-label">Assets</span>
                  <textarea
                    id="threat-model-assets"
                    name="threat-model-assets"
                    className="text-area"
                    rows={3}
                    placeholder={'One asset per line'}
                    value={threatModelAssetsDraft}
                    onChange={(event) => onThreatModelAssetsChange(event.target.value)}
                  />
                </label>
                <label className="detail-section field-stack" htmlFor="threat-model-entry-points">
                  <span className="filter-label">Entry points</span>
                  <textarea
                    id="threat-model-entry-points"
                    name="threat-model-entry-points"
                    className="text-area"
                    rows={3}
                    placeholder={'One entry point per line'}
                    value={threatModelEntryPointsDraft}
                    onChange={(event) => onThreatModelEntryPointsChange(event.target.value)}
                  />
                </label>
              </div>
              <div className="toolbar-row">
                <label className="detail-section field-stack" htmlFor="threat-model-trust-boundaries">
                  <span className="filter-label">Trust boundaries</span>
                  <textarea
                    id="threat-model-trust-boundaries"
                    name="threat-model-trust-boundaries"
                    className="text-area"
                    rows={3}
                    placeholder={'One boundary per line'}
                    value={threatModelTrustBoundariesDraft}
                    onChange={(event) => onThreatModelTrustBoundariesChange(event.target.value)}
                  />
                </label>
                <label className="detail-section field-stack" htmlFor="threat-model-abuse-cases">
                  <span className="filter-label">Abuse cases</span>
                  <textarea
                    id="threat-model-abuse-cases"
                    name="threat-model-abuse-cases"
                    className="text-area"
                    rows={3}
                    placeholder={'One abuse case per line'}
                    value={threatModelAbuseCasesDraft}
                    onChange={(event) => onThreatModelAbuseCasesChange(event.target.value)}
                  />
                </label>
              </div>
              <div className="toolbar-row">
                <label className="detail-section field-stack" htmlFor="threat-model-mitigations">
                  <span className="filter-label">Mitigations</span>
                  <textarea
                    id="threat-model-mitigations"
                    name="threat-model-mitigations"
                    className="text-area"
                    rows={3}
                    placeholder={'One mitigation per line'}
                    value={threatModelMitigationsDraft}
                    onChange={(event) => onThreatModelMitigationsChange(event.target.value)}
                  />
                </label>
                <label className="detail-section field-stack" htmlFor="threat-model-references">
                  <span className="filter-label">References</span>
                  <textarea
                    id="threat-model-references"
                    name="threat-model-references"
                    className="text-area"
                    rows={3}
                    placeholder={'One reference or URL per line'}
                    value={threatModelReferencesDraft}
                    onChange={(event) => onThreatModelReferencesChange(event.target.value)}
                  />
                </label>
              </div>
              <div className="toolbar-row">
                <button type="button" onClick={onSaveThreatModel} disabled={loading || !threatModelTitleDraft.trim()}>
                  Save threat model
                </button>
                <button type="button" className="ghost-button" onClick={onDeleteThreatModel} disabled={loading || !selectedThreatModel}>
                  Delete threat model
                </button>
              </div>
            </div>
          </section>

          <section className="detail-section">
            <div className="panel-header">
              <div>
                <h4>Browser dumps</h4>
                <p className="subtle">Saved DOM, console, and network snapshots that keep browser bugs reproducible across agents.</p>
              </div>
              <span className="tag">{issueContextPacket?.browser_dumps.length ?? 0} linked</span>
            </div>
            <div className="toolbar-row">
              <label className="detail-section field-stack runbook-picker" htmlFor="browser-dump-select">
                <span className="filter-label">Selected dump</span>
                <select
                  id="browser-dump-select"
                  name="browser-dump-select"
                  className="text-input"
                  value={selectedBrowserDumpId}
                  onChange={(event) => onSelectedBrowserDumpChange(event.target.value)}
                >
                  <option value="">New browser dump</option>
                  {(issueContextPacket?.browser_dumps ?? []).map((dump) => (
                    <option key={dump.dump_id} value={dump.dump_id}>
                      {dump.label}
                    </option>
                  ))}
                </select>
              </label>
              {selectedBrowserDump ? (
                <div className="tag-row">
                  <span className="tag">{selectedBrowserDump.source}</span>
                  {selectedBrowserDump.page_title ? <span className="tag">{selectedBrowserDump.page_title}</span> : null}
                  {selectedBrowserDump.page_url ? <span className="tag">{selectedBrowserDump.page_url}</span> : null}
                </div>
              ) : (
                <span className="subtle">Capture browser state from MCP, Playwright, or manual notes and keep it attached to the issue context.</span>
              )}
            </div>
            <div className="detail-section runbook-editor">
              <div className="toolbar-row">
                <label className="detail-section field-stack" htmlFor="browser-dump-source">
                  <span className="filter-label">Source</span>
                  <select
                    id="browser-dump-source"
                    name="browser-dump-source"
                    className="text-input"
                    value={browserDumpSourceDraft}
                    onChange={(event) => onBrowserDumpSourceChange(event.target.value as BrowserDumpRecord['source'])}
                  >
                    {['manual', 'mcp-chrome', 'playwright', 'other'].map((source) => (
                      <option key={source} value={source}>
                        {source}
                      </option>
                    ))}
                  </select>
                </label>
                <label className="detail-section field-stack" htmlFor="browser-dump-label">
                  <span className="filter-label">Label</span>
                  <input
                    id="browser-dump-label"
                    name="browser-dump-label"
                    className="text-input"
                    placeholder="checkout failure on confirm"
                    value={browserDumpLabelDraft}
                    onChange={(event) => onBrowserDumpLabelChange(event.target.value)}
                  />
                </label>
              </div>
              <div className="toolbar-row">
                <label className="detail-section field-stack" htmlFor="browser-dump-page-title">
                  <span className="filter-label">Page title</span>
                  <input
                    id="browser-dump-page-title"
                    name="browser-dump-page-title"
                    className="text-input"
                    placeholder="Checkout confirmation"
                    value={browserDumpPageTitleDraft}
                    onChange={(event) => onBrowserDumpPageTitleChange(event.target.value)}
                  />
                </label>
                <label className="detail-section field-stack" htmlFor="browser-dump-page-url">
                  <span className="filter-label">Page URL</span>
                  <input
                    id="browser-dump-page-url"
                    name="browser-dump-page-url"
                    className="text-input"
                    placeholder="https://app.example.test/orders/123"
                    value={browserDumpPageUrlDraft}
                    onChange={(event) => onBrowserDumpPageUrlChange(event.target.value)}
                  />
                </label>
              </div>
              <label className="detail-section field-stack" htmlFor="browser-dump-summary">
                <span className="filter-label">Summary</span>
                <textarea
                  id="browser-dump-summary"
                  name="browser-dump-summary"
                  className="text-area"
                  rows={3}
                  placeholder="What the operator saw and why this snapshot matters."
                  value={browserDumpSummaryDraft}
                  onChange={(event) => onBrowserDumpSummaryChange(event.target.value)}
                />
              </label>
              <label className="detail-section field-stack" htmlFor="browser-dump-dom">
                <span className="filter-label">DOM snapshot</span>
                <textarea
                  id="browser-dump-dom"
                  name="browser-dump-dom"
                  className="text-area"
                  rows={6}
                  placeholder="Relevant DOM excerpt, HTML dump, or accessibility tree text."
                  value={browserDumpDomSnapshotDraft}
                  onChange={(event) => onBrowserDumpDomSnapshotChange(event.target.value)}
                />
              </label>
              <div className="toolbar-row">
                <label className="detail-section field-stack" htmlFor="browser-dump-console">
                  <span className="filter-label">Console messages</span>
                  <textarea
                    id="browser-dump-console"
                    name="browser-dump-console"
                    className="text-area"
                    rows={4}
                    placeholder={'One console line per row'}
                    value={browserDumpConsoleDraft}
                    onChange={(event) => onBrowserDumpConsoleChange(event.target.value)}
                  />
                </label>
                <label className="detail-section field-stack" htmlFor="browser-dump-network">
                  <span className="filter-label">Network requests</span>
                  <textarea
                    id="browser-dump-network"
                    name="browser-dump-network"
                    className="text-area"
                    rows={4}
                    placeholder={'One request per row'}
                    value={browserDumpNetworkDraft}
                    onChange={(event) => onBrowserDumpNetworkChange(event.target.value)}
                  />
                </label>
              </div>
              <div className="toolbar-row">
                <label className="detail-section field-stack" htmlFor="browser-dump-screenshot-path">
                  <span className="filter-label">Screenshot path</span>
                  <input
                    id="browser-dump-screenshot-path"
                    name="browser-dump-screenshot-path"
                    className="text-input"
                    placeholder="artifacts/checkout-confirmation.png"
                    value={browserDumpScreenshotPathDraft}
                    onChange={(event) => onBrowserDumpScreenshotPathChange(event.target.value)}
                  />
                </label>
                <label className="detail-section field-stack" htmlFor="browser-dump-notes">
                  <span className="filter-label">Notes</span>
                  <textarea
                    id="browser-dump-notes"
                    name="browser-dump-notes"
                    className="text-area"
                    rows={3}
                    placeholder="Extra operator notes, browser version, or repro detail."
                    value={browserDumpNotesDraft}
                    onChange={(event) => onBrowserDumpNotesChange(event.target.value)}
                  />
                </label>
              </div>
              <div className="toolbar-row">
                <button type="button" onClick={onSaveBrowserDump} disabled={loading || !browserDumpLabelDraft.trim()}>
                  Save browser dump
                </button>
                <button type="button" className="ghost-button" onClick={onDeleteBrowserDump} disabled={loading || !selectedBrowserDump}>
                  Delete browser dump
                </button>
              </div>
            </div>
            <div className="activity-list">
              {(issueContextPacket?.browser_dumps ?? []).length ? (
                (issueContextPacket?.browser_dumps ?? []).map((dump) => (
                  <div key={dump.dump_id} className="activity-entry">
                    <div className="activity-entry-top">
                      <strong>{dump.label}</strong>
                      <small>{formatDate(dump.updated_at)}</small>
                    </div>
                    <div className="row-meta">
                      <span className="tag">{dump.source}</span>
                      {dump.page_title ? <span className="tag">{dump.page_title}</span> : null}
                      <span className="tag">console {dump.console_messages.length}</span>
                      <span className="tag">network {dump.network_requests.length}</span>
                    </div>
                    <p>{dump.summary || 'No summary recorded.'}</p>
                    {dump.notes ? <pre className="detail-mini-block">{dump.notes}</pre> : null}
                    {dump.dom_snapshot ? <pre className="detail-mini-block">{dump.dom_snapshot}</pre> : null}
                  </div>
                ))
              ) : (
                <p className="subtle">No browser dumps saved for this issue yet.</p>
              )}
            </div>
          </section>

          <WorktreeSnapshot label="Workspace provenance" worktree={issueContextPacket?.worktree} />

          <section className="detail-section">
            <h4>History</h4>
            <div className="activity-list">
              {issueActivity.length ? (
                issueActivity.map((item) => (
                  <div key={item.activity_id} className="activity-entry">
                    <div className="activity-entry-top">
                      <strong>{item.summary}</strong>
                      <small>{formatDate(item.created_at)}</small>
                    </div>
                    <div className="row-meta">
                      <span className="tag">{item.action}</span>
                      <span className="tag">{item.actor.label}</span>
                      <span className="tag">{item.actor.key}</span>
                    </div>
                  </div>
                ))
              ) : (
                <p className="subtle">No issue history yet.</p>
              )}
            </div>
          </section>

          {activeView === 'issues' ? (
            <>
              <section className="detail-section">
                <h4>Issue transitions</h4>
                <div className="activity-list">
                  {issueContextPacket?.recent_activity?.length ? (
                    issueContextPacket.recent_activity.map((item) => {
                      const beforeAfter = (item.details?.before_after as Record<string, { from?: unknown; to?: unknown }> | undefined) ?? {}
                      const fields = Object.entries(beforeAfter)
                      return (
                        <div key={item.activity_id} className="activity-entry">
                          <div className="activity-entry-top">
                            <strong>{item.summary}</strong>
                            <small>{formatDate(item.created_at)}</small>
                          </div>
                          <div className="row-meta">
                            <span className="tag">{item.actor.label}</span>
                            <span className="tag">{item.action}</span>
                          </div>
                          {fields.length ? (
                            <div className="transition-list">
                              {fields.map(([field, change]) => (
                                <div key={`${item.activity_id}-${field}`} className="transition-row">
                                  <span className="transition-field">{field}</span>
                                  <span className="transition-arrow">
                                    {String(change.from ?? 'unset')}
                                    {' -> '}
                                    {String(change.to ?? 'unset')}
                                  </span>
                                </div>
                              ))}
                            </div>
                          ) : null}
                        </div>
                      )
                    })
                  ) : (
                    <p className="subtle">No tracker transitions recorded yet.</p>
                  )}
                </div>
              </section>

              <section className="detail-section">
                <h4>Recorded fixes</h4>
                <div className="activity-list">
                  {issueContextPacket?.recent_fixes?.length ? (
                    issueContextPacket.recent_fixes.map((fix) => (
                      <div key={fix.fix_id} className="activity-entry">
                        <div className="activity-entry-top">
                          <strong>{fix.summary}</strong>
                          <small>{formatDate(fix.recorded_at)}</small>
                        </div>
                        <div className="row-meta">
                          <span className="tag">{fix.status}</span>
                          <span className="tag">{fix.actor.label}</span>
                          {fix.run_id ? <span className="tag">{fix.run_id}</span> : null}
                          {fix.session_id ? <span className="tag">{fix.session_id}</span> : null}
                        </div>
                        {fix.changed_files.length ? (
                          <div className="tag-row">
                            {fix.changed_files.slice(0, 6).map((path) => (
                              <span key={path} className="tag">
                                {path}
                              </span>
                            ))}
                          </div>
                        ) : null}
                        {fix.tests_run.length ? (
                          <pre className="detail-mini-block">{fix.tests_run.join('\n')}</pre>
                        ) : null}
                        {fix.worktree?.available ? <WorktreeSnapshot label="Recorded worktree" worktree={fix.worktree} /> : null}
                      </div>
                    ))
                  ) : (
                    <p className="subtle">No fixes recorded for this issue yet.</p>
                  )}
                </div>
              </section>

              <section className="detail-section">
                <h4>Record fix</h4>
                <div className="toolbar-row">
                  <button
                    onClick={onUseSelectedRunForFix}
                    disabled={!selectedRun || selectedRun.issue_id !== selectedIssue.bug_id}
                  >
                    Use selected run
                  </button>
                  {fixRunIdDraft ? (
                    <>
                      <span className="tag">attached run: {fixRunIdDraft}</span>
                      <button className="ghost-button" onClick={onClearFixRun}>
                        Clear run
                      </button>
                    </>
                  ) : (
                    <span className="subtle">Select a run for this issue to attach agent provenance automatically.</span>
                  )}
                </div>
                {selectedRun && selectedRun.issue_id === selectedIssue.bug_id && selectedRun.status === 'completed' ? (
                  <div className="toolbar-row">
                    <button className="ghost-button" onClick={onMarkRunInvestigationOnly} disabled={loading}>
                      Mark investigation only
                    </button>
                    <button className="ghost-button" onClick={onDismissRunReview} disabled={loading}>
                      Dismiss review candidate
                    </button>
                  </div>
                ) : null}
                <label className="detail-section field-stack" htmlFor="fix-summary">
                  <span className="filter-label">Fix summary</span>
                  <input
                    id="fix-summary"
                    name="fix-summary"
                    className="text-input"
                    placeholder="What changed"
                    value={fixSummaryDraft}
                    onChange={(event) => onFixSummaryChange(event.target.value)}
                  />
                </label>
                <label className="detail-section field-stack" htmlFor="fix-how">
                  <span className="filter-label">How it works</span>
                  <textarea
                    id="fix-how"
                    name="fix-how"
                    className="text-area"
                    rows={3}
                    placeholder="Short engineering explanation"
                    value={fixHowDraft}
                    onChange={(event) => onFixHowChange(event.target.value)}
                  />
                </label>
                <div className="toolbar-row">
                  <label className="detail-section field-stack" htmlFor="fix-files">
                    <span className="filter-label">Changed files</span>
                    <input
                      id="fix-files"
                      name="fix-files"
                      className="text-input"
                      placeholder="a.py,b.ts"
                      value={fixChangedFilesDraft}
                      onChange={(event) => onFixChangedFilesChange(event.target.value)}
                    />
                  </label>
                  <label className="detail-section field-stack" htmlFor="fix-status">
                    <span className="filter-label">Issue status after fix</span>
                    <select
                      id="fix-status"
                      name="fix-status"
                      className="text-input"
                      value={fixIssueStatusDraft}
                      onChange={(event) => onFixIssueStatusChange(event.target.value)}
                    >
                      {['open', 'triaged', 'investigating', 'in_progress', 'verification', 'resolved', 'partial'].map((status) => (
                        <option key={status} value={status}>
                          {status}
                        </option>
                      ))}
                    </select>
                  </label>
                </div>
                <label className="detail-section field-stack" htmlFor="fix-tests">
                  <span className="filter-label">Tests run</span>
                  <input
                    id="fix-tests"
                    name="fix-tests"
                    className="text-input"
                    placeholder="pytest test_file.py -q, npm test"
                    value={fixTestsDraft}
                    onChange={(event) => onFixTestsChange(event.target.value)}
                  />
                </label>
                <div className="toolbar-row">
                  <button onClick={onRecordFix} disabled={loading || !fixSummaryDraft.trim()}>
                    Record fix
                  </button>
                  <span className="subtle">Uses the selected run automatically when it matches this issue.</span>
                </div>
              </section>

              <section className="detail-section">
                <div className="panel-header">
                  <div>
                    <h4>Repository guidance</h4>
                    <p className="subtle">Always-on instructions and optional skills discovered in this repo.</p>
                  </div>
                  <div className="row-meta">
                    <span className="tag">{issueContextPacket?.guidance.length ?? 0} attached</span>
                  </div>
                </div>
                <GuidanceList
                  guidance={issueContextPacket?.guidance ?? []}
                  emptyLabel="No repository guidance files were attached to this issue context."
                />
              </section>

              <section className="detail-section">
                <div className="panel-header">
                  <div>
                    <h4>Path instructions</h4>
                    <p className="subtle">Repo-config rules matched against the issue’s ranked paths, similar to scoped review instructions.</p>
                  </div>
                  <div className="row-meta">
                    <span className="tag">{issueContextPacket?.matched_path_instructions.length ?? 0} matched</span>
                    {issueContextPacket?.repo_config?.source_path ? <span className="tag">{issueContextPacket.repo_config.source_path}</span> : null}
                  </div>
                </div>
                {issueContextPacket?.matched_path_instructions?.length ? (
                  <div className="activity-list">
                    {issueContextPacket.matched_path_instructions.map((item) => (
                      <div key={item.instruction_id} className="activity-entry">
                        <div className="activity-header">
                          <strong>{item.title || item.path}</strong>
                          <div className="row-meta">
                            {item.matched_paths.map((path) => (
                              <span key={`${item.instruction_id}-${path}`} className="tag">
                                {path}
                              </span>
                            ))}
                          </div>
                        </div>
                        <p>{item.instructions}</p>
                      </div>
                    ))}
                  </div>
                ) : (
                  <p className="subtle">No `.xmustard` path instructions matched the current issue packet.</p>
                )}
              </section>

              <section className="detail-section">
                <h4>Agent context</h4>
                <pre className="prompt-block">{issueContextPacket?.prompt ?? 'Loading context packet...'}</pre>
              </section>

              <section className="detail-section">
                <div className="panel-header">
                  <div>
                    <h4>Context replays</h4>
                    <p className="subtle">Saved prompt snapshots for future eval and replay comparisons.</p>
                  </div>
                  <span className="tag">{issueContextReplays.length} saved</span>
                </div>
                <div className="toolbar-row">
                  <label className="detail-section field-stack" htmlFor="context-replay-label">
                    <span className="filter-label">Replay label</span>
                    <input
                      id="context-replay-label"
                      name="context-replay-label"
                      className="text-input"
                      placeholder="baseline prompt"
                      value={contextReplayLabelDraft}
                      onChange={(event) => onContextReplayLabelChange(event.target.value)}
                    />
                  </label>
                  <button type="button" onClick={onCaptureIssueContextReplay} disabled={loading}>
                    Capture replay
                  </button>
                </div>
                <div className="activity-list">
                  {issueContextReplays.length ? (
                    issueContextReplays.map((replay) => (
                      <div key={replay.replay_id} className="activity-entry">
                        <div className="activity-entry-top">
                          <strong>{replay.label}</strong>
                          <small>{formatDate(replay.created_at)}</small>
                        </div>
                        <div className="row-meta">
                          <span className="tag">focus {replay.tree_focus.length}</span>
                          <span className="tag">guidance {replay.guidance_paths.length}</span>
                          <span className="tag">profiles {replay.verification_profile_ids.length}</span>
                          <span className="tag">browser {replay.browser_dump_ids.length}</span>
                        </div>
                        <div className="toolbar-row">
                          <button
                            type="button"
                            className={selectedContextReplayId === replay.replay_id ? 'secondary' : ''}
                            onClick={() => onSelectContextReplay(replay.replay_id)}
                          >
                            Select
                          </button>
                          <button type="button" onClick={() => onCompareIssueContextReplay(replay.replay_id)} disabled={loading}>
                            Compare
                          </button>
                        </div>
                        <pre className="detail-mini-block">{replay.prompt}</pre>
                      </div>
                    ))
                  ) : (
                    <p className="subtle">No prompt snapshots saved for this issue yet.</p>
                  )}
                </div>
                {contextReplayComparison && selectedContextReplayId === contextReplayComparison.replay.replay_id ? (
                  <div className="detail-section field-stack">
                    <div className="panel-header">
                      <div>
                        <h4>Replay comparison</h4>
                        <p className="subtle">{contextReplayComparison.summary}</p>
                      </div>
                      <div className="row-meta">
                        <span className={`status-pill ${contextReplayComparison.changed ? 'amber' : 'green'}`}>
                          {contextReplayComparison.changed ? 'Drift detected' : 'No drift'}
                        </span>
                      </div>
                    </div>
                    <div className="row-meta">
                      <span className="tag">saved prompt {contextReplayComparison.saved_prompt_length} chars</span>
                      <span className="tag">current prompt {contextReplayComparison.current_prompt_length} chars</span>
                      {contextReplayComparison.prompt_changed ? <span className="tag">prompt changed</span> : null}
                    </div>
                    {replayDeltaBlock(
                      'Tree focus',
                      contextReplayComparison.added_tree_focus,
                      contextReplayComparison.removed_tree_focus,
                    )}
                    {replayDeltaBlock(
                      'Guidance paths',
                      contextReplayComparison.added_guidance_paths,
                      contextReplayComparison.removed_guidance_paths,
                    )}
                    {replayDeltaBlock(
                      'Verification profiles',
                      contextReplayComparison.added_verification_profile_ids,
                      contextReplayComparison.removed_verification_profile_ids,
                    )}
                    {replayDeltaBlock(
                      'Ticket contexts',
                      contextReplayComparison.added_ticket_context_ids,
                      contextReplayComparison.removed_ticket_context_ids,
                    )}
                    {replayDeltaBlock(
                      'Browser dumps',
                      contextReplayComparison.added_browser_dump_ids,
                      contextReplayComparison.removed_browser_dump_ids,
                    )}
                  </div>
                ) : null}
              </section>

              {activeView === 'issues' ? (
                <section className="detail-section">
                  <h4>Create tracker issue</h4>
                  <label className="detail-section field-stack" htmlFor="new-issue-title">
                    <span className="filter-label">Title</span>
                    <input
                      id="new-issue-title"
                      name="new-issue-title"
                      className="text-input"
                      placeholder="New tracker-native issue"
                      value={newIssueTitleDraft}
                      onChange={(event) => onNewIssueTitleChange(event.target.value)}
                    />
                  </label>
                  <div className="toolbar-row">
                    <label className="detail-section field-stack" htmlFor="new-issue-severity">
                      <span className="filter-label">Severity</span>
                      <select
                        id="new-issue-severity"
                        name="new-issue-severity"
                        className="text-input"
                        value={newIssueSeverityDraft}
                        onChange={(event) => onNewIssueSeverityChange(event.target.value)}
                      >
                        {['P0', 'P1', 'P2', 'P3'].map((severity) => (
                          <option key={severity} value={severity}>
                            {severity}
                          </option>
                        ))}
                      </select>
                    </label>
                    <label className="detail-section field-stack" htmlFor="new-issue-labels">
                      <span className="filter-label">Labels</span>
                      <input
                        id="new-issue-labels"
                        name="new-issue-labels"
                        className="text-input"
                        placeholder="tracker,ui"
                        value={newIssueLabelsDraft}
                        onChange={(event) => onNewIssueLabelsChange(event.target.value)}
                      />
                    </label>
                  </div>
                  <label className="detail-section field-stack" htmlFor="new-issue-summary">
                    <span className="filter-label">Summary</span>
                    <textarea
                      id="new-issue-summary"
                      name="new-issue-summary"
                      className="text-area"
                      rows={3}
                      placeholder="Why this issue should be tracked"
                      value={newIssueSummaryDraft}
                      onChange={(event) => onNewIssueSummaryChange(event.target.value)}
                    />
                  </label>
                  <div className="toolbar-row">
                    <button onClick={onCreateIssue} disabled={loading || !newIssueTitleDraft.trim()}>
                      Create tracker issue
                    </button>
                    <span className="subtle">Creates a tracker-native issue without touching markdown.</span>
                  </div>
                </section>
              ) : null}
            </>
          ) : null}
        </>
      ) : null}

      {activeView === 'signals' && selectedSignal ? (
        <>
          <div className="panel-header">
            <div>
              <p className="eyebrow">Discovery signal</p>
              <h3>{selectedSignal.title}</h3>
            </div>
            <StatusPill tone={selectedSignal.severity.toLowerCase()}>{selectedSignal.severity}</StatusPill>
          </div>
          <div className="detail-copy">
            <p>{selectedSignal.summary}</p>
            <p className="subtle">
              {selectedSignal.file_path}:{selectedSignal.line}
            </p>
          </div>
          <section className="detail-section">
            <h4>Promote to issue</h4>
            <div className="toolbar-row">
              <button onClick={onPromoteSignal} disabled={Boolean(selectedSignal.promoted_bug_id)}>
                {selectedSignal.promoted_bug_id ? `Promoted as ${selectedSignal.promoted_bug_id}` : 'Promote signal'}
              </button>
            </div>
          </section>
        </>
      ) : null}

      {activeView === 'runs' && selectedRun ? (
        <>
          <div className="panel-header">
            <div>
              <p className="eyebrow">Run detail</p>
              <h3>{selectedRun.issue_id}</h3>
            </div>
            <StatusPill tone={selectedRun.status}>{selectedRun.status}</StatusPill>
          </div>

          {selectedRun.status === 'planning' ? (
            !selectedRunPlan ? (
              <section className="detail-section">
                <h4>Planning</h4>
                <p className="subtle">This run is waiting for plan approval.</p>
                <div className="toolbar-row">
                  <button type="button" onClick={onGeneratePlan} disabled={loading}>
                    Generate plan
                  </button>
                </div>
              </section>
            ) : (
              <PlanPreview
                plan={selectedRunPlan}
                loading={loading}
                onApprove={onApprovePlan}
                onReject={onRejectPlan}
              />
            )
          ) : (
            <>
              <section className="detail-section">
                <h4>Command</h4>
                <pre className="prompt-block">{selectedRun.command_preview}</pre>
              </section>
              <section className="detail-section">
                <h4>Live log</h4>
                <pre className="terminal-block">{logContent || 'Waiting for output...'}</pre>
              </section>
              <section className="detail-section">
                <h4>Run summary</h4>
                <div className="evidence-row">
                  <span>Session</span>
                  <small>{selectedRun.summary?.session_id ?? 'none'}</small>
                </div>
                <div className="evidence-row">
                  <span>Events</span>
                  <small>{selectedRun.summary?.event_count ?? 0}</small>
                </div>
                <div className="evidence-row">
                  <span>Tool events</span>
                  <small>{selectedRun.summary?.tool_event_count ?? 0}</small>
                </div>
                <div className="evidence-row">
                  <span>Last event</span>
                  <small>{selectedRun.summary?.last_event_type ?? 'unknown'}</small>
                </div>
                <div className="evidence-row">
                  <span>Runbook</span>
                  <small>{selectedRun.runbook_id ?? 'none'}</small>
                </div>
                <div className="evidence-row">
                  <span>Guidance files</span>
                  <small>{selectedRun.guidance_paths.length ? selectedRun.guidance_paths.join(', ') : 'none attached'}</small>
                </div>
                <pre className="prompt-block">{selectedRun.summary?.text_excerpt ?? 'No structured text captured yet.'}</pre>
              </section>
              <section className="detail-section">
                <div className="panel-header">
                  <div>
                    <h4>Run insights</h4>
                    <p className="subtle">Session-style review for repo context usage, risks, and next steps.</p>
                  </div>
                  {runInsight ? <span className="tag">{formatDate(runInsight.generated_at)}</span> : null}
                </div>
                {runInsight ? (
                  <>
                    <p>{runInsight.summary}</p>
                    <div className="tag-row">
                      <span className="tag">headline: {runInsight.headline}</span>
                      {runInsight.guidance_used.map((path) => (
                        <span key={`insight-${path}`} className="tag">
                          {path}
                        </span>
                      ))}
                    </div>
                    <div className="summary-grid analysis-summary-grid">
                      <SummaryCard label="Strengths" value={runInsight.strengths.length} accent="green" />
                      <SummaryCard label="Risks" value={runInsight.risks.length} accent="red" />
                      <SummaryCard label="Recommendations" value={runInsight.recommendations.length} accent="amber" />
                      <SummaryCard label="Guidance used" value={runInsight.guidance_used.length} accent="blue" />
                    </div>
                    {runInsight.acceptance_review ? (
                      <>
                        <div className="tag-row">
                          <span className="tag">acceptance {runInsight.acceptance_review.status}</span>
                          <StatusPill tone={acceptanceTone(runInsight.acceptance_review.status)}>
                            {runInsight.acceptance_review.status}
                          </StatusPill>
                          {runInsight.scope_warnings.map((warning) => (
                            <span key={`${warning.kind}-${warning.message}`} className="tag">
                              {warning.kind}: {warning.severity}
                            </span>
                          ))}
                        </div>
                        <div className="analysis-list">
                          {runInsight.acceptance_review.matched.map((item) => (
                            <div key={`acceptance-match-${item}`} className="evidence-row">
                              <span>{item}</span>
                              <small>matched criterion</small>
                            </div>
                          ))}
                          {runInsight.acceptance_review.missing.map((item) => (
                            <div key={`acceptance-missing-${item}`} className="evidence-row">
                              <span>{item}</span>
                              <small>missing criterion</small>
                            </div>
                          ))}
                          {runInsight.scope_warnings.map((warning) => (
                            <div key={`scope-${warning.kind}-${warning.message}`} className="evidence-row">
                              <span>{warning.message}</span>
                              <small>{warning.paths.join(', ') || warning.severity}</small>
                            </div>
                          ))}
                        </div>
                      </>
                    ) : null}
                    <div className="analysis-list">
                      {runInsight.strengths.length ? (
                        runInsight.strengths.map((item) => (
                          <div key={`strength-${item}`} className="evidence-row">
                            <span>{item}</span>
                            <small>strength</small>
                          </div>
                        ))
                      ) : (
                        <p className="subtle">No strengths captured for this run yet.</p>
                      )}
                    </div>
                    <div className="analysis-list">
                      {runInsight.risks.length ? (
                        runInsight.risks.map((item) => (
                          <div key={`risk-${item}`} className="evidence-row">
                            <span>{item}</span>
                            <small>risk</small>
                          </div>
                        ))
                      ) : (
                        <p className="subtle">No notable risks captured.</p>
                      )}
                    </div>
                    <div className="analysis-list">
                      {runInsight.recommendations.length ? (
                        runInsight.recommendations.map((item) => (
                          <div key={`rec-${item}`} className="evidence-row">
                            <span>{item}</span>
                            <small>recommendation</small>
                          </div>
                        ))
                      ) : (
                        <p className="subtle">No recommendations available.</p>
                      )}
                    </div>
                  </>
                ) : (
                  <p className="subtle">No run insight generated yet.</p>
                )}
              </section>
              <section className="detail-section">
                <h4>Run metrics</h4>
                {runMetrics ? (
                  <>
                    <div className="summary-grid analysis-summary-grid">
                      <SummaryCard label="Cost" value={`$${runMetrics.estimated_cost.toFixed(4)}`} accent="amber" />
                      <SummaryCard label="Input" value={runMetrics.input_tokens} accent="blue" />
                      <SummaryCard label="Output" value={runMetrics.output_tokens} accent="green" />
                      <SummaryCard label="Duration" value={`${(runMetrics.duration_ms / 1000).toFixed(1)}s`} accent="sand" />
                    </div>
                    <div className="tag-row">
                      <span className="tag">{runMetrics.runtime}</span>
                      <span className="tag">{runMetrics.model}</span>
                      <span className="tag">calculated {formatDate(runMetrics.calculated_at)}</span>
                    </div>
                  </>
                ) : (
                  <p className="subtle">No run metrics captured yet.</p>
                )}
              </section>
              <CostSummaryPanel summary={costSummary} />
              <section className="detail-section">
                <div className="panel-header">
                  <div>
                    <h4>Patch critique</h4>
                    <p className="subtle">Post-run review artifacts generated from the stored run output.</p>
                  </div>
                  {selectedRun.status === 'completed' || selectedRun.status === 'failed' ? (
                    <button type="button" className="ghost-button" onClick={onGeneratePatchCritique} disabled={loading}>
                      Generate critique
                    </button>
                  ) : null}
                </div>
                {patchCritique ? (
                  <>
                    <div className="summary-grid analysis-summary-grid">
                      <SummaryCard label="Quality" value={patchCritique.overall_quality} accent="amber" />
                      <SummaryCard label="Correctness" value={`${patchCritique.correctness}%`} accent={qualityAccent(patchCritique.correctness)} />
                      <SummaryCard label="Completeness" value={`${patchCritique.completeness}%`} accent={qualityAccent(patchCritique.completeness)} />
                      <SummaryCard label="Safety" value={`${patchCritique.safety}%`} accent={qualityAccent(patchCritique.safety)} />
                    </div>
                    <p>{patchCritique.summary || 'No critique summary available.'}</p>
                    {patchCritique.acceptance_review ? (
                      <div className="analysis-list">
                        <div className="evidence-row">
                          <span>Acceptance review</span>
                          <small>{patchCritique.acceptance_review.status}</small>
                        </div>
                        {patchCritique.acceptance_review.missing.map((item) => (
                          <div key={`critique-missing-${item}`} className="evidence-row">
                            <span>{item}</span>
                            <small>missing criterion</small>
                          </div>
                        ))}
                        {patchCritique.scope_warnings.map((warning) => (
                          <div key={`critique-scope-${warning.kind}-${warning.message}`} className="evidence-row">
                            <span>{warning.message}</span>
                            <small>{warning.paths.join(', ') || warning.severity}</small>
                          </div>
                        ))}
                      </div>
                    ) : null}
                    {patchCritique.issues_found.length ? (
                      <div className="analysis-list">
                        {patchCritique.issues_found.map((issue) => (
                          <div key={issue} className="evidence-row">
                            <span>{issue}</span>
                            <small>critique issue</small>
                          </div>
                        ))}
                      </div>
                    ) : (
                      <p className="subtle">No critique issues were detected.</p>
                    )}
                  </>
                ) : (
                  <p className="subtle">No patch critique generated yet.</p>
                )}
                <div className="activity-list">
                  {runImprovements.length ? (
                    runImprovements.map((improvement) => (
                      <div key={improvement.suggestion_id} className="activity-entry">
                        <div className="activity-entry-top">
                          <strong>{improvement.description}</strong>
                          <small>{improvement.file_path}</small>
                        </div>
                        <div className="row-meta">
                          <span className="tag">{improvement.category}</span>
                          <span className="tag">{improvement.severity}</span>
                          {improvement.line_start ? <span className="tag">line {improvement.line_start}</span> : null}
                        </div>
                        {improvement.suggested_fix ? <p>{improvement.suggested_fix}</p> : null}
                        <div className="toolbar-row">
                          <button
                            type="button"
                            className="ghost-button"
                            onClick={() => onDismissImprovement(improvement.suggestion_id)}
                            disabled={loading}
                          >
                            Dismiss suggestion
                          </button>
                        </div>
                      </div>
                    ))
                  ) : (
                    <p className="subtle">No active improvement suggestions.</p>
                  )}
                </div>
              </section>
            </>
          )}

          {selectedRun.status !== 'planning' && (
            <WorktreeSnapshot label="Run provenance" worktree={selectedRun.worktree} />
          )}

          <section className="detail-section">
            <h4>Run history</h4>
            <div className="activity-list">
              {runActivity.length ? (
                runActivity.map((item) => (
                  <div key={item.activity_id} className="activity-entry">
                    <div className="activity-entry-top">
                      <strong>{item.summary}</strong>
                      <small>{formatDate(item.created_at)}</small>
                    </div>
                    <div className="row-meta">
                      <span className="tag">{item.action}</span>
                      <span className="tag">{item.actor.label}</span>
                      <span className="tag">{item.actor.key}</span>
                    </div>
                  </div>
                ))
              ) : (
                <p className="subtle">No run history yet.</p>
              )}
            </div>
          </section>
          <div className="toolbar-row">
            {selectedRun.status === 'completed' && selectedRun.issue_id !== 'workspace-query' ? (
              <button onClick={onAcceptRunDraft} disabled={loading}>
                Accept draft as fix
              </button>
            ) : null}
            {selectedRun.status === 'completed' && selectedRun.issue_id !== 'workspace-query' ? (
              <>
                <button className="ghost-button" onClick={onMarkRunInvestigationOnly} disabled={loading}>
                  Mark investigation only
                </button>
                <button className="ghost-button" onClick={onDismissRunReview} disabled={loading}>
                  Dismiss review
                </button>
              </>
            ) : null}
            <button onClick={onRetryRun} disabled={loading}>
              Retry run
            </button>
            <button onClick={onCancelRun} disabled={loading || selectedRun.status !== 'running'}>
              Cancel run
            </button>
          </div>
        </>
      ) : null}

      {activeView === 'activity' ? (
        <>
          <div className="panel-header">
            <div>
              <p className="eyebrow">Workspace activity</p>
              <h3>{selectedActivity?.summary ?? 'No activity selected'}</h3>
            </div>
            {selectedActivity ? <StatusPill tone={selectedActivity.actor.runtime ?? 'sand'}>{selectedActivity.actor.kind}</StatusPill> : null}
          </div>
          <ActivityDigestSummary
            overview={{
              totalEvents: activityOverview?.total_events ?? 0,
              uniqueActors: activityOverview?.unique_actors ?? 0,
              uniqueActions: activityOverview?.unique_actions ?? 0,
              topActors:
                activityOverview?.top_actors.map((item) => ({
                  label: item.label,
                  count: item.count,
                  secondaryText: item.actor_key ?? item.key,
                  tone: item.label.startsWith('codex:') ? 'codex' : item.label.startsWith('opencode:') ? 'opencode' : 'sand',
                })) ?? [],
              topActions:
                activityOverview?.top_actions.map((item) => ({
                  label: item.label,
                  count: item.count,
                  secondaryText: item.action ?? item.key,
                  tone: item.label.includes('completed') ? 'fixed' : item.label.includes('failed') ? 'red' : 'sand',
                })) ?? [],
              generatedAtLabel: activityOverview?.most_recent_at ? `Latest ${formatDate(activityOverview.most_recent_at)}` : null,
            }}
          />
          <section className="summary-grid">
            <SummaryCard label="Issues touched" value={activityOverview?.issues_touched ?? 0} accent="green" />
            <SummaryCard label="Fixes touched" value={activityOverview?.fixes_touched ?? 0} accent="blue" />
            <SummaryCard label="Runs touched" value={activityOverview?.runs_touched ?? 0} accent="red" />
            <SummaryCard label="Views touched" value={activityOverview?.views_touched ?? 0} accent="ink" />
            <SummaryCard label="Agent events" value={activityOverview?.agent_events ?? 0} accent="blue" />
            <SummaryCard label="Operator events" value={activityOverview?.operator_events ?? 0} accent="sand" />
            <SummaryCard label="System events" value={activityOverview?.system_events ?? 0} accent="amber" />
          </section>
          {selectedActivity ? (
            <>
              <section className="detail-section">
                <h4>Event</h4>
                <div className="evidence-row">
                  <span>Action</span>
                  <small>{selectedActivity.action}</small>
                </div>
                <div className="evidence-row">
                  <span>Actor</span>
                  <small>
                    {selectedActivity.actor.label}
                    {selectedActivity.actor.kind ? ` · ${selectedActivity.actor.kind}` : ''}
                  </small>
                </div>
                <div className="evidence-row">
                  <span>Actor key</span>
                  <small>{selectedActivity.actor.key}</small>
                </div>
                <div className="evidence-row">
                  <span>Entity</span>
                  <small>{selectedActivity.entity_type}:{selectedActivity.entity_id}</small>
                </div>
                <div className="evidence-row">
                  <span>Recorded</span>
                  <small>{formatDate(selectedActivity.created_at)}</small>
                </div>
              </section>
              <section className="detail-section">
                <h4>Details</h4>
                <pre className="prompt-block">{JSON.stringify(selectedActivity.details, null, 2)}</pre>
              </section>
            </>
          ) : (
            <p className="subtle">No workspace activity yet.</p>
          )}
          <section className="detail-section">
            <h4>Recent workspace history</h4>
            <div className="activity-list">
              {workspaceActivity.length ? (
                workspaceActivity.map((item) => (
                  <div key={item.activity_id} className="activity-entry">
                    <div className="activity-entry-top">
                      <strong>{item.summary}</strong>
                      <small>{formatDate(item.created_at)}</small>
                    </div>
                    <div className="row-meta">
                      <span className="tag">{item.action}</span>
                      <span className="tag">{item.actor.label}</span>
                      {item.issue_id ? <span className="tag">issue:{item.issue_id}</span> : null}
                      {item.run_id ? <span className="tag">run:{item.run_id}</span> : null}
                    </div>
                  </div>
                ))
              ) : (
                <p className="subtle">No workspace history yet.</p>
              )}
            </div>
          </section>
        </>
      ) : null}

      {activeView === 'sources' ? (
        <>
          <div className="panel-header">
            <div>
              <p className="eyebrow">Source detail</p>
              <h3>{selectedSource?.label ?? 'No source selected'}</h3>
            </div>
          </div>
          <section className="detail-section">
            <h4>Feed</h4>
            {selectedSource ? (
              <>
                <div className="evidence-row">
                  <span>Kind</span>
                  <small>{selectedSource.kind}</small>
                </div>
                <div className="evidence-row">
                  <span>Path</span>
                  <small>{selectedSource.path}</small>
                </div>
                <div className="evidence-row">
                  <span>Records</span>
                  <small>{selectedSource.record_count}</small>
                </div>
                <div className="evidence-row">
                  <span>Notes</span>
                  <small>{selectedSource.notes ?? 'None'}</small>
                </div>
              </>
            ) : (
              <p className="subtle">No source metadata loaded.</p>
            )}
          </section>
          <section className="detail-section">
            <h4>Drift hotspots</h4>
            <div className="tag-row">
              {Object.entries(driftSummary).length ? (
                Object.entries(driftSummary).map(([flag, count]) => (
                  <span key={flag} className="tag">
                    {flag}: {count}
                  </span>
                ))
              ) : (
                <span className="subtle">No drift summary loaded.</span>
              )}
            </div>
          </section>
          <section className="detail-section">
            <div className="panel-header">
              <div>
                <h4>Repository guidance</h4>
                <p className="subtle">Discovered agent instructions, conventions, and optional skills for this workspace.</p>
              </div>
              <span className="tag">{workspaceGuidance.length} files</span>
            </div>
            <GuidanceList
              guidance={workspaceGuidance}
              emptyLabel="No repository guidance files were discovered for this workspace."
            />
          </section>
        </>
      ) : null}

      {activeView === 'tree' ? (
        <>
          <div className="panel-header">
            <div>
              <p className="eyebrow">Tree detail</p>
              <h3>{snapshotRootPath ?? 'No workspace loaded'}</h3>
            </div>
          </div>
          <section className="detail-section">
            <h4>Scan sources</h4>
            <div className="evidence-row">
              <span>Ledger</span>
              <small>{latestLedger ?? 'None'}</small>
            </div>
            <div className="evidence-row">
              <span>Verdicts</span>
              <small>{latestVerdicts ?? 'None'}</small>
            </div>
          </section>
        </>
      ) : null}
    </div>
  )
}
