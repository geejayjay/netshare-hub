import type { ComponentProps, ReactNode } from 'react'
import { AlertTriangle, CheckCircle2, CircleAlert, Info, type LucideIcon } from 'lucide-react'
import { cn } from '@/shared/lib/cn'
import { toneClass, type Tone } from './badge'

const toneIcon: Record<Tone, LucideIcon> = {
  neutral: Info,
  info: Info,
  success: CheckCircle2,
  warning: AlertTriangle,
  danger: CircleAlert,
}

interface AlertProps extends Omit<ComponentProps<'div'>, 'title'> {
  tone?: Tone
  /**
   * The message's heading, shown as text (never a hover tooltip). With no
   * children it is the whole message; with children it heads them.
   */
  title?: ReactNode
}

/** An inline message inside a page or form (not a pop-up notification). */
export function Alert({ tone = 'danger', title, className, children, ...props }: AlertProps) {
  const Icon = toneIcon[tone]
  const hasTitle = title !== undefined && title !== null && title !== false && title !== ''
  const hasBody = children !== undefined && children !== null && children !== false && children !== ''
  return (
    <div
      role="alert"
      className={cn('flex items-start gap-2.5 rounded-md px-3 py-2.5 text-sm', toneClass[tone], className)}
      {...props}
    >
      <Icon className="mt-0.5 size-4 shrink-0" aria-hidden="true" />
      <div className="min-w-0 flex-1 break-words">
        {hasTitle && <div className={hasBody ? 'font-semibold' : 'font-medium'}>{title}</div>}
        {hasBody && <div className={cn(hasTitle && 'mt-0.5 opacity-90')}>{children}</div>}
      </div>
    </div>
  )
}
