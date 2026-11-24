import { queryOptions, useMutation, useQueryClient } from '@tanstack/react-query'
import { api, unwrap, type Schema } from '@/shared/api'

export type BlockDisk = Schema<'BlockDisk'>
export type BlockDiskInput = Schema<'BlockDiskInput'>
export type BlockDiskPatch = Schema<'BlockDiskPatch'>
export type BlockStatus = Schema<'BlockStatus'>
export type DiskDevice = Schema<'DiskDevice'>

export const blockStatusQuery = queryOptions({
  queryKey: ['block', 'status'],
  queryFn: () => unwrap(api.GET('/block/status')),
})

export const blockDisksQuery = queryOptions({
  queryKey: ['block', 'disks'],
  queryFn: () => unwrap(api.GET('/block/disks')),
})

export const blockDevicesQuery = queryOptions({
  queryKey: ['block', 'devices'],
  queryFn: () => unwrap(api.GET('/block/devices')),
})

export function useCreateBlockDisk() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (body: BlockDiskInput) => unwrap(api.POST('/block/disks', { body })),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['block'] }),
  })
}

export function useUpdateBlockDisk() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: ({ id, body }: { id: number; body: BlockDiskPatch }) =>
      unwrap(api.PATCH('/block/disks/{id}', { params: { path: { id } }, body })),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['block'] }),
  })
}

export function useDeleteBlockDisk() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (id: number) => unwrap(api.DELETE('/block/disks/{id}', { params: { path: { id } } })),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['block'] }),
  })
}
