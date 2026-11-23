import {
  Activity,
  FolderClosed,
  FolderOpen,
  HardDrive,
  LayoutDashboard,
  Monitor,
  Settings,
  Shield,
  Users,
} from 'lucide-react'
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
  { to: '/files', label: 'File Manager', icon: FolderClosed, group: 'Storage' },
  { to: '/shares', label: 'Shares', icon: FolderOpen, group: 'Storage', permission: 'shares:read' },
  { to: '/sessions', label: 'Sessions', icon: Monitor, group: 'Security & Insight', permission: 'system:read' },
  { to: '/activity', label: 'Activity Feed', icon: Activity, group: 'Security & Insight', permission: 'audit:read' },
  { to: '/security', label: 'Security & Guard', icon: Shield, group: 'Security & Insight', permission: 'settings:read' },
  { to: '/users', label: 'Users', icon: Users, group: 'Administration', permission: 'users:read' },
  { to: '/settings', label: 'Settings', icon: Settings, group: 'Administration', permission: 'settings:read' },
]
