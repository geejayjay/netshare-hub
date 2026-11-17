import { useEffect, useId, useRef, type ComponentProps, type ReactNode } from 'react'
import { cn } from '@/shared/lib/cn'

interface MenuProps {
  /** Content of the button that opens the menu. */
  trigger: ReactNode
  triggerLabel?: string
  triggerClassName?: string
  className?: string
  children: ReactNode
}

/**
 * A dropdown menu built on the browser's popover API, which gives us the top
 * layer (never clipped by a scrolling sidebar), click-outside and Esc to close.
 *
 * Placement is automatic: above the trigger and as wide as it when the trigger
 * is wide (a sidebar footer), or to its right when it is narrow (a collapsed
 * sidebar or an icon button).
 */
export function Menu({ trigger, triggerLabel, triggerClassName, className, children }: MenuProps) {
  const id = useId()
  const triggerRef = useRef<HTMLButtonElement>(null)
  const menuRef = useRef<HTMLDivElement>(null)

  useEffect(() => {
    const menu = menuRef.current
    if (!menu) return
    const place = (e: Event) => {
      if ((e as ToggleEvent).newState !== 'open' || !triggerRef.current) return
      const a = triggerRef.current.getBoundingClientRect()
      const gap = 6
      const s = menu.style
      if (a.width >= 160) {
        s.width = `${a.width}px`
        s.left = `${a.left}px`
        s.bottom = `${window.innerHeight - a.top + gap}px`
      } else {
        s.width = ''
        s.left = `${a.right + gap}px`
        s.bottom = `${window.innerHeight - a.bottom}px`
      }
    }
    menu.addEventListener('beforetoggle', place)
    return () => menu.removeEventListener('beforetoggle', place)
  }, [])

  return (
    <>
      <button
        ref={triggerRef}
        type="button"
        popoverTarget={id}
        aria-haspopup="menu"
        aria-label={triggerLabel}
        className={triggerClassName}
      >
        {trigger}
      </button>
      <div
        ref={menuRef}
        id={id}
        popover="auto"
        role="menu"
        className={cn(
          'inset-auto m-0 min-w-56 rounded-xl border border-border bg-surface p-1.5 text-fg shadow-pop',
          className,
        )}
      >
        {children}
      </div>
    </>
  )
}

/** A clickable menu row. It closes the menu after running its action. */
export function MenuItem({ className, onClick, tone, ...props }: ComponentProps<'button'> & { tone?: 'danger' }) {
  return (
    <button
      type="button"
      role="menuitem"
      className={cn(
        'flex w-full items-center gap-2.5 rounded-md px-2.5 py-2 text-left text-sm font-medium transition-colors',
        '[&>svg]:size-4 [&>svg]:text-fg-subtle',
        tone === 'danger' ? 'text-danger hover:bg-danger-soft [&>svg]:text-danger' : 'hover:bg-surface-2',
        className,
      )}
      onClick={(e) => {
        onClick?.(e)
        e.currentTarget.closest<HTMLElement>('[popover]')?.hidePopover()
      }}
      {...props}
    />
  )
}

export function MenuSeparator() {
  return <div role="separator" className="-mx-1.5 my-1.5 h-px bg-border" />
}

/** A non-clickable row: a heading or a row holding its own control. */
export function MenuRow({ className, ...props }: ComponentProps<'div'>) {
  return <div className={cn('flex items-center gap-2.5 px-2.5 py-2 text-sm', className)} {...props} />
}
