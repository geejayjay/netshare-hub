import { useQuery } from '@tanstack/react-query'
import { Pause, Play, RefreshCw, RotateCcw, Trash2 } from 'lucide-react'
import { useCallback, useMemo, useState } from 'react'
import { useSSE } from '@/shared/api'
import { EmptyState, ErrorState, PageHeader } from '@/shared/components'
import { cn } from '@/shared/lib/cn'
import { Badge, Button, Card, Input, Select, Spinner, type Tone } from '@/shared/ui'
import { systemLogsQuery, type LogEntry, type LogLevel } from '../api'

function levelTone(level: string): Tone {
  switch (level) {
    case 'error':
      return 'danger'
    case 'warn':
      return 'warning'
    case 'info':
      return 'info'
    default:
      return 'neutral'
  }
}

function formatClock(iso: string): string {
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return ''
  return d.toLocaleTimeString(undefined, { hour12: false, hour: '2-digit', minute: '2-digit', second: '2-digit' })
}

function matchesFilters(entry: LogEntry, level: LogLevel, requestId: string, q: string): boolean {
  if (level && entry.level !== level) return false
  if (requestId && !(entry.requestId ?? '').includes(requestId)) return false
  if (q) {
    const lower = q.toLowerCase()
    const inMsg = entry.message.toLowerCase().includes(lower)
    const inUser = (entry.user ?? '').toLowerCase().includes(lower)
    const inReq = (entry.requestId ?? '').toLowerCase().includes(lower)
    const inAttrs = Object.entries(entry.attrs ?? {}).some(
      ([k, v]) => k.toLowerCase().includes(lower) || v.toLowerCase().includes(lower),
    )
    if (!inMsg && !inUser && !inReq && !inAttrs) return false
  }
  return true
}

export function LogsPage() {
  const [level, setLevel] = useState<LogLevel>('')
  const [requestId, setRequestId] = useState('')
  const [q, setQ] = useState('')
  const [live, setLive] = useState(true)
  const [streamed, setStreamed] = useState<LogEntry[]>([])

  const filters = useMemo(
    () => ({
      level: level || undefined,
      requestId: requestId.trim() || undefined,
      q: q.trim() || undefined,
      limit: 250,
    }),
    [level, requestId, q],
  )

  const logs = useQuery(systemLogsQuery(filters))

  const onLogEvent = useCallback((incoming: LogEntry) => {
    setStreamed((prev) => [incoming, ...prev].slice(0, 300))
  }, [])

  useSSE<LogEntry>('log', onLogEvent, live)

  const merged = useMemo(() => {
    const seen = new Set<number>()
    const out: LogEntry[] = []
    const trimmedReq = requestId.trim()
    const trimmedQ = q.trim()

    for (const e of streamed) {
      if (seen.has(e.seq)) continue
      if (!matchesFilters(e, level, trimmedReq, trimmedQ)) continue
      seen.add(e.seq)
      out.push(e)
    }
    for (const e of logs.data?.items ?? []) {
      if (seen.has(e.seq)) continue
      seen.add(e.seq)
      out.push(e)
    }
    return out.slice(0, 400)
  }, [streamed, logs.data, level, requestId, q])

  const hasFilters = Boolean(level || requestId || q)

  return (
    <div className="flex flex-col gap-5">
      <PageHeader
        title="Server Logs"
        info="Live structured server log tail over SSE. Paste a Request ID from any error toast to isolate the exact server log lines."
        actions={
          <div className="flex flex-wrap items-center gap-2">
            <Button variant={live ? 'primary' : 'secondary'} size="sm" onClick={() => setLive((v) => !v)}>
              {live ? <Pause className="size-3.5" /> : <Play className="size-3.5" />}
              {live ? 'Live tail' : 'Paused'}
            </Button>
            <Button variant="secondary" size="sm" onClick={() => void logs.refetch()}>
              <RefreshCw className={cn('size-3.5', logs.isFetching && 'animate-spin')} />
              Refresh
            </Button>
            {streamed.length > 0 && (
              <Button variant="ghost" size="sm" onClick={() => setStreamed([])} title="Clear live buffer">
                <Trash2 className="size-3.5" />
              </Button>
            )}
          </div>
        }
      />

      <Card>
        <div className="grid gap-2.5 border-b border-border bg-surface-2/40 p-4 sm:grid-cols-2 lg:grid-cols-5">
          <Select value={level} onChange={(e) => setLevel(e.target.value as LogLevel)} aria-label="Filter by level">
            <option value="">All levels</option>
            <option value="debug">Debug</option>
            <option value="info">Info</option>
            <option value="warn">Warn</option>
            <option value="error">Error</option>
          </Select>
          <Input
            placeholder="Request ID…"
            value={requestId}
            onChange={(e) => setRequestId(e.target.value)}
            className="font-mono text-xs"
          />
          <Input
            placeholder="Search message or attrs…"
            value={q}
            onChange={(e) => setQ(e.target.value)}
            className="lg:col-span-2"
          />
          <Button
            variant="secondary"
            disabled={!hasFilters}
            onClick={() => {
              setLevel('')
              setRequestId('')
              setQ('')
            }}
          >
            <RotateCcw className="size-3.5" />
            Reset
          </Button>
        </div>

        {logs.isLoading && merged.length === 0 ? (
          <div className="flex justify-center py-14">
            <Spinner />
          </div>
        ) : logs.error ? (
          <div className="p-6">
            <ErrorState error={logs.error} onRetry={() => void logs.refetch()} />
          </div>
        ) : merged.length === 0 ? (
          <div className="p-8">
            <EmptyState
              title="No matching log lines"
              description={hasFilters ? 'Clear or adjust your filters.' : 'Waiting for server events…'}
            />
          </div>
        ) : (
          <div className="max-h-[68dvh] divide-y divide-border overflow-y-auto font-mono text-xs">
            {merged.map((entry) => {
              const attrs = Object.entries(entry.attrs ?? {})
              return (
                <div
                  key={entry.seq}
                  className="flex flex-wrap items-baseline gap-x-2.5 gap-y-1 px-4 py-2 transition-colors hover:bg-surface-2/40"
                >
                  <span className="shrink-0 text-[0.7rem] text-fg-subtle tabular-nums">{formatClock(entry.at)}</span>
                  <Badge tone={levelTone(entry.level)} className="px-1.5 py-0 text-[0.65rem] uppercase">
                    {entry.level}
                  </Badge>
                  <span className="font-medium text-fg">{entry.message}</span>
                  {entry.requestId && (
                    <button
                      type="button"
                      onClick={() => setRequestId(entry.requestId ?? '')}
                      title="Filter by this Request ID"
                      className="rounded bg-primary-soft px-1.5 py-0.25 text-[0.6875rem] text-primary hover:underline"
                    >
                      req={entry.requestId}
                    </button>
                  )}
                  {entry.user && <span className="text-[0.6875rem] text-fg-muted">user={entry.user}</span>}
                  {attrs.map(([k, v]) => (
                    <span key={k} className="text-[0.6875rem] text-fg-subtle">
                      <span className="text-fg-muted">{k}</span>={v}
                    </span>
                  ))}
                </div>
              )
            })}
          </div>
        )}
      </Card>
    </div>
  )
}
