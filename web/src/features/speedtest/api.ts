import { useMutation } from '@tanstack/react-query'
import { API_BASE, api, getCsrfToken, unwrap, type Schema } from '@/shared/api'

export type DiskTestRequest = Schema<'DiskTestRequest'>
export type DiskTestResult = Schema<'DiskTestResult'>

export function useDiskSpeedTest() {
  return useMutation({
    mutationFn: (body: DiskTestRequest) => unwrap(api.POST('/system/speedtest/disk', { body })),
  })
}

export async function measureLatency(): Promise<{ medianMs: number; jitterMs: number }> {
  const samples: number[] = []
  for (let i = 0; i < 5; i++) {
    const t0 = performance.now()
    await fetch(`${API_BASE}/health`, { credentials: 'include', cache: 'no-store' })
    samples.push(performance.now() - t0)
  }
  samples.sort((a, b) => a - b)
  const median = samples[Math.floor(samples.length / 2)] ?? 0
  let diffSum = 0
  for (let i = 1; i < samples.length; i++) {
    diffSum += Math.abs((samples[i] ?? 0) - (samples[i - 1] ?? 0))
  }
  const jitter = samples.length > 1 ? diffSum / (samples.length - 1) : 0
  return {
    medianMs: Math.round(median * 10) / 10,
    jitterMs: Math.round(jitter * 10) / 10,
  }
}

export async function measureDownload(
  sizeMiB: number,
  onProgress: (pct: number, liveMbps: number) => void,
): Promise<{ mbps: number; mBps: number; durationMs: number }> {
  const totalBytes = sizeMiB * 1024 * 1024
  const start = performance.now()
  const res = await fetch(`${API_BASE}/system/speedtest/download?mb=${sizeMiB}`, {
    credentials: 'include',
    cache: 'no-store',
  })
  if (!res.ok || !res.body) throw new Error(`Download test failed (${res.status})`)

  const reader = res.body.getReader()
  let received = 0
  let lastEmit = start

  while (true) {
    const { done, value } = await reader.read()
    if (done) break
    received += value.byteLength
    const now = performance.now()
    if (now - lastEmit >= 120) {
      lastEmit = now
      const sec = Math.max((now - start) / 1000, 0.001)
      const mbps = (received * 8) / (sec * 1_000_000)
      onProgress(Math.min(100, Math.round((received / totalBytes) * 100)), Math.round(mbps * 10) / 10)
    }
  }

  const durationMs = Math.max(performance.now() - start, 1)
  const sec = durationMs / 1000
  const mbps = (received * 8) / (sec * 1_000_000)
  const mBps = received / (sec * 1024 * 1024)
  onProgress(100, Math.round(mbps * 10) / 10)
  return {
    mbps: Math.round(mbps * 10) / 10,
    mBps: Math.round(mBps * 10) / 10,
    durationMs: Math.round(durationMs),
  }
}

export async function measureUpload(sizeMiB: number): Promise<{ mbps: number; mBps: number; durationMs: number }> {
  const chunk = new Uint8Array(1024 * 1024)
  for (let i = 0; i < chunk.length; i += 4096) chunk[i] = (i & 0xff) ^ 0x5a
  const parts: Uint8Array<ArrayBuffer>[] = []
  for (let i = 0; i < sizeMiB; i++) parts.push(chunk)
  const blob = new Blob(parts, { type: 'application/octet-stream' })

  const headers: Record<string, string> = { 'Content-Type': 'application/octet-stream' }
  const csrf = getCsrfToken()
  if (csrf) headers['X-CSRF-Token'] = csrf

  const start = performance.now()
  const res = await fetch(`${API_BASE}/system/speedtest/upload`, {
    method: 'POST',
    credentials: 'include',
    headers,
    body: blob,
  })
  if (!res.ok) throw new Error(`Upload test failed (${res.status})`)
  const data = (await res.json()) as { bytes: number; durationMs: number; mbps: number }
  const durationMs = Math.max(performance.now() - start, 1)
  const sec = durationMs / 1000
  const bytes = data.bytes || blob.size
  return {
    mbps: Math.round(((bytes * 8) / (sec * 1_000_000)) * 10) / 10,
    mBps: Math.round((bytes / (sec * 1024 * 1024)) * 10) / 10,
    durationMs: Math.round(durationMs),
  }
}
