import type { ComponentProps } from 'react'
import { cn } from '@/shared/lib/cn'

/** Shared look for every text-like control, so inputs and selects always match. */
const controlClass =
  'h-9 w-full rounded-md border border-border-strong bg-surface px-3 text-sm text-fg shadow-xs ' +
  'transition-[border-color,box-shadow] duration-150 placeholder:text-fg-subtle ' +
  'hover:border-fg-subtle ' +
  // Focus: a soft ring in the accent color replaces the default outline.
  'focus-visible:border-primary focus-visible:outline-none focus-visible:ring-3 focus-visible:ring-primary/20 ' +
  'aria-invalid:border-danger aria-invalid:focus-visible:ring-danger/20 ' +
  'disabled:opacity-50 disabled:pointer-events-none'

export function Input({ className, ...props }: ComponentProps<'input'>) {
  return <input className={cn(controlClass, className)} {...props} />
}

export function Select({ className, ...props }: ComponentProps<'select'>) {
  return <select className={cn(controlClass, 'pr-8', className)} {...props} />
}

export function Label({ className, ...props }: ComponentProps<'label'>) {
  return <label className={cn('text-[0.8125rem] font-medium text-fg', className)} {...props} />
}
