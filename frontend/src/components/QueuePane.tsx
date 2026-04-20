import { useMemo, useState } from 'react'

import { IssueQueueControls } from './IssueQueueControls'
import { QueuePresetStrip, type QueuePreset } from './QueuePresetStrip'
import { SavedViewStrip } from './SavedViewStrip'
import { StatusPill } from './TrackerPrimitives'
import { WorkspaceActivityPanel } from './WorkspaceActivityPanel'
import { formatDate } from '../lib/format'
import type {
  ActivityRecord,
  CostSummary,
  DiscoverySignal,
  IssueQueueFilters,
  IssueQualityScore,
  IssueRecord,
  RunMetrics,
  RunRecord,
  SavedIssueView,
  SourceRecord,
  TreeNode,
  ViewMode,
} from '../lib/types'

const PAGE_SIZE = 60

type Props = {
  activeView: ViewMode
  issueQueue: IssueRecord[]
  signalQueue: DiscoverySignal[]
  runs: RunRecord[]
  reviewRuns: RunRecord[]
  sources: SourceRecord[]
  treeNodes: TreeNode[]
  treePath: string
  activity: ActivityRecord[]
  costSummary: CostSummary | null
  issueQualityById: Record<string, IssueQualityScore>
  runMetricsById: Record<string, RunMetrics>
  selectedIssueId: string | null
  selectedSignalId: string | null
  selectedRunId: string | null
  selectedSourceId: string | null
  selectedActivityId: string | null
  savedViews: SavedIssueView[]
  selectedSavedViewName?: string
  activeSavedViewId: string | null
  activePresetId: string | null
  presets: QueuePreset[]
  issueFilters: IssueQueueFilters
  issueLabelFilterDraft: string
  savedViewName: string
  signalQuery: string
  activityQuery: string
  activityActionFilter: string
  activityEntityTypeFilter: 'all' | ActivityRecord['entity_type']
  activityActorKindFilter: 'all' | ActivityRecord['actor']['kind']
  onSelectIssue: (issueId: string) => void
  onSelectSignal: (signalId: string) => void
  onSelectRun: (runId: string) => void
  onSelectSource: (sourceId: string) => void
  onSelectActivity: (activityId: string) => void
  onSelectPreset: (preset: QueuePreset) => void
  onClearPresetAndViews: () => void
  onSelectSavedView: (view: SavedIssueView) => void
  onFiltersChange: (nextFilters: IssueQueueFilters) => void
  onIssueLabelsDraftChange: (value: string) => void
  onSavedViewNameChange: (value: string) => void
  onSaveView: () => void
  onUpdateView: () => void
  onDeleteView: () => void
  onSignalQueryChange: (value: string) => void
  onActivityQueryChange: (value: string) => void
  onActivityActionFilterChange: (value: string) => void
  onActivityEntityTypeFilterChange: (value: 'all' | ActivityRecord['entity_type']) => void
  onActivityActorKindFilterChange: (value: 'all' | ActivityRecord['actor']['kind']) => void
  onNavigateTree: (path: string) => void
}

function queueEyebrow(activeView: ViewMode) {
  if (activeView === 'issues') return 'Issue queue'
  if (activeView === 'review') return 'Review inbox'
  if (activeView === 'signals') return 'Discovery queue'
  if (activeView === 'runs') return 'Run queue'
  if (activeView === 'sources') return 'Source inventory'
  if (activeView === 'drift') return 'Drift queue'
  return 'Tree browser'
}

function queueHeading(
  activeView: ViewMode,
  issueQueue: IssueRecord[],
  signalQueue: DiscoverySignal[],
  runs: RunRecord[],
  reviewRuns: RunRecord[],
  sources: SourceRecord[],
  activity: ActivityRecord[],
  treePath: string,
) {
  if (activeView === 'issues') return `${issueQueue.length} visible issues`
  if (activeView === 'review') return `${reviewRuns.length} review candidates`
  if (activeView === 'signals') return `${signalQueue.length} discovery signals`
  if (activeView === 'runs') return `${runs.length} recorded runs`
  if (activeView === 'activity') return `${activity.length} activity events`
  if (activeView === 'sources') return `${sources.length} source feeds`
  if (activeView === 'drift') return `${issueQueue.length} drifting issues`
  return treePath || '/'
}

