import { useQuery, useQueryClient } from '@tanstack/react-query'
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
  fileEntriesQuery,
  useCopyFiles,
  useCreateFolder,
  useDeleteFiles,
  useMoveFiles,
  usePrepareZip,
  useRenameFile,
  type FileEntry,
  type FileItem,
} from '../api'
import { abortTusUpload, runTusUpload, type UploadConflict, type UploadTask } from '../tus'
import { FilePreviewDialog, getPreviewKind } from './FilePreviewDialog'
import { UploadQueue } from './UploadQueue'

interface FilesPageProps {
  share: string
  path: string
  onNavigate: (next: { share: string; path: string }) => void
}

function joinPath(dir: string, name: string): string {
  return dir ? `${dir}/${name}` : name
}

function entryIcon(entry: FileEntry) {
  if (entry.isDir) return <Folder className="size-4.5 text-primary" />
  const ext = entry.name.split('.').pop()?.toLowerCase() ?? ''
  if (['zip', 'tar', 'gz', 'bz2', 'xz', '7z', 'rar'].includes(ext)) return <FileArchive className="size-4.5 text-warning" />
  const kind = getPreviewKind(entry.name)
  switch (kind) {
    case 'image':
      return <FileImage className="size-4.5 text-success" />
    case 'video':
      return <FileVideo className="size-4.5 text-primary" />
    case 'audio':
      return <FileAudio className="size-4.5 text-warning" />
    case 'pdf':
      return <FileText className="size-4.5 text-danger" />
    case 'text':
      return <FileCode2 className="size-4.5 text-fg-muted" />
    default:
      return <File className="size-4.5 text-fg-subtle" />
  }
}

