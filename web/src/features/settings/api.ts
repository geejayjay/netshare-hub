import { queryOptions, useMutation, useQueryClient } from '@tanstack/react-query'
import { api, unwrap, type Schema } from '@/shared/api'

export type SambaSettings = Schema<'ServerSettings'>
export type DiscoveryStatus = Schema<'DiscoveryStatus'>
export type DiscoveryService = Schema<'DiscoveryService'>
export type DiscoveryUpdate = Schema<'DiscoveryUpdate'>
export type TaskStatus = Schema<'Status'>

export const sambaSettingsQuery = queryOptions({
  queryKey: ['settings', 'samba'],
  queryFn: () => unwrap(api.GET('/settings/samba')),
})

export const discoveryQuery = queryOptions({
  queryKey: ['settings', 'discovery'],
  queryFn: () => unwrap(api.GET('/settings/discovery')),
})

export const systemTasksQuery = queryOptions({
  queryKey: ['system', 'tasks'],
  queryFn: () => unwrap(api.GET('/system/tasks')),
})

export function useUpdateSambaSettings() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (body: SambaSettings) => unwrap(api.PUT('/settings/samba', { body })),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['settings', 'samba'] }),
  })
}

export function useUpdateDiscovery() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (body: DiscoveryUpdate) => unwrap(api.PUT('/settings/discovery', { body })),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['settings', 'discovery'] }),
  })
}
