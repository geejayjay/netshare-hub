import { useQuery } from '@tanstack/react-query'
import { Check, Pencil, Plus, Shield, Trash2, Users, UsersRound, X } from 'lucide-react'
import { useMemo, useState, type FormEvent } from 'react'
import { notify } from '@/features/notifications'
import { usersQuery } from '@/features/users'
import { errorMessage, fieldErrors } from '@/shared/api'
import { EmptyState, ErrorState, FormField, PageHeader, PermissionGate, SearchInput } from '@/shared/components'
import { cn } from '@/shared/lib/cn'
import { formatDateTime } from '@/shared/lib/format'
import { useSession } from '@/shared/session/session'
import { Alert, Badge, Button, Card, Dialog, Input, Spinner, StatCard } from '@/shared/ui'
import { groupsQuery, useCreateGroup, useDeleteGroup, useUpdateGroup, type Group } from '../api'

function GroupEditorDialog({
  open,
  editing,
  onClose,
}: {
  open: boolean
  editing: Group | null
  onClose: () => void
}) {
  const isEdit = Boolean(editing)
  const users = useQuery(usersQuery({ pageSize: 200 }))
  const createGroup = useCreateGroup()
  const updateGroup = useUpdateGroup()

  const [name, setName] = useState(editing?.name ?? '')
  const [description, setDescription] = useState(editing?.description ?? '')
  const [memberIds, setMemberIds] = useState<number[]>(() => (editing?.members ?? []).map((m) => m.userId))
  const [memberFilter, setMemberFilter] = useState('')
  const [submitError, setSubmitError] = useState<unknown>(null)

  const allUsers = useMemo(() => users.data?.items ?? [], [users.data])
  const selectedSet = useMemo(() => new Set(memberIds), [memberIds])

  const filteredUsers = useMemo(() => {
    const q = memberFilter.trim().toLowerCase()
    return allUsers.filter((u) => {
      if (!q) return true
      return u.username.toLowerCase().includes(q) || (u.displayName ?? '').toLowerCase().includes(q)
    })
  }, [allUsers, memberFilter])

  const selectedUsers = useMemo(() => allUsers.filter((u) => selectedSet.has(u.id)), [allUsers, selectedSet])

  const errors = fieldErrors(submitError)
  const pending = createGroup.isPending || updateGroup.isPending

  function toggleMember(userId: number) {
    setMemberIds((prev) => (prev.includes(userId) ? prev.filter((id) => id !== userId) : [...prev, userId]))
  }

  function handleSubmit(e: FormEvent) {
    e.preventDefault()
    setSubmitError(null)
    if (editing) {
      updateGroup.mutate(
        {
          id: editing.id,
          body: { description: description.trim(), members: memberIds },
        },
        {
          onSuccess: () => {
            notify.success('Group updated', editing.name)
            onClose()
          },
          onError: (err) => setSubmitError(err),
        },
      )
      return
    }
    createGroup.mutate(
      {
        name: name.trim(),
        description: description.trim(),
        members: memberIds,
      },
      {
        onSuccess: (created) => {
          notify.success('Group created', created.name)
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
      title={isEdit ? `Edit group: ${editing?.name}` : 'Create group'}
      description="Groups map directly to managed Linux groups and can be granted share access."
    >
      <form onSubmit={handleSubmit} className="flex flex-col gap-4">
        {submitError !== null && <Alert tone="danger" title={errorMessage(submitError)} />}

        <FormField
          label="Group name"
          htmlFor="grp-name"
          info="Lowercase letters, digits, dashes, or underscores. Also used as the Linux group name."
          error={errors.name}
        >
          <Input
            id="grp-name"
            value={name}
            disabled={isEdit}
            placeholder="family"
            onChange={(e) => setName(e.target.value)}
            required
          />
        </FormField>

        <FormField label="Description" htmlFor="grp-desc" error={errors.description}>
          <Input
            id="grp-desc"
            value={description}
            placeholder="Shared access for household members"
            onChange={(e) => setDescription(e.target.value)}
          />
        </FormField>

        {/* Selected member chips */}
        <div className="flex flex-col gap-2">
          <div className="flex items-center justify-between text-xs font-medium text-fg">
            <span>Members ({memberIds.length})</span>
            {memberIds.length > 0 && (
              <button
                type="button"
                onClick={() => setMemberIds([])}
                className="text-[0.7rem] text-fg-subtle hover:text-fg"
              >
                Clear all
              </button>
            )}
          </div>

          {selectedUsers.length > 0 && (
            <div className="flex flex-wrap gap-1.5">
              {selectedUsers.map((u) => (
                <span
                  key={u.id}
                  className="inline-flex items-center gap-1 rounded-full border border-primary/30 bg-primary-soft/60 px-2.5 py-0.5 text-xs font-medium text-fg"
                >
                  {u.username}
                  <button
                    type="button"
                    onClick={() => toggleMember(u.id)}
                    aria-label={`Remove ${u.username}`}
                    className="rounded-full p-0.5 text-fg-muted hover:bg-surface hover:text-fg"
                  >
                    <X className="size-3" />
                  </button>
                </span>
              ))}
            </div>
          )}

          {/* Member picker */}
          <div className="overflow-hidden rounded-lg border border-border bg-bg">
            <div className="border-b border-border p-2">
              <SearchInput value={memberFilter} onChange={setMemberFilter} placeholder="Filter SMB users…" />
            </div>
            <div className="max-h-48 divide-y divide-border overflow-y-auto">
              {users.isLoading ? (
                <div className="flex justify-center py-6">
                  <Spinner />
                </div>
              ) : filteredUsers.length === 0 ? (
                <div className="py-5 text-center text-xs text-fg-muted">No matching users.</div>
              ) : (
                filteredUsers.map((u) => {
                  const checked = selectedSet.has(u.id)
                  const eligible = u.smbAccess && !u.disabled
                  return (
                    <button
                      key={u.id}
                      type="button"
                      disabled={!eligible && !checked}
                      onClick={() => toggleMember(u.id)}
                      className={cn(
                        'flex w-full items-center justify-between gap-2 px-3 py-2 text-left text-xs transition-colors',
                        checked ? 'bg-primary-soft/35' : 'hover:bg-surface-2/60',
                        !eligible && !checked && 'cursor-not-allowed opacity-50',
                      )}
                    >
                      <div className="min-w-0">
                        <div className="font-medium text-fg">{u.username}</div>
                        <div className="text-[0.68rem] text-fg-subtle">
                          {u.disabled ? 'Disabled account' : !u.smbAccess ? 'Web-only (enable SMB access first)' : u.displayName || 'SMB account'}
                        </div>
                      </div>
                      <span
                        className={cn(
                          'grid size-5 shrink-0 place-items-center rounded border',
                          checked ? 'border-primary bg-primary text-primary-fg' : 'border-border-strong bg-surface',
                        )}
                      >
                        {checked && <Check className="size-3" />}
                      </span>
                    </button>
                  )
                })
              )}
            </div>
          </div>
          {errors.members && <p className="text-xs text-danger">{errors.members}</p>}
        </div>

        <div className="flex justify-end gap-2 border-t border-border pt-4">
          <Button variant="ghost" type="button" onClick={onClose}>
            Cancel
          </Button>
          <Button type="submit" loading={pending}>
            {isEdit ? 'Save group' : 'Create group'}
          </Button>
        </div>
      </form>
    </Dialog>
  )
}

export function GroupsPage() {
  const { can } = useSession()
  const canWrite = can('groups:write')
  const query = useQuery(groupsQuery)
  const remove = useDeleteGroup()

  const [filter, setFilter] = useState('')
  const [dialogOpen, setDialogOpen] = useState(false)
  const [editing, setEditing] = useState<Group | null>(null)

  const allGroups = useMemo(() => query.data?.items ?? [], [query.data])
  const filtered = useMemo(() => {
    const q = filter.trim().toLowerCase()
    if (!q) return allGroups
    return allGroups.filter(
      (g) =>
        g.name.toLowerCase().includes(q) ||
        g.description.toLowerCase().includes(q) ||
        g.members.some((m) => m.username.toLowerCase().includes(q)),
    )
  }, [allGroups, filter])

  const totalMemberships = allGroups.reduce((acc, g) => acc + g.members.length, 0)
  const activeGroups = allGroups.filter((g) => g.members.length > 0).length

  function openCreate() {
    setEditing(null)
    setDialogOpen(true)
  }

  function openEdit(g: Group) {
    setEditing(g)
    setDialogOpen(true)
  }

  return (
    <div className="flex flex-col gap-5">
      <PageHeader
        title="Groups"
        info="Organize SMB users into real Linux groups and assign share permissions by group. Membership changes immediately re-apply share ACLs and restart active file workers."
        actions={
          <PermissionGate permission="groups:write">
            <Button onClick={openCreate}>
              <Plus className="size-4" />
              New group
            </Button>
          </PermissionGate>
        }
      />

      <div className="grid gap-4 sm:grid-cols-3">
        <StatCard label="Total groups" value={allGroups.length} icon={<UsersRound />} tone="info" />
        <StatCard label="With members" value={activeGroups} icon={<Users />} tone="success" />
        <StatCard label="Memberships" value={totalMemberships} icon={<Shield />} tone="warning" />
      </div>

      <Card>
        <div className="flex flex-wrap items-center justify-between gap-3 border-b border-border px-4 py-3">
          <SearchInput value={filter} onChange={setFilter} placeholder="Search groups or members…" />
          <span className="text-xs text-fg-subtle tabular-nums">{filtered.length} groups</span>
        </div>

        {query.isLoading ? (
          <div className="flex justify-center py-12">
            <Spinner />
          </div>
        ) : query.error ? (
          <div className="p-4">
            <ErrorState error={query.error} onRetry={() => void query.refetch()} />
          </div>
        ) : filtered.length === 0 ? (
          <div className="p-8">
            <EmptyState
              title={filter ? 'No matching groups' : 'No groups yet'}
              description={
                filter
                  ? 'Try a different search term.'
                  : 'Create a group to grant share access to multiple users at once.'
              }
              action={
                !filter && canWrite ? (
                  <Button size="sm" onClick={openCreate}>
                    <Plus className="size-3.5" />
                    New group
                  </Button>
                ) : undefined
              }
            />
          </div>
        ) : (
          <div className="divide-y divide-border">
            {filtered.map((g) => (
              <div key={g.id} className="flex flex-wrap items-start justify-between gap-3 px-5 py-4">
                <div className="min-w-0 flex-1">
                  <div className="flex flex-wrap items-center gap-2">
                    <span className="font-semibold text-fg">{g.name}</span>
                    <Badge tone="neutral">
                      {g.members.length} {g.members.length === 1 ? 'member' : 'members'}
                    </Badge>
                    <span className="text-[0.7rem] text-fg-subtle">Updated {formatDateTime(g.updatedAt)}</span>
                  </div>
                  {g.description && <p className="mt-0.5 text-xs text-fg-muted">{g.description}</p>}
                  <div className="mt-2 flex flex-wrap gap-1.5">
                    {g.members.length === 0 ? (
                      <span className="text-xs text-fg-subtle">No members assigned</span>
                    ) : (
                      g.members.map((m) => (
                        <Badge key={m.userId} tone="info">
                          {m.username}
                        </Badge>
                      ))
                    )}
                  </div>
                </div>

                {canWrite && (
                  <div className="flex shrink-0 items-center gap-1">
                    <Button size="sm" variant="secondary" onClick={() => openEdit(g)}>
                      <Pencil className="size-3.5" />
                      Edit
                    </Button>
                    <Button
                      size="icon"
                      variant="ghost"
                      className="size-8 text-fg-muted hover:text-danger"
                      aria-label={`Delete ${g.name}`}
                      onClick={() => {
                        if (!window.confirm(`Delete group "${g.name}"? Its members will lose any share access granted through it.`)) {
                          return
                        }
                        remove.mutate(g.id, {
                          onSuccess: () => notify.success('Group deleted', g.name),
                          onError: (err) => notify.error(err),
                        })
                      }}
                    >
                      <Trash2 className="size-4" />
                    </Button>
                  </div>
                )}
              </div>
            ))}
          </div>
        )}
      </Card>

      {dialogOpen && (
        <GroupEditorDialog
          key={editing ? `edit-${editing.id}` : 'new'}
          open={dialogOpen}
          editing={editing}
          onClose={() => setDialogOpen(false)}
        />
      )}
    </div>
  )
}
