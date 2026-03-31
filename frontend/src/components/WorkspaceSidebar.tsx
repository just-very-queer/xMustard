import { formatDate } from './TrackerPrimitives'
import type { ViewMode, WorkspaceRecord } from '../lib/types'

type Props = {
  workspacePath: string
  workspaces: WorkspaceRecord[]
  selectedWorkspaceId?: string | null
  activeView: ViewMode
  loading: boolean
  onWorkspacePathChange: (value: string) => void
  onLoadWorkspace: () => void
  onScanWorkspace: () => void
  onSelectWorkspace: (rootPath: string) => void
  onChangeView: (view: ViewMode) => void
  canScan: boolean
}

export function WorkspaceSidebar({
  workspacePath,
  workspaces,
  selectedWorkspaceId,
  activeView,
  loading,
  onWorkspacePathChange,
  onLoadWorkspace,
  onScanWorkspace,
  onSelectWorkspace,
  onChangeView,
  canScan,
}: Props) {
  return (
    <aside className="left-rail">
      <div className="sidebar-intro">
        <p className="eyebrow">Co_Titan_Bug_Tracker</p>
        <h1>Bug tracker</h1>
        <p className="subtle">Load a repo tree, track bug history, and send work to agents.</p>
      </div>

      <nav className="nav-cluster">
        {[
          ['issues', 'Issues'],
          ['review', 'Review'],
          ['signals', 'Discovery'],
          ['runs', 'Runs'],
          ['activity', 'Activity'],
          ['sources', 'Sources'],
          ['drift', 'Drift'],
          ['tree', 'Tree'],
        ].map(([value, label]) => (
          <button
            key={value}
            className={`nav-button ${activeView === value ? 'nav-button-active' : ''}`}
            onClick={() => onChangeView(value as ViewMode)}
          >
            {label}
          </button>
        ))}
      </nav>

      <div className="sidebar-panel">
        <label className="field-label" htmlFor="workspace-path">
          Workspace tree
        </label>
        <input
          id="workspace-path"
          name="workspace-path"
          value={workspacePath}
          onChange={(event) => onWorkspacePathChange(event.target.value)}
          className="text-input"
        />
        <div className="sidebar-actions">
          <button onClick={onLoadWorkspace} disabled={loading}>
            Load tree
          </button>
          <button className="ghost-button" onClick={onScanWorkspace} disabled={!canScan || loading}>
            Scan
          </button>
        </div>
        <div className="workspace-list">
          {workspaces.map((workspace) => (
            <button
              key={workspace.workspace_id}
              className={`workspace-chip ${selectedWorkspaceId === workspace.workspace_id ? 'workspace-chip-active' : ''}`}
              onClick={() => onSelectWorkspace(workspace.root_path)}
            >
              <span className="workspace-chip-title">{workspace.name}</span>
              <small>{workspace.latest_scan_at ? formatDate(workspace.latest_scan_at) : 'unscanned'}</small>
            </button>
          ))}
        </div>
      </div>
    </aside>
  )
}
