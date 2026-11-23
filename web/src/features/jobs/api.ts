import { queryOptions, useMutation, useQueryClient } from '@tanstack/react-query'
import { api, unwrap, type Schema } from '@/shared/api'

export type Job = Schema<'Job'>

export const jobsQuery = queryOptions({
  queryKey: ['jobs'],
  queryFn: () => unwrap(api.GET('/jobs', { params: { query: { pageSize: 50 } } })),
})

export function useCancelJob() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (id: string) => unwrap(api.POST('/jobs/{id}/cancel', { params: { path: { id } } })),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['jobs'] }),
  })
}
