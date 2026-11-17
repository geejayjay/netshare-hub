import { create } from 'zustand'
import { persist } from 'zustand/middleware'

interface LayoutState {
  /** Sidebar shows icons only (desktop). */
  navCollapsed: boolean
  toggleNavCollapsed: () => void
}

/** Per-browser layout state. Which layout variant is used is a code choice (src/app/shell.ts), not a setting. */
export const useLayoutStore = create<LayoutState>()(
  persist(
    (set) => ({
      navCollapsed: false,
      toggleNavCollapsed: () => set((s) => ({ navCollapsed: !s.navCollapsed })),
    }),
    { name: 'nsh-layout', partialize: (s) => ({ navCollapsed: s.navCollapsed }) },
  ),
)
