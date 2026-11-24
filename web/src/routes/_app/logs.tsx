import { createFileRoute } from '@tanstack/react-router'
import { LogsPage } from '@/features/logs'

export const Route = createFileRoute('/_app/logs')({
  component: LogsPage,
})
