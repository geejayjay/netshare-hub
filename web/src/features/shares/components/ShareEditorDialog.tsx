import { useQuery } from '@tanstack/react-query'
import { Check, Eye, Globe, HardDrive, Lock, Plus, Shield, Sparkles, Trash2, Users } from 'lucide-react'
import { useEffect, useState, type FormEvent } from 'react'
import { notify } from '@/features/notifications'
import { errorMessage, fieldErrors } from '@/shared/api'
import { FormField } from '@/shared/components'
import { cn } from '@/shared/lib/cn'
import { Alert, Badge, Button, Dialog, InfoTip, Input, Label, Select, Spinner, Switch, Tabs, type Tone } from '@/shared/ui'
import {
  sharePresetsQuery,
  shareUsersQuery,
  useCreateShare,
  usePreviewAccess,
  useUpdateShare,
  type NetworkRule,
  type Share,
  type ShareAccessInput,
  type SharePreset,
} from '../api'

type EditorTab = 'general' | 'access' | 'network' | 'preview'
type AccessLevel = ShareAccessInput['level']

interface ShareEditorDialogProps {
  open: boolean
  share?: Share | null
  onClose: () => void
}

function levelTone(level: string): Tone {
  switch (level) {
    case 'write':
      return 'info'
    case 'upload':
      return 'warning'
    case 'read':
      return 'success'
    default:
      return 'neutral'
  }
}

function initialAccessMap(share?: Share | null): Record<number, AccessLevel> {
  const map: Record<number, AccessLevel> = {}
  if (share) {
    for (const entry of share.access) map[entry.userId] = entry.level
  }
  return map
}

