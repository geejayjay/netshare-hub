import { useQuery } from '@tanstack/react-query'
import { Archive, Bell, Clock, Cpu, Radio, Server, Sparkles } from 'lucide-react'
import { useState, type FormEvent } from 'react'
import { notify } from '@/features/notifications'
import { errorMessage, fieldErrors } from '@/shared/api'
import { ErrorState, FormField, PageHeader } from '@/shared/components'
import { formatRelativeTime } from '@/shared/lib/format'
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
  Select,
  Spinner,
  Switch,
  Tabs,
} from '@/shared/ui'
import {
  discoveryQuery,
  sambaSettingsQuery,
  useUpdateDiscovery,
  useUpdateSambaSettings,
  type DiscoveryService,
  type SambaSettings,
} from '../api'
import { BackupPanel } from './BackupPanel'
import { NotificationsPanel } from './NotificationsPanel'
import { ScheduledTasksPanel } from './ScheduledTasksPanel'

type SettingsTab = 'samba' | 'discovery' | 'notifications' | 'tasks' | 'backup'

function SambaSettingsForm({ initial, canEdit, activityEnabled }: { initial: SambaSettings; canEdit: boolean; activityEnabled: boolean }) {
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
    <form onSubmit={handleSaveSamba} className="flex flex-col gap-5">
      {submitError !== null && <Alert tone="danger" title={errorMessage(submitError)} />}

      <div className="grid gap-4 sm:grid-cols-2">
        <FormField
          label="Server name"
          htmlFor="smb-server-name"
          info="NetBIOS name (\\NAME): 1–15 letters, digits or dashes. Leave blank to use the system hostname."
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
          info="Windows workgroup name (1–15 letters, digits or dashes). Leave blank for system default (usually WORKGROUP)."
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
        <FormField
          label="Minimum protocol"
          htmlFor="smb-min-proto"
          info="Oldest SMB dialect clients may negotiate. SMB1 is permanently disabled."
          error={errors.minProtocol}
        >
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

        <FormField
          label="Maximum protocol"
          htmlFor="smb-max-proto"
          info="Newest SMB dialect clients may negotiate."
          error={errors.maxProtocol}
        >
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

        <FormField
          label="Encryption"
          htmlFor="smb-encryption"
          info="Required refuses clients that cannot encrypt and requires minimum protocol SMB 3.00 or newer."
          error={errors.encryption}
        >
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

        <FormField
          label="Multichannel"
          htmlFor="smb-multichannel"
          info="Allows clients to aggregate multiple network links simultaneously for higher throughput."
          error={errors.multichannel}
        >
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
        <div className="flex items-center justify-between gap-3 rounded-xl border border-border bg-bg px-4 py-3">
          <div className="flex items-center gap-2.5">
            <Sparkles className="size-4 shrink-0 text-primary" />
            <span className="text-sm font-medium text-fg">macOS extensions</span>
            <InfoTip content="Enables Samba's fruit, catia, and streams_xattr modules for faster Finder browsing and Time Machine compatibility. Requires samba-vfs-modules." />
          </div>
          <Switch checked={macSupport} disabled={!canEdit} onChange={setMacSupport} aria-label="Enable macOS extensions" />
        </div>

        {activityEnabled && (
          <div className="flex items-center justify-between gap-3 rounded-xl border border-border bg-bg px-4 py-3">
            <div className="flex items-center gap-2.5">
              <Cpu className="size-4 shrink-0 text-primary" />
              <span className="text-sm font-medium text-fg">SMB activity logging</span>
              <InfoTip content="Uses Samba's full_audit module to record reads, writes, renames, and deletes for the Activity Feed and Ransomware Guard." />
            </div>
            <Switch checked={activityLog} disabled={!canEdit} onChange={setActivityLog} aria-label="Enable SMB activity log" />
          </div>
        )}
      </div>

      {canEdit && (
        <div className="flex items-center justify-between border-t border-border pt-4">
          <span className="text-xs text-fg-subtle">
            {initial.updatedAt && `Updated ${formatRelativeTime(initial.updatedAt)}`}
          </span>
          <Button type="submit" loading={saveSamba.isPending}>
            Save changes
          </Button>
        </div>
      )}
    </form>
  )
}

