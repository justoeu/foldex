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

const PRECACHE_PREFIX = 'foldex-precache-'
const LEGACY_PRECACHE = 'foldex-precache-v3'
const PRECACHE_STATE_CACHE = 'foldex-precache-state-v1'
const PRECACHE_STATE_KEY = new URL('/__foldex_precache_state__', self.location.origin).href
const FILES_CACHE = 'foldex-files-v1'
// LRU-ish bound for /api/files/* entries. Cache.keys() returns insertion
// order in Chromium; drop the oldest until under the cap.
export const FILES_CACHE_MAX_ENTRIES = 200

// Compute the precache key once per build — revisions in `__WB_MANIFEST`
// only change when an asset's content does, so the lookup is content-hash
// stable.
const PRECACHE_MANIFEST = self.__WB_MANIFEST ?? []
const PRECACHE_URLS = PRECACHE_MANIFEST.map((entry) =>
  entry.revision ? `${entry.url}?rev=${entry.revision}` : entry.url,
)
const PRECACHE = `${PRECACHE_PREFIX}${manifestHash(PRECACHE_MANIFEST)}`

interface PrecacheState {
  current: string
  previous?: string
}

let precacheState: PrecacheState | undefined

function manifestHash(entries: Array<{ url: string; revision: string | null }>): string {
  const fnvOffsetBasis = 2166136261
  const fnvPrime = 16777619
  let hash = fnvOffsetBasis
  for (const char of entries.map((entry) => `${entry.url}:${entry.revision ?? ''}`).join('|')) {
    hash ^= char.charCodeAt(0)
    hash = Math.imul(hash, fnvPrime)
  }
  return (hash >>> 0).toString(36)
}

async function readPrecacheState(): Promise<PrecacheState | null> {
  try {
    const cache = await caches.open(PRECACHE_STATE_CACHE)
    const response = await cache.match(PRECACHE_STATE_KEY)
    if (!response) return null
    const value: unknown = await response.json()
    if (typeof value !== 'object' || value === null) return null
    const state = value as Record<string, unknown>
    if (typeof state.current !== 'string') return null
    if (state.previous !== undefined && typeof state.previous !== 'string') return null
    return { current: state.current, previous: state.previous as string | undefined }
  } catch {
    return null
  }
}

async function getPrecacheState(): Promise<PrecacheState> {
  if (precacheState) return precacheState
  precacheState = (await readPrecacheState()) ?? { current: PRECACHE }
  return precacheState
}

async function writePrecacheState(state: PrecacheState): Promise<void> {
  const cache = await caches.open(PRECACHE_STATE_CACHE)
  await cache.put(PRECACHE_STATE_KEY, new Response(JSON.stringify(state)))
  precacheState = state
}

self.addEventListener('install', (event) => {
  event.waitUntil(
    (async () => {
      const storedState = await readPrecacheState()
      const cacheNames = await caches.keys()
      const previous = storedState?.current === PRECACHE
        ? storedState.previous
        : storedState?.current ?? (cacheNames.includes(LEGACY_PRECACHE) ? LEGACY_PRECACHE : undefined)
      const cache = await caches.open(PRECACHE)
      // Fetch each precache target with explicit Request so the cache key
      // ("?rev=..." suffix) matches what `match()` will lookup later.
      await Promise.all(
        PRECACHE_URLS.map((url) => cache.add(new Request(url, { cache: 'reload' }))),
      )
      await writePrecacheState({ current: PRECACHE, previous })
      // skipWaiting + clientsClaim ensures a new SW activates immediately
      // instead of waiting for every Foldex tab to close.
      await self.skipWaiting()
    })(),
  )
})

self.addEventListener('activate', (event) => {
  event.waitUntil(
    (async () => {
      const state = await getPrecacheState()
      const retained = new Set([
        state.current,
        state.previous,
        PRECACHE_STATE_CACHE,
        FILES_CACHE,
      ])
      const names = await caches.keys()
      await Promise.all(
        names
          .filter((name) => !retained.has(name))
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

  // Cross-origin GETs (og:image, favicon, Google Fonts CSS) must not go
  // through respondWith. The SW's fetch() is a connect-src action; the
  // document CSP is connect-src 'self', while img-src/style-src/font-src
  // already allow those hosts. Intercepting turns a legal <img src=https:…>
  // into a refused connect.
  if (url.origin !== self.location.origin) return

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
      // Await put + prune so FILES_CACHE stays bounded (LEAK-HYD-008).
      try {
        await cache.put(req, res.clone())
        if (cacheName === FILES_CACHE) {
          await pruneCacheLRU(cache, FILES_CACHE_MAX_ENTRIES)
        }
      } catch {
        // Quota / opaque failures are non-fatal — still return the network response.
      }
    }
    return res
  } catch {
    const cached = await cache.match(req)
    if (cached) return cached
    throw new Error('offline + no cache')
  }
}

/** Drop oldest entries until cache has at most maxEntries keys. */
export async function pruneCacheLRU(cache: Cache, maxEntries: number): Promise<void> {
  if (maxEntries <= 0) return
  const keys = await cache.keys()
  if (keys.length <= maxEntries) return
  const excess = keys.length - maxEntries
  for (let i = 0; i < excess; i++) {
    await cache.delete(keys[i]!)
  }
}

async function cacheFirst(req: Request): Promise<Response> {
  const cached = await matchPrecache(req)
  if (cached) return cached
  return fetch(req)
}

async function matchPrecache(req: Request | string): Promise<Response | undefined> {
  const state = await getPrecacheState()
  for (const name of [state.current, state.previous]) {
    if (!name) continue
    const cached = await (await caches.open(name)).match(req, { ignoreSearch: true })
    if (cached) return cached
  }
  return undefined
}

async function navigationFallback(req: Request): Promise<Response> {
  try {
    return await fetch(req)
  } catch {
    const offline = await matchPrecache('/index.html')
    if (offline) return offline
    throw new Error('offline + no shell cached')
  }
}

// ---- Web Push -----

interface PushPayload {
  link_id: number
  title: string
  url: string
  kind: 'change_detected' | 'test'
}

function isPushPayload(value: unknown): value is PushPayload {
  if (typeof value !== 'object' || value === null || Array.isArray(value)) return false
  const payload = value as Record<string, unknown>
  return (
    typeof payload.link_id === 'number' &&
    Number.isSafeInteger(payload.link_id) &&
    payload.link_id >= 0 &&
    typeof payload.title === 'string' &&
    typeof payload.url === 'string' &&
    (payload.kind === 'change_detected' || payload.kind === 'test')
  )
}

function showGenericNotification(): Promise<void> {
  return self.registration.showNotification('Foldex', {
    body: 'A bookmarked page was updated.',
    icon: '/favicon.svg',
    badge: '/favicon.svg',
  })
}

self.addEventListener('push', (event) => {
  if (!event.data) return
  let payload: unknown
  try {
    payload = event.data.json()
  } catch {
    // Malformed payload — show a generic notification so the user at least
    // sees that something happened instead of silent drops.
    event.waitUntil(showGenericNotification())
    return
  }
  if (!isPushPayload(payload)) {
    event.waitUntil(showGenericNotification())
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
