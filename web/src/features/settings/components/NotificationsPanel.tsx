import { useQuery } from '@tanstack/react-query'
import { Bell, CheckCircle2, Mail, Pencil, Plus, Send, Trash2, Webhook, XCircle } from 'lucide-react'
import { useState, type FormEvent } from 'react'
import { notify } from '@/features/notifications'
import { errorMessage, fieldErrors } from '@/shared/api'
import { EmptyState, ErrorState, FormField } from '@/shared/components'
import { formatRelativeTime } from '@/shared/lib/format'
import {
  Alert,
  Badge,
  Button,
  Card,
  CardContent,
  CardHeader,
  CardTitle,
  Dialog,
  InfoTip,
  Input,
  Select,
  Spinner,
  Switch,
} from '@/shared/ui'
import {
  notificationChannelsQuery,
  useCreateNotificationChannel,
  useDeleteNotificationChannel,
  useTestNotificationChannel,
  useUpdateNotificationChannel,
  type ChannelInput,
  type NotificationChannel,
} from '../api'

type Kind = NotificationChannel['kind']
type Severity = NotificationChannel['minSeverity']

const KIND_META: Record<Kind, { label: string; hint: string }> = {
  ntfy: { label: 'ntfy', hint: 'Push notifications via ntfy.sh or a self-hosted ntfy topic URL.' },
  gotify: { label: 'Gotify', hint: 'Self-hosted Gotify server URL plus an application token.' },
  webhook: {
    label: 'Webhook (Discord / Slack / HTTP)',
    hint: 'JSON POST payload compatible with Discord, Slack, or custom automation endpoints.',
  },
  smtp: { label: 'SMTP Email', hint: 'Send alert emails via your mail server.' },
}

const SEVERITY_TONE: Record<Severity, 'info' | 'warning' | 'danger' | 'neutral'> = {
  info: 'info',
  warning: 'warning',
  error: 'danger',
  critical: 'danger',
}

