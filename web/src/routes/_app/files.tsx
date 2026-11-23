import { createFileRoute } from '@tanstack/react-router'
import { FilesPage } from '@/features/files'

interface FilesSearch {
  share?: string
  path?: string
}

export const Route = createFileRoute('/_app/files')({
  validateSearch: (raw: Record<string, unknown>): FilesSearch => ({
    share: typeof raw.share === 'string' && raw.share ? raw.share : undefined,
    path: typeof raw.path === 'string' && raw.path ? raw.path : undefined,
  }),
  component: FilesRoute,
})

function FilesRoute() {
  const { share = '', path = '' } = Route.useSearch()
  const navigate = Route.useNavigate()
  return (
    <FilesPage
      share={share}
      path={path}
      onNavigate={(next) =>
        void navigate({
          search: {
            share: next.share || undefined,
            path: next.path || undefined,
          },
        })
      }
    />
  )
}
