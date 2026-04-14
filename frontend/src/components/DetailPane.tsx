import type {
  ActivityRecord,
  ActivityOverview,
  CostSummary,
  CoverageDelta,
  DiscoverySignal,
  DuplicateMatch,
  ImprovementSuggestion,
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
  issueContextReplays: IssueContextReplayRecord[]
  contextReplayLabelDraft: string
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
  onContextReplayLabelChange: (value: string) => void
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
  onCaptureIssueContextReplay: () => void
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
  issueContextReplays,
  contextReplayLabelDraft,
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
  onContextReplayLabelChange,
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
  onCaptureIssueContextReplay,
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
                verificationProfiles.map((profile) => (
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
                    </div>
                    <pre className="detail-mini-block">{profile.test_command}</pre>
                    {profile.coverage_command ? <pre className="detail-mini-block">{profile.coverage_command}</pre> : null}
                    {profile.source_paths.length ? (
                      <div className="tag-row">
                        {profile.source_paths.map((path) => (
                          <span key={`${profile.profile_id}-${path}`} className="tag">
                            {path}
                          </span>
                        ))}
                      </div>
                    ) : null}
                  </div>
                ))
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
                        </div>
                        <pre className="detail-mini-block">{replay.prompt}</pre>
                      </div>
                    ))
                  ) : (
                    <p className="subtle">No prompt snapshots saved for this issue yet.</p>
                  )}
                </div>
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
