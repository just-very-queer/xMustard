import type { CostSummary } from '../lib/types'
import { SummaryCard } from './TrackerPrimitives'

function formatUsd(value: number) {
  return `$${value.toFixed(4)}`
}

function formatSeconds(durationMs: number) {
  return `${(durationMs / 1000).toFixed(1)}s`
}

type Props = {
  summary: CostSummary | null
}

export function CostSummaryPanel({ summary }: Props) {
  if (!summary) {
    return (
      <section className="detail-section">
        <h4>Run costs</h4>
        <p className="subtle">No run metrics captured yet.</p>
      </section>
    )
  }

  const runtimeEntries = Object.entries(summary.cost_by_runtime).sort((a, b) => b[1] - a[1])
  const modelEntries = Object.entries(summary.cost_by_model).sort((a, b) => b[1] - a[1])

  return (
    <section className="detail-section">
      <div className="panel-header">
        <div>
          <h4>Run costs</h4>
          <p className="subtle">Estimated usage across all recorded runs in this workspace.</p>
        </div>
      </div>
      <div className="summary-grid analysis-summary-grid">
        <SummaryCard label="Total cost" value={formatUsd(summary.total_estimated_cost)} accent="amber" />
        <SummaryCard label="Runs" value={summary.total_runs} accent="ink" />
        <SummaryCard label="Tokens" value={summary.total_input_tokens + summary.total_output_tokens} accent="blue" />
        <SummaryCard label="Runtime" value={formatSeconds(summary.total_duration_ms)} accent="sand" />
      </div>
      <div className="tag-row">
        <span className="tag">input: {summary.total_input_tokens}</span>
        <span className="tag">output: {summary.total_output_tokens}</span>
        {Object.entries(summary.runs_by_status).map(([status, count]) => (
          <span key={status} className="tag">
            {status}: {count}
          </span>
        ))}
      </div>
      {runtimeEntries.length ? (
        <div className="activity-list">
          {runtimeEntries.map(([runtime, cost]) => (
            <div key={runtime} className="evidence-row">
              <span>{runtime}</span>
              <small>{formatUsd(cost)}</small>
            </div>
          ))}
        </div>
      ) : null}
      {modelEntries.length ? (
        <div className="tag-row">
          {modelEntries.slice(0, 6).map(([model, cost]) => (
            <span key={model} className="tag">
              {model}: {formatUsd(cost)}
            </span>
          ))}
        </div>
      ) : null}
    </section>
  )
}
