import { useQuery } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import { Check, Copy, FolderOpen, HardDrive, Terminal } from 'lucide-react'
import { useState } from 'react'
import { EmptyState, ErrorState, LoadingState, PageHeader } from '@/shared/components'
import { cn } from '@/shared/lib/cn'
import { copyToClipboard } from '@/shared/lib/clipboard'
import { useModules } from '@/shared/modules/modules'
import { useSession } from '@/shared/session/session'
import { Badge, Button, Card, InfoTip } from '@/shared/ui'
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
        steps: `In Finder press ⌘K (Connect to Server), enter smb://${host}/${shareName}, and sign in as ${username}.`,
      }
    case 'linux':
      return {
        address: `smb://${host}/${shareName}`,
        steps: `Open smb://${host}/${shareName} in your file manager, or run: mount -t cifs //${host}/${shareName} /mnt/${shareName} -o username=${username}`,
      }
  }
}

function ShareCard({ share, host, username }: { share: MyShare; host: string; username: string }) {
  const modules = useModules()
  const [os, setOs] = useState<OsTab>('windows')
  const [copied, setCopied] = useState(false)
  const info = connectionDetails(os, host, share.name, username)

  function copyAddress() {
    void copyToClipboard(info.address).then((ok) => {
      if (!ok) return
      setCopied(true)
      window.setTimeout(() => setCopied(false), 1600)
    })
  }

  return (
    <Card className="flex flex-col justify-between p-5 transition-colors hover:border-border-strong">
      <div>
        <div className="flex items-start justify-between gap-3">
          <div className="flex min-w-0 items-center gap-3">
            <span className="grid size-10 shrink-0 place-items-center rounded-xl bg-primary-soft text-primary">
              <HardDrive className="size-5" />
            </span>
            <div className="min-w-0">
              <h2 className="truncate text-base font-semibold tracking-tight text-fg">{share.name}</h2>
              {share.comment ? (
                <p className="truncate text-xs text-fg-muted">{share.comment}</p>
              ) : (
                <p className="text-xs text-fg-subtle">SMB network folder</p>
              )}
            </div>
          </div>
          <Badge tone={share.level === 'write' ? 'info' : share.level === 'upload' ? 'warning' : 'neutral'} dot>
            {share.level === 'write' ? 'Read & write' : share.level === 'upload' ? 'Drop box' : 'Read only'}
          </Badge>
        </div>

        <div className="mt-4 flex flex-col gap-3 border-t border-border pt-4">
          <div className="flex items-center justify-between gap-2">
            <div className="inline-flex rounded-lg border border-border bg-surface-2 p-0.5">
              {(['windows', 'macos', 'linux'] as const).map((tab) => (
                <button
                  key={tab}
                  type="button"
                  onClick={() => setOs(tab)}
                  className={cn(
                    'rounded-md px-2.5 py-1 text-xs font-medium transition-colors',
                    os === tab ? 'bg-surface text-fg shadow-xs ring-1 ring-border' : 'text-fg-muted hover:text-fg',
                  )}
                >
                  {tab === 'windows' ? 'Windows' : tab === 'macos' ? 'macOS' : 'Linux'}
                </button>
              ))}
            </div>
            {modules.fileManager && (
              <Link
                to="/files"
                search={{ share: share.name, path: '' }}
                className="inline-flex items-center gap-1.5 rounded-md border border-border-strong bg-surface px-2.5 py-1 text-xs font-medium text-fg shadow-xs transition-colors hover:bg-surface-2"
              >
                <FolderOpen className="size-3.5 text-primary" />
                Browse
              </Link>
            )}
          </div>

          <div className="flex items-center justify-between gap-2 rounded-lg border border-border bg-bg px-3 py-2">
            <div className="flex min-w-0 items-center gap-2">
              <Terminal className="size-3.5 shrink-0 text-fg-subtle" />
              <code className="truncate text-xs font-medium text-fg">{info.address}</code>
            </div>
            <div className="flex shrink-0 items-center gap-1">
              <InfoTip content={info.steps} label={`How to connect on ${os}`} />
              <Button size="icon" variant="ghost" className="size-7" aria-label={`Copy ${share.name} address`} onClick={copyAddress}>
                {copied ? <Check className="size-3.5 text-success" /> : <Copy className="size-3.5" />}
              </Button>
            </div>
          </div>
        </div>
      </div>
    </Card>
  )
}

export function MySharesPage() {
  const shares = useQuery(mySharesQuery)
  const session = useSession()
  const host = typeof window !== 'undefined' ? window.location.hostname : 'server'

  return (
    <div className="flex flex-col gap-5">
      <PageHeader
        title="My Shares"
        info="SMB network folders assigned to your account. Select your OS to copy the mount address or click Browse to open in the web File Manager."
      />

      {shares.isLoading ? (
        <LoadingState />
      ) : shares.error ? (
        <ErrorState error={shares.error} onRetry={() => void shares.refetch()} />
      ) : !shares.data?.items.length ? (
        <Card className="p-8">
          <EmptyState
            title="No shares assigned"
            description="Ask an administrator to grant your account access to an SMB share."
          />
        </Card>
      ) : (
        <div className="grid gap-4 md:grid-cols-2">
          {shares.data.items.map((share) => (
            <ShareCard key={share.id} share={share} host={host} username={session.user.username} />
          ))}
        </div>
      )}
    </div>
  )
}
