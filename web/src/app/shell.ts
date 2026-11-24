import {
  Activity,
  ClipboardList,
  Database,
  FolderClosed,
  FolderOpen,
  FolderSync,
  Gauge,
  HardDrive,
  LayoutDashboard,
  Monitor,
  PieChart,
  ScrollText,
  Settings,
  Shield,
  Stethoscope,
  Users,
  UsersRound,
} from 'lucide-react'
import type { LayoutVariant, NavItem } from '@/layouts'

/**
 * How the signed-in app is framed. This is a code choice, not a user setting:
 * change it here to move the navigation (see src/layouts/shell for the options).
 */
export const layoutVariant: LayoutVariant = 'sidebar-left'

/**
 * The main navigation: the single list the layout renders.
 * Entries are automatically hidden when the user lacks permission or the server
 * has the corresponding optional module turned off in config.toml.
 */
export const navigation: NavItem[] = [
  { to: '/', label: 'Dashboard', icon: LayoutDashboard, group: 'Overview' },
  { to: '/my-shares', label: 'My Shares', icon: HardDrive, group: 'Overview' },
  { to: '/files', label: 'File Manager', icon: FolderClosed, group: 'Storage', module: 'fileManager' },
  { to: '/shares', label: 'Shares', icon: FolderOpen, group: 'Storage', permission: 'shares:read' },
  { to: '/storage', label: 'Storage Analyzer', icon: PieChart, group: 'Storage', permission: 'system:read' },
  {
    to: '/replication',
    label: 'Replication',
    icon: FolderSync,
    group: 'Storage',
    permission: 'replication:manage',
    module: 'replication',
  },
  {
    to: '/block',
    label: 'Block Storage',
    icon: Database,
    group: 'Storage',
    permission: 'block:manage',
    module: 'blockStorage',
  },
  { to: '/sessions', label: 'Sessions', icon: Monitor, group: 'Security & Insight', permission: 'system:read', module: 'sessions' },
  { to: '/activity', label: 'Activity Feed', icon: Activity, group: 'Security & Insight', permission: 'audit:read', module: 'activityLog' },
  { to: '/audit', label: 'Audit Log', icon: ClipboardList, group: 'Security & Insight', permission: 'audit:read' },
  {
    to: '/security',
    label: 'Security & Guard',
    icon: Shield,
    group: 'Security & Insight',
    permission: 'settings:read',
    module: ['ransomwareGuard', 'bruteForce'],
  },
  { to: '/troubleshooter', label: 'Troubleshooter', icon: Stethoscope, group: 'Security & Insight', permission: 'system:read' },
  { to: '/speedtest', label: 'Speed Test', icon: Gauge, group: 'Security & Insight', permission: 'system:read' },
  { to: '/logs', label: 'Server Logs', icon: ScrollText, group: 'Security & Insight', permission: 'system:read' },
  { to: '/users', label: 'Users', icon: Users, group: 'Administration', permission: 'users:read' },
  { to: '/groups', label: 'Groups', icon: UsersRound, group: 'Administration', permission: 'groups:read' },
  { to: '/settings', label: 'Settings', icon: Settings, group: 'Administration', permission: 'settings:read' },
]
