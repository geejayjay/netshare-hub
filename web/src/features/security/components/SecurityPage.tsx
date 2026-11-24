import { useQuery } from '@tanstack/react-query'
import { Ban as BanIcon, CheckCircle2, RotateCcw, Shield, ShieldAlert, ShieldCheck, Unlock } from 'lucide-react'
import { useState, type FormEvent } from 'react'
import { notify } from '@/features/notifications'
import { errorMessage } from '@/shared/api'
import { EmptyState, ErrorState, FormField, PageHeader } from '@/shared/components'
import { cn } from '@/shared/lib/cn'
import { formatDateTime, formatRelativeTime } from '@/shared/lib/format'
import { useModules } from '@/shared/modules/modules'
import { useSession } from '@/shared/session/session'
import {
  Alert,
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
  Switch,
  Tabs,
  Textarea,
} from '@/shared/ui'
import {
  banSettingsQuery,
  bansQuery,
  guardIncidentsQuery,
  guardSettingsQuery,
  useLiftBan,
  useRestoreIncident,
  useUpdateBanSettings,
  useUpdateGuardSettings,
  type BanSettings,
  type GuardSettings,
} from '../api'

type SecurityTab = 'ransomware' | 'bruteforce'

function GuardPolicyForm({ initial, canEdit }: { initial: GuardSettings; canEdit: boolean }) {
  const saveGuard = useUpdateGuardSettings()
  const [mode, setMode] = useState<GuardSettings['mode']>(initial.mode)
  const [renames, setRenames] = useState(String(initial.renames))
  const [noteFolders, setNoteFolders] = useState(String(initial.noteFolders))

  function handleSubmit(e: FormEvent) {
    e.preventDefault()
    saveGuard.mutate(
      {
        mode,
        renames: Number.parseInt(renames, 10) || 20,
        noteFolders: Number.parseInt(noteFolders, 10) || 3,
        updatedAt: initial.updatedAt,
      },
      {
        onSuccess: () => notify.success('Ransomware Guard saved', `Mode set to "${mode}".`),
        onError: (err) => notify.error(err),
      },
    )
  }

  return (
    <form onSubmit={handleSubmit} className="flex flex-col gap-5">
      {saveGuard.error !== null && <Alert tone="danger" title={errorMessage(saveGuard.error)} />}

      <div className="grid gap-3 md:grid-cols-3">
        {(
          [
            {
              id: 'off',
              title: 'Off',
              info: 'Disables automated SMB file-pattern analysis.',
            },
            {
              id: 'learning',
              title: 'Learning',
              info: 'Alert-only monitoring. Raises critical alerts and logs incidents without blocking users. Recommended first.',
            },
            {
              id: 'protect',
              title: 'Protect',
              info: 'Auto-containment. Immediately switches the suspicious user to read-only across all shares and disconnects their session.',
            },
          ] as const
        ).map((opt) => (
          <button
            key={opt.id}
            type="button"
            disabled={!canEdit}
            onClick={() => setMode(opt.id)}
            className={cn(
              'flex items-center justify-between gap-2 rounded-xl border px-4 py-3 text-left transition-all',
              mode === opt.id
                ? 'border-primary bg-primary-soft/50 ring-1 ring-primary/30'
                : 'border-border bg-bg hover:border-border-strong',
              !canEdit && 'pointer-events-none opacity-60',
            )}
          >
            <div className="flex items-center gap-1.5">
              <span className="text-sm font-medium text-fg">{opt.title}</span>
              <InfoTip content={opt.info} label={`About ${opt.title} mode`} />
            </div>
            {mode === opt.id && <CheckCircle2 className="size-4 text-primary" />}
          </button>
        ))}
      </div>

      <div className="grid gap-4 sm:grid-cols-2">
        <FormField
          label="Suspicious renames / min"
          htmlFor="guard-renames"
          info="Triggers when one user on one host renames this many files to unusual or known-bad extensions within 60 seconds."
        >
          <Input
            id="guard-renames"
            type="number"
            min={3}
            max={100000}
            disabled={!canEdit}
            value={renames}
            onChange={(e) => setRenames(e.target.value)}
          />
        </FormField>

        <FormField
          label="Ransom-note folders"
          htmlFor="guard-notes"
          info="Triggers when known ransom note filenames (e.g. HOW_TO_DECRYPT) appear in this many distinct folders within 60 seconds."
        >
          <Input
            id="guard-notes"
            type="number"
            min={2}
            max={1000}
            disabled={!canEdit}
            value={noteFolders}
            onChange={(e) => setNoteFolders(e.target.value)}
          />
        </FormField>
      </div>

      {canEdit && (
        <div className="flex justify-end">
          <Button type="submit" loading={saveGuard.isPending}>
            Save changes
          </Button>
        </div>
      )}
    </form>
  )
}

