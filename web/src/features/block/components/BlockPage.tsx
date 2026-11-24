import { useQuery } from '@tanstack/react-query'
import { Check, Copy, Database, HardDrive, Lock, Pencil, Plus, Trash2 } from 'lucide-react'
import { useState, type FormEvent } from 'react'
import { notify } from '@/features/notifications'
import { errorMessage } from '@/shared/api'
import { EmptyState, ErrorState, FormField, PageHeader } from '@/shared/components'
import { formatBytes } from '@/shared/lib/format'
import {
  Alert,
  Badge,
  Button,
  Card,
  Dialog,
  Input,
  Select,
  Spinner,
  StatCard,
  Switch,
} from '@/shared/ui'
import {
  blockDevicesQuery,
  blockDisksQuery,
  blockStatusQuery,
  useCreateBlockDisk,
  useDeleteBlockDisk,
  useUpdateBlockDisk,
  type BlockDisk,
} from '../api'

function DiskEditorBody({ disk, onClose }: { disk?: BlockDisk | null; onClose: () => void }) {
  const isEdit = Boolean(disk)
  const devices = useQuery({ ...blockDevicesQuery, enabled: !isEdit })
  const createDisk = useCreateBlockDisk()
  const updateDisk = useUpdateBlockDisk()

  const [name, setName] = useState(disk?.name ?? '')
  const [protocol, setProtocol] = useState<BlockDisk['protocol']>(disk?.protocol ?? 'iscsi')
  const [backing, setBacking] = useState<'image' | 'device'>(disk?.device ? 'device' : 'image')
  const [imageFolder, setImageFolder] = useState(disk?.imageFolder ?? '/srv/block')
  const [sizeGiB, setSizeGiB] = useState(String(disk?.sizeGiB || 50))
  const [device, setDevice] = useState(disk?.device ?? '')
  const [confirmDevice, setConfirmDevice] = useState('')
  const [client, setClient] = useState(disk?.client ?? '')
  const [listen, setListen] = useState(disk?.listen ?? '')
  const [port, setPort] = useState(String(disk?.port || (protocol === 'iscsi' ? 3260 : 4420)))
  const [readOnly, setReadOnly] = useState(Boolean(disk?.readOnly))
  const [chapUser, setChapUser] = useState(disk?.chapUser ?? '')
  const [chapSecret, setChapSecret] = useState('')
  const [confirmSingleClient, setConfirmSingleClient] = useState(isEdit)
  const [err, setErr] = useState<unknown>(null)

  const pending = createDisk.isPending || updateDisk.isPending

  function handleSubmit(e: FormEvent) {
    e.preventDefault()
    setErr(null)

    if (isEdit && disk) {
      updateDisk.mutate(
        {
          id: disk.id,
          body: {
            name: name.trim(),
            client: client.trim(),
            listen: listen.trim() || undefined,
            port: Number.parseInt(port, 10) || undefined,
            readOnly: disk.protocol === 'iscsi' ? readOnly : undefined,
            sizeGiB: disk.imageFolder ? Number.parseInt(sizeGiB, 10) || undefined : undefined,
            chapUser: disk.protocol === 'iscsi' ? chapUser.trim() : undefined,
            chapSecret: disk.protocol === 'iscsi' && chapSecret ? chapSecret : undefined,
          },
        },
        {
          onSuccess: () => {
            notify.success('Block disk updated', name.trim())
            onClose()
          },
          onError: (e2) => setErr(e2),
        },
      )
      return
    }

    createDisk.mutate(
      {
        name: name.trim(),
        protocol,
        imageFolder: backing === 'image' ? imageFolder.trim() : undefined,
        sizeGiB: backing === 'image' ? Number.parseInt(sizeGiB, 10) || 10 : undefined,
        device: backing === 'device' ? device : undefined,
        confirmDevice: backing === 'device' ? confirmDevice.trim() : undefined,
        client: client.trim(),
        listen: listen.trim() || undefined,
        port: Number.parseInt(port, 10) || undefined,
        readOnly: protocol === 'iscsi' ? readOnly : undefined,
        chapUser: protocol === 'iscsi' && chapUser.trim() ? chapUser.trim() : undefined,
        chapSecret: protocol === 'iscsi' && chapSecret ? chapSecret : undefined,
        confirmSingleClient,
      },
      {
        onSuccess: (created) => {
          notify.success('Block disk created', created.target)
          onClose()
        },
        onError: (e2) => setErr(e2),
      },
    )
  }

  return (
    <form onSubmit={handleSubmit} className="flex flex-col gap-4">
      {err !== null && <Alert tone="danger" title={errorMessage(err)} />}

      <div className="grid gap-4 sm:grid-cols-2">
        <FormField label="Disk name" htmlFor="blk-name">
          <Input id="blk-name" value={name} onChange={(e) => setName(e.target.value)} placeholder="vm-data" required />
        </FormField>

        <FormField
          label="Protocol"
          htmlFor="blk-proto"
          info="iSCSI works on Windows, macOS, and Linux and supports CHAP authentication. NVMe/TCP is faster for Linux hosts on trusted networks."
        >
          <Select
            id="blk-proto"
            value={protocol}
            disabled={isEdit}
            onChange={(e) => {
              const p = e.target.value as BlockDisk['protocol']
              setProtocol(p)
              setPort(p === 'iscsi' ? '3260' : '4420')
            }}
          >
            <option value="iscsi">iSCSI</option>
            <option value="nvmeof">NVMe / TCP</option>
          </Select>
        </FormField>
      </div>

      {!isEdit && (
        <FormField label="Backing storage" htmlFor="blk-backing">
          <Select
            id="blk-backing"
            value={backing}
            onChange={(e) => setBacking(e.target.value as 'image' | 'device')}
          >
            <option value="image">Sparse image file (recommended)</option>
            <option value="device">Dedicated whole physical disk</option>
          </Select>
        </FormField>
      )}

      {backing === 'image' ? (
        <div className="grid gap-4 sm:grid-cols-2">
          <FormField
            label="Image folder"
            htmlFor="blk-folder"
            info="Folder on the server to hold the disk image file (must not be inside a share)."
          >
            <Input
              id="blk-folder"
              value={imageFolder}
              disabled={isEdit}
              onChange={(e) => setImageFolder(e.target.value)}
              placeholder="/srv/block"
              required
            />
          </FormField>

          <FormField
            label="Size (GiB)"
            htmlFor="blk-size"
            info={isEdit ? 'Grow only — shrinking is blocked to prevent data truncation.' : 'Virtual disk size in GiB.'}
          >
            <Input
              id="blk-size"
              type="number"
              min={isEdit && disk ? disk.sizeGiB : 1}
              max={65536}
              value={sizeGiB}
              onChange={(e) => setSizeGiB(e.target.value)}
              required
            />
          </FormField>
        </div>
      ) : (
        !isEdit && (
          <div className="flex flex-col gap-3">
            <FormField label="Physical disk" htmlFor="blk-device">
              <Select id="blk-device" value={device} onChange={(e) => setDevice(e.target.value)} required>
                <option value="">Select unused disk…</option>
                {(devices.data?.items ?? []).map((d) => (
                  <option key={d.path} value={d.path} disabled={Boolean(d.inUse)}>
                    {d.model || d.kernel} — {formatBytes(d.sizeBytes)} ({d.path}){d.inUse ? ` [${d.inUse}]` : ''}
                  </option>
                ))}
              </Select>
            </FormField>
            {device && (
              <FormField
                label="Confirm device path"
                htmlFor="blk-confirm-dev"
                info="Retype the exact /dev/disk/by-id path to confirm handing this entire disk to the client."
              >
                <Input
                  id="blk-confirm-dev"
                  value={confirmDevice}
                  onChange={(e) => setConfirmDevice(e.target.value)}
                  placeholder={device}
                  className="font-mono text-xs"
                  required
                />
              </FormField>
            )}
          </div>
        )
      )}

      <FormField
        label={protocol === 'iscsi' ? 'Allowed client IQN' : 'Allowed client NQN'}
        htmlFor="blk-client"
        info="Only this single initiator may connect. Find it in Windows iSCSI Initiator or /etc/iscsi/initiatorname.iscsi."
      >
        <Input
          id="blk-client"
          value={client}
          onChange={(e) => setClient(e.target.value)}
          placeholder={protocol === 'iscsi' ? 'iqn.1991-05.com.microsoft:workstation' : 'nqn.2014-08.org.nvmexpress:uuid:...'}
          className="font-mono text-xs"
          required
        />
      </FormField>

      <div className="grid gap-4 sm:grid-cols-2">
        <FormField label="Listen address" htmlFor="blk-listen" info="Leave blank for 0.0.0.0 (all interfaces).">
          <Input id="blk-listen" value={listen} onChange={(e) => setListen(e.target.value)} placeholder="0.0.0.0" />
        </FormField>
        <FormField label="Port" htmlFor="blk-port">
          <Input id="blk-port" type="number" min={1} max={65535} value={port} onChange={(e) => setPort(e.target.value)} />
        </FormField>
      </div>

      {protocol === 'iscsi' && (
        <>
          <div className="grid gap-4 sm:grid-cols-2">
            <FormField label="CHAP username (optional)" htmlFor="blk-chap-user">
              <Input id="blk-chap-user" value={chapUser} onChange={(e) => setChapUser(e.target.value)} />
            </FormField>
            <FormField
              label="CHAP secret (12–16 chars)"
              htmlFor="blk-chap-secret"
              info="Windows iSCSI requires CHAP secrets to be 12–16 characters."
            >
              <Input
                id="blk-chap-secret"
                type="password"
                maxLength={16}
                placeholder={isEdit && disk?.chap ? 'Leave blank to keep existing' : ''}
                value={chapSecret}
                onChange={(e) => setChapSecret(e.target.value)}
              />
            </FormField>
          </div>

          <div className="flex items-center justify-between gap-3 rounded-lg border border-border bg-bg px-3.5 py-2.5">
            <span className="text-sm font-medium text-fg">Read-only target</span>
            <Switch checked={readOnly} onChange={setReadOnly} aria-label="Read-only target" />
          </div>
        </>
      )}

      {!isEdit && (
        <label className="flex items-start gap-2.5 rounded-lg border border-warning/30 bg-warning-soft/50 p-3 text-xs text-fg">
          <input
            type="checkbox"
            checked={confirmSingleClient}
            onChange={(e) => setConfirmSingleClient(e.target.checked)}
            className="mt-0.5 accent-primary"
            required
          />
          <span>I understand that only ONE client may connect to a block disk at a time (connecting two corrupts data).</span>
        </label>
      )}

      <div className="flex justify-end gap-2 border-t border-border pt-4">
        <Button variant="secondary" onClick={onClose}>
          Cancel
        </Button>
        <Button type="submit" loading={pending} disabled={!isEdit && !confirmSingleClient}>
          {isEdit ? 'Save changes' : 'Create disk'}
        </Button>
      </div>
    </form>
  )
}

