import { useQueryClient } from '@tanstack/react-query'
import {
  AlertCircle,
  CheckCircle2,
  ChevronDown,
  ChevronUp,
  Pause,
  Play,
  RotateCcw,
  UploadCloud,
  X,
} from 'lucide-react'
import { useEffect, useState } from 'react'
import { cn } from '@/shared/lib/cn'
import { formatBytes } from '@/shared/lib/format'
import { Badge, Button } from '@/shared/ui'
import type { UploadBatchSnapshot, UploadItemSnapshot } from '../tus.worker'
import { bindUploadQueryClient, uploadActions, useUploadStore } from '../upload-store'

type DetailFilter = 'active' | 'failed' | 'queued' | 'done'

function formatEta(seconds: number | null): string {
  if (seconds === null || !Number.isFinite(seconds)) return ''
  if (seconds < 60) return `${seconds}s left`
  const m = Math.floor(seconds / 60)
  const s = seconds % 60
  if (m < 60) return `${m}m ${s}s left`
  const h = Math.floor(m / 60)
  return `${h}h ${m % 60}m left`
}

function ItemRow({ item }: { item: UploadItemSnapshot }) {
  const pct = item.size > 0 ? Math.min(100, Math.round((item.uploaded / item.size) * 100)) : item.status === 'done' ? 100 : 0
  return (
    <div className="flex flex-col gap-1 py-1.5">
      <div className="flex items-center justify-between gap-2">
        <span className="truncate text-[0.75rem] text-fg" title={item.filename}>
          {item.filename}
        </span>
        <div className="flex shrink-0 items-center gap-1 text-[0.6875rem] text-fg-subtle tabular-nums">
          {item.status === 'uploading' && <span>{pct}%</span>}
          {item.status === 'uploading' && (
            <Button
              variant="ghost"
              size="icon"
              className="size-5"
              onClick={() => uploadActions.pauseItem(item.id)}
              aria-label={`Pause ${item.filename}`}
            >
              <Pause className="size-2.5" />
            </Button>
          )}
          {(item.status === 'paused' || item.status === 'error') && (
            <Button
              variant="ghost"
              size="icon"
              className="size-5"
              onClick={() => uploadActions.resumeItem(item.id)}
              aria-label={`Resume ${item.filename}`}
            >
              <Play className="size-2.5" />
            </Button>
          )}
          {item.status !== 'done' && (
            <Button
              variant="ghost"
              size="icon"
              className="size-5 text-fg-subtle hover:text-danger"
              onClick={() => uploadActions.cancelItem(item.id)}
              aria-label={`Cancel ${item.filename}`}
            >
              <X className="size-2.5" />
            </Button>
          )}
        </div>
      </div>
      {item.status === 'uploading' && (
        <div className="h-1 w-full overflow-hidden rounded-full bg-surface-3">
          <div className="h-full bg-primary transition-all duration-150" style={{ width: `${pct}%` }} />
        </div>
      )}
      {item.status === 'error' && item.error && <span className="text-[0.6875rem] text-danger">{item.error}</span>}
    </div>
  )
}

