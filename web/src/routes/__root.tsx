import { createRootRouteWithContext, Link, Outlet } from '@tanstack/react-router'
import type { RouterContext } from '@/app/router'
import { Toaster } from '@/features/notifications'
import { EmptyState, ErrorState } from '@/shared/components'
import { Button } from '@/shared/ui'

// Route files stay thin: they pick a layout, guard access and hand over to a
// feature. No business logic and no styling beyond page framing lives here.
export const Route = createRootRouteWithContext<RouterContext>()({
  component: () => (
    <>
      <Outlet />
      <Toaster />
    </>
  ),
  errorComponent: ({ error, reset }) => (
    <div className="grid min-h-dvh place-items-center">
      <ErrorState error={error} onRetry={reset} />
    </div>
  ),
  notFoundComponent: () => (
    <div className="grid min-h-dvh place-items-center">
      <EmptyState
        title="Page not found"
        description="This page does not exist."
        action={
          <Link to="/">
            <Button variant="secondary" size="sm">
              Go to dashboard
            </Button>
          </Link>
        }
      />
    </div>
  ),
})
