import { createFileRoute } from '@tanstack/react-router'
import { TroubleshooterPage } from '@/features/troubleshooter'

export const Route = createFileRoute('/_app/troubleshooter')({
  component: TroubleshooterPage,
})