export function FilesPage({ share, path, onNavigate }: FilesPageProps) {
  const qc = useQueryClient()
  const myShares = useQuery(mySharesQuery)
  const shareList = useMemo(() => myShares.data?.items ?? [], [myShares.data])
  const activeShare = share || shareList[0]?.name || ''
  const activeShareMeta = shareList.find((s) => s.name === activeShare)
  const canWrite = activeShareMeta ? activeShareMeta.level === 'write' || activeShareMeta.level === 'upload' : true

  const [view, setView] = useState<'table' | 'grid'>('table')
  const [sort, setSort] = useState('name')
  const [filter, setFilter] = useState('')
  const [selected, setSelected] = useState<Set<string>>(new Set())
  const [dragging, setDragging] = useState(false)

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

  // Upload queue
  const [uploads, setUploads] = useState<UploadTask[]>([])
  const [uploadConflict] = useState<UploadConflict>('rename')
  const controllersRef = useRef<Map<string, AbortController>>(new Map())
  const fileInputRef = useRef<HTMLInputElement>(null)
  const folderInputRef = useRef<HTMLInputElement>(null)

  const entries = useQuery(fileEntriesQuery(activeShare, path, sort))
  const createFolder = useCreateFolder()
  const renameFile = useRenameFile()
  const deleteFiles = useDeleteFiles()
  const copyFiles = useCopyFiles()
  const moveFiles = useMoveFiles()
  const prepareZip = usePrepareZip()

  const filteredItems = useMemo(() => {
    const raw = entries.data?.entries ?? []
    const q = filter.trim().toLowerCase()
    const list = q ? raw.filter((e) => e.name.toLowerCase().includes(q)) : raw
    // Always float directories before files for a natural file-manager feel.
    return [...list].sort((a, b) => Number(b.isDir) - Number(a.isDir))
  }, [entries.data, filter])

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

  // Start a tus upload task
  const startUploadTask = useCallback(
    (task: UploadTask) => {
      const controller = new AbortController()
      controllersRef.current.set(task.id, controller)
      setUploads((prev) => prev.map((t) => (t.id === task.id ? { ...t, status: 'uploading', error: undefined } : t)))

      void runTusUpload(task, controller.signal, (patch) => {
        setUploads((prev) => prev.map((t) => (t.id === task.id ? { ...t, ...patch } : t)))
      })
        .then(() => {
          controllersRef.current.delete(task.id)
          void qc.invalidateQueries({ queryKey: ['files', 'entries', task.share] })
        })
        .catch((err: unknown) => {
          controllersRef.current.delete(task.id)
          if (controller.signal.aborted) return
          setUploads((prev) =>
            prev.map((t) =>
              t.id === task.id
                ? { ...t, status: 'error', speedBps: 0, etaSeconds: null, error: (err as Error).message || 'Upload failed' }
                : t,
            ),
          )
        })
    },
    [qc],
  )

  const enqueueFiles = useCallback(
    async (files: FileList | File[]) => {
      if (!activeShare) return
      const arr = Array.from(files)
      if (arr.length === 0) return

      // Ensure nested subfolders exist when uploading a directory via webkitdirectory.
      const createdDirs = new Set<string>()
      for (const file of arr) {
        const rel = (file as File & { webkitRelativePath?: string }).webkitRelativePath
        if (rel && rel.includes('/')) {
          const segments = rel.split('/').slice(0, -1)
          let acc = path
          for (const seg of segments) {
            acc = joinPath(acc, seg)
            if (!createdDirs.has(acc)) {
              createdDirs.add(acc)
              try {
                await createFolder.mutateAsync({ share: activeShare, path: acc })
              } catch {
                // Folder may already exist; safe to continue uploading into it.
              }
            }
          }
        }
      }

      const newTasks: UploadTask[] = arr.map((file) => {
        const rel = (file as File & { webkitRelativePath?: string }).webkitRelativePath
        const subDir = rel && rel.includes('/') ? rel.split('/').slice(0, -1).join('/') : ''
        const targetDir = subDir ? joinPath(path, subDir) : path
        return {
          id: `${Date.now()}-${Math.random().toString(36).slice(2, 8)}`,
          file,
          share: activeShare,
          dir: targetDir,
          filename: file.name,
          conflict: uploadConflict,
          size: file.size,
          uploaded: 0,
          speedBps: 0,
          etaSeconds: null,
          status: 'queued',
        }
      })

      setUploads((prev) => [...newTasks, ...prev])
      for (const task of newTasks) {
        startUploadTask(task)
      }
    },
    [activeShare, path, uploadConflict, createFolder, startUploadTask],
  )

  const handlePauseUpload = useCallback((id: string) => {
    controllersRef.current.get(id)?.abort()
    controllersRef.current.delete(id)
    setUploads((prev) => prev.map((t) => (t.id === id ? { ...t, status: 'paused', speedBps: 0, etaSeconds: null } : t)))
  }, [])

  const handleResumeUpload = useCallback(
    (id: string) => {
      const task = uploads.find((t) => t.id === id)
      if (task) startUploadTask(task)
    },
    [uploads, startUploadTask],
  )

  const handleCancelUpload = useCallback(
    (id: string) => {
      controllersRef.current.get(id)?.abort()
      controllersRef.current.delete(id)
      const task = uploads.find((t) => t.id === id)
      if (task) void abortTusUpload(task)
      setUploads((prev) => prev.filter((t) => t.id !== id))
    },
    [uploads],
  )

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

  // Keyboard shortcuts: Escape clears selection, Delete deletes selected, F2 renames single selected.
  useEffect(() => {
    const onKeyDown = (e: KeyboardEvent) => {
      const tag = (e.target as HTMLElement | null)?.tagName
      if (tag === 'INPUT' || tag === 'TEXTAREA' || tag === 'SELECT') return
      if (e.key === 'Escape' && selected.size > 0) {
        setSelected(new Set())
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
  }, [selected, canWrite, filteredItems, handleDeleteSelected])

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

  function handleOpenEntry(entry: FileEntry) {
    if (entry.isDir) {
      navigateTo({ share: activeShare, path: joinPath(path, entry.name) })
    } else {
      setPreviewEntry(entry)
    }
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

  function onDragOver(e: DragEvent) {
    if (!canWrite || !activeShare) return
    e.preventDefault()
    if (!dragging) setDragging(true)
  }

  function onDragLeave(e: DragEvent) {
    if (e.currentTarget.contains(e.relatedTarget as Node)) return
    setDragging(false)
  }

  function onDrop(e: DragEvent) {
    e.preventDefault()
    setDragging(false)
    if (!canWrite || !activeShare) return
    if (e.dataTransfer.files?.length) {
      void enqueueFiles(e.dataTransfer.files)
    }
  }

  return (
    <div className="relative flex flex-col gap-5" onDragOver={onDragOver} onDragLeave={onDragLeave} onDrop={onDrop}>
      <PageHeader
        title="File Manager"
        description="Browse, preview, organize, and upload files directly across your SMB shares."
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
                      if (e.target.files) void enqueueFiles(e.target.files)
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
                      if (e.target.files) void enqueueFiles(e.target.files)
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
        <>
          {/* Share selector pills + Breadcrumbs + Toolbar */}
          <Card className="overflow-visible">
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
                  onChange={(e) => setSort(e.target.value)}
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
                    Download ZIP
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
                  {filteredItems.length} {filteredItems.length === 1 ? 'item' : 'items'}
                </span>
              )}
            </div>

            {/* File Listing */}
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
                        ? 'Drag and drop files anywhere here, or click Upload files.'
                        : 'No files are stored in this folder yet.'
                  }
                />
              </div>
            ) : view === 'table' ? (
              <div className="overflow-x-auto">
                <table className="w-full border-collapse text-sm">
                  <thead className="border-b border-border bg-surface-2/40 text-left text-[0.6875rem] font-semibold tracking-wider text-fg-subtle uppercase">
                    <tr>
                      <th className="w-10 px-4 py-2.5">
                        <input
                          type="checkbox"
                          checked={selected.size === filteredItems.length && filteredItems.length > 0}
                          onChange={toggleSelectAll}
                          aria-label="Select all"
                          className="accent-primary"
                        />
                      </th>
                      <th className="px-3 py-2.5">Name</th>
                      <th className="w-28 px-3 py-2.5">Size</th>
                      <th className="w-44 px-3 py-2.5">Modified</th>
                      <th className="w-32 px-4 py-2.5 text-right">Actions</th>
                    </tr>
                  </thead>
                  <tbody className="divide-y divide-border">
                    {filteredItems.map((entry) => {
                      const isChecked = selected.has(entry.name)
                      const entryPath = joinPath(path, entry.name)
                      return (
                        <tr
                          key={entry.name}
                          className={cn(
                            'group transition-colors hover:bg-surface-2/50',
                            isChecked && 'bg-primary-soft/35',
                          )}
                        >
                          <td className="px-4 py-2.5">
                            <input
                              type="checkbox"
                              checked={isChecked}
                              onChange={() => toggleSelect(entry.name)}
                              aria-label={`Select ${entry.name}`}
                              className="accent-primary"
                            />
                          </td>
                          <td className="px-3 py-2.5">
                            <button
                              type="button"
                              onClick={() => handleOpenEntry(entry)}
                              className="flex max-w-full items-center gap-2.5 text-left font-medium text-fg hover:text-primary"
                            >
                              {entryIcon(entry)}
                              <span className="truncate">{entry.name}</span>
                            </button>
                          </td>
                          <td className="px-3 py-2.5 text-xs text-fg-muted tabular-nums">
                            {entry.isDir ? '—' : formatBytes(entry.size)}
                          </td>
                          <td className="px-3 py-2.5 text-xs text-fg-muted">{formatDateTime(entry.modTime)}</td>
                          <td className="px-4 py-2.5">
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
                          </td>
                        </tr>
                      )
                    })}
                  </tbody>
                </table>
              </div>
            ) : (
              <div className="grid gap-3 p-4 sm:grid-cols-2 md:grid-cols-3 lg:grid-cols-4">
                {filteredItems.map((entry) => {
                  const isChecked = selected.has(entry.name)
                  return (
                    <div
                      key={entry.name}
                      className={cn(
                        'group relative flex flex-col gap-3 rounded-xl border border-border bg-bg p-3.5 transition-all hover:border-border-strong',
                        isChecked && 'border-primary bg-primary-soft/30',
                      )}
                    >
                      <div className="flex items-start justify-between gap-2">
                        <input
                          type="checkbox"
                          checked={isChecked}
                          onChange={() => toggleSelect(entry.name)}
                          aria-label={`Select ${entry.name}`}
                          className="mt-0.5 accent-primary"
                        />
                        <button
                          type="button"
                          onClick={() => handleOpenEntry(entry)}
                          className="grid size-10 place-items-center rounded-xl bg-surface shadow-xs ring-1 ring-border"
                        >
                          {entryIcon(entry)}
                        </button>
                        <div className="w-4" />
                      </div>
                      <button
                        type="button"
                        onClick={() => handleOpenEntry(entry)}
                        className="truncate text-center text-xs font-medium text-fg hover:text-primary"
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
            )}
          </Card>
        </>
      )}

      {/* Drag & drop overlay */}
      {dragging && (
        <div className="pointer-events-none fixed inset-4 z-50 grid place-items-center rounded-2xl border-2 border-dashed border-primary bg-primary-soft/85 backdrop-blur-xs">
          <div className="flex flex-col items-center gap-2 text-primary">
            <Upload className="size-10" />
            <div className="text-lg font-semibold">Drop files to upload to /{activeShare}{path ? `/${path}` : ''}</div>
            <div className="text-xs opacity-80">Resumable chunked uploads powered by tus</div>
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
        description="Choose the destination share and folder path."
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
          <FormField label="Destination folder" htmlFor="dest-path" hint="Leave blank for the share's top folder.">
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

      {/* File preview modal */}
      <FilePreviewDialog
        open={Boolean(previewEntry)}
        share={activeShare}
        path={previewEntry ? joinPath(path, previewEntry.name) : ''}
        entry={previewEntry}
        onClose={() => setPreviewEntry(null)}
      />

      {/* Floating resumable upload queue */}
      <UploadQueue
        tasks={uploads}
        onPause={handlePauseUpload}
        onResume={handleResumeUpload}
        onCancel={handleCancelUpload}
        onClearDone={() => setUploads((prev) => prev.filter((t) => t.status !== 'done'))}
      />
    </div>
  )
}
