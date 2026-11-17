import { createFileRoute, redirect, useNavigate } from '@tanstack/react-router'
import { SetupForm, setupStatusQuery } from '@/features/auth'
import { AuthLayout } from '@/layouts'

export const Route = createFileRoute('/setup')({
  beforeLoad: async ({ context: { queryClient } }) => {
    const { needsSetup } = await queryClient.ensureQueryData(setupStatusQuery)
    if (!needsSetup) throw redirect({ to: '/login' })
  },
  component: SetupRoute,
})

function SetupRoute() {
  const navigate = useNavigate()
  return (
    <AuthLayout title="Welcome to NetShare Hub" description="Create the first admin account to get started.">
      <SetupForm onSuccess={() => void navigate({ to: '/' })} />
    </AuthLayout>
  )
}
