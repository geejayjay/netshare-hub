import { useQuery } from '@tanstack/react-query'
import { Check, Eye, Globe, HardDrive, Lock, Network, Plus, Recycle, Shield, Sparkles, Trash2, Users, UsersRound } from 'lucide-react'
import { useDeferredValue, useEffect, useState, type FormEvent } from 'react'
import { groupsQuery } from '@/features/groups'
import { notify } from '@/features/notifications'
import { errorMessage, fieldErrors } from '@/shared/api'
import { FormField } from '@/shared/components'
import { cn } from '@/shared/lib/cn'
import { useModules } from '@/shared/modules/modules'
import { Alert, Badge, Button, Dialog, InfoTip, Input, Label, Select, Spinner, Switch, Tabs, type Tone } from '@/shared/ui'
import {
  mountCheckQuery,
  nfsStatusQuery,
  sharePresetsQuery,
  shareUsersQuery,
  useCreateShare,
  usePreviewAccess,
  useUpdateShare,
  type NFSClient,
  type NetworkRule,
  type Share,
  type ShareAccessInput,
  type ShareNFS,
  type SharePreset,
} from '../api'

type EditorTab = 'general' | 'access' | 'network' | 'nfs' | 'preview'
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

function initialGroupAccessMap(share?: Share | null): Record<number, AccessLevel> {
  const map: Record<number, AccessLevel> = {}
  if (share) {
    for (const entry of share.groupAccess ?? []) map[entry.groupId] = entry.level
  }
  return map
}

