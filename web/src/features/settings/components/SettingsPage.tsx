import { useQuery } from '@tanstack/react-query'
import { Clock, Cpu, Radio, RefreshCw, Server, Sparkles } from 'lucide-react'
import { useState, type FormEvent } from 'react'
import { notify } from '@/features/notifications'
import { errorMessage, fieldErrors } from '@/shared/api'
import { EmptyState, ErrorState, FormField, PageHeader } from '@/shared/components'
import { cn } from '@/shared/lib/cn'
import { formatRelativeTime } from '@/shared/lib/format'
import { useSession } from '@/shared/session/session'
import {
  Alert,
  Badge,
  Button,
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
  Input,
  Select,
  Spinner,
  Switch,
  Tabs,
} from '@/shared/ui'
import {
  discoveryQuery,
  sambaSettingsQuery,
  systemTasksQuery,
  useUpdateDiscovery,
  useUpdateSambaSettings,
  type DiscoveryService,
  type SambaSettings,
} from '../api'

type SettingsTab = 'samba' | 'discovery' | 'tasks'

function SambaSettingsForm({ initial, canEdit }: { initial: SambaSettings; canEdit: boolean }) {
  const saveSamba = useUpdateSambaSettings()
  const [serverName, setServerName] = useState(initial.serverName ?? '')
  const [workgroup, setWorkgroup] = useState(initial.workgroup ?? '')
  const [minProtocol, setMinProtocol] = useState<SambaSettings['minProtocol']>(initial.minProtocol)
  const [maxProtocol, setMaxProtocol] = useState<SambaSettings['maxProtocol']>(initial.maxProtocol)
  const [encryption, setEncryption] = useState<SambaSettings['encryption']>(initial.encryption)
  const [multichannel, setMultichannel] = useState<SambaSettings['multichannel']>(initial.multichannel)
  const [macSupport, setMacSupport] = useState(Boolean(initial.macSupport))
  const [activityLog, setActivityLog] = useState(Boolean(initial.activityLog))
  const [submitError, setSubmitError] = useState<unknown>(null)

  const errors = fieldErrors(submitError)

  function handleSaveSamba(e: FormEvent) {
    e.preventDefault()
    setSubmitError(null)
    saveSamba.mutate(
      {
        serverName: serverName.trim(),
        workgroup: workgroup.trim(),
        minProtocol,
        maxProtocol,
        encryption,
        multichannel,
        macSupport,
        activityLog,
        updatedAt: initial.updatedAt,
      },
      {
        onSuccess: () => notify.success('Samba settings saved', 'Configuration applied and Samba reloaded.'),
        onError: (err) => setSubmitError(err),
      },
    )
  }

  return (
    <form onSubmit={handleSaveSamba} className="flex flex-col gap-6">
      {submitError !== null && <Alert tone="danger" title={errorMessage(submitError)} />}

      <div className="grid gap-4 sm:grid-cols-2">
        <FormField
          label="Server name (NetBIOS)"
          htmlFor="smb-server-name"
          hint="1–15 letters, digits or dashes. Leave blank to use system hostname."
          error={errors.serverName}
        >
          <Input
            id="smb-server-name"
            value={serverName}
            disabled={!canEdit}
            maxLength={15}
            placeholder="NETSHARE"
            onChange={(e) => setServerName(e.target.value)}
          />
        </FormField>

        <FormField
          label="Workgroup"
          htmlFor="smb-workgroup"
          hint="Windows workgroup name (typically WORKGROUP)."
          error={errors.workgroup}
        >
          <Input
            id="smb-workgroup"
            value={workgroup}
            disabled={!canEdit}
            maxLength={15}
            placeholder="WORKGROUP"
            onChange={(e) => setWorkgroup(e.target.value)}
          />
        </FormField>
      </div>

      <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
        <FormField label="Minimum protocol" htmlFor="smb-min-proto" error={errors.minProtocol}>
          <Select
            id="smb-min-proto"
            value={minProtocol}
            disabled={!canEdit}
            onChange={(e) => setMinProtocol(e.target.value as SambaSettings['minProtocol'])}
          >
            <option value="default">Default (SMB2+)</option>
            <option value="SMB2_02">SMB 2.02</option>
            <option value="SMB2_10">SMB 2.10</option>
            <option value="SMB3_00">SMB 3.00</option>
            <option value="SMB3_02">SMB 3.02</option>
            <option value="SMB3_11">SMB 3.11</option>
          </Select>
        </FormField>

        <FormField label="Maximum protocol" htmlFor="smb-max-proto" error={errors.maxProtocol}>
          <Select
            id="smb-max-proto"
            value={maxProtocol}
            disabled={!canEdit}
            onChange={(e) => setMaxProtocol(e.target.value as SambaSettings['maxProtocol'])}
          >
            <option value="default">Default (Latest)</option>
            <option value="SMB2_02">SMB 2.02</option>
            <option value="SMB2_10">SMB 2.10</option>
            <option value="SMB3_00">SMB 3.00</option>
            <option value="SMB3_02">SMB 3.02</option>
            <option value="SMB3_11">SMB 3.11</option>
          </Select>
        </FormField>

        <FormField label="Transport encryption" htmlFor="smb-encryption" error={errors.encryption}>
          <Select
            id="smb-encryption"
            value={encryption}
            disabled={!canEdit}
            onChange={(e) => setEncryption(e.target.value as SambaSettings['encryption'])}
          >
            <option value="default">Default</option>
            <option value="if-supported">If supported (SMB3)</option>
            <option value="required">Required (SMB3+)</option>
            <option value="off">Off</option>
          </Select>
        </FormField>

        <FormField label="SMB Multichannel" htmlFor="smb-multichannel" error={errors.multichannel}>
          <Select
            id="smb-multichannel"
            value={multichannel}
            disabled={!canEdit}
            onChange={(e) => setMultichannel(e.target.value as SambaSettings['multichannel'])}
          >
            <option value="default">Default</option>
            <option value="on">Enabled</option>
            <option value="off">Disabled</option>
          </Select>
        </FormField>
      </div>

      <div className="grid gap-3 md:grid-cols-2">
        <div className="flex items-start justify-between gap-3 rounded-xl border border-border bg-bg p-4">
          <div className="flex items-start gap-2.5">
            <Sparkles className="mt-0.5 size-4 shrink-0 text-primary" />
            <div>
              <div className="text-sm font-medium text-fg">macOS SMB extensions (fruit)</div>
              <p className="mt-0.5 text-xs leading-relaxed text-fg-muted">
                Faster Finder browsing, Spotlight metadata, and Time Machine compatibility. Requires{' '}
                <code>samba-vfs-modules</code>.
              </p>
            </div>
          </div>
          <Switch checked={macSupport} disabled={!canEdit} onChange={setMacSupport} aria-label="Enable macOS extensions" />
        </div>

        <div className="flex items-start justify-between gap-3 rounded-xl border border-border bg-bg p-4">
          <div className="flex items-start gap-2.5">
            <Cpu className="mt-0.5 size-4 shrink-0 text-primary" />
            <div>
              <div className="text-sm font-medium text-fg">SMB Activity Logging (full_audit)</div>
              <p className="mt-0.5 text-xs leading-relaxed text-fg-muted">
                Records file reads, writes, renames, and deletes for the Activity Feed and Ransomware Guard.
              </p>
            </div>
          </div>
          <Switch checked={activityLog} disabled={!canEdit} onChange={setActivityLog} aria-label="Enable SMB activity log" />
        </div>
      </div>

      {canEdit && (
        <div className="flex items-center justify-between border-t border-border pt-4">
          <span className="text-xs text-fg-subtle">
            {initial.updatedAt && `Last updated ${formatRelativeTime(initial.updatedAt)}`}
          </span>
          <Button type="submit" loading={saveSamba.isPending}>
            Save Samba settings
          </Button>
        </div>
      )}
    </form>
  )
}

