import { useQuery } from '@tanstack/react-query'
import { useNavigate } from '@tanstack/react-router'
import { FileUp, FolderOpen, Upload } from 'lucide-react'
import { useEffect, useState, type FormEvent } from 'react'
import { notify } from '@/features/notifications'
import { mySharesQuery } from '@/features/portal'
import { EmptyState, FormField, PageHeader } from '@/shared/components'
import { formatBytes } from '@/shared/lib/format'
import { Button, Card, CardContent, CardHeader, CardTitle, Input, Select, Spinner } from '@/shared/ui'
import { enqueueUploadBatch } from '../upload-store'

const SHARE_CACHE = 'netshare-share-target-v1'

interface SharedManifestEntry {
  key: string
  name: string
  type: string
  size: number
}

async function consumeSharedFiles(): Promise<File[]> {
  if (typeof caches === 'undefined') return []
  try {
    const cache = await caches.open(SHARE_CACHE)
    const manifestRes = await cache.match('/__shared/manifest.json')
    if (!manifestRes) return []
    const entries = (await manifestRes.json()) as SharedManifestEntry[]
    const files: File[] = []
    for (const item of entries) {
      const res = await cache.match(item.key)
      if (!res) continue
      const blob = await res.blob()
      files.push(new File([blob], item.name, { type: item.type || blob.type }))
    }
    const keys = await cache.keys()
    await Promise.all(keys.map((k) => cache.delete(k)))
    return files
  } catch {
    return []
  }
}

export function ShareTargetPage() {
  const navigate = useNavigate()
  const myShares = useQuery(mySharesQuery)
  const writable = (myShares.data?.items ?? []).filter((s) => s.level === 'write' || s.level === 'upload')

  const [files, setFiles] = useState<File[]>([])
  const [loaded, setLoaded] = useState(false)
  const [share, setShare] = useState('')
  const [destPath, setDestPath] = useState('')

  useEffect(() => {
    let active = true
    void consumeSharedFiles().then((got) => {
      if (!active) return
      setFiles(got)
      setLoaded(true)
    })
    return () => {
      active = false
    }
  }, [])

  const activeShare = share || writable[0]?.name || ''

  function handleUpload(e: FormEvent) {
    e.preventDefault()
    if (!activeShare || files.length === 0) return
    const cleanDir = destPath.trim().replace(/^\/+|\/+$/g, '')
    enqueueUploadBatch({
      share: activeShare,
      baseDir: cleanDir,
      label: `Shared ${files.length} file${files.length === 1 ? '' : 's'}`,
      files: files.map((file) => ({ file, relativeDir: '' })),
    })
    notify.success('Upload started', `Sending ${files.length} file${files.length === 1 ? '' : 's'} to ${activeShare}.`)
    void navigate({ to: '/files', search: { share: activeShare, path: cleanDir || undefined } })
  }

  return (
    <div className="mx-auto flex max-w-xl flex-col gap-5">
      <PageHeader
        title="Upload Shared Files"
        info="Files shared to NetShare Hub via Android or mobile PWA Share Target are uploaded with resumable tus transfers."
      />

      <Card>
        <CardHeader>
          <CardTitle className="flex items-center gap-2">
            <FileUp className="size-4 text-primary" />
            Destination
          </CardTitle>
        </CardHeader>
        <CardContent>
          {!loaded || myShares.isLoading ? (
            <div className="flex justify-center py-8">
              <Spinner />
            </div>
          ) : writable.length === 0 ? (
            <EmptyState
              title="No writable shares available"
              description="Ask an administrator for write or drop-box access to a share."
            />
          ) : (
            <form onSubmit={handleUpload} className="flex flex-col gap-4">
              <FormField label="Target share" htmlFor="st-share">
                <Select id="st-share" value={activeShare} onChange={(e) => setShare(e.target.value)}>
                  {writable.map((s) => (
                    <option key={s.id} value={s.name}>
                      {s.name} ({s.level})
                    </option>
                  ))}
                </Select>
              </FormField>

              <FormField label="Subfolder (optional)" htmlFor="st-path">
                <Input
                  id="st-path"
                  value={destPath}
                  placeholder="Photos/Mobile"
                  onChange={(e) => setDestPath(e.target.value)}
                />
              </FormField>

              <div className="rounded-lg border border-border bg-surface-2/40 p-3">
                <div className="flex items-center justify-between text-xs font-medium text-fg">
                  <span className="inline-flex items-center gap-1.5">
                    <FolderOpen className="size-3.5 text-primary" />
                    {files.length} file{files.length === 1 ? '' : 's'} ready
                  </span>
                  <label className="cursor-pointer text-primary hover:underline">
                    Add files
                    <input
                      type="file"
                      multiple
                      className="hidden"
                      onChange={(e) => {
                        const picked = Array.from(e.target.files ?? [])
                        if (picked.length > 0) setFiles((prev) => [...prev, ...picked])
                      }}
                    />
                  </label>
                </div>
                {files.length > 0 && (
                  <ul className="mt-2 max-h-40 space-y-1 overflow-y-auto text-xs text-fg-muted">
                    {files.map((f, i) => (
                      <li key={`${f.name}-${i}`} className="flex items-center justify-between gap-2">
                        <span className="truncate">{f.name}</span>
                        <span className="shrink-0 font-mono text-[0.7rem] text-fg-subtle">{formatBytes(f.size)}</span>
                      </li>
                    ))}
                  </ul>
                )}
              </div>

              <div className="flex justify-end gap-2">
                <Button variant="ghost" type="button" onClick={() => void navigate({ to: '/files' })}>
                  Cancel
                </Button>
                <Button type="submit" disabled={files.length === 0 || !activeShare}>
                  <Upload className="size-4" />
                  Upload to {activeShare}
                </Button>
              </div>
            </form>
          )}
        </CardContent>
      </Card>
    </div>
  )
}
