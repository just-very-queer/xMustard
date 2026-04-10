import type { RunPlan } from '../lib/types'
import { StatusPill } from './TrackerPrimitives'

type Props = {
  plan: RunPlan
  loading: boolean
  onApprove: (feedback?: string) => void
  onReject: (reason: string) => void
  onRegenerate?: () => void
}

export function PlanPreview({ plan, loading, onApprove, onReject, onRegenerate }: Props) {
  return (
    <section className="detail-section">
      <div className="panel-header">
        <div>
          <p className="eyebrow">Fix plan</p>
          <h3>{plan.summary || 'No summary'}</h3>
        </div>
        <StatusPill tone={plan.phase === 'awaiting_approval' ? 'yellow' : 'neutral'}>{plan.phase}</StatusPill>
      </div>

      {plan.reasoning && (
        <div className="evidence-row" style={{ marginTop: '0.5rem' }}>
          <span className="subtle">Reasoning:</span>
          <small>{plan.reasoning}</small>
        </div>
      )}

      <div style={{ marginTop: '1rem' }}>
        <h4>Steps ({plan.steps.length})</h4>
        {plan.steps.length === 0 ? (
          <p className="subtle">No steps generated yet.</p>
        ) : (
          <div className="plan-steps">
            {plan.steps.map((step, index) => (
              <div key={step.step_id} className="plan-step">
                <div className="plan-step-header">
                  <span className="plan-step-number">{index + 1}</span>
                  <span className="plan-step-description">{step.description}</span>
                  <span className={`impact-badge impact-${step.estimated_impact}`}>{step.estimated_impact}</span>
                </div>
                {step.files_affected.length > 0 && (
                  <div className="plan-step-files">
                    <span className="subtle">Files:</span>
                    {step.files_affected.map((file) => (
                      <span key={file} className="tag">{file}</span>
                    ))}
                  </div>
                )}
                {step.risks.length > 0 && (
                  <div className="plan-step-risks">
                    <span className="subtle">Risks:</span>
                    {step.risks.map((risk, i) => (
                      <span key={i} className="tag tag-warning">{risk}</span>
                    ))}
                  </div>
                )}
              </div>
            ))}
          </div>
        )}
      </div>

      {plan.phase === 'awaiting_approval' && (
        <div className="toolbar-row" style={{ marginTop: '1rem' }}>
          <button
            type="button"
            onClick={() => onApprove()}
            disabled={loading}
          >
            Approve plan
          </button>
          <button
            type="button"
            className="ghost-button"
            onClick={() => {
              const feedback = window.prompt('Feedback (optional):')
              if (feedback !== null) {
                onApprove(feedback)
              }
            }}
            disabled={loading}
          >
            Approve with feedback
          </button>
          {onRegenerate && (
            <button
              type="button"
              className="ghost-button"
              onClick={onRegenerate}
              disabled={loading}
            >
              Regenerate
            </button>
          )}
          <button
            type="button"
            className="ghost-button"
            onClick={() => {
              const reason = window.prompt('Reason for rejection:')
              if (reason) {
                onReject(reason)
              }
            }}
            disabled={loading}
          >
            Reject plan
          </button>
        </div>
      )}

      {plan.feedback && (
        <div className="evidence-row" style={{ marginTop: '0.5rem' }}>
          <span className="subtle">Feedback:</span>
          <small>{plan.feedback}</small>
        </div>
      )}
    </section>
  )
}
