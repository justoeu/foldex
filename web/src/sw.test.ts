import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'

type Handler = (event: any) => void

const handlers: Record<string, Handler[]> = {}
const cacheStore = new Map<string, Map<string, Response>>()

function cacheKey(req: Request | string, ignoreSearch = false) {
  const url = new URL(typeof req === 'string' ? req : req.url, 'https://x.test')
  if (ignoreSearch) url.search = ''
  return url.href
}

function makeCache(name: string) {
  if (!cacheStore.has(name)) cacheStore.set(name, new Map())
  const store = cacheStore.get(name)!
  return {
    add: vi.fn(async (req: Request | string) => {
      const key = cacheKey(req)
      store.set(key, new Response('ok', { status: 200 }))
    }),
    put: vi.fn(async (req: Request | string, res: Response) => {
      const key = cacheKey(req)
      store.set(key, res)
    }),
    match: vi.fn(async (req: Request | string, options?: CacheQueryOptions) => {
      const key = cacheKey(req, options?.ignoreSearch)
      if (store.has(key)) return store.get(key)!.clone()
      if (options?.ignoreSearch) {
        for (const [k, v] of store) {
          if (cacheKey(k, true) === key) return v.clone()
        }
      }
      return undefined
    }),
    keys: vi.fn(async () => [...store.keys()].map((k) => new Request(k))),
    delete: vi.fn(async (req: Request | string) => {
      const key = typeof req === 'string' ? req : req.url
      return store.delete(key)
    }),
  }
}

function here(path: string): string {
  return new URL(path, self.location.origin).href
}

function makeExtendableEvent(extra: Record<string, unknown> = {}) {
  let waitPromise: Promise<unknown> = Promise.resolve()
  return {
    ...extra,
    waitUntil(p: Promise<unknown>) {
      waitPromise = p
    },
    async flush() {
      await waitPromise
    },
  }
}

