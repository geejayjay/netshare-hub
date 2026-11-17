import { useForm } from '@tanstack/react-form'
import { z } from 'zod'
import { errorMessage, fieldErrors, isFieldError } from '@/shared/api'
import { TextField } from '@/shared/components'
import { Alert, Button } from '@/shared/ui'
import { useCompleteSetup } from '../api'

const schema = z
  .object({
    username: z.string().min(2, 'Must be at least 2 characters.'),
    displayName: z.string(),
    password: z.string().min(8, 'Must be at least 8 characters.'),
    confirm: z.string(),
  })
  .refine((v) => v.password === v.confirm, { path: ['confirm'], message: 'Passwords do not match.' })

export function SetupForm({ onSuccess }: { onSuccess: () => void }) {
  const setup = useCompleteSetup()
  const server = fieldErrors(setup.error)

  const form = useForm({
    defaultValues: { username: '', displayName: '', password: '', confirm: '' },
    validators: { onSubmit: schema },
    onSubmit: ({ value: { confirm: _confirm, ...body } }) => setup.mutateAsync(body).then(onSuccess, () => {}),
  })

  return (
    <form
      className="flex flex-col gap-4"
      noValidate
      onSubmit={(e) => {
        e.preventDefault()
        void form.handleSubmit()
      }}
    >
      {setup.error && !isFieldError(setup.error) && <Alert>{errorMessage(setup.error)}</Alert>}

      <form.Field name="username">
        {(field) => (
          <TextField
            field={field}
            label="Admin username"
            hint="Lowercase letters, digits, - or _."
            serverError={server.username}
            autoComplete="username"
            autoFocus
          />
        )}
      </form.Field>
      <form.Field name="displayName">
        {(field) => <TextField field={field} label="Display name (optional)" serverError={server.displayName} />}
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
      <form.Field name="confirm">
        {(field) => <TextField field={field} label="Confirm password" type="password" autoComplete="new-password" />}
      </form.Field>

      <Button type="submit" loading={setup.isPending}>
        Create admin account
      </Button>
    </form>
  )
}
