import { useInfiniteQuery, useQuery } from '@tanstack/react-query'
import { useVirtualizer } from '@tanstack/react-virtual'
import {
  Archive,
  ChevronRight,
  Copy,
  Download,
  Eye,
  File,
  FileArchive,
  FileAudio,
  FileCode2,
  FileImage,
  FileText,
  FileVideo,
  Folder,
  FolderPlus,
  FolderUp,
  HardDrive,
  Home,
  LayoutGrid,
  List,
  MoveRight,
  Pencil,
  RefreshCw,
  Trash2,
  Upload,
} from 'lucide-react'
import { useCallback, useEffect, useMemo, useRef, useState, type DragEvent, type FormEvent } from 'react'
import { notify } from '@/features/notifications'
import { mySharesQuery } from '@/features/portal'
import { EmptyState, ErrorState, FormField, PageHeader, SearchInput } from '@/shared/components'
import { cn } from '@/shared/lib/cn'
import { formatBytes, formatDateTime } from '@/shared/lib/format'
import { Badge, Button, Card, Dialog, Input, Select, Spinner } from '@/shared/ui'
import {
  fileDownloadUrl,
  fileEntriesInfiniteQuery,
  thumbnailCapabilitiesQuery,
  useCopyFiles,
  useCreateFolder,
  useDeleteFiles,
  useMoveFiles,
  usePrepareZip,
  useRenameFile,
  type FileEntry,
  type FileItem,
} from '../api'
import { collectDroppedBatch, collectPickerBatch } from '../drop-entries'
import { enqueueUploadBatch } from '../upload-store'
import { EntryThumbnail } from './EntryThumbnail'
import { FilePreviewDialog, getPreviewKind } from './FilePreviewDialog'

interface FilesPageProps {
  share: string
  path: string
  onNavigate: (next: { share: string; path: string }) => void
}

function joinPath(dir: string, name: string): string {
  return dir ? `${dir}/${name}` : name
}

function entryIcon(entry: FileEntry) {
  if (entry.isDir) return <Folder className="size-4.5 shrink-0 text-primary" />
  const ext = entry.name.split('.').pop()?.toLowerCase() ?? ''
  if (['zip', 'tar', 'gz', 'bz2', 'xz', '7z', 'rar'].includes(ext)) return <FileArchive className="size-4.5 shrink-0 text-warning" />
  const kind = getPreviewKind(entry.name)
  switch (kind) {
    case 'image':
      return <FileImage className="size-4.5 shrink-0 text-success" />
    case 'video':
      return <FileVideo className="size-4.5 shrink-0 text-primary" />
    case 'audio':
      return <FileAudio className="size-4.5 shrink-0 text-warning" />
    case 'pdf':
      return <FileText className="size-4.5 shrink-0 text-danger" />
    case 'text':
      return <FileCode2 className="size-4.5 shrink-0 text-fg-muted" />
    default:
      return <File className="size-4.5 shrink-0 text-fg-subtle" />
  }
}

type SortKey = 'name' | '-name' | '-size' | '-modTime'

