import type {
  ActivityRecord,
  ActivityOverview,
  DiscoverySignal,
  IssueContextPacket,
  IssueDriftDetail,
  IssueRecord,
  RunRecord,
  SourceRecord,
  ViewMode,
} from '../lib/types'
import { SummaryCard } from './TrackerPrimitives'
import { StatusPill, formatDate } from './TrackerPrimitives'
import { ActivityDigestSummary } from './ActivityDigestSummary'

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
  issueContextPacket: IssueContextPacket | null
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
  onRetryRun: () => void
  onCancelRun: () => void
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
  issueContextPacket,
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
  onRetryRun,
  onCancelRun,
}: Props) {
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
                <h4>Agent context</h4>
                <pre className="prompt-block">{issueContextPacket?.prompt ?? 'Loading context packet...'}</pre>
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
            <pre className="prompt-block">{selectedRun.summary?.text_excerpt ?? 'No structured text captured yet.'}</pre>
          </section>
          <WorktreeSnapshot label="Run provenance" worktree={selectedRun.worktree} />
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
