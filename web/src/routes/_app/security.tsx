import { createFileRoute } from '@tanstack/react-router'
import { SecurityPage } from '@/features/security'

export const Route = createFileRoute('/_app/security')({
  component: SecurityPage,
})
