import { RefreshCw, WifiOff } from 'lucide-react'
import { useEffect, useState } from 'react'
import { Button } from '@/shared/ui'

/** Sticky offline notice shown when the browser loses connectivity to NetShare Hub. */
export function OfflineBanner() {
  const [offline, setOffline] = useState(() => (typeof navigator !== 'undefined' ? !navigator.onLine : false))

  useEffect(() => {
    const onOffline = () => setOffline(true)
    const onOnline = () => setOffline(false)
    window.addEventListener('offline', onOffline)
    window.addEventListener('online', onOnline)
    return () => {
      window.removeEventListener('offline', onOffline)
      window.removeEventListener('online', onOnline)
    }
  }, [])

  if (!offline) return null

  return (
    <div
      role="status"
      className="flex flex-wrap items-center justify-between gap-2 border-b border-warning/30 bg-warning/15 px-4 py-2 text-xs text-fg"
    >
      <div className="flex items-center gap-2 font-medium">
        <WifiOff className="size-4 text-warning" />
        <span>Can&apos;t reach NetShare Hub — you appear to be offline. Waiting for connection…</span>
      </div>
      <Button size="sm" variant="secondary" className="h-7 px-2.5 text-xs" onClick={() => window.location.reload()}>
        <RefreshCw className="size-3" />
        Retry
      </Button>
    </div>
  )
}
