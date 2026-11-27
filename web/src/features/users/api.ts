import { keepPreviousData, queryOptions, useMutation, useQueryClient } from '@tanstack/react-query'
import { api, unwrap, type ListParams, type Schema } from '@/shared/api'

export type User = Schema<'User'>

/** Query keys for this feature, in one place so invalidation never misses. */
const keys = {
  all: ['users'] as const,
  list: (params: ListParams) => ['users', 'list', params] as const,
  roles: ['roles'] as const,
}

export const usersQuery = (params: ListParams) =>
  queryOptions({
    queryKey: keys.list(params),
    queryFn: () => unwrap(api.GET('/users', { params: { query: params } })),
    // Keep the current page on screen while the next one loads.
    placeholderData: keepPreviousData,
  })

export const rolesQuery = queryOptions({
  queryKey: keys.roles,
  queryFn: () => unwrap(api.GET('/roles')),
  staleTime: Infinity, // roles only change with a new server version
})

export function useCreateUser() {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: (body: Schema<'CreateUserRequest'>) => unwrap(api.POST('/users', { body })),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: keys.all }),
  })
}

export function useUpdateUser() {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: ({ id, body }: { id: number; body: Schema<'UpdateUserRequest'> }) =>
      unwrap(api.PATCH('/users/{id}', { params: { path: { id } }, body })),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: keys.all })
      void queryClient.invalidateQueries({ queryKey: ['shares'] })
    },
  })
}

export function useDeleteUser() {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: (id: number) => api.DELETE('/users/{id}', { params: { path: { id } } }),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: keys.all }),
  })
}
