import { useQuery } from '@tanstack/react-query'
import { createFileRoute, Navigate, Outlet, redirect } from '@tanstack/react-router'
import { layoutVariant, navigation } from '@/app/shell'
import { AccountMenu, sessionQuery } from '@/features/auth'
import { AppLayout } from '@/layouts'
import { SessionProvider } from '@/shared/session/session'

// Everything under routes/_app/ needs a login and is framed by AppLayout.
export const Route = createFileRoute('/_app')({
  beforeLoad: async ({ context: { queryClient } }) => {
    const session = await queryClient.ensureQueryData(sessionQuery)
    // /login sends first-time visitors on to /setup.
    if (!session) throw redirect({ to: '/login' })
  },
  component: AppRoute,
})

function AppRoute() {
  const { data: session } = useQuery(sessionQuery)

  // The session ended while the app was open (logout or expiry).
  if (!session) return <Navigate to="/login" />

  return (
    <SessionProvider user={session.user} permissions={session.permissions}>
      <AppLayout variant={layoutVariant} nav={navigation} navFooter={<AccountMenu />}>
        <Outlet />
      </AppLayout>
    </SessionProvider>
  )
}
