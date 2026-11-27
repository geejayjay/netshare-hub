import { useQuery } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import { Check, Copy, FolderOpen, HardDrive, LayoutGrid, List, Terminal } from 'lucide-react'
import { useMemo, useState } from 'react'
import { EmptyState, ErrorState, LoadingState, PageHeader, SearchInput } from '@/shared/components'
import { copyToClipboard } from '@/shared/lib/clipboard'
import { cn } from '@/shared/lib/cn'
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

function levelBadge(level: MyShare['level']) {
  return (
    <Badge tone={level === 'write' ? 'info' : level === 'upload' ? 'warning' : 'neutral'} dot>
      {level === 'write' ? 'Read & write' : level === 'upload' ? 'Drop box' : 'Read only'}
    </Badge>
  )
}

function ShareCard({ share, os, host, username }: { share: MyShare; os: OsTab; host: string; username: string }) {
  const modules = useModules()
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
    <Card className="flex flex-col justify-between p-4 transition-colors hover:border-border-strong">
      <div className="flex items-start justify-between gap-3">
        <div className="flex min-w-0 items-center gap-3">
          <span className="grid size-9 shrink-0 place-items-center rounded-xl bg-primary-soft text-primary">
            <HardDrive className="size-4.5" />
          </span>
          <div className="min-w-0">
            <h2 className="truncate text-sm font-semibold tracking-tight text-fg">{share.name}</h2>
            <p className="truncate text-xs text-fg-muted">{share.comment || 'SMB network folder'}</p>
          </div>
        </div>
        <div className="shrink-0">{levelBadge(share.level)}</div>
      </div>

      <div className="mt-3.5 flex items-center gap-2 border-t border-border pt-3">
        <div className="flex min-w-0 flex-1 items-center justify-between gap-2 rounded-lg border border-border bg-bg px-2.5 py-1.5">
          <div className="flex min-w-0 items-center gap-1.5">
            <Terminal className="size-3.5 shrink-0 text-fg-subtle" />
            <code className="truncate text-xs font-medium text-fg">{info.address}</code>
          </div>
          <div className="flex shrink-0 items-center gap-0.5">
            <InfoTip content={info.steps} label={`How to connect on ${os}`} />
            <Button
              size="icon"
              variant="ghost"
              className="size-6"
              aria-label={`Copy ${share.name} address`}
              onClick={copyAddress}
            >
              {copied ? <Check className="size-3.5 text-success" /> : <Copy className="size-3.5" />}
            </Button>
          </div>
        </div>
        {modules.fileManager && (
          <Link
            to="/files"
            search={{ share: share.name, path: '' }}
            className="inline-flex h-8 shrink-0 items-center gap-1.5 rounded-lg border border-border-strong bg-surface px-2.5 text-xs font-medium text-fg shadow-xs transition-colors hover:bg-surface-2"
          >
            <FolderOpen className="size-3.5 text-primary" />
            Browse
          </Link>
        )}
      </div>
    </Card>
  )
}

function ShareCompactRow({ share, os, host, username }: { share: MyShare; os: OsTab; host: string; username: string }) {
  const modules = useModules()
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
    <div className="flex flex-wrap items-center justify-between gap-3 px-4 py-2.5 transition-colors hover:bg-surface-2/40">
      <div className="flex min-w-0 flex-1 items-center gap-3">
        <HardDrive className="size-4 shrink-0 text-primary" />
        <div className="min-w-0">
          <div className="flex items-center gap-2">
            <span className="truncate text-sm font-medium text-fg">{share.name}</span>
            {levelBadge(share.level)}
          </div>
          {share.comment && <p className="truncate text-xs text-fg-subtle">{share.comment}</p>}
        </div>
      </div>

      <div className="flex shrink-0 items-center gap-2">
        <code className="hidden max-w-xs truncate rounded-md border border-border bg-bg px-2 py-1 text-xs text-fg-muted sm:inline-block">
          {info.address}
        </code>
        <InfoTip content={info.steps} label={`How to connect on ${os}`} />
        <Button size="sm" variant="secondary" className="h-7 px-2 text-xs" onClick={copyAddress}>
          {copied ? <Check className="size-3.5 text-success" /> : <Copy className="size-3.5" />}
          {copied ? 'Copied' : 'Copy'}
        </Button>
        {modules.fileManager && (
          <Link
            to="/files"
            search={{ share: share.name, path: '' }}
            className="inline-flex h-7 items-center gap-1 rounded-md border border-border-strong bg-surface px-2 text-xs font-medium text-fg hover:bg-surface-2"
          >
            <FolderOpen className="size-3.5 text-primary" />
            Browse
          </Link>
        )}
      </div>
    </div>
  )
}

