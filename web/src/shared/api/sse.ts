import { useEffect, useRef } from 'react'
import { API_BASE } from './client'

type Listener = (raw: string) => void

type ChannelMessage =
  | { type: 'ping'; tabId: string }
  | { type: 'leader'; tabId: string }
  | { type: 'resign'; tabId: string }
  | { type: 'subscribe'; eventTypes: string[] }
  | { type: 'event'; eventType: string; raw: string }

const CHANNEL_NAME = 'netshare-sse-v1'
const HEARTBEAT_MS = 2000
const LEADER_TIMEOUT_MS = 4500
const INITIAL_ELECTION_MS = 300

const tabId =
  typeof crypto !== 'undefined' && 'randomUUID' in crypto
    ? crypto.randomUUID()
    : `${Date.now()}-${Math.random().toString(36).slice(2)}`

const listeners = new Map<string, Set<Listener>>()
const attachedTypes = new Set<string>()
const peerEventTypes = new Set<string>()

let channel: BroadcastChannel | null = null
let source: EventSource | null = null
let isLeader = false
let leaderId: string | null = null
let lastLeaderSeen = 0

let reconnectTimer: number | null = null
let heartbeatTimer: number | null = null
let watchdogTimer: number | null = null
let electionTimer: number | null = null
let backoffMs = 1000

function totalLocalListeners(): number {
  let count = 0
  for (const set of listeners.values()) count += set.size
  return count
}

function dispatchLocal(eventType: string, raw: string) {
  const set = listeners.get(eventType)
  if (!set) return
  for (const fn of set) fn(raw)
}

function post(msg: ChannelMessage) {
  try {
    channel?.postMessage(msg)
  } catch {
    // channel may be closed during unload
  }
}

function attachEvent(eventType: string) {
  if (!source || attachedTypes.has(eventType)) return
  attachedTypes.add(eventType)
  source.addEventListener(eventType, (event) => {
    const raw = (event as MessageEvent<string>).data
    dispatchLocal(eventType, raw)
    post({ type: 'event', eventType, raw })
  })
}

function allRequiredEventTypes(): string[] {
  const set = new Set<string>()
  for (const k of listeners.keys()) set.add(k)
  for (const k of peerEventTypes) set.add(k)
  return Array.from(set)
}

function openEventSource() {
  if (!isLeader || source) return

  const es = new EventSource(`${API_BASE}/events`, { withCredentials: true })
  source = es
  attachedTypes.clear()

  es.onopen = () => {
    backoffMs = 1000
  }

  for (const eventType of allRequiredEventTypes()) {
    attachEvent(eventType)
  }

  es.onerror = () => {
    es.close()
    if (source === es) {
      source = null
      attachedTypes.clear()
    }
    if (!isLeader || reconnectTimer !== null) return
    const delay = backoffMs
    backoffMs = Math.min(backoffMs * 2, 15000)
    reconnectTimer = window.setTimeout(() => {
      reconnectTimer = null
      openEventSource()
    }, delay)
  }
}

function closeEventSource() {
  if (reconnectTimer !== null) {
    window.clearTimeout(reconnectTimer)
    reconnectTimer = null
  }
  if (source) {
    source.close()
    source = null
    attachedTypes.clear()
  }
  backoffMs = 1000
}

function becomeLeader() {
  if (isLeader) return
  isLeader = true
  leaderId = tabId
  lastLeaderSeen = Date.now()
  if (electionTimer !== null) {
    window.clearTimeout(electionTimer)
    electionTimer = null
  }
  post({ type: 'leader', tabId })
  openEventSource()
  if (heartbeatTimer === null) {
    heartbeatTimer = window.setInterval(() => {
      if (!isLeader) return
      post({ type: 'leader', tabId })
    }, HEARTBEAT_MS)
  }
}

function stepDown(notifyPeers = true) {
  if (!isLeader) return
  isLeader = false
  if (leaderId === tabId) leaderId = null
  if (heartbeatTimer !== null) {
    window.clearInterval(heartbeatTimer)
    heartbeatTimer = null
  }
  closeEventSource()
  if (notifyPeers) post({ type: 'resign', tabId })
}

