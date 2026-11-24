import { useQuery } from '@tanstack/react-query'
import { ChevronRight, File, Folder, Home, Recycle, RotateCcw, Trash2 } from 'lucide-react'
import { useState } from 'react'
import { notify } from '@/features/notifications'
import { isApiError } from '@/shared/api'
import { EmptyState, ErrorState } from '@/shared/components'
import { formatBytes, formatDateTime } from '@/shared/lib/format'
import { Button, Drawer, Spinner } from '@/shared/ui'
import { recycleEntriesQuery, useEmptyRecycle, useRestoreFromRecycle, type FileEntry } from '../api'

function joinPath(dir: string, name: string): string {
  return dir ? `${dir}/${name}` : name
}

function RecycleBinBody({ share, canWrite }: { share: string; canWrite: boolean }) {
  const [dir, setDir] = useState('')
  const query = useQuery(recycleEntriesQuery(share, dir))
  const restore = useRestoreFromRecycle()
  const emptyBin = useEmptyRecycle()
  const [restoringName, setRestoringName] = useState<string | null>(null)

  const disabledForShare = isApiError(query.error) && query.error.code === 'SHARE_NO_RECYCLE_BIN'
  const entries = query.data?.entries ?? []
  const crumbs = dir ? dir.split('/').filter(Boolean) : []

  function handleRestore(entry: FileEntry) {
    const targetPath = joinPath(dir, entry.name)
    setRestoringName(entry.name)
    restore.mutate(
      { share, path: targetPath },
      {
        onSuccess: (restored) => {
          setRestoringName(null)
          notify.success('Restored', `"${restored.name}" was returned to ${share}.`)
        },
        onError: (err) => {
          setRestoringName(null)
          notify.error(err)
        },
      },
    )
  }

  function handleEmpty() {
    if (!window.confirm(`Permanently delete everything in "${share}"'s recycle bin? This cannot be undone.`)) return
    emptyBin.mutate(share, {
      onSuccess: () => {
        setDir('')
        notify.success('Recycle bin emptied', `Cleared deleted items in "${share}".`)
      },
      onError: (err) => notify.error(err),
    })
  }

  if (disabledForShare) {
    return (
      <EmptyState
        title="Recycle bin is turned off"
        description={`Deletions on "${share}" are permanent. An administrator can enable per-share recycle bin retention in Shares → Edit share.`}
      />
    )
  }

  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-wrap items-center justify-between gap-2 rounded-lg border border-border bg-surface-2/40 px-3 py-2 text-xs">
        <nav className="flex flex-wrap items-center gap-1" aria-label="Recycle bin path">
          <button
            type="button"
            onClick={() => setDir('')}
            className="inline-flex items-center gap-1 rounded px-1.5 py-0.5 font-medium text-fg hover:bg-surface-2"
          >
            <Home className="size-3.5 text-primary" />
            <span>{share}</span>
          </button>
          {crumbs.map((seg, i) => {
            const p = crumbs.slice(0, i + 1).join('/')
            return (
              <span key={p} className="inline-flex items-center gap-1">
                <ChevronRight className="size-3 text-fg-subtle" />
                <button
                  type="button"
                  onClick={() => setDir(p)}
                  className="rounded px-1.5 py-0.5 font-medium text-fg hover:bg-surface-2"
                >
                  {seg}
                </button>
              </span>
            )
          })}
        </nav>

        {canWrite && (
          <Button
            size="sm"
            variant="danger"
            className="h-7 px-2.5 text-xs"
            disabled={entries.length === 0 && !dir}
            loading={emptyBin.isPending}
            onClick={handleEmpty}
          >
            <Trash2 className="size-3.5" />
            Empty bin
          </Button>
        )}
      </div>

      {query.isLoading ? (
        <div className="flex justify-center py-10">
          <Spinner />
        </div>
      ) : query.error ? (
        <ErrorState error={query.error} onRetry={() => void query.refetch()} />
      ) : entries.length === 0 ? (
        <EmptyState
          title="Recycle bin is empty"
          description={dir ? 'No deleted items in this folder.' : `Nothing deleted in "${share}" yet.`}
        />
      ) : (
        <div className="divide-y divide-border overflow-hidden rounded-xl border border-border">
          {entries.map((entry) => (
            <div key={entry.name} className="flex items-center justify-between gap-3 px-3.5 py-2.5">
              <div className="flex min-w-0 items-center gap-2.5">
                {entry.isDir ? (
                  <Folder className="size-4 shrink-0 text-primary" />
                ) : (
                  <File className="size-4 shrink-0 text-fg-muted" />
                )}
                <div className="min-w-0">
                  {entry.isDir ? (
                    <button
                      type="button"
                      onClick={() => setDir(joinPath(dir, entry.name))}
                      className="truncate text-left text-sm font-medium text-fg hover:text-primary"
                    >
                      {entry.name}
                    </button>
                  ) : (
                    <div className="truncate text-sm font-medium text-fg">{entry.name}</div>
                  )}
                  <div className="text-[0.7rem] text-fg-subtle">
                    {entry.isDir ? 'Folder' : formatBytes(entry.size)} · {formatDateTime(entry.modTime)}
                  </div>
                </div>
              </div>

              {canWrite && (
                <Button
                  size="sm"
                  variant="secondary"
                  className="h-7 shrink-0 px-2.5 text-xs"
                  loading={restoringName === entry.name}
                  onClick={() => handleRestore(entry)}
                >
                  <RotateCcw className="size-3.5" />
                  Restore
                </Button>
              )}
            </div>
          ))}
        </div>
      )}
    </div>
  )
}

export function RecycleBinDrawer({
  open,
  share,
  canWrite,
  onClose,
}: {
  open: boolean
  share: string
  canWrite: boolean
  onClose: () => void
}) {
  return (
    <Drawer
      open={open}
      onClose={onClose}
      title={share ? `Recycle Bin — ${share}` : 'Recycle Bin'}
      description="Deleted items keep their original folder structure and permissions until auto-purged."
    >
      {open && share && <RecycleBinBody key={share} share={share} canWrite={canWrite} />}
    </Drawer>
  )
}

export { Recycle as RecycleIcon }
