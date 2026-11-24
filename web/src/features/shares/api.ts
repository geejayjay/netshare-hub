import { keepPreviousData, queryOptions, useMutation, useQueryClient } from '@tanstack/react-query'
import { api, unwrap, type ListParams, type Schema } from '@/shared/api'

export type Share = Schema<'Share'>
export type ShareAccessInput = Schema<'ShareAccessInput'>
export type NetworkRule = Schema<'NetworkRule'>
export type SharePreset = Schema<'Preset'>
export type EffectiveAccess = Schema<'EffectiveAccess'>
export type PreviewAccessRequest = Schema<'PreviewAccessRequest'>
export type ShareNFS = Schema<'NFS'>
export type NFSClient = Schema<'NFSClient'>
export type NFSStatus = Schema<'NFSStatus'>

const keys = {
  all: ['shares'] as const,
  list: (params: ListParams) => ['shares', 'list', params] as const,
  users: ['shares', 'users'] as const,
  presets: ['shares', 'presets'] as const,
  nfsStatus: ['shares', 'nfs-status'] as const,
}

export const nfsStatusQuery = queryOptions({
  queryKey: keys.nfsStatus,
  queryFn: () => unwrap(api.GET('/shares/nfs/status')),
})

export const sharesQuery = (params: ListParams) =>
  queryOptions({
    queryKey: keys.list(params),
    queryFn: () => unwrap(api.GET('/shares', { params: { query: params } })),
    placeholderData: keepPreviousData,
  })

export const shareUsersQuery = queryOptions({
  queryKey: keys.users,
  queryFn: () => unwrap(api.GET('/users', { params: { query: { pageSize: 100 } } })),
})

export const sharePresetsQuery = queryOptions({
  queryKey: keys.presets,
  queryFn: () => unwrap(api.GET('/shares/presets')),
  staleTime: 5 * 60_000,
})

export function useCreateShare() {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: (body: Schema<'CreateShareRequest'>) => unwrap(api.POST('/shares', { body })),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: keys.all }),
  })
}

export function useUpdateShare() {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: ({ id, body }: { id: number; body: Schema<'UpdateShareRequest'> }) =>
      unwrap(api.PATCH('/shares/{id}', { params: { path: { id } }, body })),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: keys.all }),
  })
}

export function useDeleteShare() {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: (id: number) => unwrap(api.DELETE('/shares/{id}', { params: { path: { id } } })),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: keys.all }),
  })
}

export function useFixSharePermissions() {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: (id: number) => unwrap(api.POST('/shares/{id}/fix-permissions', { params: { path: { id } } })),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['jobs'] }),
  })
}

export function usePreviewAccess() {
  return useMutation({
    mutationFn: (body: PreviewAccessRequest) => unwrap(api.POST('/shares/effective-access', { body })),
  })
}

export function mountCheckQuery(path: string) {
  const trimmed = path.trim()
  return queryOptions({
    queryKey: ['system', 'mounts', trimmed],
    queryFn: () => unwrap(api.GET('/system/mounts', { params: { query: { path: [trimmed] } } })),
    enabled: trimmed.startsWith('/') && trimmed.length >= 2,
    staleTime: 15_000,
    retry: false,
  })
}
