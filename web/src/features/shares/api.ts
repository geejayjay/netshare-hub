import { keepPreviousData, queryOptions, useMutation, useQueryClient } from '@tanstack/react-query'
import { api, unwrap, type ListParams, type Schema } from '@/shared/api'

export type Share = Schema<'Share'>
export type ShareAccessInput = Schema<'ShareAccessInput'>

const keys = {
  all: ['shares'] as const,
  list: (params: ListParams) => ['shares', 'list', params] as const,
  users: ['shares', 'users'] as const,
}

export const sharesQuery = (params: ListParams) =>
  queryOptions({
    queryKey: keys.list(params),
    queryFn: () => unwrap(api.GET('/shares', { params: { query: params } })),
    placeholderData: keepPreviousData,
  })

export const shareUsersQuery = queryOptions({
  queryKey: keys.users,
  queryFn: () => unwrap(api.GET('/users', { params: { query: { pageSize: 100 } } })),
})

export function useCreateShare() {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: (body: Schema<'CreateShareRequest'>) => unwrap(api.POST('/shares', { body })),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: keys.all }),
  })
}

export function useDeleteShare() {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: (id: number) => unwrap(api.DELETE('/shares/{id}', { params: { path: { id } } })),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: keys.all }),
  })
}
