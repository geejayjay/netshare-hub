import { QueryCache, QueryClient } from '@tanstack/react-query'
import { endSession } from '@/features/auth'
import { isApiError } from '@/shared/api'

/** Retry only what can succeed on a second try: network failures and 5xx. Never 4xx. */
function shouldRetry(failureCount: number, error: unknown) {
  if (failureCount >= 2) return false
  return !isApiError(error) || error.status === 0 || error.status >= 500
}

export const queryClient: QueryClient = new QueryClient({
  queryCache: new QueryCache({
    // The session expired (or was ended elsewhere): sign out in the UI, which
    // sends the user to the login page.
    onError: (error) => {
      if (isApiError(error) && error.code === 'UNAUTHENTICATED') endSession(queryClient)
    },
  }),
  defaultOptions: {
    queries: { retry: shouldRetry, staleTime: 30_000, refetchOnWindowFocus: false },
    mutations: { retry: false }, // a write is never repeated automatically
  },
})
