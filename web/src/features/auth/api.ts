import { queryOptions, useMutation, useQueryClient, type QueryClient } from '@tanstack/react-query'
import { api, isApiError, setCsrfToken, unwrap, type Schema } from '@/shared/api'

export type SessionInfo = Schema<'SessionInfo'>

/** The current login, or null when signed out. */
export const sessionQuery = queryOptions({
  queryKey: ['session'],
  queryFn: async (): Promise<SessionInfo | null> => {
    try {
      const session = await unwrap(api.GET('/auth/me'))
      setCsrfToken(session.csrfToken)
      return session
    } catch (error) {
      if (isApiError(error) && error.status === 401) return null
      throw error
    }
  },
  staleTime: 60_000,
})

export const setupStatusQuery = queryOptions({
  queryKey: ['setup-status'],
  queryFn: () => unwrap(api.GET('/setup')),
  staleTime: 60_000,
})

function startSession(queryClient: QueryClient, session: SessionInfo) {
  setCsrfToken(session.csrfToken)
  queryClient.setQueryData(sessionQuery.queryKey, session)
  queryClient.setQueryData(setupStatusQuery.queryKey, { needsSetup: false })
}

/** Marks the user as signed out everywhere (after logout or an expired session). */
export function endSession(queryClient: QueryClient) {
  setCsrfToken('')
  queryClient.setQueryData(sessionQuery.queryKey, null)
}

export function useLogin() {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: (body: Schema<'LoginRequest'>) => unwrap(api.POST('/auth/login', { body })),
    onSuccess: (session) => startSession(queryClient, session),
  })
}

export function useCompleteSetup() {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: (body: Schema<'SetupRequest'>) => unwrap(api.POST('/setup', { body })),
    onSuccess: (session) => startSession(queryClient, session),
  })
}

export function useLogout() {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: () => api.POST('/auth/logout'),
    onSuccess: () => {
      endSession(queryClient)
      // Drop everything the signed-in user loaded.
      queryClient.removeQueries({ predicate: (q) => q.queryKey[0] !== sessionQuery.queryKey[0] })
    },
  })
}
