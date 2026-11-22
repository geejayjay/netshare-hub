import { FolderOpen, HardDrive, LayoutDashboard, Users } from 'lucide-react'
import type { LayoutVariant, NavItem } from '@/layouts'

/**
 * How the signed-in app is framed. This is a code choice, not a user setting:
 * change it here to move the navigation (see src/layouts/shell for the options).
 */
export const layoutVariant: LayoutVariant = 'sidebar-left'

/**
 * The main navigation: the single list the layout renders.
 * To add a page: create its route, then add one entry here.
 */
export const navigation: NavItem[] = [
  { to: '/', label: 'Dashboard', icon: LayoutDashboard, group: 'Overview' },
  { to: '/my-shares', label: 'My Shares', icon: HardDrive, group: 'Overview' },
  { to: '/shares', label: 'Shares', icon: FolderOpen, group: 'Storage', permission: 'shares:read' },
  { to: '/users', label: 'Users', icon: Users, group: 'Administration', permission: 'users:read' },
]
