import type { IssueQueueFilters } from '../lib/types'

const SEVERITIES = ['P0', 'P1', 'P2', 'P3']
const STATUSES = ['open', 'triaged', 'investigating', 'in_progress', 'verification', 'resolved', 'partial']
const SOURCES = ['ledger', 'verdict', 'manual', 'signal']

type Props = {
  filters: IssueQueueFilters
  labelsDraft: string
  savedViewName: string
  selectedViewName?: string
  mode: 'issues' | 'review' | 'drift'
  onFiltersChange: (next: IssueQueueFilters) => void
  onLabelsDraftChange: (value: string) => void
  onSavedViewNameChange: (value: string) => void
  onSaveView: () => void
  onUpdateView: () => void
  onDeleteView: () => void
  canUpdateView: boolean
  canDeleteView: boolean
}

function toggleValue(values: string[], target: string) {
  return values.includes(target) ? values.filter((value) => value !== target) : [...values, target]
}

export function IssueQueueControls({
  filters,
  labelsDraft,
  savedViewName,
  selectedViewName,
  mode,
  onFiltersChange,
  onLabelsDraftChange,
  onSavedViewNameChange,
  onSaveView,
  onUpdateView,
  onDeleteView,
  canUpdateView,
  canDeleteView,
}: Props) {
  return (
    <section className="detail-section queue-controls">
      <div className="toolbar-row queue-controls-top">
        <label className="sr-only" htmlFor="issue-query">
          Issue query
        </label>
        <input
          id="issue-query"
          name="issue-query"
          className="text-input"
          placeholder={mode === 'drift' ? 'Search drift queue' : mode === 'review' ? 'Search review queue' : 'Search issue queue'}
          value={filters.query}
          onChange={(event) => onFiltersChange({ ...filters, query: event.target.value })}
        />
        <label className="sr-only" htmlFor="issue-labels">
          Issue labels
        </label>
        <input
          id="issue-labels"
          name="issue-labels"
          className="text-input"
          placeholder="Labels: api, preview, scope"
          value={labelsDraft}
          onChange={(event) => onLabelsDraftChange(event.target.value)}
        />
      </div>
      <details className="compact-filters">
        <summary>Refine queue</summary>
        <div className="filter-group">
          <span className="filter-label">Severity</span>
          <div className="tag-row">
            {SEVERITIES.map((severity) => (
              <button
                key={severity}
                className={`filter-chip ${filters.severities.includes(severity) ? 'filter-chip-active' : ''}`}
                onClick={() => onFiltersChange({ ...filters, severities: toggleValue(filters.severities, severity) })}
              >
                {severity}
              </button>
            ))}
          </div>
        </div>

        <div className="filter-group">
          <span className="filter-label">Status</span>
          <div className="tag-row">
            {STATUSES.map((status) => (
              <button
                key={status}
                className={`filter-chip ${filters.statuses.includes(status) ? 'filter-chip-active' : ''}`}
                onClick={() => onFiltersChange({ ...filters, statuses: toggleValue(filters.statuses, status) })}
              >
                {status}
              </button>
            ))}
          </div>
        </div>

        <div className="filter-group">
          <span className="filter-label">Source</span>
          <div className="tag-row">
            {SOURCES.map((source) => (
              <button
                key={source}
                className={`filter-chip ${filters.sources.includes(source) ? 'filter-chip-active' : ''}`}
                onClick={() => onFiltersChange({ ...filters, sources: toggleValue(filters.sources, source) })}
              >
                {source}
              </button>
            ))}
          </div>
        </div>

        <div className="toolbar-row">
          <label className="toggle-row" htmlFor="followup-only">
            <input
              id="followup-only"
              name="followup-only"
              type="checkbox"
              checked={filters.needs_followup === true}
              onChange={(event) =>
                onFiltersChange({
                  ...filters,
                  needs_followup: event.target.checked ? true : null,
                })
              }
            />
            <span>Follow-up only</span>
          </label>
          <label className="toggle-row" htmlFor="drift-only">
            <input
              id="drift-only"
              name="drift-only"
              type="checkbox"
              checked={filters.drift_only || mode === 'drift'}
              disabled={mode === 'drift'}
              onChange={(event) => onFiltersChange({ ...filters, drift_only: event.target.checked })}
            />
            <span>Drift only</span>
          </label>
          <label className="toggle-row" htmlFor="review-ready-only">
            <input
              id="review-ready-only"
              name="review-ready-only"
              type="checkbox"
              checked={filters.review_ready_only}
              onChange={(event) => onFiltersChange({ ...filters, review_ready_only: event.target.checked })}
            />
            <span>Review ready</span>
          </label>
        </div>
      </details>

      <div className="toolbar-row queue-controls-save">
        <label className="sr-only" htmlFor="saved-view-name">
          Saved view name
        </label>
        <input
          id="saved-view-name"
          name="saved-view-name"
          className="text-input"
          placeholder={selectedViewName ? `Selected: ${selectedViewName}` : 'Save current view as...'}
          value={savedViewName}
          onChange={(event) => onSavedViewNameChange(event.target.value)}
        />
        <button onClick={onSaveView} disabled={!savedViewName.trim()}>
          Save view
        </button>
        <button onClick={onUpdateView} disabled={!canUpdateView}>
          Update
        </button>
        <button onClick={onDeleteView} disabled={!canDeleteView}>
          Delete
        </button>
      </div>
    </section>
  )
}
