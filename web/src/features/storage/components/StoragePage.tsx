import { useQuery } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import {
  Clock,
  Copy,
  ExternalLink,
  File,
  FolderOpen,
  HardDrive,
  Layers,
  PieChart,
  RefreshCw,
} from 'lucide-react'
import { useState } from 'react'
import { notify } from '@/features/notifications'
import { EmptyState, ErrorState, PageHeader } from '@/shared/components'
import { formatBytes, formatRelativeTime } from '@/shared/lib/format'
import { useSession } from '@/shared/session/session'
import {
  Badge,
  Button,
  Card,
  CardContent,
  CardHeader,
  CardTitle,
  InfoTip,
  Spinner,
  StatCard,
  Tabs,
} from '@/shared/ui'
import { storageReportQuery, useRunStorageScan, type StorageShareStat } from '../api'

type TabId = 'overview' | 'largest' | 'oldest' | 'duplicates'

const BAR_COLORS = [
  'bg-primary',
  'bg-emerald-500',
  'bg-amber-500',
  'bg-violet-500',
  'bg-cyan-500',
  'bg-rose-500',
]

function parentDir(filePath: string): string {
  const idx = filePath.lastIndexOf('/')
  return idx > 0 ? filePath.slice(0, idx) : ''
}

function ShareBreakdownCard({ share, totalAll }: { share: StorageShareStat; totalAll: number }) {
  const pct = totalAll > 0 ? Math.round((share.totalBytes / totalAll) * 100) : 0
  return (
    <Card>
      <CardHeader className="flex-row items-center justify-between gap-2 pb-3">
        <div className="min-w-0">
          <CardTitle className="flex items-center gap-2">
            <FolderOpen className="size-4 text-primary" />
            <span>{share.shareName}</span>
            <Badge tone="neutral">{pct}%</Badge>
          </CardTitle>
          <div className="mt-0.5 truncate font-mono text-[0.7rem] text-fg-subtle">{share.path}</div>
        </div>
        <div className="text-right">
          <div className="font-mono text-sm font-semibold text-fg">{formatBytes(share.totalBytes)}</div>
          <div className="text-[0.7rem] text-fg-subtle">
            {share.fileCount.toLocaleString()} files · {share.dirCount.toLocaleString()} folders
          </div>
        </div>
      </CardHeader>
      <CardContent className="flex flex-col gap-4 border-t border-border pt-4">
        {/* Category pills */}
        {share.categories.length > 0 && (
          <div className="flex flex-wrap gap-1.5">
            {share.categories.map((c) => (
              <span
                key={c.category}
                className="inline-flex items-center gap-1.5 rounded-md border border-border bg-surface-2/60 px-2 py-0.5 text-[0.7rem]"
              >
                <span className="font-medium text-fg">{c.category}</span>
                <span className="font-mono text-fg-subtle">{formatBytes(c.bytes)}</span>
              </span>
            ))}
          </div>
        )}

        {/* Proportional folder treemap / bars */}
        {share.topFolders.length === 0 ? (
          <div className="text-xs text-fg-muted">Empty share.</div>
        ) : (
          <div className="grid gap-2 sm:grid-cols-2">
            {share.topFolders.map((f, idx) => {
              const fPct = share.totalBytes > 0 ? Math.max(2, Math.round((f.bytes / share.totalBytes) * 100)) : 0
              return (
                <Link
                  key={f.name}
                  to="/files"
                  search={{ share: share.shareName, path: f.path || undefined }}
                  className="group flex flex-col gap-1.5 rounded-lg border border-border bg-surface-2/30 p-2.5 transition-colors hover:border-border-strong hover:bg-surface-2/70"
                >
                  <div className="flex items-center justify-between gap-2 text-xs">
                    <span className="truncate font-medium text-fg group-hover:text-primary">{f.name}</span>
                    <span className="shrink-0 font-mono text-[0.7rem] text-fg-muted">{formatBytes(f.bytes)}</span>
                  </div>
                  <div className="h-1.5 w-full overflow-hidden rounded-full bg-surface-2">
                    <div
                      className={`h-full rounded-full ${BAR_COLORS[idx % BAR_COLORS.length]}`}
                      style={{ width: `${fPct}%` }}
                    />
                  </div>
                  <div className="flex items-center justify-between text-[0.68rem] text-fg-subtle">
                    <span>{f.files.toLocaleString()} files</span>
                    <span>{fPct}%</span>
                  </div>
                </Link>
              )
            })}
          </div>
        )}
      </CardContent>
    </Card>
  )
}