export function QueuePane({
  activeView,
  issueQueue,
  signalQueue,
  runs,
  reviewRuns,
  sources,
  treeNodes,
  treePath,
  activity,
  costSummary,
  issueQualityById,
  runMetricsById,
  selectedIssueId,
  selectedSignalId,
  selectedRunId,
  selectedSourceId,
  selectedActivityId,
  savedViews,
  selectedSavedViewName,
  activeSavedViewId,
  activePresetId,
  presets,
  issueFilters,
  issueLabelFilterDraft,
  savedViewName,
  signalQuery,
  activityQuery,
  activityActionFilter,
  activityEntityTypeFilter,
  activityActorKindFilter,
  onSelectIssue,
  onSelectSignal,
  onSelectRun,
  onSelectSource,
  onSelectActivity,
  onSelectPreset,
  onClearPresetAndViews,
  onSelectSavedView,
  onFiltersChange,
  onIssueLabelsDraftChange,
  onSavedViewNameChange,
  onSaveView,
  onUpdateView,
  onDeleteView,
  onSignalQueryChange,
  onActivityQueryChange,
  onActivityActionFilterChange,
  onActivityEntityTypeFilterChange,
  onActivityActorKindFilterChange,
  onNavigateTree,
}: Props) {
  const [pageState, setPageState] = useState({ scope: '', value: 0 })
  const totalCount =
    activeView === 'issues' || activeView === 'drift'
      ? issueQueue.length
      : activeView === 'review'
        ? reviewRuns.length
      : activeView === 'signals'
        ? signalQueue.length
        : activeView === 'runs'
          ? runs.length
          : activeView === 'sources'
            ? sources.length
            : activeView === 'tree'
              ? treeNodes.length
              : activity.length
  const pageCount = Math.max(1, Math.ceil(totalCount / PAGE_SIZE))
  const paginationScope = `${activeView}:${issueQueue.length}:${reviewRuns.length}:${signalQueue.length}:${runs.length}:${sources.length}:${treeNodes.length}:${activity.length}`
  const currentPage = pageState.scope === paginationScope ? Math.min(pageState.value, pageCount - 1) : 0

  function updatePage(nextValue: number | ((current: number) => number)) {
    setPageState((current) => {
      const currentValue = current.scope === paginationScope ? current.value : 0
      const resolved = typeof nextValue === 'function' ? nextValue(currentValue) : nextValue
      return { scope: paginationScope, value: resolved }
    })
  }

  const pagedIssues = useMemo(
    () => issueQueue.slice(currentPage * PAGE_SIZE, currentPage * PAGE_SIZE + PAGE_SIZE),
    [currentPage, issueQueue],
  )
  const pagedSignals = useMemo(
    () => signalQueue.slice(currentPage * PAGE_SIZE, currentPage * PAGE_SIZE + PAGE_SIZE),
    [currentPage, signalQueue],
  )
  const pagedRuns = useMemo(
    () => runs.slice(currentPage * PAGE_SIZE, currentPage * PAGE_SIZE + PAGE_SIZE),
    [currentPage, runs],
  )
  const pagedReviewRuns = useMemo(
    () => reviewRuns.slice(currentPage * PAGE_SIZE, currentPage * PAGE_SIZE + PAGE_SIZE),
    [currentPage, reviewRuns],
  )
  const pagedSources = useMemo(
    () => sources.slice(currentPage * PAGE_SIZE, currentPage * PAGE_SIZE + PAGE_SIZE),
    [currentPage, sources],
  )
  const pagedTreeNodes = useMemo(
    () => treeNodes.slice(currentPage * PAGE_SIZE, currentPage * PAGE_SIZE + PAGE_SIZE),
    [currentPage, treeNodes],
  )
  const pageStart = totalCount ? currentPage * PAGE_SIZE + 1 : 0
  const pageEnd = totalCount ? Math.min(totalCount, (currentPage + 1) * PAGE_SIZE) : 0

  return (
    <div className="panel queue-panel">
      <div className="panel-header">
        <div>
          <p className="eyebrow">{queueEyebrow(activeView)}</p>
          <h3>{queueHeading(activeView, issueQueue, signalQueue, runs, reviewRuns, sources, activity, treePath)}</h3>
        </div>
      </div>

      {activeView === 'issues' || activeView === 'drift' ? (
        <>
          <details className="queue-tools">
            <summary>Queue tools</summary>
            <QueuePresetStrip
              presets={presets}
              selectedPresetId={activePresetId}
              onSelect={onSelectPreset}
              onClear={onClearPresetAndViews}
            />
            <SavedViewStrip
              views={savedViews}
              selectedViewId={activeSavedViewId}
              onSelect={onSelectSavedView}
              onClear={onClearPresetAndViews}
            />
          </details>
          <IssueQueueControls
            filters={issueFilters}
            labelsDraft={issueLabelFilterDraft}
            savedViewName={savedViewName}
            selectedViewName={selectedSavedViewName}
            mode={activeView}
            onFiltersChange={onFiltersChange}
            onLabelsDraftChange={onIssueLabelsDraftChange}
            onSavedViewNameChange={onSavedViewNameChange}
            onSaveView={onSaveView}
            onUpdateView={onUpdateView}
            onDeleteView={onDeleteView}
            canUpdateView={Boolean(activeSavedViewId)}
            canDeleteView={Boolean(activeSavedViewId)}
          />
          <div className="queue-table-head">
            <span>ID</span>
            <span>Severity</span>
            <span>Quality</span>
            <span>Status</span>
            <span>{activeView === 'drift' ? 'Drift' : 'Doc / Code'}</span>
            <span>Title</span>
          </div>
          <div className="list-pane">
            {pagedIssues.map((issue) => (
              <button
                key={issue.bug_id}
                type="button"
                className={`issue-table-row ${selectedIssueId === issue.bug_id ? 'issue-table-row-active' : ''}`}
                onClick={() => onSelectIssue(issue.bug_id)}
              >
                <strong className="issue-row-id">{issue.bug_id}</strong>
                <span className={`pill pill-${issue.severity.toLowerCase()}`}>{issue.severity}</span>
                <span className="issue-row-quality">
                  {issueQualityById[issue.bug_id] ? `${Math.round(issueQualityById[issue.bug_id].overall)}%` : 'n/a'}
                </span>
                <span className={`pill pill-${issue.issue_status}`}>{issue.issue_status}</span>
                <span className="issue-row-status">
                  {activeView === 'drift'
                    ? `${issue.drift_flags.length} drift`
                    : issue.review_ready_count > 0
                      ? `${issue.review_ready_count} review-ready`
                      : `${issue.doc_status} / ${issue.code_status}`}
                </span>
                <span className="issue-row-title">{issue.title}</span>
              </button>
            ))}
          </div>
        </>
      ) : null}

      {activeView === 'review' ? (
        <div className="list-pane">
          {pagedReviewRuns.length ? (
            pagedReviewRuns.map((run) => (
              <button
                key={run.run_id}
                type="button"
                className={`list-row ${selectedRunId === run.run_id ? 'list-row-active' : ''}`}
                onClick={() => onSelectRun(run.run_id)}
              >
                <div className="row-title">
                  <strong>{run.issue_id}</strong>
                  <span>{run.summary?.text_excerpt ?? run.title}</span>
                </div>
                <div className="row-meta">
                  <StatusPill tone={run.runtime}>{run.runtime}</StatusPill>
                  <StatusPill tone={run.status}>{run.status}</StatusPill>
                  <span>{run.model}</span>
                </div>
              </button>
            ))
          ) : (
            <p className="subtle">No review candidates are waiting right now.</p>
          )}
        </div>
      ) : null}

      {activeView === 'signals' ? (
        <>
          <section className="detail-section queue-controls">
            <label className="sr-only" htmlFor="signal-query">
              Search discovery queue
            </label>
            <input
              id="signal-query"
              name="signal-query"
              className="text-input"
              placeholder="Search discovery queue"
              value={signalQuery}
              onChange={(event) => onSignalQueryChange(event.target.value)}
            />
          </section>
          <div className="list-pane">
            {pagedSignals.map((signal) => (
              <button
                key={signal.signal_id}
                type="button"
                className={`list-row ${selectedSignalId === signal.signal_id ? 'list-row-active' : ''}`}
                onClick={() => onSelectSignal(signal.signal_id)}
              >
                <div className="row-title">
                  <strong>{signal.kind}</strong>
                  <span>{signal.summary}</span>
                </div>
                <div className="row-meta">
                  <StatusPill tone={signal.severity.toLowerCase()}>{signal.severity}</StatusPill>
                  <span className="path-chip">
                    {signal.file_path}:{signal.line}
                  </span>
                </div>
              </button>
            ))}
          </div>
        </>
      ) : null}

      {activeView === 'runs' ? (
        <div className="list-pane">
          {pagedRuns.map((run) => (
            <button
              key={run.run_id}
              type="button"
              className={`list-row ${selectedRunId === run.run_id ? 'list-row-active' : ''}`}
              onClick={() => onSelectRun(run.run_id)}
            >
              <div className="row-title">
                <strong>{run.runtime}</strong>
                <span>{run.issue_id}</span>
              </div>
              <div className="row-meta">
                <StatusPill tone={run.status}>{run.status}</StatusPill>
                <span>{run.model}</span>
                <span>{runMetricsById[run.run_id] ? `$${runMetricsById[run.run_id].estimated_cost.toFixed(4)}` : 'cost n/a'}</span>
              </div>
            </button>
          ))}
          {costSummary ? (
            <div className="evidence-row queue-inline-summary">
              <span>Workspace total</span>
              <small>
                ${costSummary.total_estimated_cost.toFixed(4)} · {costSummary.total_runs} runs ·{' '}
                {costSummary.total_input_tokens + costSummary.total_output_tokens} tokens
              </small>
            </div>
          ) : null}
        </div>
      ) : null}

      {activeView === 'activity' ? (
        <WorkspaceActivityPanel
          activities={activity}
          title="Workspace activity"
          eyebrow="History feed"
          query={activityQuery}
          actionFilter={activityActionFilter}
          entityTypeFilter={activityEntityTypeFilter}
          actorKindFilter={activityActorKindFilter}
          selectedActivityId={selectedActivityId}
          onQueryChange={onActivityQueryChange}
          onActionFilterChange={onActivityActionFilterChange}
          onEntityTypeFilterChange={onActivityEntityTypeFilterChange}
          onActorKindFilterChange={onActivityActorKindFilterChange}
          onSelectActivity={(entry) => onSelectActivity(entry.activity_id)}
        />
      ) : null}

      {activeView === 'sources' ? (
        <div className="list-pane">
          {pagedSources.map((source) => (
            <button
              key={source.source_id}
              type="button"
              className={`list-row ${selectedSourceId === source.source_id ? 'list-row-active' : ''}`}
              onClick={() => onSelectSource(source.source_id)}
            >
              <div className="row-title">
                <strong>{source.kind}</strong>
                <span>{source.label}</span>
              </div>
              <div className="row-meta">
                <span>{source.record_count} records</span>
                <span>{source.modified_at ? formatDate(source.modified_at) : 'runtime feed'}</span>
              </div>
            </button>
          ))}
        </div>
      ) : null}

      {activeView === 'tree' ? (
        <div className="list-pane">
          {treePath ? (
            <button type="button" className="back-button" onClick={() => onNavigateTree(treePath.split('/').slice(0, -1).join('/'))}>
              Up
            </button>
          ) : null}
          {pagedTreeNodes.map((node) => (
            <button
              key={node.path}
              type="button"
              className="list-row"
              onClick={() => {
                if (node.node_type === 'directory') onNavigateTree(node.path)
              }}
            >
              <div className="row-title">
                <strong>{node.node_type === 'directory' ? 'dir' : 'file'}</strong>
                <span>{node.path}</span>
              </div>
              <div className="row-meta">
                <span>{node.node_type}</span>
                {node.size_bytes ? <span>{node.size_bytes} B</span> : null}
              </div>
            </button>
          ))}
        </div>
      ) : null}

      {['issues', 'review', 'drift', 'signals', 'runs', 'sources', 'tree'].includes(activeView) && totalCount > PAGE_SIZE ? (
        <div className="queue-pagination">
          <span className="subtle">
            Showing {pageStart}-{pageEnd} of {totalCount}
          </span>
          <div className="toolbar-row">
            <button type="button" className="ghost-button" onClick={() => updatePage((current) => Math.max(0, current - 1))} disabled={currentPage === 0}>
              Prev
            </button>
            <span className="subtle">
              Page {currentPage + 1} / {pageCount}
            </span>
            <button
              type="button"
              className="ghost-button"
              onClick={() => updatePage((current) => Math.min(pageCount - 1, current + 1))}
              disabled={currentPage >= pageCount - 1}
            >
              Next
            </button>
          </div>
        </div>
      ) : null}
    </div>
  )
}