function BanPolicyForm({ initial, canEdit }: { initial: BanSettings; canEdit: boolean }) {
  const saveBans = useUpdateBanSettings()
  const [banEnabled, setBanEnabled] = useState(initial.enabled)
  const [failures, setFailures] = useState(String(initial.failures))
  const [windowMinutes, setWindowMinutes] = useState(String(initial.windowMinutes))
  const [banMinutes, setBanMinutes] = useState(String(initial.banMinutes))
  const [maxBanHours, setMaxBanHours] = useState(String(initial.maxBanHours))
  const [followDevice, setFollowDevice] = useState(initial.followDevice)
  const [allowListText, setAllowListText] = useState((initial.allowList ?? []).join('\n'))

  function handleSubmit(e: FormEvent) {
    e.preventDefault()
    const allowList = allowListText
      .split(/[\n,]/)
      .map((s) => s.trim())
      .filter(Boolean)
    saveBans.mutate(
      {
        enabled: banEnabled,
        failures: Number.parseInt(failures, 10) || 5,
        windowMinutes: Number.parseInt(windowMinutes, 10) || 10,
        banMinutes: Number.parseInt(banMinutes, 10) || 15,
        maxBanHours: Number.parseInt(maxBanHours, 10) || 24,
        followDevice,
        allowList,
        updatedAt: initial.updatedAt,
      },
      {
        onSuccess: () => notify.success('Brute-force protection saved'),
        onError: (err) => notify.error(err),
      },
    )
  }

  return (
    <form onSubmit={handleSubmit} className="flex flex-col gap-5">
      {saveBans.error !== null && <Alert tone="danger" title={errorMessage(saveBans.error)} />}

      <div className="grid gap-3 sm:grid-cols-2">
        <div className="flex items-center justify-between gap-3 rounded-xl border border-border bg-bg px-4 py-3">
          <div className="flex items-center gap-1.5">
            <span className="text-sm font-medium text-fg">Automatic bans</span>
            <InfoTip content="Counts failed SMB logins and bans abusive IPs with exponential backoff. Also enables Samba auth logging." />
          </div>
          <Switch checked={banEnabled} disabled={!canEdit} onChange={setBanEnabled} aria-label="Enable brute-force bans" />
        </div>

        <div className="flex items-center justify-between gap-3 rounded-xl border border-border bg-bg px-4 py-3">
          <div className="flex items-center gap-1.5">
            <span className="text-sm font-medium text-fg">Follow device MAC</span>
            <InfoTip content="When a banned LAN device acquires a new DHCP address, recognize its hardware MAC address and ban the new IP too." />
          </div>
          <Switch checked={followDevice} disabled={!canEdit} onChange={setFollowDevice} aria-label="Follow device MAC" />
        </div>
      </div>

      <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
        <FormField label="Failed logins" htmlFor="ban-failures" info="Number of failed attempts before triggering a ban.">
          <Input
            id="ban-failures"
            type="number"
            min={3}
            max={1000}
            disabled={!canEdit}
            value={failures}
            onChange={(e) => setFailures(e.target.value)}
          />
        </FormField>
        <FormField label="Window (min)" htmlFor="ban-window" info="Rolling time window in minutes for counting failures.">
          <Input
            id="ban-window"
            type="number"
            min={1}
            max={1440}
            disabled={!canEdit}
            value={windowMinutes}
            onChange={(e) => setWindowMinutes(e.target.value)}
          />
        </FormField>
        <FormField label="First ban (min)" htmlFor="ban-minutes" info="Duration of the first offence. Repeat offences within a week last 4x longer.">
          <Input
            id="ban-minutes"
            type="number"
            min={1}
            max={10080}
            disabled={!canEdit}
            value={banMinutes}
            onChange={(e) => setBanMinutes(e.target.value)}
          />
        </FormField>
        <FormField label="Max ban (hrs)" htmlFor="ban-max" info="Upper limit for escalated repeat bans.">
          <Input
            id="ban-max"
            type="number"
            min={1}
            max={8760}
            disabled={!canEdit}
            value={maxBanHours}
            onChange={(e) => setMaxBanHours(e.target.value)}
          />
        </FormField>
      </div>

      <FormField
        label="Allow-list"
        htmlFor="ban-allowlist"
        info="IPs or CIDR ranges that will never be banned (e.g. your workstation or VPN subnet). One entry per line."
      >
        <Textarea
          id="ban-allowlist"
          disabled={!canEdit}
          value={allowListText}
          onChange={(e) => setAllowListText(e.target.value)}
          placeholder={'192.168.1.10\n10.8.0.0/24'}
        />
      </FormField>

      {canEdit && (
        <div className="flex justify-end">
          <Button type="submit" loading={saveBans.isPending}>
            Save changes
          </Button>
        </div>
      )}
    </form>
  )
}

