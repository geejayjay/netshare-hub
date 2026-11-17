import type { QueryClient } from '@tanstack/react-query'
import { createRouter } from '@tanstack/react-router'
import { routeTree } from '@/routeTree.gen'
import { queryClient } from './query-client'

/** What every route can read in beforeLoad/loader. */
export interface RouterContext {
  queryClient: QueryClient
}

export const router = createRouter({
  routeTree,
  context: { queryClient },
  defaultPreload: 'intent',
  // TanStack Query owns caching, so the router should always ask it.
  defaultPreloadStaleTime: 0,
})

declare module '@tanstack/react-router' {
  interface Register {
    router: typeof router
  }
}
