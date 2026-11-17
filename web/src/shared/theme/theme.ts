import { create } from 'zustand'
import { persist } from 'zustand/middleware'

export const themes = ['light', 'dark', 'system'] as const
export type Theme = (typeof themes)[number]

interface ThemeState {
  theme: Theme
  setTheme: (theme: Theme) => void
}

/** The user's color-scheme preference, remembered in this browser. */
export const useThemeStore = create<ThemeState>()(
  persist((set) => ({ theme: 'system', setTheme: (theme) => set({ theme }) }), { name: 'nsh-theme' }),
)

// tokens.css reads data-theme; "system" means no attribute, so the OS decides.
function apply(theme: Theme) {
  const root = document.documentElement
  if (theme === 'system') delete root.dataset.theme
  else root.dataset.theme = theme
}

apply(useThemeStore.getState().theme)

useThemeStore.subscribe((state) => {
  // Suppress transitions for one frame so the whole page changes at once.
  const root = document.documentElement
  root.dataset.themeSwitching = ''
  apply(state.theme)
  requestAnimationFrame(() => requestAnimationFrame(() => delete root.dataset.themeSwitching))
})
