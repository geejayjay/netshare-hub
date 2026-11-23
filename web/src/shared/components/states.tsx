import { CircleAlert, Inbox, ShieldOff } from 'lucide-react'
import type { ReactNode } from 'react'
import { errorMessage, errorReference } from '@/shared/api'
import { Button, Spinner } from '@/shared/ui'

/** Layout shared by every "nothing to show here" state. */
function State({
  icon,
  title,
  description,
  action,
}: {
  icon: ReactNode
  title: string
  description?: ReactNode
  action?: ReactNode
}) {
  return (
    <div className="flex flex-col items-center gap-3 px-6 py-12 text-center">
      <div className="text-fg-muted [&>svg]:size-8">{icon}</div>
      <div>
        <p className="font-medium">{title}</p>
        {description && <div className="mt-1 text-sm text-fg-muted">{description}</div>}
      </div>
      {action}
    </div>
  )
}

export function EmptyState({ title, description, action }: { title: string; description?: string; action?: ReactNode }) {
  return <State icon={<Inbox />} title={title} description={description} action={action} />
}

/** Shows any error with its message, the request ID to quote, and a retry button. */
export function ErrorState({ error, onRetry }: { error: unknown; onRetry?: () => void }) {
  const reference = errorReference(error)
  return (
    <State
      icon={<CircleAlert className="text-danger" />}
      title={errorMessage(error)}
      description={
        reference && (
          <>
            Reference: <code className="font-mono select-all">{reference}</code>
          </>
        )
      }
      action={
        onRetry && (
          <Button variant="secondary" size="sm" onClick={onRetry}>
            Try again
          </Button>
        )
      }
    />
  )
}

export function LoadingState({ label }: { label?: string }) {
  return (
    <div className="flex flex-col items-center justify-center gap-3 px-6 py-12 text-center text-sm text-fg-muted">
      <Spinner />
      {label && <span>{label}</span>}
    </div>
  )
}

export function NoAccessState() {
  return (
    <State
      icon={<ShieldOff />}
      title="You do not have access to this page"
      description="Ask an administrator if you think this is a mistake."
    />
  )
}
