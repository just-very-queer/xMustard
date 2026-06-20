import { useCallback, useEffect, useState } from 'react'
import {
  getActiveMemory,
  listMemory,
  proposeMemory,
  verifyMemory,
  type ActiveMemory,
  type MemoryEntry,
} from '../lib/api'

// MemoryPanel surfaces the governed runtime memory for a workspace: the verified
// active context (with stale/conflict flags), the pending proposals an operator can
// approve or reject, and a form to add a new memory.
export function MemoryPanel({ workspaceId }: { workspaceId: string }) {
  const [active, setActive] = useState<ActiveMemory | null>(null)
  const [pending, setPending] = useState<MemoryEntry[]>([])
  const [content, setContent] = useState('')
  const [title, setTitle] = useState('')
  const [paths, setPaths] = useState('')
  const [error, setError] = useState('')

  const refresh = useCallback(async () => {
    if (!workspaceId) return
    try {
      const [a, all] = await Promise.all([
        getActiveMemory(workspaceId),
        listMemory(workspaceId, 'pending'),
      ])
      setActive(a)
      setPending(all)
      setError('')
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    }
  }, [workspaceId])

  useEffect(() => {
    void refresh()
  }, [refresh])

  async function submit() {
    if (!content.trim()) return
    try {
      await proposeMemory(workspaceId, {
        content: content.trim(),
        title: title.trim() || undefined,
        paths: paths.trim() ? paths.split(',').map((p) => p.trim()) : undefined,
      })
      setContent('')
      setTitle('')
      setPaths('')
      await refresh()
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    }
  }

  async function vote(entryId: string, approve: boolean) {
    try {
      await verifyMemory(workspaceId, entryId, approve)
      await refresh()
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    }
  }

  if (!workspaceId) return null

  return (
    <section className="memory-panel">
      <header className="memory-header">
        <h3>Governed memory</h3>
        {active && (
          <span className="memory-stats">
            {active.active_count} verified
            {active.stale_count > 0 && <em className="memory-stale"> · {active.stale_count} stale</em>}
            {active.conflicts?.length > 0 && (
              <em className="memory-conflict"> · {active.conflicts.length} conflict</em>
            )}
          </span>
        )}
      </header>
      {error && <p className="memory-error">{error}</p>}

      <div className="memory-compose">
        <input placeholder="title (optional)" value={title} onChange={(e) => setTitle(e.target.value)} />
        <textarea
          placeholder="a durable fact / decision / gotcha to remember"
          value={content}
          onChange={(e) => setContent(e.target.value)}
        />
        <input
          placeholder="paths it's about, comma-separated (so recall flags it stale)"
          value={paths}
          onChange={(e) => setPaths(e.target.value)}
        />
        <button onClick={() => void submit()} disabled={!content.trim()}>
          Propose memory
        </button>
      </div>

      {pending.length > 0 && (
        <div className="memory-section">
          <h4>Pending verification ({pending.length})</h4>
          <ul>
            {pending.map((m) => (
              <li key={m.id} className="memory-pending">
                <div className="memory-body">
                  <strong>{m.title || m.id}</strong> — {m.content}
                  <span className="memory-meta">
                    {' '}
                    by {m.source} · {m.verifications.filter((v) => v.approve).length}/
                    {m.required_verifications} approvals
                  </span>
                </div>
                <div className="memory-actions">
                  <button onClick={() => void vote(m.id, true)}>Approve</button>
                  <button onClick={() => void vote(m.id, false)}>Reject</button>
                </div>
              </li>
            ))}
          </ul>
        </div>
      )}

      <div className="memory-section">
        <h4>Verified context ({active?.active_count ?? 0})</h4>
        <ul>
          {active?.entries.map((m) => (
            <li key={m.id} className={m.stale ? 'memory-entry memory-is-stale' : 'memory-entry'}>
              <strong>{m.title || m.id}</strong> — {m.content}
              {m.paths && m.paths.length > 0 && (
                <span className="memory-meta"> · {m.paths.join(', ')}</span>
              )}
              {m.stale && <span className="memory-stale-tag"> STALE: {m.stale_paths?.join(', ')}</span>}
            </li>
          ))}
        </ul>
      </div>
    </section>
  )
}
