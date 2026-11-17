const dateTime = new Intl.DateTimeFormat(undefined, { dateStyle: 'medium', timeStyle: 'short' })

/** Formats an ISO timestamp from the API for display. */
export function formatDateTime(iso: string) {
  return dateTime.format(new Date(iso))
}

const units = ['B', 'KB', 'MB', 'GB', 'TB', 'PB']

/** Formats a byte count, e.g. 1536 -> "1.5 KB". */
export function formatBytes(bytes: number) {
  let value = bytes
  let unit = 0
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024
    unit++
  }
  return `${unit === 0 ? value : value.toFixed(1)} ${units[unit]}`
}
