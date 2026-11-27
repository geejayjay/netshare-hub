import { queryOptions, useMutation, useQueryClient } from '@tanstack/react-query'
import { api, unwrap, type Schema } from '@/shared/api'

export type StorageReport = Schema<'StorageReport'>
export type StorageShareStat = Schema<'StorageShareStat'>
export type StorageFileItem = Schema<'StorageFileItem'>
export type StorageDuplicateGroup = Schema<'StorageDuplicateGroup'>
export type StorageScanConfig = Schema<'StorageScanConfig'>

export const storageReportQuery = queryOptions({
  queryKey: ['storage', 'report'],
  queryFn: () => unwrap(api.GET('/storage/report')),
  staleTime: 15_000,
  refetchInterval: (query) => (query.state.data?.scanning ? 1500 : false),
})

export function useUpdateStorageConfig() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (body: StorageScanConfig) => unwrap(api.PUT('/storage/config', { body })),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['storage', 'report'] }),
  })
}

export function useRunStorageScan() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (body?: StorageScanConfig) => unwrap(api.POST('/storage/scan', body ? { body } : {})),
    onSuccess: (report) => {
      qc.setQueryData(['storage', 'report'], report)
    },
  })
}
