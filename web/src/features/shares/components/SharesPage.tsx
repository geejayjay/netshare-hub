import { useQuery } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import { FolderOpen, Globe, HardDrive, Lock, Pencil, Plus, Trash2, Wrench } from 'lucide-react'
import { useMemo, useState } from 'react'
import { notify } from '@/features/notifications'
import type { ListParams } from '@/shared/api'
import { DataTable, EmptyState, PageHeader, PermissionGate, SearchInput, type ColumnDef } from '@/shared/components'
import { formatDateTime } from '@/shared/lib/format'
import { useSession } from '@/shared/session/session'
import { Badge, Button, Card, StatCard } from '@/shared/ui'
import { sharesQuery, useDeleteShare, useFixSharePermissions, type Share } from '../api'
import { ShareEditorDialog } from './ShareEditorDialog'

interface SharesPageProps {
  params: ListParams
  onParamsChange: (params: ListParams) => void
}

export function SharesPage({ params, onParamsChange }: SharesPageProps) {
  const shares = useQuery(sharesQuery(params))
  const deleteShare = useDeleteShare()
  const fixPerms = useFixSharePermissions()
  const session = useSession()
  const [editorOpen, setEditorOpen] = useState(false)
  const [editing, setEditing] = useState<Share | null>(null)

  const items = shares.data?.items ?? []
  const tmCount = items.filter((s) => s.timeMachine).length
  const restrictedCount = items.filter((s) => s.networkRules.length > 0).length

  const columns = useMemo<ColumnDef<Share>[]>(
    () => [
      {
        id: 'name',
        header: 'Share',
        enableSorting: true,
        cell: ({ row }) => {
          const s = row.original
          return (
            <div className="flex flex-col gap-0.5 py-0.5">
              <div className="flex flex-wrap items-center gap-1.5">
                <span className="font-medium text-fg">{s.name}</span>
                {s.timeMachine && <Badge tone="info">Time Machine</Badge>}
                {s.readOnly && <Badge tone="warning">Read-only</Badge>}
                {s.guestAccess && <Badge tone="success">Guest</Badge>}
              </div>
              <code className="text-xs text-fg-subtle">{s.path}</code>
            </div>
          )
        },
      },
      {
        id: 'preset',
        header: 'Preset',
        cell: ({ row }) => (
          <Badge tone="neutral" className="capitalize">
            {row.original.preset || 'custom'}
          </Badge>
        ),
      },
      {
        id: 'access',
        header: 'Access',
        cell: ({ row }) => {
          const s = row.original
          if (s.access.length === 0) {
            return <span className="text-xs text-fg-muted">{s.guestAccess ? 'Guest only' : 'No users'}</span>
          }
          return (
            <div className="flex flex-wrap gap-1">
              {s.access.slice(0, 4).map((a) => (
                <Badge key={a.userId} tone={a.level === 'write' ? 'info' : a.level === 'upload' ? 'warning' : 'neutral'}>
                  {a.username} · {a.level}
                </Badge>
              ))}
              {s.access.length > 4 && <Badge tone="neutral">+{s.access.length - 4}</Badge>}
            </div>
          )
        },
      },
      {
        id: 'network',
        header: 'Network',
        cell: ({ row }) =>
          row.original.networkRules.length === 0 ? (
            <span className="text-xs text-fg-subtle">Any host</span>
          ) : (
            <Badge tone="info" className="tabular-nums">
              <Globe className="size-3" />
              {row.original.networkRules.length} {row.original.networkRules.length === 1 ? 'rule' : 'rules'}
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
        cell: ({ row }) => {
          const s = row.original
          return (
            <div className="flex items-center justify-end gap-1">
              <Link
                to="/files"
                search={{ share: s.name, path: '' }}
                className="inline-flex size-8 items-center justify-center rounded-md text-fg-muted transition-colors hover:bg-surface-2 hover:text-fg"
                title={`Browse ${s.name}`}
                aria-label={`Browse ${s.name}`}
              >
                <FolderOpen className="size-4" />
              </Link>
              {session.can('shares:write') && (
                <>
                  <Button
                    variant="ghost"
                    size="icon"
                    className="size-8"
                    title={`Edit ${s.name}`}
                    aria-label={`Edit ${s.name}`}
                    onClick={() => {
                      setEditing(s)
                      setEditorOpen(true)
                    }}
                  >
                    <Pencil className="size-4" />
                  </Button>
                  <Button
                    variant="ghost"
                    size="icon"
                    className="size-8"
                    title="Re-apply folder permissions"
                    aria-label={`Fix permissions for ${s.name}`}
                    onClick={() => {
                      fixPerms.mutate(s.id, {
                        onSuccess: () => notify.success('Permissions repair queued', `Fixing ACLs on "${s.name}".`),
                        onError: (err) => notify.error(err),
                      })
                    }}
                  >
                    <Wrench className="size-4" />
                  </Button>
                  <Button
                    variant="ghost"
                    size="icon"
                    className="size-8 text-fg-muted hover:text-danger"
                    title={`Delete ${s.name}`}
                    aria-label={`Delete ${s.name}`}
                    onClick={() => {
                      if (!window.confirm(`Stop sharing "${s.name}"? Files on disk are kept.`)) return
                      deleteShare.mutate(s.id, {
                        onSuccess: () => notify.success('Share removed'),
                        onError: (error) => notify.error(error),
                      })
                    }}
                  >
                    <Trash2 className="size-4" />
                  </Button>
                </>
              )}
            </div>
          )
        },
      },
    ],
    [session, deleteShare, fixPerms],
  )

  return (
    <div className="flex flex-col gap-6">
      <PageHeader
        title="Shares"
        description="Manage SMB folders, access presets, Time Machine targets, and per-share firewall rules."
        actions={
          <PermissionGate permission="shares:write">
            <Button
              onClick={() => {
                setEditing(null)
                setEditorOpen(true)
              }}
            >
              <Plus className="size-4" />
              New share
            </Button>
          </PermissionGate>
        }
      />

      <div className="grid gap-4 sm:grid-cols-3">
        <StatCard
          label="Total shares"
          value={shares.data?.page?.total ?? items.length}
          detail="Active SMB definitions"
          icon={<FolderOpen />}
          tone="info"
        />
        <StatCard
          label="Time Machine"
          value={tmCount}
          detail="macOS backup targets on this page"
          icon={<HardDrive />}
          tone="success"
        />
        <StatCard
          label="Network-restricted"
          value={restrictedCount}
          detail="Shares with IP/CIDR allow or block rules"
          icon={<Lock />}
          tone="warning"
        />
      </div>

      <Card>
        <div className="flex flex-wrap items-center justify-between gap-3 border-b border-border px-4 py-3">
          <SearchInput
            value={params.q ?? ''}
            onChange={(q) => onParamsChange({ ...params, q: q || undefined, page: undefined })}
            placeholder="Search shares by name or path…"
          />
          <span className="text-xs text-fg-subtle tabular-nums">
            {shares.data?.page?.total ?? items.length} configured
          </span>
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
          empty={
            <EmptyState
              title="No shares yet"
              description={params.q ? 'Try a different search.' : 'Create your first SMB share to expose a folder.'}
            />
          }
        />
      </Card>

      <ShareEditorDialog
        open={editorOpen}
        share={editing}
        onClose={() => {
          setEditorOpen(false)
          setEditing(null)
        }}
      />
    </div>
  )
}
