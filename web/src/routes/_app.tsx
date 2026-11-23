import { useQuery } from '@tanstack/react-query'
import { createFileRoute, Navigate, Outlet, redirect } from '@tanstack/react-router'
import { layoutVariant, navigation } from '@/app/shell'
import { AlertBell, CriticalAlertBanner } from '@/features/alerts'
import { AccountMenu, sessionQuery } from '@/features/auth'
import { JobsButton } from '@/features/jobs'
import { AppLayout } from '@/layouts'
import { ThemeSwitcher } from '@/shared/components'
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
      <AppLayout
        variant={layoutVariant}
        nav={navigation}
        navFooter={<AccountMenu />}
        headerActions={
          <>
            <JobsButton />
            <AlertBell />
            <ThemeSwitcher className="ml-1 max-sm:hidden" />
          </>
        }
        banner={<CriticalAlertBanner />}
      >
        <Outlet />
      </AppLayout>
    </SessionProvider>
  )
}
