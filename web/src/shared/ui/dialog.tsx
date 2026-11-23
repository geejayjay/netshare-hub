import { X } from 'lucide-react'
import { useEffect, useRef, type ReactNode } from 'react'
import { cn } from '@/shared/lib/cn'
import { Button } from './button'

const dialogSize = {
  sm: 'w-[min(26rem,calc(100vw-2rem))]',
  md: 'w-[min(32rem,calc(100vw-2rem))]',
  lg: 'w-[min(44rem,calc(100vw-2rem))]',
  xl: 'w-[min(56rem,calc(100vw-2rem))]',
} as const

interface DialogProps {
  open: boolean
  onClose: () => void
  title: string
  description?: string
  size?: keyof typeof dialogSize
  children: ReactNode
}

/**
 * Modal dialog built on the browser's <dialog>, with focus trapping,
 * Esc to close, backdrop blur, and scrollable body for taller forms.
 */
export function Dialog({ open, onClose, title, description, size = 'md', children }: DialogProps) {
  const ref = useRef<HTMLDialogElement>(null)

  useEffect(() => {
    const dialog = ref.current
    if (!dialog) return
    if (open && !dialog.open) dialog.showModal()
    if (!open && dialog.open) dialog.close()
  }, [open])

  return (
    <dialog
      ref={ref}
      onClose={onClose}
      onClick={(e) => e.target === ref.current && onClose()}
      className={cn(
        'm-auto max-h-[calc(100dvh-2.5rem)] overflow-hidden rounded-xl border border-border bg-surface p-0 text-fg shadow-pop backdrop:bg-black/50 backdrop:backdrop-blur-[3px]',
        dialogSize[size],
      )}
    >
      {open && (
        <div className="animate-modal-in flex max-h-[calc(100dvh-2.5rem)] flex-col">
          <header className="flex shrink-0 items-start justify-between gap-4 border-b border-border px-6 py-4">
            <div className="min-w-0">
              <h2 className="text-[0.9375rem] font-semibold tracking-tight text-fg">{title}</h2>
              {description && <p className="mt-0.5 text-sm text-fg-muted">{description}</p>}
            </div>
            <Button variant="ghost" size="icon" className="-mt-1 -mr-2 size-8 shrink-0" onClick={onClose} aria-label="Close">
              <X className="size-4" />
            </Button>
          </header>
          <div className="overflow-y-auto px-6 py-5">{children}</div>
        </div>
      )}
    </dialog>
  )
}
