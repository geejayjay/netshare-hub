import { queryOptions, useMutation, useQueryClient } from '@tanstack/react-query'
import { api, unwrap, type Schema } from '@/shared/api'

export type SearchHit = Schema<'SearchHit'>
export type SearchResponse = Schema<'SearchResponse'>
export type SearchStatus = Schema<'SearchStatus'>

export function searchFilesQuery(params: { q: string; kind?: 'all' | 'file' | 'dir'; shareId?: number; limit?: number }) {
  return queryOptions({
    queryKey: ['search', 'files', params],
    queryFn: () =>
      unwrap(
        api.GET('/search', {
          params: {
            query: {
              q: params.q,
              kind: params.kind && params.kind !== 'all' ? params.kind : undefined,
              shareId: params.shareId || undefined,
              limit: params.limit ?? 30,
            },
          },
        }),
      ),
    enabled: params.q.trim().length > 0,
    staleTime: 10_000,
  })
}

export const searchStatusQuery = queryOptions({
  queryKey: ['search', 'status'],
  queryFn: () => unwrap(api.GET('/search/status')),
  staleTime: 15_000,
})

export function useReindexSearch() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: () => unwrap(api.POST('/search/reindex')),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ['search'] })
    },
  })
}
