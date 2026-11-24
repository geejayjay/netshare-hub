import { keepPreviousData, queryOptions } from '@tanstack/react-query'
import { api, unwrap, type Schema } from '@/shared/api'

export type AuditEntry = Schema<'AuditEntry'>

export interface AuditFilters {
  actor?: string
  action?: string
  q?: string
  page?: number
  pageSize?: number
}

export function auditLogQuery(filters: AuditFilters) {
  return queryOptions({
    queryKey: ['audit', filters],
    queryFn: () =>
      unwrap(
        api.GET('/audit', {
          params: {
            query: {
              actor: filters.actor || undefined,
              action: filters.action || undefined,
              q: filters.q || undefined,
              page: filters.page ?? 1,
              pageSize: filters.pageSize ?? 50,
            },
          },
        }),
      ),
    placeholderData: keepPreviousData,
  })
}
