import { useQuery } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import { AlertTriangle, Check, CheckCircle2, Copy, RefreshCw, Terminal, XCircle } from 'lucide-react'
import { useMemo, useState } from 'react'
import { bansQuery, guardSettingsQuery } from '@/features/security'
import { discoveryQuery, sambaSettingsQuery } from '@/features/settings'
import { nfsStatusQuery, sharesQuery } from '@/features/shares'
import { api, unwrap } from '@/shared/api'
import { PageHeader } from '@/shared/components'
import { cn } from '@/shared/lib/cn'
import { useModules } from '@/shared/modules/modules'
import { useSession } from '@/shared/session/session'
import { Badge, Button, Card, CardContent, CardHeader, CardTitle, Select, Spinner } from '@/shared/ui'

type CheckState = 'pass' | 'warn' | 'fail'

interface DiagnosticCheck {
  id: string
  title: string
  state: CheckState
  detail: string
  fixTo?: '/settings' | '/shares' | '/security' | '/sessions'
  fixLabel?: string
}

const healthQuery = {
  queryKey: ['health'],
  queryFn: () => unwrap(api.GET('/health')),
}

const mountsQuery = {
  queryKey: ['system', 'mounts'],
  queryFn: () => unwrap(api.GET('/system/mounts')),
}

function warningLabel(code: string): string {
  switch (code) {
    case 'read_only_mount':
      return 'Read-only bind mount — writes will fail until remounted rw'
    case 'on_root_disk':
      return 'Folder resides on container root disk — use a dedicated bind mount (mp0)'
    case 'uid_mapping_mismatch':
      return 'Unmapped UID/GID (65534/nobody) — fix unprivileged LXC idmap or host ownership'
    case 'path_missing':
      return 'Folder does not exist on disk'
    default:
      return code
  }
}

type ClientOS = 'windows' | 'macos' | 'linux'

