import { useQuery } from '@tanstack/react-query'
import {
  Check,
  Copy,
  Eye,
  FolderSync,
  History,
  KeyRound,
  Link2,
  Pencil,
  Play,
  Plus,
  Server,
  Trash2,
} from 'lucide-react'
import { useState, type FormEvent } from 'react'
import { notify } from '@/features/notifications'
import { sharesQuery } from '@/features/shares'
import { errorMessage } from '@/shared/api'
import { EmptyState, ErrorState, FormField, PageHeader } from '@/shared/components'
import { formatBytes, formatDateTime, formatRelativeTime } from '@/shared/lib/format'
import {
  Alert,
  Badge,
  Button,
  Card,
  Dialog,
  Drawer,
  InfoTip,
  Input,
  Select,
  Spinner,
  Switch,
  Tabs,
  Textarea,
  type Tone,
} from '@/shared/ui'
import {
  replicationJobRunsQuery,
  replicationJobsQuery,
  replicationPeersQuery,
  replicationReceiversQuery,
  replicationStatusQuery,
  useCreatePeer,
  useCreateReceiver,
  useCreateReplicationJob,
  useDeletePeer,
  useDeleteReceiver,
  useDeleteReplicationJob,
  useNewPairingCode,
  usePreviewReplicationJob,
  useRunReplicationJob,
  useUpdateReplicationJob,
  type ReplicationJob,
  type ReplicationPeer,
} from '../api'

type RepTab = 'jobs' | 'peers' | 'receivers'
const WEEKDAYS = ['Sunday', 'Monday', 'Tuesday', 'Wednesday', 'Thursday', 'Friday', 'Saturday']

function runTone(status?: string): Tone {
  switch (status) {
    case 'done':
      return 'success'
    case 'running':
    case 'queued':
      return 'info'
    case 'partial':
      return 'warning'
    case 'failed':
      return 'danger'
    default:
      return 'neutral'
  }
}

function minuteToHHMM(mins: number): string {
  const clamped = Math.max(0, Math.min(1439, mins || 0))
  const h = String(Math.floor(clamped / 60)).padStart(2, '0')
  const m = String(clamped % 60).padStart(2, '0')
  return `${h}:${m}`
}

function hhmmToMinute(hhmm: string): number {
  const [h = 0, m = 0] = hhmm.split(':').map((n) => Number.parseInt(n, 10) || 0)
  return Math.max(0, Math.min(1439, h * 60 + m))
}

