import type { ComponentProps } from 'react'
import { cn } from '@/shared/lib/cn'
import { toneClass, type Tone } from './badge'

/** An inline message inside a page or form (not a pop-up notification). */
export function Alert({ tone = 'danger', className, ...props }: ComponentProps<'div'> & { tone?: Tone }) {
  return <div role="alert" className={cn('rounded-md px-3 py-2 text-sm', toneClass[tone], className)} {...props} />
}
