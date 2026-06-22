import { useCallback, useEffect, useState } from 'react'
import {
  listProviders,
  addProvider,
  removeProvider,
  listRoutingRules,
  setRoutingRule,
  listPrincipals,
  mintToken,
  rotateToken,
  revokeToken,
  listAuthAudit,
} from '../lib/api'
import type {
  OpenAIProvider,
  RoutingRule,
  AuthPrincipal,
  AuthAuditEvent,
} from '../lib/types'

// AdminPanel is the operator cockpit for the HTTP-only control surfaces that are
// deliberately NOT on the 9-tool MCP agent surface: OpenAI-compatible providers,
// task-typed routing rules, and bearer-token management (mint/rotate/revoke + the
// auth-audit trail). It talks to /api/providers*, /api/routes, and /api/auth/*.
export function AdminPanel() {
  return (
    <div className="admin-panel" style={{ display: 'grid', gap: '1.5rem', padding: '1rem' }}>
      <ProvidersSection />
      <RoutingSection />
      <TokensSection />
    </div>
  )
}

const TASK_TYPES = [
  'locate',
  'code_edit_patch',
  'multi_step_debug_reason',
  'repo_qa_explain',
  'test_gen_validate',
  'vision_ui_diagnose',
]

function ProvidersSection() {
  const [providers, setProviders] = useState<OpenAIProvider[]>([])
  const [form, setForm] = useState<OpenAIProvider>({
    name: '',
    kind: 'ollama',
    base_url: '',
    api_key_env: '',
    default_model: '',
    supports_vision: false,
  })
  const [error, setError] = useState('')

  const refresh = useCallback(async () => {
    try {
      setProviders(await listProviders())
      setError('')
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    }
  }, [])
  useEffect(() => {
    void refresh()
  }, [refresh])

  async function add() {
    if (!form.name.trim() || !form.base_url.trim()) {
      setError('name and base_url are required')
      return
    }
    try {
      await addProvider({ ...form, name: form.name.trim(), base_url: form.base_url.trim() })
      setForm({ name: '', kind: 'ollama', base_url: '', api_key_env: '', default_model: '', supports_vision: false })
      await refresh()
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    }
  }
  async function remove(name: string) {
    try {
      await removeProvider(name)
      await refresh()
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    }
  }

  return (
    <section className="sidebar-panel">
      <h2>Providers</h2>
      <p className="subtle">OpenAI-compatible backends. The secret lives in an env var (api_key_env) — never stored here.</p>
      {error && <p className="error-text">{error}</p>}
      <table className="data-table" style={{ width: '100%' }}>
        <thead>
          <tr><th>Name</th><th>Kind</th><th>Base URL</th><th>Key env</th><th>Default model</th><th>Vision</th><th></th></tr>
        </thead>
        <tbody>
          {providers.map((p) => (
            <tr key={p.name}>
              <td>{p.name}</td>
              <td>{p.kind}</td>
              <td><code>{p.base_url}</code></td>
              <td>{p.api_key_env || '—'}</td>
              <td>{p.default_model || '—'}</td>
              <td>{p.supports_vision ? 'yes' : 'no'}</td>
              <td><button className="nav-button" onClick={() => void remove(p.name)}>Remove</button></td>
            </tr>
          ))}
          {providers.length === 0 && (
            <tr><td colSpan={7} className="subtle">No providers configured.</td></tr>
          )}
        </tbody>
      </table>
      <div className="field-row" style={{ display: 'flex', flexWrap: 'wrap', gap: '0.5rem', marginTop: '0.75rem' }}>
        <input placeholder="name" value={form.name} onChange={(e) => setForm({ ...form, name: e.target.value })} />
        <select value={form.kind} onChange={(e) => setForm({ ...form, kind: e.target.value })}>
          {['ollama', 'openai', 'vllm', 'lmstudio', 'custom'].map((k) => <option key={k} value={k}>{k}</option>)}
        </select>
        <input placeholder="base_url (http://…)" value={form.base_url} onChange={(e) => setForm({ ...form, base_url: e.target.value })} />
        <input placeholder="api_key_env (optional)" value={form.api_key_env} onChange={(e) => setForm({ ...form, api_key_env: e.target.value })} />
        <input placeholder="default_model (optional)" value={form.default_model} onChange={(e) => setForm({ ...form, default_model: e.target.value })} />
        <label className="subtle" style={{ display: 'flex', alignItems: 'center', gap: '0.25rem' }}>
          <input type="checkbox" checked={form.supports_vision} onChange={(e) => setForm({ ...form, supports_vision: e.target.checked })} /> vision
        </label>
        <button className="nav-button nav-button-active" onClick={() => void add()}>Add provider</button>
      </div>
    </section>
  )
}