function JobEditorBody({
  job,
  peers,
  onClose,
}: {
  job?: ReplicationJob | null
  peers: ReplicationPeer[]
  onClose: () => void
}) {
  const isEdit = Boolean(job)
  const shares = useQuery(sharesQuery({ pageSize: 200 }))
  const createJob = useCreateReplicationJob()
  const updateJob = useUpdateReplicationJob()

  const [name, setName] = useState(job?.name ?? '')
  const [shareId, setShareId] = useState(job ? String(job.shareId) : '')
  const [destType, setDestType] = useState<'local' | 'peer'>(job?.peerId ? 'peer' : 'local')
  const [localPath, setLocalPath] = useState(job?.localPath ?? '')
  const [peerId, setPeerId] = useState(job?.peerId ? String(job.peerId) : '')
  const [schedule, setSchedule] = useState<ReplicationJob['schedule']>(job?.schedule ?? 'daily')
  const [weekday, setWeekday] = useState(String(job?.weekday ?? 0))
  const [timeStr, setTimeStr] = useState(minuteToHHMM(job?.atMinute ?? 180))
  const [enabled, setEnabled] = useState(job?.enabled ?? true)
  const [mirror, setMirror] = useState(job?.mirror ?? false)
  const [bandwidthKiB, setBandwidthKiB] = useState(String(job?.bandwidthKiB ?? 0))
  const [excludesText, setExcludesText] = useState((job?.excludes ?? []).join('\n'))
  const [err, setErr] = useState<unknown>(null)

  const pending = createJob.isPending || updateJob.isPending

  function handleSubmit(e: FormEvent) {
    e.preventDefault()
    setErr(null)
    const excludes = excludesText
      .split('\n')
      .map((s) => s.trim())
      .filter(Boolean)
    const common = {
      name: name.trim(),
      localPath: destType === 'local' ? localPath.trim() : undefined,
      peerId: destType === 'peer' && peerId ? Number(peerId) : undefined,
      schedule,
      weekday: schedule === 'weekly' ? Number(weekday) : 0,
      atMinute: schedule === 'daily' || schedule === 'weekly' ? hhmmToMinute(timeStr) : 0,
      enabled,
      mirror,
      bandwidthKiB: Math.max(0, Number.parseInt(bandwidthKiB, 10) || 0),
      excludes,
    }

    if (isEdit && job) {
      updateJob.mutate(
        { id: job.id, body: common },
        {
          onSuccess: () => {
            notify.success('Replication job updated', job.name)
            onClose()
          },
          onError: (e2) => setErr(e2),
        },
      )
      return
    }

    createJob.mutate(
      {
        ...common,
        shareId: Number(shareId),
      },
      {
        onSuccess: (created) => {
          notify.success('Replication job created', created.name)
          onClose()
        },
        onError: (e2) => setErr(e2),
      },
    )
  }

  return (
    <form onSubmit={handleSubmit} className="flex flex-col gap-4">
      {err !== null && <Alert tone="danger" title={errorMessage(err)} />}

      <div className="grid gap-4 sm:grid-cols-2">
        <FormField label="Job name" htmlFor="rep-name">
          <Input id="rep-name" value={name} onChange={(e) => setName(e.target.value)} placeholder="Nightly offsite" required />
        </FormField>

        <FormField label="Source share" htmlFor="rep-share">
          <Select
            id="rep-share"
            value={shareId}
            disabled={isEdit}
            onChange={(e) => setShareId(e.target.value)}
            required
          >
            <option value="">Select share…</option>
            {(shares.data?.items ?? []).map((s) => (
              <option key={s.id} value={String(s.id)}>
                {s.name} ({s.path})
              </option>
            ))}
          </Select>
        </FormField>
      </div>

      <div className="grid gap-4 sm:grid-cols-2">
        <FormField label="Destination type" htmlFor="rep-dest-type">
          <Select
            id="rep-dest-type"
            value={destType}
            onChange={(e) => setDestType(e.target.value as 'local' | 'peer')}
          >
            <option value="local">Local disk / folder</option>
            <option value="peer">Paired remote peer</option>
          </Select>
        </FormField>

        {destType === 'local' ? (
          <FormField
            label="Local path"
            htmlFor="rep-local"
            info="A folder on another local disk (must not be or contain an active share)."
          >
            <Input
              id="rep-local"
              value={localPath}
              onChange={(e) => setLocalPath(e.target.value)}
              placeholder="/mnt/backup-disk/media"
              required
            />
          </FormField>
        ) : (
          <FormField label="Remote peer" htmlFor="rep-peer">
            <Select id="rep-peer" value={peerId} onChange={(e) => setPeerId(e.target.value)} required>
              <option value="">Select paired peer…</option>
              {peers.map((p) => (
                <option key={p.id} value={String(p.id)}>
                  {p.name} ({p.host})
                </option>
              ))}
            </Select>
          </FormField>
        )}
      </div>

      <div className="grid gap-4 sm:grid-cols-3">
        <FormField label="Schedule" htmlFor="rep-schedule">
          <Select
            id="rep-schedule"
            value={schedule}
            onChange={(e) => setSchedule(e.target.value as ReplicationJob['schedule'])}
          >
            <option value="manual">Manual only</option>
            <option value="hourly">Hourly</option>
            <option value="daily">Daily</option>
            <option value="weekly">Weekly</option>
          </Select>
        </FormField>

        {schedule === 'weekly' && (
          <FormField label="Day" htmlFor="rep-weekday">
            <Select id="rep-weekday" value={weekday} onChange={(e) => setWeekday(e.target.value)}>
              {WEEKDAYS.map((d, idx) => (
                <option key={d} value={idx}>
                  {d}
                </option>
              ))}
            </Select>
          </FormField>
        )}

        {(schedule === 'daily' || schedule === 'weekly') && (
          <FormField label="Time (24h)" htmlFor="rep-time">
            <Input id="rep-time" type="time" value={timeStr} onChange={(e) => setTimeStr(e.target.value)} required />
          </FormField>
        )}
      </div>

      <div className="grid gap-3 sm:grid-cols-2">
        <div className="flex items-center justify-between gap-2 rounded-lg border border-border bg-bg px-3.5 py-2.5">
          <div className="flex items-center gap-1.5">
            <span className="text-sm font-medium text-fg">Scheduled runs</span>
            <InfoTip content="Turn off to pause automatic runs; manual runs and dry-run previews still work." />
          </div>
          <Switch checked={enabled} onChange={setEnabled} aria-label="Enable scheduled runs" />
        </div>

        <div className="flex items-center justify-between gap-2 rounded-lg border border-border bg-bg px-3.5 py-2.5">
          <div className="flex items-center gap-1.5">
            <span className="text-sm font-medium text-fg">Mirror deletions</span>
            <InfoTip content="Deletes files at the destination that were deleted from the share. Leave off to keep deleted files in the replica." />
          </div>
          <Switch checked={mirror} onChange={setMirror} aria-label="Mirror deletions" />
        </div>
      </div>

      <div className="grid gap-4 sm:grid-cols-2">
        <FormField label="Bandwidth limit (KiB/s)" htmlFor="rep-bw" info="0 means unlimited transfer speed.">
          <Input
            id="rep-bw"
            type="number"
            min={0}
            value={bandwidthKiB}
            onChange={(e) => setBandwidthKiB(e.target.value)}
          />
        </FormField>

        <FormField
          label="Exclude patterns"
          htmlFor="rep-excludes"
          info="Optional rsync exclude rules, one per line (e.g. *.tmp, cache/)."
        >
          <Textarea
            id="rep-excludes"
            value={excludesText}
            onChange={(e) => setExcludesText(e.target.value)}
            placeholder={'*.tmp\ncache/'}
          />
        </FormField>
      </div>

      <div className="flex justify-end gap-2 border-t border-border pt-4">
        <Button variant="secondary" onClick={onClose}>
          Cancel
        </Button>
        <Button type="submit" loading={pending}>
          {isEdit ? 'Save changes' : 'Create job'}
        </Button>
      </div>
    </form>
  )
}

