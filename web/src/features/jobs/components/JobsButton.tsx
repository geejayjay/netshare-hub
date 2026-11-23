import { useQuery, useQueryClient } from '@tanstack/react-query'
import { Layers, XCircle } from 'lucide-react'
import { useCallback, useState } from 'react'
import { notify } from '@/features/notifications'
import { useSSE } from '@/shared/api'
import { EmptyState, ErrorState } from '@/shared/components'
import { formatRelativeTime } from '@/shared/lib/format'
import { Badge, Button, Drawer, Spinner, type Tone } from '@/shared/ui'
import { jobsQuery, useCancelJob, type Job } from '../api'

function jobTone(status: Job['status']): Tone {
  switch (status) {
    case 'succeeded':
      return 'success'
    case 'failed':
      return 'danger'
    case 'running':
      return 'info'
    case 'cancelled':
      return 'neutral'
    default:
      return 'warning'
  }
}

export function JobsButton() {
  const qc = useQueryClient()
  const [open, setOpen] = useState(false)
  const jobs = useQuery(jobsQuery)
  const cancelJob = useCancelJob()

  const onJobEvent = useCallback(() => {
    void qc.invalidateQueries({ queryKey: ['jobs'] })
  }, [qc])

  useSSE<Job>('job', onJobEvent, true)

  const items = jobs.data?.items ?? []
  const activeCount = items.filter((j) => j.status === 'running' || j.status === 'queued').length

  return (
    <>
      <Button
        variant="ghost"
        size="sm"
        className="h-8 gap-1.5 px-2 text-xs text-fg-muted hover:text-fg"
        aria-label="Background jobs"
        onClick={() => setOpen(true)}
      >
        {activeCount > 0 ? <Spinner className="size-3.5 text-primary" /> : <Layers className="size-4" />}
        {activeCount > 0 && <span className="font-medium text-fg tabular-nums">{activeCount}</span>}
      </Button>

      <Drawer
        open={open}
        onClose={() => setOpen(false)}
        title="Background jobs"
        description="File operations, permission fixes, and maintenance tasks."
      >
        {jobs.isLoading ? (
          <div className="flex justify-center py-12">
            <Spinner />
          </div>
        ) : jobs.error ? (
          <ErrorState error={jobs.error} onRetry={() => void jobs.refetch()} />
        ) : items.length === 0 ? (
          <EmptyState title="No jobs yet" description="Long-running file operations and maintenance jobs appear here." />
        ) : (
          <div className="flex flex-col gap-3">
            {items.map((job) => {
              const pct = job.total > 0 ? Math.min(100, Math.round((job.done / job.total) * 100)) : null
              const canCancel = (job.status === 'running' || job.status === 'queued') && !job.cancelRequested
              return (
                <div key={job.id} className="flex flex-col gap-2 rounded-lg border border-border bg-bg p-3.5">
                  <div className="flex items-start justify-between gap-2">
                    <div className="min-w-0">
                      <div className="truncate text-sm font-medium text-fg">{job.title || job.kind}</div>
                      <div className="text-[0.6875rem] text-fg-subtle">{job.kind}</div>
                    </div>
                    <Badge tone={jobTone(job.status)} dot>
                      {job.cancelRequested && job.status === 'running' ? 'cancelling' : job.status}
                    </Badge>
                  </div>

                  {job.message && <p className="text-xs text-fg-muted">{job.message}</p>}

                  {(job.status === 'running' || pct !== null) && job.status !== 'succeeded' && (
                    <div className="flex flex-col gap-1">
                      <div className="h-1.5 w-full overflow-hidden rounded-full bg-surface-3">
                        <div
                          className="h-full rounded-full bg-primary transition-all duration-300"
                          style={{ width: `${pct ?? 35}%` }}
                        />
                      </div>
                      {pct !== null && (
                        <div className="flex justify-between text-[0.6875rem] text-fg-subtle tabular-nums">
                          <span>
                            {job.done} / {job.total}
                          </span>
                          <span>{pct}%</span>
                        </div>
                      )}
                    </div>
                  )}

                  {job.errorMessage && (
                    <p className="rounded-md bg-danger-soft px-2.5 py-1.5 text-xs text-danger">{job.errorMessage}</p>
                  )}

                  <div className="flex items-center justify-between gap-2 pt-0.5 text-[0.6875rem] text-fg-subtle">
                    <span>{formatRelativeTime(job.finishedAt || job.startedAt || job.createdAt)}</span>
                    {canCancel && (
                      <Button
                        variant="ghost"
                        size="sm"
                        className="h-6 px-2 text-xs text-danger hover:bg-danger-soft"
                        onClick={() =>
                          cancelJob.mutate(job.id, {
                            onSuccess: () => notify.info('Cancellation requested'),
                            onError: (err) => notify.error(err),
                          })
                        }
                      >
                        <XCircle className="size-3.5" />
                        Cancel
                      </Button>
                    )}
                  </div>
                </div>
              )
            })}
          </div>
        )}
      </Drawer>
    </>
  )
}
