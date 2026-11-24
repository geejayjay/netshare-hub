import { queryOptions, useMutation, useQueryClient } from '@tanstack/react-query'
import { api, unwrap, type Schema } from '@/shared/api'

export type ReplicationStatus = Schema<'ReplicationStatus'>
export type ReplicationJob = Schema<'ReplicationJob'>
export type ReplicationJobRequest = Schema<'ReplicationJobRequest'>
export type ReplicationJobPatch = Schema<'ReplicationJobPatch'>
export type ReplicationRun = Schema<'ReplicationRun'>
export type ReplicationPeer = Schema<'ReplicationPeer'>
export type ReplicationPeerRequest = Schema<'ReplicationPeerRequest'>
export type ReplicationReceiver = Schema<'ReplicationReceiver'>
export type ReplicationReceiverRequest = Schema<'ReplicationReceiverRequest'>
export type ReplicationReceiverCode = Schema<'ReplicationReceiverCode'>

export const replicationStatusQuery = queryOptions({
  queryKey: ['replication', 'status'],
  queryFn: () => unwrap(api.GET('/replication/status')),
})

export const replicationJobsQuery = queryOptions({
  queryKey: ['replication', 'jobs'],
  queryFn: () => unwrap(api.GET('/replication/jobs')),
})

export const replicationJobRunsQuery = (id: number) =>
  queryOptions({
    queryKey: ['replication', 'jobs', id, 'runs'],
    queryFn: () => unwrap(api.GET('/replication/jobs/{id}/runs', { params: { path: { id } } })),
    enabled: id > 0,
  })

export const replicationPeersQuery = queryOptions({
  queryKey: ['replication', 'peers'],
  queryFn: () => unwrap(api.GET('/replication/peers')),
})

export const replicationReceiversQuery = queryOptions({
  queryKey: ['replication', 'receivers'],
  queryFn: () => unwrap(api.GET('/replication/receivers')),
})

export function useCreateReplicationJob() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (body: ReplicationJobRequest) => unwrap(api.POST('/replication/jobs', { body })),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['replication', 'jobs'] }),
  })
}

export function useUpdateReplicationJob() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: ({ id, body }: { id: number; body: ReplicationJobPatch }) =>
      unwrap(api.PATCH('/replication/jobs/{id}', { params: { path: { id } }, body })),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['replication', 'jobs'] }),
  })
}

export function useDeleteReplicationJob() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (id: number) => unwrap(api.DELETE('/replication/jobs/{id}', { params: { path: { id } } })),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['replication', 'jobs'] }),
  })
}

export function useRunReplicationJob() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (id: number) => unwrap(api.POST('/replication/jobs/{id}/run', { params: { path: { id } } })),
    onSuccess: (_d, id) => {
      void qc.invalidateQueries({ queryKey: ['replication', 'jobs'] })
      void qc.invalidateQueries({ queryKey: ['replication', 'jobs', id, 'runs'] })
      void qc.invalidateQueries({ queryKey: ['jobs'] })
    },
  })
}

export function usePreviewReplicationJob() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (id: number) => unwrap(api.POST('/replication/jobs/{id}/preview', { params: { path: { id } } })),
    onSuccess: (_d, id) => {
      void qc.invalidateQueries({ queryKey: ['replication', 'jobs', id, 'runs'] })
      void qc.invalidateQueries({ queryKey: ['jobs'] })
    },
  })
}

export function useCreatePeer() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (body: ReplicationPeerRequest) => unwrap(api.POST('/replication/peers', { body })),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['replication', 'peers'] }),
  })
}

export function useDeletePeer() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (id: number) => unwrap(api.DELETE('/replication/peers/{id}', { params: { path: { id } } })),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['replication', 'peers'] }),
  })
}

export function useCreateReceiver() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (body: ReplicationReceiverRequest) => unwrap(api.POST('/replication/receivers', { body })),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['replication', 'receivers'] }),
  })
}

export function useDeleteReceiver() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (id: number) => unwrap(api.DELETE('/replication/receivers/{id}', { params: { path: { id } } })),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['replication', 'receivers'] }),
  })
}

export function useNewPairingCode() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (id: number) => unwrap(api.POST('/replication/receivers/{id}/pairing-code', { params: { path: { id } } })),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['replication', 'receivers'] }),
  })
}
