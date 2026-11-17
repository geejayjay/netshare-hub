import { Link, useRouterState } from '@tanstack/react-router'
import { ChevronRight, PanelLeft } from 'lucide-react'
import { Fragment, useState, type ReactNode } from 'react'
import { useSession } from '@/shared/session/session'
import { Button, LogoMark } from '@/shared/ui'
import { useLayoutStore } from './layout-store'
import { Shell, type LayoutVariant } from './shell/Shell'
import type { NavItem } from './types'

interface AppLayoutProps {
  /** Which arrangement to use. A code choice, see src/app/shell.ts. */
  variant: LayoutVariant
  /** Navigation entries. Entries the user has no permission for are hidden. */
  nav: NavItem[]
  /** Pinned to the bottom of the navigation, e.g. the account menu. */
  navFooter?: ReactNode
  children: ReactNode
}

/**
 * The layout for signed-in pages. It fills the Shell's slots and knows nothing
 * about any feature: navigation and the account menu are passed in.
 */
export function AppLayout({ variant, nav, navFooter, children }: AppLayoutProps) {
  const { navCollapsed, toggleNavCollapsed } = useLayoutStore()
  const [navOpen, setNavOpen] = useState(false)
  const { can } = useSession()

  const visible = nav.filter((item) => !item.permission || can(item.permission))
  const current = useCurrentItem(visible)

  // One button: opens the drawer on small screens, collapses the sidebar on large ones.
  const toggleNav = () => {
    if (window.matchMedia('(min-width: 48rem)').matches) toggleNavCollapsed()
    else setNavOpen((open) => !open)
  }

  return (
    <Shell
      variant={variant}
      navCollapsed={navCollapsed}
      navOpen={navOpen}
      onNavClose={() => setNavOpen(false)}
      brand={<Brand />}
      nav={
        <nav className="shell-nav-list" aria-label="Main">
          {visible.map((item, i) => (
            <Fragment key={item.to}>
              {item.group && item.group !== visible[i - 1]?.group && (
                <div className="shell-nav-section">{item.group}</div>
              )}
              <Link
                to={item.to}
                className="shell-nav-link"
                activeOptions={{ exact: item.to === '/' }}
                onClick={() => setNavOpen(false)}
                title={item.label}
              >
                <item.icon />
                <span data-nav-label>{item.label}</span>
              </Link>
            </Fragment>
          ))}
        </nav>
      }
      navFooter={navFooter}
      header={
        <>
          <Button
            variant="ghost"
            size="icon"
            className="shell-nav-toggle size-8"
            onClick={toggleNav}
            aria-label="Toggle navigation"
          >
            <PanelLeft className="size-4" />
          </Button>
          {current && (
            <nav aria-label="Breadcrumb" className="flex min-w-0 items-center gap-1.5 text-sm">
              {current.group && (
                <>
                  <span className="text-fg-subtle max-sm:hidden">{current.group}</span>
                  <ChevronRight className="size-3.5 text-fg-subtle max-sm:hidden" />
                </>
              )}
              <span className="truncate font-medium">{current.label}</span>
            </nav>
          )}
        </>
      }
    >
      {children}
    </Shell>
  )
}

/** The nav entry for the current page (exact match for "/", prefix match otherwise). */
function useCurrentItem(items: NavItem[]) {
  const path = useRouterState({ select: (s) => s.location.pathname })
  return items.find((item) => (item.to === '/' ? path === '/' : path.startsWith(String(item.to))))
}

/** Logo tile + product name. */
export function Brand() {
  return (
    <>
      <LogoMark />
      <span data-nav-label className="flex flex-col leading-tight">
        <span className="text-[0.9375rem] font-semibold tracking-tight">NetShare Hub</span>
        <span className="text-[0.6875rem] font-medium text-fg-subtle">Network shares</span>
      </span>
    </>
  )
}
