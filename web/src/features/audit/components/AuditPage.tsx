import { useQuery } from '@tanstack/react-query'
import { ChevronLeft, ChevronRight, Eye, RefreshCw, RotateCcw } from 'lucide-react'
import { useState } from 'react'
import { EmptyState, ErrorState, PageHeader } from '@/shared/components'
import { cn } from '@/shared/lib/cn'
import { formatDateTime } from '@/shared/lib/format'
import { Badge, Button, Card, Drawer, Input, Spinner, type Tone } from '@/shared/ui'
import { auditLogQuery, type AuditEntry } from '../api'

function actionTone(action: string): Tone {
  if (action.startsWith('DELETE')) return 'danger'
  if (action.startsWith('PUT') || action.startsWith('PATCH')) return 'warning'
  if (action.startsWith('POST')) return 'info'
  return 'neutral'
}

function prettyJSON(raw?: string): string {
  if (!raw) return '—'
  try {
    return JSON.stringify(JSON.parse(raw), null, 2)
  } catch {
    return raw
  }
}

export function AuditPage() {
  const [actor, setActor] = useState('')
  const [action, setAction] = useState('')
  const [q, setQ] = useState('')
  const [page, setPage] = useState(1)
  const [selected, setSelected] = useState<AuditEntry | null>(null)

  const logs = useQuery(
    auditLogQuery({
      actor: actor.trim() || undefined,
      action: action.trim() || undefined,
      q: q.trim() || undefined,
      page,
      pageSize: 50,
    }),
  )

  const items = logs.data?.items ?? []
  const meta = logs.data?.page
  const hasFilters = Boolean(actor || action || q)

  return (
    <div className="flex flex-col gap-5">
      <PageHeader
        title="Audit Log"
        info="Immutable append-only record of administrative API changes (users, shares, settings, security, scheduler, replication, block disks). Passwords and secrets are automatically redacted."
        actions={
          <Button variant="secondary" size="sm" onClick={() => void logs.refetch()}>
            <RefreshCw className={cn('size-3.5', logs.isFetching && 'animate-spin')} />
            Refresh
          </Button>
        }
      />

      <Card>
        <div className="grid gap-2.5 border-b border-border bg-surface-2/40 p-4 sm:grid-cols-2 lg:grid-cols-4">
          <Input
            placeholder="Actor username…"
            value={actor}
            onChange={(e) => {
              setActor(e.target.value)
              setPage(1)
            }}
          />
          <Input
            placeholder="Action (e.g. POST shares)…"
            value={action}
            onChange={(e) => {
              setAction(e.target.value)
              setPage(1)
            }}
          />
          <Input
            placeholder="Search target, payload, request ID…"
            value={q}
            onChange={(e) => {
              setQ(e.target.value)
              setPage(1)
            }}
          />
          <Button
            variant="secondary"
            disabled={!hasFilters}
            onClick={() => {
              setActor('')
              setAction('')
              setQ('')
              setPage(1)
            }}
          >
            <RotateCcw className="size-3.5" />
            Reset
          </Button>
        </div>

        {logs.isLoading && !logs.data ? (
          <div className="flex justify-center py-14">
            <Spinner />
          </div>
        ) : logs.error ? (
          <div className="p-6">
            <ErrorState error={logs.error} onRetry={() => void logs.refetch()} />
          </div>
        ) : items.length === 0 ? (
          <div className="p-8">
            <EmptyState
              title="No audit events"
              description={hasFilters ? 'No events match your current filters.' : 'Admin changes will appear here automatically.'}
            />
          </div>
        ) : (
          <>
            <div className="overflow-x-auto">
              <table className="w-full border-collapse text-sm">
                <thead className="border-b border-border bg-surface-2/50 text-left text-[0.6875rem] font-semibold tracking-wider text-fg-subtle uppercase">
                  <tr>
                    <th className="px-4 py-2.5">Time</th>
                    <th className="px-4 py-2.5">Actor</th>
                    <th className="px-4 py-2.5">Action</th>
                    <th className="px-4 py-2.5">Target</th>
                    <th className="px-4 py-2.5">Request ID</th>
                    <th className="px-4 py-2.5 text-right">Details</th>
                  </tr>
                </thead>
                <tbody className="divide-y divide-border">
                  {items.map((row) => (
                    <tr key={row.id} className="hover:bg-surface-2/40">
                      <td className="px-4 py-2.5 text-xs whitespace-nowrap text-fg-muted tabular-nums">
                        {formatDateTime(row.at)}
                      </td>
                      <td className="px-4 py-2.5 font-medium text-fg">{row.actor || 'system'}</td>
                      <td className="px-4 py-2.5">
                        <Badge tone={actionTone(row.action)}>{row.action}</Badge>
                      </td>
                      <td className="px-4 py-2.5 font-mono text-xs text-fg-muted">{row.target}</td>
                      <td className="px-4 py-2.5 font-mono text-xs text-fg-subtle">{row.requestId || '—'}</td>
                      <td className="px-4 py-2.5 text-right">
                        <Button variant="ghost" size="icon" className="size-7" onClick={() => setSelected(row)} aria-label="Inspect payload">
                          <Eye className="size-3.5" />
                        </Button>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>

            {meta && (
              <div className="flex items-center justify-between border-t border-border px-4 py-3 text-xs text-fg-muted tabular-nums">
                <span>
                  Page {meta.page} of {Math.max(meta.totalPages, 1)} ({meta.total} total)
                </span>
                <div className="flex items-center gap-2">
                  <Button variant="secondary" size="sm" disabled={meta.page <= 1} onClick={() => setPage(meta.page - 1)}>
                    <ChevronLeft className="size-3.5" />
                    Prev
                  </Button>
                  <Button variant="secondary" size="sm" disabled={!meta.hasNext} onClick={() => setPage(meta.page + 1)}>
                    Next
                    <ChevronRight className="size-3.5" />
                  </Button>
                </div>
              </div>
            )}
          </>
        )}
      </Card>

      <Drawer
        open={Boolean(selected)}
        onClose={() => setSelected(null)}
        title={selected ? selected.action : 'Audit event'}
        description={selected ? `${selected.actor} · ${formatDateTime(selected.at)}` : undefined}
      >
        {selected && (
          <div className="flex flex-col gap-4 text-xs">
            <div className="grid grid-cols-2 gap-2 rounded-lg border border-border bg-bg p-3">
              <div>
                <div className="text-[0.6875rem] text-fg-subtle uppercase">Target</div>
                <code className="font-medium text-fg">{selected.target}</code>
              </div>
              <div>
                <div className="text-[0.6875rem] text-fg-subtle uppercase">Client IP</div>
                <code className="text-fg-muted">{selected.clientIp || '—'}</code>
              </div>
              <div className="col-span-2">
                <div className="text-[0.6875rem] text-fg-subtle uppercase">Request ID</div>
                <code className="text-fg-muted select-all">{selected.requestId || '—'}</code>
              </div>
            </div>

            {selected.before && (
              <div>
                <div className="mb-1.5 text-[0.6875rem] font-semibold text-fg-subtle uppercase">Before</div>
                <pre className="max-h-60 overflow-auto rounded-lg border border-border bg-bg p-3 font-mono text-[0.72rem] text-fg">
                  {prettyJSON(selected.before)}
                </pre>
              </div>
            )}

            <div>
              <div className="mb-1.5 text-[0.6875rem] font-semibold text-fg-subtle uppercase">Payload (sanitized)</div>
              <pre className="max-h-72 overflow-auto rounded-lg border border-border bg-bg p-3 font-mono text-[0.72rem] text-fg">
                {prettyJSON(selected.after)}
              </pre>
            </div>
          </div>
        )}
      </Drawer>
    </div>
  )
}
