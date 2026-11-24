import { queryOptions, useMutation, useQueryClient } from '@tanstack/react-query'
import { API_BASE, api, getCsrfToken, unwrap, type Schema } from '@/shared/api'

export type SambaSettings = Schema<'ServerSettings'>
export type DiscoveryStatus = Schema<'DiscoveryStatus'>
export type DiscoveryService = Schema<'DiscoveryService'>
export type DiscoveryUpdate = Schema<'DiscoveryUpdate'>
export type TaskStatus = Schema<'Status'>
export type UpdateTaskRequest = Schema<'UpdateRequest'>
export type RestoreRequest = Schema<'RestoreRequest'>
export type BackupSummary = Schema<'Summary'>

export const sambaSettingsQuery = queryOptions({
  queryKey: ['settings', 'samba'],
  queryFn: () => unwrap(api.GET('/settings/samba')),
})

export const discoveryQuery = queryOptions({
  queryKey: ['settings', 'discovery'],
  queryFn: () => unwrap(api.GET('/settings/discovery')),
})

export const systemTasksQuery = queryOptions({
  queryKey: ['system', 'tasks'],
  queryFn: () => unwrap(api.GET('/system/tasks')),
})

export function useUpdateSambaSettings() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (body: SambaSettings) => unwrap(api.PUT('/settings/samba', { body })),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['settings', 'samba'] }),
  })
}

export function useUpdateDiscovery() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (body: DiscoveryUpdate) => unwrap(api.PUT('/settings/discovery', { body })),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['settings', 'discovery'] }),
  })
}

export function useUpdateTask() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: ({ name, body }: { name: string; body: UpdateTaskRequest }) =>
      unwrap(api.PUT('/system/tasks/{name}', { params: { path: { name } }, body })),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['system', 'tasks'] }),
  })
}

export function useRunTaskNow() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (name: string) => unwrap(api.POST('/system/tasks/{name}/run', { params: { path: { name } } })),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['system', 'tasks'] }),
  })
}

export function useResetTask() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (name: string) => unwrap(api.POST('/system/tasks/{name}/reset', { params: { path: { name } } })),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['system', 'tasks'] }),
  })
}

export function useRestoreBackup() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (body: RestoreRequest) => unwrap(api.POST('/system/restore', { body })),
    onSuccess: (summary) => {
      if (summary.applied) {
        void qc.invalidateQueries()
      }
    },
  })
}

/** Downloads an encrypted configuration backup file as a browser download. */
export async function downloadEncryptedBackup(passphrase: string, smbPasswords: boolean): Promise<string> {
  const headers: Record<string, string> = { 'Content-Type': 'application/json' }
  const csrf = getCsrfToken()
  if (csrf) headers['X-CSRF-Token'] = csrf

  const res = await fetch(`${API_BASE}/system/backup`, {
    method: 'POST',
    credentials: 'include',
    headers,
    body: JSON.stringify({ passphrase, smbPasswords }),
  })
  if (!res.ok) {
    let msg = `Backup failed (${res.status})`
    try {
      const problem = (await res.json()) as { detail?: string; title?: string }
      msg = problem.detail || problem.title || msg
    } catch {
      // keep fallback message
    }
    throw new Error(msg)
  }

  const disp = res.headers.get('Content-Disposition') ?? ''
  const match = disp.match(/filename="([^"]+)"/)
  const filename = match?.[1] ?? `netshare-hub-backup-${new Date().toISOString().slice(0, 10)}.nshbackup`
  const blob = await res.blob()
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = filename
  document.body.appendChild(a)
  a.click()
  a.remove()
  URL.revokeObjectURL(url)
  return filename
}
