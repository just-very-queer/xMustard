import { formatDate } from '../lib/format'
import type { RepoGuidanceHealth, RepoGuidanceRecord, ViewMode, WorkspaceRecord } from '../lib/types'

type Props = {
  workspacePath: string
  workspaces: WorkspaceRecord[]
  selectedWorkspaceId?: string | null
  activeView: ViewMode
  loading: boolean
  workspaceGuidance: RepoGuidanceRecord[]
  guidanceHealth: RepoGuidanceHealth | null
  verificationProfileCount: number
  onWorkspacePathChange: (value: string) => void
  onLoadWorkspace: () => void
  onScanWorkspace: () => void
  onGenerateGuidanceStarter: (templateId: 'agents' | 'openhands_repo' | 'conventions') => void
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
  guidanceHealth,
  verificationProfileCount,
  onWorkspacePathChange,
  onLoadWorkspace,
  onScanWorkspace,
  onGenerateGuidanceStarter,
  onSelectWorkspace,
  onChangeView,
  canScan,
}: Props) {
  const alwaysOnCount = workspaceGuidance.filter((item) => item.always_on).length
  const instructionCount = workspaceGuidance.filter((item) =>
    item.kind === 'agent_instructions' || item.kind === 'conventions',
  ).length
  const starterTemplates = guidanceHealth?.starters ?? []
  const guidanceTone =
    guidanceHealth?.status === 'healthy' ? 'status-chip-ok' : guidanceHealth?.status === 'missing' ? 'status-chip-bad' : 'status-chip-warn'

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
              <h4>{guidanceHealth?.status === 'healthy' ? 'Guided workspace' : workspaceGuidance.length ? 'Needs attention' : 'Needs setup'}</h4>
            </div>
            <span className={`status-chip ${guidanceTone}`}>
              {guidanceHealth ? guidanceHealth.status : workspaceGuidance.length ? `${workspaceGuidance.length} files` : '0 files'}
            </span>
          </div>
          {workspaceGuidance.length ? (
            <>
              <p className="subtle">{guidanceHealth?.summary ?? 'The tracker found repo instructions and will attach them to runs and issue context.'}</p>
              <div className="tag-row">
                <span className="tag">Always on: {alwaysOnCount}</span>
                <span className="tag">Instructions: {instructionCount}</span>
                <span className="tag">Verification: {verificationProfileCount}</span>
                {guidanceHealth?.missing_files.length ? <span className="tag">Missing: {guidanceHealth.missing_files.length}</span> : null}
                {guidanceHealth?.stale_files.length ? <span className="tag">Stale: {guidanceHealth.stale_files.length}</span> : null}
              </div>
              <div className="guidance-sidebar-list">
                {workspaceGuidance.slice(0, 3).map((item) => (
                  <div key={item.guidance_id} className="guidance-sidebar-item">
                    <strong>{item.title}</strong>
                    <small>{item.path}</small>
                  </div>
                ))}
              </div>
              {starterTemplates.length ? (
                <div className="sidebar-actions">
                  {starterTemplates.filter((item) => !item.exists || item.stale).slice(0, 2).map((item) => (
                    <button key={item.template_id} className="ghost-button" type="button" onClick={() => onGenerateGuidanceStarter(item.template_id)}>
                      {item.exists ? `Refresh ${item.title}` : `Add ${item.title}`}
                    </button>
                  ))}
                </div>
              ) : null}
            </>
          ) : (
            <>
              <p className="subtle">{guidanceHealth?.summary ?? 'Add one lightweight repo instruction file so runs stop relying on generic defaults.'}</p>
              <div className="tag-row">
                <span className="tag">Recommended: `AGENTS.md`</span>
                <span className="tag">Optional: `CONVENTIONS.md`</span>
              </div>
              <div className="sidebar-actions">
                <button type="button" onClick={() => onGenerateGuidanceStarter('agents')}>
                  Add AGENTS.md
                </button>
                <button className="ghost-button" type="button" onClick={() => onGenerateGuidanceStarter('openhands_repo')}>
                  Add repo microagent
                </button>
              </div>
            </>
          )}
        </div>
      ) : null}
    </aside>
  )
}
