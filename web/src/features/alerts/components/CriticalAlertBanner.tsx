import { useQuery } from '@tanstack/react-query'
import { AlertTriangle, Check } from 'lucide-react'
import { notify } from '@/features/notifications'
import { useSession } from '@/shared/session/session'
import { Button } from '@/shared/ui'
import { alertsQuery, useAcknowledgeAlert } from '../api'

/**
 * Sticky banner rendered at the top of the main workspace when there are
 * unacknowledged critical alerts.
 */
export function CriticalAlertBanner() {
  const { can } = useSession()
  const allowed = can('alerts:manage')
  const activeAlerts = useQuery({ ...alertsQuery(true), enabled: allowed })
  const ack = useAcknowledgeAlert()

  if (!allowed) return null

  const critical = (activeAlerts.data?.items ?? []).filter(
    (a) => a.active && a.severity === 'critical' && !a.acknowledgedAt,
  )
  const first = critical[0]
  if (!first) return null

  return (
    <div
      role="alert"
      className="flex flex-wrap items-center justify-between gap-3 rounded-xl border border-danger/30 bg-danger-soft px-4 py-3 text-sm text-danger"
    >
      <div className="flex min-w-0 items-center gap-2.5">
        <AlertTriangle className="size-4 shrink-0" />
        <div className="min-w-0">
          <span className="font-semibold">{first.title}</span>
          <span className="mx-1.5 opacity-50">·</span>
          <span className="text-fg">{first.message}</span>
          {critical.length > 1 && (
            <span className="ml-2 text-xs font-medium opacity-80">+{critical.length - 1} more critical</span>
          )}
        </div>
      </div>
      <Button
        variant="secondary"
        size="sm"
        className="h-7 shrink-0 text-xs"
        loading={ack.isPending}
        onClick={() =>
          ack.mutate(first.id, {
            onSuccess: () => notify.success('Alert acknowledged'),
            onError: (err) => notify.error(err),
          })
        }
      >
        <Check className="size-3.5" />
        Acknowledge
      </Button>
    </div>
  )
}
