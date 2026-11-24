import { useQuery } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import {
  Activity,
  ArrowRight,
  Bell,
  FolderOpen,
  HardDrive,
  Monitor,
  Radio,
  Server,
  Shield,
  Users,
} from 'lucide-react'
import { activityQuery } from '@/features/activity'
import { alertsQuery } from '@/features/alerts'
import { mySharesQuery } from '@/features/portal'
import { guardSettingsQuery } from '@/features/security'
import { sessionsQuery } from '@/features/sessions'
import { discoveryQuery, sambaSettingsQuery } from '@/features/settings'
import { sharesQuery } from '@/features/shares'
import { api, unwrap } from '@/shared/api'
import { ErrorState, PageHeader } from '@/shared/components'
import { formatRelativeTime } from '@/shared/lib/format'
import { useSession } from '@/shared/session/session'
import { Badge, Card, CardContent, CardDescription, CardHeader, CardTitle, Spinner, StatCard } from '@/shared/ui'

const healthQuery = {
  queryKey: ['health'],
  queryFn: () => unwrap(api.GET('/health')),
}

export function DashboardPage() {
  const { user, can } = useSession()
  const health = useQuery(healthQuery)
  const myShares = useQuery(mySharesQuery)
  const allShares = useQuery({ ...sharesQuery({ pageSize: 100 }), enabled: can('shares:read') })
  const sessions = useQuery({ ...sessionsQuery, enabled: can('system:read') })
  const alerts = useQuery({ ...alertsQuery(true), enabled: can('alerts:manage') })
  const activity = useQuery({ ...activityQuery({ limit: 8 }), enabled: can('audit:read') })
  const samba = useQuery({ ...sambaSettingsQuery, enabled: can('settings:read') })
  const discovery = useQuery({ ...discoveryQuery, enabled: can('settings:read') })
  const guard = useQuery({ ...guardSettingsQuery, enabled: can('settings:read') })

  const activeAlerts = (alerts.data?.items ?? []).filter((a) => !a.acknowledgedAt)
  const activeSessions = sessions.data?.items ?? []
  const shareCount = can('shares:read')
    ? (allShares.data?.page?.total ?? allShares.data?.items.length ?? 0)
    : (myShares.data?.items.length ?? 0)

  return (
    <div className="flex flex-col gap-5">
      <PageHeader
        title={`Welcome, ${user.displayName || user.username}`}
        actions={
          <div className="flex flex-wrap items-center gap-2">
            {health.data && (
              <Badge tone="success" dot className="px-3 py-1">
                Online · v{health.data.version}
              </Badge>
            )}
            <Link
              to="/files"
              search={{ share: undefined, path: undefined }}
              className="inline-flex h-9 items-center gap-2 rounded-md bg-primary px-3.5 text-sm font-medium text-primary-fg shadow-xs transition-colors hover:bg-primary-hover"
            >
              <FolderOpen className="size-4" />
              File Manager
            </Link>
          </div>
        }
      />

      {health.error && <ErrorState error={health.error} onRetry={() => void health.refetch()} />}

      {/* KPI Summary Grid */}
      <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
        <StatCard
          label={can('shares:read') ? 'Configured Shares' : 'My Shares'}
          value={shareCount}
          detail={
            can('shares:read') ? (
              <Link to="/shares" className="inline-flex items-center gap-1 text-primary hover:underline">
                Manage shares <ArrowRight className="size-3" />
              </Link>
            ) : (
              <Link to="/my-shares" className="inline-flex items-center gap-1 text-primary hover:underline">
                Mount instructions <ArrowRight className="size-3" />
              </Link>
            )
          }
          icon={<HardDrive />}
          tone="info"
        />

        {can('system:read') && (
          <StatCard
            label="Active SMB Sessions"
            value={sessions.isLoading ? '—' : activeSessions.length}
            detail={
              <Link to="/sessions" className="inline-flex items-center gap-1 text-primary hover:underline">
                Inspect connections <ArrowRight className="size-3" />
              </Link>
            }
            icon={<Monitor />}
            tone="success"
          />
        )}

        {can('alerts:manage') && (
          <StatCard
            label="Unacknowledged Alerts"
            value={alerts.isLoading ? '—' : activeAlerts.length}
            detail={activeAlerts.length === 0 ? 'All systems nominal' : 'Review in notifications bell'}
            icon={<Bell />}
            tone={activeAlerts.length > 0 ? 'danger' : 'success'}
          />
        )}

        {can('settings:read') && (
          <StatCard
            label="Ransomware Guard"
            value={<span className="capitalize">{guard.data?.mode ?? '—'}</span>}
            detail={
              <Link to="/security" className="inline-flex items-center gap-1 text-primary hover:underline">
                Security settings <ArrowRight className="size-3" />
              </Link>
            }
            icon={<Shield />}
            tone={guard.data?.mode === 'protect' ? 'success' : guard.data?.mode === 'learning' ? 'warning' : 'neutral'}
          />
        )}
      </div>

      {/* Main Content Two-Column Grid */}
      <div className="grid gap-5 lg:grid-cols-3">
        {/* Left 2 columns: Recent Activity (or My Shares for plain users) */}
        <Card className="lg:col-span-2">
          <CardHeader className="flex-row items-center justify-between gap-2 pb-4">
            <CardTitle>{can('audit:read') ? 'Recent Activity' : 'Your Shares'}</CardTitle>
            {can('audit:read') && (
              <Link
                to="/activity"
                className="inline-flex items-center gap-1 text-xs font-medium text-primary hover:underline"
              >
                View all <ArrowRight className="size-3.5" />
              </Link>
            )}
          </CardHeader>
          <CardContent className="border-t border-border p-0">
            {can('audit:read') ? (
              activity.isLoading ? (
                <div className="flex justify-center py-10">
                  <Spinner />
                </div>
              ) : (activity.data?.items ?? []).length === 0 ? (
                <div className="flex flex-col items-center gap-1.5 py-10 text-center text-sm text-fg-muted">
                  <Activity className="size-5 text-fg-subtle" />
                  <span>No recent SMB activity recorded yet.</span>
                </div>
              ) : (
                <div className="divide-y divide-border">
                  {(activity.data?.items ?? []).map((ev) => (
                    <div key={ev.id} className="flex items-center justify-between gap-3 px-5 py-3 text-sm">
                      <div className="flex min-w-0 items-center gap-2.5">
                        <Badge tone={ev.ok ? 'info' : 'danger'}>{ev.op}</Badge>
                        <div className="min-w-0">
                          <div className="truncate font-mono text-xs text-fg">
                            {ev.share}/{ev.path || ''}
                          </div>
                          <div className="text-[0.7rem] text-fg-muted">
                            {ev.user || 'guest'} · {ev.clientIp}
                          </div>
                        </div>
                      </div>
                      <span className="shrink-0 text-xs text-fg-subtle">{formatRelativeTime(ev.at)}</span>
                    </div>
                  ))}
                </div>
              )
            ) : (myShares.data?.items ?? []).length === 0 ? (
              <div className="p-8 text-center text-sm text-fg-muted">No shares assigned to your account yet.</div>
            ) : (
              <div className="divide-y divide-border">
                {(myShares.data?.items ?? []).map((sh) => (
                  <div key={sh.id} className="flex items-center justify-between gap-3 px-5 py-3.5">
                    <div className="flex items-center gap-3">
                      <span className="grid size-8 place-items-center rounded-lg bg-primary-soft text-primary">
                        <HardDrive className="size-4" />
                      </span>
                      <div>
                        <div className="text-sm font-medium text-fg">{sh.name}</div>
                        <div className="text-xs text-fg-muted">{sh.comment || 'SMB network share'}</div>
                      </div>
                    </div>
                    <div className="flex items-center gap-2">
                      <Badge tone={sh.level === 'write' ? 'info' : 'neutral'}>{sh.level}</Badge>
                      <Link
                        to="/files"
                        search={{ share: sh.name, path: '' }}
                        className="text-xs font-medium text-primary hover:underline"
                      >
                        Browse
                      </Link>
                    </div>
                  </div>
                ))}
              </div>
            )}
          </CardContent>
        </Card>

        {/* Right column: System Status & Quick Links */}
        <div className="flex flex-col gap-5">
          {can('settings:read') && (
            <Card>
              <CardHeader className="pb-3">
                <CardTitle className="flex items-center gap-2">
                  <Server className="size-4 text-primary" />
                  Server & Discovery
                </CardTitle>
              </CardHeader>
              <CardContent className="flex flex-col gap-2.5 border-t border-border pt-4 text-xs">
                <div className="flex items-center justify-between">
                  <span className="text-fg-muted">SMB Encryption</span>
                  <Badge tone={samba.data?.encryption === 'required' ? 'success' : 'neutral'}>
                    {samba.data?.encryption ?? 'default'}
                  </Badge>
                </div>
                <div className="flex items-center justify-between">
                  <span className="text-fg-muted">macOS Extensions</span>
                  <Badge tone={samba.data?.macSupport ? 'success' : 'neutral'}>
                    {samba.data?.macSupport ? 'Enabled' : 'Off'}
                  </Badge>
                </div>
                <div className="flex items-center justify-between">
                  <span className="text-fg-muted">mDNS / Bonjour</span>
                  <Badge tone={discovery.data?.mdns.running ? 'success' : 'neutral'} dot>
                    {discovery.data?.mdns.running ? 'Running' : 'Stopped'}
                  </Badge>
                </div>
                <div className="flex items-center justify-between">
                  <span className="text-fg-muted">WS-Discovery</span>
                  <Badge tone={discovery.data?.wsd.running ? 'success' : 'neutral'} dot>
                    {discovery.data?.wsd.running ? 'Running' : 'Stopped'}
                  </Badge>
                </div>
                <Link
                  to="/settings"
                  className="mt-2 inline-flex items-center gap-1.5 text-xs font-medium text-primary hover:underline"
                >
                  <Radio className="size-3.5" />
                  Configure Samba & Discovery
                </Link>
              </CardContent>
            </Card>
          )}

          <Card>
            <CardHeader className="pb-3">
              <CardTitle className="flex items-center gap-2">
                <Users className="size-4 text-primary" />
                Account & Role
              </CardTitle>
              <CardDescription>
                Signed in as <span className="font-medium text-fg">{user.username}</span> ({user.role}).
              </CardDescription>
            </CardHeader>
            <CardContent className="border-t border-border pt-4">
              <div className="flex flex-wrap gap-1.5">
                {can('shares:read') && (
                  <Link
                    to="/shares"
                    className="rounded-md border border-border bg-bg px-2.5 py-1.5 text-xs font-medium text-fg transition-colors hover:border-border-strong hover:bg-surface-2"
                  >
                    Manage Shares
                  </Link>
                )}
                {can('users:read') && (
                  <Link
                    to="/users"
                    className="rounded-md border border-border bg-bg px-2.5 py-1.5 text-xs font-medium text-fg transition-colors hover:border-border-strong hover:bg-surface-2"
                  >
                    Manage Users
                  </Link>
                )}
                <Link
                  to="/my-shares"
                  className="rounded-md border border-border bg-bg px-2.5 py-1.5 text-xs font-medium text-fg transition-colors hover:border-border-strong hover:bg-surface-2"
                >
                  Connect Guide
                </Link>
              </div>
            </CardContent>
          </Card>
        </div>
      </div>
    </div>
  )
}
