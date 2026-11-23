import type { ReactNode } from 'react'
import { cn } from '@/shared/lib/cn'
import { toneClass, type Tone } from './badge'

interface StatCardProps {
  label: string
  value: ReactNode
  detail?: ReactNode
  icon?: ReactNode
  tone?: Tone
  className?: string
  onClick?: () => void
}

/** Sleek KPI metric card with tabular figures and optional icon accent. */
export function StatCard({ label, value, detail, icon, tone = 'info', className, onClick }: StatCardProps) {
  const Tag = onClick ? 'button' : 'div'
  return (
    <Tag
      type={onClick ? 'button' : undefined}
      onClick={onClick}
      className={cn(
        'flex flex-col gap-3 rounded-xl border border-border bg-surface p-5 text-left shadow-card transition-all duration-150',
        onClick && 'hover:border-border-strong hover:bg-surface-2/40',
        className,
      )}
    >
      <div className="flex items-center justify-between gap-2">
        <span className="text-xs font-medium text-fg-muted">{label}</span>
        {icon && (
          <span className={cn('grid size-8 place-items-center rounded-lg [&>svg]:size-4', toneClass[tone])}>
            {icon}
          </span>
        )}
      </div>
      <div className="flex flex-col gap-1">
        <div className="text-2xl font-semibold tracking-tight text-fg tabular-nums">{value}</div>
        {detail && <div className="text-xs text-fg-muted">{detail}</div>}
      </div>
    </Tag>
  )
}
