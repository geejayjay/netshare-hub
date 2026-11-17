import type { ComponentProps } from 'react'
import { cn } from '@/shared/lib/cn'

/** Styled table parts. Use <DataTable> for data; use these only to build it. */

export function Table({ className, ...props }: ComponentProps<'table'>) {
  return (
    <div className="w-full overflow-x-auto">
      <table className={cn('w-full border-collapse text-sm', className)} {...props} />
    </div>
  )
}

export function TableHead({ className, ...props }: ComponentProps<'thead'>) {
  return <thead className={cn('border-b border-border bg-surface-2/50 text-left', className)} {...props} />
}

export function TableBody(props: ComponentProps<'tbody'>) {
  return <tbody {...props} />
}

export function TableRow({ className, ...props }: ComponentProps<'tr'>) {
  return (
    <tr
      className={cn('border-b border-border transition-colors last:border-0 hover:bg-surface-2/50', className)}
      {...props}
    />
  )
}

export function TableHeaderCell({ className, ...props }: ComponentProps<'th'>) {
  return (
    <th
      className={cn('h-10 px-4 text-[0.6875rem] font-semibold tracking-[0.05em] whitespace-nowrap text-fg-subtle uppercase', className)}
      {...props}
    />
  )
}

export function TableCell({ className, ...props }: ComponentProps<'td'>) {
  return <td className={cn('h-13 px-4', className)} {...props} />
}
