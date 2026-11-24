import { useQuery } from '@tanstack/react-query'
import { CalendarClock, Play, RefreshCw, RotateCcw } from 'lucide-react'
import { useState, type FormEvent } from 'react'
import { notify } from '@/features/notifications'
import { EmptyState, ErrorState, FormField } from '@/shared/components'
import { cn } from '@/shared/lib/cn'
import { formatRelativeTime } from '@/shared/lib/format'
import { Badge, Button, Card, Dialog, InfoTip, Input, Select, Spinner, Switch } from '@/shared/ui'
import {
  systemTasksQuery,
  useResetTask,
  useRunTaskNow,
  useUpdateTask,
  type TaskStatus,
  type UpdateTaskRequest,
} from '../api'

const WEEKDAYS = ['Sunday', 'Monday', 'Tuesday', 'Wednesday', 'Thursday', 'Friday', 'Saturday']

const PRESET_INTERVALS = [
  { label: 'Every 5 min', minutes: 5 },
  { label: 'Every 15 min', minutes: 15 },
  { label: 'Every 30 min', minutes: 30 },
  { label: 'Hourly', minutes: 60 },
  { label: 'Every 6 hours', minutes: 360 },
  { label: 'Every 12 hours', minutes: 720 },
]

function TaskScheduleBody({ task, onClose }: { task: TaskStatus; onClose: () => void }) {
  const updateTask = useUpdateTask()
  const [enabled, setEnabled] = useState(task.enabled)
  const [mode, setMode] = useState<UpdateTaskRequest['mode']>(task.mode)
  const [everyMinutes, setEveryMinutes] = useState(String(task.everyMinutes || 60))
  const [dailyTime, setDailyTime] = useState(task.dailyTime || '03:00')
  const [weekday, setWeekday] = useState(String(task.weekday ?? 0))

  function handleSubmit(e: FormEvent) {
    e.preventDefault()
    updateTask.mutate(
      {
        name: task.name,
        body: {
          enabled,
          mode,
          everyMinutes: mode === 'interval' ? Number.parseInt(everyMinutes, 10) || 60 : undefined,
          dailyTime: mode !== 'interval' ? dailyTime : undefined,
          weekday: mode === 'weekly' ? Number.parseInt(weekday, 10) || 0 : undefined,
        },
      },
      {
        onSuccess: (updated) => {
          notify.success(
            'Schedule updated',
            updated.enabled ? `${task.name} → ${updated.schedule}` : `${task.name} paused`,
          )
          onClose()
        },
        onError: (err) => notify.error(err),
      },
    )
  }

  return (
    <form onSubmit={handleSubmit} className="flex flex-col gap-4">
      <div className="flex items-center justify-between gap-3 rounded-lg border border-border bg-bg px-3.5 py-2.5">
        <div className="flex items-center gap-1.5">
          <span className="text-sm font-medium text-fg">Run automatically</span>
          <InfoTip content="Turn off to pause this background task completely. You can still trigger it manually with Run now." />
        </div>
        <Switch checked={enabled} onChange={setEnabled} aria-label="Enable task" />
      </div>

      <FormField label="Frequency" htmlFor="task-mode">
        <Select
          id="task-mode"
          value={mode}
          onChange={(e) => setMode(e.target.value as UpdateTaskRequest['mode'])}
        >
          <option value="interval">Interval (every N minutes)</option>
          <option value="daily">Daily (specific time)</option>
          <option value="weekly">Weekly (day & time)</option>
        </Select>
      </FormField>

      {mode === 'interval' && (
        <div className="flex flex-col gap-2">
          <FormField
            label="Interval (minutes)"
            htmlFor="task-interval"
            info="Choose between 1 minute and 10,080 minutes (7 days)."
          >
            <Input
              id="task-interval"
              type="number"
              min={1}
              max={10080}
              value={everyMinutes}
              onChange={(e) => setEveryMinutes(e.target.value)}
              required
            />
          </FormField>
          <div className="flex flex-wrap gap-1.5">
            {PRESET_INTERVALS.map((p) => (
              <button
                key={p.minutes}
                type="button"
                onClick={() => setEveryMinutes(String(p.minutes))}
                className="rounded-md border border-border bg-bg px-2 py-1 text-xs text-fg-muted transition-colors hover:border-border-strong hover:text-fg"
              >
                {p.label}
              </button>
            ))}
          </div>
        </div>
      )}

      {mode === 'weekly' && (
        <FormField label="Day of week" htmlFor="task-weekday">
          <Select id="task-weekday" value={weekday} onChange={(e) => setWeekday(e.target.value)}>
            {WEEKDAYS.map((label, idx) => (
              <option key={label} value={idx}>
                {label}
              </option>
            ))}
          </Select>
        </FormField>
      )}

      {(mode === 'daily' || mode === 'weekly') && (
        <FormField label="Time (24h local)" htmlFor="task-time" info="Server local wall-clock time (HH:MM).">
          <Input
            id="task-time"
            type="time"
            value={dailyTime}
            onChange={(e) => setDailyTime(e.target.value)}
            required
          />
        </FormField>
      )}

      <div className="flex items-center justify-between border-t border-border pt-4">
        <span className="text-xs text-fg-subtle">Default: {task.defaultSchedule}</span>
        <div className="flex items-center gap-2">
          <Button variant="secondary" onClick={onClose}>
            Cancel
          </Button>
          <Button type="submit" loading={updateTask.isPending}>
            Save schedule
          </Button>
        </div>
      </div>
    </form>
  )
}

