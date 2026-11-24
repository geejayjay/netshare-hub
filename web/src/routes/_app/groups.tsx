import { createFileRoute } from '@tanstack/react-router'
import { GroupsPage } from '@/features/groups'

export const Route = createFileRoute('/_app/groups')({
  component: GroupsPage,
})
