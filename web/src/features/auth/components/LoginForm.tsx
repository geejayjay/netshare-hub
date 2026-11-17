import { useForm } from '@tanstack/react-form'
import { z } from 'zod'
import { errorMessage, fieldErrors, isFieldError } from '@/shared/api'
import { TextField } from '@/shared/components'
import { Alert, Button } from '@/shared/ui'
import { useLogin } from '../api'

const schema = z.object({
  username: z.string().min(1, 'Enter your username.'),
  password: z.string().min(1, 'Enter your password.'),
})

export function LoginForm({ onSuccess }: { onSuccess: () => void }) {
  const login = useLogin()
  const server = fieldErrors(login.error)

  const form = useForm({
    defaultValues: { username: '', password: '' },
    validators: { onSubmit: schema },
    onSubmit: ({ value }) => login.mutateAsync(value).then(onSuccess, () => {}),
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
      {login.error && !isFieldError(login.error) && <Alert>{errorMessage(login.error)}</Alert>}

      <form.Field name="username">
        {(field) => (
          <TextField field={field} label="Username" serverError={server.username} autoComplete="username" autoFocus />
        )}
      </form.Field>
      <form.Field name="password">
        {(field) => (
          <TextField
            field={field}
            label="Password"
            serverError={server.password}
            type="password"
            autoComplete="current-password"
          />
        )}
      </form.Field>

      <Button type="submit" loading={login.isPending}>
        Log in
      </Button>
    </form>
  )
}