function DiscoveryCard({
  title,
  info,
  service,
  systemd,
  canEdit,
  loading,
  onToggle,
}: {
  title: string
  info: string
  service: DiscoveryService
  systemd: boolean
  canEdit: boolean
  loading: boolean
  onToggle: (next: boolean) => void
}) {
  const disabled = !canEdit || !systemd || !service.installed || loading

  return (
    <Card className="p-4">
      <div className="flex items-center justify-between gap-4">
        <div className="flex min-w-0 items-center gap-3">
          <span className="grid size-9 shrink-0 place-items-center rounded-lg bg-primary-soft text-primary">
            <Radio className="size-4" />
          </span>
          <div className="min-w-0">
            <div className="flex flex-wrap items-center gap-2">
              <h3 className="text-sm font-semibold text-fg">{title}</h3>
              <InfoTip content={info} label={`About ${title}`} />
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
            {service.hint && <p className="mt-1 text-xs text-fg-muted">{service.hint}</p>}
          </div>
        </div>

        <Switch checked={service.on} disabled={disabled} onChange={onToggle} aria-label={`Toggle ${title}`} />
      </div>
    </Card>
  )
}

export function SettingsPage() {
  const { can } = useSession()
  const modules = useModules()
  const canEdit = can('settings:write')
  const [tab, setTab] = useState<SettingsTab>('samba')

  const samba = useQuery(sambaSettingsQuery)
  const discovery = useQuery({ ...discoveryQuery, enabled: modules.discovery })
  const saveDiscovery = useUpdateDiscovery()

  return (
    <div className="flex flex-col gap-5">
      <PageHeader
        title="Settings"
        info="Server-wide Samba protocol options, LAN discovery daemons, configurable background task schedules, and encrypted config backups."
      />

      <Tabs
        value={tab}
        onChange={setTab}
        items={[
          { id: 'samba', label: 'Samba Server', icon: <Server className="size-3.5" /> },
          ...(modules.discovery ? [{ id: 'discovery' as const, label: 'Discovery', icon: <Radio className="size-3.5" /> }] : []),
          { id: 'notifications' as const, label: 'Notifications', icon: <Bell className="size-3.5" /> },
          ...(can('system:read') ? [{ id: 'tasks' as const, label: 'Scheduled Tasks', icon: <Clock className="size-3.5" /> }] : []),
          ...(modules.backup && can('backup:manage')
            ? [{ id: 'backup' as const, label: 'Backup & Restore', icon: <Archive className="size-3.5" /> }]
            : []),
        ]}
      />

      {tab === 'samba' && (
        <Card>
          <CardHeader>
            <CardTitle className="flex items-center gap-1.5">
              Samba Global Configuration
              <InfoTip content="Applied across every SMB share. Saving validates the configuration and reloads Samba." />
            </CardTitle>
          </CardHeader>
          <CardContent>
            {samba.isLoading ? (
              <div className="flex justify-center py-10">
                <Spinner />
              </div>
            ) : samba.error ? (
              <ErrorState error={samba.error} onRetry={() => void samba.refetch()} />
            ) : samba.data ? (
              <SambaSettingsForm
                key={samba.data.updatedAt || 'samba'}
                initial={samba.data}
                canEdit={canEdit}
                activityEnabled={modules.activityLog}
              />
            ) : null}
          </CardContent>
        </Card>
      )}

      {tab === 'discovery' && modules.discovery && (
        <div className="flex flex-col gap-3">
          {discovery.isLoading ? (
            <div className="flex justify-center py-12">
              <Spinner />
            </div>
          ) : discovery.error ? (
            <ErrorState error={discovery.error} onRetry={() => void discovery.refetch()} />
          ) : discovery.data ? (
            <>
              {!discovery.data.systemd && (
                <Alert tone="warning" title="Systemd unavailable — daemon toggles are read-only on this host." />
              )}

              <DiscoveryCard
                title="mDNS / Bonjour (Avahi)"
                info="Broadcasts this server to macOS Finder, Linux file managers, and advertises Apple Time Machine shares automatically."
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
                title="WS-Discovery (Windows)"
                info="Advertises this server in Windows File Explorer under Network neighborhood."
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

      {tab === 'notifications' && <NotificationsPanel canEdit={canEdit} />}

      {tab === 'tasks' && can('system:read') && <ScheduledTasksPanel canEdit={canEdit} />}

      {tab === 'backup' && modules.backup && can('backup:manage') && <BackupPanel />}
    </div>
  )
}
