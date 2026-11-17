import type { ReactNode } from 'react'
import './shell.css'

/**
 * The available arrangements. Which one the app uses is chosen in code
 * (src/app/shell.ts). To add one: add a name here and one block in shell.css.
 */
export const layoutVariants = ['sidebar-left', 'sidebar-right', 'topbar'] as const
export type LayoutVariant = (typeof layoutVariants)[number]

interface ShellProps {
  variant: LayoutVariant
  /** Sidebar shows icons only (desktop). */
  navCollapsed?: boolean
  /** Navigation drawer is open (small screens). */
  navOpen?: boolean
  onNavClose?: () => void
  /** Logo and product name. */
  brand: ReactNode
  /** Navigation links. */
  nav: ReactNode
  /** Pinned to the bottom of the navigation, e.g. the account menu. */
  navFooter?: ReactNode
  /** Header content: nav toggle, breadcrumb... */
  header: ReactNode
  /** The page. */
  children: ReactNode
}

/**
 * The frame every signed-in page lives in. It only knows about slots; where
 * each slot sits is decided by `variant` in CSS.
 *
 * Slot contract: any element inside the nav marked `data-nav-label` is hidden
 * when the sidebar is collapsed to icons. Slot content uses that instead of
 * knowing about the collapsed state.
 */
export function Shell({ variant, navCollapsed, navOpen, onNavClose, brand, nav, navFooter, header, children }: ShellProps) {
  return (
    <div className="shell" data-layout={variant} data-nav-collapsed={navCollapsed} data-nav-open={navOpen}>
      <aside className="shell-nav">
        <div className="shell-brand">{brand}</div>
        <div className="shell-nav-body">{nav}</div>
        {navFooter && <div className="shell-nav-footer">{navFooter}</div>}
      </aside>
      <div className="shell-backdrop" onClick={onNavClose} aria-hidden="true" />
      <header className="shell-header">
        <div className="shell-brand">{brand}</div>
        {header}
      </header>
      <main className="shell-main">
        <div className="shell-main-inner">{children}</div>
      </main>
    </div>
  )
}
