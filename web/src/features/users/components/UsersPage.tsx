import { useQuery } from '@tanstack/react-query'
import { Plus, Shield, Trash2, UserCheck, Users as UsersIcon } from 'lucide-react'
import { useMemo, useState } from 'react'
import { notify } from '@/features/notifications'
import type { ListParams } from '@/shared/api'
import { DataTable, EmptyState, PageHeader, PermissionGate, SearchInput, type ColumnDef } from '@/shared/components'
import { formatDateTime } from '@/shared/lib/format'
import { useSession } from '@/shared/session/session'
import { Badge, Button, Card, StatCard } from '@/shared/ui'
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

  const items = users.data?.items ?? []
  const activeCount = items.filter((u) => !u.disabled).length
  const adminCount = items.filter((u) => u.role === 'admin').length

  const columns = useMemo<ColumnDef<User>[]>(() => {
    const roleName = (id: string) => roles.data?.items.find((r) => r.id === id)?.name ?? id
    return [
      {
        id: 'username',
        header: 'User',
        enableSorting: true,
        cell: ({ row }) => (
          <div className="flex flex-col">
            <span className="font-medium text-fg">{row.original.username}</span>
            {row.original.displayName && <span className="text-xs text-fg-muted">{row.original.displayName}</span>}
          </div>
        ),
      },
      {
        id: 'role',
        header: 'Role',
        enableSorting: true,
        cell: ({ row }) => (
          <Badge tone={row.original.role === 'admin' ? 'info' : 'neutral'}>{roleName(row.original.role)}</Badge>
        ),
      },
      {
        id: 'smbAccess',
        header: 'SMB Access',
        cell: ({ row }) =>
          row.original.smbAccess ? (
            <Badge tone="success">Enabled</Badge>
          ) : (
            <Badge tone="neutral">Web only</Badge>
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
        cell: ({ row }) => <span className="text-xs text-fg-muted">{formatDateTime(row.original.createdAt)}</span>,
      },
      {
        id: 'actions',
        header: '',
        cell: ({ row }) =>
          session.can('users:write') &&
          row.original.id !== session.user.id && (
            <div className="flex justify-end">
              <Button
                variant="ghost"
                size="icon"
                className="size-8 text-fg-muted hover:text-danger"
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
            </div>
          ),
      },
    ]
  }, [roles.data, session, deleteUser])

  return (
    <div className="flex flex-col gap-6">
      <PageHeader
        title="Users"
        description="Manage accounts, roles, and SMB network access credentials."
        actions={
          <PermissionGate permission="users:write">
            <Button onClick={() => setCreating(true)}>
              <Plus className="size-4" />
              New user
            </Button>
          </PermissionGate>
        }
      />

      <div className="grid gap-4 sm:grid-cols-3">
        <StatCard
          label="Total users"
          value={users.data?.page?.total ?? items.length}
          detail="Registered accounts"
          icon={<UsersIcon />}
          tone="info"
        />
        <StatCard
          label="Active accounts"
          value={activeCount}
          detail="Enabled on this page"
          icon={<UserCheck />}
          tone="success"
        />
        <StatCard
          label="Administrators"
          value={adminCount}
          detail="Full server access"
          icon={<Shield />}
          tone="warning"
        />
      </div>

      <Card>
        <div className="flex flex-wrap items-center justify-between gap-3 border-b border-border px-4 py-3">
          <SearchInput
            value={params.q ?? ''}
            onChange={(q) => onParamsChange({ ...params, q: q || undefined, page: undefined })}
            placeholder="Search users…"
          />
          <span className="text-xs text-fg-subtle tabular-nums">
            {users.data?.page?.total ?? items.length} accounts
          </span>
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
    </div>
  )
}