function RoutingSection() {
  const [rules, setRules] = useState<RoutingRule[]>([])
  const [form, setForm] = useState<RoutingRule>({ task_type: TASK_TYPES[0], provider: '', model: '' })
  const [error, setError] = useState('')

  const refresh = useCallback(async () => {
    try {
      setRules(await listRoutingRules())
      setError('')
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    }
  }, [])
  useEffect(() => {
    void refresh()
  }, [refresh])

  async function save() {
    if (!form.provider.trim() || !form.model.trim()) {
      setError('provider and model are required')
      return
    }
    try {
      setRules(await setRoutingRule({ ...form, provider: form.provider.trim(), model: form.model.trim() }))
      setForm({ task_type: TASK_TYPES[0], provider: '', model: '' })
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    }
  }

  return (
    <section className="sidebar-panel">
      <h2>Task-typed routing</h2>
      <p className="subtle">Map each task type to a provider + model. Setting an existing task type replaces its rule.</p>
      {error && <p className="error-text">{error}</p>}
      <table className="data-table" style={{ width: '100%' }}>
        <thead><tr><th>Task type</th><th>Provider</th><th>Model</th></tr></thead>
        <tbody>
          {rules.map((r) => (
            <tr key={r.task_type}><td>{r.task_type}</td><td>{r.provider}</td><td>{r.model}</td></tr>
          ))}
          {rules.length === 0 && <tr><td colSpan={3} className="subtle">No routing rules.</td></tr>}
        </tbody>
      </table>
      <div className="field-row" style={{ display: 'flex', flexWrap: 'wrap', gap: '0.5rem', marginTop: '0.75rem' }}>
        <select value={form.task_type} onChange={(e) => setForm({ ...form, task_type: e.target.value })}>
          {TASK_TYPES.map((t) => <option key={t} value={t}>{t}</option>)}
        </select>
        <input placeholder="provider" value={form.provider} onChange={(e) => setForm({ ...form, provider: e.target.value })} />
        <input placeholder="model" value={form.model} onChange={(e) => setForm({ ...form, model: e.target.value })} />
        <button className="nav-button nav-button-active" onClick={() => void save()}>Set rule</button>
      </div>
    </section>
  )
}

function TokensSection() {
  const [principals, setPrincipals] = useState<AuthPrincipal[]>([])
  const [audit, setAudit] = useState<AuthAuditEvent[]>([])
  const [id, setId] = useState('')
  const [role, setRole] = useState('agent')
  const [ttl, setTtl] = useState('0')
  const [minted, setMinted] = useState<{ id: string; token: string } | null>(null)
  const [error, setError] = useState('')

  const refresh = useCallback(async () => {
    try {
      const [p, a] = await Promise.all([listPrincipals(), listAuthAudit(25)])
      setPrincipals(p)
      setAudit(a)
      setError('')
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    }
  }, [])
  useEffect(() => {
    void refresh()
  }, [refresh])

  async function mint() {
    if (!id.trim()) {
      setError('token id is required')
      return
    }
    try {
      const res = await mintToken(id.trim(), role, Number(ttl) || 0)
      setMinted({ id: res.id, token: res.token })
      setId('')
      await refresh()
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    }
  }
  async function rotate(pid: string) {
    try {
      const res = await rotateToken(pid, 0)
      setMinted({ id: res.id, token: res.token })
      await refresh()
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    }
  }
  async function revoke(pid: string) {
    try {
      await revokeToken(pid)
      await refresh()
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    }
  }

  return (
    <section className="sidebar-panel">
      <h2>Tokens &amp; auth</h2>
      <p className="subtle">Bearer tokens per principal (admin · agent · readonly). The raw token shows once at mint/rotate — store it now.</p>
      {error && <p className="error-text">{error}</p>}
      {minted && (
        <p className="callout">
          New token for <strong>{minted.id}</strong>: <code>{minted.token}</code> — not recoverable.
          <button className="nav-button" onClick={() => setMinted(null)} style={{ marginLeft: '0.5rem' }}>Dismiss</button>
        </p>
      )}
      <table className="data-table" style={{ width: '100%' }}>
        <thead><tr><th>Principal</th><th>Role</th><th></th></tr></thead>
        <tbody>
          {principals.map((p) => (
            <tr key={p.id}>
              <td>{p.id}</td>
              <td>{p.role}</td>
              <td>
                <button className="nav-button" onClick={() => void rotate(p.id)}>Rotate</button>{' '}
                <button className="nav-button" onClick={() => void revoke(p.id)}>Revoke</button>
              </td>
            </tr>
          ))}
          {principals.length === 0 && <tr><td colSpan={3} className="subtle">No tokens (auth is open).</td></tr>}
        </tbody>
      </table>
      <div className="field-row" style={{ display: 'flex', flexWrap: 'wrap', gap: '0.5rem', marginTop: '0.75rem' }}>
        <input placeholder="token id" value={id} onChange={(e) => setId(e.target.value)} />
        <select value={role} onChange={(e) => setRole(e.target.value)}>
          {['admin', 'agent', 'readonly'].map((r) => <option key={r} value={r}>{r}</option>)}
        </select>
        <input placeholder="ttl seconds (0 = never)" value={ttl} onChange={(e) => setTtl(e.target.value)} style={{ width: '10rem' }} />
        <button className="nav-button nav-button-active" onClick={() => void mint()}>Mint token</button>
      </div>

      <h3 style={{ marginTop: '1rem' }}>Recent auth events</h3>
      <table className="data-table" style={{ width: '100%' }}>
        <thead><tr><th>When</th><th>Action</th><th>Actor</th><th>Detail</th></tr></thead>
        <tbody>
          {audit.map((e) => (
            <tr key={e.event_id}>
              <td className="subtle">{e.created_at}</td>
              <td>{e.action}</td>
              <td>{e.actor}</td>
              <td className="subtle">{e.detail || e.token_id || ''}</td>
            </tr>
          ))}
          {audit.length === 0 && <tr><td colSpan={4} className="subtle">No auth events.</td></tr>}
        </tbody>
      </table>
    </section>
  )
}
