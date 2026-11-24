/// <reference lib="webworker" />

export type UploadConflict = 'rename' | 'replace'
export type UploadItemStatus = 'queued' | 'uploading' | 'paused' | 'done' | 'error' | 'cancelled'
export type UploadBatchStatus = 'uploading' | 'paused' | 'done' | 'done_with_errors' | 'cancelled'

export interface WorkerEnqueueItem {
  id: string
  file: File
  dir: string
  filename: string
  fingerprint: string
  savedUrl?: string | null
}

export interface UploadItemSnapshot {
  id: string
  batchId: string
  filename: string
  dir: string
  size: number
  uploaded: number
  status: UploadItemStatus
  error?: string
}

export interface UploadBatchSnapshot {
  id: string
  label: string
  share: string
  baseDir: string
  status: UploadBatchStatus
  totalFiles: number
  doneFiles: number
  failedFiles: number
  activeFiles: number
  queuedFiles: number
  pausedFiles: number
  totalBytes: number
  uploadedBytes: number
  speedBps: number
  etaSeconds: number | null
  createdAt: number
  /** Only active, failed, and a small preview slice of queued/done items so the UI stays tiny. */
  activeItems: UploadItemSnapshot[]
  failedItems: UploadItemSnapshot[]
  queuedPreview: UploadItemSnapshot[]
  donePreview: UploadItemSnapshot[]
}

export type WorkerInMessage =
  | {
      type: 'enqueue-batch'
      apiBase: string
      csrfToken: string
      batchId: string
      label: string
      share: string
      baseDir: string
      conflict: UploadConflict
      dirsToCreate: string[]
      items: WorkerEnqueueItem[]
    }
  | { type: 'set-csrf'; csrfToken: string }
  | { type: 'pause-batch'; batchId: string }
  | { type: 'resume-batch'; batchId: string }
  | { type: 'retry-batch-failed'; batchId: string }
  | { type: 'cancel-batch'; batchId: string }
  | { type: 'pause-item'; itemId: string }
  | { type: 'resume-item'; itemId: string }
  | { type: 'cancel-item'; itemId: string }
  | { type: 'clear-finished' }

export type WorkerOutMessage =
  | { type: 'snapshot'; batches: UploadBatchSnapshot[] }
  | { type: 'persist-url'; fingerprint: string; url: string }
  | { type: 'clear-url'; fingerprint: string }
  | { type: 'share-dirty'; share: string }
  | {
      type: 'batch-complete'
      batchId: string
      label: string
      share: string
      baseDir: string
      totalFiles: number
      doneFiles: number
      failedFiles: number
      totalBytes: number
    }

const TUS_VERSION = '1.0.0'
const CHUNK_SIZE = 1024 * 1024 // 1 MiB
const CONCURRENCY = 3
const SNAPSHOT_INTERVAL_MS = 150
const PREVIEW_LIMIT = 40

interface InternalItem {
  id: string
  batchId: string
  file: File
  share: string
  dir: string
  filename: string
  conflict: UploadConflict
  fingerprint: string
  uploadUrl: string | null
  size: number
  uploaded: number
  status: UploadItemStatus
  error?: string
}

interface InternalBatch {
  id: string
  label: string
  share: string
  baseDir: string
  conflict: UploadConflict
  createdAt: number
  paused: boolean
  completedNotified: boolean
  dirsToCreate: string[]
  dirsReady: boolean
  dirsCreating: Promise<void> | null
  itemIds: string[]
  // Rolling speed window for smooth aggregate MB/s + ETA
  speedWindowStart: number
  speedWindowBytes: number
  speedBps: number
}

let apiBase = '/api/v1'
let csrfToken = ''

const batches = new Map<string, InternalBatch>()
const items = new Map<string, InternalItem>()
const controllers = new Map<string, AbortController>()
let activeSlots = 0
let snapshotDirty = false
let snapshotTimer: ReturnType<typeof setTimeout> | null = null

