const dateTime = new Intl.DateTimeFormat(undefined, { dateStyle: 'medium', timeStyle: 'short' })
const relTime = new Intl.RelativeTimeFormat(undefined, { numeric: 'auto' })

/** Formats an ISO timestamp from the API for display. */
export function formatDateTime(iso: string) {
  if (!iso) return '—'
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return '—'
  return dateTime.format(d)
}

/** Formats an ISO timestamp relative to now, e.g. "3 minutes ago". */
export function formatRelativeTime(iso: string) {
  if (!iso) return '—'
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return '—'
  const diffSec = Math.round((d.getTime() - Date.now()) / 1000)
  const abs = Math.abs(diffSec)
  if (abs < 45) return 'just now'
  if (abs < 3600) return relTime.format(Math.round(diffSec / 60), 'minute')
  if (abs < 86400) return relTime.format(Math.round(diffSec / 3600), 'hour')
  if (abs < 86400 * 30) return relTime.format(Math.round(diffSec / 86400), 'day')
  return dateTime.format(d)
}

const units = ['B', 'KB', 'MB', 'GB', 'TB', 'PB']

/** Formats a byte count, e.g. 1536 -> "1.5 KB". */
export function formatBytes(bytes: number) {
  if (!Number.isFinite(bytes) || bytes < 0) return '0 B'
  let value = bytes
  let unit = 0
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024
    unit++
  }
  return `${unit === 0 ? Math.round(value) : value.toFixed(1)} ${units[unit]}`
}
