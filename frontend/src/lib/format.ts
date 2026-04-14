export function formatDate(value?: string | null) {
  if (!value) return 'never'
  return new Date(value).toLocaleString()
}