export function StoragePage() {
  const { can } = useSession()
  const canScan = can('settings:write')
  const [tab, setTab] = useState<TabId>('overview')

  const query = useQuery(storageReportQuery)
  const scan = useRunStorageScan()

  function handleScan() {
    scan.mutate(undefined, {
      onSuccess: (rep) =>
        notify.success(
          'Storage scan complete',
          `Scanned ${rep.totalFiles.toLocaleString()} files (${formatBytes(rep.totalBytes)}) in ${rep.durationMs} ms.`,
        ),
      onError: (err) => notify.error(err),
    })
  }

  const report = query.data

  return (
    <div className="flex flex-col gap-5">
      <PageHeader
        title="Storage Analyzer"
        info="Inspect space usage per share and folder, spot the largest or oldest files, and identify exact SHA-256 duplicates. Scheduled scans can be configured under Settings → Scheduled Tasks."
        actions={
          canScan ? (
            <Button onClick={handleScan} loading={scan.isPending || Boolean(report?.scanning)}>
              <RefreshCw className="size-4" />
              {report?.scannedAt ? 'Rescan shares' : 'Run scan'}
            </Button>
          ) : undefined
        }
      />

      {query.isLoading ? (
        <div className="flex justify-center py-14">
          <Spinner />
        </div>
      ) : query.error ? (
        <ErrorState error={query.error} onRetry={() => void query.refetch()} />
      ) : !report || !report.scannedAt ? (
        <Card>
          <CardContent className="py-10">
            <EmptyState
              title="No storage report yet"
              description="Run a scan to calculate per-share usage, folder breakdowns, largest/oldest files, and duplicate savings."
              action={
                canScan ? (
                  <Button onClick={handleScan} loading={scan.isPending}>
                    <RefreshCw className="size-4" />
                    Run storage scan
                  </Button>
                ) : undefined
              }
            />
          </CardContent>
        </Card>
      ) : (
        <>
          {/* KPI Strip */}
          <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
            <StatCard
              label="Total Analyzed"
              value={formatBytes(report.totalBytes)}
              detail={`${report.shares.length} share${report.shares.length === 1 ? '' : 's'}`}
              icon={<HardDrive className="size-4" />}
            />
            <StatCard
              label="Files & Folders"
              value={report.totalFiles.toLocaleString()}
              detail={`${report.totalDirs.toLocaleString()} folders`}
              icon={<Layers className="size-4" />}
            />
            <StatCard
              label="Reclaimable Duplicates"
              value={formatBytes(report.reclaimableBytes)}
              detail={`${report.duplicates.length} duplicate group${report.duplicates.length === 1 ? '' : 's'}`}
              tone={report.reclaimableBytes > 0 ? 'warning' : 'success'}
              icon={<Copy className="size-4" />}
            />
            <StatCard
              label="Last Scanned"
              value={formatRelativeTime(report.scannedAt)}
              detail={`Completed in ${report.durationMs} ms`}
              icon={<Clock className="size-4" />}
            />
          </div>

          {/* Stacked share proportion bar */}
          {report.totalBytes > 0 && report.shares.length > 0 && (
            <Card>
              <CardHeader className="pb-2">
                <CardTitle className="flex items-center gap-1.5 text-sm">
                  Share Distribution
                  <InfoTip content="Proportional share of total analyzed storage across all SMB shares." />
                </CardTitle>
              </CardHeader>
              <CardContent className="flex flex-col gap-2.5">
                <div className="flex h-3 w-full overflow-hidden rounded-full bg-surface-2">
                  {report.shares.map((s, idx) => {
                    const w = (s.totalBytes / report.totalBytes) * 100
                    if (w < 0.5) return null
                    return (
                      <div
                        key={s.shareId}
                        className={`${BAR_COLORS[idx % BAR_COLORS.length]} h-full transition-all`}
                        style={{ width: `${w}%` }}
                        title={`${s.shareName}: ${formatBytes(s.totalBytes)}`}
                      />
                    )
                  })}
                </div>
                <div className="flex flex-wrap gap-x-4 gap-y-1 text-xs">
                  {report.shares.map((s, idx) => (
                    <span key={s.shareId} className="inline-flex items-center gap-1.5 text-fg-muted">
                      <span className={`size-2.5 rounded-full ${BAR_COLORS[idx % BAR_COLORS.length]}`} />
                      <span className="font-medium text-fg">{s.shareName}</span>
                      <span className="font-mono text-[0.72rem]">{formatBytes(s.totalBytes)}</span>
                    </span>
                  ))}
                </div>
              </CardContent>
            </Card>
          )}

          <Tabs
            value={tab}
            onChange={setTab}
            items={[
              { id: 'overview', label: 'Shares & Folders', icon: <PieChart className="size-3.5" /> },
              { id: 'largest', label: `Largest (${report.largestFiles.length})`, icon: <File className="size-3.5" /> },
              { id: 'oldest', label: `Oldest (${report.oldestFiles.length})`, icon: <Clock className="size-3.5" /> },
              { id: 'duplicates', label: `Duplicates (${report.duplicates.length})`, icon: <Copy className="size-3.5" /> },
            ]}
          />

          {tab === 'overview' && (
            <div className="grid gap-4">
              {report.shares.map((sh) => (
                <ShareBreakdownCard key={sh.shareId} share={sh} totalAll={report.totalBytes} />
              ))}
            </div>
          )}

          {(tab === 'largest' || tab === 'oldest') && (
            <Card>
              <CardContent className="p-0">
                {(tab === 'largest' ? report.largestFiles : report.oldestFiles).length === 0 ? (
                  <div className="p-6 text-center text-xs text-fg-muted">No files found.</div>
                ) : (
                  <div className="divide-y divide-border">
                    {(tab === 'largest' ? report.largestFiles : report.oldestFiles).map((f) => (
                      <div
                        key={`${f.shareName}:${f.path}`}
                        className="flex flex-wrap items-center justify-between gap-3 px-5 py-3"
                      >
                        <div className="min-w-0">
                          <div className="truncate text-sm font-medium text-fg">{f.name}</div>
                          <div className="truncate text-xs text-fg-subtle">
                            <span className="font-medium text-fg-muted">{f.shareName}</span> / {f.path}
                          </div>
                        </div>
                        <div className="flex shrink-0 items-center gap-3">
                          <span className="text-xs text-fg-subtle">{formatRelativeTime(f.modTime)}</span>
                          <Badge tone="neutral" className="font-mono">
                            {formatBytes(f.size)}
                          </Badge>
                          <Link
                            to="/files"
                            search={{ share: f.shareName, path: parentDir(f.path) || undefined }}
                            className="inline-flex items-center gap-1 rounded-md border border-border px-2 py-1 text-xs text-fg-muted hover:bg-surface-2 hover:text-fg"
                          >
                            Open
                            <ExternalLink className="size-3" />
                          </Link>
                        </div>
                      </div>
                    ))}
                  </div>
                )}
              </CardContent>
            </Card>
          )}

          {tab === 'duplicates' && (
            <div className="flex flex-col gap-3">
              {report.duplicates.length === 0 ? (
                <Card>
                  <CardContent className="py-10">
                    <EmptyState
                      title="No duplicate files detected"
                      description="All files ≥ 1 KiB across your scanned shares have unique SHA-256 content."
                    />
                  </CardContent>
                </Card>
              ) : (
                report.duplicates.map((grp) => (
                  <Card key={grp.sha256}>
                    <CardHeader className="flex-row items-center justify-between gap-2 pb-2.5">
                      <div className="flex items-center gap-2">
                        <Badge tone="warning">Save {formatBytes(grp.reclaimableBytes)}</Badge>
                        <span className="text-xs text-fg-muted">
                          {grp.files.length} copies × {formatBytes(grp.size)}
                        </span>
                      </div>
                      <code className="text-[0.7rem] text-fg-subtle">{grp.sha256.slice(0, 12)}…</code>
                    </CardHeader>
                    <CardContent className="divide-y divide-border border-t border-border p-0">
                      {grp.files.map((f) => (
                        <div
                          key={`${f.shareName}:${f.path}`}
                          className="flex flex-wrap items-center justify-between gap-2 px-5 py-2.5 text-xs"
                        >
                          <div className="min-w-0 truncate">
                            <span className="font-semibold text-fg">{f.shareName}</span>
                            <span className="text-fg-muted"> / {f.path}</span>
                          </div>
                          <div className="flex shrink-0 items-center gap-2">
                            <span className="text-fg-subtle">{formatRelativeTime(f.modTime)}</span>
                            <Link
                              to="/files"
                              search={{ share: f.shareName, path: parentDir(f.path) || undefined }}
                              className="inline-flex items-center gap-1 rounded border border-border px-2 py-0.5 text-[0.7rem] text-fg-muted hover:bg-surface-2 hover:text-fg"
                            >
                              Locate
                              <ExternalLink className="size-3" />
                            </Link>
                          </div>
                        </div>
                      ))}
                    </CardContent>
                  </Card>
                ))
              )}
            </div>
          )}
        </>
      )}
    </div>
  )
}
