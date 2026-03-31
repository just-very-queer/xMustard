import type { WorktreeStatus } from '../lib/types'
import { formatDate } from './TrackerPrimitives'

type Props = {
  workspaceName?: string | null
  workspacePath?: string | null
  latestScanAt?: string | null
  worktree?: WorktreeStatus | null
  backendHealthy?: boolean | null
  canExport: boolean
  canToggleExecution?: boolean
  executionOpen?: boolean
  onExport: () => void
  onToggleExecution?: () => void
}

export function AppTopbar({
  workspaceName,
  workspacePath,
  latestScanAt,
  worktree,
  backendHealthy,
  canExport,
  canToggleExecution,
  executionOpen,
  onExport,
  onToggleExecution,
}: Props) {
  return (
    <header className="topbar">
      <div>
        <p className="eyebrow">Bug operations</p>
        <h2>{workspaceName ?? 'Load a workspace'}</h2>
        <p className="subtle">{workspacePath ?? 'Select a repository to start triage.'}</p>
      </div>
      <div className="topbar-actions">
        {backendHealthy !== null ? (
          <span className={`status-chip ${backendHealthy ? 'status-chip-ok' : 'status-chip-bad'}`}>
            API {backendHealthy ? 'up' : 'down'}
          </span>
        ) : null}
        {worktree?.is_git_repo ? (
          <span className="subtle">
            {worktree.branch ?? 'detached'} · {worktree.dirty_files} dirty
            {worktree.ahead || worktree.behind ? ` · +${worktree.ahead}/-${worktree.behind}` : ''}
          </span>
        ) : null}
        {latestScanAt ? <span className="subtle">Last scan {formatDate(latestScanAt)}</span> : null}
        {canToggleExecution ? (
          <button className="ghost-button" onClick={onToggleExecution}>
            {executionOpen ? 'Hide console' : 'Agent console'}
          </button>
        ) : null}
        <button onClick={onExport} disabled={!canExport}>
          Copy export JSON
        </button>
      </div>
    </header>
  )
}