function JobRunsDrawer({ job, onClose }: { job: ReplicationJob | null; onClose: () => void }) {
  const runs = useQuery(replicationJobRunsQuery(job?.id ?? 0))

  return (
    <Drawer
      open={Boolean(job)}
      onClose={onClose}
      title={job ? `Runs — ${job.name}` : 'Run history'}
      description="Recent sync runs and dry-run previews."
    >
      {runs.isLoading ? (
        <div className="flex justify-center py-10">
          <Spinner />
        </div>
      ) : runs.error ? (
        <ErrorState error={runs.error} onRetry={() => void runs.refetch()} />
      ) : (runs.data?.items ?? []).length === 0 ? (
        <EmptyState title="No runs yet" description="Run a dry-run preview or sync to see history here." />
      ) : (
        <div className="flex flex-col gap-3">
          {(runs.data?.items ?? []).map((r) => (
            <div key={r.id} className="flex flex-col gap-2 rounded-lg border border-border bg-bg p-3.5 text-xs">
              <div className="flex items-center justify-between gap-2">
                <div className="flex items-center gap-1.5">
                  <Badge tone={runTone(r.status)} dot>
                    {r.status}
                  </Badge>
                  {r.dryRun && <Badge tone="neutral">Preview</Badge>}
                </div>
                <span className="text-fg-subtle">{formatRelativeTime(r.finishedAt || r.startedAt || r.queuedAt)}</span>
              </div>
              <div className="flex flex-wrap gap-3 text-fg-muted tabular-nums">
                <span>{r.transferred.toLocaleString()} copied</span>
                <span>{r.deleted.toLocaleString()} deleted</span>
                <span>{formatBytes(r.bytes)}</span>
              </div>
              {r.error && <div className="rounded bg-danger-soft px-2 py-1 text-danger">{r.error}</div>}
              {r.changes && r.changes.length > 0 && (
                <pre className="max-h-40 overflow-auto rounded bg-surface-2 p-2 font-mono text-[0.6875rem] text-fg-muted">
                  {r.changes.slice(0, 50).join('\n')}
                  {r.changes.length > 50 ? `\n… +${r.changes.length - 50} more` : ''}
                </pre>
              )}
            </div>
          ))}
        </div>
      )}
    </Drawer>
  )
}

