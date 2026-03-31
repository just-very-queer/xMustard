import type { Dispatch, SetStateAction } from 'react'
import type { AppSettings, IssueContextPacket, LocalAgentCapabilities, RuntimeModel, IssueRecord, RuntimeProbeResult } from '../lib/types'

type Props = {
  runtime: 'codex' | 'opencode'
  model: string
  runtimeModels: RuntimeModel[]
  capabilities: LocalAgentCapabilities | null
  instruction: string
  queryPrompt: string
  issueContextPacket: IssueContextPacket | null
  selectedRunbookId: string
  runbookNameDraft: string
  runbookDescriptionDraft: string
  runbookTemplateDraft: string
  settings: AppSettings | null
  terminalId: string | null
  terminalInput: string
  terminalOutput: string
  runtimeProbe: RuntimeProbeResult | null
  runtimeProbeLoading: boolean
  selectedIssue: IssueRecord | null
  loading: boolean
  onRuntimeChange: (value: 'codex' | 'opencode') => void
  onModelChange: (value: string) => void
  onInstructionChange: (value: string) => void
  onQueryPromptChange: (value: string) => void
  onSelectedRunbookChange: (value: string) => void
  onRunbookNameDraftChange: (value: string) => void
  onRunbookDescriptionDraftChange: (value: string) => void
  onRunbookTemplateDraftChange: (value: string) => void
  onLoadRunbookInstruction: (mode: 'verify' | 'fix' | 'drift') => void
  onApplySelectedRunbook: () => void
  onSaveRunbook: () => void
  onDeleteRunbook: () => void
  onSettingsChange: Dispatch<SetStateAction<AppSettings | null>>
  onSaveSettings: () => void
  onRefreshCapabilities: () => void
  onProbeRuntime: () => void
  onOpenTerminal: () => void
  onCloseTerminal: () => void
  onTerminalInputChange: (value: string) => void
  onSendTerminal: () => void
  onQueryAgent: () => void
  onRunIssue: () => void
  onOpenRuns: () => void
  workspaceLoaded: boolean
}