function post(msg: WorkerOutMessage) {
  self.postMessage(msg)
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

function authHeaders(extra: Record<string, string> = {}): Record<string, string> {
  const headers: Record<string, string> = { 'Tus-Resumable': TUS_VERSION, ...extra }
  if (csrfToken) headers['X-CSRF-Token'] = csrfToken
  return headers
}

function toItemSnapshot(it: InternalItem): UploadItemSnapshot {
  return {
    id: it.id,
    batchId: it.batchId,
    filename: it.filename,
    dir: it.dir,
    size: it.size,
    uploaded: it.uploaded,
    status: it.status,
    error: it.error,
  }
}

function buildSnapshot(): UploadBatchSnapshot[] {
  const out: UploadBatchSnapshot[] = []
  for (const batch of batches.values()) {
    let doneFiles = 0
    let failedFiles = 0
    let activeFiles = 0
    let queuedFiles = 0
    let pausedFiles = 0
    let totalBytes = 0
    let uploadedBytes = 0
    const activeItems: UploadItemSnapshot[] = []
    const failedItems: UploadItemSnapshot[] = []
    const queuedPreview: UploadItemSnapshot[] = []
    const donePreview: UploadItemSnapshot[] = []

    for (const id of batch.itemIds) {
      const it = items.get(id)
      if (!it || it.status === 'cancelled') continue
      totalBytes += it.size
      uploadedBytes += it.uploaded
      switch (it.status) {
        case 'done':
          doneFiles++
          if (donePreview.length < PREVIEW_LIMIT) donePreview.push(toItemSnapshot(it))
          break
        case 'error':
          failedFiles++
          if (failedItems.length < PREVIEW_LIMIT) failedItems.push(toItemSnapshot(it))
          break
        case 'uploading':
          activeFiles++
          activeItems.push(toItemSnapshot(it))
          break
        case 'paused':
          pausedFiles++
          if (queuedPreview.length < PREVIEW_LIMIT) queuedPreview.push(toItemSnapshot(it))
          break
        case 'queued':
          queuedFiles++
          if (queuedPreview.length < PREVIEW_LIMIT) queuedPreview.push(toItemSnapshot(it))
          break
      }
    }

    const totalFiles = doneFiles + failedFiles + activeFiles + queuedFiles + pausedFiles
    let status: UploadBatchStatus = 'uploading'
    if (totalFiles === 0) {
      status = 'cancelled'
    } else if (activeFiles === 0 && queuedFiles === 0 && pausedFiles === 0) {
      status = failedFiles > 0 ? 'done_with_errors' : 'done'
    } else if (batch.paused && activeFiles === 0) {
      status = 'paused'
    }

    const speedBps = status === 'uploading' ? batch.speedBps : 0
    const remainingBytes = Math.max(0, totalBytes - uploadedBytes)
    const etaSeconds = status === 'uploading' && speedBps > 0 ? Math.ceil(remainingBytes / speedBps) : null

    out.push({
      id: batch.id,
      label: batch.label,
      share: batch.share,
      baseDir: batch.baseDir,
      status,
      totalFiles,
      doneFiles,
      failedFiles,
      activeFiles,
      queuedFiles,
      pausedFiles,
      totalBytes,
      uploadedBytes,
      speedBps,
      etaSeconds,
      createdAt: batch.createdAt,
      activeItems,
      failedItems,
      queuedPreview,
      donePreview,
    })

    if (!batch.completedNotified && (status === 'done' || status === 'done_with_errors')) {
      batch.completedNotified = true
      batch.speedBps = 0
      post({
        type: 'batch-complete',
        batchId: batch.id,
        label: batch.label,
        share: batch.share,
        baseDir: batch.baseDir,
        totalFiles,
        doneFiles,
        failedFiles,
        totalBytes,
      })
    }
  }

  // Newest batches first.
  out.sort((a, b) => b.createdAt - a.createdAt)
  return out
}

function flushSnapshotNow() {
  snapshotDirty = false
  if (snapshotTimer !== null) {
    clearTimeout(snapshotTimer)
    snapshotTimer = null
  }
  post({ type: 'snapshot', batches: buildSnapshot() })
}

function scheduleSnapshot() {
  snapshotDirty = true
  if (snapshotTimer !== null) return
  snapshotTimer = setTimeout(() => {
    snapshotTimer = null
    if (snapshotDirty) flushSnapshotNow()
  }, SNAPSHOT_INTERVAL_MS)
}

async function ensureBatchDirs(batch: InternalBatch): Promise<void> {
  if (batch.dirsReady || batch.dirsToCreate.length === 0) {
    batch.dirsReady = true
    return
  }
  if (batch.dirsCreating) {
    await batch.dirsCreating
    return
  }
  batch.dirsCreating = (async () => {
    // Shallowest folders first so parent directories always exist before children.
    const sorted = [...batch.dirsToCreate].sort((a, b) => a.split('/').length - b.split('/').length)
    for (const dirPath of sorted) {
      if (batch.paused) break
      try {
        const headers: Record<string, string> = { 'Content-Type': 'application/json' }
        if (csrfToken) headers['X-CSRF-Token'] = csrfToken
        await fetch(`${apiBase}/files/${encodeURIComponent(batch.share)}/folders`, {
          method: 'POST',
          credentials: 'include',
          headers,
          body: JSON.stringify({ path: dirPath }),
        })
      } catch {
        // Folder may already exist or fail on individual upload; continue cleanly.
      }
    }
    batch.dirsReady = true
  })()
  await batch.dirsCreating
  batch.dirsCreating = null
}

async function uploadItem(it: InternalItem, batch: InternalBatch): Promise<void> {
  const controller = new AbortController()
  controllers.set(it.id, controller)
  const signal = controller.signal

  try {
    await ensureBatchDirs(batch)
    if (signal.aborted || it.status !== 'uploading') return

    let uploadUrl = it.uploadUrl
    let offset = 0

    // Resume from server offset if a previous upload URL exists.
    if (uploadUrl) {
      const head = await fetch(uploadUrl, {
        method: 'HEAD',
        credentials: 'include',
        headers: { 'Tus-Resumable': TUS_VERSION },
        signal,
      })
      if (head.ok) {
        offset = Number.parseInt(head.headers.get('Upload-Offset') ?? '0', 10) || 0
        it.uploaded = offset
      } else {
        post({ type: 'clear-url', fingerprint: it.fingerprint })
        uploadUrl = null
        it.uploadUrl = null
      }
    }

    if (!uploadUrl) {
      const create = await fetch(`${apiBase}/uploads`, {
        method: 'POST',
        credentials: 'include',
        headers: authHeaders({
          'Upload-Length': String(it.size),
          'Upload-Metadata': encodeMetadata({
            share: it.share,
            path: it.dir,
            filename: it.filename,
            conflict: it.conflict,
          }),
        }),
        signal,
      })
      if (!create.ok) throw new Error(`Init failed (${create.status})`)
      const loc = create.headers.get('Location')
      if (!loc) throw new Error('Missing upload Location')
      uploadUrl = loc
      it.uploadUrl = loc
      post({ type: 'persist-url', fingerprint: it.fingerprint, url: loc })
    }

    if (it.size === 0) {
      post({ type: 'clear-url', fingerprint: it.fingerprint })
      it.uploadUrl = null
      it.uploaded = 0
      it.status = 'done'
      post({ type: 'share-dirty', share: it.share })
      return
    }

    while (offset < it.size) {
      if (signal.aborted) return
      const end = Math.min(offset + CHUNK_SIZE, it.size)
      const chunk = it.file.slice(offset, end)

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
        if (res.status === 404) {
          post({ type: 'clear-url', fingerprint: it.fingerprint })
          it.uploadUrl = null
        }
        throw new Error(`Upload failed (${res.status})`)
      }

      const nextOffset = Number.parseInt(res.headers.get('Upload-Offset') ?? String(end), 10)
      const sent = Math.max(0, nextOffset - offset)
      offset = nextOffset
      it.uploaded = offset

      batch.speedWindowBytes += sent
      const elapsed = (performance.now() - batch.speedWindowStart) / 1000
      if (elapsed >= 0.25) {
        batch.speedBps = batch.speedWindowBytes / elapsed
        if (elapsed >= 2.5) {
          batch.speedWindowStart = performance.now()
          batch.speedWindowBytes = 0
        }
      }
      scheduleSnapshot()
    }

    post({ type: 'clear-url', fingerprint: it.fingerprint })
    it.uploadUrl = null
    it.uploaded = it.size
    it.status = 'done'
    post({ type: 'share-dirty', share: it.share })
  } catch (err) {
    if (signal.aborted) return
    it.status = 'error'
    it.error = (err as Error).message || 'Upload failed'
  } finally {
    controllers.delete(it.id)
    activeSlots = Math.max(0, activeSlots - 1)
    pumpQueue()
    scheduleSnapshot()
  }
}