function BatchCard({ batch }: { batch: UploadBatchSnapshot }) {
  const [expanded, setExpanded] = useState(false)
  const [filter, setFilter] = useState<DetailFilter>(batch.failedFiles > 0 ? 'failed' : 'active')

  const pct =
    batch.totalBytes > 0
      ? Math.min(100, Math.round((batch.uploadedBytes / batch.totalBytes) * 100))
      : batch.totalFiles > 0
        ? Math.round((batch.doneFiles / batch.totalFiles) * 100)
        : 0

  const target = `/${batch.share}${batch.baseDir ? `/${batch.baseDir}` : ''}`
  const isDone = batch.status === 'done'
  const hasErrors = batch.failedFiles > 0

  const detailItems =
    filter === 'active'
      ? batch.activeItems
      : filter === 'failed'
        ? batch.failedItems
        : filter === 'queued'
          ? batch.queuedPreview
          : batch.donePreview

  const detailTotal =
    filter === 'active'
      ? batch.activeFiles
      : filter === 'failed'
        ? batch.failedFiles
        : filter === 'queued'
          ? batch.queuedFiles + batch.pausedFiles
          : batch.doneFiles

  return (
    <div className="flex flex-col gap-2 px-3.5 py-3">
      {/* Batch title + controls */}
      <div className="flex items-start justify-between gap-2">
        <div className="min-w-0">
          <div className="flex items-center gap-1.5">
            {isDone ? (
              <CheckCircle2 className="size-3.5 shrink-0 text-success" />
            ) : hasErrors && batch.status === 'done_with_errors' ? (
              <AlertCircle className="size-3.5 shrink-0 text-danger" />
            ) : null}
            <span className="truncate text-xs font-semibold text-fg" title={batch.label}>
              {batch.label}
            </span>
          </div>
          <div className="truncate font-mono text-[0.6875rem] text-fg-subtle">{target}</div>
        </div>

        <div className="flex shrink-0 items-center gap-0.5">
          {batch.status === 'uploading' && (
            <Button
              variant="ghost"
              size="icon"
              className="size-6"
              title="Pause batch"
              aria-label="Pause batch"
              onClick={() => uploadActions.pauseBatch(batch.id)}
            >
              <Pause className="size-3" />
            </Button>
          )}
          {batch.status === 'paused' && (
            <Button
              variant="ghost"
              size="icon"
              className="size-6"
              title="Resume batch"
              aria-label="Resume batch"
              onClick={() => uploadActions.resumeBatch(batch.id)}
            >
              <Play className="size-3" />
            </Button>
          )}
          {hasErrors && (
            <Button
              variant="ghost"
              size="icon"
              className="size-6 text-warning hover:text-fg"
              title="Retry failed files"
              aria-label="Retry failed files"
              onClick={() => uploadActions.retryBatchFailed(batch.id)}
            >
              <RotateCcw className="size-3" />
            </Button>
          )}
          <Button
            variant="ghost"
            size="icon"
            className="size-6 text-fg-subtle hover:text-danger"
            title={isDone ? 'Dismiss' : 'Cancel batch'}
            aria-label={isDone ? 'Dismiss batch' : 'Cancel batch'}
            onClick={() => uploadActions.cancelBatch(batch.id)}
          >
            <X className="size-3" />
          </Button>
        </div>
      </div>

      {/* Single smooth aggregate progress bar */}
      <div className="h-1.5 w-full overflow-hidden rounded-full bg-surface-3">
        <div
          className={cn(
            'h-full transition-all duration-150',
            isDone ? 'bg-success' : batch.status === 'done_with_errors' ? 'bg-danger' : 'bg-primary',
          )}
          style={{ width: `${pct}%` }}
        />
      </div>

      {/* Aggregate file count, bytes, speed, ETA */}
      <div className="flex flex-wrap items-center justify-between gap-2 text-[0.6875rem] text-fg-muted tabular-nums">
        <span>
          {batch.doneFiles.toLocaleString()} / {batch.totalFiles.toLocaleString()} files ·{' '}
          {formatBytes(batch.uploadedBytes)} / {formatBytes(batch.totalBytes)}
        </span>
        <span>
          {batch.status === 'uploading' && batch.speedBps > 0 && (
            <>
              {formatBytes(batch.speedBps)}/s · {formatEta(batch.etaSeconds)}
            </>
          )}
          {batch.status === 'paused' && <Badge tone="warning">Paused</Badge>}
          {batch.status === 'done' && <span className="text-success">Complete</span>}
          {batch.status === 'done_with_errors' && (
            <span className="text-danger">{batch.failedFiles.toLocaleString()} failed</span>
          )}
        </span>
      </div>

      {/* Compact inline active items (max 3) when collapsed */}
      {!expanded && batch.activeItems.length > 0 && (
        <div className="divide-y divide-border/60 rounded-lg border border-border/70 bg-bg px-2.5 py-0.5">
          {batch.activeItems.map((it) => (
            <ItemRow key={it.id} item={it} />
          ))}
        </div>
      )}

      {/* Expandable capped drill-down for large batches */}
      {batch.totalFiles > 1 && (
        <div>
          <button
            type="button"
            onClick={() => setExpanded((v) => !v)}
            className="text-[0.6875rem] font-medium text-primary hover:underline"
          >
            {expanded ? 'Hide file details' : hasErrors ? `Inspect ${batch.failedFiles} failed & details` : 'Show file details'}
          </button>

          {expanded && (
            <div className="mt-2 flex flex-col gap-2 rounded-lg border border-border bg-bg p-2.5">
              <div className="flex flex-wrap gap-1">
                {(
                  [
                    { id: 'active', label: `Active (${batch.activeFiles})` },
                    { id: 'failed', label: `Failed (${batch.failedFiles})` },
                    { id: 'queued', label: `Queued (${batch.queuedFiles + batch.pausedFiles})` },
                    { id: 'done', label: `Done (${batch.doneFiles})` },
                  ] as const
                ).map((t) => (
                  <button
                    key={t.id}
                    type="button"
                    onClick={() => setFilter(t.id)}
                    className={cn(
                      'rounded px-2 py-0.5 text-[0.6875rem] font-medium tabular-nums transition-colors',
                      filter === t.id ? 'bg-primary-soft text-primary' : 'text-fg-muted hover:text-fg',
                    )}
                  >
                    {t.label}
                  </button>
                ))}
              </div>

              {detailItems.length === 0 ? (
                <div className="py-2 text-center text-[0.6875rem] text-fg-subtle">None</div>
              ) : (
                <div className="max-h-40 divide-y divide-border/60 overflow-y-auto">
                  {detailItems.map((it) => (
                    <ItemRow key={it.id} item={it} />
                  ))}
                  {detailTotal > detailItems.length && (
                    <div className="pt-1.5 text-center text-[0.6875rem] text-fg-subtle">
                      +{(detailTotal - detailItems.length).toLocaleString()} more
                    </div>
                  )}
                </div>
              )}
            </div>
          )}
        </div>
      )}
    </div>
  )
}

