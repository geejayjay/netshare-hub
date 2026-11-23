import type { ComponentProps } from 'react'
import { cn } from '@/shared/lib/cn'

/**
 * Tone is the shared vocabulary for status colors. Badges, alerts and
 * notifications all use the same five tones.
 */
export type Tone = 'neutral' | 'info' | 'success' | 'warning' | 'danger'

/** Soft background + matching text for each tone. */
export const toneClass: Record<Tone, string> = {
  neutral: 'bg-surface-2 text-fg-muted ring-1 ring-inset ring-border',
  info: 'bg-primary-soft text-primary ring-1 ring-inset ring-primary/15',
  success: 'bg-success-soft text-success ring-1 ring-inset ring-success/15',
  warning: 'bg-warning-soft text-warning ring-1 ring-inset ring-warning/15',
  danger: 'bg-danger-soft text-danger ring-1 ring-inset ring-danger/15',
}

interface BadgeProps extends ComponentProps<'span'> {
  tone?: Tone
  /** Shows a small status dot before the label. */
  dot?: boolean
}

export function Badge({ className, tone = 'neutral', dot, children, ...props }: BadgeProps) {
  return (
    <span
      className={cn(
        'inline-flex items-center gap-1.5 rounded-full px-2.5 py-0.5 text-[0.75rem] font-medium whitespace-nowrap',
        toneClass[tone],
        className,
      )}
      {...props}
    >
      {dot && <span className="size-1.5 rounded-full bg-current" aria-hidden="true" />}
      {children}
    </span>
  )
}
