import { createContext, useContext, useMemo, type ReactNode } from 'react'
import type { Schema } from '@/shared/api'

export type Permission = Schema<'Permission'>
export type SessionUser = Schema<'User'>

/**
 * The current login, as shared code sees it. Shared code and layouts read the
 * session through this context and never import the auth feature; the auth
 * feature is what fills it in.
 */
export interface Session {
  user: SessionUser
  permissions: readonly Permission[]
  /** True when the user holds the permission. UI only: the server checks every request. */
  can: (permission: Permission) => boolean
}

const SessionContext = createContext<Session | null>(null)

export function SessionProvider({
  user,
  permissions,
  children,
}: {
  user: SessionUser
  permissions: readonly Permission[]
  children: ReactNode
}) {
  const session = useMemo<Session>(() => {
    const granted = new Set(permissions)
    return { user, permissions, can: (p) => granted.has(p) }
  }, [user, permissions])

  return <SessionContext value={session}>{children}</SessionContext>
}

/** The current session. Only call inside the authenticated part of the app. */
export function useSession(): Session {
  const session = useContext(SessionContext)
  if (!session) throw new Error('useSession must be used inside <SessionProvider>')
  return session
}

/** True when the current user holds the permission. */
export function usePermission(permission: Permission): boolean {
  return useSession().can(permission)
}