export function ReplicationPage() {
  const [tab, setTab] = useState<RepTab>('jobs')
  const [editingJob, setEditingJob] = useState<ReplicationJob | null>(null)
  const [jobDialogOpen, setJobDialogOpen] = useState(false)
  const [historyJob, setHistoryJob] = useState<ReplicationJob | null>(null)

  // Peer dialog
  const [peerOpen, setPeerOpen] = useState(false)
  const [peerName, setPeerName] = useState('')
  const [peerUrl, setPeerUrl] = useState('')
  const [peerSshPort, setPeerSshPort] = useState('22')
  const [peerCode, setPeerCode] = useState('')

  // Receiver dialog + pairing code modal
  const [recvOpen, setRecvOpen] = useState(false)
  const [recvName, setRecvName] = useState('')
  const [recvPath, setRecvPath] = useState('')
  const [pairingCode, setPairingCode] = useState<string | null>(null)
  const [copiedCode, setCopiedCode] = useState(false)

  const status = useQuery(replicationStatusQuery)
  const jobs = useQuery(replicationJobsQuery)
  const peers = useQuery(replicationPeersQuery)
  const receivers = useQuery(replicationReceiversQuery)

  const updateJob = useUpdateReplicationJob()
  const deleteJob = useDeleteReplicationJob()
  const runJob = useRunReplicationJob()
  const previewJob = usePreviewReplicationJob()
  const createPeer = useCreatePeer()
  const deletePeer = useDeletePeer()
  const createReceiver = useCreateReceiver()
  const deleteReceiver = useDeleteReceiver()
  const newPairingCode = useNewPairingCode()

  function handlePairPeer(e: FormEvent) {
    e.preventDefault()
    createPeer.mutate(
      {
        name: peerName.trim(),
        url: peerUrl.trim(),
        sshPort: Number.parseInt(peerSshPort, 10) || 22,
        code: peerCode.trim(),
      },
      {
        onSuccess: (p) => {
          notify.success('Peer paired', `${p.name} (${p.host})`)
          setPeerOpen(false)
          setPeerName('')
          setPeerUrl('')
          setPeerCode('')
        },
        onError: (err) => notify.error(err),
      },
    )
  }

  function handleCreateReceiver(e: FormEvent) {
    e.preventDefault()
    createReceiver.mutate(
      { name: recvName.trim(), path: recvPath.trim() },
      {
        onSuccess: (res) => {
          notify.success('Receiver created', res.receiver.name)
          setRecvOpen(false)
          setRecvName('')
          setRecvPath('')
          setPairingCode(res.code)
        },
        onError: (err) => notify.error(err),
      },
    )
  }

  return (
    <div className="flex flex-col gap-5">
      <PageHeader
        title="Replication"
        info="Replicate SMB shares to a second local disk or another paired NetShare Hub server over SSH + rsync with dry-run previews."
        actions={
          <div className="flex items-center gap-2">
            {tab === 'jobs' && (
              <Button
                size="sm"
                onClick={() => {
                  setEditingJob(null)
                  setJobDialogOpen(true)
                }}
              >
                <Plus className="size-4" />
                New sync job
              </Button>
            )}
            {tab === 'peers' && (
              <Button size="sm" onClick={() => setPeerOpen(true)}>
                <Link2 className="size-4" />
                Pair peer
              </Button>
            )}
            {tab === 'receivers' && (
              <Button size="sm" onClick={() => setRecvOpen(true)}>
                <Plus className="size-4" />
                New receiver
              </Button>
            )}
          </div>
        }
      />

      {status.data && status.data.hints && status.data.hints.length > 0 && (
        <Alert tone="warning" title={status.data.hints.join(' · ')} />
      )}

      <div className="flex flex-wrap items-center justify-between gap-3">
        <Tabs
          value={tab}
          onChange={setTab}
          items={[
            { id: 'jobs', label: 'Sync Jobs', icon: <FolderSync className="size-3.5" />, badge: jobs.data?.items.length },
            { id: 'peers', label: 'Remote Peers', icon: <Server className="size-3.5" />, badge: peers.data?.items.length },
            { id: 'receivers', label: 'Receivers', icon: <KeyRound className="size-3.5" />, badge: receivers.data?.items.length },
          ]}
        />
        {status.data && (
          <div className="flex flex-wrap items-center gap-1.5 text-xs">
            <Badge tone={status.data.rsync ? 'success' : 'warning'} dot>
              rsync
            </Badge>
            <Badge tone={status.data.sshClient ? 'success' : 'warning'} dot>
              ssh
            </Badge>
            <Badge tone={status.data.rrsync ? 'success' : 'neutral'} dot>
              rrsync
            </Badge>
          </div>
        )}
      </div>

      {tab === 'jobs' && (
        <Card>
          {jobs.isLoading ? (
            <div className="flex justify-center py-12">
              <Spinner />
            </div>
          ) : jobs.error ? (
            <div className="p-5">
              <ErrorState error={jobs.error} onRetry={() => void jobs.refetch()} />
            </div>
          ) : (jobs.data?.items ?? []).length === 0 ? (
            <div className="p-8">
              <EmptyState title="No replication jobs" description="Create a job to sync a share to a backup disk or remote peer." />
            </div>
          ) : (
            <div className="overflow-x-auto">
              <table className="w-full border-collapse text-sm">
                <thead className="border-b border-border bg-surface-2/50 text-left text-[0.6875rem] font-semibold tracking-wider text-fg-subtle uppercase">
                  <tr>
                    <th className="w-16 px-4 py-2.5">Active</th>
                    <th className="px-4 py-2.5">Job</th>
                    <th className="px-4 py-2.5">Destination</th>
                    <th className="px-4 py-2.5">Schedule</th>
                    <th className="px-4 py-2.5">Last run</th>
                    <th className="px-4 py-2.5 text-right">Actions</th>
                  </tr>
                </thead>
                <tbody className="divide-y divide-border">
                  {(jobs.data?.items ?? []).map((j) => (
                    <tr key={j.id} className="hover:bg-surface-2/40">
                      <td className="px-4 py-3">
                        <Switch
                          checked={j.enabled}
                          onChange={(next) =>
                            updateJob.mutate(
                              { id: j.id, body: { enabled: next } },
                              {
                                onSuccess: () => notify.success(next ? 'Job enabled' : 'Job paused', j.name),
                                onError: (err) => notify.error(err),
                              },
                            )
                          }
                          aria-label={`Toggle ${j.name}`}
                        />
                      </td>
                      <td className="px-4 py-3">
                        <div className="flex items-center gap-1.5">
                          <span className="font-medium text-fg">{j.name}</span>
                          {j.mirror && <Badge tone="warning">Mirror</Badge>}
                        </div>
                        <div className="text-xs text-fg-muted">Share: {j.shareName}</div>
                      </td>
                      <td className="px-4 py-3 font-mono text-xs text-fg-muted">
                        {j.peerName ? `peer:${j.peerName}` : j.localPath}
                      </td>
                      <td className="px-4 py-3 text-xs text-fg-muted">
                        <Badge tone="neutral" className="capitalize">
                          {j.schedule}
                        </Badge>
                        {j.nextRunAt && j.enabled && (
                          <div className="mt-0.5 text-[0.6875rem] text-fg-subtle">
                            Next {formatRelativeTime(j.nextRunAt)}
                          </div>
                        )}
                      </td>
                      <td className="px-4 py-3 text-xs">
                        {j.lastRun ? (
                          <div className="flex flex-col gap-0.5">
                            <Badge tone={runTone(j.lastRun.status)} dot>
                              {j.lastRun.status}
                            </Badge>
                            <span className="text-[0.6875rem] text-fg-subtle">
                              {formatRelativeTime(j.lastRun.finishedAt || j.lastRun.queuedAt)}
                            </span>
                          </div>
                        ) : (
                          <span className="text-fg-subtle">Never</span>
                        )}
                      </td>
                      <td className="px-4 py-3">
                        <div className="flex items-center justify-end gap-1">
                          <Button
                            variant="ghost"
                            size="icon"
                            className="size-8"
                            title="Dry-run preview"
                            onClick={() =>
                              previewJob.mutate(j.id, {
                                onSuccess: () => {
                                  notify.info('Preview queued', `Checking changes for ${j.name}.`)
                                  setHistoryJob(j)
                                },
                                onError: (err) => notify.error(err),
                              })
                            }
                          >
                            <Eye className="size-3.5" />
                          </Button>
                          <Button
                            variant="ghost"
                            size="icon"
                            className="size-8"
                            title="Run sync now"
                            onClick={() =>
                              runJob.mutate(j.id, {
                                onSuccess: () => notify.info('Sync queued', `Replicating ${j.name}.`),
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
                            title="Run history"
                            onClick={() => setHistoryJob(j)}
                          >
                            <History className="size-3.5" />
                          </Button>
                          <Button
                            variant="ghost"
                            size="icon"
                            className="size-8"
                            title="Edit job"
                            onClick={() => {
                              setEditingJob(j)
                              setJobDialogOpen(true)
                            }}
                          >
                            <Pencil className="size-3.5" />
                          </Button>
                          <Button
                            variant="ghost"
                            size="icon"
                            className="size-8 text-fg-muted hover:text-danger"
                            title="Delete job"
                            onClick={() => {
                              if (!window.confirm(`Delete replication job "${j.name}"?`)) return
                              deleteJob.mutate(j.id, {
                                onSuccess: () => notify.success('Job deleted'),
                                onError: (err) => notify.error(err),
                              })
                            }}
                          >
                            <Trash2 className="size-3.5" />
                          </Button>
                        </div>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </Card>
      )}

      {tab === 'peers' && (
        <Card>
          {peers.isLoading ? (
            <div className="flex justify-center py-12">
              <Spinner />
            </div>
          ) : peers.error ? (
            <div className="p-5">
              <ErrorState error={peers.error} onRetry={() => void peers.refetch()} />
            </div>
          ) : (peers.data?.items ?? []).length === 0 ? (
            <div className="p-8">
              <EmptyState title="No remote peers paired" description="Create a receiver on another NetShare Hub and pair it with its code." />
            </div>
          ) : (
            <div className="divide-y divide-border">
              {(peers.data?.items ?? []).map((p) => (
                <div key={p.id} className="flex flex-wrap items-center justify-between gap-3 px-5 py-3.5">
                  <div>
                    <div className="font-medium text-fg">{p.name}</div>
                    <div className="font-mono text-xs text-fg-muted">
                      {p.url} · SSH :{p.sshPort}
                    </div>
                  </div>
                  <div className="flex items-center gap-2">
                    <span className="text-xs text-fg-subtle">{formatDateTime(p.createdAt)}</span>
                    <Button
                      variant="ghost"
                      size="icon"
                      className="size-8 text-fg-muted hover:text-danger"
                      onClick={() => {
                        if (!window.confirm(`Remove peer "${p.name}"?`)) return
                        deletePeer.mutate(p.id, {
                          onSuccess: () => notify.success('Peer removed'),
                          onError: (err) => notify.error(err),
                        })
                      }}
                    >
                      <Trash2 className="size-4" />
                    </Button>
                  </div>
                </div>
              ))}
            </div>
          )}
        </Card>
      )}

      {tab === 'receivers' && (
        <Card>
          {receivers.isLoading ? (
            <div className="flex justify-center py-12">
              <Spinner />
            </div>
          ) : receivers.error ? (
            <div className="p-5">
              <ErrorState error={receivers.error} onRetry={() => void receivers.refetch()} />
            </div>
          ) : (receivers.data?.items ?? []).length === 0 ? (
            <div className="p-8">
              <EmptyState title="No receiving folders" description="Add a local folder so another NetShare Hub can replicate shares onto this server." />
            </div>
          ) : (
            <div className="divide-y divide-border">
              {(receivers.data?.items ?? []).map((r) => (
                <div key={r.id} className="flex flex-wrap items-center justify-between gap-3 px-5 py-3.5">
                  <div>
                    <div className="flex items-center gap-2">
                      <span className="font-medium text-fg">{r.name}</span>
                      <Badge tone={r.paired ? 'success' : 'neutral'} dot>
                        {r.paired ? 'Paired' : 'Waiting'}
                      </Badge>
                    </div>
                    <code className="text-xs text-fg-muted">{r.path}</code>
                  </div>
                  <div className="flex items-center gap-1.5">
                    <Button
                      variant="secondary"
                      size="sm"
                      onClick={() =>
                        newPairingCode.mutate(r.id, {
                          onSuccess: (res) => setPairingCode(res.code),
                          onError: (err) => notify.error(err),
                        })
                      }
                    >
                      <KeyRound className="size-3.5" />
                      Pairing code
                    </Button>
                    <Button
                      variant="ghost"
                      size="icon"
                      className="size-8 text-fg-muted hover:text-danger"
                      onClick={() => {
                        if (!window.confirm(`Delete receiver "${r.name}"? Files on disk are kept.`)) return
                        deleteReceiver.mutate(r.id, {
                          onSuccess: () => notify.success('Receiver deleted'),
                          onError: (err) => notify.error(err),
                        })
                      }}
                    >
                      <Trash2 className="size-4" />
                    </Button>
                  </div>
                </div>
              ))}
            </div>
          )}
        </Card>
      )}

      {/* Create / Edit Sync Job Dialog */}
      <Dialog
        open={jobDialogOpen}
        onClose={() => setJobDialogOpen(false)}
        size="lg"
        title={editingJob ? `Edit job — ${editingJob.name}` : 'New replication job'}
      >
        <JobEditorBody
          key={editingJob ? `edit-${editingJob.id}-${editingJob.updatedAt}` : 'create'}
          job={editingJob}
          peers={peers.data?.items ?? []}
          onClose={() => setJobDialogOpen(false)}
        />
      </Dialog>

      {/* Pair Peer Dialog */}
      <Dialog open={peerOpen} onClose={() => setPeerOpen(false)} size="sm" title="Pair remote NetShare Hub">
        <form onSubmit={handlePairPeer} className="flex flex-col gap-4">
          <FormField label="Peer name" htmlFor="peer-name">
            <Input id="peer-name" value={peerName} onChange={(e) => setPeerName(e.target.value)} placeholder="Offsite NAS" required />
          </FormField>
          <FormField label="Web URL" htmlFor="peer-url" info="Base address of the remote NetShare Hub, e.g. http://192.168.1.20:8080.">
            <Input id="peer-url" value={peerUrl} onChange={(e) => setPeerUrl(e.target.value)} placeholder="http://192.168.1.20:8080" required />
          </FormField>
          <FormField label="SSH port" htmlFor="peer-ssh">
            <Input id="peer-ssh" type="number" min={1} max={65535} value={peerSshPort} onChange={(e) => setPeerSshPort(e.target.value)} />
          </FormField>
          <FormField label="One-time pairing code" htmlFor="peer-code" info="Generated on the receiving NetShare Hub under Replication → Receivers.">
            <Input id="peer-code" value={peerCode} onChange={(e) => setPeerCode(e.target.value)} className="font-mono text-xs" required />
          </FormField>
          <div className="flex justify-end gap-2">
            <Button variant="secondary" onClick={() => setPeerOpen(false)}>
              Cancel
            </Button>
            <Button type="submit" loading={createPeer.isPending}>
              Pair
            </Button>
          </div>
        </form>
      </Dialog>

      {/* New Receiver Dialog */}
      <Dialog open={recvOpen} onClose={() => setRecvOpen(false)} size="sm" title="New receiving folder">
        <form onSubmit={handleCreateReceiver} className="flex flex-col gap-4">
          <FormField label="Receiver name" htmlFor="recv-name">
            <Input id="recv-name" value={recvName} onChange={(e) => setRecvName(e.target.value)} placeholder="office-backup" required />
          </FormField>
          <FormField label="Folder path" htmlFor="recv-path" info="Dedicated folder on this machine (cannot be or contain a share).">
            <Input id="recv-path" value={recvPath} onChange={(e) => setRecvPath(e.target.value)} placeholder="/srv/replicas/office" required />
          </FormField>
          <div className="flex justify-end gap-2">
            <Button variant="secondary" onClick={() => setRecvOpen(false)}>
              Cancel
            </Button>
            <Button type="submit" loading={createReceiver.isPending}>
              Create & generate code
            </Button>
          </div>
        </form>
      </Dialog>

      {/* Pairing Code Display Dialog */}
      <Dialog
        open={Boolean(pairingCode)}
        onClose={() => {
          setPairingCode(null)
          setCopiedCode(false)
        }}
        size="sm"
        title="One-time pairing code"
      >
        {pairingCode && (
          <div className="flex flex-col gap-4">
            <p className="text-xs text-fg-muted">
              Enter this code on the sending NetShare Hub within 15 minutes. It works once.
            </p>
            <div className="flex items-center justify-between gap-2 rounded-lg border border-border bg-bg px-3 py-2.5">
              <code className="truncate text-xs font-semibold text-fg">{pairingCode}</code>
              <Button
                variant="ghost"
                size="icon"
                className="size-7 shrink-0"
                onClick={() => {
                  void navigator.clipboard?.writeText(pairingCode).then(() => {
                    setCopiedCode(true)
                    window.setTimeout(() => setCopiedCode(false), 1600)
                  })
                }}
              >
                {copiedCode ? <Check className="size-3.5 text-success" /> : <Copy className="size-3.5" />}
              </Button>
            </div>
            <div className="flex justify-end">
              <Button onClick={() => setPairingCode(null)}>Done</Button>
            </div>
          </div>
        )}
      </Dialog>

      <JobRunsDrawer job={historyJob} onClose={() => setHistoryJob(null)} />
    </div>
  )
}