function ChannelFormDialog({
  open,
  editing,
  onClose,
}: {
  open: boolean
  editing: NotificationChannel | null
  onClose: () => void
}) {
  const create = useCreateNotificationChannel()
  const update = useUpdateNotificationChannel()

  const [name, setName] = useState(editing?.name ?? '')
  const [kind, setKind] = useState<Kind>(editing?.kind ?? 'ntfy')
  const [minSeverity, setMinSeverity] = useState<Severity>(editing?.minSeverity ?? 'warning')
  const [enabled, setEnabled] = useState(editing ? editing.enabled : true)

  const [url, setUrl] = useState(editing?.config.url ?? '')
  const [token, setToken] = useState('')
  const [secret, setSecret] = useState('')
  const [host, setHost] = useState(editing?.config.host ?? '')
  const [port, setPort] = useState(String(editing?.config.port ?? 587))
  const [username, setUsername] = useState(editing?.config.username ?? '')
  const [password, setPassword] = useState('')
  const [from, setFrom] = useState(editing?.config.from ?? '')
  const [to, setTo] = useState(editing?.config.to ?? '')
  const [submitError, setSubmitError] = useState<unknown>(null)

  const errors = fieldErrors(submitError)
  const pending = create.isPending || update.isPending

  function handleSubmit(e: FormEvent) {
    e.preventDefault()
    setSubmitError(null)
    const config: ChannelInput['config'] =
      kind === 'smtp'
        ? {
            host: host.trim(),
            port: Number(port) || 587,
            username: username.trim() || undefined,
            password: password || undefined,
            from: from.trim(),
            to: to.trim(),
          }
        : {
            url: url.trim(),
            token: token || undefined,
            secret: secret || undefined,
          }

    if (editing) {
      update.mutate(
        {
          id: editing.id,
          body: { name: name.trim(), enabled, minSeverity, config },
        },
        {
          onSuccess: () => {
            notify.success('Channel updated', name.trim())
            onClose()
          },
          onError: (err) => setSubmitError(err),
        },
      )
      return
    }

    create.mutate(
      { name: name.trim(), kind, enabled, minSeverity, config },
      {
        onSuccess: () => {
          notify.success('Channel added', name.trim())
          onClose()
        },
        onError: (err) => setSubmitError(err),
      },
    )
  }

  return (
    <Dialog
      open={open}
      onClose={onClose}
      size="md"
      title={editing ? `Edit ${editing.name}` : 'Add notification channel'}
      description={KIND_META[kind].hint}
    >
      <form onSubmit={handleSubmit} className="flex flex-col gap-4">
        <div className="flex flex-col gap-4">
          {submitError !== null && <Alert tone="danger" title={errorMessage(submitError)} />}

          <div className="grid gap-3 sm:grid-cols-2">
            <FormField label="Channel name" htmlFor="nc-name" error={errors.name}>
              <Input
                id="nc-name"
                value={name}
                placeholder="Homelab Discord"
                onChange={(e) => setName(e.target.value)}
                required
              />
            </FormField>

            <FormField label="Provider" htmlFor="nc-kind" error={errors.kind}>
              <Select
                id="nc-kind"
                value={kind}
                disabled={Boolean(editing)}
                onChange={(e) => setKind(e.target.value as Kind)}
              >
                <option value="ntfy">ntfy</option>
                <option value="gotify">Gotify</option>
                <option value="webhook">Webhook (Discord / Slack / HTTP)</option>
                <option value="smtp">SMTP Email</option>
              </Select>
            </FormField>
          </div>

          <div className="grid gap-3 sm:grid-cols-2">
            <FormField
              label="Minimum severity"
              htmlFor="nc-sev"
              info="Only new alerts at or above this severity are sent to this channel."
              error={errors.minSeverity}
            >
              <Select id="nc-sev" value={minSeverity} onChange={(e) => setMinSeverity(e.target.value as Severity)}>
                <option value="info">Info and above</option>
                <option value="warning">Warning and above</option>
                <option value="error">Error and above</option>
                <option value="critical">Critical only</option>
              </Select>
            </FormField>

            <div className="flex items-end pb-1">
              <div className="flex w-full items-center justify-between rounded-lg border border-border bg-surface-2/40 px-3 py-2">
                <span className="text-xs font-medium text-fg">Enabled</span>
                <Switch checked={enabled} onChange={setEnabled} aria-label="Channel enabled" />
              </div>
            </div>
          </div>

          {kind !== 'smtp' ? (
            <>
              <FormField
                label={kind === 'ntfy' ? 'Topic URL' : kind === 'gotify' ? 'Server base URL' : 'Webhook URL'}
                htmlFor="nc-url"
                error={errors['config.url']}
              >
                <Input
                  id="nc-url"
                  type="url"
                  value={url}
                  placeholder={
                    kind === 'ntfy'
                      ? 'https://ntfy.sh/my-nas-alerts'
                      : kind === 'gotify'
                        ? 'https://gotify.example.com'
                        : 'https://discord.com/api/webhooks/...'
                  }
                  onChange={(e) => setUrl(e.target.value)}
                  required
                />
              </FormField>

              {(kind === 'ntfy' || kind === 'gotify') && (
                <FormField
                  label={kind === 'gotify' ? 'Application token' : 'Access token (optional)'}
                  htmlFor="nc-token"
                  info={editing?.hasSecret ? 'Leave blank to keep the existing stored token.' : undefined}
                  error={errors['config.token']}
                >
                  <Input
                    id="nc-token"
                    type="password"
                    value={token}
                    required={kind === 'gotify' && !editing?.hasSecret}
                    placeholder={editing?.hasSecret ? '•••••••• (unchanged)' : 'tk_...'}
                    onChange={(e) => setToken(e.target.value)}
                  />
                </FormField>
              )}

              {kind === 'webhook' && (
                <FormField
                  label="HMAC signing secret (optional)"
                  htmlFor="nc-secret"
                  info="When set, requests include an X-NetShare-Signature: sha256=... header."
                >
                  <Input
                    id="nc-secret"
                    type="password"
                    value={secret}
                    placeholder={editing?.hasSecret ? '•••••••• (unchanged)' : 'Optional shared secret'}
                    onChange={(e) => setSecret(e.target.value)}
                  />
                </FormField>
              )}
            </>
          ) : (
            <>
              <div className="grid gap-3 sm:grid-cols-3">
                <div className="sm:col-span-2">
                  <FormField label="SMTP host" htmlFor="nc-host" error={errors['config.host']}>
                    <Input
                      id="nc-host"
                      value={host}
                      placeholder="smtp.example.com"
                      onChange={(e) => setHost(e.target.value)}
                      required
                    />
                  </FormField>
                </div>
                <FormField label="Port" htmlFor="nc-port" error={errors['config.port']}>
                  <Input
                    id="nc-port"
                    type="number"
                    min={1}
                    max={65535}
                    value={port}
                    onChange={(e) => setPort(e.target.value)}
                    required
                  />
                </FormField>
              </div>
              <div className="grid gap-3 sm:grid-cols-2">
                <FormField label="Username" htmlFor="nc-user">
                  <Input id="nc-user" value={username} onChange={(e) => setUsername(e.target.value)} />
                </FormField>
                <FormField
                  label="Password"
                  htmlFor="nc-pass"
                  info={editing?.hasSecret ? 'Leave blank to keep stored password.' : undefined}
                >
                  <Input
                    id="nc-pass"
                    type="password"
                    value={password}
                    placeholder={editing?.hasSecret ? '•••••••• (unchanged)' : ''}
                    onChange={(e) => setPassword(e.target.value)}
                  />
                </FormField>
              </div>
              <div className="grid gap-3 sm:grid-cols-2">
                <FormField label="From address" htmlFor="nc-from" error={errors['config.from']}>
                  <Input
                    id="nc-from"
                    type="email"
                    value={from}
                    placeholder="nas@example.com"
                    onChange={(e) => setFrom(e.target.value)}
                    required
                  />
                </FormField>
                <FormField
                  label="Recipients"
                  htmlFor="nc-to"
                  info="Comma-separated email addresses."
                  error={errors['config.to']}
                >
                  <Input
                    id="nc-to"
                    value={to}
                    placeholder="admin@example.com"
                    onChange={(e) => setTo(e.target.value)}
                    required
                  />
                </FormField>
              </div>
            </>
          )}
        </div>
        <div className="flex items-center justify-end gap-2 border-t border-border pt-4">
          <Button variant="ghost" type="button" onClick={onClose}>
            Cancel
          </Button>
          <Button type="submit" loading={pending}>
            {editing ? 'Save changes' : 'Add channel'}
          </Button>
        </div>
      </form>
    </Dialog>
  )
}

