import { createFileRoute } from '@tanstack/react-router'
import { ReplicationPage } from '@/features/replication'

export const Route = createFileRoute('/_app/replication')({
  component: ReplicationPage,
})
