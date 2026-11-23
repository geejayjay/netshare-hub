import { keepPreviousData, queryOptions } from '@tanstack/react-query'
import { API_BASE, api, unwrap, type Schema } from '@/shared/api'

export type ActivityEntry = Schema<'ActivityEntry'>
export type ActivityOp =
  | 'connect'
  | 'disconnect'
  | 'write'
  | 'rename'
  | 'delete'
  | 'create_folder'
  | 'login'
  | 'login_failed'

export interface ActivityFilters {
  share?: string
  user?: string
  op?: ActivityOp
  clientIp?: string
  path?: string
  before?: number
  limit?: number
}

export function activityQuery(filters: ActivityFilters) {
  return queryOptions({
    queryKey: ['activity', filters],
    queryFn: () =>
      unwrap(
        api.GET('/activity', {
          params: {
            query: {
              share: filters.share || undefined,
              user: filters.user || undefined,
              op: filters.op || undefined,
              clientIp: filters.clientIp || undefined,
              path: filters.path || undefined,
              before: filters.before || undefined,
              limit: filters.limit ?? 50,
            },
          },
        }),
      ),
    placeholderData: keepPreviousData,
  })
}

export function activityExportUrl(filters: ActivityFilters): string {
  const qs = new URLSearchParams()
  if (filters.share) qs.set('share', filters.share)
  if (filters.user) qs.set('user', filters.user)
  if (filters.op) qs.set('op', filters.op)
  if (filters.clientIp) qs.set('clientIp', filters.clientIp)
  if (filters.path) qs.set('path', filters.path)
  const str = qs.toString()
  return `${API_BASE}/activity/export${str ? `?${str}` : ''}`
}
