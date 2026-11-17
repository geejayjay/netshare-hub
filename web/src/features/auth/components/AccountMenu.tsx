import { ChevronsUpDown, LogOut } from 'lucide-react'
import { notify } from '@/features/notifications'
import { ThemeSwitcher } from '@/shared/components'
import { useSession } from '@/shared/session/session'
import { Menu, MenuItem, MenuRow, MenuSeparator } from '@/shared/ui'
import { useLogout } from '../api'

/** "share_manager" -> "Share manager" */
function roleLabel(role: string) {
  const text = role.replace(/_/g, ' ')
  return text.charAt(0).toUpperCase() + text.slice(1)
}

function Avatar({ name }: { name: string }) {
  return (
    <span
      aria-hidden="true"
      className="grid size-8 shrink-0 place-items-center rounded-full bg-linear-to-br from-primary-soft to-surface-3 text-[0.8125rem] font-semibold text-primary uppercase ring-1 ring-border"
    >
      {name.charAt(0)}
    </span>
  )
}

/**
 * The signed-in account, pinned to the bottom of the sidebar. Opens a menu
 * with the theme choice and log out. Text parts carry data-nav-label so the
 * layout can hide them when the sidebar is collapsed.
 */
export function AccountMenu() {
  const { user } = useSession()
  const logout = useLogout()
  const name = user.displayName || user.username

  return (
    <Menu
      triggerLabel={`Account: ${name}`}
      triggerClassName="flex w-full items-center gap-2.5 rounded-lg p-1.5 text-left transition-colors hover:bg-surface-3 aria-expanded:bg-surface-3"
      trigger={
        <>
          <Avatar name={name} />
          <span data-nav-label className="flex min-w-0 flex-1 flex-col leading-tight">
            <span className="truncate text-sm font-medium">{name}</span>
            <span className="truncate text-xs text-fg-subtle">{roleLabel(user.role)}</span>
          </span>
          <ChevronsUpDown data-nav-label className="size-4 shrink-0 text-fg-subtle" />
        </>
      }
    >
      <MenuRow>
        <Avatar name={name} />
        <span className="flex min-w-0 flex-col leading-tight">
          <span className="truncate font-medium">{name}</span>
          <span className="truncate text-xs text-fg-subtle">@{user.username}</span>
        </span>
      </MenuRow>
      <MenuSeparator />
      <MenuRow className="justify-between">
        <span className="font-medium text-fg-muted">Theme</span>
        <ThemeSwitcher />
      </MenuRow>
      <MenuSeparator />
      <MenuItem
        tone="danger"
        disabled={logout.isPending}
        onClick={() => logout.mutate(undefined, { onError: (error) => notify.error(error) })}
      >
        <LogOut />
        Log out
      </MenuItem>
    </Menu>
  )
}
