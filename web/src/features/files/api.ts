import { infiniteQueryOptions, queryOptions, useMutation, useQueryClient } from '@tanstack/react-query'
import { API_BASE, api, unwrap, type Schema } from '@/shared/api'

export type FileEntry = Schema<'Entry'>
export type FileItem = Schema<'Item'>
export type TransferRequest = Schema<'TransferRequest'>

export function fileEntriesInfiniteQuery(share: string, path: string) {
  return infiniteQueryOptions({
    queryKey: ['files', 'entries', share, path],
    initialPageParam: '' as string,
    queryFn: ({ pageParam }) =>
      unwrap(
        api.GET('/files/{share}/entries', {
          params: {
            path: { share },
            query: {
              path: path || undefined,
              after: pageParam || undefined,
              limit: 500,
            },
          },
        }),
      ),
    getNextPageParam: (lastPage) => (lastPage.next ? lastPage.next : undefined),
    enabled: Boolean(share),
  })
}

export function fileDownloadUrl(share: string, path: string, inline = false) {
  const qs = new URLSearchParams({ path })
  if (inline) qs.set('inline', 'true')
  return `${API_BASE}/files/${encodeURIComponent(share)}/download?${qs.toString()}`
}

export function useCreateFolder() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: ({ share, path }: { share: string; path: string }) =>
      unwrap(api.POST('/files/{share}/folders', { params: { path: { share } }, body: { path } })),
    onSuccess: (_data, vars) => qc.invalidateQueries({ queryKey: ['files', 'entries', vars.share] }),
  })
}

export function useRenameFile() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: ({ share, path, name }: { share: string; path: string; name: string }) =>
      unwrap(api.POST('/files/{share}/rename', { params: { path: { share } }, body: { path, name } })),
    onSuccess: (_data, vars) => qc.invalidateQueries({ queryKey: ['files', 'entries', vars.share] }),
  })
}

export function useDeleteFiles() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (items: FileItem[]) => unwrap(api.POST('/files/delete', { body: { items } })),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ['files', 'entries'] })
      void qc.invalidateQueries({ queryKey: ['jobs'] })
    },
  })
}

export function useCopyFiles() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (body: TransferRequest) => unwrap(api.POST('/files/copy', { body })),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ['files', 'entries'] })
      void qc.invalidateQueries({ queryKey: ['jobs'] })
    },
  })
}

export function useMoveFiles() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (body: TransferRequest) => unwrap(api.POST('/files/move', { body })),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ['files', 'entries'] })
      void qc.invalidateQueries({ queryKey: ['jobs'] })
    },
  })
}

export function usePrepareZip() {
  return useMutation({
    mutationFn: (items: FileItem[]) => unwrap(api.POST('/files/zip', { body: { items } })),
  })
}

export const thumbnailCapabilitiesQuery = queryOptions({
  queryKey: ['files', 'thumbnails', 'capabilities'],
  queryFn: () => unwrap(api.GET('/files/thumbnails/capabilities')),
  staleTime: 5 * 60_000,
})

export function thumbnailUrl(share: string, path: string, size = 320): string {
  const params = new URLSearchParams({ share, path, size: String(size) })
  return `${API_BASE}/files/thumbnail?${params.toString()}`
}

export function recycleEntriesQuery(share: string, path: string) {
  return queryOptions({
    queryKey: ['files', 'recycle', share, path],
    queryFn: () =>
      unwrap(
        api.GET('/files/{share}/recycle', {
          params: {
            path: { share },
            query: { path: path || undefined, limit: 200 },
          },
        }),
      ),
    enabled: Boolean(share),
    retry: false,
  })
}

export function useRestoreFromRecycle() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: ({ share, path }: { share: string; path: string }) =>
      unwrap(api.POST('/files/{share}/recycle/restore', { params: { path: { share } }, body: { path } })),
    onSuccess: (_entry, vars) => {
      void qc.invalidateQueries({ queryKey: ['files', 'recycle', vars.share] })
      void qc.invalidateQueries({ queryKey: ['files', 'entries', vars.share] })
    },
  })
}

export function useEmptyRecycle() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (share: string) => unwrap(api.POST('/files/{share}/recycle/empty', { params: { path: { share } } })),
    onSuccess: (_res, share) => {
      void qc.invalidateQueries({ queryKey: ['files', 'recycle', share] })
    },
  })
}
