import { queryOptions, useMutation, useQueryClient } from '@tanstack/react-query'
import { api, unwrap, type Schema } from '@/shared/api'

export type StorageReport = Schema<'StorageReport'>
export type StorageShareStat = Schema<'StorageShareStat'>
export type StorageFileItem = Schema<'StorageFileItem'>
export type StorageDuplicateGroup = Schema<'StorageDuplicateGroup'>

export const storageReportQuery = queryOptions({
  queryKey: ['storage', 'report'],
  queryFn: () => unwrap(api.GET('/storage/report')),
  staleTime: 15_000,
})

export function useRunStorageScan() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: () => unwrap(api.POST('/storage/scan')),
    onSuccess: (report) => {
      qc.setQueryData(['storage', 'report'], report)
    },
  })
}
