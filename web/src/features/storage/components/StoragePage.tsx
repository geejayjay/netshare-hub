import { useQuery } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import {
  AlertTriangle,
  ChevronDown,
  ChevronRight,
  Clock,
  Copy,
  ExternalLink,
  File,
  Filter,
  FolderOpen,
  HardDrive,
  Layers,
  LayoutGrid,
  List,
  PieChart,
  RefreshCw,
} from 'lucide-react'
import { useMemo, useState } from 'react'
import { notify } from '@/features/notifications'
import { sharesQuery } from '@/features/shares'
import { EmptyState, ErrorState, FormField, PageHeader, SearchInput } from '@/shared/components'
import { cn } from '@/shared/lib/cn'
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
  Input,
  Spinner,
  StatCard,
  Tabs,
} from '@/shared/ui'
import {
  storageReportQuery,
  useRunStorageScan,
  useUpdateStorageConfig,
  type StorageShareStat,
} from '../api'

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

function ShareBreakdownCard({
  share,
  totalAll,
  canScan,
  isScanning,
  isScanningThis,
  onScanShare,
}: {
  share: StorageShareStat
  totalAll: number
  canScan: boolean
  isScanning: boolean
  isScanningThis: boolean
  onScanShare: (shareName: string) => void
}) {
  const pct = totalAll > 0 ? Math.round((share.totalBytes / totalAll) * 100) : 0
  return (
    <Card>
      <CardHeader className="flex-row items-center justify-between gap-2 pb-3">
        <div className="min-w-0">
          <CardTitle className="flex items-center gap-2">
            <FolderOpen className="size-4 text-primary" />
            <span>{share.shareName}</span>
            {share.skipped ? (
              <Badge tone="warning">Skipped</Badge>
            ) : (
              <Badge tone="neutral">{pct}%</Badge>
            )}
          </CardTitle>
          <div className="mt-0.5 truncate font-mono text-[0.7rem] text-fg-subtle">
            {share.path}
            {share.scannedAt && !share.skipped ? ` · scanned ${formatRelativeTime(share.scannedAt)}` : ''}
          </div>
        </div>
        <div className="flex shrink-0 items-center gap-3">
          <div className="text-right">
            <div className="font-mono text-sm font-semibold text-fg">
              {share.skipped ? '—' : formatBytes(share.totalBytes)}
            </div>
            <div className="text-[0.7rem] text-fg-subtle">
              {share.skipped
                ? 'Not scanned'
                : `${share.fileCount.toLocaleString()} files · ${share.dirCount.toLocaleString()} folders`}
            </div>
          </div>
          {canScan && (
            <Button
              variant="secondary"
              size="sm"
              className="h-8 px-2.5 text-xs"
              disabled={isScanning}
              loading={isScanningThis}
              onClick={() => onScanShare(share.shareName)}
            >
              <RefreshCw className="size-3.5" />
              Scan
            </Button>
          )}
        </div>
      </CardHeader>
      <CardContent className="flex flex-col gap-4 border-t border-border pt-4">
        {share.skipped ? (
          <div className="flex items-center gap-2 text-xs text-fg-muted">
            <AlertTriangle className="size-3.5 shrink-0 text-warning" />
            <span>{share.skipReason || 'Skipped during scan.'}</span>
          </div>
        ) : (
          <>
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
          </>
        )}
      </CardContent>
    </Card>
  )
}

