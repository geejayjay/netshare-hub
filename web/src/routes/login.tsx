import { createFileRoute, redirect, useNavigate } from '@tanstack/react-router'
import { LoginForm, sessionQuery, setupStatusQuery } from '@/features/auth'
import { AuthLayout } from '@/layouts'

export const Route = createFileRoute('/login')({
  beforeLoad: async ({ context: { queryClient } }) => {
    const { needsSetup } = await queryClient.ensureQueryData(setupStatusQuery)
    if (needsSetup) throw redirect({ to: '/setup' })
    if (await queryClient.ensureQueryData(sessionQuery)) throw redirect({ to: '/' })
  },
  component: LoginRoute,
})

function LoginRoute() {
  const navigate = useNavigate()
  return (
    <AuthLayout title="Log in" description="Use your NetShare Hub account.">
      <LoginForm onSuccess={() => void navigate({ to: '/' })} />
    </AuthLayout>
  )
}