function ShareEditorBody({ share, onClose }: { share?: Share | null; onClose: () => void }) {
  const isEdit = Boolean(share)
  const modules = useModules()
  const presets = useQuery(sharePresetsQuery)
  const users = useQuery(shareUsersQuery)
  const groups = useQuery(groupsQuery)
  const nfsStatus = useQuery({ ...nfsStatusQuery, enabled: modules.nfs })
  const createShare = useCreateShare()
  const updateShare = useUpdateShare()
  const preview = usePreviewAccess()

  const [tab, setTab] = useState<EditorTab>('general')
  const [preset, setPreset] = useState(share?.preset || 'custom')
  const [name, setName] = useState(share?.name ?? '')
  const [path, setPath] = useState(share?.path ?? '')
  const [createDirectory, setCreateDirectory] = useState(!isEdit)
  const deferredPath = useDeferredValue(path)
  const mountCheck = useQuery(mountCheckQuery(deferredPath))
  const mountInfo = (mountCheck.data?.checks ?? [])[0]
  const mountWarnings = (mountInfo?.warnings ?? []).filter((w) => !(w === 'path_missing' && !isEdit && createDirectory))
  const [comment, setComment] = useState(share?.comment ?? '')
  const [readOnly, setReadOnly] = useState(share?.readOnly ?? false)
  const [guestAccess, setGuestAccess] = useState(share?.guestAccess ?? false)
  const [hideUnreadable, setHideUnreadable] = useState(share?.hideUnreadable ?? false)
  const [timeMachine, setTimeMachine] = useState(share?.timeMachine ?? false)
  const [timeMachineMaxGiB, setTimeMachineMaxGiB] = useState(String(share?.timeMachineMaxGiB ?? 0))
  const [recycleEnabled, setRecycleEnabled] = useState((share?.recycleDays ?? 0) > 0)
  const [recycleDays, setRecycleDays] = useState(String(share?.recycleDays && share.recycleDays > 0 ? share.recycleDays : 30))
  const [access, setAccess] = useState<Record<number, AccessLevel>>(() => initialAccessMap(share))
  const [groupAccess, setGroupAccess] = useState<Record<number, AccessLevel>>(() => initialGroupAccessMap(share))
  const [networkRules, setNetworkRules] = useState<NetworkRule[]>(() => share?.networkRules ?? [])
  const [newRuleAction, setNewRuleAction] = useState<NetworkRule['action']>('allow')
  const [newRuleAddress, setNewRuleAddress] = useState('')
  const [nfsEnabled, setNfsEnabled] = useState(Boolean(share?.nfs?.enabled))
  const [nfsMode, setNfsMode] = useState<NonNullable<ShareNFS['mode']>>(share?.nfs?.mode ?? 'user')
  const [nfsAsUserId, setNfsAsUserId] = useState<string>(share?.nfs?.asUserId ? String(share.nfs.asUserId) : '')
  const [nfsClients, setNfsClients] = useState<NFSClient[]>(() => share?.nfs?.clients ?? [])
  const [newNfsClient, setNewNfsClient] = useState('')
  const [newNfsReadOnly, setNewNfsReadOnly] = useState(false)
  const [submitError, setSubmitError] = useState<unknown>(null)

  const accessList: ShareAccessInput[] = Object.entries(access).map(([userId, level]) => ({
    userId: Number(userId),
    level,
  }))
  const groupAccessList = Object.entries(groupAccess).map(([groupId, level]) => ({
    groupId: Number(groupId),
    level,
  }))

  const previewKey = JSON.stringify({ accessList, groupAccessList, guestAccess, readOnly, timeMachine, networkRules })
  useEffect(() => {
    if (tab !== 'preview') return
    preview.mutate({
      access: accessList,
      groupAccess: groupAccessList,
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

  function setGroupLevel(groupId: number, level: AccessLevel | 'none') {
    setGroupAccess((prev) => {
      const next = { ...prev }
      if (level === 'none') delete next[groupId]
      else next[groupId] = level
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

  function addNfsClient() {
    const addr = newNfsClient.trim()
    if (!addr) return
    setNfsClients((prev) => [...prev, { address: addr, readOnly: newNfsReadOnly }])
    setNewNfsClient('')
    setNewNfsReadOnly(false)
  }

  function removeNfsClient(index: number) {
    setNfsClients((prev) => prev.filter((_, i) => i !== index))
  }

  const errors = fieldErrors(submitError)
  const pending = createShare.isPending || updateShare.isPending

  function handleSubmit(e: FormEvent) {
    e.preventDefault()
    setSubmitError(null)
    const maxGiB = Math.max(0, Number.parseInt(timeMachineMaxGiB || '0', 10) || 0)
    const parsedRecycleDays = recycleEnabled ? Math.max(1, Math.min(365, Number.parseInt(recycleDays || '30', 10) || 30)) : 0
    const nfsPayload: ShareNFS | undefined = modules.nfs
      ? {
          enabled: nfsEnabled,
          mode: nfsMode,
          asUserId: nfsMode === 'user' && nfsAsUserId ? Number(nfsAsUserId) : undefined,
          clients: nfsClients,
        }
      : undefined

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
            recycleDays: parsedRecycleDays,
            access: accessList,
            groupAccess: groupAccessList,
            networkRules,
            nfs: nfsPayload,
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
        recycleDays: parsedRecycleDays,
        access: accessList,
        groupAccess: groupAccessList,
        networkRules,
        nfs: nfsPayload,
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
  const groupItems = groups.data?.items ?? []

  return (
    <form onSubmit={handleSubmit} className="flex flex-col gap-5">
      {submitError !== null && <Alert tone="danger" title={errorMessage(submitError)} />}

      <Tabs
        value={tab}
        onChange={setTab}
        items={[
          { id: 'general', label: 'General', icon: <Sparkles className="size-3.5" /> },
          { id: 'access', label: 'Access', icon: <Users className="size-3.5" />, badge: accessList.length + groupAccessList.length },
          { id: 'network', label: 'Network', icon: <Globe className="size-3.5" />, badge: networkRules.length },
          ...(modules.nfs
            ? [{ id: 'nfs' as const, label: 'NFS', icon: <Network className="size-3.5" />, badge: nfsEnabled ? nfsClients.length : undefined }]
            : []),
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

          {mountWarnings.length > 0 && (
            <Alert
              tone="warning"
              title={`LXC / Mount notice (${mountCheck.data?.environment ?? 'host'}${mountInfo?.mountPoint ? ` · ${mountInfo.mountPoint}` : ''})`}
            >
              <ul className="mt-1 list-disc pl-4 text-xs">
                {mountWarnings.map((w) => (
                  <li key={w}>
                    {w === 'read_only_mount'
                      ? 'This path is on a read-only mount — writes will fail.'
                      : w === 'on_root_disk'
                        ? 'This path is on the container root disk rather than a bind-mounted dataset.'
                        : w === 'uid_mapping_mismatch'
                          ? 'Folder owner is nobody (65534) — check unprivileged LXC UID mapping.'
                          : w === 'path_missing'
                            ? 'Folder does not exist yet on disk (enable "Create folder" or create it first).'
                            : w}
                  </li>
                ))}
              </ul>
            </Alert>
          )}

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

          <div className="flex flex-col gap-3 rounded-xl border border-border bg-bg px-4 py-3">
            <div className="flex items-center justify-between gap-3">
              <div className="flex items-center gap-2">
                <Recycle className="size-4 text-primary" />
                <span className="text-sm font-medium text-fg">Recycle bin</span>
                <InfoTip content="Moves deleted files (from SMB and the web File Manager) into a hidden .recycle folder and automatically purges them after the retention period." />
              </div>
              <Switch
                checked={recycleEnabled}
                disabled={timeMachine}
                onChange={setRecycleEnabled}
                aria-label="Enable recycle bin"
              />
            </div>

            {recycleEnabled && (
              <FormField
                label="Retention (days)"
                htmlFor="share-recycle-days"
                info="Deleted items older than this many days are purged automatically by the daily cleanup task (1–365 days)."
                error={errors.recycleDays}
              >
                <Input
                  id="share-recycle-days"
                  type="number"
                  min={1}
                  max={365}
                  value={recycleDays}
                  onChange={(e) => setRecycleDays(e.target.value)}
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

          {groupItems.length > 0 && (
            <div className="overflow-hidden rounded-xl border border-border">
              <div className="flex items-center justify-between border-b border-border bg-surface-2/60 px-4 py-2 text-xs font-medium text-fg-muted">
                <span className="inline-flex items-center gap-1.5">
                  <UsersRound className="size-3.5 text-primary" />
                  Groups
                </span>
                <span className="inline-flex items-center gap-1">
                  Level
                  <InfoTip content="Each member of a group inherits this level unless they have a direct user entry below. Multiple groups grant the highest level." />
                </span>
              </div>
              <div className="divide-y divide-border">
                {groupItems.map((g) => {
                  const current = groupAccess[g.id] ?? 'none'
                  return (
                    <div key={g.id} className="flex flex-wrap items-center justify-between gap-3 px-4 py-2.5">
                      <div className="flex min-w-0 items-center gap-2">
                        <span className="text-sm font-medium text-fg">{g.name}</span>
                        <Badge tone="neutral">
                          {g.members.length} {g.members.length === 1 ? 'member' : 'members'}
                        </Badge>
                      </div>
                      <div className="inline-flex rounded-lg border border-border bg-surface-2 p-0.5">
                        {(['none', 'read', 'upload', 'write'] as const).map((lvl) => {
                          const disabled = timeMachine && (lvl === 'read' || lvl === 'upload')
                          return (
                            <button
                              key={lvl}
                              type="button"
                              disabled={disabled}
                              onClick={() => setGroupLevel(g.id, lvl)}
                              className={cn(
                                'rounded-md px-2.5 py-1 text-xs font-medium capitalize transition-colors',
                                current === lvl
                                  ? 'bg-surface text-fg shadow-xs'
                                  : 'text-fg-muted hover:text-fg',
                                disabled && 'cursor-not-allowed opacity-40',
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
            </div>
          )}

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

      {tab === 'nfs' && modules.nfs && (
        <div className="flex flex-col gap-4">
          {nfsStatus.data && (!nfsStatus.data.supported || !nfsStatus.data.installed || !nfsStatus.data.running) && (
            <Alert
              tone="warning"
              title={
                nfsStatus.data.hint ||
                (!nfsStatus.data.supported
                  ? 'NFS server cannot run in this environment.'
                  : !nfsStatus.data.installed
                    ? 'nfs-kernel-server is not installed.'
                    : 'NFS server is currently stopped.')
              }
            />
          )}

          <div className="flex items-center justify-between gap-3 rounded-xl border border-border bg-bg px-4 py-3">
            <div className="flex items-center gap-2">
              <Network className="size-4 text-primary" />
              <span className="text-sm font-medium text-fg">Export over NFS</span>
              <InfoTip content="Exports this share over NFSv4 to explicit client IPs/CIDRs only. NFS exports are never open to every address." />
            </div>
            <Switch checked={nfsEnabled} onChange={setNfsEnabled} aria-label="Export over NFS" />
          </div>

          {nfsEnabled && (
            <>
              <div className="grid gap-4 sm:grid-cols-2">
                <FormField
                  label="Identity mapping"
                  htmlFor="nfs-mode"
                  info="Act as one user maps every NFS client request to a single NetShare Hub account (safest). Trust client UIDs honors client Linux UIDs directly (root still maps to nobody)."
                >
                  <Select
                    id="nfs-mode"
                    value={nfsMode}
                    onChange={(e) => setNfsMode(e.target.value as NonNullable<ShareNFS['mode']>)}
                  >
                    <option value="user">Act as one user (recommended)</option>
                    <option value="client-ids">Trust client UIDs</option>
                  </Select>
                </FormField>

                {nfsMode === 'user' && (
                  <FormField
                    label="Act as user"
                    htmlFor="nfs-as-user"
                    info="Must be a user with read or write access to this share."
                    error={errors['nfs.asUserId']}
                  >
                    <Select id="nfs-as-user" value={nfsAsUserId} onChange={(e) => setNfsAsUserId(e.target.value)}>
                      <option value="">Select user…</option>
                      {userItems
                        .filter((u) => access[u.id] === 'read' || access[u.id] === 'write')
                        .map((u) => (
                          <option key={u.id} value={String(u.id)}>
                            {u.username} ({access[u.id]})
                          </option>
                        ))}
                    </Select>
                  </FormField>
                )}
              </div>

              <div className="flex flex-col gap-2.5">
                <div className="flex items-center gap-1.5">
                  <Label>Allowed NFS clients</Label>
                  <InfoTip content="At least one IP address or CIDR range is required (e.g. 192.168.1.50 or 192.168.1.0/24). Wildcard /0 ranges are rejected." />
                </div>

                <div className="flex flex-wrap items-center gap-2">
                  <Input
                    className="min-w-44 flex-1"
                    value={newNfsClient}
                    onChange={(e) => setNewNfsClient(e.target.value)}
                    placeholder="192.168.1.50 or 192.168.1.0/24"
                    onKeyDown={(e) => {
                      if (e.key === 'Enter') {
                        e.preventDefault()
                        addNfsClient()
                      }
                    }}
                  />
                  <label className="inline-flex items-center gap-1.5 rounded-md border border-border bg-bg px-2.5 py-2 text-xs text-fg-muted">
                    <input
                      type="checkbox"
                      checked={newNfsReadOnly}
                      onChange={(e) => setNewNfsReadOnly(e.target.checked)}
                      className="accent-primary"
                    />
                    Read-only
                  </label>
                  <Button variant="secondary" onClick={addNfsClient}>
                    <Plus className="size-4" />
                    Add
                  </Button>
                </div>

                {nfsClients.length === 0 ? (
                  <div className="rounded-xl border border-dashed border-border p-5 text-center text-xs text-fg-muted">
                    Add at least one client IP or CIDR subnet to enable NFS export.
                  </div>
                ) : (
                  <div className="divide-y divide-border overflow-hidden rounded-xl border border-border">
                    {nfsClients.map((c, idx) => (
                      <div key={`${c.address}-${idx}`} className="flex items-center justify-between px-4 py-2.5">
                        <div className="flex items-center gap-2.5">
                          <code className="text-xs text-fg">{c.address}</code>
                          <Badge tone={c.readOnly ? 'warning' : 'info'}>{c.readOnly ? 'read-only' : 'rw'}</Badge>
                        </div>
                        <Button
                          variant="ghost"
                          size="icon"
                          className="size-7 text-fg-subtle hover:text-danger"
                          aria-label="Remove NFS client"
                          onClick={() => removeNfsClient(idx)}
                        >
                          <Trash2 className="size-3.5" />
                        </Button>
                      </div>
                    ))}
                  </div>
                )}
              </div>
            </>
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
                        {u.via && <Badge tone="neutral">via {u.via}</Badge>}
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
          {recycleEnabled && <Badge tone="success">Recycle {recycleDays || 30}d</Badge>}
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
