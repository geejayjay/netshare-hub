import { createFileRoute } from '@tanstack/react-router'
import { MySharesPage } from '@/features/portal'

export const Route = createFileRoute('/_app/my-shares')({
  component: MySharesPage,
})
