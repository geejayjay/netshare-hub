import { createFileRoute, useNavigate } from '@tanstack/react-router'
import { UsersPage } from '@/features/users'
import { parseListParams, type ListParams } from '@/shared/api'
import { NoAccessState, PermissionGate } from '@/shared/components'

export const Route = createFileRoute('/_app/users')({
  // Search, sort and page live in the URL, so a view can be bookmarked or shared.
  validateSearch: (search): ListParams => parseListParams(search),
  component: UsersRoute,
})

function UsersRoute() {
  const params = Route.useSearch()
  const navigate = useNavigate({ from: Route.fullPath })

  return (
    <PermissionGate permission="users:read" fallback={<NoAccessState />}>
      <UsersPage params={params} onParamsChange={(search) => void navigate({ search, replace: true })} />
    </PermissionGate>
  )
}