function nextQueuedItem(): { item: InternalItem; batch: InternalBatch } | null {
  for (const batch of batches.values()) {
    if (batch.paused) continue
    for (const id of batch.itemIds) {
      const it = items.get(id)
      if (it && it.status === 'queued') {
        return { item: it, batch }
      }
    }
  }
  return null
}

function pumpQueue() {
  while (activeSlots < CONCURRENCY) {
    const next = nextQueuedItem()
    if (!next) break
    activeSlots++
    next.item.status = 'uploading'
    next.item.error = undefined
    void uploadItem(next.item, next.batch)
  }
}

async function abortRemoteUpload(it: InternalItem) {
  const url = it.uploadUrl
  it.uploadUrl = null
  post({ type: 'clear-url', fingerprint: it.fingerprint })
  if (!url) return
  try {
    await fetch(url, {
      method: 'DELETE',
      credentials: 'include',
      headers: authHeaders(),
    })
  } catch {
    // ignore abort errors
  }
}

self.onmessage = (e: MessageEvent<WorkerInMessage>) => {
  const msg = e.data
  switch (msg.type) {
    case 'set-csrf':
      csrfToken = msg.csrfToken
      break

    case 'enqueue-batch': {
      apiBase = msg.apiBase
      csrfToken = msg.csrfToken
      const itemIds: string[] = []
      for (const raw of msg.items) {
        itemIds.push(raw.id)
        items.set(raw.id, {
          id: raw.id,
          batchId: msg.batchId,
          file: raw.file,
          share: msg.share,
          dir: raw.dir,
          filename: raw.filename,
          conflict: msg.conflict,
          fingerprint: raw.fingerprint,
          uploadUrl: raw.savedUrl ?? null,
          size: raw.file.size,
          uploaded: 0,
          status: 'queued',
        })
      }
      batches.set(msg.batchId, {
        id: msg.batchId,
        label: msg.label,
        share: msg.share,
        baseDir: msg.baseDir,
        conflict: msg.conflict,
        createdAt: Date.now(),
        paused: false,
        completedNotified: false,
        dirsToCreate: msg.dirsToCreate,
        dirsReady: msg.dirsToCreate.length === 0,
        dirsCreating: null,
        itemIds,
        speedWindowStart: performance.now(),
        speedWindowBytes: 0,
        speedBps: 0,
      })
      pumpQueue()
      flushSnapshotNow()
      break
    }

    case 'pause-batch': {
      const batch = batches.get(msg.batchId)
      if (!batch) break
      batch.paused = true
      batch.speedBps = 0
      for (const id of batch.itemIds) {
        const it = items.get(id)
        if (!it) continue
        if (it.status === 'uploading') {
          controllers.get(id)?.abort()
          it.status = 'paused'
        } else if (it.status === 'queued') {
          it.status = 'paused'
        }
      }
      flushSnapshotNow()
      break
    }

    case 'resume-batch': {
      const batch = batches.get(msg.batchId)
      if (!batch) break
      batch.paused = false
      batch.completedNotified = false
      batch.speedWindowStart = performance.now()
      batch.speedWindowBytes = 0
      for (const id of batch.itemIds) {
        const it = items.get(id)
        if (it && it.status === 'paused') it.status = 'queued'
      }
      pumpQueue()
      flushSnapshotNow()
      break
    }

    case 'retry-batch-failed': {
      const batch = batches.get(msg.batchId)
      if (!batch) break
      batch.paused = false
      batch.completedNotified = false
      batch.speedWindowStart = performance.now()
      batch.speedWindowBytes = 0
      for (const id of batch.itemIds) {
        const it = items.get(id)
        if (it && it.status === 'error') {
          it.status = 'queued'
          it.error = undefined
        }
      }
      pumpQueue()
      flushSnapshotNow()
      break
    }

    case 'cancel-batch': {
      const batch = batches.get(msg.batchId)
      if (!batch) break
      for (const id of batch.itemIds) {
        const it = items.get(id)
        if (!it) continue
        controllers.get(id)?.abort()
        if (it.status !== 'done') {
          void abortRemoteUpload(it)
        }
        items.delete(id)
      }
      batches.delete(msg.batchId)
      pumpQueue()
      flushSnapshotNow()
      break
    }

    case 'pause-item': {
      const it = items.get(msg.itemId)
      if (!it) break
      if (it.status === 'uploading') {
        controllers.get(it.id)?.abort()
        it.status = 'paused'
      } else if (it.status === 'queued') {
        it.status = 'paused'
      }
      flushSnapshotNow()
      break
    }

    case 'resume-item': {
      const it = items.get(msg.itemId)
      if (!it) break
      if (it.status === 'paused' || it.status === 'error') {
        const batch = batches.get(it.batchId)
        if (batch) {
          batch.paused = false
          batch.completedNotified = false
        }
        it.status = 'queued'
        it.error = undefined
        pumpQueue()
        flushSnapshotNow()
      }
      break
    }

    case 'cancel-item': {
      const it = items.get(msg.itemId)
      if (!it) break
      controllers.get(it.id)?.abort()
      if (it.status !== 'done') void abortRemoteUpload(it)
      it.status = 'cancelled'
      flushSnapshotNow()
      break
    }

    case 'clear-finished': {
      for (const [id, batch] of batches.entries()) {
        const allSettled = batch.itemIds.every((itemId) => {
          const it = items.get(itemId)
          return !it || it.status === 'done' || it.status === 'cancelled'
        })
        if (allSettled) {
          for (const itemId of batch.itemIds) items.delete(itemId)
          batches.delete(id)
        }
      }
      flushSnapshotNow()
      break
    }
  }
}