export function ScheduledTasksPanel({ canEdit }: { canEdit: boolean }) {
  const tasks = useQuery(systemTasksQuery)
  const updateTask = useUpdateTask()
  const runNow = useRunTaskNow()
  const resetTask = useResetTask()
  const [editing, setEditing] = useState<TaskStatus | null>(null)

  function toggleTask(t: TaskStatus, nextEnabled: boolean) {
    updateTask.mutate(
      {
        name: t.name,
        body: {
          enabled: nextEnabled,
          mode: t.mode,
          everyMinutes: t.mode === 'interval' ? t.everyMinutes || 60 : undefined,
          dailyTime: t.mode !== 'interval' ? t.dailyTime || '03:00' : undefined,
          weekday: t.mode === 'weekly' ? t.weekday ?? 0 : undefined,
        },
      },
      {
        onSuccess: () => notify.success(nextEnabled ? 'Task enabled' : 'Task paused', t.name),
        onError: (err) => notify.error(err),
      },
    )
  }

  return (
    <>
      <Card>
        <div className="flex items-center justify-between border-b border-border px-5 py-3">
          <div className="flex items-center gap-1.5">
            <h2 className="text-sm font-semibold">Scheduled Tasks</h2>
            <InfoTip content="Toggle individual background tasks on/off, customize their frequency or run time, or trigger them on demand." />
          </div>
          <Button variant="secondary" size="sm" onClick={() => void tasks.refetch()}>
            <RefreshCw className={cn('size-3.5', tasks.isFetching && 'animate-spin')} />
            Refresh
          </Button>
        </div>

        {tasks.isLoading ? (
          <div className="flex justify-center py-10">
            <Spinner />
          </div>
        ) : tasks.error ? (
          <div className="p-5">
            <ErrorState error={tasks.error} onRetry={() => void tasks.refetch()} />
          </div>
        ) : (tasks.data?.items ?? []).length === 0 ? (
          <div className="p-8">
            <EmptyState title="No scheduled tasks" />
          </div>
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full border-collapse text-sm">
              <thead className="border-b border-border bg-surface-2/50 text-left text-[0.6875rem] font-semibold tracking-wider text-fg-subtle uppercase">
                <tr>
                  <th className="w-16 px-4 py-2.5">Active</th>
                  <th className="px-4 py-2.5">Task</th>
                  <th className="px-4 py-2.5">Schedule</th>
                  <th className="px-4 py-2.5">Status</th>
                  <th className="px-4 py-2.5">Last run</th>
                  <th className="px-4 py-2.5">Next run</th>
                  <th className="px-4 py-2.5 text-right">Actions</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-border">
                {(tasks.data?.items ?? []).map((t) => (
                  <tr key={t.name} className={cn('hover:bg-surface-2/40', !t.enabled && 'opacity-70')}>
                    <td className="px-4 py-3">
                      <Switch
                        checked={t.enabled}
                        disabled={!canEdit || updateTask.isPending}
                        onChange={(next) => toggleTask(t, next)}
                        aria-label={`Toggle ${t.name}`}
                      />
                    </td>
                    <td className="px-4 py-3">
                      <div className="flex items-center gap-1.5">
                        <span className="font-medium text-fg">{t.name}</span>
                        {t.customized && <Badge tone="info">custom</Badge>}
                      </div>
                      {t.lastError && <div className="mt-0.5 text-xs text-danger">{t.lastError}</div>}
                    </td>
                    <td className="px-4 py-3 font-mono text-xs text-fg-muted">{t.schedule}</td>
                    <td className="px-4 py-3">
                      <Badge
                        tone={t.running ? 'info' : !t.enabled ? 'neutral' : t.lastError ? 'danger' : 'success'}
                        dot
                      >
                        {t.running ? 'Running' : !t.enabled ? 'Paused' : t.lastError ? 'Error' : 'Idle'}
                      </Badge>
                    </td>
                    <td className="px-4 py-3 text-xs text-fg-muted tabular-nums">
                      {formatRelativeTime(t.lastEnd || t.lastStart || '')}
                      {t.runs > 0 && <span className="ml-1 text-fg-subtle">({t.runs})</span>}
                    </td>
                    <td className="px-4 py-3 text-xs text-fg-muted">
                      {t.enabled ? formatRelativeTime(t.nextRun || '') : 'Paused'}
                    </td>
                    <td className="px-4 py-3">
                      {canEdit && (
                        <div className="flex items-center justify-end gap-1">
                          <Button
                            variant="ghost"
                            size="icon"
                            className="size-8"
                            title="Run now"
                            aria-label={`Run ${t.name} now`}
                            disabled={t.running}
                            onClick={() =>
                              runNow.mutate(t.name, {
                                onSuccess: () => notify.info('Task triggered', `Running ${t.name} now.`),
                                onError: (err) => notify.error(err),
                              })
                            }
                          >
                            <Play className="size-3.5" />
                          </Button>
                          <Button
                            variant="ghost"
                            size="icon"
                            className="size-8"
                            title="Edit schedule"
                            aria-label={`Edit schedule for ${t.name}`}
                            onClick={() => setEditing(t)}
                          >
                            <CalendarClock className="size-3.5" />
                          </Button>
                          {t.customized && (
                            <Button
                              variant="ghost"
                              size="icon"
                              className="size-8 text-fg-muted hover:text-fg"
                              title={`Reset to default (${t.defaultSchedule})`}
                              aria-label={`Reset ${t.name} to default`}
                              onClick={() =>
                                resetTask.mutate(t.name, {
                                  onSuccess: () => notify.success('Schedule reset', `${t.name} restored to default.`),
                                  onError: (err) => notify.error(err),
                                })
                              }
                            >
                              <RotateCcw className="size-3.5" />
                            </Button>
                          )}
                        </div>
                      )}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </Card>

      <Dialog
        open={Boolean(editing)}
        onClose={() => setEditing(null)}
        size="sm"
        title={editing ? `Schedule — ${editing.name}` : 'Edit schedule'}
      >
        {editing && <TaskScheduleBody key={editing.name} task={editing} onClose={() => setEditing(null)} />}
      </Dialog>
    </>
  )
}