export function BlockPage() {
  const status = useQuery(blockStatusQuery)
  const disks = useQuery(blockDisksQuery)
  const deleteDisk = useDeleteBlockDisk()
  const [editorOpen, setEditorOpen] = useState(false)
  const [editing, setEditing] = useState<BlockDisk | null>(null)
  const [copiedId, setCopiedId] = useState<number | null>(null)

  const items = disks.data?.items ?? []
  const totalGiB = items.reduce((s, d) => s + (d.sizeGiB ?? 0), 0)
  const connectedCount = items.filter((d) => d.connected).length

  return (
    <div className="flex flex-col gap-5">
      <PageHeader
        title="Block Storage"
        info="Expose raw virtual disks or physical drives over iSCSI or NVMe/TCP for VMs, Proxmox, or dedicated single-host mounts."
        actions={
          <Button
            size="sm"
            onClick={() => {
              setEditing(null)
              setEditorOpen(true)
            }}
          >
            <Plus className="size-4" />
            New disk
          </Button>
        }
      />

      {status.data?.hint && <Alert tone="warning" title={status.data.hint} />}

      <div className="grid gap-4 sm:grid-cols-3">
        <StatCard label="Block disks" value={items.length} icon={<Database />} tone="info" />
        <StatCard label="Provisioned" value={`${totalGiB} GiB`} icon={<HardDrive />} tone="success" />
        <StatCard label="Connected" value={connectedCount} icon={<Lock />} tone="warning" />
      </div>

      <Card>
        <div className="flex items-center justify-between border-b border-border px-5 py-3">
          <span className="text-sm font-semibold">Targets</span>
          {status.data && (
            <div className="flex items-center gap-1.5">
              <Badge tone={status.data.iscsi ? 'success' : 'neutral'} dot>
                iSCSI
              </Badge>
              <Badge tone={status.data.nvmeof ? 'success' : 'neutral'} dot>
                NVMe/TCP
              </Badge>
            </div>
          )}
        </div>

        {disks.isLoading ? (
          <div className="flex justify-center py-12">
            <Spinner />
          </div>
        ) : disks.error ? (
          <div className="p-5">
            <ErrorState error={disks.error} onRetry={() => void disks.refetch()} />
          </div>
        ) : items.length === 0 ? (
          <div className="p-8">
            <EmptyState title="No block disks configured" description="Create an iSCSI or NVMe/TCP disk to attach storage to a single client." />
          </div>
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full border-collapse text-sm">
              <thead className="border-b border-border bg-surface-2/50 text-left text-[0.6875rem] font-semibold tracking-wider text-fg-subtle uppercase">
                <tr>
                  <th className="px-4 py-2.5">Disk</th>
                  <th className="px-4 py-2.5">Target</th>
                  <th className="px-4 py-2.5">Client</th>
                  <th className="px-4 py-2.5">Size</th>
                  <th className="px-4 py-2.5">Status</th>
                  <th className="px-4 py-2.5 text-right">Actions</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-border">
                {items.map((d) => (
                  <tr key={d.id} className="hover:bg-surface-2/40">
                    <td className="px-4 py-3">
                      <div className="flex items-center gap-1.5">
                        <span className="font-medium text-fg">{d.name}</span>
                        <Badge tone="info" className="uppercase">
                          {d.protocol}
                        </Badge>
                        {d.chap && <Badge tone="success">CHAP</Badge>}
                        {d.readOnly && <Badge tone="warning">RO</Badge>}
                      </div>
                      <code className="text-[0.6875rem] text-fg-subtle">{d.device || d.imageFolder}</code>
                    </td>
                    <td className="px-4 py-3">
                      <div className="flex items-center gap-1.5">
                        <code className="max-w-56 truncate text-xs text-fg" title={d.target}>
                          {d.target}
                        </code>
                        <Button
                          variant="ghost"
                          size="icon"
                          className="size-6 shrink-0"
                          title="Copy target IQN/NQN"
                          onClick={() => {
                            void navigator.clipboard?.writeText(d.target).then(() => {
                              setCopiedId(d.id)
                              window.setTimeout(() => setCopiedId(null), 1500)
                            })
                          }}
                        >
                          {copiedId === d.id ? <Check className="size-3 text-success" /> : <Copy className="size-3" />}
                        </Button>
                      </div>
                      <div className="text-[0.6875rem] text-fg-subtle tabular-nums">
                        {d.listen || '0.0.0.0'}:{d.port}
                      </div>
                    </td>
                    <td className="max-w-44 truncate px-4 py-3 font-mono text-xs text-fg-muted" title={d.client}>
                      {d.client}
                    </td>
                    <td className="px-4 py-3 text-xs text-fg-muted tabular-nums">{d.sizeGiB} GiB</td>
                    <td className="px-4 py-3">
                      <Badge tone={d.connected ? 'success' : 'neutral'} dot>
                        {d.connected ? 'Connected' : 'Idle'}
                      </Badge>
                    </td>
                    <td className="px-4 py-3">
                      <div className="flex items-center justify-end gap-1">
                        <Button
                          variant="ghost"
                          size="icon"
                          className="size-8"
                          title="Edit disk"
                          onClick={() => {
                            setEditing(d)
                            setEditorOpen(true)
                          }}
                        >
                          <Pencil className="size-3.5" />
                        </Button>
                        <Button
                          variant="ghost"
                          size="icon"
                          className="size-8 text-fg-muted hover:text-danger"
                          title="Delete disk"
                          onClick={() => {
                            if (!window.confirm(`Remove block disk "${d.name}"?`)) return
                            deleteDisk.mutate(d.id, {
                              onSuccess: () => notify.success('Block disk removed'),
                              onError: (err) => notify.error(err),
                            })
                          }}
                        >
                          <Trash2 className="size-3.5" />
                        </Button>
                      </div>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </Card>

      <Dialog
        open={editorOpen}
        onClose={() => setEditorOpen(false)}
        size="lg"
        title={editing ? `Edit disk — ${editing.name}` : 'New block disk'}
      >
        <DiskEditorBody key={editing ? `edit-${editing.id}-${editing.updatedAt}` : 'create'} disk={editing} onClose={() => setEditorOpen(false)} />
      </Dialog>
    </div>
  )
}
