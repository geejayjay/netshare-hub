import { CheckCircle2, ChevronDown, ChevronUp, Pause, Play, Trash2, UploadCloud, X, XCircle } from 'lucide-react'
import { useState } from 'react'
import { formatBytes } from '@/shared/lib/format'
import { Badge, Button } from '@/shared/ui'
import type { UploadTask } from '../tus'

interface UploadQueueProps {
  tasks: UploadTask[]
  onPause: (id: string) => void
  onResume: (id: string) => void
  onCancel: (id: string) => void
  onClearDone: () => void
}

function formatEta(seconds: number | null): string {
  if (seconds === null || !Number.isFinite(seconds)) return ''
  if (seconds < 60) return `${seconds}s left`
  const m = Math.floor(seconds / 60)
  const s = seconds % 60
  return `${m}m ${s}s left`
}

export function UploadQueue({ tasks, onPause, onResume, onCancel, onClearDone }: UploadQueueProps) {
  const [collapsed, setCollapsed] = useState(false)

  if (tasks.length === 0) return null

  const active = tasks.filter((t) => t.status === 'uploading' || t.status === 'queued').length
  const done = tasks.filter((t) => t.status === 'done').length

  return (
    <div className="fixed right-5 bottom-5 z-40 w-[min(24rem,calc(100vw-2rem))] overflow-hidden rounded-xl border border-border-strong bg-surface shadow-pop">
      <div className="flex items-center justify-between gap-2 border-b border-border bg-surface-2/60 px-3.5 py-2.5">
        <div className="flex items-center gap-2">
          <UploadCloud className="size-4 text-primary" />
          <span className="text-xs font-semibold text-fg">
            {active > 0 ? `Uploading ${active} file${active === 1 ? '' : 's'}` : `Uploads (${done}/${tasks.length})`}
          </span>
        </div>
        <div className="flex items-center gap-1">
          {done > 0 && (
            <Button variant="ghost" size="sm" className="h-6 px-2 text-[0.7rem]" onClick={onClearDone}>
              Clear done
            </Button>
          )}
          <Button
            variant="ghost"
            size="icon"
            className="size-6"
            onClick={() => setCollapsed((c) => !c)}
            aria-label={collapsed ? 'Expand upload queue' : 'Collapse upload queue'}
          >
            {collapsed ? <ChevronUp className="size-3.5" /> : <ChevronDown className="size-3.5" />}
          </Button>
        </div>
      </div>

      {!collapsed && (
        <div className="max-h-72 divide-y divide-border overflow-y-auto">
          {tasks.map((t) => {
            const pct = t.size > 0 ? Math.min(100, Math.round((t.uploaded / t.size) * 100)) : t.status === 'done' ? 100 : 0
            return (
              <div key={t.id} className="flex flex-col gap-1.5 px-3.5 py-2.5">
                <div className="flex items-center justify-between gap-2">
                  <span className="truncate text-xs font-medium text-fg" title={t.filename}>
                    {t.filename}
                  </span>
                  <div className="flex shrink-0 items-center gap-1">
                    {t.status === 'uploading' && (
                      <Button variant="ghost" size="icon" className="size-6" onClick={() => onPause(t.id)} aria-label="Pause">
                        <Pause className="size-3" />
                      </Button>
                    )}
                    {(t.status === 'paused' || t.status === 'error') && (
                      <Button variant="ghost" size="icon" className="size-6" onClick={() => onResume(t.id)} aria-label="Resume">
                        <Play className="size-3" />
                      </Button>
                    )}
                    {t.status !== 'done' ? (
                      <Button
                        variant="ghost"
                        size="icon"
                        className="size-6 text-fg-subtle hover:text-danger"
                        onClick={() => onCancel(t.id)}
                        aria-label="Cancel upload"
                      >
                        <X className="size-3" />
                      </Button>
                    ) : (
                      <CheckCircle2 className="size-3.5 text-success" />
                    )}
                  </div>
                </div>

                <div className="h-1.5 w-full overflow-hidden rounded-full bg-surface-3">
                  <div
                    className={
                      t.status === 'error'
                        ? 'h-full bg-danger'
                        : t.status === 'done'
                          ? 'h-full bg-success'
                          : 'h-full bg-primary transition-all duration-200'
                    }
                    style={{ width: `${pct}%` }}
                  />
                </div>

                <div className="flex items-center justify-between gap-2 text-[0.6875rem] text-fg-subtle tabular-nums">
                  <span>
                    {formatBytes(t.uploaded)} / {formatBytes(t.size)}
                  </span>
                  <span>
                    {t.status === 'uploading' && t.speedBps > 0 && (
                      <>
                        {formatBytes(t.speedBps)}/s · {formatEta(t.etaSeconds)}
                      </>
                    )}
                    {t.status === 'paused' && <Badge tone="warning">Paused</Badge>}
                    {t.status === 'queued' && 'Queued'}
                    {t.status === 'done' && 'Complete'}
                    {t.status === 'error' && (
                      <span className="inline-flex items-center gap-1 text-danger">
                        <XCircle className="size-3" /> {t.error || 'Failed'}
                      </span>
                    )}
                    {t.status === 'cancelled' && (
                      <span className="inline-flex items-center gap-1">
                        <Trash2 className="size-3" /> Cancelled
                      </span>
                    )}
                  </span>
                </div>
              </div>
            )
          })}
        </div>
      )}
    </div>
  )
}
