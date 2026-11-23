import { createFileRoute } from '@tanstack/react-router'
import { SessionsPage } from '@/features/sessions'

export const Route = createFileRoute('/_app/sessions')({
  component: SessionsPage,
})
