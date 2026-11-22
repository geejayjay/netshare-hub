import { createFileRoute, useNavigate } from '@tanstack/react-router'
import { SharesPage } from '@/features/shares'
import { parseListParams, type ListParams } from '@/shared/api'
import { NoAccessState, PermissionGate } from '@/shared/components'

export const Route = createFileRoute('/_app/shares')({
  validateSearch: (search): ListParams => parseListParams(search),
  component: SharesRoute,
})

function SharesRoute() {
  const params = Route.useSearch()
  const navigate = useNavigate({ from: Route.fullPath })

  return (
    <PermissionGate permission="shares:read" fallback={<NoAccessState />}>
      <SharesPage params={params} onParamsChange={(search) => void navigate({ search, replace: true })} />
    </PermissionGate>
  )
}
