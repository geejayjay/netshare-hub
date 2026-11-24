import { useQuery } from '@tanstack/react-query'
import { useNavigate } from '@tanstack/react-router'
import { File, Folder, RefreshCw, Search, X } from 'lucide-react'
import { useDeferredValue, useEffect, useRef, useState } from 'react'
import { notify } from '@/features/notifications'
import { cn } from '@/shared/lib/cn'
import { formatBytes, formatRelativeTime } from '@/shared/lib/format'
import { useSession } from '@/shared/session/session'
import { Badge, Button, Spinner } from '@/shared/ui'
import { searchFilesQuery, searchStatusQuery, useReindexSearch, type SearchHit } from '../api'

function parentDir(filePath: string): string {
  const idx = filePath.lastIndexOf('/')
  return idx > 0 ? filePath.slice(0, idx) : ''
}

export function GlobalSearchButton() {
  const [open, setOpen] = useState(false)

  useEffect(() => {
    function onKeyDown(e: KeyboardEvent) {
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === 'k') {
        e.preventDefault()
        setOpen((prev) => !prev)
      }
    }
    window.addEventListener('keydown', onKeyDown)
    return () => window.removeEventListener('keydown', onKeyDown)
  }, [])

  return (
    <>
      <button
        type="button"
        onClick={() => setOpen(true)}
        className="inline-flex h-8 items-center gap-2 rounded-lg border border-border bg-surface-2/60 px-2.5 text-xs text-fg-muted transition-colors hover:border-border-strong hover:bg-surface-2 hover:text-fg"
        aria-label="Search files across shares"
      >
        <Search className="size-3.5" />
        <span className="hidden sm:inline">Search files…</span>
        <kbd className="hidden rounded border border-border bg-surface px-1 py-0.5 font-mono text-[0.65rem] text-fg-subtle md:inline">
          Ctrl K
        </kbd>
      </button>

      {open && <GlobalSearchPalette onClose={() => setOpen(false)} />}
    </>
  )
}

