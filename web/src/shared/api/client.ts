import createClient, { type Middleware } from 'openapi-fetch'
import { ApiError, type Problem } from './problem'
import type { paths } from './schema'

/** Where the API lives. Change it here if the prefix ever changes. */
export const API_BASE = '/api/v1'

const SAFE_METHODS = new Set(['GET', 'HEAD', 'OPTIONS'])

// The CSRF token lives in memory only. It arrives with the session info.
let csrfToken = ''
export function setCsrfToken(token: string) {
  csrfToken = token
}

async function toApiError(response: Response): Promise<ApiError> {
  try {
    return new ApiError((await response.json()) as Problem)
  } catch {
    // Not a problem+json body (e.g. a proxy error page).
    return new ApiError({
      status: response.status,
      code: response.status >= 500 ? 'INTERNAL' : 'BAD_REQUEST',
      requestId: response.headers.get('X-Request-ID') ?? undefined,
    })
  }
}

const middleware: Middleware = {
  onRequest({ request }) {
    if (!SAFE_METHODS.has(request.method) && csrfToken) {
      request.headers.set('X-CSRF-Token', csrfToken)
    }
    return request
  },
  // Every non-2xx response becomes a thrown ApiError, so callers handle
  // failures in one way (try/catch, or TanStack Query's error state).
  async onResponse({ response }) {
    if (!response.ok) throw await toApiError(response)
    return response
  },
  onError() {
    return new ApiError({ status: 0, code: 'NETWORK_ERROR' })
  },
}

/** The typed API client. Paths, parameters and bodies are checked against the server's spec. */
export const api = createClient<paths>({ baseUrl: API_BASE })
api.use(middleware)

/** Returns the response body of a call. Failures have already been thrown as ApiError. */
export async function unwrap<T>(call: Promise<{ data?: T }>): Promise<T> {
  const { data } = await call
  return data as T
}