function DiscoveryCard({
  title,
  subtitle,
  service,
  systemd,
  canEdit,
  loading,
  onToggle,
}: {
  title: string
  subtitle: string
  service: DiscoveryService
  systemd: boolean
  canEdit: boolean
  loading: boolean
  onToggle: (next: boolean) => void
}) {
  const disabled = !canEdit || !systemd || !service.installed || loading

  return (
    <Card className="p-5">
      <div className="flex items-start justify-between gap-4">
        <div className="flex items-start gap-3">
          <span className="grid size-10 shrink-0 place-items-center rounded-xl bg-primary-soft text-primary">
            <Radio className="size-5" />
          </span>
          <div className="min-w-0">
            <div className="flex flex-wrap items-center gap-2">
              <h3 className="text-sm font-semibold text-fg">{title}</h3>
              {!service.installed ? (
                <Badge tone="warning">Not installed</Badge>
              ) : service.running ? (
                <Badge tone="success" dot>
                  Running
                </Badge>
              ) : (
                <Badge tone="neutral" dot>
                  Stopped
                </Badge>
              )}
              {service.unit && (
                <code className="rounded bg-surface-2 px-1.5 py-0.5 text-[0.7rem] text-fg-muted">{service.unit}</code>
              )}
            </div>
            <p className="mt-1 text-xs leading-relaxed text-fg-muted">{subtitle}</p>
            {service.hint && <p className="mt-2 rounded-md bg-surface-2 px-2.5 py-1.5 text-xs text-fg-muted">{service.hint}</p>}
          </div>
        </div>

        <Switch checked={service.on} disabled={disabled} onChange={onToggle} aria-label={`Toggle ${title}`} />
      </div>
    </Card>
  )
}

