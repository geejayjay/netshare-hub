import { Bell, ChevronDown, ChevronUp, CircleAlert, CircleCheck, Info, TriangleAlert, X, type LucideIcon } from 'lucide-react'
import { useEffect, useState } from 'react'
import { cn } from '@/shared/lib/cn'
import { Button, toneClass, type Tone } from '@/shared/ui'
import { useNotifications, type Notification } from './store'

const toneIcon: Record<Tone, LucideIcon> = {
  neutral: Info,
  info: Info,
  success: CircleCheck,
  warning: TriangleAlert,
  danger: CircleAlert,
}

/** How many toasts peek out of the collapsed stack. */
const COLLAPSED_VISIBLE = 3

/**
 * The toast stack. Collapsed, the newest toast is on top and older ones peek
 * out behind it; hovering or pressing the stack expands it into a list.
 *
 * This is the Phase 0 version. The notification center (bell panel), swipe
 * gestures and server-pushed alerts are added in Phase 1 (PLAN.md §6.15).
 */
export function Toaster() {
  const { items, expanded, hidden, dismiss, clear, setExpanded, setHidden } = useNotifications()
  const [hovered, setHovered] = useState(false)
  const open = expanded || hovered

  // Esc dismisses the newest toast.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      const newest = useNotifications.getState().items[0]
      if (e.key === 'Escape' && newest) useNotifications.getState().dismiss(newest.id)
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [])

  if (items.length === 0) return null

  if (hidden) {
    return (
      <Button
        variant="secondary"
        className="fixed right-4 bottom-4 z-50 shadow-pop"
        onClick={() => setHidden(false)}
        aria-label={`Show ${items.length} notifications`}
      >
        <Bell className="size-4" />
        {items.length}
      </Button>
    )
  }

  const shown = open ? items : items.slice(0, COLLAPSED_VISIBLE)

  return (
    <section
      aria-label="Notifications"
      className="fixed right-4 bottom-4 z-50 flex w-[min(24rem,calc(100vw-2rem))] flex-col gap-2 max-md:top-4 max-md:bottom-auto"
      onMouseEnter={() => setHovered(true)}
      onMouseLeave={() => setHovered(false)}
    >
      <ol
        className={cn('flex flex-col', open ? 'max-h-[70dvh] gap-2 overflow-y-auto' : 'cursor-pointer')}
        onClick={() => !open && setExpanded(true)}
      >
        {shown.map((item, index) => (
          <li
            key={item.id}
            // Collapsed: each older toast is tucked under the one above it.
            style={open || index === 0 ? undefined : { marginTop: '-2.75rem', scale: `${1 - index * 0.04}`, zIndex: -index }}
            className="relative origin-bottom transition-all duration-200"
          >
            <Toast item={item} paused={open} obscured={!open && index > 0} onDismiss={() => dismiss(item.id)} />
          </li>
        ))}
      </ol>

      {(open || items.length > 1) && (
        <div className="flex items-center justify-end gap-1 text-sm">
          {!open && items.length > COLLAPSED_VISIBLE && (
            <span className="mr-auto pl-1 text-fg-muted">+{items.length - COLLAPSED_VISIBLE} more</span>
          )}
          <Button variant="ghost" size="sm" onClick={() => setExpanded(!expanded)}>
            {expanded ? <ChevronDown className="size-4" /> : <ChevronUp className="size-4" />}
            {expanded ? 'Collapse' : 'Expand'}
          </Button>
          <Button variant="ghost" size="sm" onClick={clear}>
            Clear all
          </Button>
          <Button variant="ghost" size="sm" onClick={() => setHidden(true)}>
            Hide
          </Button>
        </div>
      )}
    </section>
  )
}

function Toast({
  item,
  paused,
  obscured,
  onDismiss,
}: {
  item: Notification
  /** Auto-dismiss waits while the stack is open, so nothing vanishes while being read. */
  paused: boolean
  /** Tucked behind another toast: keep the shape, hide the content. */
  obscured: boolean
  onDismiss: () => void
}) {
  useEffect(() => {
    if (!item.duration || paused) return
    const timer = setTimeout(() => useNotifications.getState().dismiss(item.id), item.duration)
    return () => clearTimeout(timer)
  }, [item.duration, item.id, paused])

  const Icon = toneIcon[item.tone]
  const urgent = item.tone === 'danger' || item.tone === 'warning'

  return (
    <div
      role={urgent ? 'alert' : 'status'}
      className="flex items-start gap-3 rounded-lg border border-border bg-surface p-3 shadow-pop"
    >
      <span className={cn('grid size-7 shrink-0 place-items-center rounded-md', toneClass[item.tone])}>
        <Icon className="size-4" />
      </span>
      <div className={cn('min-w-0 flex-1 text-sm', obscured && 'invisible')}>
        <p className="font-medium">
          {item.title}
          {item.count > 1 && <span className="ml-1.5 text-fg-muted">×{item.count}</span>}
        </p>
        {item.description && <p className="text-fg-muted">{item.description}</p>}
        {item.reference && (
          <p className="mt-1 text-xs text-fg-muted">
            Reference: <code className="font-mono select-all">{item.reference}</code>
          </p>
        )}
      </div>
      <Button
        variant="ghost"
        size="icon"
        className={cn('-m-1.5 shrink-0', obscured && 'invisible')}
        onClick={(e) => {
          e.stopPropagation()
          onDismiss()
        }}
        aria-label="Dismiss notification"
      >
        <X className="size-4" />
      </Button>
    </div>
  )
}
