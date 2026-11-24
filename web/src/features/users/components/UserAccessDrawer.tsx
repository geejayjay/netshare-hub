import { useQuery } from '@tanstack/react-query'
import { FolderOpen, ShieldCheck, UsersRound } from 'lucide-react'
import { groupsQuery, type Group } from '@/features/groups'
import { sharesQuery, type Share } from '@/features/shares'
import { Badge, Drawer, InfoTip, Spinner, type Tone } from '@/shared/ui'
import type { User } from '../api'

const LEVEL_RANK: Record<string, number> = { none: 0, upload: 1, read: 2, write: 3 }

function effectiveForUser(user: User, share: Share, userGroups: Group[]): { label: string; reason: string; tone: Tone } {
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

  // Group-inherited access: pick highest level across user's groups.
  const myGroupIds = new Set(userGroups.map((g) => g.id))
  let bestLevel: 'read' | 'upload' | 'write' | null = null
  let bestGroup = ''
  for (const ga of share.groupAccess ?? []) {
    if (!myGroupIds.has(ga.groupId)) continue
    if (!bestLevel || (LEVEL_RANK[ga.level] ?? 0) > (LEVEL_RANK[bestLevel] ?? 0)) {
      bestLevel = ga.level
      bestGroup = ga.groupName
    }
  }
  if (bestLevel) {
    if (share.readOnly && bestLevel !== 'read') {
      return { label: 'Read only', reason: `Via group "${bestGroup}", capped by read-only share`, tone: 'success' }
    }
    if (bestLevel === 'write') {
      return { label: 'Read + Write', reason: `Via group "${bestGroup}"`, tone: 'info' }
    }
    if (bestLevel === 'upload') {
      return { label: 'Drop box', reason: `Via group "${bestGroup}" (upload-only)`, tone: 'warning' }
    }
    return { label: 'Read only', reason: `Via group "${bestGroup}"`, tone: 'success' }
  }

  if (share.guestAccess) {
    return { label: 'Read only', reason: 'Guest access is enabled on share', tone: 'success' }
  }
  return { label: 'No access', reason: 'Not listed directly or via any group', tone: 'neutral' }
}

export function UserAccessDrawer({ user, onClose }: { user: User | null; onClose: () => void }) {
  const shares = useQuery({
    ...sharesQuery({ pageSize: 100 }),
    enabled: Boolean(user),
  })
  const groups = useQuery({
    ...groupsQuery,
    enabled: Boolean(user),
  })

  const items = shares.data?.items ?? []
  const userGroups = (groups.data?.items ?? []).filter((g) => g.members.some((m) => m.userId === user?.id))

  return (
    <Drawer
      open={Boolean(user)}
      onClose={onClose}
      title={user ? `Where can ${user.username} go?` : 'User access'}
      description={user?.displayName || 'Effective share permissions, group memberships, and LXC UID mapping info'}
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

          {/* Group memberships */}
          <div className="flex flex-col gap-2">
            <div className="flex items-center gap-1.5 text-xs font-semibold text-fg-muted">
              <UsersRound className="size-3.5 text-primary" />
              Groups ({userGroups.length})
            </div>
            {userGroups.length === 0 ? (
              <div className="text-xs text-fg-subtle">Not a member of any group.</div>
            ) : (
              <div className="flex flex-wrap gap-1.5">
                {userGroups.map((g) => (
                  <Badge key={g.id} tone="info">
                    {g.name}
                  </Badge>
                ))}
              </div>
            )}
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

            {shares.isLoading || groups.isLoading ? (
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
                  const eff = effectiveForUser(user, sh, userGroups)
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
