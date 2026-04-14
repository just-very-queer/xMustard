import type { CSSProperties } from 'react'

import { formatDate } from '../lib/format'
import type { ActivityRecord } from '../lib/types'

type EntityTypeFilter = ActivityRecord['entity_type'] | 'all'
type ActorKindFilter = ActivityRecord['actor']['kind'] | 'all'

type Props = {
  activities: ActivityRecord[]
  title?: string
  eyebrow?: string
  emptyMessage?: string
  query?: string
  actionFilter?: string
  entityTypeFilter?: EntityTypeFilter
  actorKindFilter?: ActorKindFilter
  limit?: number
  selectedActivityId?: string | null
  onQueryChange?: (value: string) => void
  onActionFilterChange?: (value: string) => void
  onEntityTypeFilterChange?: (value: EntityTypeFilter) => void
  onActorKindFilterChange?: (value: ActorKindFilter) => void
  onSelectActivity?: (activity: ActivityRecord) => void
}

const filterGridStyle: CSSProperties = {
  display: 'grid',
  gap: '0.75rem',
  gridTemplateColumns: 'repeat(auto-fit, minmax(160px, 1fr))',
}

const searchFieldStyle: CSSProperties = {
  gridColumn: '1 / -1',
}

const selectStyle: CSSProperties = {
  appearance: 'none',
}

const cardButtonStyle: CSSProperties = {
  width: '100%',
  color: 'inherit',
  textAlign: 'left',
  cursor: 'pointer',
}

const cardMetaStyle: CSSProperties = {
  display: 'flex',
  alignItems: 'center',
  justifyContent: 'space-between',
  gap: '0.75rem',
  flexWrap: 'wrap',
}

const cardSummaryStyle: CSSProperties = {
  color: '#d7d1c6',
  fontSize: '0.92rem',
  lineHeight: 1.45,
}

function normalize(value?: string | null) {
  return value?.trim().toLowerCase() ?? ''
}

function matchesQuery(activity: ActivityRecord, query: string) {
  if (!query) return true
  const haystack = [
    activity.summary,
    activity.action,
    activity.entity_type,
    activity.entity_id,
    activity.issue_id,
    activity.run_id,
    activity.actor.kind,
    activity.actor.name,
    activity.actor.runtime,
    activity.actor.model,
  ]
    .filter(Boolean)
    .join(' ')
    .toLowerCase()
  return haystack.includes(query)
}

function detailCount(activity: ActivityRecord) {
  return Object.keys(activity.details ?? {}).length
}

function activityTone(activity: ActivityRecord) {
  const action = activity.action.toLowerCase()
  if (action.includes('fail') || action.includes('error')) return 'red'
  if (action.includes('fix') || action.includes('complete') || action.includes('resolved')) return 'fixed'
  if (activity.actor.runtime === 'opencode') return 'opencode'
  if (activity.actor.runtime === 'codex') return 'codex'
  return 'sand'
}

