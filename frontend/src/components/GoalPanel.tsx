import { useEffect, useState } from 'react'
import {
  appendGoalIteration,
  createGoal,
  listGoals,
  readGoalContext,
  readGoalLedger,
  updateGoalStatus,
} from '../lib/api'
import type { GoalRecord, GoalStatus, VerificationProfileRecord } from '../lib/types'

type Props = {
  workspaceId: string | null
  runtime: 'codex' | 'opencode'
  model: string
  verificationProfiles: VerificationProfileRecord[]
}

function parseLines(value: string) {
  return value
    .split('\n')
    .map((line) => line.trim())
    .filter(Boolean)
}

function goalSummary(goal: GoalRecord | null) {
  if (!goal) return 'No goal selected.'
  const criteria = goal.acceptance_criteria?.length ?? 0
  const evidence = goal.evidence?.length ?? 0
  return `${goal.status} · ${criteria} criteria · ${evidence} evidence records`
}

export function GoalPanel({ workspaceId, runtime, model, verificationProfiles }: Props) {
  const [goals, setGoals] = useState<GoalRecord[]>([])
  const [selectedGoalId, setSelectedGoalId] = useState('')
  const [title, setTitle] = useState('')
  const [objective, setObjective] = useState('')
  const [criteria, setCriteria] = useState('')
  const [tranche, setTranche] = useState('')
  const [surface, setSurface] = useState('')
  const [commands, setCommands] = useState('')
  const [notes, setNotes] = useState('')
  const [iterationSummary, setIterationSummary] = useState('')
  const [iterationOutcome, setIterationOutcome] = useState('pending')
  const [iterationFiles, setIterationFiles] = useState('')
  const [evidenceCommand, setEvidenceCommand] = useState('')
  const [evidenceOutcome, setEvidenceOutcome] = useState('pass')
  const [statusDraft, setStatusDraft] = useState<GoalStatus>('active')
  const [skipReason, setSkipReason] = useState('')
  const [exportText, setExportText] = useState('')
  const [message, setMessage] = useState('')
  const [loading, setLoading] = useState(false)

  const selectedGoal = goals.find((goal) => goal.goal_id === selectedGoalId) ?? null

  async function refreshGoals(nextSelectedGoalId = selectedGoalId) {
    if (!workspaceId) {
      setGoals([])
      setSelectedGoalId('')
      return
    }
    const nextGoals = await listGoals(workspaceId)
    setGoals(nextGoals)
    if (nextSelectedGoalId && nextGoals.some((goal) => goal.goal_id === nextSelectedGoalId)) {
      setSelectedGoalId(nextSelectedGoalId)
    } else {
      setSelectedGoalId(nextGoals[0]?.goal_id ?? '')
    }
  }

  useEffect(() => {
    // Intentionally re-fetch only when the workspace changes; refreshGoals is
    // recreated every render, so depending on it would refetch on each render.
    void refreshGoals().catch((error) => setMessage(error instanceof Error ? error.message : String(error)))
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [workspaceId])

  async function handleCreateGoal() {
    if (!workspaceId || !title.trim() || !objective.trim()) return
    setLoading(true)
    setMessage('')
    try {
      const created = await createGoal(workspaceId, {
        title: title.trim(),
        objective: objective.trim(),
        acceptance_criteria: parseLines(criteria),
        current_tranche: tranche.trim(),
        allowed_surface: parseLines(surface),
        verification_commands: parseLines(commands),
        verification_profile_ids: verificationProfiles.map((profile) => profile.profile_id),
        runtime_preference: runtime,
        preferred_model: model,
        resumption_notes: notes.trim(),
      })
      setTitle('')
      setObjective('')
      setCriteria('')
      setTranche('')
      setSurface('')
      setCommands('')
      setNotes('')
      await refreshGoals(created.goal_id)
      setMessage('Goal created. Markdown ledger generated from structured data.')
    } catch (error) {
      setMessage(error instanceof Error ? error.message : String(error))
    } finally {
      setLoading(false)
    }
  }

  async function handleAppendIteration() {
    if (!workspaceId || !selectedGoal || !iterationSummary.trim()) return
    setLoading(true)
    setMessage('')
    try {
      await appendGoalIteration(workspaceId, selectedGoal.goal_id, {
        role: 'operator',
        summary: iterationSummary.trim(),
        outcome: iterationOutcome.trim(),
        runtime,
        model,
        files_touched: parseLines(iterationFiles),
        evidence: evidenceCommand.trim()
          ? [{
              kind: 'verification',
              label: 'Operator evidence',
              command: evidenceCommand.trim(),
              outcome: evidenceOutcome.trim(),
            }]
          : [],
      })
      setIterationSummary('')
      setIterationFiles('')
      setEvidenceCommand('')
      await refreshGoals(selectedGoal.goal_id)
      setMessage('Iteration appended and ledger regenerated.')
    } catch (error) {
      setMessage(error instanceof Error ? error.message : String(error))
    } finally {
      setLoading(false)
    }
  }

  async function handleStatusUpdate() {
    if (!workspaceId || !selectedGoal) return
    setLoading(true)
    setMessage('')
    try {
      await updateGoalStatus(workspaceId, selectedGoal.goal_id, {
        status: statusDraft,
        verification_skipped_reason: skipReason.trim() || undefined,
      })
      setSkipReason('')
      await refreshGoals(selectedGoal.goal_id)
      setMessage('Goal status updated.')
    } catch (error) {
      setMessage(error instanceof Error ? error.message : String(error))
    } finally {
      setLoading(false)
    }
  }

  async function handleRead(kind: 'ledger' | 'context') {
    if (!workspaceId || !selectedGoal) return
    setLoading(true)
    setMessage('')
    try {
      const text = kind === 'ledger'
        ? await readGoalLedger(workspaceId, selectedGoal.goal_id)
        : await readGoalContext(workspaceId, selectedGoal.goal_id)
      setExportText(text)
      await navigator.clipboard?.writeText(text)
      setMessage(`${kind === 'ledger' ? 'Ledger' : 'Context packet'} loaded and copied when clipboard access is available.`)
    } catch (error) {
      setMessage(error instanceof Error ? error.message : String(error))
    } finally {
      setLoading(false)
    }
  }

  return (
    <section className="detail-section">
      <h4>/goal ledger</h4>
      <p className="subtle">
        Goal stays controller. Worker output becomes evidence, not automatic acceptance.
      </p>
      <div className="tag-row">
        <span className="tag">runtime: {runtime}</span>
        <span className="tag">model: {model || 'not selected'}</span>
        <span className="tag">verification profiles: {verificationProfiles.length}</span>
      </div>

      <div className="toolbar-row">
        <label className="detail-section field-stack runbook-picker" htmlFor="goal-select">
          <span className="filter-label">Saved goals</span>
          <select
            id="goal-select"
            name="goal-select"
            className="text-input"
            value={selectedGoalId}
            onChange={(event) => setSelectedGoalId(event.target.value)}
            disabled={!workspaceId || loading}
          >
            {goals.length ? goals.map((goal) => (
              <option key={goal.goal_id} value={goal.goal_id}>{goal.title}</option>
            )) : <option value="">No goals yet</option>}
          </select>
        </label>
        <button type="button" className="ghost-button" onClick={() => void refreshGoals()} disabled={!workspaceId || loading}>
          Refresh goals
        </button>
      </div>

      {selectedGoal ? (
        <div className="detail-section">
          <div className="row-meta">
            <span className={`pill pill-${selectedGoal.status === 'complete' ? 'fixed' : selectedGoal.status === 'blocked' ? 'red' : 'yellow'}`}>
              {selectedGoal.status}
            </span>
            <span className="tag">{selectedGoal.goal_id}</span>
          </div>
          <strong>{selectedGoal.title}</strong>
          <p className="subtle">{selectedGoal.objective}</p>
          <p className="subtle">{goalSummary(selectedGoal)}</p>
          <div className="toolbar-row">
            <button type="button" className="ghost-button" onClick={() => void handleRead('ledger')} disabled={loading}>
              Copy ledger
            </button>
            <button type="button" className="ghost-button" onClick={() => void handleRead('context')} disabled={loading}>
              Copy context packet
            </button>
          </div>
        </div>
      ) : null}

      <div className="detail-section">
        <h4>Create goal</h4>
        <label className="detail-section field-stack" htmlFor="goal-title">
          <span className="filter-label">Title</span>
          <input id="goal-title" className="text-input" value={title} onChange={(event) => setTitle(event.target.value)} />
        </label>
        <label className="detail-section field-stack" htmlFor="goal-objective">
          <span className="filter-label">Objective</span>
          <textarea id="goal-objective" className="text-area" rows={3} value={objective} onChange={(event) => setObjective(event.target.value)} />
        </label>
        <label className="detail-section field-stack" htmlFor="goal-criteria">
          <span className="filter-label">Acceptance criteria, one per line</span>
          <textarea id="goal-criteria" className="text-area" rows={3} value={criteria} onChange={(event) => setCriteria(event.target.value)} />
        </label>
        <label className="detail-section field-stack" htmlFor="goal-tranche">
          <span className="filter-label">Current tranche</span>
          <input id="goal-tranche" className="text-input" value={tranche} onChange={(event) => setTranche(event.target.value)} />
        </label>
        <label className="detail-section field-stack" htmlFor="goal-surface">
          <span className="filter-label">Allowed surface, one path per line</span>
          <textarea id="goal-surface" className="text-area" rows={3} value={surface} onChange={(event) => setSurface(event.target.value)} />
        </label>
        <label className="detail-section field-stack" htmlFor="goal-commands">
          <span className="filter-label">Verification commands, one per line</span>
          <textarea id="goal-commands" className="text-area" rows={3} value={commands} onChange={(event) => setCommands(event.target.value)} />
        </label>
        <label className="detail-section field-stack" htmlFor="goal-notes">
          <span className="filter-label">Resumption notes</span>
          <textarea id="goal-notes" className="text-area" rows={2} value={notes} onChange={(event) => setNotes(event.target.value)} />
        </label>
        <button type="button" onClick={() => void handleCreateGoal()} disabled={!workspaceId || loading || !title.trim() || !objective.trim()}>
          Create goal
        </button>
      </div>

      <div className="detail-section">
        <h4>Append evidence</h4>
        <label className="detail-section field-stack" htmlFor="goal-iteration-summary">
          <span className="filter-label">Iteration summary</span>
          <textarea id="goal-iteration-summary" className="text-area" rows={3} value={iterationSummary} onChange={(event) => setIterationSummary(event.target.value)} />
        </label>
        <div className="toolbar-row">
          <label className="detail-section field-stack" htmlFor="goal-iteration-outcome">
            <span className="filter-label">Outcome</span>
            <input id="goal-iteration-outcome" className="text-input" value={iterationOutcome} onChange={(event) => setIterationOutcome(event.target.value)} />
          </label>
          <label className="detail-section field-stack" htmlFor="goal-evidence-outcome">
            <span className="filter-label">Evidence outcome</span>
            <input id="goal-evidence-outcome" className="text-input" value={evidenceOutcome} onChange={(event) => setEvidenceOutcome(event.target.value)} />
          </label>
        </div>
        <label className="detail-section field-stack" htmlFor="goal-iteration-files">
          <span className="filter-label">Files touched, one per line</span>
          <textarea id="goal-iteration-files" className="text-area" rows={2} value={iterationFiles} onChange={(event) => setIterationFiles(event.target.value)} />
        </label>
        <label className="detail-section field-stack" htmlFor="goal-evidence-command">
          <span className="filter-label">Verification/evidence command</span>
          <input id="goal-evidence-command" className="text-input" value={evidenceCommand} onChange={(event) => setEvidenceCommand(event.target.value)} />
        </label>
        <button type="button" onClick={() => void handleAppendIteration()} disabled={!selectedGoal || loading || !iterationSummary.trim()}>
          Append iteration
        </button>
      </div>

      <div className="detail-section">
        <h4>Status gate</h4>
        <div className="toolbar-row">
          <select className="text-input" value={statusDraft} onChange={(event) => setStatusDraft(event.target.value as GoalStatus)}>
            <option value="draft">draft</option>
            <option value="active">active</option>
            <option value="blocked">blocked</option>
            <option value="complete">complete</option>
            <option value="archived">archived</option>
          </select>
          <button type="button" onClick={() => void handleStatusUpdate()} disabled={!selectedGoal || loading}>
            Update status
          </button>
        </div>
        <label className="detail-section field-stack" htmlFor="goal-skip-reason">
          <span className="filter-label">Verification skip reason, required only when completing without evidence</span>
          <textarea id="goal-skip-reason" className="text-area" rows={2} value={skipReason} onChange={(event) => setSkipReason(event.target.value)} />
        </label>
      </div>

      {message ? <p className="subtle">{message}</p> : null}
      {exportText ? <pre className="prompt-block">{exportText}</pre> : null}
    </section>
  )
}
