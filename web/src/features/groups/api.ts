import { queryOptions, useMutation, useQueryClient } from '@tanstack/react-query'
import { api, unwrap, type Schema } from '@/shared/api'

export type Group = Schema<'Group'>
export type GroupMember = Schema<'GroupMember'>

export const groupsQuery = queryOptions({
  queryKey: ['groups'],
  queryFn: () => unwrap(api.GET('/groups')),
  staleTime: 15_000,
})

export function useCreateGroup() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (body: { name: string; description?: string; members?: number[] }) =>
      unwrap(api.POST('/groups', { body })),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ['groups'] })
    },
  })
}

export function useUpdateGroup() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: ({ id, body }: { id: number; body: { description?: string; members?: number[] } }) =>
      unwrap(api.PATCH('/groups/{id}', { params: { path: { id } }, body })),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ['groups'] })
      void qc.invalidateQueries({ queryKey: ['shares'] })
    },
  })
}

export function useDeleteGroup() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (id: number) => unwrap(api.DELETE('/groups/{id}', { params: { path: { id } } })),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ['groups'] })
      void qc.invalidateQueries({ queryKey: ['shares'] })
    },
  })
}
