import { queryOptions, useMutation, useQueryClient } from '@tanstack/react-query'
import { api, unwrap, type Schema } from '@/shared/api'

export type SmbSession = Schema<'Session'>

export const sessionsQuery = queryOptions({
  queryKey: ['sessions'],
  queryFn: () => unwrap(api.GET('/system/sessions')),
  refetchInterval: 10_000,
})

export function useDisconnectSession() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (id: string) => unwrap(api.POST('/system/sessions/{id}/disconnect', { params: { path: { id } } })),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['sessions'] }),
  })
}
