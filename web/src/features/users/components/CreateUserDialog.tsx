import { useForm } from '@tanstack/react-form'
import { useQuery } from '@tanstack/react-query'
import { z } from 'zod'
import { notify } from '@/features/notifications'
import { errorMessage, fieldErrors, isFieldError } from '@/shared/api'
import { SelectField, TextField } from '@/shared/components'
import { Alert, Button, Dialog, Switch } from '@/shared/ui'
import { rolesQuery, useCreateUser } from '../api'

const schema = z.object({
  username: z.string().min(2, 'Must be at least 2 characters.'),
  displayName: z.string(),
  role: z.string().min(1, 'Pick a role.'),
  password: z.string().min(8, 'Must be at least 8 characters.'),
  smbAccess: z.boolean(),
})

export function CreateUserDialog({ open, onClose }: { open: boolean; onClose: () => void }) {
  const roles = useQuery(rolesQuery)
  const create = useCreateUser()
  const server = fieldErrors(create.error)

  const form = useForm({
    defaultValues: { username: '', displayName: '', role: 'user', password: '', smbAccess: true },
    validators: { onSubmit: schema },
    onSubmit: ({ value }) =>
      create.mutateAsync(value).then(
        (user) => {
          notify.success('User created', `${user.username} can now log in.`)
          close()
        },
        () => {}, // the error is shown in the form
      ),
  })

  function close() {
    form.reset()
    create.reset()
    onClose()
  }

  return (
    <Dialog open={open} onClose={close} title="New user" description="The user can log in with this password.">
      <form
        className="flex flex-col gap-4"
        noValidate
        onSubmit={(e) => {
          e.preventDefault()
          void form.handleSubmit()
        }}
      >
        {create.error && !isFieldError(create.error) && <Alert>{errorMessage(create.error)}</Alert>}

        <form.Field name="username">
          {(field) => (
            <TextField
              field={field}
              label="Username"
              hint="Lowercase letters, digits, - or _."
              serverError={server.username}
              autoComplete="off"
            />
          )}
        </form.Field>
        <form.Field name="displayName">
          {(field) => <TextField field={field} label="Display name (optional)" serverError={server.displayName} />}
        </form.Field>
        <form.Field name="role">
          {(field) => (
            <SelectField
              field={field}
              label="Role"
              serverError={server.role}
              options={(roles.data?.items ?? []).map((r) => ({ value: r.id, label: r.name }))}
            />
          )}
        </form.Field>
        <form.Field name="password">
          {(field) => (
            <TextField
              field={field}
              label="Password"
              hint="At least 8 characters."
              serverError={server.password}
              type="password"
              autoComplete="new-password"
            />
          )}
        </form.Field>
        <form.Field name="smbAccess">
          {(field) => (
            <div className="flex items-start justify-between gap-4 rounded-lg border border-border bg-surface-2/60 p-3">
              <div className="flex flex-col gap-0.5">
                <label htmlFor="create-user-smb" className="text-sm font-medium text-fg">
                  SMB access
                </label>
                <span className="text-xs text-fg-muted">
                  Create Samba credentials so this user can be granted share access.
                </span>
              </div>
              <Switch
                id="create-user-smb"
                checked={field.state.value}
                onChange={field.handleChange}
                aria-label="SMB access"
              />
            </div>
          )}
        </form.Field>

        <div className="flex justify-end gap-2">
          <Button variant="secondary" onClick={close}>
            Cancel
          </Button>
          <Button type="submit" loading={create.isPending}>
            Create user
          </Button>
        </div>
      </form>
    </Dialog>
  )
}