function GlobalSearchPalette({ onClose }: { onClose: () => void }) {
  const navigate = useNavigate()
  const { can } = useSession()
  const canReindex = can('settings:write')

  const [query, setQuery] = useState('')
  const [kind, setKind] = useState<'all' | 'file' | 'dir'>('all')
  const [selectedIndex, setSelectedIndex] = useState(0)
  const inputRef = useRef<HTMLInputElement>(null)

  const deferred = useDeferredValue(query.trim())
  const status = useQuery(searchStatusQuery)
  const results = useQuery(searchFilesQuery({ q: deferred, kind, limit: 30 }))
  const reindex = useReindexSearch()

  const items = results.data?.items ?? []
  const activeIdx = items.length > 0 ? Math.min(selectedIndex, items.length - 1) : 0

  useEffect(() => {
    inputRef.current?.focus()
  }, [])

  function openHit(hit: SearchHit) {
    const targetPath = hit.isDir ? hit.path : parentDir(hit.path)
    onClose()
    void navigate({
      to: '/files',
      search: {
        share: hit.shareName,
        path: targetPath || undefined,
      },
    })
  }

  function handleKeyDown(e: React.KeyboardEvent) {
    if (e.key === 'Escape') {
      e.preventDefault()
      onClose()
      return
    }
    if (e.key === 'ArrowDown' && items.length > 0) {
      e.preventDefault()
      setSelectedIndex((i) => (i + 1) % items.length)
      return
    }
    if (e.key === 'ArrowUp' && items.length > 0) {
      e.preventDefault()
      setSelectedIndex((i) => (i - 1 + items.length) % items.length)
      return
    }
    if (e.key === 'Enter' && items[activeIdx]) {
      e.preventDefault()
      openHit(items[activeIdx])
    }
  }

  function handleReindex() {
    reindex.mutate(undefined, {
      onSuccess: (st) => notify.success('Search index updated', `${st.indexedCount.toLocaleString()} items indexed.`),
      onError: (err) => notify.error(err),
    })
  }

  return (
    <div
      className="fixed inset-0 z-50 flex items-start justify-center bg-black/50 p-4 pt-[12vh] backdrop-blur-[2px]"
      onClick={(e) => {
        if (e.target === e.currentTarget) onClose()
      }}
      onKeyDown={handleKeyDown}
    >
      <div className="animate-modal-in flex w-[min(40rem,100%)] flex-col overflow-hidden rounded-xl border border-border bg-surface shadow-pop">
        {/* Search input bar */}
        <div className="flex items-center gap-2.5 border-b border-border px-4 py-3">
          <Search className="size-4 shrink-0 text-fg-subtle" />
          <input
            ref={inputRef}
            type="text"
            value={query}
            onChange={(e) => {
              setQuery(e.target.value)
              setSelectedIndex(0)
            }}
            placeholder="Search files and folders across your shares…"
            className="w-full bg-transparent text-sm text-fg placeholder:text-fg-subtle focus:outline-none"
          />
          {results.isFetching && <Spinner className="size-4 shrink-0" />}
          {query && (
            <button
              type="button"
              onClick={() => setQuery('')}
              className="rounded p-1 text-fg-subtle hover:bg-surface-2 hover:text-fg"
              aria-label="Clear search"
            >
              <X className="size-3.5" />
            </button>
          )}
        </div>

        {/* Filter bar */}
        <div className="flex items-center justify-between gap-2 border-b border-border bg-surface-2/40 px-4 py-2 text-xs">
          <div className="flex items-center gap-1">
            {(['all', 'file', 'dir'] as const).map((k) => (
              <button
                key={k}
                type="button"
                onClick={() => {
                  setKind(k)
                  setSelectedIndex(0)
                }}
                className={cn(
                  'rounded-md px-2 py-0.5 font-medium transition-colors',
                  kind === k ? 'bg-primary text-primary-fg' : 'text-fg-muted hover:bg-surface-2 hover:text-fg',
                )}
              >
                {k === 'all' ? 'All' : k === 'file' ? 'Files' : 'Folders'}
              </button>
            ))}
          </div>

          <div className="flex items-center gap-2 text-[0.7rem] text-fg-subtle">
            {status.data && (
              <span>
                {status.data.indexedCount.toLocaleString()} indexed
                {status.data.lastIndexedAt ? ` · ${formatRelativeTime(status.data.lastIndexedAt)}` : ''}
              </span>
            )}
            {canReindex && (
              <Button size="sm" variant="ghost" className="h-6 px-2 text-[0.7rem]" loading={reindex.isPending} onClick={handleReindex}>
                <RefreshCw className="size-3" />
                Reindex
              </Button>
            )}
          </div>
        </div>

        {/* Results list */}
        <div className="max-h-[52vh] overflow-y-auto">
          {!deferred ? (
            <div className="px-5 py-8 text-center text-xs text-fg-muted">
              {status.data && status.data.indexedCount === 0 && !status.data.lastIndexedAt ? (
                <div className="flex flex-col items-center gap-2">
                  <span>The search index hasn&apos;t been built yet.</span>
                  {canReindex && (
                    <Button size="sm" variant="secondary" loading={reindex.isPending} onClick={handleReindex}>
                      <RefreshCw className="size-3.5" />
                      Build search index now
                    </Button>
                  )}
                </div>
              ) : (
                'Type a filename, extension, or folder name. Results only include shares you can access.'
              )}
            </div>
          ) : items.length === 0 && !results.isFetching ? (
            <div className="px-5 py-8 text-center text-xs text-fg-muted">
              No matching files or folders for &ldquo;{deferred}&rdquo;.
            </div>
          ) : (
            <div className="divide-y divide-border/60">
              {items.map((hit, idx) => (
                <button
                  key={`${hit.shareId}:${hit.path}`}
                  type="button"
                  onMouseEnter={() => setSelectedIndex(idx)}
                  onClick={() => openHit(hit)}
                  className={cn(
                    'flex w-full items-center justify-between gap-3 px-4 py-2.5 text-left transition-colors',
                    idx === activeIdx ? 'bg-primary/10' : 'hover:bg-surface-2/60',
                  )}
                >
                  <div className="flex min-w-0 items-center gap-2.5">
                    {hit.isDir ? (
                      <Folder className="size-4 shrink-0 text-primary" />
                    ) : (
                      <File className="size-4 shrink-0 text-fg-muted" />
                    )}
                    <div className="min-w-0">
                      <div className="truncate text-sm font-medium text-fg">{hit.name}</div>
                      <div className="truncate text-[0.7rem] text-fg-subtle">{hit.path}</div>
                    </div>
                  </div>
                  <div className="flex shrink-0 items-center gap-2">
                    <Badge tone="neutral">{hit.shareName}</Badge>
                    {!hit.isDir && <span className="font-mono text-[0.7rem] text-fg-subtle">{formatBytes(hit.size)}</span>}
                  </div>
                </button>
              ))}
            </div>
          )}
        </div>

        {/* Footer hints */}
        <div className="flex items-center justify-between border-t border-border bg-surface-2/30 px-4 py-2 text-[0.7rem] text-fg-subtle">
          <span>↑↓ navigate · Enter open in File Manager · Esc close</span>
          <span>FTS5 access-checked</span>
        </div>
      </div>
    </div>
  )
}
