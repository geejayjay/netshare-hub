import type { ReactNode } from 'react'
import { cn } from '@/shared/lib/cn'

export interface TabItem<T extends string> {
  id: T
  label: string
  icon?: ReactNode
  badge?: string | number
}

/** Clean segmented tab bar for settings, editors, and sub-views. */
export function Tabs<T extends string>({
  items,
  value,
  onChange,
  className,
}: {
  items: TabItem<T>[]
  value: T
  onChange: (value: T) => void
  className?: string
}) {
  return (
    <div
      role="tablist"
      className={cn(
        'inline-flex flex-wrap items-center gap-1 rounded-lg border border-border bg-surface-2 p-1',
        className,
      )}
    >
      {items.map((item) => {
        const active = item.id === value
        return (
          <button
            key={item.id}
            type="button"
            role="tab"
            aria-selected={active}
            onClick={() => onChange(item.id)}
            className={cn(
              'inline-flex items-center gap-1.5 rounded-md px-3 py-1.5 text-xs font-medium transition-all duration-150',
              active
                ? 'bg-surface text-fg shadow-xs ring-1 ring-border'
                : 'text-fg-muted hover:text-fg',
            )}
          >
            {item.icon}
            <span>{item.label}</span>
            {item.badge !== undefined && (
              <span
                className={cn(
                  'rounded-full px-1.5 py-0.25 text-[0.6875rem] tabular-nums',
                  active ? 'bg-primary-soft text-primary' : 'bg-surface-3 text-fg-muted',
                )}
              >
                {item.badge}
              </span>
            )}
          </button>
        )
      })}
    </div>
  )
}
