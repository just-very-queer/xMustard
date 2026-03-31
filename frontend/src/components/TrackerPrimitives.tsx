export function formatDate(value?: string | null) {
  if (!value) return 'never'
  return new Date(value).toLocaleString()
}

export function StatusPill({ children, tone }: { children: string; tone: string }) {
  return <span className={`pill pill-${tone}`}>{children}</span>
}

export function SummaryCard({ label, value, accent }: { label: string; value: string | number; accent: string }) {
  return (
    <article className="summary-card">
      <span className="summary-label">{label}</span>
      <strong className={`summary-value accent-${accent}`}>{value}</strong>
    </article>
  )
}
