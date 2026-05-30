/// <reference lib="webworker" />
// Custom Service Worker. Built by vite-plugin-pwa (`strategies: 'injectManifest'`)
// — Vite injects the precache manifest at the `self.__WB_MANIFEST` marker
// during build, so the SW source ships its own caching code instead of
// pulling in any workbox-* runtime packages. Two reasons for the
// hand-rolled approach:
//
//   1. The CLAUDE.md package manager is bun. Adding workbox-* would require
//      regenerating bun.lock, which the dev environment can't always do.
//      A handful of cache.put() calls beats a dependency tree.
//   2. The runtime surface here is small: precache build assets, runtime
//      NetworkFirst on /api/files/*, and the Web Push event listeners.

export {} // keep this file a module for the lib reference above

declare const self: ServiceWorkerGlobalScope & {
  __WB_MANIFEST: Array<{ url: string; revision: string | null }>
}

// Bumping this evicts the entire previous cache on activation — keep it in
// step with vite.config.ts `workbox.cacheId` for the older generateSW
// pipeline. New value lives ONLY here now that the SW is hand-written.
const PRECACHE = 'foldex-precache-v3'
const FILES_CACHE = 'foldex-files-v1'

// Compute the precache key once per build — revisions in `__WB_MANIFEST`
// only change when an asset's content does, so the lookup is content-hash
// stable.
const PRECACHE_URLS = (self.__WB_MANIFEST ?? []).map((entry) =>
  entry.revision ? `${entry.url}?rev=${entry.revision}` : entry.url,
)

self.addEventListener('install', (event) => {
  event.waitUntil(
    (async () => {
      const cache = await caches.open(PRECACHE)
      // Fetch each precache target with explicit Request so the cache key
      // ("?rev=..." suffix) matches what `match()` will lookup later.
      await Promise.all(
        PRECACHE_URLS.map((url) =>
          cache.add(new Request(url, { cache: 'reload' })).catch(() => undefined),
        ),
      )
    })(),
  )
  // skipWaiting + clientsClaim ensures a new SW activates immediately
  // instead of waiting for every Foldex tab to close — matches the
  // previous generateSW behaviour.
  self.skipWaiting()
})

self.addEventListener('activate', (event) => {
  event.waitUntil(
    (async () => {
      // Evict any cache whose name doesn't match our current set — this
      // includes the older `foldex-v2` cache from the generateSW era.
      const names = await caches.keys()
      await Promise.all(
        names
          .filter((name) => name !== PRECACHE && name !== FILES_CACHE)
          .map((name) => caches.delete(name)),
      )
      await self.clients.claim()
    })(),
  )
})

self.addEventListener('fetch', (event) => {
  const req = event.request
  if (req.method !== 'GET') return
  const url = new URL(req.url)

  // /api and /go must always reach the backend — no caching, no offline
  // fallback. They mutate state on click and the backend is the source of
  // truth.
  if (url.pathname.startsWith('/api/') && !url.pathname.startsWith('/api/files/')) return
  if (url.pathname.startsWith('/go/')) return
  if (url.pathname === '/healthz') return

  // /api/files/* are favicons + og:images proxied through the backend.
  // NetworkFirst with a 30-day fallback so refreshes land on next view but
  // offline still has the previous image.
  if (url.pathname.startsWith('/api/files/')) {
    event.respondWith(networkFirst(req, FILES_CACHE))
    return
  }

  // SPA navigation: fall back to /index.html when offline so the router
  // can mount and take over.
  if (req.mode === 'navigate') {
    event.respondWith(navigationFallback(req))
    return
  }

  // Everything else (built JS/CSS/assets) is precached — try the cache,
  // fall back to network if the precache missed.
  event.respondWith(cacheFirst(req))
})

async function networkFirst(req: Request, cacheName: string): Promise<Response> {
  const cache = await caches.open(cacheName)
  try {
    const res = await fetch(req)
    if (res && res.status === 200) {
      // Clone before stashing — Response bodies are single-use streams.
      cache.put(req, res.clone()).catch(() => undefined)
    }
    return res
  } catch {
    const cached = await cache.match(req)
    if (cached) return cached
    throw new Error('offline + no cache')
  }
}

async function cacheFirst(req: Request): Promise<Response> {
  const cache = await caches.open(PRECACHE)
  const cached = await cache.match(req, { ignoreSearch: true })
  if (cached) return cached
  return fetch(req)
}

async function navigationFallback(req: Request): Promise<Response> {
  try {
    return await fetch(req)
  } catch {
    const cache = await caches.open(PRECACHE)
    const offline = await cache.match('/index.html')
    if (offline) return offline
    throw new Error('offline + no shell cached')
  }
}

// ---- Web Push -----

interface PushPayload {
  link_id: number
  title: string
  url: string
  kind: string // "change_detected" | "test"
}

self.addEventListener('push', (event) => {
  if (!event.data) return
  let payload: PushPayload
  try {
    payload = event.data.json() as PushPayload
  } catch {
    // Malformed payload — show a generic notification so the user at least
    // sees that something happened instead of silent drops.
    event.waitUntil(
      self.registration.showNotification('Foldex', {
        body: 'A bookmarked page was updated.',
        icon: '/favicon.svg',
        badge: '/favicon.svg',
      }),
    )
    return
  }
  const isTest = payload.kind === 'test'
  const title = isTest ? 'Foldex test notification' : payload.title || 'Foldex update'
  const body = isTest
    ? 'Push setup is working.'
    : `This page was updated — click to open`
  event.waitUntil(
    self.registration.showNotification(title, {
      body,
      icon: '/favicon.svg',
      badge: '/favicon.svg',
      // Same `tag` for the same link replaces the previous notification
      // instead of stacking — avoids a wall of "X updated" for one link
      // that flickers between two states across multiple checks.
      tag: `foldex-link-${payload.link_id || 'misc'}`,
      data: { link_id: payload.link_id, url: payload.url, kind: payload.kind },
    }),
  )
})

self.addEventListener('notificationclick', (event) => {
  event.notification.close()
  const data = (event.notification.data ?? {}) as { link_id?: number; url?: string; kind?: string }
  // For tests, just focus or open the SPA root.
  const target = data.kind === 'test' || !data.link_id ? '/' : `/go/${data.link_id}`
  event.waitUntil(
    (async () => {
      const all = await self.clients.matchAll({ type: 'window', includeUncontrolled: true })
      // Reuse an existing Foldex tab if one is open.
      for (const client of all) {
        if ('focus' in client) {
          await client.focus()
          if ('navigate' in client) {
            try {
              await (client as WindowClient).navigate(target)
              return
            } catch {
              // Falls through to openWindow when navigate is blocked
              // (cross-origin, etc.).
            }
          }
        }
      }
      await self.clients.openWindow(target)
    })(),
  )
})
