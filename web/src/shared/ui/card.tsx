import type { ComponentProps } from 'react'
import { cn } from '@/shared/lib/cn'

export function Card({ className, ...props }: ComponentProps<'div'>) {
  return (
    <div
      className={cn('overflow-hidden rounded-xl border border-border bg-surface shadow-card', className)}
      {...props}
    />
  )
}

export function CardHeader({ className, ...props }: ComponentProps<'div'>) {
  return <div className={cn('flex flex-col gap-1 p-5 pb-0', className)} {...props} />
}

export function CardTitle({ className, ...props }: ComponentProps<'h2'>) {
  return <h2 className={cn('text-[0.9375rem] font-semibold', className)} {...props} />
}

export function CardDescription({ className, ...props }: ComponentProps<'p'>) {
  return <p className={cn('text-sm text-fg-muted', className)} {...props} />
}

export function CardContent({ className, ...props }: ComponentProps<'div'>) {
  return <div className={cn('p-5', className)} {...props} />
}