export function MySharesPage() {
  const shares = useQuery(mySharesQuery)
  const session = useSession()
  const host = typeof window !== 'undefined' ? window.location.hostname : 'server'
  const [os, setOs] = useState<OsTab>('windows')
  const [search, setSearch] = useState('')
  const [view, setView] = useState<'cards' | 'list'>('cards')

  const items = useMemo(() => shares.data?.items ?? [], [shares.data])
  const filtered = useMemo(() => {
    const q = search.trim().toLowerCase()
    if (!q) return items
    return items.filter(
      (s) => s.name.toLowerCase().includes(q) || (s.comment ?? '').toLowerCase().includes(q),
    )
  }, [items, search])

  return (
    <div className="flex flex-col gap-5">
      <PageHeader
        title="My Shares"
        info="SMB network folders assigned to your account. Switch OS once to copy mount addresses or click Browse to open in the File Manager."
        actions={
          items.length > 0 ? (
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
          ) : undefined
        }
      />

      {shares.isLoading ? (
        <LoadingState />
      ) : shares.error ? (
        <ErrorState error={shares.error} onRetry={() => void shares.refetch()} />
      ) : items.length === 0 ? (
        <Card className="p-8">
          <EmptyState
            title="No shares assigned"
            description="Ask an administrator to grant your account access to an SMB share."
          />
        </Card>
      ) : (
        <>
          <div className="flex flex-wrap items-center justify-between gap-3">
            <div className="flex items-center gap-3">
              <div className="w-60">
                <SearchInput value={search} onChange={setSearch} placeholder="Filter shares…" />
              </div>
              <span className="text-xs text-fg-subtle tabular-nums">
                {filtered.length === items.length
                  ? `${items.length} share${items.length === 1 ? '' : 's'}`
                  : `${filtered.length} of ${items.length}`}
              </span>
            </div>
            <div className="inline-flex rounded-lg border border-border bg-surface-2 p-0.5">
              <button
                type="button"
                onClick={() => setView('cards')}
                className={cn(
                  'rounded-md p-1.5 transition-colors',
                  view === 'cards' ? 'bg-surface text-fg shadow-xs' : 'text-fg-muted hover:text-fg',
                )}
                aria-label="Card view"
              >
                <LayoutGrid className="size-3.5" />
              </button>
              <button
                type="button"
                onClick={() => setView('list')}
                className={cn(
                  'rounded-md p-1.5 transition-colors',
                  view === 'list' ? 'bg-surface text-fg shadow-xs' : 'text-fg-muted hover:text-fg',
                )}
                aria-label="Compact list view"
              >
                <List className="size-3.5" />
              </button>
            </div>
          </div>

          {filtered.length === 0 ? (
            <Card className="p-8">
              <EmptyState title="No matching shares" description="Try a different search term." />
            </Card>
          ) : view === 'cards' ? (
            <div className="grid gap-3.5 md:grid-cols-2 xl:grid-cols-3">
              {filtered.map((share) => (
                <ShareCard key={share.id} share={share} os={os} host={host} username={session.user.username} />
              ))}
            </div>
          ) : (
            <Card className="divide-y divide-border overflow-hidden">
              {filtered.map((share) => (
                <ShareCompactRow key={share.id} share={share} os={os} host={host} username={session.user.username} />
              ))}
            </Card>
          )}
        </>
      )}
    </div>
  )
}