/**
 * Global floating batch upload tray. Mounted once in the app shell so uploads
 * continue seamlessly even while navigating between pages.
 */
export function UploadTray() {
  const qc = useQueryClient()
  const batches = useUploadStore((s) => s.batches)
  const [collapsed, setCollapsed] = useState(false)

  useEffect(() => {
    bindUploadQueryClient(qc)
  }, [qc])

  if (batches.length === 0) return null

  const activeBatches = batches.filter((b) => b.status === 'uploading').length
  const totalFilesActive = batches.reduce((sum, b) => sum + (b.status === 'uploading' ? b.totalFiles - b.doneFiles : 0), 0)
  const hasFinished = batches.some((b) => b.status === 'done')

  return (
    <div className="fixed right-5 bottom-5 z-40 w-[min(25rem,calc(100vw-2rem))] overflow-hidden rounded-xl border border-border-strong bg-surface shadow-pop">
      <div className="flex items-center justify-between gap-2 border-b border-border bg-surface-2/70 px-3.5 py-2.5">
        <div className="flex items-center gap-2">
          <UploadCloud className="size-4 text-primary" />
          <span className="text-xs font-semibold text-fg tabular-nums">
            {activeBatches > 0
              ? `Uploading ${totalFilesActive.toLocaleString()} remaining`
              : `${batches.length} upload ${batches.length === 1 ? 'batch' : 'batches'}`}
          </span>
        </div>
        <div className="flex items-center gap-1">
          {hasFinished && (
            <Button variant="ghost" size="sm" className="h-6 px-2 text-[0.7rem]" onClick={uploadActions.clearFinished}>
              Clear done
            </Button>
          )}
          <Button
            variant="ghost"
            size="icon"
            className="size-6"
            onClick={() => setCollapsed((c) => !c)}
            aria-label={collapsed ? 'Expand uploads' : 'Collapse uploads'}
          >
            {collapsed ? <ChevronUp className="size-3.5" /> : <ChevronDown className="size-3.5" />}
          </Button>
        </div>
      </div>

      {!collapsed && (
        <div className="max-h-[26rem] divide-y divide-border overflow-y-auto">
          {batches.map((batch) => (
            <BatchCard key={batch.id} batch={batch} />
          ))}
        </div>
      )}
    </div>
  )
}
