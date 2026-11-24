export interface DroppedFile {
  file: File
  /** Relative folder path inside the drop (e.g. "2026/Trips" or ""). */
  relativeDir: string
}

export interface DroppedBatch {
  label: string
  files: DroppedFile[]
}

interface FsEntry {
  isFile: boolean
  isDirectory: boolean
  name: string
  file?: (cb: (file: File) => void, err?: (e: unknown) => void) => void
  createReader?: () => { readEntries: (cb: (entries: FsEntry[]) => void, err?: (e: unknown) => void) => void }
}

function readAllDirectoryEntries(reader: NonNullable<FsEntry['createReader']> extends () => infer R ? R : never): Promise<FsEntry[]> {
  return new Promise((resolve) => {
    const all: FsEntry[] = []
    const step = () => {
      reader.readEntries(
        (batch) => {
          if (batch.length === 0) resolve(all)
          else {
            all.push(...batch)
            step()
          }
        },
        () => resolve(all),
      )
    }
    step()
  })
}

function entryToFile(entry: FsEntry): Promise<File | null> {
  return new Promise((resolve) => {
    if (!entry.file) return resolve(null)
    entry.file(
      (f) => resolve(f),
      () => resolve(null),
    )
  })
}

/**
 * Recursively collects files from a drag-and-drop event, supporting nested
 * folders via webkitGetAsEntry() while yielding periodically to keep the UI responsive.
 */
export async function collectDroppedBatch(dt: DataTransfer): Promise<DroppedBatch> {
  const items = Array.from(dt.items ?? [])
  const roots: FsEntry[] = []

  for (const item of items) {
    const getEntry = (item as DataTransferItem & { webkitGetAsEntry?: () => FsEntry | null }).webkitGetAsEntry
    if (typeof getEntry === 'function') {
      const entry = getEntry.call(item)
      if (entry) roots.push(entry)
    }
  }

  // Fallback for browsers without webkitGetAsEntry.
  if (roots.length === 0) {
    const plain = Array.from(dt.files ?? []).map((file) => ({ file, relativeDir: '' }))
    return {
      label: plain.length === 1 ? (plain[0]?.file.name ?? 'Upload') : `${plain.length} files`,
      files: plain,
    }
  }

  const out: DroppedFile[] = []
  const queue: Array<{ entry: FsEntry; dir: string }> = roots.map((entry) => ({ entry, dir: '' }))
  let processed = 0

  while (queue.length > 0) {
    const current = queue.shift()!
    if (current.entry.isFile) {
      const f = await entryToFile(current.entry)
      if (f) out.push({ file: f, relativeDir: current.dir })
    } else if (current.entry.isDirectory && current.entry.createReader) {
      const nextDir = current.dir ? `${current.dir}/${current.entry.name}` : current.entry.name
      const children = await readAllDirectoryEntries(current.entry.createReader())
      for (const child of children) {
        queue.push({ entry: child, dir: nextDir })
      }
    }

    processed++
    if (processed % 150 === 0) {
      await new Promise((r) => setTimeout(r, 0))
    }
  }

  const label =
    roots.length === 1 && roots[0]
      ? roots[0].name
      : `${out.length.toLocaleString()} ${out.length === 1 ? 'file' : 'files'}`

  return { label, files: out }
}

/** Collects files from an <input type="file" multiple> or webkitdirectory picker. */
export function collectPickerBatch(fileList: FileList): DroppedBatch {
  const arr = Array.from(fileList)
  let rootFolder = ''
  const files: DroppedFile[] = arr.map((file) => {
    const rel = (file as File & { webkitRelativePath?: string }).webkitRelativePath ?? ''
    if (rel.includes('/')) {
      const parts = rel.split('/')
      if (!rootFolder && parts[0]) rootFolder = parts[0]
      return { file, relativeDir: parts.slice(0, -1).join('/') }
    }
    return { file, relativeDir: '' }
  })

  const label = rootFolder || (files.length === 1 ? (files[0]?.file.name ?? 'Upload') : `${files.length.toLocaleString()} files`)
  return { label, files }
}
