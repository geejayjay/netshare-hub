import type { ReactNode } from 'react'
import { ThemeSwitcher } from '@/shared/components'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/shared/ui'
import { Brand } from './AppLayout'

/** The layout for signed-out pages (login, first-run setup): one centered card. */
export function AuthLayout({
  title,
  description,
  children,
}: {
  title: string
  description?: string
  children: ReactNode
}) {
  return (
    <div className="relative grid min-h-dvh place-items-center overflow-hidden bg-chrome p-4">
      <div
        aria-hidden="true"
        className="pointer-events-none absolute inset-x-0 -top-48 mx-auto size-[44rem] rounded-full bg-primary/14 blur-3xl"
      />
      <div className="relative flex w-full max-w-[23.5rem] flex-col gap-6">
        <div className="flex items-center gap-2.5">
          <Brand />
          <ThemeSwitcher className="ml-auto" />
        </div>
        <Card className="border-border-strong/80 shadow-pop">
          <CardHeader className="p-6 pb-0">
            <CardTitle className="text-lg tracking-tight">{title}</CardTitle>
            {description && <CardDescription>{description}</CardDescription>}
          </CardHeader>
          <CardContent className="p-6">{children}</CardContent>
        </Card>
      </div>
    </div>
  )
}