function scheduleElection(delayMs: number) {
  if (isLeader || electionTimer !== null || totalLocalListeners() === 0) return
  electionTimer = window.setTimeout(() => {
    electionTimer = null
    if (totalLocalListeners() === 0) return
    if (!leaderId || Date.now() - lastLeaderSeen > LEADER_TIMEOUT_MS) {
      becomeLeader()
    }
  }, delayMs)
}

function ensureChannel() {
  if (channel || typeof BroadcastChannel === 'undefined') return
  channel = new BroadcastChannel(CHANNEL_NAME)

  channel.onmessage = (e: MessageEvent<ChannelMessage>) => {
    const msg = e.data
    if (!msg) return
    switch (msg.type) {
      case 'ping':
        if (isLeader) {
          post({ type: 'leader', tabId })
        }
        break
      case 'leader':
        if (msg.tabId === tabId) break
        // Tie-breaker if two tabs promoted simultaneously: smaller tabId stays leader.
        if (isLeader && msg.tabId < tabId) {
          stepDown(false)
        }
        if (!isLeader) {
          leaderId = msg.tabId
          lastLeaderSeen = Date.now()
          if (electionTimer !== null) {
            window.clearTimeout(electionTimer)
            electionTimer = null
          }
          if (totalLocalListeners() > 0) {
            post({ type: 'subscribe', eventTypes: Array.from(listeners.keys()) })
          }
        }
        break
      case 'resign':
        if (leaderId === msg.tabId) {
          leaderId = null
          lastLeaderSeen = 0
          scheduleElection(100 + Math.floor(Math.random() * 150))
        }
        break
      case 'subscribe':
        for (const et of msg.eventTypes) {
          peerEventTypes.add(et)
          if (isLeader) attachEvent(et)
        }
        break
      case 'event':
        if (!isLeader) {
          dispatchLocal(msg.eventType, msg.raw)
        }
        break
    }
  }

  window.addEventListener('beforeunload', () => {
    if (isLeader) stepDown(true)
  })
}

function startTransport() {
  if (typeof BroadcastChannel === 'undefined') {
    // Fallback for environments without BroadcastChannel: single-tab direct SSE.
    isLeader = true
    openEventSource()
    return
  }

  ensureChannel()
  if (isLeader) {
    openEventSource()
    return
  }

  if (watchdogTimer === null) {
    watchdogTimer = window.setInterval(() => {
      if (isLeader || totalLocalListeners() === 0) return
      if (!leaderId || Date.now() - lastLeaderSeen > LEADER_TIMEOUT_MS) {
        becomeLeader()
      }
    }, HEARTBEAT_MS)
  }

  if (leaderId && Date.now() - lastLeaderSeen <= LEADER_TIMEOUT_MS) {
    post({ type: 'subscribe', eventTypes: Array.from(listeners.keys()) })
  } else {
    post({ type: 'ping', tabId })
    scheduleElection(INITIAL_ELECTION_MS)
  }
}

function stopIfIdle() {
  if (totalLocalListeners() > 0) return
  if (electionTimer !== null) {
    window.clearTimeout(electionTimer)
    electionTimer = null
  }
  if (watchdogTimer !== null) {
    window.clearInterval(watchdogTimer)
    watchdogTimer = null
  }
  if (isLeader) {
    stepDown(true)
  }
}

/**
 * Subscribe to a server-sent event type.
 * Multiple tabs automatically share ONE /api/v1/events connection via BroadcastChannel
 * leader election (preventing HTTP/1.1 6-connection exhaustion on self-hosted LANs).
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

  if (totalLocalListeners() === 1) {
    startTransport()
  } else if (isNewType) {
    if (isLeader) attachEvent(eventType)
    else post({ type: 'subscribe', eventTypes: [eventType] })
  }

  return () => {
    const current = listeners.get(eventType)
    if (!current) return
    current.delete(wrapped)
    if (current.size === 0) listeners.delete(eventType)
    stopIfIdle()
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
