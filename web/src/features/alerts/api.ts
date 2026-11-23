import { queryOptions, useMutation, useQueryClient } from '@tanstack/react-query'
import { api, unwrap, type Schema } from '@/shared/api'

export type ServerAlert = Schema<'Alert'>

export const alertsQuery = (active?: boolean) =>
  queryOptions({
    queryKey: ['alerts', { active }],
    queryFn: () => unwrap(api.GET('/alerts', { params: { query: active !== undefined ? { active } : {} } })),
  })

export function useAcknowledgeAlert() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (id: number) => unwrap(api.POST('/alerts/{id}/acknowledge', { params: { path: { id } } })),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['alerts'] }),
  })
}

export function useDeleteAlert() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (id: number) => unwrap(api.DELETE('/alerts/{id}', { params: { path: { id } } })),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['alerts'] }),
  })
}
