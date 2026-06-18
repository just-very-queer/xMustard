import { useEffect, useState } from 'react'
import {
  getWorkspaceChanges,
  getWorkspaceDrift,
  getWorkspaceHotspots,
  getBlastRadius,
  getCockpitDashboard,
  indexWorkspace,
  type ChangeSet,
  type DriftReport,
  type Hotspot,
  type BlastRadius,
  type CockpitDashboard,
} from '../lib/api'

// Repo cockpit: change state + intelligence inspector. Surfaces the gitnexus-style
// change tracking, drift honesty, symbol-graph hotspots, and blast radius that the
// Rust core computes — the "what changed / what's risky to touch" view.
export function Cockpit({ workspaceId }: { workspaceId: string }) {
  const [changes, setChanges] = useState<ChangeSet | null>(null)
  const [drift, setDrift] = useState<DriftReport | null>(null)
  const [hotspots, setHotspots] = useState<Hotspot[]>([])
  const [dashboard, setDashboard] = useState<CockpitDashboard | null>(null)
  const [symbol, setSymbol] = useState('')
  const [blast, setBlast] = useState<BlastRadius | null>(null)
  const [message, setMessage] = useState('')
  const [loading, setLoading] = useState(false)

  async function refresh() {
    if (!workspaceId) return
    setLoading(true)
    setMessage('')
    try {
      const [c, d, h, db] = await Promise.all([
        getWorkspaceChanges(workspaceId).catch(() => null),
        getWorkspaceDrift(workspaceId).catch(() => null),
        getWorkspaceHotspots(workspaceId, 12).catch(() => [] as Hotspot[]),
        getCockpitDashboard(workspaceId).catch(() => null),
      ])
      setChanges(c)
      setDrift(d)
      setHotspots(h ?? [])
      setDashboard(db)
    } catch (e) {
      setMessage(e instanceof Error ? e.message : String(e))
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => {
    void refresh()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [workspaceId])

  async function lookupBlast() {
    if (!symbol.trim()) return
    try {
      setBlast(await getBlastRadius(workspaceId, symbol.trim()))
    } catch (e) {
      setMessage(e instanceof Error ? e.message : String(e))
    }
  }

  async function reindex() {
    try {
      await indexWorkspace(workspaceId)
      await refresh()
      setMessage('Re-indexed baseline.')
    } catch (e) {
      setMessage(e instanceof Error ? e.message : String(e))
    }
  }

  if (!workspaceId) {
    return <div className="cockpit-empty">Select a workspace to open the cockpit.</div>
  }

  return (
    <div className="cockpit">
      <header className="cockpit-header">
        <h2>Repo Cockpit</h2>
        <div className="cockpit-actions">
          <button onClick={() => void refresh()} disabled={loading}>
            {loading ? 'Refreshing…' : 'Refresh'}
          </button>
          <button onClick={() => void reindex()}>Re-index baseline</button>
        </div>
      </header>
      {message && <div className="cockpit-message">{message}</div>}

      {drift && drift.stale && (
        <div className="cockpit-drift-banner" role="alert">
          ⚠ Index may be stale — {drift.reasons.join('; ')}
          {drift.sibling_clone && ' (sibling-clone drift)'}
        </div>
      )}

      <div className="cockpit-grid">
        <section className="cockpit-pane">
          <h3>Risk state</h3>
          {dashboard ? (
            <ul className="cockpit-stats">
              <li>Issues: {dashboard.issues_total}</li>
              <li>Needs follow-up: {dashboard.needs_followup_count}</li>
              <li>Avg quality: {dashboard.avg_quality}</li>
              <li>Low quality: {dashboard.low_quality_count}</li>
              <li>Review-ready: {dashboard.review_ready_count}</li>
              <li>Audit events: {dashboard.audit_event_count}</li>
              <li>
                By severity:{' '}
                {Object.entries(dashboard.issues_by_severity)
                  .map(([k, v]) => `${k}:${v}`)
                  .join('  ')}
              </li>
            </ul>
          ) : (
            <p className="cockpit-muted">No dashboard data.</p>
          )}
        </section>

        <section className="cockpit-pane">
          <h3>Change state {changes ? `(${changes.changed_files.length} files)` : ''}</h3>
          {changes && changes.changed_files.length > 0 ? (
            <>
              <ul className="cockpit-changes">
                {changes.changed_files.slice(0, 12).map((f) => (
                  <li key={f.path}>
                    <span className={`change-tag change-${f.change}`}>{f.change}</span> {f.path}
                  </li>
                ))}
              </ul>
              <p className="cockpit-muted">
                Dirty symbols: {changes.dirty_symbols.length}
                {changes.dirty_symbols.slice(0, 6).length > 0 &&
                  ` — ${changes.dirty_symbols
                    .slice(0, 6)
                    .map((s) => s.symbol)
                    .join(', ')}…`}
              </p>
            </>
          ) : (
            <p className="cockpit-muted">No working-tree changes (or path not resolvable here).</p>
          )}
        </section>

        <section className="cockpit-pane">
          <h3>Hotspots (risky to touch)</h3>
          {hotspots.length > 0 ? (
            <ol className="cockpit-hotspots">
              {hotspots.map((h) => (
                <li key={h.path}>
                  <span className="cockpit-weight">{h.inbound_weight}</span> {h.path}{' '}
                  <span className="cockpit-muted">({h.dependent_count} dependents)</span>
                </li>
              ))}
            </ol>
          ) : (
            <p className="cockpit-muted">No symbol-graph hotspots.</p>
          )}
        </section>

        <section className="cockpit-pane">
          <h3>Intelligence inspector — blast radius</h3>
          <div className="cockpit-blast-input">
            <input
              value={symbol}
              onChange={(e) => setSymbol(e.target.value)}
              placeholder="symbol name (e.g. loadSnapshot)"
              onKeyDown={(e) => {
                if (e.key === 'Enter') void lookupBlast()
              }}
            />
            <button onClick={() => void lookupBlast()}>Trace</button>
          </div>
          {blast && (
            <div className="cockpit-blast-result">
              <p>
                <strong>{blast.symbol}</strong> defined in {blast.defined_in.join(', ') || '—'},
                referenced by <strong>{blast.referencing_file_count}</strong> files:
              </p>
              <ul className="cockpit-muted">
                {blast.referencing_files.slice(0, 10).map((p) => (
                  <li key={p}>{p}</li>
                ))}
              </ul>
            </div>
          )}
        </section>
      </div>
    </div>
  )
}
