import type { components } from './schema'

export type Problem = components['schemas']['Problem']
export type FieldError = components['schemas']['FieldError']

/**
 * The one error type the UI deals with. Every failed API call (and every
 * network failure) is turned into an ApiError by the client.
 */
export class ApiError extends Error {
  readonly status: number
  readonly code: string
  readonly requestId?: string
  readonly fields: FieldError[]

  constructor(problem: Pick<Problem, 'status' | 'code'> & Partial<Problem>) {
    super(problem.detail || problem.title || problem.code)
    this.name = 'ApiError'
    this.status = problem.status
    this.code = problem.code
    this.requestId = problem.requestId
    this.fields = problem.errors ?? []
  }
}

export function isApiError(error: unknown): error is ApiError {
  return error instanceof ApiError
}

/**
 * Messages by error code. The server already sends a friendly "detail", so
 * add a code here only to reword or translate it.
 */
const messages: Record<string, string> = {
  NETWORK_ERROR: 'Cannot reach NetShare Hub. Check your connection and try again.',
  INTERNAL: 'Something went wrong on the server.',
}

/** A message that is safe and useful to show to the user. */
export function errorMessage(error: unknown): string {
  if (isApiError(error)) return messages[error.code] ?? error.message
  return 'Something unexpected happened.'
}

/** The request ID to quote when asking for help, if there is one. */
export function errorReference(error: unknown): string | undefined {
  return isApiError(error) ? error.requestId : undefined
}

/** Field errors as { fieldName: message }, ready to show next to form inputs. */
export function fieldErrors(error: unknown): Record<string, string> {
  if (!isApiError(error)) return {}
  return Object.fromEntries(error.fields.map((f) => [f.field, f.message]))
}

/** True when the error is only about form fields (no need for a toast as well). */
export function isFieldError(error: unknown): boolean {
  return isApiError(error) && error.fields.length > 0
}
