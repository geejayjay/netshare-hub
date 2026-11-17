import type { ReactNode } from 'react'
import { usePermission, type Permission } from '@/shared/session/session'

/**
 * Renders children only when the user holds the permission.
 * This hides UI; it is not security. The server checks every request.
 */
export function PermissionGate({
  permission,
  fallback = null,
  children,
}: {
  permission: Permission
  fallback?: ReactNode
  children: ReactNode
}) {
  return usePermission(permission) ? children : fallback
}
