import { useQuery } from '@tanstack/react-query'
import { FolderOpen, ShieldCheck } from 'lucide-react'
import { sharesQuery, type Share } from '@/features/shares'
import { Badge, Drawer, InfoTip, Spinner, type Tone } from '@/shared/ui'
import type { User } from '../api'

function effectiveForUser(user: User, share: Share): { label: string; reason: string; tone: Tone } {
  if (user.disabled) {
    return { label: 'Blocked', reason: 'Account is disabled', tone: 'danger' }
  }
  if (!user.smbAccess) {
    return { label: 'No access', reason: 'Web-only account (SMB access off)', tone: 'neutral' }
  }
  const direct = share.access.find((a) => a.userId === user.id)
  if (direct) {
    if (share.readOnly && direct.level !== 'read') {
      return { label: 'Read only', reason: `Direct (${direct.level}), capped by read-only share`, tone: 'success' }
    }
    if (direct.level === 'write') {
      return { label: 'Read + Write', reason: 'Assigned directly on share', tone: 'info' }
    }
    if (direct.level === 'upload') {
      return { label: 'Drop box', reason: 'Upload-only (cannot list files)', tone: 'warning' }
    }
    return { label: 'Read only', reason: 'Assigned directly on share', tone: 'success' }
  }
  if (share.guestAccess) {
    return { label: 'Read only', reason: 'Guest access is enabled on share', tone: 'success' }
  }
  return { label: 'No access', reason: 'Not listed in share access', tone: 'neutral' }
}

export function UserAccessDrawer({ user, onClose }: { user: User | null; onClose: () => void }) {
  const shares = useQuery({
    ...sharesQuery({ pageSize: 100 }),
    enabled: Boolean(user),
  })

  const items = shares.data?.items ?? []

  return (
    <Drawer
      open={Boolean(user)}
      onClose={onClose}
      title={user ? `Where can ${user.username} go?` : 'User access'}
      description={user?.displayName || 'Effective share permissions and LXC UID mapping info'}
    >
      {user && (
        <div className="flex flex-col gap-5">
          {/* Account summary & LXC UID hint */}
          <div className="flex flex-wrap items-center gap-2 rounded-lg border border-border bg-surface-2/40 p-3 text-xs">
            <Badge tone={user.role === 'admin' ? 'info' : 'neutral'}>{user.role}</Badge>
            <Badge tone={user.smbAccess ? 'success' : 'neutral'}>{user.smbAccess ? 'SMB enabled' : 'Web only'}</Badge>
            <Badge tone={user.disabled ? 'warning' : 'success'} dot>
              {user.disabled ? 'Disabled' : 'Active'}
            </Badge>
            <span className="ml-auto inline-flex items-center gap-1 text-[0.7rem] text-fg-subtle">
              LXC UID mapping
              <InfoTip content="Managed Linux accounts start at UID 1000 inside the container. On a Proxmox unprivileged LXC host, container UID N maps to host UID 100000 + N (e.g. 1000 → 101000)." />
            </span>
          </div>

          {/* Share access matrix for this user */}
          <div className="flex flex-col gap-2">
            <div className="flex items-center justify-between text-xs font-semibold text-fg-muted">
              <span className="inline-flex items-center gap-1.5">
                <ShieldCheck className="size-3.5 text-primary" />
                Effective Share Access
              </span>
              <span className="font-normal text-fg-subtle">{items.length} shares</span>
            </div>

            {shares.isLoading ? (
              <div className="flex justify-center py-8">
                <Spinner />
              </div>
            ) : items.length === 0 ? (
              <div className="rounded-lg border border-border p-4 text-center text-xs text-fg-muted">
                No shares configured yet.
              </div>
            ) : (
              <div className="divide-y divide-border overflow-hidden rounded-lg border border-border">
                {items.map((sh) => {
                  const eff = effectiveForUser(user, sh)
                  return (
                    <div key={sh.id} className="flex items-start justify-between gap-3 px-3.5 py-2.5">
                      <div className="min-w-0">
                        <div className="flex items-center gap-1.5 text-sm font-medium text-fg">
                          <FolderOpen className="size-3.5 shrink-0 text-fg-muted" />
                          <span className="truncate">{sh.name}</span>
                        </div>
                        <div className="mt-0.5 text-[0.7rem] text-fg-subtle">{eff.reason}</div>
                      </div>
                      <Badge tone={eff.tone} className="shrink-0">
                        {eff.label}
                      </Badge>
                    </div>
                  )
                })}
              </div>
            )}
          </div>
        </div>
      )}
    </Drawer>
  )
}
