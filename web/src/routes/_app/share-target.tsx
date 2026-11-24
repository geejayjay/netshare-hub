import { createFileRoute } from '@tanstack/react-router'
import { ShareTargetPage } from '@/features/files'

export const Route = createFileRoute('/_app/share-target')({
  component: ShareTargetPage,
})
