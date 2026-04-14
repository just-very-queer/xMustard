import { formatDate } from '../lib/format'
import type { RepoGuidanceRecord, ViewMode, WorkspaceRecord } from '../lib/types'

type Props = {
  workspacePath: string
  workspaces: WorkspaceRecord[]
  selectedWorkspaceId?: string | null
  activeView: ViewMode
  loading: boolean
  workspaceGuidance: RepoGuidanceRecord[]
  verificationProfileCount: number
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
  workspaceGuidance,
  verificationProfileCount,
  onWorkspacePathChange,
  onLoadWorkspace,
  onScanWorkspace,
  onSelectWorkspace,
  onChangeView,
  canScan,
}: Props) {
  const alwaysOnCount = workspaceGuidance.filter((item) => item.always_on).length
  const instructionCount = workspaceGuidance.filter((item) =>
    item.kind === 'agent_instructions' || item.kind === 'conventions',
  ).length

  return (
    <aside className="left-rail">
      <div className="sidebar-intro">
        <p className="eyebrow">xMustard</p>
        <h1>Bug operations</h1>
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

      {selectedWorkspaceId ? (
        <div className="sidebar-panel guidance-sidebar-panel">
          <div className="panel-header">
            <div>
              <p className="eyebrow">Repo guidance</p>
              <h4>{workspaceGuidance.length ? 'Guided workspace' : 'Needs setup'}</h4>
            </div>
            <span className={`status-chip ${workspaceGuidance.length ? 'status-chip-ok' : 'status-chip-warn'}`}>
              {workspaceGuidance.length ? `${workspaceGuidance.length} files` : '0 files'}
            </span>
          </div>
          {workspaceGuidance.length ? (
            <>
              <p className="subtle">The tracker found repo instructions and will attach them to runs and issue context.</p>
              <div className="tag-row">
                <span className="tag">Always on: {alwaysOnCount}</span>
                <span className="tag">Instructions: {instructionCount}</span>
                <span className="tag">Verification: {verificationProfileCount}</span>
              </div>
              <div className="guidance-sidebar-list">
                {workspaceGuidance.slice(0, 3).map((item) => (
                  <div key={item.guidance_id} className="guidance-sidebar-item">
                    <strong>{item.title}</strong>
                    <small>{item.path}</small>
                  </div>
                ))}
              </div>
            </>
          ) : (
            <>
              <p className="subtle">Add one lightweight repo instruction file so runs stop relying on generic defaults.</p>
              <div className="tag-row">
                <span className="tag">Recommended: `AGENTS.md`</span>
                <span className="tag">Optional: `CONVENTIONS.md`</span>
              </div>
              <button className="ghost-button" type="button" onClick={() => onChangeView('tree')}>
                Open repo tree
              </button>
            </>
          )}
        </div>
      ) : null}
    </aside>
  )
}
