import type { IssueRecord, LocalAgentCapabilities, RuntimeModel, RuntimeProbeResult } from '../lib/types'

type Props = {
  runtime: 'codex' | 'opencode'
  model: string
  runtimeModels: RuntimeModel[]
  capabilities: LocalAgentCapabilities | null
  selectedIssue: IssueRecord | null
  queryPrompt: string
  runtimeProbe: RuntimeProbeResult | null
  runtimeProbeLoading: boolean
  backendHealthy: boolean | null
  loading: boolean
  onRuntimeChange: (value: 'codex' | 'opencode') => void
  onModelChange: (value: string) => void
  onQueryPromptChange: (value: string) => void
  onProbeRuntime: () => void
  onQueryAgent: () => void
  onRunIssue: () => void
  onOpenRuns: () => void
  onToggleExecution: () => void
  executionOpen: boolean
}

export function AgentDock({
  runtime,
  model,
  runtimeModels,
  capabilities,
  selectedIssue,
  queryPrompt,
  runtimeProbe,
  runtimeProbeLoading,
  backendHealthy,
  loading,
  onRuntimeChange,
  onModelChange,
  onQueryPromptChange,
  onProbeRuntime,
  onQueryAgent,
  onRunIssue,
  onOpenRuns,
  onToggleExecution,
  executionOpen,
}: Props) {
  const runtimeEntry = capabilities?.runtimes.find((entry) => entry.runtime === runtime) ?? null
  const lastProbeOk = runtimeProbe?.runtime === runtime && runtimeProbe?.model === model ? runtimeProbe.ok : null

  return (
    <section className="agent-dock panel">
      <div className="agent-dock-header">
        <div>
          <p className="eyebrow">Agent access</p>
          <h3>Test, query, and run from the main surface</h3>
        </div>
        <div className="tag-row">
          <span className={`status-chip ${backendHealthy ? 'status-chip-ok' : backendHealthy === false ? 'status-chip-bad' : ''}`}>
            API {backendHealthy ? 'up' : backendHealthy === false ? 'down' : 'unknown'}
          </span>
          <span className={`status-chip ${runtimeEntry?.available ? 'status-chip-ok' : 'status-chip-bad'}`}>
            {runtimeEntry?.available ? `${runtime} ready` : `${runtime} unavailable`}
          </span>
          {lastProbeOk !== null ? (
            <span className={`status-chip ${lastProbeOk ? 'status-chip-ok' : 'status-chip-bad'}`}>
              Probe {lastProbeOk ? 'passed' : 'failed'}
            </span>
          ) : null}
        </div>
      </div>

      <div className="agent-dock-grid">
        <label className="detail-section field-stack" htmlFor="agent-runtime">
          <span className="filter-label">Runtime</span>
          <select
            id="agent-runtime"
            className="text-input"
            value={runtime}
            onChange={(event) => onRuntimeChange(event.target.value as 'codex' | 'opencode')}
          >
            {(capabilities?.runtimes ?? []).map((entry) => (
              <option key={entry.runtime} value={entry.runtime} disabled={!entry.available}>
                {entry.runtime}
              </option>
            ))}
          </select>
        </label>
        <label className="detail-section field-stack" htmlFor="agent-model">
          <span className="filter-label">Model</span>
          <select id="agent-model" className="text-input" value={model} onChange={(event) => onModelChange(event.target.value)}>
            {runtimeModels.map((entry) => (
              <option key={entry.id} value={entry.id}>
                {entry.id}
              </option>
            ))}
          </select>
        </label>
        <div className="agent-dock-actions">
          <button type="button" className="ghost-button" onClick={onProbeRuntime} disabled={!model || runtimeProbeLoading}>
            {runtimeProbeLoading ? 'Testing…' : 'Test runtime'}
          </button>
          <button type="button" className="ghost-button" onClick={onOpenRuns}>
            Runs
          </button>
          <button type="button" className="ghost-button" onClick={onToggleExecution}>
            {executionOpen ? 'Hide console' : 'Open console'}
          </button>
        </div>
      </div>

      <div className="agent-dock-query">
        <label className="detail-section field-stack" htmlFor="agent-query">
          <span className="filter-label">Workspace query</span>
          <textarea
            id="agent-query"
            className="text-area"
            rows={3}
            value={queryPrompt}
            onChange={(event) => onQueryPromptChange(event.target.value)}
            placeholder="Ask for a repo scan, a bug sweep, or a concise diagnosis."
          />
        </label>
        <div className="agent-dock-actions">
          <button type="button" onClick={onQueryAgent} disabled={!model || !queryPrompt.trim() || loading}>
            Query workspace
          </button>
          <button type="button" onClick={onRunIssue} disabled={!selectedIssue || !model || loading}>
            Run selected issue
          </button>
        </div>
        <p className="subtle">
          Agents access this system through workspace runs. Use query for repo-wide inspection, or run the selected issue with the full context packet.
        </p>
      </div>

      {runtimeProbe ? (
        <div className="agent-dock-probe">
          <div className="row-meta">
            <span className={`pill pill-${runtimeProbe.ok ? 'fixed' : 'red'}`}>{runtimeProbe.ok ? 'probe ok' : 'probe failed'}</span>
            <span className="tag">{runtimeProbe.runtime}</span>
            <span className="tag">{runtimeProbe.model}</span>
            <span className="tag">{runtimeProbe.duration_ms} ms</span>
          </div>
          <p className="subtle">
            {runtimeProbe.binary_path ?? 'binary unresolved'}
            {runtimeProbe.exit_code !== null && runtimeProbe.exit_code !== undefined ? ` · exit ${runtimeProbe.exit_code}` : ''}
          </p>
        </div>
      ) : null}
    </section>
  )
}
