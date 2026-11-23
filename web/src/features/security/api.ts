import { queryOptions, useMutation, useQueryClient } from '@tanstack/react-query'
import { api, unwrap, type Schema } from '@/shared/api'

export type GuardSettings = Schema<'Settings'>
export type GuardIncident = Schema<'Incident'>
export type BanSettings = Schema<'BanSettings'>
export type Ban = Schema<'Ban'>

export const guardSettingsQuery = queryOptions({
  queryKey: ['guard', 'settings'],
  queryFn: () => unwrap(api.GET('/guard/settings')),
})

export const guardIncidentsQuery = queryOptions({
  queryKey: ['guard', 'incidents'],
  queryFn: () => unwrap(api.GET('/guard/incidents', { params: { query: { pageSize: 50 } } })),
})

export const banSettingsQuery = queryOptions({
  queryKey: ['guard', 'bans', 'settings'],
  queryFn: () => unwrap(api.GET('/guard/bans/settings')),
})

export const bansQuery = (active?: boolean) =>
  queryOptions({
    queryKey: ['guard', 'bans', { active }],
    queryFn: () =>
      unwrap(api.GET('/guard/bans', { params: { query: { pageSize: 50, active: active ?? undefined } } })),
  })

export function useUpdateGuardSettings() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (body: GuardSettings) => unwrap(api.PUT('/guard/settings', { body })),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['guard', 'settings'] }),
  })
}

export function useRestoreIncident() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (id: number) => unwrap(api.POST('/guard/incidents/{id}/restore', { params: { path: { id } } })),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['guard', 'incidents'] }),
  })
}

export function useUpdateBanSettings() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (body: BanSettings) => unwrap(api.PUT('/guard/bans/settings', { body })),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['guard', 'bans', 'settings'] }),
  })
}

export function useLiftBan() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (id: number) => unwrap(api.POST('/guard/bans/{id}/lift', { params: { path: { id } } })),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['guard', 'bans'] }),
  })
}
