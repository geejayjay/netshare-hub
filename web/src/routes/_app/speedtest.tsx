import { createFileRoute } from '@tanstack/react-router'
import { SpeedTestPage } from '@/features/speedtest'

export const Route = createFileRoute('/_app/speedtest')({
  component: SpeedTestPage,
})
