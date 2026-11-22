import { queryOptions } from '@tanstack/react-query'
import { api, unwrap, type Schema } from '@/shared/api'

export type MyShare = Schema<'MyShare'>

export const mySharesQuery = queryOptions({
  queryKey: ['my', 'shares'] as const,
  queryFn: () => unwrap(api.GET('/my/shares')),
})