export function StoragePage() {
  const { can } = useSession()
  const canScan = can('settings:write')
  const [tab, setTab] = useState<TabId>('overview')
  const [showOptions, setShowOptions] = useState(false)
  const [showExcluded, setShowExcluded] = useState(false)
  const [shareView, setShareView] = useState<'cards' | 'table'>('cards')
  const [shareSearch, setShareSearch] = useState('')
  const [exclusionSearch, setExclusionSearch] = useState('')
  const [draftExcludeShares, setDraftExcludeShares] = useState<string[] | null>(null)
  const [draftExcludePaths, setDraftExcludePaths] = useState<string | null>(null)

  const query = useQuery(storageReportQuery)
  const shares = useQuery(sharesQuery({ pageSize: 100 }))
  const scan = useRunStorageScan()
  const saveConfig = useUpdateStorageConfig()

  const report = query.data
  const allShares = useMemo(() => shares.data?.items ?? [], [shares.data])
  const excludeShares = useMemo(
    () => draftExcludeShares ?? report?.excludeShares ?? [],
    [draftExcludeShares, report?.excludeShares],
  )
  const excludePathsText = draftExcludePaths ?? (report?.excludePaths ?? []).join(', ')

  const savedExcludedSet = useMemo(
    () => new Set((report?.excludeShares ?? []).map((s) => s.toLowerCase())),
    [report?.excludeShares],
  )
  const { activeShares, excludedReportShares } = useMemo(() => {
    const active: StorageShareStat[] = []
    const excluded: StorageShareStat[] = []
    for (const sh of report?.shares ?? []) {
      const isExcluded =
        (sh.skipped && sh.skipReason === 'Excluded from scan') ||
        (sh.skipped && savedExcludedSet.has(sh.shareName.toLowerCase()))
      if (isExcluded) excluded.push(sh)
      else active.push(sh)
    }
    return { activeShares: active, excludedReportShares: excluded }
  }, [report?.shares, savedExcludedSet])

  const filteredActiveShares = useMemo(() => {
    const q = shareSearch.trim().toLowerCase()
    if (!q) return activeShares
    return activeShares.filter(
      (s) => s.shareName.toLowerCase().includes(q) || s.path.toLowerCase().includes(q),
    )
  }, [activeShares, shareSearch])

  const filteredConfigShares = useMemo(() => {
    const q = exclusionSearch.trim().toLowerCase()
    if (!q) return allShares
    return allShares.filter((s) => s.name.toLowerCase().includes(q))
  }, [allShares, exclusionSearch])

  const distributedShares = useMemo(
    () => activeShares.filter((s) => !s.skipped && s.totalBytes > 0),
    [activeShares],
  )

  function buildConfig() {
    const excludePaths = excludePathsText
      .split(/[\n,]/)
      .map((s) => s.trim())
      .filter(Boolean)
    return { excludeShares, excludePaths }
  }

  function toggleShareExclusion(name: string) {
    setDraftExcludeShares((prev) => {
      const current = prev ?? report?.excludeShares ?? []
      return current.some((s) => s.toLowerCase() === name.toLowerCase())
        ? current.filter((s) => s.toLowerCase() !== name.toLowerCase())
        : [...current, name]
    })
  }

  function handleSaveConfig() {
    saveConfig.mutate(buildConfig(), {
      onSuccess: () => {
        setDraftExcludeShares(null)
        setDraftExcludePaths(null)
        notify.success('Exclusions saved', 'Applied to manual and scheduled storage scans.')
      },
      onError: (err) => notify.error(err),
    })
  }

  function handleScan(targetShares?: string[]) {
    const cfg = buildConfig()
    scan.mutate(
      targetShares && targetShares.length > 0 ? { ...cfg, shares: targetShares } : cfg,
      {
        onSuccess: () => {
          setDraftExcludeShares(null)
          setDraftExcludePaths(null)
          notify.success(
            'Storage scan started',
            targetShares && targetShares.length === 1
              ? `Scanning ${targetShares[0]} in the background…`
              : 'Scanning selected shares in the background…',
          )
        },
        onError: (err) => notify.error(err),
      },
    )
  }

  const isScanning = scan.isPending || Boolean(report?.scanning)
  const optionsVisible = showOptions || (!query.isLoading && (!report || !report.scannedAt))
  const excludedCount = excludeShares.length + excludePathsText.split(/[\n,]/).filter((s) => s.trim()).length

  return (
    <div className="flex flex-col gap-5">
      <PageHeader
        title="Storage Analyzer"
        info="Inspect space usage per share and folder, spot the largest or oldest files, and identify exact SHA-256 duplicates. Scheduled scans can be configured under Settings → Scheduled Tasks."
        actions={
          canScan ? (
            <div className="flex items-center gap-2">
              <Button variant="secondary" onClick={() => setShowOptions((v) => !v)}>
                <Filter className="size-4" />
                Exclusions{excludedCount > 0 ? ` (${excludedCount})` : ''}
              </Button>
              <Button onClick={() => handleScan()} loading={isScanning}>
                <RefreshCw className="size-4" />
                {report?.scannedAt ? 'Rescan all' : 'Scan all'}
              </Button>
            </div>
          ) : undefined
        }
      />

      {canScan && optionsVisible && (
        <Card>
          <CardHeader className="pb-2">
            <CardTitle className="flex items-center gap-2 text-sm">
              <Filter className="size-4 text-primary" />
              Scan Scope &amp; Exclusions
            </CardTitle>
          </CardHeader>
          <CardContent className="flex flex-col gap-4">
            <div className="flex flex-col gap-2">
              <div className="flex flex-wrap items-center justify-between gap-2">
                <div className="text-xs font-medium text-fg">
                  Shares to scan ({allShares.length - excludeShares.length} of {allShares.length} included)
                </div>
                {allShares.length > 0 && (
                  <div className="flex items-center gap-2">
                    {allShares.length > 6 && (
                      <div className="w-44">
                        <SearchInput value={exclusionSearch} onChange={setExclusionSearch} placeholder="Filter shares…" />
                      </div>
                    )}
                    <Button
                      variant="ghost"
                      size="sm"
                      className="h-7 px-2 text-xs"
                      onClick={() => setDraftExcludeShares([])}
                    >
                      Include all
                    </Button>
                    <Button
                      variant="ghost"
                      size="sm"
                      className="h-7 px-2 text-xs"
                      onClick={() => setDraftExcludeShares(allShares.map((s) => s.name))}
                    >
                      Exclude all
                    </Button>
                  </div>
                )}
              </div>
              {allShares.length === 0 ? (
                <div className="text-xs text-fg-muted">No shares configured yet.</div>
              ) : (
                <div className="flex max-h-44 flex-wrap gap-2 overflow-y-auto rounded-lg border border-border bg-bg p-2.5">
                  {filteredConfigShares.map((sh) => {
                    const isExcluded = excludeShares.some((s) => s.toLowerCase() === sh.name.toLowerCase())
                    return (
                      <button
                        key={sh.id}
                        type="button"
                        onClick={() => toggleShareExclusion(sh.name)}
                        className={`inline-flex items-center gap-1.5 rounded-lg border px-2.5 py-1 text-xs font-medium transition-colors ${
                          isExcluded
                            ? 'border-border bg-surface-2/40 text-fg-subtle line-through'
                            : 'border-primary/40 bg-primary-soft text-primary'
                        }`}
                      >
                        <FolderOpen className="size-3.5" />
                        <span>{sh.name}</span>
                        {isExcluded && <span className="text-[0.65rem] no-underline">(excluded)</span>}
                      </button>
                    )
                  })}
                </div>
              )}
            </div>

            <FormField
              label="Exclude folders or paths (comma-separated)"
              htmlFor="storage-exclude-paths"
              hint="Matches folder names anywhere (e.g. qbittorrent, node_modules, .git) or share-relative paths (e.g. Games/SteamLibrary)."
            >
              <Input
                id="storage-exclude-paths"
                value={excludePathsText}
                onChange={(e) => setDraftExcludePaths(e.target.value)}
                placeholder="e.g. qbittorrent, node_modules, cache"
              />
            </FormField>

            <div className="flex items-center justify-end gap-2">
              <Button variant="secondary" size="sm" onClick={handleSaveConfig} loading={saveConfig.isPending}>
                Save exclusions
              </Button>
              <Button size="sm" onClick={() => handleScan()} loading={isScanning}>
                <RefreshCw className="size-3.5" />
                Scan all
              </Button>
            </div>
          </CardContent>
        </Card>
      )}

      {report?.scanning && (
        <Card className="border-primary/30 bg-primary-soft/30">
          <CardContent className="flex items-center justify-between gap-3 py-3">
            <div className="flex items-center gap-2.5 text-sm text-fg">
              <Spinner className="size-4" />
              <span>
                Scanning <strong>{report.scanningShare || 'shares'}</strong>…{' '}
                <span className="font-mono text-xs text-fg-muted">
                  {(report.scannedFiles ?? 0).toLocaleString()} files · {formatBytes(report.scannedBytes ?? 0)}
                </span>
              </span>
            </div>
            <Badge tone="info">In progress</Badge>
          </CardContent>
        </Card>
      )}

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
                  <Button onClick={() => handleScan()} loading={isScanning}>
                    <RefreshCw className="size-4" />
                    Scan all shares
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
              detail={
                excludedReportShares.length > 0
                  ? `${activeShares.length} analyzed · ${excludedReportShares.length} excluded`
                  : `${activeShares.length} share${activeShares.length === 1 ? '' : 's'}`
              }
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
          {report.totalBytes > 0 && distributedShares.length > 0 && (
            <Card>
              <CardHeader className="pb-2">
                <CardTitle className="flex items-center gap-1.5 text-sm">
                  Share Distribution
                  <InfoTip content="Proportional share of total analyzed storage across included SMB shares." />
                </CardTitle>
              </CardHeader>
              <CardContent className="flex flex-col gap-2.5">
                <div className="flex h-3 w-full overflow-hidden rounded-full bg-surface-2">
                  {distributedShares.map((s, idx) => {
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
                  {distributedShares.map((s, idx) => (
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
              { id: 'overview', label: `Shares & Folders (${activeShares.length})`, icon: <PieChart className="size-3.5" /> },
              { id: 'largest', label: `Largest (${report.largestFiles.length})`, icon: <File className="size-3.5" /> },
              { id: 'oldest', label: `Oldest (${report.oldestFiles.length})`, icon: <Clock className="size-3.5" /> },
              { id: 'duplicates', label: `Duplicates (${report.duplicates.length})`, icon: <Copy className="size-3.5" /> },
            ]}
          />

          {tab === 'overview' && (
            <div className="flex flex-col gap-4">
              {activeShares.length > 0 && (
                <div className="flex flex-wrap items-center justify-between gap-2">
                  <div className="w-60">
                    <SearchInput value={shareSearch} onChange={setShareSearch} placeholder="Filter shares…" />
                  </div>
                  <div className="inline-flex rounded-lg border border-border bg-surface-2 p-0.5">
                    <button
                      type="button"
                      onClick={() => setShareView('cards')}
                      className={cn(
                        'rounded-md p-1.5 transition-colors',
                        shareView === 'cards' ? 'bg-surface text-fg shadow-xs' : 'text-fg-muted hover:text-fg',
                      )}
                      aria-label="Card view"
                    >
                      <LayoutGrid className="size-3.5" />
                    </button>
                    <button
                      type="button"
                      onClick={() => setShareView('table')}
                      className={cn(
                        'rounded-md p-1.5 transition-colors',
                        shareView === 'table' ? 'bg-surface text-fg shadow-xs' : 'text-fg-muted hover:text-fg',
                      )}
                      aria-label="Compact table view"
                    >
                      <List className="size-3.5" />
                    </button>
                  </div>
                </div>
              )}

              {filteredActiveShares.length === 0 ? (
                <Card className="p-6 text-center text-xs text-fg-muted">
                  {activeShares.length === 0
                    ? 'All shares are currently excluded from scans.'
                    : 'No shares match your filter.'}
                </Card>
              ) : shareView === 'cards' ? (
                <div className="grid gap-4">
                  {filteredActiveShares.map((sh) => (
                    <ShareBreakdownCard
                      key={sh.shareId}
                      share={sh}
                      totalAll={report.totalBytes}
                      canScan={canScan}
                      isScanning={isScanning}
                      isScanningThis={Boolean(report.scanning && report.scanningShare === sh.shareName)}
                      onScanShare={(name) => handleScan([name])}
                    />
                  ))}
                </div>
              ) : (
                <Card className="overflow-hidden">
                  <div className="overflow-x-auto">
                    <table className="w-full text-left text-xs">
                      <thead className="border-b border-border bg-surface-2/50 text-fg-muted">
                        <tr>
                          <th className="px-4 py-2.5 font-medium">Share</th>
                          <th className="px-4 py-2.5 font-medium text-right">Size</th>
                          <th className="px-4 py-2.5 font-medium text-right">Files</th>
                          <th className="hidden px-4 py-2.5 font-medium md:table-cell">Top folder</th>
                          <th className="hidden px-4 py-2.5 font-medium sm:table-cell">Scanned</th>
                          {canScan && <th className="px-4 py-2.5 text-right font-medium" />}
                        </tr>
                      </thead>
                      <tbody className="divide-y divide-border">
                        {filteredActiveShares.map((sh) => (
                          <tr key={sh.shareId} className="hover:bg-surface-2/30">
                            <td className="px-4 py-2.5">
                              <div className="flex items-center gap-2">
                                <FolderOpen className="size-3.5 shrink-0 text-primary" />
                                <span className="font-medium text-fg">{sh.shareName}</span>
                                {sh.skipped && <Badge tone="warning">Skipped</Badge>}
                              </div>
                              <div className="mt-0.5 max-w-xs truncate font-mono text-[0.68rem] text-fg-subtle">
                                {sh.skipped ? sh.skipReason : sh.path}
                              </div>
                            </td>
                            <td className="px-4 py-2.5 text-right font-mono font-medium text-fg">
                              {sh.skipped ? '—' : formatBytes(sh.totalBytes)}
                            </td>
                            <td className="px-4 py-2.5 text-right font-mono text-fg-muted">
                              {sh.skipped ? '—' : sh.fileCount.toLocaleString()}
                            </td>
                            <td className="hidden px-4 py-2.5 text-fg-muted md:table-cell">
                              {sh.topFolders[0]
                                ? `${sh.topFolders[0].name} (${formatBytes(sh.topFolders[0].bytes)})`
                                : '—'}
                            </td>
                            <td className="hidden px-4 py-2.5 text-fg-subtle sm:table-cell">
                              {sh.scannedAt ? formatRelativeTime(sh.scannedAt) : '—'}
                            </td>
                            {canScan && (
                              <td className="px-4 py-2.5 text-right">
                                <Button
                                  variant="secondary"
                                  size="sm"
                                  className="h-7 px-2 text-xs"
                                  disabled={isScanning}
                                  loading={Boolean(report.scanning && report.scanningShare === sh.shareName)}
                                  onClick={() => handleScan([sh.shareName])}
                                >
                                  <RefreshCw className="size-3" />
                                  Scan
                                </Button>
                              </td>
                            )}
                          </tr>
                        ))}
                      </tbody>
                    </table>
                  </div>
                </Card>
              )}

              {excludedReportShares.length > 0 && (
                <Card className="overflow-hidden border-dashed">
                  <button
                    type="button"
                    onClick={() => setShowExcluded((v) => !v)}
                    className="flex w-full items-center justify-between px-4 py-2.5 text-left text-xs font-medium text-fg-muted transition-colors hover:bg-surface-2/40 hover:text-fg"
                  >
                    <span className="inline-flex items-center gap-1.5">
                      {showExcluded ? <ChevronDown className="size-3.5" /> : <ChevronRight className="size-3.5" />}
                      Excluded from scans ({excludedReportShares.length})
                    </span>
                    <span className="text-[0.7rem] text-fg-subtle">
                      {showExcluded ? 'Hide' : 'Show excluded shares'}
                    </span>
                  </button>
                  {showExcluded && (
                    <div className="divide-y divide-border border-t border-border">
                      {excludedReportShares.map((sh) => (
                        <div
                          key={sh.shareId}
                          className="flex flex-wrap items-center justify-between gap-2 px-4 py-2 text-xs"
                        >
                          <div className="min-w-0">
                            <span className="font-medium text-fg-muted">{sh.shareName}</span>
                            <span className="ml-2 font-mono text-[0.68rem] text-fg-subtle">{sh.path}</span>
                          </div>
                          {canScan && (
                            <div className="flex items-center gap-1.5">
                              <Button
                                variant="ghost"
                                size="sm"
                                className="h-7 px-2 text-xs"
                                onClick={() => {
                                  const next = excludeShares.filter(
                                    (s) => s.toLowerCase() !== sh.shareName.toLowerCase(),
                                  )
                                  setDraftExcludeShares(next)
                                  saveConfig.mutate({
                                    excludeShares: next,
                                    excludePaths: buildConfig().excludePaths,
                                  })
                                }}
                              >
                                Include
                              </Button>
                              <Button
                                variant="secondary"
                                size="sm"
                                className="h-7 px-2 text-xs"
                                disabled={isScanning}
                                onClick={() => handleScan([sh.shareName])}
                              >
                                <RefreshCw className="size-3" />
                                Scan once
                              </Button>
                            </div>
                          )}
                        </div>
                      ))}
                    </div>
                  )}
                </Card>
              )}
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
