import { queryOptions } from '@tanstack/react-query'
import { api, unwrap, type Schema } from '@/shared/api'

export type LogEntry = Schema<'LogEntry'>
export type LogLevel = '' | 'debug' | 'info' | 'warn' | 'error'

export interface LogFilters {
  level?: LogLevel
  requestId?: string
  q?: string
  limit?: number
}

export function systemLogsQuery(filters: LogFilters) {
  return queryOptions({
    queryKey: ['system', 'logs', filters],
    queryFn: () =>
      unwrap(
        api.GET('/system/logs', {
          params: {
            query: {
              level: filters.level || undefined,
              requestId: filters.requestId || undefined,
              q: filters.q || undefined,
              limit: filters.limit ?? 200,
            },
          },
        }),
      ),
  })
}
