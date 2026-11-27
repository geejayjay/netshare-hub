import { useQuery } from '@tanstack/react-query'
import { Check, Copy, KeyRound, Trash2 } from 'lucide-react'
import { useState, type FormEvent } from 'react'
import { notify } from '@/features/notifications'
import { errorMessage } from '@/shared/api'
import { copyToClipboard } from '@/shared/lib/clipboard'
import { formatDateTime, formatRelativeTime } from '@/shared/lib/format'
import { Alert, Badge, Button, Dialog, Input, Label, Select, Spinner, Switch } from '@/shared/ui'
import { apiTokensQuery, useCreateApiToken, useRevokeApiToken, type CreatedApiToken } from '../api'

const expiryChoices = [
  { days: 30, label: '30 days' },
  { days: 90, label: '90 days' },
  { days: 365, label: '1 year' },
  { days: 0, label: 'Never' },
]

/**
 * The signed-in user's API tokens: for scripts and tools that call the API
 * without a browser. A token acts as its owner; the token itself is shown
 * once, right after it is made.
 */
export function ApiTokensDialog({ open, onClose }: { open: boolean; onClose: () => void }) {
  const tokens = useQuery({ ...apiTokensQuery, enabled: open })
  const create = useCreateApiToken()
  const revoke = useRevokeApiToken()

  const [name, setName] = useState('')
  const [readOnly, setReadOnly] = useState(false)
  const [days, setDays] = useState(90)
  const [created, setCreated] = useState<CreatedApiToken | null>(null)
  const [copied, setCopied] = useState(false)

  function close() {
    setCreated(null)
    setCopied(false)
    create.reset()
    onClose()
  }

  function submit(e: FormEvent) {
    e.preventDefault()
    create.mutate(
      { name: name.trim(), readOnly, expiresInDays: days },
      {
        onSuccess: (token) => {
          setCreated(token)
          setName('')
          setReadOnly(false)
        },
      },
    )
  }

  function copy(token: string) {
    void copyToClipboard(token).then((ok) => {
      if (!ok) return
      setCopied(true)
      window.setTimeout(() => setCopied(false), 1600)
    })
  }

  return (
    <Dialog
      open={open}
      onClose={close}
      size="lg"
      title="API tokens"
      description="For scripts and tools that use the API without a browser. A token can do what you can do (or only read), but it can never manage tokens or take a config backup."
    >
      <div className="flex flex-col gap-5">
        {created && (
          <Alert tone="success" title={`Token "${created.name}" created. Copy it now: it is not shown again.`}>
            <div className="mt-2 flex items-center gap-2">
              <code className="min-w-0 flex-1 truncate rounded-md bg-surface px-2.5 py-1.5 font-mono text-xs text-fg ring-1 ring-border">
                {created.token}
              </code>
              <Button size="sm" variant="secondary" onClick={() => copy(created.token)} aria-label="Copy token">
                {copied ? <Check className="size-4" /> : <Copy className="size-4" />}
                {copied ? 'Copied' : 'Copy'}
              </Button>
            </div>
            <p className="mt-2 text-xs">
              Send it as <code className="font-mono">Authorization: Bearer &lt;token&gt;</code>.
            </p>
          </Alert>
        )}

        <form
          onSubmit={submit}
          className="grid gap-3 rounded-xl border border-border bg-bg p-4 sm:grid-cols-[1fr_auto_auto] sm:items-end"
        >
          <div className="flex flex-col gap-1.5">
            <Label htmlFor="token-name">Name</Label>
            <Input
              id="token-name"
              value={name}
              maxLength={64}
              placeholder="e.g. homelab monitoring"
              onChange={(e) => setName(e.target.value)}
            />
          </div>
          <div className="flex flex-col gap-1.5">
            <Label htmlFor="token-expiry">Expires</Label>
            <Select id="token-expiry" value={days} onChange={(e) => setDays(Number(e.target.value))}>
              {expiryChoices.map((c) => (
                <option key={c.days} value={c.days}>
                  {c.label}
                </option>
              ))}
            </Select>
          </div>
          <Button type="submit" loading={create.isPending} disabled={name.trim() === ''}>
            <KeyRound className="size-4" />
            Create token
          </Button>
          <div className="flex items-center gap-2 text-sm text-fg-muted sm:col-span-3">
            <Switch checked={readOnly} onChange={setReadOnly} aria-label="Read-only token" />
            Read-only (can look, cannot change anything)
          </div>
          {create.error && <Alert className="sm:col-span-3" title={errorMessage(create.error)} />}
        </form>

        <div className="flex flex-col gap-2">
          <h3 className="text-sm font-semibold text-fg">Your tokens</h3>
          {tokens.isLoading ? (
            <div className="flex justify-center py-6">
              <Spinner />
            </div>
          ) : tokens.error ? (
            <Alert title={errorMessage(tokens.error)} />
          ) : tokens.data?.items.length ? (
            <ul className="divide-y divide-border rounded-xl border border-border">
              {tokens.data.items.map((t) => {
                // Compared with when the list was loaded (render stays pure).
                const expired = t.expiresAt !== undefined && new Date(t.expiresAt).getTime() < tokens.dataUpdatedAt
                return (
                  <li key={t.id} className="flex items-center gap-3 px-4 py-3">
                    <div className="flex min-w-0 flex-1 flex-col gap-1">
                      <div className="flex flex-wrap items-center gap-2">
                        <span className="truncate text-sm font-medium text-fg">{t.name}</span>
                        <code className="font-mono text-xs text-fg-subtle">{t.prefix}…</code>
                        {t.readOnly && <Badge tone="info">Read-only</Badge>}
                        {expired && <Badge tone="danger">Expired</Badge>}
                      </div>
                      <span className="text-xs text-fg-subtle">
                        Created {formatDateTime(t.createdAt)} ·{' '}
                        {t.lastUsedAt ? `last used ${formatRelativeTime(t.lastUsedAt)}` : 'never used'}
                        {t.expiresAt && !expired && ` · expires ${formatDateTime(t.expiresAt)}`}
                      </span>
                    </div>
                    <Button
                      size="sm"
                      variant="ghost"
                      aria-label={`Revoke ${t.name}`}
                      disabled={revoke.isPending}
                      onClick={() => {
                        if (!window.confirm(`Revoke "${t.name}"? Anything using it stops working at once.`)) return
                        revoke.mutate(t.id, {
                          onSuccess: () => notify.success('Token revoked'),
                          onError: (error) => notify.error(error),
                        })
                      }}
                    >
                      <Trash2 className="size-4" />
                      Revoke
                    </Button>
                  </li>
                )
              })}
            </ul>
          ) : (
            <p className="rounded-xl border border-dashed border-border px-4 py-6 text-center text-sm text-fg-subtle">
              No tokens yet.
            </p>
          )}
        </div>
      </div>
    </Dialog>
  )
}
