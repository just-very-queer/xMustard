import type { IssueQueueFilters } from '../lib/types'

export type QueuePreset = {
  presetId: string
  name: string
  description: string
  count: number
  mode: 'issues' | 'review' | 'drift'
  filters: IssueQueueFilters
}

type Props = {
  presets: QueuePreset[]
  selectedPresetId: string | null
  onSelect: (preset: QueuePreset) => void
  onClear: () => void
}

export function QueuePresetStrip({ presets, selectedPresetId, onSelect, onClear }: Props) {
  return (
    <section className="detail-section">
      <div className="toolbar-row saved-view-header">
        <div>
          <p className="eyebrow">System queues</p>
          <h4>Preset triage lanes</h4>
        </div>
        <button className="ghost-button" onClick={onClear}>
          Reset
        </button>
      </div>
      <div className="saved-view-strip">
        {presets.map((preset) => (
          <button
            key={preset.presetId}
            className={`saved-view-chip ${selectedPresetId === preset.presetId ? 'saved-view-chip-active' : ''}`}
            onClick={() => onSelect(preset)}
          >
            <strong>{preset.name}</strong>
            <small>{preset.description}</small>
            <small>{preset.count} issues</small>
          </button>
        ))}
      </div>
    </section>
  )
}
