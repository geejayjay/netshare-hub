export { api, unwrap, setCsrfToken, getCsrfToken, API_BASE } from './client'
export { ApiError, isApiError, errorMessage, errorReference, fieldErrors, isFieldError } from './problem'
export type { Problem, FieldError } from './problem'
export { parseListParams } from './list'
export type { ListResponse, ListParams, PageMeta } from './list'
export { subscribeSSE, useSSE } from './sse'
export type { components, paths } from './schema'

import type { components } from './schema'

/** Shortcut for a schema type from the API spec: Schema<'User'>. */
export type Schema<K extends keyof components['schemas']> = components['schemas'][K]