export function WorkspaceActivityPanel({
  activities,
  title = 'Workspace activity',
  eyebrow = 'Activity feed',
  emptyMessage = 'No workspace activity matches the current filters.',
  query = '',
  actionFilter = 'all',
  entityTypeFilter = 'all',
  actorKindFilter = 'all',
  limit,
  selectedActivityId,
  onQueryChange,
  onActionFilterChange,
  onEntityTypeFilterChange,
  onActorKindFilterChange,
  onSelectActivity,
}: Props) {
  const normalizedQuery = normalize(query)
  const actionOptions = Array.from(new Set(activities.map((activity) => activity.action))).sort((left, right) =>
    left.localeCompare(right),
  )

  const filteredActivities = activities
    .filter((activity) => (actionFilter === 'all' ? true : activity.action === actionFilter))
    .filter((activity) => (entityTypeFilter === 'all' ? true : activity.entity_type === entityTypeFilter))
    .filter((activity) => (actorKindFilter === 'all' ? true : activity.actor.kind === actorKindFilter))
    .filter((activity) => matchesQuery(activity, normalizedQuery))

  const visibleActivities = typeof limit === 'number' ? filteredActivities.slice(0, limit) : filteredActivities

  return (
    <section className="panel">
      <div className="panel-header">
        <div>
          <p className="eyebrow">{eyebrow}</p>
          <h3>{title}</h3>
        </div>
        <p className="subtle">
          {visibleActivities.length} of {activities.length}
        </p>
      </div>

      <div className="detail-section queue-controls">
        <div style={filterGridStyle}>
          <label className="sr-only" htmlFor="activity-query">
            Search activity
          </label>
          <input
            id="activity-query"
            name="activity-query"
            className="text-input"
            placeholder="Search summary, action, actor, issue, or run"
            value={query}
            onChange={(event) => onQueryChange?.(event.target.value)}
            style={searchFieldStyle}
          />

          <label className="detail-section" htmlFor="activity-action-filter">
            <span className="filter-label">Action</span>
            <select
              id="activity-action-filter"
              name="activity-action-filter"
              className="text-input"
              value={actionFilter}
              onChange={(event) => onActionFilterChange?.(event.target.value)}
              style={selectStyle}
            >
              <option value="all">All actions</option>
              {actionOptions.map((action) => (
                <option key={action} value={action}>
                  {action}
                </option>
              ))}
            </select>
          </label>

          <label className="detail-section" htmlFor="activity-entity-filter">
            <span className="filter-label">Entity</span>
            <select
              id="activity-entity-filter"
              name="activity-entity-filter"
              className="text-input"
              value={entityTypeFilter}
              onChange={(event) => onEntityTypeFilterChange?.(event.target.value as EntityTypeFilter)}
              style={selectStyle}
            >
              <option value="all">All entities</option>
              <option value="issue">Issue</option>
              <option value="run">Run</option>
              <option value="view">View</option>
              <option value="signal">Signal</option>
              <option value="workspace">Workspace</option>
              <option value="settings">Settings</option>
            </select>
          </label>

          <label className="detail-section" htmlFor="activity-actor-filter">
            <span className="filter-label">Actor</span>
            <select
              id="activity-actor-filter"
              name="activity-actor-filter"
              className="text-input"
              value={actorKindFilter}
              onChange={(event) => onActorKindFilterChange?.(event.target.value as ActorKindFilter)}
              style={selectStyle}
            >
              <option value="all">All actors</option>
              <option value="operator">Operator</option>
              <option value="agent">Agent</option>
              <option value="system">System</option>
            </select>
          </label>
        </div>
      </div>

      <div className="activity-list">
        {visibleActivities.length ? (
          visibleActivities.map((activity) => {
            const isSelected = selectedActivityId === activity.activity_id
            const content = (
              <>
                <div className="activity-entry-top">
                  <strong>{activity.summary}</strong>
                  <small>{formatDate(activity.created_at)}</small>
                </div>
                <div style={cardSummaryStyle}>
                  {activity.entity_type} · {activity.entity_id}
                </div>
                <div className="row-meta">
                  <span className={`pill pill-${activityTone(activity)}`}>{activity.action}</span>
                  <span className="tag">
                    {activity.actor.kind}: {activity.actor.name}
                  </span>
                  {activity.actor.runtime ? <span className="tag">{activity.actor.runtime}</span> : null}
                  {activity.actor.model ? <span className="tag">{activity.actor.model}</span> : null}
                  {activity.issue_id ? <span className="tag">{activity.issue_id}</span> : null}
                  {activity.run_id ? <span className="tag">{activity.run_id}</span> : null}
                </div>
                <div style={cardMetaStyle}>
                  <span className="subtle">activity {activity.activity_id}</span>
                  <span className="subtle">{detailCount(activity)} detail fields</span>
                </div>
              </>
            )

            if (onSelectActivity) {
              return (
                <button
                  key={activity.activity_id}
                  type="button"
                  className="activity-entry"
                  onClick={() => onSelectActivity(activity)}
                  style={{
                    ...cardButtonStyle,
                    borderColor: isSelected ? 'rgba(236, 224, 186, 0.22)' : undefined,
                    background: isSelected ? 'rgba(236, 224, 186, 0.12)' : undefined,
                  }}
                >
                  {content}
                </button>
              )
            }

            return (
              <article key={activity.activity_id} className="activity-entry">
                {content}
              </article>
            )
          })
        ) : (
          <p className="subtle">{emptyMessage}</p>
        )}
      </div>
    </section>
  )
}
