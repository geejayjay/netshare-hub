// Offline shell & Android Share Target service worker for NetShare Hub PWA.
// Never caches /api/ requests so auth, SSE, and live data are always fresh.
const CACHE = 'netshare-shell-v2'
const SHARE_CACHE = 'netshare-share-target-v1'
const SHELL = ['/', '/favicon.svg', '/manifest.webmanifest']

self.addEventListener('install', (event) => {
  event.waitUntil(
    caches
      .open(CACHE)
      .then((cache) => cache.addAll(SHELL))
      .then(() => self.skipWaiting()),
  )
})

self.addEventListener('activate', (event) => {
  event.waitUntil(
    caches
      .keys()
      .then((keys) =>
        Promise.all(keys.filter((k) => k !== CACHE && k !== SHARE_CACHE).map((k) => caches.delete(k))),
      )
      .then(() => self.clients.claim()),
  )
})

self.addEventListener('fetch', (event) => {
  const req = event.request
  const url = new URL(req.url)
  if (url.origin !== self.location.origin) return

  // Handle Android PWA Share Target POST -> stash files in Cache Storage and redirect.
  if (req.method === 'POST' && url.pathname === '/share-target') {
    event.respondWith(
      (async () => {
        try {
          const form = await req.formData()
          const cache = await caches.open(SHARE_CACHE)
          const existing = await cache.keys()
          await Promise.all(existing.map((k) => cache.delete(k)))

          const manifest = []
          let idx = 0
          for (const val of form.getAll('files')) {
            if (val instanceof File) {
              const key = `/__shared/${idx++}/${encodeURIComponent(val.name)}`
              await cache.put(
                key,
                new Response(val, {
                  headers: {
                    'Content-Type': val.type || 'application/octet-stream',
                    'X-File-Name': encodeURIComponent(val.name),
                  },
                }),
              )
              manifest.push({ key, name: val.name, type: val.type, size: val.size })
            }
          }
          await cache.put(
            '/__shared/manifest.json',
            new Response(JSON.stringify(manifest), { headers: { 'Content-Type': 'application/json' } }),
          )
        } catch {
          // Ignore malformed share payloads and redirect anyway.
        }
        return Response.redirect('/share-target?received=1', 303)
      })(),
    )
    return
  }

  if (req.method !== 'GET' || url.pathname.startsWith('/api/')) return

  // Network-first for HTML navigations (fallback to cached shell offline).
  if (req.mode === 'navigate') {
    event.respondWith(fetch(req).catch(() => caches.match('/')))
    return
  }

  // Cache-first for hashed static assets.
  if (url.pathname.startsWith('/assets/')) {
    event.respondWith(
      caches.match(req).then(
        (hit) =>
          hit ||
          fetch(req).then((res) => {
            if (res.ok) {
              const clone = res.clone()
              void caches.open(CACHE).then((c) => c.put(req, clone))
            }
            return res
          }),
      ),
    )
  }
})