export function FilesPage({ share, path, onNavigate }: FilesPageProps) {
  const myShares = useQuery(mySharesQuery)
  const thumbCaps = useQuery(thumbnailCapabilitiesQuery)
  const videoThumbsEnabled = Boolean(thumbCaps.data?.video)
  const shareList = useMemo(() => myShares.data?.items ?? [], [myShares.data])
  const activeShare = share || shareList[0]?.name || ''
  const activeShareMeta = shareList.find((s) => s.name === activeShare)
  const canWrite = activeShareMeta ? activeShareMeta.level === 'write' || activeShareMeta.level === 'upload' : true

  const [view, setView] = useState<'table' | 'grid'>('table')
  const [sort, setSort] = useState<SortKey>('name')
  const [filter, setFilter] = useState('')
  const [selected, setSelected] = useState<Set<string>>(new Set())
  const [dragging, setDragging] = useState(false)
  const [scanningDrop, setScanningDrop] = useState(false)
  const [gridCols, setGridCols] = useState(4)

  const navigateTo = useCallback(
    (next: { share: string; path: string }) => {
      setSelected(new Set())
      setFilter('')
      onNavigate(next)
    },
    [onNavigate],
  )

  // Dialog states
  const [mkdirOpen, setMkdirOpen] = useState(false)
  const [newFolderName, setNewFolderName] = useState('')
  const [renameTarget, setRenameTarget] = useState<FileEntry | null>(null)
  const [renameValue, setRenameValue] = useState('')
  const [transferMode, setTransferMode] = useState<'copy' | 'move' | null>(null)
  const [destShare, setDestShare] = useState('')
  const [destPath, setDestPath] = useState('')
  const [transferConflict, setTransferConflict] = useState<'rename' | 'replace' | 'skip'>('rename')
  const [previewEntry, setPreviewEntry] = useState<FileEntry | null>(null)
  const [contextMenu, setContextMenu] = useState<{ x: number; y: number; entry: FileEntry } | null>(null)

  const fileInputRef = useRef<HTMLInputElement>(null)
  const folderInputRef = useRef<HTMLInputElement>(null)
  const scrollContainerRef = useRef<HTMLDivElement>(null)

  const entries = useInfiniteQuery(fileEntriesInfiniteQuery(activeShare, path))
  const createFolder = useCreateFolder()
  const renameFile = useRenameFile()
  const deleteFiles = useDeleteFiles()
  const copyFiles = useCopyFiles()
  const moveFiles = useMoveFiles()
  const prepareZip = usePrepareZip()

  const allEntries = useMemo(() => {
    const out: FileEntry[] = []
    for (const p of entries.data?.pages ?? []) {
      if (p.entries) out.push(...p.entries)
    }
    return out
  }, [entries.data])

  const filteredItems = useMemo(() => {
    const q = filter.trim().toLowerCase()
    const list = q ? allEntries.filter((e) => e.name.toLowerCase().includes(q)) : allEntries
    return [...list].sort((a, b) => {
      const dirDiff = Number(b.isDir) - Number(a.isDir)
      if (dirDiff !== 0) return dirDiff
      switch (sort) {
        case '-name':
          return b.name.localeCompare(a.name)
        case '-size':
          return b.size - a.size
        case '-modTime':
          return b.modTime.localeCompare(a.modTime)
        default:
          return a.name.localeCompare(b.name)
      }
    })
  }, [allEntries, filter, sort])

  // Track responsive column count for virtualized grid view.
  useEffect(() => {
    const el = scrollContainerRef.current
    if (!el) return
    const updateCols = () => {
      const w = el.clientWidth
      if (w >= 960) setGridCols(4)
      else if (w >= 680) setGridCols(3)
      else if (w >= 440) setGridCols(2)
      else setGridCols(1)
    }
    updateCols()
    const ro = new ResizeObserver(updateCols)
    ro.observe(el)
    return () => ro.disconnect()
  }, [view, activeShare])

  const rowCount = view === 'table' ? filteredItems.length : Math.ceil(filteredItems.length / gridCols)
  const virtualizer = useVirtualizer({
    count: rowCount,
    getScrollElement: () => scrollContainerRef.current,
    estimateSize: () => (view === 'table' ? 44 : 164),
    overscan: 12,
  })

  const virtualRows = virtualizer.getVirtualItems()
  const lastVirtualIndex = virtualRows[virtualRows.length - 1]?.index ?? -1

  // Auto-load next 500-entry cursor page as user scrolls near the bottom of huge folders.
  const { hasNextPage, isFetchingNextPage, fetchNextPage } = entries
  useEffect(() => {
    if (lastVirtualIndex < 0) return
    const threshold = Math.max(0, rowCount - 15)
    if (lastVirtualIndex >= threshold && hasNextPage && !isFetchingNextPage) {
      void fetchNextPage()
    }
  }, [lastVirtualIndex, rowCount, hasNextPage, isFetchingNextPage, fetchNextPage])

  const selectedItems = useMemo<FileItem[]>(() => {
    return Array.from(selected).map((name) => ({ share: activeShare, path: joinPath(path, name) }))
  }, [selected, activeShare, path])

  const breadcrumbs = useMemo(() => {
    if (!path) return []
    const parts = path.split('/').filter(Boolean)
    return parts.map((label, i) => ({
      label,
      path: parts.slice(0, i + 1).join('/'),
    }))
  }, [path])

  const handleDeleteSelected = useCallback(() => {
    if (selectedItems.length === 0) return
    const label = selectedItems.length === 1 ? `"${Array.from(selected)[0]}"` : `${selectedItems.length} items`
    if (!window.confirm(`Permanently delete ${label}? This cannot be undone.`)) return
    deleteFiles.mutate(selectedItems, {
      onSuccess: (res) => {
        setSelected(new Set())
        if (res.job) notify.info('Delete queued', 'Large deletion running in background jobs.')
        else notify.success('Deleted', `Removed ${label}.`)
      },
      onError: (err) => notify.error(err),
    })
  }, [selectedItems, selected, deleteFiles])

  const handleOpenEntry = useCallback(
    (entry: FileEntry) => {
      if (entry.isDir) {
        navigateTo({ share: activeShare, path: joinPath(path, entry.name) })
      } else {
        setPreviewEntry(entry)
      }
    },
    [activeShare, navigateTo, path],
  )

  // Close context menu on any outside click or scroll.
  useEffect(() => {
    if (!contextMenu) return
    const close = () => setContextMenu(null)
    window.addEventListener('click', close)
    window.addEventListener('scroll', close, true)
    return () => {
      window.removeEventListener('click', close)
      window.removeEventListener('scroll', close, true)
    }
  }, [contextMenu])

  // Keyboard shortcuts: Escape, Ctrl/Cmd+A, Backspace (up folder), Enter (open), Delete, F2 (rename).
  useEffect(() => {
    const onKeyDown = (e: KeyboardEvent) => {
      const tag = (e.target as HTMLElement | null)?.tagName
      if (tag === 'INPUT' || tag === 'TEXTAREA' || tag === 'SELECT') return
      if (e.key === 'Escape') {
        if (contextMenu) {
          setContextMenu(null)
          return
        }
        if (selected.size > 0) setSelected(new Set())
      } else if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === 'a' && filteredItems.length > 0) {
        e.preventDefault()
        setSelected(new Set(filteredItems.map((i) => i.name)))
      } else if (e.key === 'Backspace' && path && activeShare) {
        e.preventDefault()
        const parent = path.includes('/') ? path.slice(0, path.lastIndexOf('/')) : ''
        navigateTo({ share: activeShare, path: parent })
      } else if (e.key === 'Enter' && selected.size === 1) {
        const name = Array.from(selected)[0]
        const target = filteredItems.find((i) => i.name === name)
        if (target) {
          e.preventDefault()
          handleOpenEntry(target)
        }
      } else if (e.key === 'Delete' && selected.size > 0 && canWrite) {
        e.preventDefault()
        handleDeleteSelected()
      } else if (e.key === 'F2' && selected.size === 1 && canWrite) {
        e.preventDefault()
        const name = Array.from(selected)[0]
        const target = filteredItems.find((i) => i.name === name)
        if (target) {
          setRenameTarget(target)
          setRenameValue(target.name)
        }
      }
    }
    window.addEventListener('keydown', onKeyDown)
    return () => window.removeEventListener('keydown', onKeyDown)
  }, [selected, canWrite, filteredItems, handleDeleteSelected, contextMenu, path, activeShare, navigateTo, handleOpenEntry])

  function openContextMenu(e: React.MouseEvent, entry: FileEntry) {
    e.preventDefault()
    if (!selected.has(entry.name)) {
      setSelected(new Set([entry.name]))
    }
    const menuW = 200
    const menuH = 260
    const x = Math.min(e.clientX, Math.max(8, window.innerWidth - menuW - 8))
    const y = Math.min(e.clientY, Math.max(8, window.innerHeight - menuH - 8))
    setContextMenu({ x, y, entry })
  }

  function toggleSelect(name: string) {
    setSelected((prev) => {
      const next = new Set(prev)
      if (next.has(name)) next.delete(name)
      else next.add(name)
      return next
    })
  }

  function toggleSelectAll() {
    if (selected.size === filteredItems.length) setSelected(new Set())
    else setSelected(new Set(filteredItems.map((i) => i.name)))
  }

  function handleCreateFolder(e: FormEvent) {
    e.preventDefault()
    const trimmed = newFolderName.trim()
    if (!trimmed || !activeShare) return
    createFolder.mutate(
      { share: activeShare, path: joinPath(path, trimmed) },
      {
        onSuccess: () => {
          notify.success('Folder created', trimmed)
          setNewFolderName('')
          setMkdirOpen(false)
        },
        onError: (err) => notify.error(err),
      },
    )
  }

  function handleRenameSubmit(e: FormEvent) {
    e.preventDefault()
    if (!renameTarget || !renameValue.trim() || !activeShare) return
    renameFile.mutate(
      { share: activeShare, path: joinPath(path, renameTarget.name), name: renameValue.trim() },
      {
        onSuccess: () => {
          notify.success('Renamed', `${renameTarget.name} → ${renameValue.trim()}`)
          setRenameTarget(null)
          setSelected(new Set())
        },
        onError: (err) => notify.error(err),
      },
    )
  }

  function handleTransferSubmit(e: FormEvent) {
    e.preventDefault()
    if (!transferMode || selectedItems.length === 0) return
    const payload = {
      items: selectedItems,
      destination: { share: destShare || activeShare, path: destPath.trim().replace(/^\/+|\/+$/g, '') },
      conflict: transferConflict,
    }
    const mutation = transferMode === 'copy' ? copyFiles : moveFiles
    mutation.mutate(payload, {
      onSuccess: (res) => {
        setTransferMode(null)
        setSelected(new Set())
        if (res.job) notify.info(`${transferMode === 'copy' ? 'Copy' : 'Move'} queued`, 'Track progress in Jobs.')
        else notify.success(transferMode === 'copy' ? 'Copied' : 'Moved', `${selectedItems.length} item(s) updated.`)
      },
      onError: (err) => notify.error(err),
    })
  }

  function handleZipDownload() {
    if (selectedItems.length === 0) return
    prepareZip.mutate(selectedItems, {
      onSuccess: (res) => {
        window.location.assign(res.url)
      },
      onError: (err) => notify.error(err),
    })
  }

  function handlePickerFiles(fileList: FileList | null) {
    if (!fileList || fileList.length === 0 || !activeShare) return
    const batch = collectPickerBatch(fileList)
    enqueueUploadBatch({ share: activeShare, baseDir: path, label: batch.label, files: batch.files })
  }

  function onDragOver(e: DragEvent) {
    if (!canWrite || !activeShare) return
    e.preventDefault()
    if (!dragging) setDragging(true)
  }

  function onDragLeave(e: DragEvent) {
    if (e.currentTarget.contains(e.relatedTarget as Node)) return
    setDragging(false)
  }

  async function onDrop(e: DragEvent) {
    e.preventDefault()
    setDragging(false)
    if (!canWrite || !activeShare) return
    setScanningDrop(true)
    try {
      const batch = await collectDroppedBatch(e.dataTransfer)
      if (batch.files.length > 0) {
        enqueueUploadBatch({ share: activeShare, baseDir: path, label: batch.label, files: batch.files })
      }
    } finally {
      setScanningDrop(false)
    }
  }

  return (
    <div className="relative flex flex-col gap-5" onDragOver={onDragOver} onDragLeave={onDragLeave} onDrop={(e) => void onDrop(e)}>
      <PageHeader
        title="File Manager"
        info="Drag & drop files or entire folders to start a resumable batch upload (runs in a background Web Worker). Shortcuts: F2 rename, Delete remove, Esc clear selection."
        actions={
          activeShare && (
            <div className="flex flex-wrap items-center gap-2">
              <Button variant="secondary" size="sm" onClick={() => void entries.refetch()} aria-label="Refresh">
                <RefreshCw className={cn('size-3.5', entries.isFetching && 'animate-spin')} />
                Refresh
              </Button>
              {canWrite && (
                <>
                  <Button variant="secondary" size="sm" onClick={() => setMkdirOpen(true)}>
                    <FolderPlus className="size-3.5" />
                    New folder
                  </Button>
                  <Button variant="secondary" size="sm" onClick={() => folderInputRef.current?.click()}>
                    <FolderUp className="size-3.5" />
                    Upload folder
                  </Button>
                  <Button size="sm" onClick={() => fileInputRef.current?.click()}>
                    <Upload className="size-3.5" />
                    Upload files
                  </Button>
                  <input
                    ref={fileInputRef}
                    type="file"
                    multiple
                    className="hidden"
                    onChange={(e) => {
                      handlePickerFiles(e.target.files)
                      e.target.value = ''
                    }}
                  />
                  <input
                    ref={folderInputRef}
                    type="file"
                    multiple
                    // @ts-expect-error webkitdirectory is standard in Chromium/Firefox/Safari
                    webkitdirectory=""
                    className="hidden"
                    onChange={(e) => {
                      handlePickerFiles(e.target.files)
                      e.target.value = ''
                    }}
                  />
                </>
              )}
            </div>
          )
        }
      />

      {myShares.isLoading ? (
        <div className="flex justify-center py-16">
          <Spinner />
        </div>
      ) : shareList.length === 0 ? (
        <Card className="p-8">
          <EmptyState
            title="No accessible shares"
            description="Create a share or ask an administrator to grant you access before browsing files."
          />
        </Card>
      ) : (
        <Card className="overflow-hidden">
          {/* Share selector pills + Toolbar */}
          <div className="flex flex-wrap items-center justify-between gap-3 border-b border-border px-4 py-3">
            <div className="flex flex-wrap items-center gap-1.5">
              {shareList.map((s) => {
                const isCurrent = s.name === activeShare
                return (
                  <button
                    key={s.id}
                    type="button"
                    onClick={() => navigateTo({ share: s.name, path: '' })}
                    className={cn(
                      'inline-flex items-center gap-1.5 rounded-lg border px-3 py-1.5 text-xs font-medium transition-all',
                      isCurrent
                        ? 'border-primary/40 bg-primary-soft text-primary shadow-xs'
                        : 'border-border bg-bg text-fg-muted hover:border-border-strong hover:text-fg',
                    )}
                  >
                    <HardDrive className="size-3.5" />
                    <span>{s.name}</span>
                  </button>
                )
              })}
            </div>

            <div className="flex items-center gap-2">
              <div className="w-44">
                <SearchInput value={filter} onChange={setFilter} placeholder="Filter folder…" />
              </div>
              <Select
                className="h-8 w-36 text-xs"
                value={sort}
                onChange={(e) => setSort(e.target.value as SortKey)}
                aria-label="Sort files"
              >
                <option value="name">Name (A–Z)</option>
                <option value="-name">Name (Z–A)</option>
                <option value="-size">Largest first</option>
                <option value="-modTime">Newest first</option>
              </Select>
              <div className="inline-flex rounded-lg border border-border bg-surface-2 p-0.5">
                <button
                  type="button"
                  onClick={() => setView('table')}
                  className={cn(
                    'rounded-md p-1.5 transition-colors',
                    view === 'table' ? 'bg-surface text-fg shadow-xs' : 'text-fg-muted hover:text-fg',
                  )}
                  aria-label="Table view"
                >
                  <List className="size-3.5" />
                </button>
                <button
                  type="button"
                  onClick={() => setView('grid')}
                  className={cn(
                    'rounded-md p-1.5 transition-colors',
                    view === 'grid' ? 'bg-surface text-fg shadow-xs' : 'text-fg-muted hover:text-fg',
                  )}
                  aria-label="Grid view"
                >
                  <LayoutGrid className="size-3.5" />
                </button>
              </div>
            </div>
          </div>

          {/* Breadcrumbs + Selection Action Bar */}
          <div className="flex flex-wrap items-center justify-between gap-2 border-b border-border bg-surface-2/40 px-4 py-2">
            <nav aria-label="Folder path" className="flex min-w-0 flex-wrap items-center gap-1 text-xs">
              <button
                type="button"
                onClick={() => navigateTo({ share: activeShare, path: '' })}
                className="inline-flex items-center gap-1 rounded-md px-2 py-1 font-medium text-fg-muted transition-colors hover:bg-surface-3 hover:text-fg"
              >
                <Home className="size-3.5" />
                <span>{activeShare}</span>
              </button>
              {breadcrumbs.map((crumb) => (
                <span key={crumb.path} className="inline-flex items-center gap-1">
                  <ChevronRight className="size-3.5 text-fg-subtle" />
                  <button
                    type="button"
                    onClick={() => navigateTo({ share: activeShare, path: crumb.path })}
                    className="rounded-md px-2 py-1 font-medium text-fg transition-colors hover:bg-surface-3"
                  >
                    {crumb.label}
                  </button>
                </span>
              ))}
            </nav>

            {selected.size > 0 ? (
              <div className="flex flex-wrap items-center gap-1.5">
                <Badge tone="info" className="tabular-nums">
                  {selected.size} selected
                </Badge>
                <Button variant="secondary" size="sm" className="h-7 px-2.5 text-xs" loading={prepareZip.isPending} onClick={handleZipDownload}>
                  <Archive className="size-3.5" />
                  ZIP
                </Button>
                {canWrite && (
                  <>
                    <Button
                      variant="secondary"
                      size="sm"
                      className="h-7 px-2.5 text-xs"
                      onClick={() => {
                        setDestShare(activeShare)
                        setDestPath(path)
                        setTransferMode('copy')
                      }}
                    >
                      <Copy className="size-3.5" />
                      Copy
                    </Button>
                    <Button
                      variant="secondary"
                      size="sm"
                      className="h-7 px-2.5 text-xs"
                      onClick={() => {
                        setDestShare(activeShare)
                        setDestPath(path)
                        setTransferMode('move')
                      }}
                    >
                      <MoveRight className="size-3.5" />
                      Move
                    </Button>
                    <Button
                      variant="danger"
                      size="sm"
                      className="h-7 px-2.5 text-xs"
                      loading={deleteFiles.isPending}
                      onClick={handleDeleteSelected}
                    >
                      <Trash2 className="size-3.5" />
                      Delete
                    </Button>
                  </>
                )}
              </div>
            ) : (
              <span className="text-[0.75rem] text-fg-subtle tabular-nums">
                {filteredItems.length.toLocaleString()} {filteredItems.length === 1 ? 'item' : 'items'}
                {entries.hasNextPage && '+'}
              </span>
            )}
          </div>

          {/* Virtualized File Listing */}
          {entries.isLoading ? (
            <div className="flex justify-center py-16">
              <Spinner />
            </div>
          ) : entries.error ? (
            <div className="p-6">
              <ErrorState error={entries.error} onRetry={() => void entries.refetch()} />
            </div>
          ) : filteredItems.length === 0 ? (
            <div className="p-8">
              <EmptyState
                title={filter ? 'No matching files' : 'This folder is empty'}
                description={
                  filter
                    ? 'Clear the filter to see all entries.'
                    : canWrite
                      ? 'Drag and drop files or folders here, or click Upload.'
                      : undefined
                }
              />
            </div>
          ) : (
            <>
              {view === 'table' && (
                <div className="grid grid-cols-[2.5rem_minmax(0,1fr)_6.5rem_10.5rem_6.5rem] items-center border-b border-border bg-surface-2/50 px-4 py-2 text-[0.6875rem] font-semibold tracking-wider text-fg-subtle uppercase">
                  <div>
                    <input
                      type="checkbox"
                      checked={selected.size === filteredItems.length && filteredItems.length > 0}
                      onChange={toggleSelectAll}
                      aria-label="Select all"
                      className="accent-primary"
                    />
                  </div>
                  <div>Name</div>
                  <div className="max-sm:hidden">Size</div>
                  <div className="max-md:hidden">Modified</div>
                  <div className="text-right">Actions</div>
                </div>
              )}

              <div ref={scrollContainerRef} className="max-h-[65dvh] min-h-64 overflow-y-auto">
                <div
                  style={{
                    height: `${virtualizer.getTotalSize()}px`,
                    width: '100%',
                    position: 'relative',
                  }}
                >
                  {virtualRows.map((vRow) => {
                    if (view === 'table') {
                      const entry = filteredItems[vRow.index]
                      if (!entry) return null
                      const isChecked = selected.has(entry.name)
                      const entryPath = joinPath(path, entry.name)

                      return (
                        <div
                          key={entry.name}
                          onContextMenu={(e) => openContextMenu(e, entry)}
                          style={{
                            position: 'absolute',
                            top: 0,
                            left: 0,
                            width: '100%',
                            height: `${vRow.size}px`,
                            transform: `translateY(${vRow.start}px)`,
                          }}
                          className={cn(
                            'group grid grid-cols-[2.5rem_minmax(0,1fr)_6.5rem_10.5rem_6.5rem] items-center border-b border-border px-4 text-sm transition-colors hover:bg-surface-2/50',
                            isChecked && 'bg-primary-soft/35',
                          )}
                        >
                          <div>
                            <input
                              type="checkbox"
                              checked={isChecked}
                              onChange={() => toggleSelect(entry.name)}
                              aria-label={`Select ${entry.name}`}
                              className="accent-primary"
                            />
                          </div>
                          <div className="min-w-0 pr-3">
                            <button
                              type="button"
                              onClick={() => handleOpenEntry(entry)}
                              className="flex max-w-full items-center gap-2.5 text-left font-medium text-fg hover:text-primary"
                            >
                              {entryIcon(entry)}
                              <span className="truncate">{entry.name}</span>
                            </button>
                          </div>
                          <div className="text-xs text-fg-muted tabular-nums max-sm:hidden">
                            {entry.isDir ? '—' : formatBytes(entry.size)}
                          </div>
                          <div className="truncate text-xs text-fg-muted max-md:hidden">
                            {formatDateTime(entry.modTime)}
                          </div>
                          <div className="flex items-center justify-end gap-0.5 opacity-80 transition-opacity group-hover:opacity-100">
                            {!entry.isDir && (
                              <>
                                <Button
                                  variant="ghost"
                                  size="icon"
                                  className="size-7"
                                  title="Preview"
                                  onClick={() => setPreviewEntry(entry)}
                                >
                                  <Eye className="size-3.5" />
                                </Button>
                                <a
                                  href={fileDownloadUrl(activeShare, entryPath)}
                                  download={entry.name}
                                  className="inline-flex size-7 items-center justify-center rounded-md text-fg-muted hover:bg-surface-2 hover:text-fg"
                                  title="Download"
                                >
                                  <Download className="size-3.5" />
                                </a>
                              </>
                            )}
                            {canWrite && (
                              <Button
                                variant="ghost"
                                size="icon"
                                className="size-7"
                                title="Rename"
                                onClick={() => {
                                  setRenameTarget(entry)
                                  setRenameValue(entry.name)
                                }}
                              >
                                <Pencil className="size-3.5" />
                              </Button>
                            )}
                          </div>
                        </div>
                      )
                    }

                    // Virtualized grid row
                    const startIdx = vRow.index * gridCols
                    const slice = filteredItems.slice(startIdx, startIdx + gridCols)
                    return (
                      <div
                        key={vRow.key}
                        style={{
                          position: 'absolute',
                          top: 0,
                          left: 0,
                          width: '100%',
                          height: `${vRow.size}px`,
                          transform: `translateY(${vRow.start}px)`,
                          gridTemplateColumns: `repeat(${gridCols}, minmax(0, 1fr))`,
                        }}
                        className="grid gap-3 px-4 py-1.5"
                      >
                        {slice.map((entry) => {
                          const isChecked = selected.has(entry.name)
                          return (
                            <div
                              key={entry.name}
                              onContextMenu={(e) => openContextMenu(e, entry)}
                              className={cn(
                                'group flex flex-col justify-between rounded-xl border border-border bg-bg p-3 transition-all hover:border-border-strong',
                                isChecked && 'border-primary bg-primary-soft/30',
                              )}
                            >
                              <div className="relative flex flex-col gap-2">
                                <input
                                  type="checkbox"
                                  checked={isChecked}
                                  onChange={() => toggleSelect(entry.name)}
                                  aria-label={`Select ${entry.name}`}
                                  className="absolute top-1.5 left-1.5 z-10 accent-primary"
                                />
                                <button
                                  type="button"
                                  onClick={() => handleOpenEntry(entry)}
                                  className="flex h-20 w-full items-center justify-center"
                                >
                                  <EntryThumbnail
                                    share={activeShare}
                                    path={joinPath(path, entry.name)}
                                    name={entry.name}
                                    isDir={entry.isDir}
                                    videoEnabled={videoThumbsEnabled}
                                    fallback={entryIcon(entry)}
                                  />
                                </button>
                              </div>
                              <button
                                type="button"
                                onClick={() => handleOpenEntry(entry)}
                                className="mt-2 truncate text-center text-xs font-medium text-fg hover:text-primary"
                                title={entry.name}
                              >
                                {entry.name}
                              </button>
                              <div className="text-center text-[0.6875rem] text-fg-subtle tabular-nums">
                                {entry.isDir ? 'Folder' : formatBytes(entry.size)}
                              </div>
                            </div>
                          )
                        })}
                      </div>
                    )
                  })}
                </div>

                {entries.isFetchingNextPage && (
                  <div className="flex items-center justify-center gap-2 py-3 text-xs text-fg-muted">
                    <Spinner className="size-3.5" />
                    Loading more entries…
                  </div>
                )}
              </div>
            </>
          )}
        </Card>
      )}

      {/* Drag & drop overlay */}
      {(dragging || scanningDrop) && (
        <div className="pointer-events-none fixed inset-4 z-50 grid place-items-center rounded-2xl border-2 border-dashed border-primary bg-primary-soft/85 backdrop-blur-xs">
          <div className="flex flex-col items-center gap-2 text-primary">
            {scanningDrop ? <Spinner className="size-8" /> : <Upload className="size-10" />}
            <div className="text-lg font-semibold">
              {scanningDrop
                ? 'Scanning dropped folders…'
                : `Drop files or folders into /${activeShare}${path ? `/${path}` : ''}`}
            </div>
            <div className="text-xs opacity-80">Tracked as one resumable upload batch in the background</div>
          </div>
        </div>
      )}

      {/* New folder dialog */}
      <Dialog open={mkdirOpen} onClose={() => setMkdirOpen(false)} size="sm" title="Create folder">
        <form onSubmit={handleCreateFolder} className="flex flex-col gap-4">
          <FormField label="Folder name" htmlFor="mkdir-name">
            <Input
              id="mkdir-name"
              autoFocus
              value={newFolderName}
              onChange={(e) => setNewFolderName(e.target.value)}
              placeholder="Projects"
              required
            />
          </FormField>
          <div className="flex justify-end gap-2">
            <Button variant="secondary" onClick={() => setMkdirOpen(false)}>
              Cancel
            </Button>
            <Button type="submit" loading={createFolder.isPending}>
              Create
            </Button>
          </div>
        </form>
      </Dialog>

      {/* Rename dialog */}
      <Dialog
        open={Boolean(renameTarget)}
        onClose={() => setRenameTarget(null)}
        size="sm"
        title={`Rename ${renameTarget?.isDir ? 'folder' : 'file'}`}
      >
        <form onSubmit={handleRenameSubmit} className="flex flex-col gap-4">
          <FormField label="New name" htmlFor="rename-input">
            <Input
              id="rename-input"
              autoFocus
              value={renameValue}
              onChange={(e) => setRenameValue(e.target.value)}
              required
            />
          </FormField>
          <div className="flex justify-end gap-2">
            <Button variant="secondary" onClick={() => setRenameTarget(null)}>
              Cancel
            </Button>
            <Button type="submit" loading={renameFile.isPending}>
              Rename
            </Button>
          </div>
        </form>
      </Dialog>

      {/* Copy / Move dialog */}
      <Dialog
        open={Boolean(transferMode)}
        onClose={() => setTransferMode(null)}
        size="sm"
        title={transferMode === 'copy' ? `Copy ${selected.size} item(s)` : `Move ${selected.size} item(s)`}
      >
        <form onSubmit={handleTransferSubmit} className="flex flex-col gap-4">
          <FormField label="Destination share" htmlFor="dest-share">
            <Select id="dest-share" value={destShare} onChange={(e) => setDestShare(e.target.value)}>
              {shareList.map((s) => (
                <option key={s.id} value={s.name}>
                  {s.name}
                </option>
              ))}
            </Select>
          </FormField>
          <FormField
            label="Destination folder"
            htmlFor="dest-path"
            info="Path inside the destination share. Leave blank for the share's top folder."
          >
            <Input
              id="dest-path"
              value={destPath}
              onChange={(e) => setDestPath(e.target.value)}
              placeholder="Documents/Archive"
            />
          </FormField>
          <FormField label="If a file already exists" htmlFor="dest-conflict">
            <Select
              id="dest-conflict"
              value={transferConflict}
              onChange={(e) => setTransferConflict(e.target.value as 'rename' | 'replace' | 'skip')}
            >
              <option value="rename">Keep both (auto-rename)</option>
              <option value="replace">Replace existing</option>
              <option value="skip">Skip existing</option>
            </Select>
          </FormField>
          <div className="flex justify-end gap-2">
            <Button variant="secondary" onClick={() => setTransferMode(null)}>
              Cancel
            </Button>
            <Button type="submit" loading={copyFiles.isPending || moveFiles.isPending}>
              {transferMode === 'copy' ? 'Copy here' : 'Move here'}
            </Button>
          </div>
        </form>
      </Dialog>

      {/* Right-click context menu */}
      {contextMenu && (
        <div
          style={{ left: contextMenu.x, top: contextMenu.y }}
          onClick={(e) => e.stopPropagation()}
          className="fixed z-50 w-48 overflow-hidden rounded-xl border border-border bg-surface p-1 text-xs shadow-pop"
        >
          <button
            type="button"
            onClick={() => {
              const target = contextMenu.entry
              setContextMenu(null)
              handleOpenEntry(target)
            }}
            className="flex w-full items-center gap-2 rounded-lg px-2.5 py-2 text-left font-medium text-fg hover:bg-surface-2"
          >
            <Eye className="size-3.5 text-fg-muted" />
            {contextMenu.entry.isDir ? 'Open folder' : 'Preview'}
          </button>

          {!contextMenu.entry.isDir && selected.size <= 1 && (
            <a
              href={fileDownloadUrl(activeShare, joinPath(path, contextMenu.entry.name))}
              download={contextMenu.entry.name}
              onClick={() => setContextMenu(null)}
              className="flex w-full items-center gap-2 rounded-lg px-2.5 py-2 text-left text-fg hover:bg-surface-2"
            >
              <Download className="size-3.5 text-fg-muted" />
              Download
            </a>
          )}

          <button
            type="button"
            onClick={() => {
              setContextMenu(null)
              handleZipDownload()
            }}
            className="flex w-full items-center gap-2 rounded-lg px-2.5 py-2 text-left text-fg hover:bg-surface-2"
          >
            <Archive className="size-3.5 text-fg-muted" />
            Download as ZIP{selected.size > 1 ? ` (${selected.size})` : ''}
          </button>

          {canWrite && (
            <>
              <div className="my-1 border-t border-border" />
              {selected.size === 1 && (
                <button
                  type="button"
                  onClick={() => {
                    const target = contextMenu.entry
                    setContextMenu(null)
                    setRenameTarget(target)
                    setRenameValue(target.name)
                  }}
                  className="flex w-full items-center gap-2 rounded-lg px-2.5 py-2 text-left text-fg hover:bg-surface-2"
                >
                  <Pencil className="size-3.5 text-fg-muted" />
                  Rename
                  <span className="ml-auto font-mono text-[0.65rem] text-fg-subtle">F2</span>
                </button>
              )}
              <button
                type="button"
                onClick={() => {
                  setContextMenu(null)
                  setDestShare(activeShare)
                  setDestPath(path)
                  setTransferMode('copy')
                }}
                className="flex w-full items-center gap-2 rounded-lg px-2.5 py-2 text-left text-fg hover:bg-surface-2"
              >
                <Copy className="size-3.5 text-fg-muted" />
                Copy{selected.size > 1 ? ` (${selected.size})` : ''}
              </button>
              <button
                type="button"
                onClick={() => {
                  setContextMenu(null)
                  setDestShare(activeShare)
                  setDestPath(path)
                  setTransferMode('move')
                }}
                className="flex w-full items-center gap-2 rounded-lg px-2.5 py-2 text-left text-fg hover:bg-surface-2"
              >
                <MoveRight className="size-3.5 text-fg-muted" />
                Move{selected.size > 1 ? ` (${selected.size})` : ''}
              </button>
              <div className="my-1 border-t border-border" />
              <button
                type="button"
                onClick={() => {
                  setContextMenu(null)
                  handleDeleteSelected()
                }}
                className="flex w-full items-center gap-2 rounded-lg px-2.5 py-2 text-left font-medium text-danger hover:bg-danger/10"
              >
                <Trash2 className="size-3.5" />
                Delete{selected.size > 1 ? ` (${selected.size})` : ''}
                <span className="ml-auto font-mono text-[0.65rem] opacity-70">Del</span>
              </button>
            </>
          )}
        </div>
      )}

      {/* File preview modal */}
      <FilePreviewDialog
        open={Boolean(previewEntry)}
        share={activeShare}
        path={previewEntry ? joinPath(path, previewEntry.name) : ''}
        entry={previewEntry}
        onClose={() => setPreviewEntry(null)}
      />
    </div>
  )
}
