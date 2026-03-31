import type { SavedIssueView } from '../lib/types'

type Props = {
  views: SavedIssueView[]
  selectedViewId: string | null
  onSelect: (view: SavedIssueView) => void
  onClear: () => void
}

export function SavedViewStrip({ views, selectedViewId, onSelect, onClear }: Props) {
  return (
    <section className="detail-section">
      <div className="toolbar-row saved-view-header">
        <div>
          <p className="eyebrow">Saved queues</p>
          <h4>Persistent triage views</h4>
        </div>
        <button className="ghost-button" onClick={onClear}>
          Clear
        </button>
      </div>
      <div className="saved-view-strip">
        {views.length ? (
          views.map((view) => (
            <button
              key={view.view_id}
              className={`saved-view-chip ${selectedViewId === view.view_id ? 'saved-view-chip-active' : ''}`}
              onClick={() => onSelect(view)}
            >
              <strong>{view.name}</strong>
              <small>
                {view.severities.length ? view.severities.join(', ') : 'All'}
                {view.needs_followup ? ' · follow-up' : ''}
                {view.drift_only ? ' · drift' : ''}
              </small>
            </button>
          ))
        ) : (
          <span className="subtle">No saved views yet.</span>
        )}
      </div>
    </section>
  )
}
