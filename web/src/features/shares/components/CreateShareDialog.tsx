import { useForm } from '@tanstack/react-form'
import { useQuery } from '@tanstack/react-query'
import { useState } from 'react'
import { z } from 'zod'
import { notify } from '@/features/notifications'
import { errorMessage, fieldErrors, isFieldError } from '@/shared/api'
import { TextField } from '@/shared/components'
import { Alert, Button, Dialog, Select } from '@/shared/ui'
import { shareUsersQuery, useCreateShare, type ShareAccessInput } from '../api'

const schema = z.object({
  name: z.string().min(1, 'Enter a share name.').max(32, 'Must be 32 characters or fewer.'),
  path: z.string().min(2, 'Enter a folder path on the server.'),
  comment: z.string().max(100, 'Must be 100 characters or fewer.'),
  createDirectory: z.boolean(),
})

type AccessChoice = 'none' | 'read' | 'write'

export function CreateShareDialog({ open, onClose }: { open: boolean; onClose: () => void }) {
  const users = useQuery({ ...shareUsersQuery, enabled: open })
  const create = useCreateShare()
  const server = fieldErrors(create.error)
  const [accessMap, setAccessMap] = useState<Record<number, AccessChoice>>({})

  const form = useForm({
    defaultValues: { name: '', path: '', comment: '', createDirectory: true },
    validators: { onSubmit: schema },
    onSubmit: ({ value }) => {
      const access: ShareAccessInput[] = Object.entries(accessMap)
        .filter((entry): entry is [string, 'read' | 'write'] => entry[1] === 'read' || entry[1] === 'write')
        .map(([userId, level]) => ({ userId: Number(userId), level }))

      return create
        .mutateAsync({
          name: value.name,
          path: value.path,
          comment: value.comment || undefined,
          createDirectory: value.createDirectory,
          access,
        })
        .then(
          (share) => {
            notify.success('Share created', `\\\\server\\${share.name} is now active.`)
            close()
          },
          () => {},
        )
    },
  })

  function close() {
    form.reset()
    create.reset()
    setAccessMap({})
    onClose()
  }

  const smbUsers = (users.data?.items ?? []).filter((u) => !u.disabled && u.smbAccess)

  return (
    <Dialog open={open} onClose={close} title="New share" description="Expose a folder on the server over SMB.">
      <form
        className="flex flex-col gap-4"
        noValidate
        onSubmit={(e) => {
          e.preventDefault()
          void form.handleSubmit()
        }}
      >
        {create.error && !isFieldError(create.error) && <Alert>{errorMessage(create.error)}</Alert>}

        <form.Field name="name">
          {(field) => (
            <TextField
              field={field}
              label="Share name"
              hint="Letters, digits, - or _. Clients connect to \\server\name."
              serverError={server.name}
              autoComplete="off"
              placeholder="media"
            />
          )}
        </form.Field>

        <form.Field name="path">
          {(field) => (
            <TextField
              field={field}
              label="Folder path"
              hint="Absolute path on the server."
              serverError={server.path}
              autoComplete="off"
              placeholder="/srv/shares/media"
            />
          )}
        </form.Field>

        <form.Field name="comment">
          {(field) => (
            <TextField field={field} label="Description (optional)" serverError={server.comment} />
          )}
        </form.Field>

        <form.Field name="createDirectory">
          {(field) => (
            <label className="flex items-center gap-2 text-sm text-fg">
              <input
                type="checkbox"
                className="size-4 rounded border-border accent-accent"
                checked={field.state.value}
                onChange={(e) => field.handleChange(e.target.checked)}
              />
              Create folder if it does not exist
            </label>
          )}
        </form.Field>

        <div className="flex flex-col gap-2">
          <span className="text-sm font-medium text-fg">User access</span>
          {smbUsers.length === 0 ? (
            <p className="text-sm text-fg-muted">No active users with SMB access yet.</p>
          ) : (
            <div className="flex max-h-48 flex-col gap-2 overflow-y-auto rounded-md border border-border p-2">
              {smbUsers.map((u) => (
                <div key={u.id} className="flex items-center justify-between gap-2 text-sm">
                  <span className="truncate font-medium">{u.username}</span>
                  <Select
                    aria-label={`Access for ${u.username}`}
                    className="w-36"
                    value={accessMap[u.id] ?? 'none'}
                    onChange={(e) =>
                      setAccessMap((prev) => ({
                        ...prev,
                        [u.id]: e.target.value as AccessChoice,
                      }))
                    }
                  >
                    <option value="none">No access</option>
                    <option value="read">Read only</option>
                    <option value="write">Read &amp; write</option>
                  </Select>
                </div>
              ))}
            </div>
          )}
        </div>

        <div className="flex justify-end gap-2">
          <Button variant="secondary" onClick={close}>
            Cancel
          </Button>
          <Button type="submit" loading={create.isPending}>
            Create share
          </Button>
        </div>
      </form>
    </Dialog>
  )
}
