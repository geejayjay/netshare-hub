import type { ReactNode } from 'react'
import { InfoTip } from '@/shared/ui'

/** The title row at the top of every page: crisp title, optional (i) info tip or short subtitle, optional actions. */
export function PageHeader({
  title,
  description,
  info,
  actions,
}: {
  title: string
  description?: string
  info?: ReactNode
  actions?: ReactNode
}) {
  return (
    <header className="mb-5 flex flex-wrap items-center justify-between gap-4">
      <div className="min-w-0">
        <div className="flex items-center gap-2">
          <h1 className="text-xl font-semibold tracking-tight text-fg">{title}</h1>
          {info && <InfoTip content={info} label={`About ${title}`} />}
        </div>
        {description && <p className="mt-0.5 text-xs text-fg-muted">{description}</p>}
      </div>
      {actions && <div className="flex items-center gap-2">{actions}</div>}
    </header>
  )
}
