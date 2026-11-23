import { API_BASE, getCsrfToken } from '@/shared/api'

const TUS_VERSION = '1.0.0'
const CHUNK_SIZE = 1024 * 1024 // 1 MiB chunks for smooth progress + quick pause/resume
const FINGERPRINT_PREFIX = 'netshare.tus.'

export type UploadStatus = 'queued' | 'uploading' | 'paused' | 'done' | 'error' | 'cancelled'
export type UploadConflict = 'rename' | 'replace'

export interface UploadTask {
  id: string
  file: File
  share: string
  dir: string
  filename: string
  conflict: UploadConflict
  size: number
  uploaded: number
  speedBps: number
  etaSeconds: number | null
  status: UploadStatus
  error?: string
  uploadUrl?: string
}

function b64(value: string): string {
  const bytes = new TextEncoder().encode(value)
  let binary = ''
  for (const b of bytes) binary += String.fromCharCode(b)
  return btoa(binary)
}

function encodeMetadata(meta: Record<string, string>): string {
  return Object.entries(meta)
    .map(([k, v]) => `${k} ${b64(v)}`)
    .join(',')
}

function fingerprintKey(share: string, dir: string, file: File): string {
  return `${FINGERPRINT_PREFIX}${share}:${dir}:${file.name}:${file.size}:${file.lastModified}`
}

function getSavedUrl(key: string): string | null {
  try {
    return localStorage.getItem(key)
  } catch {
    return null
  }
}

function saveUrl(key: string, url: string) {
  try {
    localStorage.setItem(key, url)
  } catch {
    // ignore quota errors
  }
}

function clearSavedUrl(key: string) {
  try {
    localStorage.removeItem(key)
  } catch {
    // ignore
  }
}

function authHeaders(extra: Record<string, string> = {}): HeadersInit {
  const headers: Record<string, string> = {
    'Tus-Resumable': TUS_VERSION,
    ...extra,
  }
  const csrf = getCsrfToken()
  if (csrf) headers['X-CSRF-Token'] = csrf
  return headers
}

/**
 * Uploads a single file using the server's tus 1.0.0 resumable protocol.
 * Resumes seamlessly from the last stored server offset across pauses or refreshes.
 */
export async function runTusUpload(
  task: UploadTask,
  signal: AbortSignal,
  onProgress: (patch: Partial<UploadTask>) => void,
): Promise<void> {
  const fpKey = fingerprintKey(task.share, task.dir, task.file)
  let uploadUrl = task.uploadUrl ?? getSavedUrl(fpKey)
  let offset = 0

  // 1. If we have a previous upload URL, ask the server for the current offset.
  if (uploadUrl) {
    const head = await fetch(uploadUrl, {
      method: 'HEAD',
      credentials: 'include',
      headers: { 'Tus-Resumable': TUS_VERSION },
      signal,
    })
    if (head.ok) {
      offset = Number.parseInt(head.headers.get('Upload-Offset') ?? '0', 10) || 0
    } else {
      clearSavedUrl(fpKey)
      uploadUrl = null
    }
  }

  // 2. Create a new tus upload resource if needed.
  if (!uploadUrl) {
    const create = await fetch(`${API_BASE}/uploads`, {
      method: 'POST',
      credentials: 'include',
      headers: authHeaders({
        'Upload-Length': String(task.size),
        'Upload-Metadata': encodeMetadata({
          share: task.share,
          path: task.dir,
          filename: task.filename,
          conflict: task.conflict,
        }),
      }),
      signal,
    })
    if (!create.ok) {
      throw new Error(`Upload init failed (${create.status})`)
    }
    const loc = create.headers.get('Location')
    if (!loc) throw new Error('Server did not return upload Location header')
    uploadUrl = loc
    saveUrl(fpKey, uploadUrl)
    onProgress({ uploadUrl })
  }

  // Handle 0-byte files immediately.
  if (task.size === 0) {
    clearSavedUrl(fpKey)
    onProgress({ uploaded: 0, speedBps: 0, etaSeconds: 0, status: 'done' })
    return
  }

  let windowStart = performance.now()
  let windowBytes = 0

  // 3. Stream chunks via PATCH until complete.
  while (offset < task.size) {
    if (signal.aborted) return
    const end = Math.min(offset + CHUNK_SIZE, task.size)
    const chunk = task.file.slice(offset, end)

    const res = await fetch(uploadUrl, {
      method: 'PATCH',
      credentials: 'include',
      headers: authHeaders({
        'Content-Type': 'application/offset+octet-stream',
        'Upload-Offset': String(offset),
      }),
      body: chunk,
      signal,
    })

    if (!res.ok) {
      if (res.status === 404) clearSavedUrl(fpKey)
      throw new Error(`Chunk upload failed (${res.status})`)
    }

    const nextOffset = Number.parseInt(res.headers.get('Upload-Offset') ?? String(end), 10)
    const sent = Math.max(0, nextOffset - offset)
    offset = nextOffset
    windowBytes += sent

    const elapsedSec = Math.max(0.05, (performance.now() - windowStart) / 1000)
    const speedBps = windowBytes / elapsedSec
    const remaining = Math.max(0, task.size - offset)
    const etaSeconds = speedBps > 0 ? Math.ceil(remaining / speedBps) : null

    if (elapsedSec > 2) {
      windowStart = performance.now()
      windowBytes = 0
    }

    onProgress({ uploaded: offset, speedBps, etaSeconds })
  }

  clearSavedUrl(fpKey)
  onProgress({ uploaded: task.size, speedBps: 0, etaSeconds: 0, status: 'done' })
}

export async function abortTusUpload(task: UploadTask): Promise<void> {
  const fpKey = fingerprintKey(task.share, task.dir, task.file)
  const url = task.uploadUrl ?? getSavedUrl(fpKey)
  clearSavedUrl(fpKey)
  if (!url) return
  try {
    await fetch(url, {
      method: 'DELETE',
      credentials: 'include',
      headers: authHeaders(),
    })
  } catch {
    // ignore cleanup failures
  }
}
