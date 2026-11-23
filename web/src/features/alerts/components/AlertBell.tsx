import { useQuery, useQueryClient } from '@tanstack/react-query'
import { Bell, BellOff, Check, Trash2 } from 'lucide-react'
import { useCallback, useState } from 'react'
import { notify } from '@/features/notifications'
import { useSSE } from '@/shared/api'
import { EmptyState, ErrorState } from '@/shared/components'
import { formatRelativeTime } from '@/shared/lib/format'
import { useSession } from '@/shared/session/session'
import { Badge, Button, Drawer, Spinner, Tabs, type Tone } from '@/shared/ui'
import { alertsQuery, useAcknowledgeAlert, useDeleteAlert, type ServerAlert } from '../api'

const MUTE_KEY = 'netshare.alerts.muted'

function severityTone(severity: string): Tone {
  switch (severity) {
    case 'critical':
      return 'danger'
    case 'warning':
      return 'warning'
    default:
      return 'info'
  }
}

export function AlertBell() {
  const { can } = useSession()
  const qc = useQueryClient()
  const [open, setOpen] = useState(false)
  const [filter, setFilter] = useState<'active' | 'all'>('active')
  const [muted, setMuted] = useState(() => {
    try {
      return localStorage.getItem(MUTE_KEY) === '1'
    } catch {
      return false
    }
  })

  const allowed = can('alerts:manage')
  const activeAlerts = useQuery({ ...alertsQuery(true), enabled: allowed })
  const allAlerts = useQuery({ ...alertsQuery(undefined), enabled: allowed && open && filter === 'all' })
  const ack = useAcknowledgeAlert()
  const del = useDeleteAlert()

  const onAlertEvent = useCallback(
    (incoming: ServerAlert) => {
      void qc.invalidateQueries({ queryKey: ['alerts'] })
      if (!muted && incoming.active && !incoming.acknowledgedAt) {
        if (incoming.severity === 'critical') {
          notify.error(new Error(`${incoming.title}: ${incoming.message}`))
        } else {
          notify.info(incoming.title, incoming.message)
        }
      }
    },
    [qc, muted],
  )

  useSSE<ServerAlert>('alert', onAlertEvent, allowed)

  if (!allowed) return null

  const unackedCount = (activeAlerts.data?.items ?? []).filter((a) => !a.acknowledgedAt).length
  const list = filter === 'active' ? activeAlerts : allAlerts
  const items = list.data?.items ?? []

  function toggleMute() {
    const next = !muted
    setMuted(next)
    try {
      localStorage.setItem(MUTE_KEY, next ? '1' : '0')
    } catch {
      // ignore storage errors
    }
  }

  return (
    <>
      <Button
        variant="ghost"
        size="icon"
        className="relative size-8"
        aria-label="Notifications"
        onClick={() => setOpen(true)}
      >
        {muted ? <BellOff className="size-4 text-fg-subtle" /> : <Bell className="size-4" />}
        {unackedCount > 0 && (
          <span className="absolute -top-0.5 -right-0.5 flex h-4 min-w-4 items-center justify-center rounded-full bg-danger px-1 text-[0.625rem] font-semibold text-danger-fg tabular-nums">
            {unackedCount > 99 ? '99+' : unackedCount}
          </span>
        )}
      </Button>

      <Drawer
        open={open}
        onClose={() => setOpen(false)}
        title="Notifications"
        description="Live server alerts and security warnings."
        headerExtra={
          <Button
            variant="ghost"
            size="sm"
            onClick={toggleMute}
            title={muted ? 'Unmute live popups' : 'Mute live popups'}
          >
            {muted ? <BellOff className="size-3.5" /> : <Bell className="size-3.5" />}
            {muted ? 'Muted' : 'Live'}
          </Button>
        }
      >
        <div className="mb-4 flex items-center justify-between gap-2">
          <Tabs
            value={filter}
            onChange={setFilter}
            items={[
              { id: 'active', label: 'Active', badge: activeAlerts.data?.items.length ?? 0 },
              { id: 'all', label: 'History' },
            ]}
          />
        </div>

        {list.isLoading ? (
          <div className="flex justify-center py-12">
            <Spinner />
          </div>
        ) : list.error ? (
          <ErrorState error={list.error} onRetry={() => void list.refetch()} />
        ) : items.length === 0 ? (
          <EmptyState
            title={filter === 'active' ? 'All clear' : 'No alert history'}
            description={filter === 'active' ? 'No active server alerts right now.' : 'Past alerts will appear here.'}
          />
        ) : (
          <div className="flex flex-col gap-3">
            {items.map((alert) => (
              <div
                key={alert.id}
                className="flex flex-col gap-2 rounded-lg border border-border bg-bg p-3.5 transition-colors hover:border-border-strong"
              >
                <div className="flex items-start justify-between gap-2">
                  <div className="flex flex-wrap items-center gap-1.5">
                    <Badge tone={severityTone(alert.severity)} dot>
                      {alert.severity}
                    </Badge>
                    {alert.count > 1 && (
                      <Badge tone="neutral" className="tabular-nums">
                        ×{alert.count}
                      </Badge>
                    )}
                    {alert.acknowledgedAt && <Badge tone="success">Ack</Badge>}
                  </div>
                  <span className="shrink-0 text-[0.7rem] text-fg-subtle">{formatRelativeTime(alert.lastAt)}</span>
                </div>

                <div>
                  <div className="text-sm font-medium text-fg">{alert.title}</div>
                  <p className="mt-0.5 text-xs leading-relaxed text-fg-muted">{alert.message}</p>
                </div>

                <div className="flex items-center justify-between gap-2 pt-1">
                  <span className="text-[0.6875rem] text-fg-subtle">{alert.source}</span>
                  <div className="flex items-center gap-1">
                    {!alert.acknowledgedAt && alert.active && (
                      <Button
                        variant="secondary"
                        size="sm"
                        className="h-7 px-2.5 text-xs"
                        loading={ack.isPending}
                        onClick={() =>
                          ack.mutate(alert.id, {
                            onSuccess: () => notify.success('Alert acknowledged'),
                            onError: (err) => notify.error(err),
                          })
                        }
                      >
                        <Check className="size-3.5" />
                        Acknowledge
                      </Button>
                    )}
                    <Button
                      variant="ghost"
                      size="icon"
                      className="size-7 text-fg-subtle hover:text-danger"
                      aria-label="Delete alert"
                      onClick={() =>
                        del.mutate(alert.id, {
                          onError: (err) => notify.error(err),
                        })
                      }
                    >
                      <Trash2 className="size-3.5" />
                    </Button>
                  </div>
                </div>
              </div>
            ))}
          </div>
        )}
      </Drawer>
    </>
  )
}
