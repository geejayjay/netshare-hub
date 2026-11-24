import type { LinkProps } from '@tanstack/react-router'
import type { LucideIcon } from 'lucide-react'
import type { ModuleKey } from '@/shared/modules/modules'
import type { Permission } from '@/shared/session/session'

/** One entry in the main navigation. The list itself lives in src/app/shell.ts. */
export interface NavItem {
  to: LinkProps['to']
  label: string
  icon: LucideIcon
  /** Section heading. Consecutive items with the same group are shown together. */
  group?: string
  /** Hide the entry unless the user holds this permission. */
  permission?: Permission
  /** Hide the entry unless the server has this optional module (or any of them) enabled. */
  module?: ModuleKey | ModuleKey[]
}