export function TroubleshooterPage() {
  const { can } = useSession()
  const modules = useModules()
  const [selectedShare, setSelectedShare] = useState('')
  const [os, setOs] = useState<ClientOS>('windows')
  const [copiedCmd, setCopiedCmd] = useState(false)

  const health = useQuery(healthQuery)
  const mounts = useQuery({ ...mountsQuery, enabled: can('system:read') })
  const shares = useQuery({ ...sharesQuery({ pageSize: 200 }), enabled: can('shares:read') })
  const samba = useQuery({ ...sambaSettingsQuery, enabled: can('settings:read') })
  const discovery = useQuery({ ...discoveryQuery, enabled: can('settings:read') && modules.discovery })
  const guard = useQuery({ ...guardSettingsQuery, enabled: can('settings:read') && modules.ransomwareGuard })
  const bans = useQuery({ ...bansQuery(true), enabled: can('settings:read') && modules.bruteForce })
  const nfsStatus = useQuery({ ...nfsStatusQuery, enabled: can('shares:read') && modules.nfs })

  const loading = health.isLoading || shares.isLoading || samba.isLoading

  const checks = useMemo<DiagnosticCheck[]>(() => {
    const out: DiagnosticCheck[] = []

    // 1. Service health
    if (health.data?.status === 'ok') {
      out.push({
        id: 'health',
        title: 'NetShare Hub service',
        state: 'pass',
        detail: `API healthy (v${health.data.version}).`,
      })
    } else if (health.error) {
      out.push({
        id: 'health',
        title: 'NetShare Hub service',
        state: 'fail',
        detail: 'Could not reach /api/v1/health.',
      })
    }

    // 2. SMB Protocol & Encryption
    if (samba.data) {
      const encReq = samba.data.encryption === 'required'
      const lowMin = samba.data.minProtocol === 'SMB2_02' || samba.data.minProtocol === 'SMB2_10'
      if (encReq && lowMin) {
        out.push({
          id: 'smb-proto',
          title: 'SMB protocol & encryption',
          state: 'fail',
          detail: 'Required encryption needs minimum protocol SMB 3.00 or newer.',
          fixTo: '/settings',
          fixLabel: 'Fix in Settings',
        })
      } else {
        out.push({
          id: 'smb-proto',
          title: 'SMB protocol & encryption',
          state: 'pass',
          detail: `Min ${samba.data.minProtocol} · Encryption: ${samba.data.encryption}.`,
        })
      }
    }

    // 3. Shares reachability
    if (shares.data) {
      const unreachable = shares.data.items.filter((s) => s.access.length === 0 && !s.guestAccess)
      if (shares.data.items.length === 0) {
        out.push({
          id: 'shares',
          title: 'SMB shares configured',
          state: 'warn',
          detail: 'No shares exist yet.',
          fixTo: '/shares',
          fixLabel: 'Create share',
        })
      } else if (unreachable.length > 0) {
        out.push({
          id: 'shares',
          title: 'Share access assignments',
          state: 'warn',
          detail: `${unreachable.map((s) => s.name).join(', ')} have no assigned users and guest access off.`,
          fixTo: '/shares',
          fixLabel: 'Edit access',
        })
      } else {
        out.push({
          id: 'shares',
          title: 'Share access assignments',
          state: 'pass',
          detail: `All ${shares.data.items.length} shares have valid user or guest access.`,
        })
      }

      // 4. Time Machine & macOS extensions
      const tmShares = shares.data.items.filter((s) => s.timeMachine)
      if (tmShares.length > 0 && samba.data) {
        const mdnsOk = !modules.discovery || Boolean(discovery.data?.mdns.running)
        if (!samba.data.macSupport || !mdnsOk) {
          out.push({
            id: 'timemachine',
            title: 'macOS Time Machine readiness',
            state: 'warn',
            detail: !samba.data.macSupport
              ? 'macOS SMB extensions (fruit) are off.'
              : 'Avahi mDNS is stopped — Macs will not auto-discover Time Machine targets.',
            fixTo: '/settings',
            fixLabel: 'Check Settings',
          })
        } else {
          out.push({
            id: 'timemachine',
            title: 'macOS Time Machine readiness',
            state: 'pass',
            detail: `${tmShares.length} Time Machine target(s) ready with fruit + mDNS.`,
          })
        }
      }
    }

    // 5. Discovery daemons
    if (modules.discovery && discovery.data) {
      if (!discovery.data.wsd.running) {
        out.push({
          id: 'wsd',
          title: 'Windows Network Discovery (WSD)',
          state: 'warn',
          detail: discovery.data.wsd.hint || 'WS-Discovery is stopped. Windows clients must connect by IP/hostname.',
          fixTo: '/settings',
          fixLabel: 'Discovery settings',
        })
      } else {
        out.push({
          id: 'wsd',
          title: 'Windows Network Discovery (WSD)',
          state: 'pass',
          detail: 'Running — server appears in Windows Network Explorer.',
        })
      }
    }

    // 6. Ransomware Guard alignment
    if (modules.ransomwareGuard && guard.data && samba.data) {
      if (guard.data.mode !== 'off' && !samba.data.activityLog) {
        out.push({
          id: 'guard-audit',
          title: 'Ransomware Guard telemetry',
          state: 'warn',
          detail: `Guard is in "${guard.data.mode}" mode, but SMB activity logging is off.`,
          fixTo: '/settings',
          fixLabel: 'Enable logging',
        })
      } else {
        out.push({
          id: 'guard-audit',
          title: 'Ransomware Guard telemetry',
          state: 'pass',
          detail: guard.data.mode === 'off' ? 'Guard is off.' : `Guard active (${guard.data.mode}) with SMB audit feed.`,
        })
      }
    }

    // 7. Active brute-force bans
    if (modules.bruteForce && bans.data) {
      const active = bans.data.items.filter((b) => b.active)
      if (active.length > 0) {
        out.push({
          id: 'bans',
          title: 'Blocked client IPs',
          state: 'warn',
          detail: `${active.length} client IP(s) currently banned (${active.slice(0, 3).map((b) => b.address).join(', ')}).`,
          fixTo: '/security',
          fixLabel: 'Manage bans',
        })
      } else {
        out.push({
          id: 'bans',
          title: 'Blocked client IPs',
          state: 'pass',
          detail: 'No client IPs are currently banned.',
        })
      }
    }

    // 8. NFS server check
    if (modules.nfs && nfsStatus.data) {
      if (!nfsStatus.data.supported || !nfsStatus.data.installed || !nfsStatus.data.running) {
        out.push({
          id: 'nfs',
          title: 'NFS kernel server',
          state: 'warn',
          detail: nfsStatus.data.hint || 'NFS server is not running.',
        })
      } else {
        out.push({
          id: 'nfs',
          title: 'NFS kernel server',
          state: 'pass',
          detail: `Running (${nfsStatus.data.exports.length} active export(s)).`,
        })
      }
    }

    return out
  }, [health.data, health.error, samba.data, shares.data, discovery.data, guard.data, bans.data, nfsStatus.data, modules])

  const shareList = shares.data?.items ?? []
  const activeShare = shareList.find((s) => s.name === selectedShare) ?? shareList[0]
  const host = typeof window !== 'undefined' ? window.location.hostname : 'server'

  const testCommand = activeShare
    ? os === 'windows'
      ? `net use * \\\\${host}\\${activeShare.name} /user:<username>`
      : os === 'macos'
        ? `open smb://<username>@${host}/${activeShare.name}`
        : `smbclient //${host}/${activeShare.name} -U <username>`
    : ''

  function rerunAll() {
    void health.refetch()
    void mounts.refetch()
    void shares.refetch()
    void samba.refetch()
    if (modules.discovery) void discovery.refetch()
    if (modules.ransomwareGuard) void guard.refetch()
    if (modules.bruteForce) void bans.refetch()
    if (modules.nfs) void nfsStatus.refetch()
  }

  return (
    <div className="flex flex-col gap-5">
      <PageHeader
        title="Connection Troubleshooter"
        info="Runs automated checks across Samba settings, LAN discovery, share permissions, active IP bans, and optional modules to diagnose client mount failures."
        actions={
          <Button variant="secondary" size="sm" onClick={rerunAll}>
            <RefreshCw className={cn('size-3.5', (health.isFetching || samba.isFetching) && 'animate-spin')} />
            Re-run checks
          </Button>
        }
      />

      <Card>
        <CardHeader className="pb-3">
          <CardTitle>Diagnostic Checks</CardTitle>
        </CardHeader>
        <CardContent className="border-t border-border p-0">
          {loading ? (
            <div className="flex justify-center py-12">
              <Spinner />
            </div>
          ) : (
            <div className="divide-y divide-border">
              {checks.map((c) => (
                <div key={c.id} className="flex flex-wrap items-center justify-between gap-3 px-5 py-3.5">
                  <div className="flex min-w-0 items-start gap-3">
                    {c.state === 'pass' ? (
                      <CheckCircle2 className="mt-0.5 size-4.5 shrink-0 text-success" />
                    ) : c.state === 'warn' ? (
                      <AlertTriangle className="mt-0.5 size-4.5 shrink-0 text-warning" />
                    ) : (
                      <XCircle className="mt-0.5 size-4.5 shrink-0 text-danger" />
                    )}
                    <div className="min-w-0">
                      <div className="text-sm font-medium text-fg">{c.title}</div>
                      <div className="text-xs text-fg-muted">{c.detail}</div>
                    </div>
                  </div>
                  <div className="flex shrink-0 items-center gap-2">
                    <Badge tone={c.state === 'pass' ? 'success' : c.state === 'warn' ? 'warning' : 'danger'}>
                      {c.state === 'pass' ? 'OK' : c.state === 'warn' ? 'Warning' : 'Issue'}
                    </Badge>
                    {c.fixTo && c.fixLabel && (
                      <Link
                        to={c.fixTo}
                        className="rounded-md border border-border-strong bg-surface px-2.5 py-1 text-xs font-medium text-fg shadow-xs hover:bg-surface-2"
                      >
                        {c.fixLabel}
                      </Link>
                    )}
                  </div>
                </div>
              ))}
            </div>
          )}
        </CardContent>
      </Card>

      {mounts.data && (
        <Card>
          <CardHeader className="flex-row items-center justify-between gap-2 pb-3">
            <CardTitle>LXC & Mount Health</CardTitle>
            <div className="flex items-center gap-1.5">
              <Badge tone="info" className="uppercase">
                {mounts.data.environment}
              </Badge>
              {mounts.data.unprivileged && <Badge tone="neutral">unprivileged</Badge>}
            </div>
          </CardHeader>
          <CardContent className="border-t border-border p-0">
            {(mounts.data.checks ?? []).length === 0 ? (
              <div className="px-5 py-4 text-xs text-fg-muted">No share paths to inspect yet.</div>
            ) : (
              <div className="divide-y divide-border">
                {(mounts.data.checks ?? []).map((m) => {
                  const warnings = m.warnings ?? []
                  const hasWarn = warnings.length > 0
                  return (
                    <div key={m.path} className="flex flex-wrap items-start justify-between gap-3 px-5 py-3">
                      <div className="flex min-w-0 items-start gap-2.5">
                        {hasWarn ? (
                          <AlertTriangle className="mt-0.5 size-4 shrink-0 text-warning" />
                        ) : (
                          <CheckCircle2 className="mt-0.5 size-4 shrink-0 text-success" />
                        )}
                        <div className="min-w-0">
                          <code className="text-xs font-medium text-fg">{m.path}</code>
                          <div className="text-[0.7rem] text-fg-subtle">
                            Mount: <code>{m.mountPoint || '—'}</code>
                            {m.fsType && ` · ${m.fsType}`}
                            {m.readOnly && ' · ro'}
                          </div>
                          {hasWarn && (
                            <ul className="mt-1 list-disc pl-4 text-xs text-warning">
                              {warnings.map((w) => (
                                <li key={w}>{warningLabel(w)}</li>
                              ))}
                            </ul>
                          )}
                        </div>
                      </div>
                      <Badge tone={hasWarn ? 'warning' : 'success'}>{hasWarn ? 'Check' : 'OK'}</Badge>
                    </div>
                  )
                })}
              </div>
            )}
          </CardContent>
        </Card>
      )}

      {activeShare && (
        <Card>
          <CardHeader className="pb-3">
            <CardTitle className="flex items-center gap-2">
              <Terminal className="size-4 text-primary" />
              Client Connection Command
            </CardTitle>
          </CardHeader>
          <CardContent className="flex flex-col gap-4 border-t border-border pt-4">
            <div className="flex flex-wrap items-center gap-3">
              <Select
                className="w-56"
                value={activeShare.name}
                onChange={(e) => setSelectedShare(e.target.value)}
                aria-label="Select share"
              >
                {shareList.map((s) => (
                  <option key={s.id} value={s.name}>
                    {s.name} ({s.path})
                  </option>
                ))}
              </Select>

              <div className="inline-flex rounded-lg border border-border bg-surface-2 p-0.5">
                {(['windows', 'macos', 'linux'] as const).map((tab) => (
                  <button
                    key={tab}
                    type="button"
                    onClick={() => setOs(tab)}
                    className={cn(
                      'rounded-md px-2.5 py-1 text-xs font-medium transition-colors',
                      os === tab ? 'bg-surface text-fg shadow-xs ring-1 ring-border' : 'text-fg-muted hover:text-fg',
                    )}
                  >
                    {tab === 'windows' ? 'Windows' : tab === 'macos' ? 'macOS' : 'Linux'}
                  </button>
                ))}
              </div>
            </div>

            <div className="flex items-center justify-between gap-2 rounded-lg border border-border bg-bg px-3.5 py-2.5">
              <code className="truncate text-xs text-fg">{testCommand}</code>
              <Button
                variant="ghost"
                size="icon"
                className="size-7 shrink-0"
                onClick={() => {
                  void navigator.clipboard?.writeText(testCommand).then(() => {
                    setCopiedCmd(true)
                    window.setTimeout(() => setCopiedCmd(false), 1500)
                  })
                }}
              >
                {copiedCmd ? <Check className="size-3.5 text-success" /> : <Copy className="size-3.5" />}
              </Button>
            </div>

            <div className="flex flex-wrap items-center gap-2 text-xs text-fg-muted">
              <span>Allowed users:</span>
              {activeShare.access.length === 0 ? (
                <Badge tone="neutral">{activeShare.guestAccess ? 'Guests only' : 'None'}</Badge>
              ) : (
                activeShare.access.map((a) => (
                  <Badge key={a.userId} tone="info">
                    {a.username} ({a.level})
                  </Badge>
                ))
              )}
              {activeShare.networkRules.length > 0 && (
                <Badge tone="warning">{activeShare.networkRules.length} IP rule(s)</Badge>
              )}
            </div>
          </CardContent>
        </Card>
      )}
    </div>
  )
}
