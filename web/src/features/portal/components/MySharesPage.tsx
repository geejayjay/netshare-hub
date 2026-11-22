import { useQuery } from '@tanstack/react-query'
import { Check, Copy, FolderOpen } from 'lucide-react'
import { useState } from 'react'
import { EmptyState, ErrorState, LoadingState, PageHeader } from '@/shared/components'
import { useSession } from '@/shared/session/session'
import { Badge, Button, Card } from '@/shared/ui'
import { mySharesQuery, type MyShare } from '../api'

type OsTab = 'windows' | 'macos' | 'linux'

function connectionDetails(os: OsTab, host: string, shareName: string, username: string) {
  switch (os) {
    case 'windows':
      return {
        address: `\\\\${host}\\${shareName}`,
        steps: `Open File Explorer, paste \\\\${host}\\${shareName} in the address bar, and sign in as ${username}.`,
      }
    case 'macos':
      return {
        address: `smb://${host}/${shareName}`,
        steps: `In Finder press Cmd+K (Connect to Server), enter smb://${host}/${shareName}, and sign in as ${username}.`,
      }
    case 'linux':
      return {
        address: `smb://${host}/${shareName}`,
        steps: `Use your file manager with smb://${host}/${shareName}, or run: mount -t cifs //${host}/${shareName} /mnt/${shareName} -o username=${username}`,
      }
  }
}

function ShareCard({ share, host, username }: { share: MyShare; host: string; username: string }) {
  const [os, setOs] = useState<OsTab>('windows')
  const [copied, setCopied] = useState(false)
  const info = connectionDetails(os, host, share.name, username)

  function copyAddress() {
    void navigator.clipboard?.writeText(info.address).then(() => {
      setCopied(true)
      window.setTimeout(() => setCopied(false), 1500)
    })
  }

  return (
    <Card className="p-4">
      <div className="flex flex-wrap items-start justify-between gap-2">
        <div className="flex items-center gap-2.5">
          <FolderOpen className="size-5 text-accent" />
          <div>
            <h2 className="text-base font-semibold text-fg">{share.name}</h2>
            {share.comment && <p className="text-sm text-fg-muted">{share.comment}</p>}
          </div>
        </div>
        <Badge tone={share.level === 'write' ? 'info' : 'neutral'}>
          {share.level === 'write' ? 'Read & write' : 'Read only'}
        </Badge>
      </div>

      <div className="mt-4 flex flex-col gap-3 border-t border-border pt-3">
        <div className="flex flex-wrap items-center gap-1.5">
          {(['windows', 'macos', 'linux'] as const).map((tab) => (
            <Button
              key={tab}
              size="sm"
              variant={os === tab ? 'primary' : 'ghost'}
              onClick={() => setOs(tab)}
            >
              {tab === 'windows' ? 'Windows' : tab === 'macos' ? 'macOS' : 'Linux'}
            </Button>
          ))}
        </div>

        <div className="flex items-center justify-between gap-2 rounded-md bg-surface-muted px-3 py-2">
          <code className="truncate text-xs text-fg">{info.address}</code>
          <Button size="icon" variant="ghost" aria-label={`Copy ${share.name} address`} onClick={copyAddress}>
            {copied ? <Check className="size-4 text-success" /> : <Copy className="size-4" />}
          </Button>
        </div>

        <p className="text-xs text-fg-muted">{info.steps}</p>
      </div>
    </Card>
  )
}

export function MySharesPage() {
  const shares = useQuery(mySharesQuery)
  const session = useSession()
  const host = typeof window !== 'undefined' ? window.location.hostname : 'server'

  return (
    <>
      <PageHeader
        title="My Shares"
        description="Network folders you can connect to and how to mount them."
      />

      {shares.isLoading ? (
        <LoadingState />
      ) : shares.error ? (
        <ErrorState error={shares.error} onRetry={() => void shares.refetch()} />
      ) : !shares.data?.items.length ? (
        <Card className="p-6">
          <EmptyState
            title="No shares assigned"
            description="Ask an administrator to grant your account access to a share."
          />
        </Card>
      ) : (
        <div className="grid gap-4 md:grid-cols-2">
          {shares.data.items.map((share) => (
            <ShareCard key={share.id} share={share} host={host} username={session.user.username} />
          ))}
        </div>
      )}
    </>
  )
}
