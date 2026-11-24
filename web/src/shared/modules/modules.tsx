import { queryOptions } from '@tanstack/react-query'
import { createContext, useContext, type ReactNode } from 'react'
import { api, unwrap, type Schema } from '@/shared/api'

export type Modules = Schema<'Modules'>
export type ModuleKey = keyof Modules

const defaultModules: Modules = {
  fileManager: true,
  activityLog: true,
  ransomwareGuard: true,
  bruteForce: true,
  discovery: true,
  sessions: true,
  backup: true,
  nfs: false,
  replication: false,
  blockStorage: false,
}

export const modulesQuery = queryOptions({
  queryKey: ['system', 'modules'],
  queryFn: () => unwrap(api.GET('/system/modules')),
  staleTime: 5 * 60_000,
})

const ModulesContext = createContext<Modules>(defaultModules)

export function ModulesProvider({ modules, children }: { modules?: Modules; children: ReactNode }) {
  return <ModulesContext.Provider value={modules ?? defaultModules}>{children}</ModulesContext.Provider>
}

export function useModules(): Modules {
  return useContext(ModulesContext)
}

/** Returns true if a module (or at least one module in an array) is enabled on the server. */
export function isModuleEnabled(modules: Modules, key?: ModuleKey | ModuleKey[]): boolean {
  if (!key) return true
  if (Array.isArray(key)) return key.some((k) => Boolean(modules[k]))
  return Boolean(modules[key])
}
