import { errorMessage, errorReference } from '@/shared/api'
import { useNotifications } from './store'

const AUTO_DISMISS_MS = 5000

const push = useNotifications.getState().push

/**
 * The one way to tell the user something happened. Call it from anywhere:
 *
 *   notify.success('User created')
 *   notify.error(error)            // any thrown error; shows the request ID
 *
 * Success and info messages dismiss themselves. Warnings and errors stay
 * until the user dismisses them.
 */
export const notify = {
  success: (title: string, description?: string) =>
    push({ tone: 'success', title, description, duration: AUTO_DISMISS_MS }),

  info: (title: string, description?: string) => push({ tone: 'info', title, description, duration: AUTO_DISMISS_MS }),

  warning: (title: string, description?: string) => push({ tone: 'warning', title, description }),

  error: (error: unknown, title?: string) =>
    push({
      tone: 'danger',
      title: title ?? errorMessage(error),
      description: title ? errorMessage(error) : undefined,
      reference: errorReference(error),
    }),
}
