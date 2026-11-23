import { useQuery } from '@tanstack/react-query'
import { Download, ExternalLink, FileCode2, FileText } from 'lucide-react'
import { formatBytes, formatDateTime } from '@/shared/lib/format'
import { Alert, Dialog, Spinner } from '@/shared/ui'
import { fileDownloadUrl, type FileEntry } from '../api'

type PreviewKind = 'image' | 'video' | 'audio' | 'pdf' | 'text' | 'unsupported'

const IMAGE_EXT = new Set(['png', 'jpg', 'jpeg', 'gif', 'webp', 'svg', 'avif', 'bmp', 'ico'])
const VIDEO_EXT = new Set(['mp4', 'webm', 'mov', 'ogv', 'm4v'])
const AUDIO_EXT = new Set(['mp3', 'wav', 'flac', 'm4a', 'ogg', 'aac', 'opus'])
const TEXT_EXT = new Set([
  'txt',
  'md',
  'markdown',
  'json',
  'yaml',
  'yml',
  'toml',
  'xml',
  'csv',
  'log',
  'sh',
  'bash',
  'go',
  'ts',
  'tsx',
  'js',
  'jsx',
  'mjs',
  'cjs',
  'py',
  'rs',
  'c',
  'h',
  'cpp',
  'java',
  'css',
  'html',
  'sql',
  'conf',
  'ini',
  'env',
  'dockerfile',
  'gitignore',
])

export function getPreviewKind(filename: string): PreviewKind {
  const dot = filename.lastIndexOf('.')
  const ext = dot >= 0 ? filename.slice(dot + 1).toLowerCase() : ''
  if (IMAGE_EXT.has(ext)) return 'image'
  if (VIDEO_EXT.has(ext)) return 'video'
  if (AUDIO_EXT.has(ext)) return 'audio'
  if (ext === 'pdf') return 'pdf'
  if (TEXT_EXT.has(ext) || filename.toLowerCase() === 'readme' || filename.toLowerCase() === 'makefile') return 'text'
  return 'unsupported'
}

interface FilePreviewDialogProps {
  open: boolean
  share: string
  path: string
  entry: FileEntry | null
  onClose: () => void
}

const MAX_TEXT_PREVIEW_BYTES = 512 * 1024

export function FilePreviewDialog({ open, share, path, entry, onClose }: FilePreviewDialogProps) {
  const kind = entry ? getPreviewKind(entry.name) : 'unsupported'
  const inlineUrl = entry ? fileDownloadUrl(share, path, true) : ''
  const downloadUrl = entry ? fileDownloadUrl(share, path, false) : ''
  const tooLarge = Boolean(entry && kind === 'text' && entry.size > MAX_TEXT_PREVIEW_BYTES)

  const textQuery = useQuery({
    queryKey: ['files', 'preview-text', share, path, entry?.modTime],
    enabled: open && Boolean(entry) && kind === 'text' && !tooLarge,
    queryFn: async ({ signal }) => {
      const res = await fetch(inlineUrl, { credentials: 'include', signal })
      if (!res.ok) throw new Error(`HTTP ${res.status}`)
      return res.text()
    },
  })

  return (
    <Dialog
      open={open && Boolean(entry)}
      onClose={onClose}
      size="xl"
      title={entry?.name ?? 'Preview'}
      description={entry ? `${formatBytes(entry.size)} · Modified ${formatDateTime(entry.modTime)}` : undefined}
    >
      {entry && (
        <div className="flex flex-col gap-4">
          <div className="flex min-h-64 items-center justify-center overflow-hidden rounded-xl border border-border bg-bg">
            {kind === 'image' && (
              <img src={inlineUrl} alt={entry.name} className="max-h-[62dvh] max-w-full object-contain p-2" />
            )}

            {kind === 'video' && (
              <video src={inlineUrl} controls className="max-h-[62dvh] w-full bg-black">
                Your browser cannot play this video.
              </video>
            )}

            {kind === 'audio' && (
              <div className="flex w-full max-w-md flex-col items-center gap-4 p-8">
                <span className="grid size-14 place-items-center rounded-2xl bg-primary-soft text-primary">
                  <FileText className="size-7" />
                </span>
                <audio src={inlineUrl} controls className="w-full" />
              </div>
            )}

            {kind === 'pdf' && <iframe src={inlineUrl} title={entry.name} className="h-[65dvh] w-full border-0" />}

            {kind === 'text' && (
              <div className="w-full">
                {tooLarge ? (
                  <div className="p-6">
                    <Alert
                      tone="warning"
                      title={`File is ${formatBytes(entry.size)} (preview limit is ${formatBytes(MAX_TEXT_PREVIEW_BYTES)}). Download to inspect.`}
                    />
                  </div>
                ) : textQuery.isLoading ? (
                  <div className="flex justify-center py-16">
                    <Spinner />
                  </div>
                ) : textQuery.error ? (
                  <div className="p-6">
                    <Alert tone="warning" title="Could not load text preview." />
                  </div>
                ) : (
                  <pre className="max-h-[62dvh] overflow-auto p-4 font-mono text-xs leading-relaxed text-fg">
                    <code>{textQuery.data}</code>
                  </pre>
                )}
              </div>
            )}

            {kind === 'unsupported' && (
              <div className="flex flex-col items-center gap-3 p-10 text-center">
                <span className="grid size-12 place-items-center rounded-xl bg-surface-2 text-fg-muted">
                  <FileCode2 className="size-6" />
                </span>
                <div>
                  <p className="text-sm font-medium text-fg">No inline preview available</p>
                  <p className="mt-0.5 text-xs text-fg-muted">Download the file to open it locally.</p>
                </div>
              </div>
            )}
          </div>

          <div className="flex flex-wrap items-center justify-between gap-2">
            <code className="truncate text-xs text-fg-subtle">
              /{share}/{path}
            </code>
            <div className="flex items-center gap-2">
              {kind !== 'unsupported' && (
                <a
                  href={inlineUrl}
                  target="_blank"
                  rel="noreferrer"
                  className="inline-flex h-8 items-center gap-1.5 rounded-md border border-border-strong bg-surface px-3 text-xs font-medium text-fg shadow-xs hover:bg-surface-2"
                >
                  <ExternalLink className="size-3.5" />
                  Open raw
                </a>
              )}
              <a
                href={downloadUrl}
                download={entry.name}
                className="inline-flex h-8 items-center gap-1.5 rounded-md bg-primary px-3 text-xs font-medium text-primary-fg shadow-xs hover:bg-primary-hover"
              >
                <Download className="size-3.5" />
                Download
              </a>
            </div>
          </div>
        </div>
      )}
    </Dialog>
  )
}
