import { createFileRoute } from '@tanstack/react-router'
import { BlockPage } from '@/features/block'

export const Route = createFileRoute('/_app/block')({
  component: BlockPage,
})