function ShareEditorBody({ share, onClose }: { share?: Share | null; onClose: () => void }) {
  const isEdit = Boolean(share)
  const presets = useQuery(sharePresetsQuery)
  const users = useQuery(shareUsersQuery)
  const createShare = useCreateShare()
  const updateShare = useUpdateShare()
  const preview = usePreviewAccess()

  const [tab, setTab] = useState<EditorTab>('general')
  const [preset, setPreset] = useState(share?.preset || 'custom')
  const [name, setName] = useState(share?.name ?? '')
  const [path, setPath] = useState(share?.path ?? '')
  const [createDirectory, setCreateDirectory] = useState(!isEdit)
  const [comment, setComment] = useState(share?.comment ?? '')
  const [readOnly, setReadOnly] = useState(share?.readOnly ?? false)
  const [guestAccess, setGuestAccess] = useState(share?.guestAccess ?? false)
  const [hideUnreadable, setHideUnreadable] = useState(share?.hideUnreadable ?? false)
  const [timeMachine, setTimeMachine] = useState(share?.timeMachine ?? false)
  const [timeMachineMaxGiB, setTimeMachineMaxGiB] = useState(String(share?.timeMachineMaxGiB ?? 0))
  const [access, setAccess] = useState<Record<number, AccessLevel>>(() => initialAccessMap(share))
  const [networkRules, setNetworkRules] = useState<NetworkRule[]>(() => share?.networkRules ?? [])
  const [newRuleAction, setNewRuleAction] = useState<NetworkRule['action']>('allow')
  const [newRuleAddress, setNewRuleAddress] = useState('')
  const [submitError, setSubmitError] = useState<unknown>(null)

  const accessList: ShareAccessInput[] = Object.entries(access).map(([userId, level]) => ({
    userId: Number(userId),
    level,
  }))

  const previewKey = JSON.stringify({ accessList, guestAccess, readOnly, timeMachine, networkRules })
  useEffect(() => {
    if (tab !== 'preview') return
    preview.mutate({
      access: accessList,
      guestAccess,
      readOnly,
      timeMachine,
      networkRules,
    })
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [tab, previewKey])

  function applyPreset(p: SharePreset) {
    setPreset(p.id)
    setReadOnly(p.readOnly)
    setGuestAccess(p.guestAccess)
    setTimeMachine(p.timeMachine)
    if (p.timeMachine) {
      setReadOnly(false)
      setGuestAccess(false)
    }
    if (p.defaultLevel) {
      setAccess((prev) => {
        const next: Record<number, AccessLevel> = {}
        for (const uid of Object.keys(prev)) {
          next[Number(uid)] = p.defaultLevel as AccessLevel
        }
        return next
      })
    }
  }

  function setUserLevel(userId: number, level: AccessLevel | 'none') {
    setAccess((prev) => {
      const next = { ...prev }
      if (level === 'none') delete next[userId]
      else next[userId] = level
      return next
    })
  }

  function addNetworkRule() {
    const addr = newRuleAddress.trim()
    if (!addr) return
    setNetworkRules((prev) => [...prev, { action: newRuleAction, address: addr }])
    setNewRuleAddress('')
  }

  function removeNetworkRule(index: number) {
    setNetworkRules((prev) => prev.filter((_, i) => i !== index))
  }

  const errors = fieldErrors(submitError)
  const pending = createShare.isPending || updateShare.isPending

  function handleSubmit(e: FormEvent) {
    e.preventDefault()
    setSubmitError(null)
    const maxGiB = Math.max(0, Number.parseInt(timeMachineMaxGiB || '0', 10) || 0)

    if (isEdit && share) {
      updateShare.mutate(
        {
          id: share.id,
          body: {
            comment: comment.trim(),
            preset,
            readOnly,
            guestAccess,
            hideUnreadable,
            timeMachine,
            timeMachineMaxGiB: timeMachine ? maxGiB : 0,
            access: accessList,
            networkRules,
          },
        },
        {
          onSuccess: () => {
            notify.success('Share updated', `Saved changes for "${share.name}".`)
            onClose()
          },
          onError: (err) => setSubmitError(err),
        },
      )
      return
    }

    createShare.mutate(
      {
        name: name.trim(),
        path: path.trim(),
        createDirectory,
        comment: comment.trim() || undefined,
        preset,
        readOnly,
        guestAccess,
        hideUnreadable,
        timeMachine,
        timeMachineMaxGiB: timeMachine ? maxGiB : 0,
        access: accessList,
        networkRules,
      },
      {
        onSuccess: (created) => {
          notify.success('Share created', `Shared "${created.name}" over SMB.`)
          onClose()
        },
        onError: (err) => setSubmitError(err),
      },
    )
  }

  const presetItems = presets.data?.items ?? []
  const userItems = users.data?.items ?? []

  return (
    <form onSubmit={handleSubmit} className="flex flex-col gap-5">
      {submitError !== null && <Alert tone="danger" title={errorMessage(submitError)} />}

      <Tabs
        value={tab}
        onChange={setTab}
        items={[
          { id: 'general', label: 'General', icon: <Sparkles className="size-3.5" /> },
          { id: 'access', label: 'Access', icon: <Users className="size-3.5" />, badge: accessList.length },
          { id: 'network', label: 'Network', icon: <Globe className="size-3.5" />, badge: networkRules.length },
          { id: 'preview', label: 'Preview', icon: <Eye className="size-3.5" /> },
        ]}
      />

      {tab === 'general' && (
        <div className="flex flex-col gap-4">
          {presetItems.length > 0 && (
            <div className="flex flex-col gap-2">
              <div className="flex items-center gap-1.5">
                <Label>Preset</Label>
                <InfoTip content="Quickly fills recommended read-only, guest, and Time Machine options. You can customize any option afterwards." />
              </div>
              <div className="grid gap-2 sm:grid-cols-2">
                {presetItems.map((p) => {
                  const selected = preset === p.id
                  return (
                    <button
                      key={p.id}
                      type="button"
                      onClick={() => applyPreset(p)}
                      className={cn(
                        'flex items-center justify-between gap-2 rounded-lg border px-3 py-2.5 text-left transition-all',
                        selected
                          ? 'border-primary bg-primary-soft/50 ring-1 ring-primary/30'
                          : 'border-border bg-bg hover:border-border-strong',
                      )}
                    >
                      <div className="flex min-w-0 items-center gap-1.5">
                        <span className="truncate text-sm font-medium text-fg">{p.name}</span>
                        {p.description && <InfoTip content={p.description} label={`About ${p.name}`} />}
                      </div>
                      <div className="flex shrink-0 items-center gap-1.5">
                        {p.timeMachine && <Badge tone="info">TM</Badge>}
                        {selected && <Check className="size-4 text-primary" />}
                      </div>
                    </button>
                  )
                })}
              </div>
            </div>
          )}

          <div className="grid gap-4 sm:grid-cols-2">
            <FormField
              label="Share name"
              htmlFor="share-name"
              info="The network name clients connect to (\\server\name). Letters, numbers, and dashes."
              error={errors.name}
            >
              <Input
                id="share-name"
                value={name}
                onChange={(e) => setName(e.target.value)}
                disabled={isEdit}
                placeholder="media"
                required
              />
            </FormField>

            <FormField
              label="Folder path"
              htmlFor="share-path"
              info="Full absolute folder path on the server."
              error={errors.path}
            >
              <Input
                id="share-path"
                value={path}
                onChange={(e) => setPath(e.target.value)}
                disabled={isEdit}
                placeholder="/srv/shares/media"
                required
              />
            </FormField>
          </div>

          {!isEdit && (
            <div className="flex items-center justify-between gap-3 rounded-lg border border-border bg-bg px-3.5 py-2.5">
              <div className="flex items-center gap-1.5">
                <span className="text-sm font-medium text-fg">Create folder if missing</span>
                <InfoTip content="Automatically creates the directory on disk if it does not exist yet (its parent directory must exist)." />
              </div>
              <Switch checked={createDirectory} onChange={setCreateDirectory} aria-label="Create folder if missing" />
            </div>
          )}

          <FormField label="Description" htmlFor="share-comment" error={errors.comment}>
            <Input
              id="share-comment"
              value={comment}
              onChange={(e) => setComment(e.target.value)}
              placeholder="Optional note shown to users"
              maxLength={100}
            />
          </FormField>

          <div className="flex flex-col gap-3 rounded-xl border border-border bg-bg px-4 py-3">
            <div className="flex items-center justify-between gap-3">
              <div className="flex items-center gap-2">
                <HardDrive className="size-4 text-primary" />
                <span className="text-sm font-medium text-fg">Apple Time Machine</span>
                <InfoTip content="Enables macOS fruit extensions and advertises this share for Time Machine backups. Requires authenticated write users." />
              </div>
              <Switch
                checked={timeMachine}
                onChange={(checked) => {
                  setTimeMachine(checked)
                  if (checked) {
                    setReadOnly(false)
                    setGuestAccess(false)
                  }
                }}
                aria-label="Enable Time Machine"
              />
            </div>

            {timeMachine && (
              <FormField
                label="Quota (GiB)"
                htmlFor="tm-max-gib"
                info="Maximum disk size reported to Time Machine. Set to 0 to expose the full underlying disk."
                error={errors.timeMachineMaxGiB}
              >
                <Input
                  id="tm-max-gib"
                  type="number"
                  min={0}
                  max={1048576}
                  value={timeMachineMaxGiB}
                  onChange={(e) => setTimeMachineMaxGiB(e.target.value)}
                />
              </FormField>
            )}
          </div>
        </div>
      )}

      {tab === 'access' && (
        <div className="flex flex-col gap-4">
          <div className="grid gap-2.5 sm:grid-cols-3">
            <div className="flex items-center justify-between gap-2 rounded-lg border border-border bg-bg px-3 py-2.5">
              <div className="flex items-center gap-1">
                <span className="text-xs font-medium text-fg">Read-only</span>
                <InfoTip content="Prevents all writes regardless of per-user permissions." />
              </div>
              <Switch checked={readOnly} disabled={timeMachine} onChange={setReadOnly} aria-label="Read-only share" />
            </div>

            <div className="flex items-center justify-between gap-2 rounded-lg border border-border bg-bg px-3 py-2.5">
              <div className="flex items-center gap-1">
                <span className="text-xs font-medium text-fg">Guest read</span>
                <InfoTip content="Anyone on the network can browse and read without a password." />
              </div>
              <Switch
                checked={guestAccess}
                disabled={timeMachine}
                onChange={setGuestAccess}
                aria-label="Guest read access"
              />
            </div>

            <div className="flex items-center justify-between gap-2 rounded-lg border border-border bg-bg px-3 py-2.5">
              <div className="flex items-center gap-1">
                <span className="text-xs font-medium text-fg">Hide unreadable</span>
                <InfoTip content="Hides files and folders a user cannot open (access-based enumeration). May slow down very large directories." />
              </div>
              <Switch checked={hideUnreadable} onChange={setHideUnreadable} aria-label="Hide unreadable files" />
            </div>
          </div>

          <div className="overflow-hidden rounded-xl border border-border">
            <div className="flex items-center justify-between border-b border-border bg-surface-2/60 px-4 py-2 text-xs font-medium text-fg-muted">
              <span>User</span>
              <span className="inline-flex items-center gap-1">
                Level
                <InfoTip content="Read = view only · Drop box = upload without viewing others' files · Write = full read & write." />
              </span>
            </div>

            {users.isLoading ? (
              <div className="flex justify-center py-8">
                <Spinner />
              </div>
            ) : userItems.length === 0 ? (
              <div className="p-6 text-center text-sm text-fg-muted">No users available.</div>
            ) : (
              <div className="divide-y divide-border">
                {userItems.map((u) => {
                  const current = access[u.id] ?? 'none'
                  return (
                    <div key={u.id} className="flex flex-wrap items-center justify-between gap-3 px-4 py-2.5">
                      <div className="flex min-w-0 items-center gap-2">
                        <span className="text-sm font-medium text-fg">{u.username}</span>
                        {u.displayName && <span className="text-xs text-fg-muted">{u.displayName}</span>}
                        {u.disabled && <Badge tone="danger">disabled</Badge>}
                      </div>
                      <div className="inline-flex rounded-lg border border-border bg-surface-2 p-0.5">
                        {(['none', 'read', 'upload', 'write'] as const).map((lvl) => {
                          const active = current === lvl
                          return (
                            <button
                              key={lvl}
                              type="button"
                              onClick={() => setUserLevel(u.id, lvl)}
                              className={cn(
                                'rounded-md px-2.5 py-1 text-xs font-medium capitalize transition-colors',
                                active
                                  ? 'bg-surface text-fg shadow-xs ring-1 ring-border'
                                  : 'text-fg-muted hover:text-fg',
                              )}
                            >
                              {lvl === 'upload' ? 'Drop box' : lvl}
                            </button>
                          )
                        })}
                      </div>
                    </div>
                  )
                })}
              </div>
            )}
          </div>
        </div>
      )}

      {tab === 'network' && (
        <div className="flex flex-col gap-4">
          <div className="flex items-center gap-1.5">
            <Label>IP & CIDR rules</Label>
            <InfoTip content="When any 'allow' rule exists, only matching addresses may connect. 'Block' rules always deny access. Leave empty to allow any host." />
          </div>

          <div className="flex flex-wrap gap-2">
            <Select
              className="w-28"
              value={newRuleAction}
              onChange={(e) => setNewRuleAction(e.target.value as NetworkRule['action'])}
              aria-label="Rule action"
            >
              <option value="allow">Allow</option>
              <option value="block">Block</option>
            </Select>
            <Input
              className="flex-1"
              value={newRuleAddress}
              onChange={(e) => setNewRuleAddress(e.target.value)}
              placeholder="192.168.1.0/24 or 10.0.0.50"
              onKeyDown={(e) => {
                if (e.key === 'Enter') {
                  e.preventDefault()
                  addNetworkRule()
                }
              }}
            />
            <Button variant="secondary" onClick={addNetworkRule}>
              <Plus className="size-4" />
              Add
            </Button>
          </div>

          {networkRules.length === 0 ? (
            <div className="rounded-xl border border-dashed border-border p-6 text-center text-xs text-fg-muted">
              No rules — any host on the network may connect.
            </div>
          ) : (
            <div className="divide-y divide-border overflow-hidden rounded-xl border border-border">
              {networkRules.map((rule, idx) => (
                <div key={`${rule.action}-${rule.address}-${idx}`} className="flex items-center justify-between px-4 py-2.5">
                  <div className="flex items-center gap-2.5">
                    <Badge tone={rule.action === 'allow' ? 'success' : 'danger'}>{rule.action}</Badge>
                    <code className="text-xs text-fg">{rule.address}</code>
                  </div>
                  <Button
                    variant="ghost"
                    size="icon"
                    className="size-7 text-fg-subtle hover:text-danger"
                    aria-label="Remove rule"
                    onClick={() => removeNetworkRule(idx)}
                  >
                    <Trash2 className="size-3.5" />
                  </Button>
                </div>
              ))}
            </div>
          )}
        </div>
      )}

      {tab === 'preview' && (
        <div className="flex flex-col gap-4">
          {preview.isPending ? (
            <div className="flex justify-center py-10">
              <Spinner />
            </div>
          ) : preview.error ? (
            <Alert tone="danger" title={errorMessage(preview.error)} />
          ) : preview.data ? (
            <>
              <div className="grid gap-3 sm:grid-cols-3">
                <div className="rounded-lg border border-border bg-bg p-3">
                  <div className="text-[0.6875rem] font-medium text-fg-subtle uppercase">Guests</div>
                  <div className="mt-1">
                    <Badge tone={levelTone(preview.data.guests)}>{preview.data.guests}</Badge>
                  </div>
                </div>
                <div className="rounded-lg border border-border bg-bg p-3">
                  <div className="text-[0.6875rem] font-medium text-fg-subtle uppercase">Others</div>
                  <div className="mt-1">
                    <Badge tone={levelTone(preview.data.others)}>{preview.data.others}</Badge>
                  </div>
                </div>
                <div className="rounded-lg border border-border bg-bg p-3">
                  <div className="text-[0.6875rem] font-medium text-fg-subtle uppercase">Network</div>
                  <div className="mt-1 text-xs font-medium text-fg">{preview.data.network || 'Any host'}</div>
                </div>
              </div>

              {preview.data.note && (
                <div className="flex items-center gap-2 rounded-lg border border-border bg-surface-2/50 px-3 py-2 text-xs text-fg-muted">
                  <Shield className="size-3.5 shrink-0 text-primary" />
                  <span>{preview.data.note}</span>
                </div>
              )}

              <div className="divide-y divide-border overflow-hidden rounded-xl border border-border">
                {preview.data.users.length === 0 ? (
                  <div className="p-6 text-center text-xs text-fg-muted">No users assigned yet.</div>
                ) : (
                  preview.data.users.map((u) => (
                    <div key={u.userId} className="flex flex-wrap items-center justify-between gap-2 px-4 py-2.5">
                      <div className="flex items-center gap-1.5">
                        <span className="text-sm font-medium text-fg">{u.username}</span>
                        {u.reason && <InfoTip content={u.reason} label={`Reason for ${u.username}`} />}
                      </div>
                      <Badge tone={levelTone(u.level)} dot>
                        {u.level}
                      </Badge>
                    </div>
                  ))
                )}
              </div>
            </>
          ) : null}
        </div>
      )}

      <footer className="flex items-center justify-between gap-3 border-t border-border pt-4">
        <div className="flex items-center gap-1.5 text-xs text-fg-muted">
          {readOnly && (
            <Badge tone="warning">
              <Lock className="size-3" /> Read-only
            </Badge>
          )}
          {guestAccess && <Badge tone="info">Guest</Badge>}
          {timeMachine && <Badge tone="info">Time Machine</Badge>}
        </div>
        <div className="flex items-center gap-2">
          <Button variant="secondary" onClick={onClose}>
            Cancel
          </Button>
          <Button type="submit" loading={pending}>
            {isEdit ? 'Save changes' : 'Create share'}
          </Button>
        </div>
      </footer>
    </form>
  )
}

export function ShareEditorDialog({ open, share, onClose }: ShareEditorDialogProps) {
  const isEdit = Boolean(share)
  return (
    <Dialog open={open} onClose={onClose} size="lg" title={isEdit ? `Edit share — ${share?.name}` : 'New SMB share'}>
      <ShareEditorBody key={share ? `edit-${share.id}-${share.updatedAt}` : 'create'} share={share} onClose={onClose} />
    </Dialog>
  )
}