export function SettingsPage() {
  const { can } = useSession()
  const canEdit = can('settings:write')
  const [tab, setTab] = useState<SettingsTab>('samba')

  const samba = useQuery(sambaSettingsQuery)
  const discovery = useQuery(discoveryQuery)
  const tasks = useQuery({ ...systemTasksQuery, enabled: can('system:read') })
  const saveDiscovery = useUpdateDiscovery()

  return (
    <div className="flex flex-col gap-6">
      <PageHeader
        title="Settings"
        description="Configure server-wide Samba behavior, LAN discovery daemons, and scheduled maintenance."
      />

      <Tabs
        value={tab}
        onChange={setTab}
        items={[
          { id: 'samba', label: 'Samba Server', icon: <Server className="size-3.5" /> },
          { id: 'discovery', label: 'Network Discovery', icon: <Radio className="size-3.5" /> },
          ...(can('system:read') ? [{ id: 'tasks' as const, label: 'Scheduled Tasks', icon: <Clock className="size-3.5" /> }] : []),
        ]}
      />

      {tab === 'samba' && (
        <Card>
          <CardHeader>
            <CardTitle>Samba Global Configuration</CardTitle>
            <CardDescription>
              Applied across all SMB shares. SMB1 is permanently disabled for security.
            </CardDescription>
          </CardHeader>
          <CardContent>
            {samba.isLoading ? (
              <div className="flex justify-center py-10">
                <Spinner />
              </div>
            ) : samba.error ? (
              <ErrorState error={samba.error} onRetry={() => void samba.refetch()} />
            ) : samba.data ? (
              <SambaSettingsForm key={samba.data.updatedAt || 'samba'} initial={samba.data} canEdit={canEdit} />
            ) : null}
          </CardContent>
        </Card>
      )}

      {tab === 'discovery' && (
        <div className="flex flex-col gap-4">
          {discovery.isLoading ? (
            <div className="flex justify-center py-12">
              <Spinner />
            </div>
          ) : discovery.error ? (
            <ErrorState error={discovery.error} onRetry={() => void discovery.refetch()} />
          ) : discovery.data ? (
            <>
              {!discovery.data.systemd && (
                <Alert tone="warning" title="Systemd is not available on this host">
                  Discovery daemons reflect their current status, but cannot be toggled automatically without systemd.
                </Alert>
              )}

              <DiscoveryCard
                title="mDNS / Bonjour (Avahi)"
                subtitle="Broadcasts this server to macOS Finder, Linux file managers, and advertises Apple Time Machine shares automatically."
                service={discovery.data.mdns}
                systemd={discovery.data.systemd}
                canEdit={canEdit}
                loading={saveDiscovery.isPending}
                onToggle={(next) =>
                  saveDiscovery.mutate(
                    { mdns: next },
                    {
                      onSuccess: () => notify.success('mDNS updated', `Avahi discovery turned ${next ? 'on' : 'off'}.`),
                      onError: (err) => notify.error(err),
                    },
                  )
                }
              />

              <DiscoveryCard
                title="WS-Discovery (Windows Network)"
                subtitle="Makes this server appear automatically inside Windows File Explorer under the Network neighborhood."
                service={discovery.data.wsd}
                systemd={discovery.data.systemd}
                canEdit={canEdit}
                loading={saveDiscovery.isPending}
                onToggle={(next) =>
                  saveDiscovery.mutate(
                    { wsd: next },
                    {
                      onSuccess: () =>
                        notify.success('WS-Discovery updated', `Windows discovery turned ${next ? 'on' : 'off'}.`),
                      onError: (err) => notify.error(err),
                    },
                  )
                }
              />
            </>
          ) : null}
        </div>
      )}

      {tab === 'tasks' && (
        <Card>
          <div className="flex items-center justify-between border-b border-border px-5 py-3.5">
            <div>
              <h2 className="text-[0.9375rem] font-semibold">Scheduled Maintenance Tasks</h2>
              <p className="text-xs text-fg-muted">Internal periodic jobs run by NetShare Hub.</p>
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
                    <th className="px-4 py-2.5">Task</th>
                    <th className="px-4 py-2.5">Schedule</th>
                    <th className="px-4 py-2.5">Status</th>
                    <th className="px-4 py-2.5">Runs</th>
                    <th className="px-4 py-2.5">Last run</th>
                    <th className="px-4 py-2.5">Next run</th>
                  </tr>
                </thead>
                <tbody className="divide-y divide-border">
                  {(tasks.data?.items ?? []).map((t) => (
                    <tr key={t.name} className="hover:bg-surface-2/40">
                      <td className="px-4 py-3">
                        <div className="font-medium text-fg">{t.name}</div>
                        {t.lastError && <div className="text-xs text-danger">{t.lastError}</div>}
                      </td>
                      <td className="px-4 py-3 font-mono text-xs text-fg-muted">{t.schedule}</td>
                      <td className="px-4 py-3">
                        <Badge tone={t.running ? 'info' : t.lastError ? 'danger' : 'success'} dot>
                          {t.running ? 'Running' : t.lastError ? 'Error' : 'Idle'}
                        </Badge>
                      </td>
                      <td className="px-4 py-3 text-xs text-fg-muted tabular-nums">{t.runs}</td>
                      <td className="px-4 py-3 text-xs text-fg-muted">{formatRelativeTime(t.lastEnd || t.lastStart || '')}</td>
                      <td className="px-4 py-3 text-xs text-fg-muted">{formatRelativeTime(t.nextRun || '')}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </Card>
      )}
    </div>
  )
}