describe('service worker (sw.ts)', () => {
  beforeEach(async () => {
    vi.resetModules()
    cacheStore.clear()
    for (const k of Object.keys(handlers)) delete handlers[k]

    const g = globalThis as any
    g.__WB_MANIFEST = [
      { url: 'https://x.test/index.html', revision: 'abc' },
      { url: 'https://x.test/assets/app.js', revision: null },
    ]
    g.skipWaiting = vi.fn()
    g.clients = {
      claim: vi.fn(async () => undefined),
      matchAll: vi.fn(async () => []),
      openWindow: vi.fn(async () => null),
    }
    g.registration = {
      showNotification: vi.fn(async () => undefined),
    }

    vi.spyOn(self, 'addEventListener').mockImplementation((type: string, listener: any) => {
      ;(handlers[type] ??= []).push(listener)
    })

    g.caches = {
      open: vi.fn(async (name: string) => makeCache(name)),
      keys: vi.fn(async () => ['foldex-precache-v3', 'foldex-files-v1', 'foldex-old']),
      delete: vi.fn(async (name: string) => {
        cacheStore.delete(name)
        return true
      }),
      match: vi.fn(async () => undefined),
    }

    await import('./sw')
  })

  afterEach(() => {
    vi.restoreAllMocks()
  })

  it('install precaches manifest entries and calls skipWaiting', async () => {
    const ev = makeExtendableEvent()
    handlers.install![0](ev)
    await ev.flush()
    expect((globalThis as any).skipWaiting).toHaveBeenCalled()
    expect((globalThis as any).caches.open).toHaveBeenCalledWith(
      expect.stringMatching(/^foldex-precache-(?!state)/),
    )
  })

  it('offlineNavigationMatchesRevisionedShell', async () => {
    const install = makeExtendableEvent()
    handlers.install![0](install)
    await install.flush()

    vi.stubGlobal('fetch', vi.fn(async () => {
      throw new Error('offline')
    }))
    let responded: Promise<Response> | undefined
    handlers.fetch![0]({
      request: { url: here('/folder'), method: 'GET', mode: 'navigate' },
      respondWith(p: Promise<Response>) {
        responded = p
      },
    })

    expect(await (await responded!).text()).toBe('ok')
  })

  it('retains and serves the prior asset generation after activation', async () => {
    const previous = makeCache('foldex-precache-v3')
    await previous.put(here('/assets/old-lazy.js'), new Response('old lazy chunk'))

    const install = makeExtendableEvent()
    handlers.install![0](install)
    await install.flush()
    const activate = makeExtendableEvent()
    handlers.activate![0](activate)
    await activate.flush()

    let responded: Promise<Response> | undefined
    handlers.fetch![0]({
      request: new Request(here('/assets/old-lazy.js')),
      respondWith(p: Promise<Response>) {
        responded = p
      },
    })

    expect((globalThis as any).caches.delete).not.toHaveBeenCalledWith('foldex-precache-v3')
    expect(await (await responded!).text()).toBe('old lazy chunk')
  })

  it('rotates three hashed generations and evicts only the oldest', async () => {
    const stateKey = new URL('/__foldex_precache_state__', self.location.origin).href
    const stateCache = makeCache('foldex-precache-state-v1')
    await stateCache.put(stateKey, new Response(JSON.stringify({
      current: 'foldex-precache-generation-before-this-build',
      previous: 'foldex-precache-two-generations-old',
    })))
    await makeCache('foldex-precache-generation-before-this-build').put(here('/assets/prior-lazy.js'), new Response('prior'))
    await makeCache('foldex-precache-two-generations-old').put('/assets/oldest-lazy.js', new Response('oldest'))
    ;(globalThis as any).caches.keys = vi.fn(async () => [
      ...cacheStore.keys(),
      'foldex-files-v1',
    ])

    const install = makeExtendableEvent()
    handlers.install![0](install)
    await install.flush()
    const activate = makeExtendableEvent()
    handlers.activate![0](activate)
    await activate.flush()

    const stored = await stateCache.match(stateKey)
    const state = await stored!.json() as { current: string; previous: string }
    expect(state.current).toMatch(/^foldex-precache-(?!state)/)
    expect(state.previous).toBe('foldex-precache-generation-before-this-build')
    expect((globalThis as any).caches.delete).toHaveBeenCalledWith('foldex-precache-two-generations-old')
    expect((globalThis as any).caches.delete).not.toHaveBeenCalledWith('foldex-precache-generation-before-this-build')

    let responded: Promise<Response> | undefined
    handlers.fetch![0]({
      request: new Request(here('/assets/prior-lazy.js')),
      respondWith(p: Promise<Response>) {
        responded = p
      },
    })
    expect(await (await responded!).text()).toBe('prior')
  })

  it('fails installation when any precache asset cannot be cached', async () => {
    const cache = makeCache('failed-precache')
    cache.add.mockImplementation(async (req: Request | string) => {
      if (new URL(typeof req === 'string' ? req : req.url).pathname === '/assets/app.js') {
        throw new Error('asset unavailable')
      }
    })
    ;(globalThis as any).caches.open = vi.fn(async () => cache)

    const ev = makeExtendableEvent()
    handlers.install![0](ev)

    await expect(ev.flush()).rejects.toThrow('asset unavailable')
    expect((globalThis as any).skipWaiting).not.toHaveBeenCalled()
  })

  it('activate evicts unknown caches and claims clients', async () => {
    const install = makeExtendableEvent()
    handlers.install![0](install)
    await install.flush()
    const ev = makeExtendableEvent()
    handlers.activate![0](ev)
    await ev.flush()
    expect((globalThis as any).caches.delete).toHaveBeenCalledWith('foldex-old')
    expect((globalThis as any).clients.claim).toHaveBeenCalled()
  })

  it('fetch ignores non-GET and non-file API routes', () => {
    const respondWith = vi.fn()
    handlers.fetch![0]({
      request: new Request('https://x.test/api/links', { method: 'POST' }),
      respondWith,
    })
    handlers.fetch![0]({
      request: new Request('https://x.test/api/links'),
      respondWith,
    })
    handlers.fetch![0]({
      request: new Request('https://x.test/go/1'),
      respondWith,
    })
    handlers.fetch![0]({
      request: new Request('https://x.test/healthz'),
      respondWith,
    })
    expect(respondWith).not.toHaveBeenCalled()
  })

  it('fetch does not intercept cross-origin GETs (SW fetch is connect-src)', () => {
    const respondWith = vi.fn()
    handlers.fetch![0]({
      request: new Request('https://www.cambioreal.com/img/social-thumb.png'),
      respondWith,
    })
    handlers.fetch![0]({
      request: new Request('https://fonts.googleapis.com/css2?family=Nunito+Sans'),
      respondWith,
    })
    expect(respondWith).not.toHaveBeenCalled()
  })

  it('fetch uses networkFirst for /api/files/*', async () => {
    const body = new Response('img', { status: 200 })
    vi.stubGlobal('fetch', vi.fn(async () => body.clone()))
    let responded: Promise<Response> | undefined
    handlers.fetch![0]({
      request: new Request(here('/api/files/links/1.jpg')),
      respondWith(p: Promise<Response>) {
        responded = p
      },
    })
    const res = await responded!
    expect(res.status).toBe(200)
    expect(await res.text()).toBe('img')
  })

  it('FILES_CACHE prunes to max entries after put', async () => {
    const { pruneCacheLRU, FILES_CACHE_MAX_ENTRIES } = await import('./sw')
    const cache = makeCache('foldex-files-v1')
    const max = 5
    for (let i = 0; i < max + 3; i++) {
      await cache.put(new Request(`https://x.test/api/files/${i}.jpg`), new Response('x'))
    }
    expect((await cache.keys()).length).toBe(max + 3)
    await pruneCacheLRU(cache as any, max)
    expect((await cache.keys()).length).toBe(max)
    // Oldest keys dropped first (insertion order).
    expect(await cache.match(new Request('https://x.test/api/files/0.jpg'))).toBeUndefined()
    expect(await cache.match(new Request('https://x.test/api/files/3.jpg'))).toBeTruthy()
    expect(FILES_CACHE_MAX_ENTRIES).toBe(200)
  })

  it('networkFirst bounds FILES_CACHE via prune after many puts', async () => {
    const cache = makeCache('foldex-files-v1')
    ;(globalThis as any).caches.open = vi.fn(async () => cache)
    // Seed near the real cap so a few network puts trigger prune.
    const { FILES_CACHE_MAX_ENTRIES } = await import('./sw')
    for (let i = 0; i < FILES_CACHE_MAX_ENTRIES; i++) {
      await cache.put(new Request(here(`/api/files/seed-${i}.jpg`)), new Response('s'))
    }
    vi.stubGlobal('fetch', vi.fn(async (req: Request) => new Response('img', { status: 200 })))
    for (let i = 0; i < 5; i++) {
      let responded: Promise<Response> | undefined
      handlers.fetch![0]({
        request: new Request(here(`/api/files/new-${i}.jpg`)),
        respondWith(p: Promise<Response>) {
          responded = p
        },
      })
      await responded!
    }
    expect((await cache.keys()).length).toBeLessThanOrEqual(FILES_CACHE_MAX_ENTRIES)
  })

  it('networkFirst falls back to cache when offline', async () => {
    const cache = makeCache('foldex-files-v1')
    await cache.put(new Request(here('/api/files/x.png')), new Response('cached', { status: 200 }))
    ;(globalThis as any).caches.open = vi.fn(async () => cache)
    vi.stubGlobal('fetch', vi.fn(async () => {
      throw new Error('offline')
    }))
    let responded: Promise<Response> | undefined
    handlers.fetch![0]({
      request: new Request(here('/api/files/x.png')),
      respondWith(p: Promise<Response>) {
        responded = p
      },
    })
    const res = await responded!
    expect(await res.text()).toBe('cached')
  })

  it('networkFirst throws when offline and cache miss', async () => {
    ;(globalThis as any).caches.open = vi.fn(async () => makeCache('foldex-files-v1'))
    vi.stubGlobal('fetch', vi.fn(async () => {
      throw new Error('offline')
    }))
    let responded: Promise<Response> | undefined
    handlers.fetch![0]({
      request: new Request(here('/api/files/miss.png')),
      respondWith(p: Promise<Response>) {
        responded = p
      },
    })
    await expect(responded!).rejects.toThrow(/offline/)
  })

  it('fetch uses navigation fallback for navigate mode', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => new Response('live', { status: 200 })))
    let responded: Promise<Response> | undefined
    handlers.fetch![0]({
      request: { url: here('/'), method: 'GET', mode: 'navigate' },
      respondWith(p: Promise<Response>) {
        responded = p
      },
    })
    expect(await (await responded!).text()).toBe('live')
  })

  it('navigation fallback serves /index.html when offline', async () => {
    const cache = makeCache('foldex-precache-v3')
    await cache.put('/index.html', new Response('<html>shell</html>', { status: 200 }))
    ;(globalThis as any).caches.open = vi.fn(async () => cache)
    vi.stubGlobal('fetch', vi.fn(async () => {
      throw new Error('offline')
    }))
    let responded: Promise<Response> | undefined
    handlers.fetch![0]({
      request: { url: here('/folder'), method: 'GET', mode: 'navigate' },
      respondWith(p: Promise<Response>) {
        responded = p
      },
    })
    expect(await (await responded!).text()).toBe('<html>shell</html>')
  })

  it('navigation fallback throws when offline and shell missing', async () => {
    ;(globalThis as any).caches.open = vi.fn(async () => makeCache('foldex-precache-v3'))
    vi.stubGlobal('fetch', vi.fn(async () => {
      throw new Error('offline')
    }))
    let responded: Promise<Response> | undefined
    handlers.fetch![0]({
      request: { url: here('/'), method: 'GET', mode: 'navigate' },
      respondWith(p: Promise<Response>) {
        responded = p
      },
    })
    await expect(responded!).rejects.toThrow(/offline/)
  })

  it('fetch uses cacheFirst for static assets', async () => {
    const cache = makeCache('foldex-precache-v3')
    await cache.put(new Request(here('/assets/app.js')), new Response('js', { status: 200 }))
    ;(globalThis as any).caches.open = vi.fn(async () => cache)
    let responded: Promise<Response> | undefined
    handlers.fetch![0]({
      request: new Request(here('/assets/app.js')),
      respondWith(p: Promise<Response>) {
        responded = p
      },
    })
    expect(await (await responded!).text()).toBe('js')
  })

  it('cacheFirst falls through to network on miss', async () => {
    ;(globalThis as any).caches.open = vi.fn(async () => makeCache('foldex-precache-v3'))
    vi.stubGlobal('fetch', vi.fn(async () => new Response('net', { status: 200 })))
    let responded: Promise<Response> | undefined
    handlers.fetch![0]({
      request: new Request(here('/assets/miss.js')),
      respondWith(p: Promise<Response>) {
        responded = p
      },
    })
    expect(await (await responded!).text()).toBe('net')
  })

  it('push with no data is a no-op', () => {
    handlers.push![0]({ data: null, waitUntil: vi.fn() })
    expect((globalThis as any).registration.showNotification).not.toHaveBeenCalled()
  })

  it('push with malformed JSON shows generic notification', async () => {
    const ev = makeExtendableEvent({
      data: {
        json: () => {
          throw new Error('bad json')
        },
      },
    })
    handlers.push![0](ev)
    await ev.flush()
    expect((globalThis as any).registration.showNotification).toHaveBeenCalledWith(
      'Foldex',
      expect.objectContaining({ body: expect.stringMatching(/updated/i) }),
    )
  })

  it.each([
    ['null', null],
    ['array', []],
    ['string', 'payload'],
    ['number', 42],
    ['boolean', true],
    ['incomplete object', { title: 'Missing fields' }],
    ['invalid id', { kind: 'change_detected', link_id: '7', title: 'Title', url: '/go/title' }],
    ['invalid title', { kind: 'change_detected', link_id: 7, title: 7, url: '/go/title' }],
    ['invalid url', { kind: 'change_detected', link_id: 7, title: 'Title', url: null }],
  ])('push with valid JSON %s shows generic notification', async (_name, payload) => {
    const ev = makeExtendableEvent({
      data: { json: () => payload },
    })

    expect(() => handlers.push![0](ev)).not.toThrow()
    await ev.flush()
    expect((globalThis as any).registration.showNotification).toHaveBeenCalledWith(
      'Foldex',
      expect.objectContaining({ body: expect.stringMatching(/updated/i) }),
    )
  })

  it('push with change_detected payload shows link title', async () => {
    const ev = makeExtendableEvent({
      data: {
        json: () => ({
          link_id: 7,
          title: 'HN',
          url: 'https://news.ycombinator.com',
          kind: 'change_detected',
        }),
      },
    })
    handlers.push![0](ev)
    await ev.flush()
    expect((globalThis as any).registration.showNotification).toHaveBeenCalledWith(
      'HN',
      expect.objectContaining({
        tag: 'foldex-link-7',
        data: expect.objectContaining({ link_id: 7 }),
      }),
    )
  })

  it('push with test kind shows test copy', async () => {
    const ev = makeExtendableEvent({
      data: {
        json: () => ({ link_id: 0, title: '', url: '', kind: 'test' }),
      },
    })
    handlers.push![0](ev)
    await ev.flush()
    expect((globalThis as any).registration.showNotification).toHaveBeenCalledWith(
      'Foldex test notification',
      expect.objectContaining({ body: expect.stringMatching(/working/i) }),
    )
  })

  it('push falls back to default title when title empty', async () => {
    const ev = makeExtendableEvent({
      data: {
        json: () => ({ link_id: 3, title: '', url: 'https://x', kind: 'change_detected' }),
      },
    })
    handlers.push![0](ev)
    await ev.flush()
    expect((globalThis as any).registration.showNotification).toHaveBeenCalledWith(
      'Foldex update',
      expect.any(Object),
    )
  })

  it('notificationclick opens window when no client exists', async () => {
    const close = vi.fn()
    const ev = makeExtendableEvent({
      notification: { close, data: { link_id: 9, kind: 'change_detected' } },
    })
    handlers.notificationclick![0](ev)
    await ev.flush()
    expect(close).toHaveBeenCalled()
    expect((globalThis as any).clients.openWindow).toHaveBeenCalledWith('/go/9')
  })

  it('notificationclick for test kind opens root', async () => {
    const ev = makeExtendableEvent({
      notification: { close: vi.fn(), data: { kind: 'test' } },
    })
    handlers.notificationclick![0](ev)
    await ev.flush()
    expect((globalThis as any).clients.openWindow).toHaveBeenCalledWith('/')
  })

  it('notificationclick reuses focused client and navigates', async () => {
    const focus = vi.fn(async () => undefined)
    const navigate = vi.fn(async () => undefined)
    ;(globalThis as any).clients.matchAll = vi.fn(async () => [{ focus, navigate }])
    const ev = makeExtendableEvent({
      notification: { close: vi.fn(), data: { link_id: 4, kind: 'change_detected' } },
    })
    handlers.notificationclick![0](ev)
    await ev.flush()
    expect(focus).toHaveBeenCalled()
    expect(navigate).toHaveBeenCalledWith('/go/4')
    expect((globalThis as any).clients.openWindow).not.toHaveBeenCalled()
  })

  it('notificationclick falls back to openWindow when navigate throws', async () => {
    const focus = vi.fn(async () => undefined)
    const navigate = vi.fn(async () => {
      throw new Error('blocked')
    })
    ;(globalThis as any).clients.matchAll = vi.fn(async () => [{ focus, navigate }])
    const ev = makeExtendableEvent({
      notification: { close: vi.fn(), data: { link_id: 5, kind: 'change_detected' } },
    })
    handlers.notificationclick![0](ev)
    await ev.flush()
    expect((globalThis as any).clients.openWindow).toHaveBeenCalledWith('/go/5')
  })

  it('notificationclick with empty data opens root', async () => {
    const ev = makeExtendableEvent({
      notification: { close: vi.fn(), data: null },
    })
    handlers.notificationclick![0](ev)
    await ev.flush()
    expect((globalThis as any).clients.openWindow).toHaveBeenCalledWith('/')
  })
})
