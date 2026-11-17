import { useQuery } from '@tanstack/react-query'
import { api, unwrap } from '@/shared/api'
import { ErrorState, PageHeader } from '@/shared/components'
import { useSession } from '@/shared/session/session'
import { Badge, Card, CardContent, CardDescription, CardHeader, CardTitle, Spinner } from '@/shared/ui'

const healthQuery = {
  queryKey: ['health'],
  queryFn: () => unwrap(api.GET('/health')),
}

/** Placeholder dashboard. Real widgets (disk usage, sessions, alerts) arrive with their features. */
export function DashboardPage() {
  const { user, permissions } = useSession()
  const health = useQuery(healthQuery)

  return (
    <>
      <PageHeader title={`Welcome, ${user.displayName || user.username}`} description="NetShare Hub is up and running." />

      <div className="grid gap-4 md:grid-cols-2">
        <Card>
          <CardHeader>
            <CardTitle>Server</CardTitle>
            <CardDescription>Status of the NetShare Hub service.</CardDescription>
          </CardHeader>
          <CardContent>
            {health.error ? (
              <ErrorState error={health.error} onRetry={() => void health.refetch()} />
            ) : health.data ? (
              <div className="flex items-center gap-2">
                <Badge tone="success" dot>
                  {health.data.status === 'ok' ? 'Running' : health.data.status}
                </Badge>
                <span className="text-sm text-fg-muted">Version {health.data.version}</span>
              </div>
            ) : (
              <Spinner />
            )}
          </CardContent>
        </Card>

        <Card>
          <CardHeader>
            <CardTitle>Your access</CardTitle>
            <CardDescription>
              Signed in as <span className="font-medium text-fg">{user.username}</span> ({user.role}).
            </CardDescription>
          </CardHeader>
          <CardContent className="flex flex-wrap gap-1.5">
            {permissions.map((p) => (
              <Badge key={p}>{p}</Badge>
            ))}
          </CardContent>
        </Card>
      </div>
    </>
  )
}
