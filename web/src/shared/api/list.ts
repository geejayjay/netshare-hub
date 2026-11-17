import type { components } from './schema'

export type PageMeta = components['schemas']['Meta']

/**
 * Every list endpoint returns this envelope. "page" is present only when the
 * endpoint is paginated, so the same components work for both kinds of list.
 */
export interface ListResponse<T> {
  items: T[]
  page?: PageMeta
}

/** Query parameters accepted by paginated list endpoints. */
export interface ListParams {
  page?: number
  pageSize?: number
  /** Field name; prefix with "-" for descending. */
  sort?: string
  q?: string
}

/** Reads list parameters from a URL search object, dropping anything invalid. */
export function parseListParams(search: Record<string, unknown>): ListParams {
  const page = Number(search.page)
  const pageSize = Number(search.pageSize)
  return {
    page: Number.isInteger(page) && page > 1 ? page : undefined,
    pageSize: Number.isInteger(pageSize) && pageSize > 0 ? pageSize : undefined,
    sort: typeof search.sort === 'string' && search.sort ? search.sort : undefined,
    q: typeof search.q === 'string' && search.q ? search.q : undefined,
  }
}
