import { X } from 'lucide-react'
import { useEffect, type ReactNode } from 'react'
import { cn } from '@/shared/lib/cn'
import { Button } from './button'

interface DrawerProps {
  open: boolean
  onClose: () => void
  title: string
  description?: string
  headerExtra?: ReactNode
  widthClass?: string
  children: ReactNode
}

/**
 * Right-hand slide-over panel for notifications, background jobs, and inspectors.
 */
export function Drawer({
  open,
  onClose,
  title,
  description,
  headerExtra,
  widthClass = 'w-[min(28rem,100vw)]',
  children,
}: DrawerProps) {
  useEffect(() => {
    if (!open) return
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onClose()
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [open, onClose])

  if (!open) return null

  return (
    <div className="fixed inset-0 z-50 flex justify-end" role="dialog" aria-modal="true" aria-label={title}>
      <div
        className="fixed inset-0 bg-black/40 backdrop-blur-[2px]"
        onClick={onClose}
        aria-hidden="true"
      />
      <aside
        className={cn(
          'animate-drawer-in relative z-10 flex h-full flex-col border-l border-border bg-surface text-fg shadow-pop',
          widthClass,
        )}
      >
        <header className="flex shrink-0 items-start justify-between gap-3 border-b border-border px-5 py-4">
          <div className="min-w-0">
            <h2 className="text-[0.9375rem] font-semibold tracking-tight">{title}</h2>
            {description && <p className="mt-0.5 text-xs text-fg-muted">{description}</p>}
          </div>
          <div className="flex shrink-0 items-center gap-1.5">
            {headerExtra}
            <Button variant="ghost" size="icon" className="size-8" onClick={onClose} aria-label="Close">
              <X className="size-4" />
            </Button>
          </div>
        </header>
        <div className="flex-1 overflow-y-auto p-5">{children}</div>
      </aside>
    </div>
  )
}
