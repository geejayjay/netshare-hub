import { X } from 'lucide-react'
import { useEffect, useRef, type ReactNode } from 'react'
import { Button } from './button'

interface DialogProps {
  open: boolean
  onClose: () => void
  title: string
  description?: string
  children: ReactNode
}

/**
 * Modal dialog built on the browser's <dialog>, which gives us focus trapping,
 * Esc to close and the backdrop for free.
 */
export function Dialog({ open, onClose, title, description, children }: DialogProps) {
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
      // A click on the backdrop lands on the <dialog> element itself.
      onClick={(e) => e.target === ref.current && onClose()}
      className="m-auto w-[min(30rem,calc(100vw-2rem))] rounded-xl border border-border bg-surface p-0 text-fg shadow-pop backdrop:bg-black/40 backdrop:backdrop-blur-[2px]"
    >
      {open && (
        <div className="flex flex-col gap-5 p-6">
          <header className="flex items-start justify-between gap-4">
            <div>
              <h2 className="text-base font-semibold">{title}</h2>
              {description && <p className="mt-0.5 text-sm text-fg-muted">{description}</p>}
            </div>
            <Button variant="ghost" size="icon" className="-mt-1.5 -mr-2 size-8" onClick={onClose} aria-label="Close">
              <X className="size-4" />
            </Button>
          </header>
          {children}
        </div>
      )}
    </dialog>
  )
}
