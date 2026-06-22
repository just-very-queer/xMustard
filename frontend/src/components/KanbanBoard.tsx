// Linear-like kanban board for runtime: issues grouped by status into columns.
// Decoupled from the full issue type — it only needs the card fields.

export type KanbanIssue = {
  bug_id: string
  title: string
  severity: string
  issue_status: string
}

// Preferred column order; any other statuses are appended after these.
const STATUS_ORDER = ['open', 'triaged', 'in_progress', 'blocked', 'review', 'verified', 'closed']

function orderedStatuses(issues: KanbanIssue[]): string[] {
  const present = new Set(issues.map((i) => i.issue_status || 'unset'))
  const ordered = STATUS_ORDER.filter((s) => present.has(s))
  for (const s of present) {
    if (!ordered.includes(s)) ordered.push(s)
  }
  return ordered.length > 0 ? ordered : ['unset']
}

export function KanbanBoard({
  issues,
  onSelect,
}: {
  issues: KanbanIssue[]
  onSelect?: (bugId: string) => void
}) {
  const statuses = orderedStatuses(issues)
  const byStatus = (status: string) =>
    issues.filter((i) => (i.issue_status || 'unset') === status)

  return (
    <div className="kanban">
      <div className="kanban-board">
        {statuses.map((status) => {
          const cards = byStatus(status)
          return (
            <div className="kanban-column" key={status}>
              <header className="kanban-column-header">
                <span className="kanban-column-title">{status}</span>
                <span className="kanban-column-count">{cards.length}</span>
              </header>
              <div className="kanban-column-body">
                {cards.map((issue) => (
                  <button
                    key={issue.bug_id}
                    className={`kanban-card severity-${(issue.severity || 'unset').toLowerCase()}`}
                    onClick={() => onSelect?.(issue.bug_id)}
                  >
                    <span className="kanban-card-severity">{issue.severity || '—'}</span>
                    <span className="kanban-card-title">{issue.title || issue.bug_id}</span>
                    <span className="kanban-card-id">{issue.bug_id}</span>
                  </button>
                ))}
                {cards.length === 0 && <div className="kanban-empty">—</div>}
              </div>
            </div>
          )
        })}
      </div>
    </div>
  )
}
