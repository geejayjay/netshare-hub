import { useQuery } from '@tanstack/react-query'
import { useState, type FormEvent } from 'react'
import { notify } from '@/features/notifications'
import { errorMessage, fieldErrors, isFieldError, type Schema } from '@/shared/api'
import { FormField } from '@/shared/components'
import { useSession } from '@/shared/session/session'
import { Alert, Button, Dialog, Input, Select, Switch } from '@/shared/ui'
import { rolesQuery, useUpdateUser, type User } from '../api'

interface EditUserDialogProps {
  user: User | null
  onClose: () => void
}

export function EditUserDialog({ user, onClose }: EditUserDialogProps) {
  return (
    <Dialog
      open={user !== null}
      onClose={onClose}
      title={user ? `Edit ${user.username}` : 'Edit user'}
      description="Update role, SMB network access, or password."
    >
      {user && <EditUserForm key={user.id} user={user} onClose={onClose} />}
    </Dialog>
  )
}

function EditUserForm({ user, onClose }: { user: User; onClose: () => void }) {
  const roles = useQuery(rolesQuery)
  const update = useUpdateUser()
  const session = useSession()
  const isSelf = user.id === session.user.id

  const [displayName, setDisplayName] = useState(user.displayName ?? '')
  const [role, setRole] = useState<string>(user.role)
  const [smbAccess, setSmbAccess] = useState(user.smbAccess)
  const [disabled, setDisabled] = useState(user.disabled)
  const [password, setPassword] = useState('')
  const [localError, setLocalError] = useState<string | null>(null)

  const server = fieldErrors(update.error)
  const enablingSmb = !user.smbAccess && smbAccess
  const disablingSmb = user.smbAccess && !smbAccess

  function submit(e: FormEvent) {
    e.preventDefault()
    setLocalError(null)

    if (enablingSmb && password.length < 8) {
      setLocalError('Enter a password (at least 8 characters) to enable SMB access.')
      return
    }
    if (password.length > 0 && password.length < 8) {
      setLocalError('Password must be at least 8 characters.')
      return
    }

    const body: Schema<'UpdateUserRequest'> = {}
    if (displayName !== (user.displayName ?? '')) body.displayName = displayName
    if (!isSelf && role !== user.role) body.role = role
    if (!isSelf && disabled !== user.disabled) body.disabled = disabled
    if (smbAccess !== user.smbAccess) body.smbAccess = smbAccess
    if (password.length > 0) body.password = password

    if (Object.keys(body).length === 0) {
      onClose()
      return
    }

    update.mutate(
      { id: user.id, body },
      {
        onSuccess: (updated) => {
          notify.success(
            'User updated',
            enablingSmb ? `${updated.username} now has SMB access.` : `${updated.username} saved.`,
          )
          onClose()
        },
      },
    )
  }

  return (
    <form className="flex flex-col gap-4" noValidate onSubmit={submit}>
      {localError && <Alert>{localError}</Alert>}
      {update.error && !isFieldError(update.error) && <Alert>{errorMessage(update.error)}</Alert>}

      <FormField label="Display name" htmlFor="edit-user-display" error={server.displayName}>
        <Input
          id="edit-user-display"
          value={displayName}
          onChange={(e) => setDisplayName(e.target.value)}
          placeholder="Optional"
        />
      </FormField>

      <FormField
        label="Role"
        htmlFor="edit-user-role"
        hint={isSelf ? 'You cannot change your own role.' : undefined}
        error={server.role}
      >
        <Select
          id="edit-user-role"
          value={role}
          disabled={isSelf}
          onChange={(e) => setRole(e.target.value)}
        >
          {(roles.data?.items ?? []).map((r) => (
            <option key={r.id} value={r.id}>
              {r.name}
            </option>
          ))}
        </Select>
      </FormField>

      <div className="flex items-start justify-between gap-4 rounded-lg border border-border bg-surface-2/60 p-3">
        <div className="flex flex-col gap-0.5">
          <label htmlFor="edit-user-smb" className="text-sm font-medium text-fg">
            SMB access
          </label>
          <span className="text-xs text-fg-muted">
            Allow this account to connect to network shares over SMB.
          </span>
        </div>
        <Switch id="edit-user-smb" checked={smbAccess} onChange={setSmbAccess} aria-label="SMB access" />
      </div>

      {disablingSmb && (
        <Alert tone="warning">
          Turning off SMB access removes {user.username} from every share.
        </Alert>
      )}

      <FormField
        label={enablingSmb ? 'Password (required for SMB)' : 'New password (optional)'}
        htmlFor="edit-user-password"
        hint={
          enablingSmb
            ? 'At least 8 characters. Sets both the web login and Samba password.'
            : 'Leave blank to keep the current password. At least 8 characters.'
        }
        error={server.password}
      >
        <Input
          id="edit-user-password"
          type="password"
          autoComplete="new-password"
          value={password}
          onChange={(e) => setPassword(e.target.value)}
          placeholder={enablingSmb ? 'Enter password to create Samba credentials' : 'Leave blank to keep current'}
        />
      </FormField>

      {!isSelf && (
        <div className="flex items-start justify-between gap-4 rounded-lg border border-border bg-surface-2/60 p-3">
          <div className="flex flex-col gap-0.5">
            <label htmlFor="edit-user-disabled" className="text-sm font-medium text-fg">
              Account disabled
            </label>
            <span className="text-xs text-fg-muted">Blocks web login and SMB access without deleting the account.</span>
          </div>
          <Switch
            id="edit-user-disabled"
            checked={disabled}
            onChange={setDisabled}
            aria-label="Account disabled"
          />
        </div>
      )}

      <div className="flex justify-end gap-2">
        <Button variant="secondary" onClick={onClose}>
          Cancel
        </Button>
        <Button type="submit" loading={update.isPending}>
          Save changes
        </Button>
      </div>
    </form>
  )
}
