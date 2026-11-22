import { useQuery } from '@tanstack/react-query'
import { Plus, Trash2 } from 'lucide-react'
import { useMemo, useState } from 'react'
import { notify } from '@/features/notifications'
import type { ListParams } from '@/shared/api'
import { DataTable, EmptyState, PageHeader, PermissionGate, SearchInput, type ColumnDef } from '@/shared/components'
import { formatDateTime } from '@/shared/lib/format'
import { useSession } from '@/shared/session/session'
import { Badge, Button, Card } from '@/shared/ui'
import { sharesQuery, useDeleteShare, type Share } from '../api'
import { CreateShareDialog } from './CreateShareDialog'

interface SharesPageProps {
  params: ListParams
  onParamsChange: (params: ListParams) => void
}

export function SharesPage({ params, onParamsChange }: SharesPageProps) {
  const shares = useQuery(sharesQuery(params))
  const deleteShare = useDeleteShare()
  const session = useSession()
  const [creating, setCreating] = useState(false)

  const columns = useMemo<ColumnDef<Share>[]>(
    () => [
      {
        id: 'name',
        header: 'Name',
        enableSorting: true,
        cell: ({ row }) => <span className="font-medium">{row.original.name}</span>,
      },
      {
        id: 'path',
        header: 'Path',
        enableSorting: true,
        cell: ({ row }) => <code className="text-xs text-fg-muted">{row.original.path}</code>,
      },
      {
        id: 'access',
        header: 'Access',
        cell: ({ row }) =>
          row.original.access.length === 0 ? (
            <span className="text-sm text-fg-muted">No users</span>
          ) : (
            <div className="flex flex-wrap gap-1">
              {row.original.access.map((a) => (
                <Badge key={a.userId} tone={a.level === 'write' ? 'info' : 'neutral'}>
                  {a.username} ({a.level})
                </Badge>
              ))}
            </div>
          ),
      },
      {
        id: 'comment',
        header: 'Description',
        cell: ({ row }) => row.original.comment || '—',
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
          session.can('shares:write') && (
            <Button
              variant="ghost"
              size="icon"
              aria-label={`Delete ${row.original.name}`}
              onClick={() => {
                if (!window.confirm(`Stop sharing "${row.original.name}"? Files on disk are kept.`)) return
                deleteShare.mutate(row.original.id, {
                  onSuccess: () => notify.success('Share removed'),
                  onError: (error) => notify.error(error),
                })
              }}
            >
              <Trash2 className="size-4" />
            </Button>
          ),
      },
    ],
    [session, deleteShare],
  )

  return (
    <>
      <PageHeader
        title="Shares"
        description="Folders shared over SMB."
        actions={
          <PermissionGate permission="shares:write">
            <Button onClick={() => setCreating(true)}>
              <Plus className="size-4" />
              New share
            </Button>
          </PermissionGate>
        }
      />

      <Card>
        <div className="border-b border-border px-4 py-3">
          <SearchInput
            value={params.q ?? ''}
            onChange={(q) => onParamsChange({ ...params, q: q || undefined, page: undefined })}
            placeholder="Search shares…"
          />
        </div>
        <DataTable
          columns={columns}
          data={shares.data}
          isLoading={shares.isFetching}
          error={shares.error}
          onRetry={() => void shares.refetch()}
          sort={params.sort}
          onSortChange={(sort) => onParamsChange({ ...params, sort, page: undefined })}
          onPageChange={(page) => onParamsChange({ ...params, page })}
          empty={<EmptyState title="No shares yet" description={params.q && 'Try a different search.'} />}
        />
      </Card>

      <CreateShareDialog open={creating} onClose={() => setCreating(false)} />
    </>
  )
}
