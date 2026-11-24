import { useQuery } from '@tanstack/react-query'
import { createFileRoute, Navigate, Outlet, redirect } from '@tanstack/react-router'
import { layoutVariant, navigation } from '@/app/shell'
import { AlertBell, CriticalAlertBanner } from '@/features/alerts'
import { AccountMenu, sessionQuery } from '@/features/auth'
import { UploadTray } from '@/features/files'
import { JobsButton } from '@/features/jobs'
import { GlobalSearchButton } from '@/features/search'
import { AppLayout } from '@/layouts'
import { OfflineBanner, ThemeSwitcher } from '@/shared/components'
import { ModulesProvider, modulesQuery } from '@/shared/modules/modules'
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
  const { data: modules } = useQuery({ ...modulesQuery, enabled: Boolean(session) })

  // The session ended while the app was open (logout or expiry).
  if (!session) return <Navigate to="/login" />

  return (
    <SessionProvider user={session.user} permissions={session.permissions}>
      <ModulesProvider modules={modules}>
        <AppLayout
          variant={layoutVariant}
          nav={navigation}
          navFooter={<AccountMenu />}
          headerActions={
            <>
              <GlobalSearchButton />
              <JobsButton />
              <AlertBell />
              <ThemeSwitcher className="ml-1 max-sm:hidden" />
            </>
          }
          banner={
            <>
              <OfflineBanner />
              <CriticalAlertBanner />
            </>
          }
        >
          <Outlet />
        </AppLayout>
        <UploadTray />
      </ModulesProvider>
    </SessionProvider>
  )
}
