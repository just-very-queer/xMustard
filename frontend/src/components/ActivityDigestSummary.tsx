import type { CSSProperties } from 'react'

import { StatusPill, SummaryCard } from './TrackerPrimitives'

export type ActivityDigestItem = {
  label: string
  count: number
  secondaryText?: string | null
  tone?: string
}

export type ActivityDigestOverview = {
  totalEvents: number
  uniqueActors?: number
  uniqueActions?: number
  topActors: ActivityDigestItem[]
  topActions: ActivityDigestItem[]
  generatedAtLabel?: string | null
}

type Props = {
  overview: ActivityDigestOverview
  title?: string
  eyebrow?: string
  emptyActorsMessage?: string
  emptyActionsMessage?: string
}

const summaryGridStyle: CSSProperties = {
  display: 'grid',
  gap: '0.75rem',
  gridTemplateColumns: 'repeat(auto-fit, minmax(140px, 1fr))',
}

function DigestList({
  heading,
  items,
  emptyMessage,
}: {
  heading: string
  items: ActivityDigestItem[]
  emptyMessage: string
}) {
  return (
    <section className="detail-section">
      <h4>{heading}</h4>
      {items.length ? (
        items.map((item) => (
          <div key={`${heading}-${item.label}`} className="evidence-row">
            <div className="detail-copy">
              <span>{item.label}</span>
              {item.secondaryText ? <small className="subtle">{item.secondaryText}</small> : null}
            </div>
            <div className="row-meta">
              {item.tone ? <StatusPill tone={item.tone}>{item.tone}</StatusPill> : null}
              <strong>{item.count}</strong>
            </div>
          </div>
        ))
      ) : (
        <p className="subtle">{emptyMessage}</p>
      )}
    </section>
  )
}

export function ActivityDigestSummary({
  overview,
  title = 'Activity digest',
  eyebrow = 'Activity summary',
  emptyActorsMessage = 'No actor activity recorded.',
  emptyActionsMessage = 'No action activity recorded.',
}: Props) {
  return (
    <section className="panel">
      <div className="panel-header">
        <div>
          <p className="eyebrow">{eyebrow}</p>
          <h3>{title}</h3>
        </div>
        {overview.generatedAtLabel ? <span className="subtle">{overview.generatedAtLabel}</span> : null}
      </div>

      <div style={summaryGridStyle}>
        <SummaryCard label="Total events" value={overview.totalEvents} accent="amber" />
        <SummaryCard label="Actors" value={overview.uniqueActors ?? overview.topActors.length} accent="blue" />
        <SummaryCard label="Actions" value={overview.uniqueActions ?? overview.topActions.length} accent="sand" />
      </div>

      <div className="detail-section">
        <DigestList heading="Top actors" items={overview.topActors} emptyMessage={emptyActorsMessage} />
        <DigestList heading="Top actions" items={overview.topActions} emptyMessage={emptyActionsMessage} />
      </div>
    </section>
  )
}