export function NotificationsPanel({ canEdit }: { canEdit: boolean }) {
  const query = useQuery(notificationChannelsQuery)
  const update = useUpdateNotificationChannel()
  const remove = useDeleteNotificationChannel()
  const test = useTestNotificationChannel()

  const [dialogOpen, setDialogOpen] = useState(false)
  const [editing, setEditing] = useState<NotificationChannel | null>(null)
  const [testingId, setTestingId] = useState<number | null>(null)

  function openAdd() {
    setEditing(null)
    setDialogOpen(true)
  }

  function openEdit(ch: NotificationChannel) {
    setEditing(ch)
    setDialogOpen(true)
  }

  function handleTest(ch: NotificationChannel) {
    setTestingId(ch.id)
    test.mutate(ch.id, {
      onSuccess: (res) => {
        setTestingId(null)
        if (res.ok) {
          notify.success('Test notification delivered', `${ch.name} responded in ${res.latencyMs} ms.`)
        } else {
          notify.error(res.error || 'Delivery failed', `${ch.name} test failed`)
        }
      },
      onError: (err) => {
        setTestingId(null)
        notify.error(err)
      },
    })
  }

  const items = query.data?.items ?? []

  return (
    <Card>
      <CardHeader className="flex-row items-center justify-between gap-2 pb-3">
        <CardTitle className="flex items-center gap-1.5">
          Notification Channels
          <InfoTip content="Forward server alerts (ransomware blocks, bans, replication failures, disk warnings) to ntfy, Gotify, Discord/Slack webhooks, or SMTP email." />
        </CardTitle>
        {canEdit && (
          <Button size="sm" onClick={openAdd}>
            <Plus className="size-3.5" />
            Add channel
          </Button>
        )}
      </CardHeader>
      <CardContent className="border-t border-border p-0">
        {query.isLoading ? (
          <div className="flex justify-center py-10">
            <Spinner />
          </div>
        ) : query.error ? (
          <div className="p-4">
            <ErrorState error={query.error} onRetry={() => void query.refetch()} />
          </div>
        ) : items.length === 0 ? (
          <div className="p-6">
            <EmptyState
              title="No notification channels yet"
              description="Add ntfy, Gotify, Discord/Slack webhook, or SMTP email to receive alerts off-box."
              action={
                canEdit ? (
                  <Button size="sm" onClick={openAdd}>
                    <Plus className="size-3.5" />
                    Add channel
                  </Button>
                ) : undefined
              }
            />
          </div>
        ) : (
          <div className="divide-y divide-border">
            {items.map((ch) => (
              <div key={ch.id} className="flex flex-wrap items-center justify-between gap-3 px-5 py-3.5">
                <div className="flex min-w-0 items-start gap-3">
                  <div className="mt-0.5 flex size-8 shrink-0 items-center justify-center rounded-lg bg-surface-2 text-fg-muted">
                    {ch.kind === 'smtp' ? (
                      <Mail className="size-4" />
                    ) : ch.kind === 'webhook' ? (
                      <Webhook className="size-4" />
                    ) : (
                      <Bell className="size-4" />
                    )}
                  </div>
                  <div className="min-w-0">
                    <div className="flex flex-wrap items-center gap-2">
                      <span className="text-sm font-semibold text-fg">{ch.name}</span>
                      <Badge tone="neutral" className="uppercase">
                        {ch.kind}
                      </Badge>
                      <Badge tone={SEVERITY_TONE[ch.minSeverity]}>≥ {ch.minSeverity}</Badge>
                      {!ch.enabled && <Badge tone="neutral">Paused</Badge>}
                    </div>
                    <div className="mt-0.5 truncate text-xs text-fg-muted">
                      {ch.kind === 'smtp'
                        ? `${ch.config.host}:${ch.config.port} → ${ch.config.to}`
                        : ch.config.url}
                    </div>
                    <div className="mt-1 flex flex-wrap items-center gap-3 text-[0.7rem] text-fg-subtle">
                      {ch.lastSentAt && (
                        <span className="inline-flex items-center gap-1 text-success">
                          <CheckCircle2 className="size-3" />
                          Last sent {formatRelativeTime(ch.lastSentAt)}
                        </span>
                      )}
                      {ch.lastError && (
                        <span className="inline-flex items-center gap-1 text-danger" title={ch.lastError}>
                          <XCircle className="size-3" />
                          {ch.lastError.slice(0, 90)}
                        </span>
                      )}
                    </div>
                  </div>
                </div>

                <div className="flex items-center gap-2">
                  {canEdit && (
                    <>
                      <Switch
                        checked={ch.enabled}
                        aria-label={`Toggle ${ch.name}`}
                        onChange={(next) =>
                          update.mutate(
                            { id: ch.id, body: { enabled: next } },
                            { onError: (err) => notify.error(err) },
                          )
                        }
                      />
                      <Button
                        size="sm"
                        variant="secondary"
                        loading={testingId === ch.id}
                        onClick={() => handleTest(ch)}
                      >
                        <Send className="size-3.5" />
                        Test
                      </Button>
                      <Button size="sm" variant="ghost" aria-label={`Edit ${ch.name}`} onClick={() => openEdit(ch)}>
                        <Pencil className="size-3.5" />
                      </Button>
                      <Button
                        size="sm"
                        variant="ghost"
                        aria-label={`Delete ${ch.name}`}
                        onClick={() =>
                          remove.mutate(ch.id, {
                            onSuccess: () => notify.success('Channel removed', ch.name),
                            onError: (err) => notify.error(err),
                          })
                        }
                      >
                        <Trash2 className="size-3.5 text-danger" />
                      </Button>
                    </>
                  )}
                </div>
              </div>
            ))}
          </div>
        )}
      </CardContent>

      {dialogOpen && (
        <ChannelFormDialog
          key={editing ? `edit-${editing.id}` : 'new'}
          open={dialogOpen}
          editing={editing}
          onClose={() => setDialogOpen(false)}
        />
      )}
    </Card>
  )
}
