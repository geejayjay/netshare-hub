import { useQuery } from '@tanstack/react-query'
import { Activity, ChevronLeft, ChevronRight, Download, RefreshCw, RotateCcw } from 'lucide-react'
import { useState } from 'react'
import { EmptyState, ErrorState, PageHeader } from '@/shared/components'
import { cn } from '@/shared/lib/cn'
import { formatDateTime } from '@/shared/lib/format'
import { Badge, Button, Card, Input, Select, Spinner, type Tone } from '@/shared/ui'
import { activityExportUrl, activityQuery, type ActivityFilters, type ActivityOp } from '../api'

function opTone(op: string, ok: boolean): Tone {
  if (!ok) return 'danger'
  switch (op) {
    case 'delete':
    case 'rmdir':
      return 'danger'
    case 'rename':
      return 'warning'
    case 'write':
    case 'create':
    case 'mkdir':
      return 'info'
    default:
      return 'neutral'
  }
}

export function ActivityPage() {
  const [share, setShare] = useState('')
  const [user, setUser] = useState('')
  const [op, setOp] = useState<ActivityOp | ''>('')
  const [clientIp, setClientIp] = useState('')
  const [path, setPath] = useState('')
  const [cursorStack, setCursorStack] = useState<number[]>([])

  const before = cursorStack[cursorStack.length - 1]
  const filters: ActivityFilters = {
    share: share.trim() || undefined,
    user: user.trim() || undefined,
    op: op || undefined,
    clientIp: clientIp.trim() || undefined,
    path: path.trim() || undefined,
    before,
    limit: 50,
  }

  const feed = useQuery(activityQuery(filters))
  const items = feed.data?.items ?? []
  const nextCursor = feed.data?.next ?? 0
  const hasFilters = Boolean(share || user || op || clientIp || path)

  function resetPagination() {
    setCursorStack([])
  }

  function clearAllFilters() {
    setShare('')
    setUser('')
    setOp('')
    setClientIp('')
    setPath('')
    setCursorStack([])
  }

  return (
    <div className="flex flex-col gap-5">
      <PageHeader
        title="Activity Feed"
        info="SMB audit stream of connects, writes, renames, deletes, and logins across all shares. Enable SMB activity logging in Settings to record events."
        actions={
          <div className="flex items-center gap-2">
            <Button variant="secondary" size="sm" onClick={() => void feed.refetch()}>
              <RefreshCw className={cn('size-3.5', feed.isFetching && 'animate-spin')} />
              Refresh
            </Button>
            <a
              href={activityExportUrl(filters)}
              download="smb-activity.csv"
              className="inline-flex h-8 items-center gap-1.5 rounded-md border border-border-strong bg-surface px-3 text-[0.8125rem] font-medium text-fg shadow-xs transition-colors hover:bg-surface-2"
            >
              <Download className="size-3.5" />
              Export CSV
            </a>
          </div>
        }
      />

      <Card>
        <div className="grid gap-2.5 border-b border-border bg-surface-2/40 p-4 sm:grid-cols-2 lg:grid-cols-6">
          <Input
            placeholder="Share…"
            value={share}
            onChange={(e) => {
              setShare(e.target.value)
              resetPagination()
            }}
          />
          <Input
            placeholder="Username…"
            value={user}
            onChange={(e) => {
              setUser(e.target.value)
              resetPagination()
            }}
          />
          <Select
            value={op}
            onChange={(e) => {
              setOp(e.target.value as ActivityOp | '')
              resetPagination()
            }}
            aria-label="Filter by operation"
          >
            <option value="">All operations</option>
            <option value="connect">Connect</option>
            <option value="disconnect">Disconnect</option>
            <option value="write">Write</option>
            <option value="rename">Rename</option>
            <option value="delete">Delete</option>
            <option value="create_folder">Create folder</option>
            <option value="login">Login</option>
            <option value="login_failed">Login failed</option>
          </Select>
          <Input
            placeholder="Client IP…"
            value={clientIp}
            onChange={(e) => {
              setClientIp(e.target.value)
              resetPagination()
            }}
          />
          <Input
            placeholder="Path contains…"
            value={path}
            onChange={(e) => {
              setPath(e.target.value)
              resetPagination()
            }}
          />
          <Button variant="secondary" disabled={!hasFilters} onClick={clearAllFilters}>
            <RotateCcw className="size-3.5" />
            Reset
          </Button>
        </div>

        {feed.isLoading && !feed.data ? (
          <div className="flex justify-center py-14">
            <Spinner />
          </div>
        ) : feed.error ? (
          <div className="p-6">
            <ErrorState error={feed.error} onRetry={() => void feed.refetch()} />
          </div>
        ) : items.length === 0 ? (
          <div className="p-8">
            <EmptyState
              title="No activity recorded"
              description={
                hasFilters
                  ? 'No events match your current filters.'
                  : 'Enable SMB activity logging in Settings to record client file operations.'
              }
            />
          </div>
        ) : (
          <>
            <div className="overflow-x-auto">
              <table className="w-full border-collapse text-sm">
                <thead className="border-b border-border bg-surface-2/50 text-left text-[0.6875rem] font-semibold tracking-wider text-fg-subtle uppercase">
                  <tr>
                    <th className="px-4 py-2.5">Time</th>
                    <th className="px-4 py-2.5">Operation</th>
                    <th className="px-4 py-2.5">User</th>
                    <th className="px-4 py-2.5">Share</th>
                    <th className="px-4 py-2.5">Path</th>
                    <th className="px-4 py-2.5">Client</th>
                  </tr>
                </thead>
                <tbody className="divide-y divide-border">
                  {items.map((ev) => (
                    <tr key={ev.id} className="transition-colors hover:bg-surface-2/40">
                      <td className="px-4 py-2.5 text-xs whitespace-nowrap text-fg-muted tabular-nums">
                        {formatDateTime(ev.at)}
                      </td>
                      <td className="px-4 py-2.5">
                        <Badge tone={opTone(ev.op, ev.ok)} dot>
                          {ev.op}
                        </Badge>
                      </td>
                      <td className="px-4 py-2.5 font-medium text-fg">{ev.user || '—'}</td>
                      <td className="px-4 py-2.5">
                        <Badge tone="neutral">{ev.share}</Badge>
                      </td>
                      <td className="max-w-md px-4 py-2.5">
                        <div className="truncate font-mono text-xs text-fg" title={ev.path}>
                          {ev.path || '/'}
                        </div>
                        {ev.path2 && (
                          <div className="truncate font-mono text-[0.6875rem] text-fg-muted" title={ev.path2}>
                            → {ev.path2}
                          </div>
                        )}
                        {!ev.ok && ev.error && <div className="text-[0.6875rem] text-danger">{ev.error}</div>}
                      </td>
                      <td className="px-4 py-2.5 text-xs text-fg-muted tabular-nums">
                        <div>{ev.clientIp || '—'}</div>
                        {ev.clientName && ev.clientName !== ev.clientIp && (
                          <div className="text-[0.6875rem] text-fg-subtle">{ev.clientName}</div>
                        )}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>

            <div className="flex items-center justify-between border-t border-border px-4 py-3 text-xs text-fg-muted">
              <span className="inline-flex items-center gap-1.5">
                <Activity className="size-3.5 text-primary" />
                Showing {items.length} events {cursorStack.length > 0 && `(page ${cursorStack.length + 1})`}
              </span>
              <div className="flex items-center gap-2">
                <Button
                  variant="secondary"
                  size="sm"
                  disabled={cursorStack.length === 0}
                  onClick={() => setCursorStack((prev) => prev.slice(0, -1))}
                >
                  <ChevronLeft className="size-3.5" />
                  Newer
                </Button>
                <Button
                  variant="secondary"
                  size="sm"
                  disabled={!nextCursor}
                  onClick={() => setCursorStack((prev) => [...prev, nextCursor])}
                >
                  Older
                  <ChevronRight className="size-3.5" />
                </Button>
              </div>
            </div>
          </>
        )}
      </Card>
    </div>
  )
}
