import { useState, type ReactNode } from 'react'
import { thumbnailUrl } from '../api'

const IMAGE_EXTS = new Set(['jpg', 'jpeg', 'png', 'gif'])
const VIDEO_EXTS = new Set(['mp4', 'mkv', 'mov', 'webm', 'avi', 'm4v'])

function getExt(name: string): string {
  const dot = name.lastIndexOf('.')
  return dot > 0 ? name.slice(dot + 1).toLowerCase() : ''
}

export function canThumbnail(name: string, isDir: boolean, videoEnabled: boolean): boolean {
  if (isDir) return false
  const ext = getExt(name)
  if (IMAGE_EXTS.has(ext)) return true
  if (videoEnabled && VIDEO_EXTS.has(ext)) return true
  return false
}

export function EntryThumbnail({
  share,
  path,
  name,
  isDir,
  videoEnabled,
  fallback,
}: {
  share: string
  path: string
  name: string
  isDir: boolean
  videoEnabled: boolean
  fallback: ReactNode
}) {
  const [failed, setFailed] = useState(false)
  const supported = !failed && canThumbnail(name, isDir, videoEnabled)

  if (!supported) {
    return (
      <div className="grid size-12 place-items-center rounded-lg bg-surface shadow-xs ring-1 ring-border">
        {fallback}
      </div>
    )
  }

  return (
    <div className="relative h-20 w-full overflow-hidden rounded-lg bg-surface-2 ring-1 ring-border">
      <img
        src={thumbnailUrl(share, path, 320)}
        alt={name}
        loading="lazy"
        decoding="async"
        onError={() => setFailed(true)}
        className="size-full object-cover transition-transform duration-200 group-hover:scale-[1.03]"
      />
    </div>
  )
}
