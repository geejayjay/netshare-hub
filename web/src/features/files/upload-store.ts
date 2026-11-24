import type { QueryClient } from '@tanstack/react-query'
import { create } from 'zustand'
import { notify } from '@/features/notifications'
import { API_BASE, getCsrfToken } from '@/shared/api'
import { formatBytes } from '@/shared/lib/format'
import type { DroppedFile } from './drop-entries'
import type { UploadBatchSnapshot, UploadConflict, WorkerInMessage, WorkerOutMessage } from './tus.worker'

const FINGERPRINT_PREFIX = 'netshare.tus.'

interface UploadStoreState {
  batches: UploadBatchSnapshot[]
  setBatches: (batches: UploadBatchSnapshot[]) => void
}

export const useUploadStore = create<UploadStoreState>((set) => ({
  batches: [],
  setBatches: (batches) => set({ batches }),
}))

let worker: Worker | null = null
let boundQueryClient: QueryClient | null = null
const dirtyTimers = new Map<string, number>()

function fingerprintFor(share: string, dir: string, file: File): string {
  return `${FINGERPRINT_PREFIX}${share}:${dir}:${file.name}:${file.size}:${file.lastModified}`
}

function readSavedUrl(fp: string): string | null {
  try {
    return localStorage.getItem(fp)
  } catch {
    return null
  }
}

function writeSavedUrl(fp: string, url: string) {
  try {
    localStorage.setItem(fp, url)
  } catch {
    // ignore storage quota errors
  }
}

function removeSavedUrl(fp: string) {
  try {
    localStorage.removeItem(fp)
  } catch {
    // ignore
  }
}

function scheduleShareInvalidate(share: string, immediate = false) {
  if (!boundQueryClient) return
  if (immediate) {
    const t = dirtyTimers.get(share)
    if (t !== undefined) {
      window.clearTimeout(t)
      dirtyTimers.delete(share)
    }
    void boundQueryClient.invalidateQueries({ queryKey: ['files', 'entries', share] })
    return
  }
  if (dirtyTimers.has(share)) return
  const timer = window.setTimeout(() => {
    dirtyTimers.delete(share)
    void boundQueryClient?.invalidateQueries({ queryKey: ['files', 'entries', share] })
  }, 2000)
  dirtyTimers.set(share, timer)
}

function ensureWorker(): Worker {
  if (worker) return worker
  const w = new Worker(new URL('./tus.worker.ts', import.meta.url), { type: 'module' })
  w.onmessage = (e: MessageEvent<WorkerOutMessage>) => {
    const msg = e.data
    switch (msg.type) {
      case 'snapshot':
        useUploadStore.getState().setBatches(msg.batches)
        break
      case 'persist-url':
        writeSavedUrl(msg.fingerprint, msg.url)
        break
      case 'clear-url':
        removeSavedUrl(msg.fingerprint)
        break
      case 'share-dirty':
        scheduleShareInvalidate(msg.share, false)
        break
      case 'batch-complete': {
        scheduleShareInvalidate(msg.share, true)
        const target = `/${msg.share}${msg.baseDir ? `/${msg.baseDir}` : ''}`
        if (msg.failedFiles === 0) {
          notify.success(
            'Upload complete',
            `${msg.doneFiles.toLocaleString()} ${msg.doneFiles === 1 ? 'file' : 'files'} (${formatBytes(msg.totalBytes)}) → ${target}`,
          )
        } else {
          notify.error(
            new Error(
              `${msg.label}: ${msg.doneFiles.toLocaleString()} of ${msg.totalFiles.toLocaleString()} uploaded, ${msg.failedFiles.toLocaleString()} failed.`,
            ),
          )
        }
        break
      }
    }
  }
  worker = w
  return w
}

function send(msg: WorkerInMessage) {
  ensureWorker().postMessage(msg)
}

/** Binds the app's QueryClient so completed uploads refresh folder listings cleanly. */
export function bindUploadQueryClient(qc: QueryClient) {
  boundQueryClient = qc
}

function joinPath(base: string, rel: string): string {
  if (!base) return rel
  if (!rel) return base
  return `${base}/${rel}`
}

/**
 * Enqueues an entire drag-and-drop or folder selection as ONE tracked batch
 * running in the background Web Worker (3 concurrent tus streams).
 */
export function enqueueUploadBatch({
  share,
  baseDir,
  label,
  files,
  conflict = 'rename',
}: {
  share: string
  baseDir: string
  label: string
  files: DroppedFile[]
  conflict?: UploadConflict
}) {
  if (!share || files.length === 0) return

  const batchId = `batch-${Date.now()}-${Math.random().toString(36).slice(2, 7)}`
  const dirsSet = new Set<string>()
  const workerItems = files.map(({ file, relativeDir }, idx) => {
    const dir = joinPath(baseDir, relativeDir)
    if (relativeDir) {
      const parts = relativeDir.split('/').filter(Boolean)
      let acc = baseDir
      for (const seg of parts) {
        acc = joinPath(acc, seg)
        dirsSet.add(acc)
      }
    }
    const fingerprint = fingerprintFor(share, dir, file)
    return {
      id: `${batchId}-${idx}`,
      file,
      dir,
      filename: file.name,
      fingerprint,
      savedUrl: readSavedUrl(fingerprint),
    }
  })

  send({
    type: 'enqueue-batch',
    apiBase: API_BASE,
    csrfToken: getCsrfToken(),
    batchId,
    label,
    share,
    baseDir,
    conflict,
    dirsToCreate: Array.from(dirsSet),
    items: workerItems,
  })
}

export const uploadActions = {
  pauseBatch: (batchId: string) => send({ type: 'pause-batch', batchId }),
  resumeBatch: (batchId: string) => {
    send({ type: 'set-csrf', csrfToken: getCsrfToken() })
    send({ type: 'resume-batch', batchId })
  },
  retryBatchFailed: (batchId: string) => {
    send({ type: 'set-csrf', csrfToken: getCsrfToken() })
    send({ type: 'retry-batch-failed', batchId })
  },
  cancelBatch: (batchId: string) => send({ type: 'cancel-batch', batchId }),
  pauseItem: (itemId: string) => send({ type: 'pause-item', itemId }),
  resumeItem: (itemId: string) => {
    send({ type: 'set-csrf', csrfToken: getCsrfToken() })
    send({ type: 'resume-item', itemId })
  },
  cancelItem: (itemId: string) => send({ type: 'cancel-item', itemId }),
  clearFinished: () => send({ type: 'clear-finished' }),
}