export function SecurityPage() {
  const { can } = useSession()
  const modules = useModules()
  const canEdit = can('settings:write')
  const [tab, setTab] = useState<SecurityTab>(modules.ransomwareGuard ? 'ransomware' : 'bruteforce')
  const [banFilter, setBanFilter] = useState<'active' | 'all'>('active')

  const guardSettings = useQuery({ ...guardSettingsQuery, enabled: modules.ransomwareGuard })
  const incidents = useQuery({ ...guardIncidentsQuery, enabled: modules.ransomwareGuard })
  const banSettings = useQuery({ ...banSettingsQuery, enabled: modules.bruteForce })
  const bans = useQuery({ ...bansQuery(banFilter === 'active' ? true : undefined), enabled: modules.bruteForce })

  const restoreIncident = useRestoreIncident()
  const liftBan = useLiftBan()

  const incidentItems = incidents.data?.items ?? []
  const blockedIncidents = incidentItems.filter((i) => i.blocked && !i.restoredAt).length
  const activeBanItems = (bans.data?.items ?? []).filter((b) => b.active)

  return (
    <div className="flex flex-col gap-5">
      <PageHeader
        title="Security & Guard"
        info="Automated ransomware containment and SMB brute-force login protection."
      />

      <div className="grid gap-4 sm:grid-cols-3">
        <StatCard
          label="Ransomware Guard"
          value={<span className="capitalize">{guardSettings.data?.mode ?? '—'}</span>}
          icon={<Shield />}
          tone={guardSettings.data?.mode === 'protect' ? 'success' : guardSettings.data?.mode === 'learning' ? 'warning' : 'neutral'}
        />
        <StatCard
          label="Active containments"
          value={blockedIncidents}
          icon={<ShieldAlert />}
          tone={blockedIncidents > 0 ? 'danger' : 'success'}
        />
        <StatCard
          label="Active bans"
          value={activeBanItems.length}
          icon={<BanIcon />}
          tone={activeBanItems.length > 0 ? 'warning' : 'info'}
        />
      </div>

      <Tabs
        value={tab}
        onChange={setTab}
        items={[
          ...(modules.ransomwareGuard
            ? [
                {
                  id: 'ransomware' as const,
                  label: 'Ransomware Guard',
                  icon: <ShieldAlert className="size-3.5" />,
                  badge: blockedIncidents || undefined,
                },
              ]
            : []),
          ...(modules.bruteForce
            ? [
                {
                  id: 'bruteforce' as const,
                  label: 'Brute-Force Protection',
                  icon: <BanIcon className="size-3.5" />,
                  badge: activeBanItems.length || undefined,
                },
              ]
            : []),
        ]}
      />

      {tab === 'ransomware' && (
        <div className="flex flex-col gap-5">
          <Card>
            <CardHeader>
              <CardTitle className="flex items-center gap-1.5">
                Detection Policy
                <InfoTip content="Watches live SMB activity for mass suspicious renames and ransom notes across folders." />
              </CardTitle>
            </CardHeader>
            <CardContent>
              {guardSettings.isLoading ? (
                <div className="flex justify-center py-8">
                  <Spinner />
                </div>
              ) : guardSettings.error ? (
                <ErrorState error={guardSettings.error} onRetry={() => void guardSettings.refetch()} />
              ) : guardSettings.data ? (
                <GuardPolicyForm
                  key={guardSettings.data.updatedAt || 'guard'}
                  initial={guardSettings.data}
                  canEdit={canEdit}
                />
              ) : null}
            </CardContent>
          </Card>

          <Card>
            <CardHeader>
              <CardTitle className="flex items-center gap-1.5">
                Recorded Incidents
                <InfoTip content="Contained users can be restored to their original share permissions once the client machine is clean." />
              </CardTitle>
            </CardHeader>
            <CardContent className="p-0 pt-4">
              {incidents.isLoading ? (
                <div className="flex justify-center py-10">
                  <Spinner />
                </div>
              ) : incidents.error ? (
                <div className="p-5">
                  <ErrorState error={incidents.error} onRetry={() => void incidents.refetch()} />
                </div>
              ) : incidentItems.length === 0 ? (
                <div className="p-8">
                  <EmptyState title="No incidents recorded" />
                </div>
              ) : (
                <div className="divide-y divide-border border-t border-border">
                  {incidentItems.map((inc) => (
                    <div key={inc.id} className="flex flex-wrap items-start justify-between gap-4 px-5 py-4">
                      <div className="flex min-w-0 flex-col gap-1.5">
                        <div className="flex flex-wrap items-center gap-2">
                          <span className="font-semibold text-fg">{inc.username}</span>
                          <Badge tone={inc.blocked && !inc.restoredAt ? 'danger' : inc.restoredAt ? 'success' : 'warning'} dot>
                            {inc.restoredAt ? 'Restored' : inc.blocked ? 'Blocked' : 'Alerted'}
                          </Badge>
                          <Badge tone="neutral">{inc.rule}</Badge>
                          <span className="text-xs text-fg-muted tabular-nums">
                            {inc.count} hits · <code>{inc.clientIp}</code>
                          </span>
                        </div>
                        <div className="flex flex-wrap items-center gap-1.5 text-xs text-fg-muted">
                          {inc.shares.map((sh) => (
                            <Badge key={sh} tone="neutral">
                              {sh}
                            </Badge>
                          ))}
                          <span className="text-fg-subtle">{formatDateTime(inc.at)}</span>
                        </div>
                        {inc.samples.length > 0 && (
                          <div className="mt-0.5 flex flex-wrap gap-1.5">
                            {inc.samples.slice(0, 5).map((sample) => (
                              <code key={sample} className="rounded bg-surface-2 px-1.5 py-0.5 text-[0.7rem] text-fg-muted">
                                {sample}
                              </code>
                            ))}
                          </div>
                        )}
                      </div>

                      {inc.blocked && !inc.restoredAt && canEdit && (
                        <Button
                          variant="secondary"
                          size="sm"
                          loading={restoreIncident.isPending && restoreIncident.variables === inc.id}
                          onClick={() => {
                            if (!window.confirm(`Restore write access for "${inc.username}"? Make sure their device is clean.`)) {
                              return
                            }
                            restoreIncident.mutate(inc.id, {
                              onSuccess: () => notify.success('Access restored', `${inc.username}'s permissions were restored.`),
                              onError: (err) => notify.error(err),
                            })
                          }}
                        >
                          <RotateCcw className="size-3.5" />
                          Restore access
                        </Button>
                      )}
                    </div>
                  ))}
                </div>
              )}
            </CardContent>
          </Card>
        </div>
      )}

      {tab === 'bruteforce' && (
        <div className="flex flex-col gap-5">
          <Card>
            <CardHeader>
              <CardTitle className="flex items-center gap-1.5">
                Brute-Force Policy
                <InfoTip content="Monitors failed SMB logins and automatically bans abusive clients with escalating durations." />
              </CardTitle>
            </CardHeader>
            <CardContent>
              {banSettings.isLoading ? (
                <div className="flex justify-center py-8">
                  <Spinner />
                </div>
              ) : banSettings.error ? (
                <ErrorState error={banSettings.error} onRetry={() => void banSettings.refetch()} />
              ) : banSettings.data ? (
                <BanPolicyForm
                  key={banSettings.data.updatedAt || 'bans'}
                  initial={banSettings.data}
                  canEdit={canEdit}
                />
              ) : null}
            </CardContent>
          </Card>

          <Card>
            <div className="flex flex-wrap items-center justify-between gap-3 border-b border-border px-5 py-3">
              <div className="flex items-center gap-1.5">
                <h2 className="text-sm font-semibold">Banned Clients</h2>
                <InfoTip content="IPs and LAN devices currently or previously blocked from SMB authentication." />
              </div>
              <Tabs
                value={banFilter}
                onChange={setBanFilter}
                items={[
                  { id: 'active', label: 'Active' },
                  { id: 'all', label: 'History' },
                ]}
              />
            </div>

            {bans.isLoading ? (
              <div className="flex justify-center py-10">
                <Spinner />
              </div>
            ) : bans.error ? (
              <div className="p-5">
                <ErrorState error={bans.error} onRetry={() => void bans.refetch()} />
              </div>
            ) : (bans.data?.items ?? []).length === 0 ? (
              <div className="p-8">
                <EmptyState title={banFilter === 'active' ? 'No active bans' : 'No bans recorded'} />
              </div>
            ) : (
              <div className="overflow-x-auto">
                <table className="w-full border-collapse text-sm">
                  <thead className="border-b border-border bg-surface-2/50 text-left text-[0.6875rem] font-semibold tracking-wider text-fg-subtle uppercase">
                    <tr>
                      <th className="px-4 py-2.5">Address / MAC</th>
                      <th className="px-4 py-2.5">Status</th>
                      <th className="px-4 py-2.5">Offences</th>
                      <th className="px-4 py-2.5">Accounts</th>
                      <th className="px-4 py-2.5">Until</th>
                      <th className="px-4 py-2.5 text-right">Actions</th>
                    </tr>
                  </thead>
                  <tbody className="divide-y divide-border">
                    {(bans.data?.items ?? []).map((ban) => (
                      <tr key={ban.id} className="hover:bg-surface-2/40">
                        <td className="px-4 py-3 font-mono text-xs">
                          <div className="font-medium text-fg">{ban.address}</div>
                          {ban.mac && <div className="text-[0.6875rem] text-fg-subtle">{ban.mac}</div>}
                        </td>
                        <td className="px-4 py-3">
                          <Badge tone={ban.active ? 'danger' : 'neutral'} dot>
                            {ban.active ? 'Active' : ban.liftedAt ? 'Lifted' : 'Expired'}
                          </Badge>
                        </td>
                        <td className="px-4 py-3 text-xs text-fg-muted tabular-nums">
                          {ban.failures} fails · #{ban.offence}
                        </td>
                        <td className="px-4 py-3">
                          <div className="flex flex-wrap gap-1">
                            {ban.users.map((u) => (
                              <Badge key={u} tone="neutral">
                                {u}
                              </Badge>
                            ))}
                          </div>
                        </td>
                        <td className="px-4 py-3 text-xs text-fg-muted">{formatRelativeTime(ban.until)}</td>
                        <td className="px-4 py-3 text-right">
                          {ban.active && canEdit ? (
                            <Button
                              variant="secondary"
                              size="sm"
                              className="h-7 px-2.5 text-xs"
                              loading={liftBan.isPending && liftBan.variables === ban.id}
                              onClick={() =>
                                liftBan.mutate(ban.id, {
                                  onSuccess: () => notify.success('Ban lifted', `${ban.address} can connect again.`),
                                  onError: (err) => notify.error(err),
                                })
                              }
                            >
                              <Unlock className="size-3.5" />
                              Lift
                            </Button>
                          ) : (
                            <span className="inline-flex items-center gap-1 text-xs text-fg-subtle">
                              <ShieldCheck className="size-3.5" /> Done
                            </span>
                          )}
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )}
          </Card>
        </div>
      )}
    </div>
  )
}
