import { useEffect, useRef } from 'react'
import { API_BASE } from './client'

type Listener = (raw: string) => void

const listeners = new Map<string, Set<Listener>>()
let source: EventSource | null = null
let reconnectTimer: number | null = null
let backoffMs = 1000

function totalListeners(): number {
  let count = 0
  for (const set of listeners.values()) count += set.size
  return count
}

function attachEvent(eventType: string) {
  if (!source) return
  source.addEventListener(eventType, (event) => {
    const data = (event as MessageEvent<string>).data
    const set = listeners.get(eventType)
    if (!set) return
    for (const fn of set) fn(data)
  })
}

function connect() {
  if (source || totalListeners() === 0) return

  const es = new EventSource(`${API_BASE}/events`, { withCredentials: true })
  source = es

  es.onopen = () => {
    backoffMs = 1000
  }

  for (const eventType of listeners.keys()) {
    attachEvent(eventType)
  }

  es.onerror = () => {
    es.close()
    if (source === es) source = null
    if (totalListeners() === 0 || reconnectTimer !== null) return
    const delay = backoffMs
    backoffMs = Math.min(backoffMs * 2, 15000)
    reconnectTimer = window.setTimeout(() => {
      reconnectTimer = null
      connect()
    }, delay)
  }
}

function disconnectIfIdle() {
  if (totalListeners() > 0) return
  if (reconnectTimer !== null) {
    window.clearTimeout(reconnectTimer)
    reconnectTimer = null
  }
  if (source) {
    source.close()
    source = null
  }
  backoffMs = 1000
}

/**
 * Subscribe to a server-sent event type on the shared per-tab connection.
 */
export function subscribeSSE<T = unknown>(eventType: string, handler: (data: T) => void): () => void {
  let set = listeners.get(eventType)
  const isNewType = !set
  if (!set) {
    set = new Set()
    listeners.set(eventType, set)
  }

  const wrapped: Listener = (raw) => {
    try {
      handler(JSON.parse(raw) as T)
    } catch {
      // Ignore malformed frames from server
    }
  }
  set.add(wrapped)

  if (!source) {
    connect()
  } else if (isNewType) {
    attachEvent(eventType)
  }

  return () => {
    const current = listeners.get(eventType)
    if (!current) return
    current.delete(wrapped)
    if (current.size === 0) listeners.delete(eventType)
    disconnectIfIdle()
  }
}

/** Subscribe to an SSE event type for the lifetime of a component. */
export function useSSE<T = unknown>(eventType: string, handler: (data: T) => void, enabled = true) {
  const handlerRef = useRef(handler)
  useEffect(() => {
    handlerRef.current = handler
  }, [handler])

  useEffect(() => {
    if (!enabled) return
    return subscribeSSE<T>(eventType, (data) => handlerRef.current(data))
  }, [eventType, enabled])
}
