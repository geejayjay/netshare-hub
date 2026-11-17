import { useQuery } from '@tanstack/react-query'
import { Plus, Trash2 } from 'lucide-react'
import { useMemo, useState } from 'react'
import { notify } from '@/features/notifications'
import type { ListParams } from '@/shared/api'
import { DataTable, EmptyState, PageHeader, PermissionGate, SearchInput, type ColumnDef } from '@/shared/components'
import { formatDateTime } from '@/shared/lib/format'
import { useSession } from '@/shared/session/session'
import { Badge, Button, Card } from '@/shared/ui'
import { rolesQuery, useDeleteUser, usersQuery, type User } from '../api'
import { CreateUserDialog } from './CreateUserDialog'

interface UsersPageProps {
  /** Search, sort and page. The route keeps them in the URL. */
  params: ListParams
  onParamsChange: (params: ListParams) => void
}

export function UsersPage({ params, onParamsChange }: UsersPageProps) {
  const users = useQuery(usersQuery(params))
  const roles = useQuery(rolesQuery)
  const deleteUser = useDeleteUser()
  const session = useSession()
  const [creating, setCreating] = useState(false)

  const columns = useMemo<ColumnDef<User>[]>(() => {
    const roleName = (id: string) => roles.data?.items.find((r) => r.id === id)?.name ?? id
    return [
      {
        id: 'username',
        header: 'Username',
        enableSorting: true,
        cell: ({ row }) => <span className="font-medium">{row.original.username}</span>,
      },
      { id: 'displayName', header: 'Name', enableSorting: true, cell: ({ row }) => row.original.displayName || '—' },
      {
        id: 'role',
        header: 'Role',
        enableSorting: true,
        cell: ({ row }) => (
          <Badge tone={row.original.role === 'admin' ? 'info' : 'neutral'}>{roleName(row.original.role)}</Badge>
        ),
      },
      {
        id: 'status',
        header: 'Status',
        cell: ({ row }) =>
          row.original.disabled ? (
            <Badge tone="warning" dot>
              Disabled
            </Badge>
          ) : (
            <Badge tone="success" dot>
              Active
            </Badge>
          ),
      },
      {
        id: 'createdAt',
        header: 'Created',
        enableSorting: true,
        cell: ({ row }) => <span className="text-fg-muted">{formatDateTime(row.original.createdAt)}</span>,
      },
      {
        id: 'actions',
        header: '',
        cell: ({ row }) =>
          session.can('users:write') &&
          row.original.id !== session.user.id && (
            <Button
              variant="ghost"
              size="icon"
              aria-label={`Delete ${row.original.username}`}
              onClick={() => {
                if (!window.confirm(`Delete user "${row.original.username}"?`)) return
                deleteUser.mutate(row.original.id, {
                  onSuccess: () => notify.success('User deleted'),
                  onError: (error) => notify.error(error),
                })
              }}
            >
              <Trash2 className="size-4" />
            </Button>
          ),
      },
    ]
  }, [roles.data, session, deleteUser])

  return (
    <>
      <PageHeader
        title="Users"
        description="People who can log in to NetShare Hub."
        actions={
          <PermissionGate permission="users:write">
            <Button onClick={() => setCreating(true)}>
              <Plus className="size-4" />
              New user
            </Button>
          </PermissionGate>
        }
      />

      <Card>
        <div className="border-b border-border px-4 py-3">
          <SearchInput
            value={params.q ?? ''}
            onChange={(q) => onParamsChange({ ...params, q: q || undefined, page: undefined })}
            placeholder="Search users…"
          />
        </div>
        <DataTable
          columns={columns}
          data={users.data}
          isLoading={users.isFetching}
          error={users.error}
          onRetry={() => void users.refetch()}
          sort={params.sort}
          onSortChange={(sort) => onParamsChange({ ...params, sort, page: undefined })}
          onPageChange={(page) => onParamsChange({ ...params, page })}
          empty={<EmptyState title="No users found" description={params.q && 'Try a different search.'} />}
        />
      </Card>

      <CreateUserDialog open={creating} onClose={() => setCreating(false)} />
    </>
  )
}
