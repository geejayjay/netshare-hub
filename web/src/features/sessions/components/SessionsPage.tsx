import { useQuery } from '@tanstack/react-query'
import { FileText, Lock, Monitor, RefreshCw, ShieldCheck, Unplug } from 'lucide-react'
import { useMemo, useState } from 'react'
import { notify } from '@/features/notifications'
import { EmptyState, ErrorState, PageHeader, PermissionGate, SearchInput } from '@/shared/components'
import { cn } from '@/shared/lib/cn'
import { formatRelativeTime } from '@/shared/lib/format'
import { Badge, Button, Card, Spinner, StatCard } from '@/shared/ui'
import { sessionsQuery, useDisconnectSession } from '../api'

export function SessionsPage() {
  const sessions = useQuery(sessionsQuery)
  const disconnect = useDisconnectSession()
  const [query, setQuery] = useState('')

  const items = useMemo(() => sessions.data?.items ?? [], [sessions.data])
  const filtered = useMemo(() => {
    const q = query.trim().toLowerCase()
    if (!q) return items
    return items.filter(
      (s) =>
        s.username.toLowerCase().includes(q) ||
        s.clientIp.toLowerCase().includes(q) ||
        (s.machine ?? '').toLowerCase().includes(q) ||
        s.shares.some((sh) => sh.toLowerCase().includes(q)),
    )
  }, [items, query])

  const encryptedCount = items.filter((s) => s.encrypted).length
  const totalOpenFiles = items.reduce((sum, s) => sum + s.openFiles, 0)

  return (
    <div className="flex flex-col gap-5">
      <PageHeader
        title="Active Sessions"
        info="Live Samba client connections (auto-refreshes every 10s). Disconnecting terminates only that SMB session; permitted clients can reconnect."
        actions={
          <Button variant="secondary" size="sm" onClick={() => void sessions.refetch()}>
            <RefreshCw className={cn('size-3.5', sessions.isFetching && 'animate-spin')} />
            Refresh
          </Button>
        }
      />

      <div className="grid gap-4 sm:grid-cols-3">
        <StatCard label="Connected clients" value={items.length} icon={<Monitor />} tone="info" />
        <StatCard label="Encrypted" value={encryptedCount} icon={<Lock />} tone="success" />
        <StatCard label="Open files" value={totalOpenFiles} icon={<FileText />} tone="warning" />
      </div>

      <Card>
        <div className="flex flex-wrap items-center justify-between gap-3 border-b border-border px-4 py-3">
          <SearchInput value={query} onChange={setQuery} placeholder="Filter by user, IP, host, or share…" />
          <span className="text-xs text-fg-subtle tabular-nums">
            {filtered.length} of {items.length} shown
          </span>
        </div>

        {sessions.isLoading ? (
          <div className="flex justify-center py-14">
            <Spinner />
          </div>
        ) : sessions.error ? (
          <div className="p-6">
            <ErrorState error={sessions.error} onRetry={() => void sessions.refetch()} />
          </div>
        ) : filtered.length === 0 ? (
          <div className="p-8">
            <EmptyState
              title={items.length === 0 ? 'No active SMB connections' : 'No matching sessions'}
              description={
                items.length === 0
                  ? 'Clients connected via Windows, macOS, or Linux will appear here live.'
                  : 'Try adjusting your search query.'
              }
            />
          </div>
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full border-collapse text-sm">
              <thead className="border-b border-border bg-surface-2/50 text-left text-[0.6875rem] font-semibold tracking-wider text-fg-subtle uppercase">
                <tr>
                  <th className="px-4 py-2.5">User</th>
                  <th className="px-4 py-2.5">Client</th>
                  <th className="px-4 py-2.5">Mounted shares</th>
                  <th className="px-4 py-2.5">Protocol & security</th>
                  <th className="px-4 py-2.5">Open files</th>
                  <th className="px-4 py-2.5">Connected</th>
                  <th className="px-4 py-2.5 text-right">Actions</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-border">
                {filtered.map((s) => (
                  <tr key={s.id} className="transition-colors hover:bg-surface-2/40">
                    <td className="px-4 py-3 font-medium text-fg">{s.username || 'guest'}</td>
                    <td className="px-4 py-3 text-xs tabular-nums">
                      <div className="font-mono text-fg">
                        {s.clientIp}
                        {s.clientPort ? `:${s.clientPort}` : ''}
                      </div>
                      {s.machine && s.machine !== s.clientIp && (
                        <div className="text-[0.6875rem] text-fg-subtle">{s.machine}</div>
                      )}
                    </td>
                    <td className="px-4 py-3">
                      <div className="flex flex-wrap gap-1">
                        {s.shares.length === 0 ? (
                          <span className="text-xs text-fg-subtle">IPC$</span>
                        ) : (
                          s.shares.map((sh) => (
                            <Badge key={sh} tone="neutral">
                              {sh}
                            </Badge>
                          ))
                        )}
                      </div>
                    </td>
                    <td className="px-4 py-3">
                      <div className="flex flex-wrap items-center gap-1.5">
                        <Badge tone="info">{s.dialect || 'SMB'}</Badge>
                        {s.encrypted && (
                          <Badge tone="success">
                            <Lock className="size-3" /> Encrypted
                          </Badge>
                        )}
                        {s.signed && !s.encrypted && (
                          <Badge tone="neutral">
                            <ShieldCheck className="size-3" /> Signed
                          </Badge>
                        )}
                      </div>
                    </td>
                    <td className="px-4 py-3 text-xs text-fg-muted tabular-nums">{s.openFiles}</td>
                    <td className="px-4 py-3 text-xs text-fg-muted">{formatRelativeTime(s.connectedAt ?? '')}</td>
                    <td className="px-4 py-3 text-right">
                      <PermissionGate permission="sessions:manage">
                        <Button
                          variant="ghost"
                          size="sm"
                          className="h-7 px-2.5 text-xs text-danger hover:bg-danger-soft"
                          loading={disconnect.isPending && disconnect.variables === s.id}
                          onClick={() => {
                            if (!window.confirm(`Disconnect ${s.username || 'client'} (${s.clientIp})?`)) return
                            disconnect.mutate(s.id, {
                              onSuccess: () => notify.success('Session disconnected', `${s.username} (${s.clientIp})`),
                              onError: (err) => notify.error(err),
                            })
                          }}
                        >
                          <Unplug className="size-3.5" />
                          Disconnect
                        </Button>
                      </PermissionGate>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </Card>
    </div>
  )
}
