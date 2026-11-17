import { create } from 'zustand'
import type { Tone } from '@/shared/ui'

export interface Notification {
  id: number
  tone: Tone
  title: string
  description?: string
  /** Request ID to quote when asking for help. */
  reference?: string
  /** Milliseconds until it dismisses itself; undefined = stays until dismissed. */
  duration?: number
  /** How many identical notifications were merged into this one. */
  count: number
}

interface NotificationState {
  items: Notification[]
  expanded: boolean
  hidden: boolean
  push: (n: Omit<Notification, 'id' | 'count'>) => void
  dismiss: (id: number) => void
  clear: () => void
  setExpanded: (expanded: boolean) => void
  setHidden: (hidden: boolean) => void
}

let nextId = 1

export const useNotifications = create<NotificationState>((set) => ({
  items: [],
  expanded: false,
  hidden: false,

  push: (n) =>
    set((state) => {
      // Merge identical notifications so a burst never floods the screen.
      const same = state.items.find((i) => i.tone === n.tone && i.title === n.title && i.description === n.description)
      if (same) {
        const merged = { ...same, ...n, id: nextId++, count: same.count + 1 }
        return { items: [merged, ...state.items.filter((i) => i !== same)] }
      }
      return { items: [{ ...n, id: nextId++, count: 1 }, ...state.items] }
    }),

  dismiss: (id) => set((state) => ({ items: state.items.filter((i) => i.id !== id) })),
  clear: () => set({ items: [], expanded: false }),
  setExpanded: (expanded) => set({ expanded }),
  setHidden: (hidden) => set({ hidden }),
}))