export function ExecutionPane({
  runtime,
  model,
  runtimeModels,
  capabilities,
  instruction,
  queryPrompt,
  issueContextPacket,
  selectedRunbookId,
  runbookNameDraft,
  runbookDescriptionDraft,
  runbookTemplateDraft,
  settings,
  terminalId,
  terminalInput,
  terminalOutput,
  runtimeProbe,
  runtimeProbeLoading,
  selectedIssue,
  loading,
  onRuntimeChange,
  onModelChange,
  onInstructionChange,
  onQueryPromptChange,
  onSelectedRunbookChange,
  onRunbookNameDraftChange,
  onRunbookDescriptionDraftChange,
  onRunbookTemplateDraftChange,
  onLoadRunbookInstruction,
  onApplySelectedRunbook,
  onSaveRunbook,
  onDeleteRunbook,
  onSettingsChange,
  onSaveSettings,
  onRefreshCapabilities,
  onProbeRuntime,
  onOpenTerminal,
  onCloseTerminal,
  onTerminalInputChange,
  onSendTerminal,
  onQueryAgent,
  onRunIssue,
  onOpenRuns,
  workspaceLoaded,
}: Props) {
  const runtimeAvailability = new Map(
    (capabilities?.runtimes ?? []).map((entry) => [entry.runtime, entry.available]),
  )
  const selectedRunbook = issueContextPacket?.available_runbooks.find((item) => item.runbook_id === selectedRunbookId) ?? null

  return (
    <div className="panel run-panel">
      <div className="panel-header">
        <div>
          <p className="eyebrow">Execution surface</p>
          <h3>Codex and OpenCode</h3>
        </div>
      </div>

      <section className="detail-section">
        <h4>Runtime</h4>
        <div className="toolbar-row">
          <label className="detail-section field-stack" htmlFor="runtime-select">
            <span className="filter-label">Runtime</span>
            <select
              id="runtime-select"
              name="runtime-select"
              className="text-input"
              value={runtime}
              onChange={(event) => onRuntimeChange(event.target.value as 'codex' | 'opencode')}
            >
              <option value="codex" disabled={runtimeAvailability.get('codex') === false}>Codex</option>
              <option value="opencode" disabled={runtimeAvailability.get('opencode') === false}>OpenCode</option>
            </select>
          </label>
          <label className="detail-section field-stack" htmlFor="model-select">
            <span className="filter-label">Model</span>
            <select id="model-select" name="model-select" className="text-input" value={model} onChange={(event) => onModelChange(event.target.value)}>
              {runtimeModels.map((entry) => (
                <option key={entry.id} value={entry.id}>
                  {entry.id}
                </option>
              ))}
            </select>
          </label>
        </div>
        <div className="tag-row">
          <span className="tag">Selected runtime: {capabilities?.selected_runtime ?? runtime}</span>
          <span className="tag">Live subscribe: {capabilities?.supports_live_subscribe ? 'yes' : 'no'}</span>
          <span className="tag">Terminal: {capabilities?.supports_terminal ? 'yes' : 'no'}</span>
        </div>
        <div className="toolbar-row">
          <button type="button" className="ghost-button" onClick={onRefreshCapabilities}>
            Refresh runtimes
          </button>
          <button type="button" onClick={onProbeRuntime} disabled={!workspaceLoaded || !model || runtimeProbeLoading}>
            {runtimeProbeLoading ? 'Testing runtime...' : 'Test runtime'}
          </button>
          <button type="button" className="ghost-button" onClick={onOpenRuns} disabled={!workspaceLoaded}>
            Open runs
          </button>
        </div>
        {runtimeProbe ? (
          <div className="detail-section probe-result">
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
            <pre className="prompt-block">{runtimeProbe.output_excerpt ?? runtimeProbe.error ?? 'No runtime output captured.'}</pre>
          </div>
        ) : null}
      </section>

      <section className="detail-section">
        <h4>Operator instruction</h4>
        <label className="detail-section field-stack" htmlFor="run-instruction">
          <span className="filter-label">Instruction</span>
          <textarea
            id="run-instruction"
            name="run-instruction"
            className="text-area"
            rows={7}
            placeholder="Optional: restrict surface, ask for verification, or request JSON-only output."
            value={instruction}
            onChange={(event) => onInstructionChange(event.target.value)}
          />
        </label>
      </section>

      <section className="detail-section">
        <h4>Quick query</h4>
        <label className="detail-section field-stack" htmlFor="query-prompt">
          <span className="filter-label">Workspace query</span>
          <textarea
            id="query-prompt"
            name="query-prompt"
            className="text-area"
            rows={4}
            placeholder="Ask the selected runtime to inspect the workspace or explain the current issue."
            value={queryPrompt}
            onChange={(event) => onQueryPromptChange(event.target.value)}
          />
        </label>
        <div className="toolbar-row">
          <button type="button" onClick={onQueryAgent} disabled={!workspaceLoaded || !model || !queryPrompt.trim() || loading}>
            Query agent
          </button>
          <span className="subtle">One-shot workspace run. Results appear in Runs.</span>
        </div>
      </section>

      <section className="detail-section">
        <h4>Runbook</h4>
        <div className="toolbar-row">
          <label className="detail-section field-stack runbook-picker" htmlFor="selected-runbook">
            <span className="filter-label">Selected runbook</span>
            <select
              id="selected-runbook"
              name="selected-runbook"
              className="text-input"
              value={selectedRunbookId}
              onChange={(event) => onSelectedRunbookChange(event.target.value)}
            >
              {(issueContextPacket?.available_runbooks ?? []).map((runbook) => (
                <option key={runbook.runbook_id} value={runbook.runbook_id}>
                  {runbook.name}
                </option>
              ))}
            </select>
          </label>
          <button type="button" className="ghost-button" onClick={() => onLoadRunbookInstruction('verify')}>
            Load verify runbook
          </button>
          <button type="button" className="ghost-button" onClick={() => onLoadRunbookInstruction('fix')}>
            Load fix runbook
          </button>
          <button type="button" className="ghost-button" onClick={() => onLoadRunbookInstruction('drift')}>
            Load drift audit
          </button>
          <button type="button" onClick={onApplySelectedRunbook} disabled={!issueContextPacket?.available_runbooks.length}>
            Load selected runbook
          </button>
        </div>
        {selectedRunbook ? (
          <p className="subtle">
            {selectedRunbook.description || 'No description set.'}
            {selectedRunbook.built_in ? ' Built-in runbook.' : ' Custom runbook.'}
          </p>
        ) : null}
        <ul className="runbook-list">
          {(issueContextPacket?.runbook ?? []).map((step) => (
            <li key={step}>{step}</li>
          ))}
        </ul>
        <div className="detail-section runbook-editor">
          <label className="detail-section field-stack" htmlFor="runbook-name">
            <span className="filter-label">Runbook name</span>
            <input
              id="runbook-name"
              name="runbook-name"
              className="text-input"
              placeholder="Focused verify"
              value={runbookNameDraft}
              onChange={(event) => onRunbookNameDraftChange(event.target.value)}
            />
          </label>
          <label className="detail-section field-stack" htmlFor="runbook-description">
            <span className="filter-label">Description</span>
            <input
              id="runbook-description"
              name="runbook-description"
              className="text-input"
              placeholder="When to use this runbook"
              value={runbookDescriptionDraft}
              onChange={(event) => onRunbookDescriptionDraftChange(event.target.value)}
            />
          </label>
          <label className="detail-section field-stack" htmlFor="runbook-template">
            <span className="filter-label">Template</span>
            <textarea
              id="runbook-template"
              name="runbook-template"
              className="text-area"
              rows={6}
              placeholder="1. Verify...\n2. Inspect...\n3. Report..."
              value={runbookTemplateDraft}
              onChange={(event) => onRunbookTemplateDraftChange(event.target.value)}
            />
          </label>
          <div className="toolbar-row">
            <button type="button" onClick={onSaveRunbook} disabled={!selectedIssue || loading || !runbookNameDraft.trim() || !runbookTemplateDraft.trim()}>
              Save custom runbook
            </button>
            <button type="button" className="ghost-button" onClick={onDeleteRunbook} disabled={!selectedRunbook || selectedRunbook.built_in || loading}>
              Delete custom runbook
            </button>
          </div>
        </div>
      </section>

      <section className="detail-section">
        <h4>Agent settings</h4>
        <div className="toolbar-row">
          <label className="detail-section field-stack" htmlFor="settings-runtime">
            <span className="filter-label">Default runtime</span>
            <select
              id="settings-runtime"
              name="settings-runtime"
              className="text-input"
              value={settings?.local_agent_type ?? runtime}
              onChange={(event) =>
                onSettingsChange((current) =>
                  current
                    ? { ...current, local_agent_type: event.target.value as 'codex' | 'opencode' }
                    : { local_agent_type: event.target.value as 'codex' | 'opencode' },
                )
              }
            >
              <option value="codex" disabled={runtimeAvailability.get('codex') === false}>Codex</option>
              <option value="opencode" disabled={runtimeAvailability.get('opencode') === false}>OpenCode</option>
            </select>
          </label>
          <button type="button" onClick={onSaveSettings} disabled={!settings}>
            Save runtime settings
          </button>
        </div>
        <label className="detail-section field-stack" htmlFor="codex-bin">
          <span className="filter-label">Codex binary path</span>
          <input
            id="codex-bin"
            name="codex-bin"
            className="text-input"
            placeholder="Codex binary path"
            value={settings?.codex_bin ?? ''}
            onChange={(event) =>
              onSettingsChange((current) => ({ ...(current ?? { local_agent_type: runtime }), codex_bin: event.target.value }))
            }
          />
        </label>
        <label className="detail-section field-stack" htmlFor="opencode-bin">
          <span className="filter-label">OpenCode binary path</span>
          <input
            id="opencode-bin"
            name="opencode-bin"
            className="text-input"
            placeholder="OpenCode binary path"
            value={settings?.opencode_bin ?? ''}
            onChange={(event) =>
              onSettingsChange((current) => ({ ...(current ?? { local_agent_type: runtime }), opencode_bin: event.target.value }))
            }
          />
        </label>
        <label className="detail-section field-stack" htmlFor="codex-args">
          <span className="filter-label">Codex args</span>
          <input
            id="codex-args"
            name="codex-args"
            className="text-input"
            placeholder="Codex args"
            value={settings?.codex_args ?? ''}
            onChange={(event) =>
              onSettingsChange((current) => ({ ...(current ?? { local_agent_type: runtime }), codex_args: event.target.value }))
            }
          />
        </label>
      </section>

      <section className="detail-section">
        <h4>Workspace terminal</h4>
        <div className="toolbar-row">
          <button type="button" onClick={onOpenTerminal} disabled={!workspaceLoaded || Boolean(terminalId)}>
            Open terminal
          </button>
          <button type="button" onClick={onCloseTerminal} disabled={!terminalId}>
            Close terminal
          </button>
        </div>
        <div className="toolbar-row">
          <label className="detail-section field-stack" htmlFor="terminal-input">
            <span className="filter-label">Command</span>
            <input
              id="terminal-input"
              name="terminal-input"
              className="text-input"
              placeholder="Run a workspace command"
              value={terminalInput}
              onChange={(event) => onTerminalInputChange(event.target.value)}
            />
          </label>
          <button type="button" onClick={onSendTerminal} disabled={!terminalId || !terminalInput.trim()}>
            Send
          </button>
        </div>
        <pre className="terminal-block">{terminalOutput || 'Open a terminal to stream workspace output.'}</pre>
      </section>

      <div className="toolbar-row">
        <button type="button" onClick={onRunIssue} disabled={!selectedIssue || !workspaceLoaded || !model || loading}>
          Start issue run
        </button>
        <span className="subtle">{selectedIssue ? `Target ${selectedIssue.bug_id}` : 'Select an issue'}</span>
      </div>
    </div>
  )
}
