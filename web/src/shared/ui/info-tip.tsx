import { Info } from 'lucide-react'
import { useCallback, useId, useRef, type ReactNode } from 'react'
import { cn } from '@/shared/lib/cn'

interface InfoTipProps {
  /** Explanation shown on hover or keyboard focus. */
  content: ReactNode
  /** Accessible label for the trigger button. */
  label?: string
  className?: string
}

/**
 * Compact circular "i" trigger that reveals a clean explanation on hover/focus.
 * Uses the native Popover API so it renders in the browser's top layer and is
 * never clipped by cards, tables, drawers, or <dialog> modals.
 */
export function InfoTip({ content, label = 'More information', className }: InfoTipProps) {
  const id = useId()
  const triggerRef = useRef<HTMLButtonElement>(null)
  const tipRef = useRef<HTMLDivElement>(null)

  const show = useCallback(() => {
    const trigger = triggerRef.current
    const tip = tipRef.current
    if (!trigger || !tip) return

    try {
      if (!tip.matches(':popover-open')) tip.showPopover()
    } catch {
      return
    }

    const a = trigger.getBoundingClientRect()
    const tipRect = tip.getBoundingClientRect()
    const gap = 8
    const margin = 12

    // Prefer above the icon; flip below if too close to the top of the viewport.
    let top = a.top - tipRect.height - gap
    if (top < margin) {
      top = a.bottom + gap
    }

    // Center horizontally over the icon and clamp within the viewport.
    let left = a.left + a.width / 2 - tipRect.width / 2
    left = Math.max(margin, Math.min(left, window.innerWidth - tipRect.width - margin))

    tip.style.top = `${top}px`
    tip.style.left = `${left}px`
  }, [])

  const hide = useCallback(() => {
    const tip = tipRef.current
    if (!tip) return
    try {
      if (tip.matches(':popover-open')) tip.hidePopover()
    } catch {
      // ignore if already closed
    }
  }, [])

  return (
    <span className={cn('inline-flex items-center align-middle', className)}>
      <button
        ref={triggerRef}
        type="button"
        aria-label={label}
        aria-describedby={id}
        onMouseEnter={show}
        onMouseLeave={hide}
        onFocus={show}
        onBlur={hide}
        onClick={(e) => {
          e.preventDefault()
          e.stopPropagation()
          const tip = tipRef.current
          if (tip?.matches(':popover-open')) hide()
          else show()
        }}
        className="inline-flex size-4 items-center justify-center rounded-full text-fg-subtle transition-colors hover:bg-surface-2 hover:text-fg focus-visible:text-fg"
      >
        <Info className="size-3.5" />
      </button>
      <div
        ref={tipRef}
        id={id}
        role="tooltip"
        popover="manual"
        className="pointer-events-none fixed inset-auto m-0 max-w-xs rounded-lg border border-border-strong bg-surface px-3 py-2 text-xs leading-relaxed font-normal text-fg-muted shadow-pop"
      >
        {content}
      </div>
    </span>
  )
}
