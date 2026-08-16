import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import {
  getStoredSecret,
  setStoredSecret,
  authenticatedFetch,
  advanceAuthEpoch,
  http,
  readCsrfToken,
  resetRefreshState,
  setSessionLostHandler,
  CSRF_HEADER,
} from './client'

/** Drives the request interceptor without going over the wire. */
async function runRequestInterceptor(config: Record<string, unknown>) {
  const handlers = (http.interceptors.request as unknown as {
    handlers: Array<{ fulfilled?: (c: any) => any }>
  }).handlers
  const fulfilled = handlers.find((h) => h?.fulfilled)?.fulfilled
  return fulfilled!({ headers: {}, ...config })
}

describe('SHARED_SECRET client helpers', () => {
  beforeEach(() => {
    localStorage.clear()
  })

  afterEach(() => {
    localStorage.clear()
    vi.restoreAllMocks()
  })

  it('getStoredSecret returns empty when unset', () => {
    expect(getStoredSecret()).toBe('')
  })

  it('setStoredSecret round-trips and clears', () => {
    setStoredSecret('s3cret')
    expect(getStoredSecret()).toBe('s3cret')
    setStoredSecret('')
    expect(getStoredSecret()).toBe('')
    expect(localStorage.getItem('foldex.secret')).toBeNull()
  })

  it('request interceptor attaches X-Foldex-Secret when stored', async () => {
    setStoredSecret('my-secret')
    const handlers = (http.interceptors.request as unknown as { handlers: Array<{ fulfilled?: (c: any) => any }> }).handlers
    const fulfilled = handlers.find((h) => h?.fulfilled)?.fulfilled
    expect(fulfilled).toBeTypeOf('function')
    const cfg = await fulfilled!({ headers: {} as Record<string, string> })
    expect((cfg.headers as Record<string, string>)['X-Foldex-Secret']).toBe('my-secret')
  })

  it('request interceptor omits header when secret empty', async () => {
    setStoredSecret('')
    const handlers = (http.interceptors.request as unknown as { handlers: Array<{ fulfilled?: (c: any) => any }> }).handlers
    const fulfilled = handlers.find((h) => h?.fulfilled)?.fulfilled
    const cfg = await fulfilled!({ headers: {} as Record<string, string> })
    expect((cfg.headers as Record<string, string>)['X-Foldex-Secret']).toBeUndefined()
  })
})

describe('CSRF double-submit', () => {
  beforeEach(() => {
    document.cookie = 'fx_csrf=; expires=Thu, 01 Jan 1970 00:00:00 GMT; path=/'
  })

  it('reads the token from its cookie', () => {
    document.cookie = 'fx_csrf=abc123; path=/'
    expect(readCsrfToken()).toBe('abc123')
  })

  it('URL-decodes the cookie value', () => {
    document.cookie = 'fx_csrf=a%2Fb%3Dc; path=/'
    expect(readCsrfToken()).toBe('a/b=c')
  })

  it('returns empty when the cookie is absent', () => {
    expect(readCsrfToken()).toBe('')
  })

  it.each(['post', 'put', 'patch', 'delete'])('attaches the header on %s', async (method) => {
    document.cookie = 'fx_csrf=tok; path=/'
    const cfg = await runRequestInterceptor({ method })
    expect((cfg.headers as Record<string, string>)[CSRF_HEADER]).toBe('tok')
  })

  // Safe verbs carry no CSRF risk, and the backend ignores the header there —
  // sending it anyway would just be noise on every page load.
  it.each(['get', 'head'])('omits the header on %s', async (method) => {
    document.cookie = 'fx_csrf=tok; path=/'
    const cfg = await runRequestInterceptor({ method })
    expect((cfg.headers as Record<string, string>)[CSRF_HEADER]).toBeUndefined()
  })

  it('never overwrites a header the caller set explicitly', async () => {
    document.cookie = 'fx_csrf=cookie-token; path=/'
    const cfg = await runRequestInterceptor({
      method: 'post',
      headers: { [CSRF_HEADER]: 'explicit' },
    })
    expect((cfg.headers as Record<string, string>)[CSRF_HEADER]).toBe('explicit')
  })
})

describe('session cookies', () => {
  it('sends credentials on every request', () => {
    // Without withCredentials axios drops cookies on any cross-origin call —
    // which is exactly the dev setup (web :9088, API :9089). The session would
    // silently never be sent and every request would look anonymous.
    expect(http.defaults.withCredentials).toBe(true)
  })
})

describe('authenticatedFetch', () => {
  beforeEach(() => {
    resetRefreshState()
    localStorage.clear()
    document.cookie = 'fx_csrf=; expires=Thu, 01 Jan 1970 00:00:00 GMT; path=/'
  })

  it('adds session, secret, and CSRF credentials to streamed POSTs', async () => {
    setStoredSecret('shared')
    document.cookie = 'fx_csrf=csrf-token; path=/'
    const fetchSpy = vi.spyOn(globalThis, 'fetch').mockResolvedValue({ status: 200 } as Response)

    await authenticatedFetch('/api/backup', { method: 'POST' })

    const init = fetchSpy.mock.calls[0][1]!
    const headers = init.headers as Headers
    expect(init.credentials).toBe('include')
    expect(headers.get('X-Foldex-Secret')).toBe('shared')
    expect(headers.get(CSRF_HEADER)).toBe('csrf-token')
  })

  it('uses the shared refresh flight before retrying a 401', async () => {
    const cancel = vi.fn(async () => undefined)
    const fetchSpy = vi.spyOn(globalThis, 'fetch')
      .mockResolvedValueOnce({ status: 401, body: { cancel } } as unknown as Response)
      .mockResolvedValueOnce({ status: 200 } as Response)
    vi.spyOn(http, 'post').mockResolvedValue({ data: {} } as never)

    const response = await authenticatedFetch('/api/backup', { method: 'POST' })

    expect(response.status).toBe(200)
    expect(cancel).toHaveBeenCalledOnce()
    expect(fetchSpy).toHaveBeenCalledTimes(2)
    expect(http.post).toHaveBeenCalledWith('/api/auth/refresh', null, expect.anything())
  })
})

describe('401 single-flight refresh', () => {
  /** Drives the response interceptor's error arm directly. */
  async function runErrorInterceptor(error: unknown) {
    const handlers = (http.interceptors.response as unknown as {
      handlers: Array<{ rejected?: (e: any) => any }>
    }).handlers
    const rejected = handlers.find((h) => h?.rejected)?.rejected
    return rejected!(error)
  }

  beforeEach(() => {
    resetRefreshState()
    setSessionLostHandler(null)
  })

  afterEach(() => {
    setSessionLostHandler(null)
    vi.restoreAllMocks()
  })

  it('refreshes once and retries the original request', async () => {
    const post = vi.spyOn(http, 'post').mockResolvedValue({ data: {} } as never)
    const request = vi.spyOn(http, 'request').mockResolvedValue({ status: 200 } as never)

    await runErrorInterceptor({
      response: { status: 401 },
      config: { url: '/api/links', method: 'get', headers: {} },
    })

    expect(post).toHaveBeenCalledWith('/api/auth/refresh', null, expect.anything())
    expect(request).toHaveBeenCalledTimes(1)
    expect(request.mock.calls[0][0]).toMatchObject({ _retried: true })
  })

  /**
   * The single-flight guarantee.
   *
   * App mounts four authenticated queries at once; when the access cookie
   * expires they all 401 within milliseconds of each other. Without a shared
   * promise each would POST /api/auth/refresh with the SAME refresh cookie —
   * and the backend's reuse detector exists precisely to treat a re-presented
   * refresh token as an attack. Its 10-second grace window forgives the race,
   * but relying on someone else's safety net is not a design.
   */
  it('coalesces concurrent 401s into ONE refresh', async () => {
    let refreshes = 0
    let releaseRefresh!: () => void
    const refreshPending = new Promise<void>((resolve) => {
      releaseRefresh = resolve
    })
    vi.spyOn(http, 'post').mockImplementation(async () => {
      refreshes++
      await refreshPending
      return { data: {} } as never
    })
    vi.spyOn(http, 'request').mockResolvedValue({ status: 200 } as never)

    const requests = Promise.all(
      ['/api/entries', '/api/folders', '/api/tags', '/api/stats'].map((url) =>
        runErrorInterceptor({
          response: { status: 401 },
          config: { url, method: 'get', headers: {} },
        }),
      ),
    )
    await vi.waitFor(() => expect(refreshes).toBe(1))
    releaseRefresh()
    await requests

    expect(refreshes).toBe(1)
  })

  it('does not retry when sign out advances the auth epoch during refresh', async () => {
    let finishRefresh!: () => void
    const refresh = new Promise<void>((resolve) => {
      finishRefresh = resolve
    })
    vi.spyOn(http, 'post').mockReturnValue(refresh as never)
    const request = vi.spyOn(http, 'request').mockResolvedValue({ status: 200 } as never)
    const originalError = {
      response: { status: 401 },
      config: { url: '/api/links', method: 'get', headers: {} },
    }

    const pending = runErrorInterceptor(originalError)
    await vi.waitFor(() => expect(http.post).toHaveBeenCalledWith('/api/auth/refresh', null, expect.anything()))

    advanceAuthEpoch()
    finishRefresh()

    await expect(pending).rejects.toBe(originalError)
    expect(request).not.toHaveBeenCalled()
  })

  it('does not retry a request that already was retried', async () => {
    const post = vi.spyOn(http, 'post')
    await expect(
      runErrorInterceptor({
        response: { status: 401 },
        config: { url: '/api/links', method: 'get', headers: {}, _retried: true },
      }),
    ).rejects.toBeTruthy()
    // Without the flag this is an infinite loop, not a slow failure.
    expect(post).not.toHaveBeenCalled()
  })

  it('notifies the session-lost handler when the refresh itself fails', async () => {
    const lost = vi.fn()
    setSessionLostHandler(lost)
    vi.spyOn(http, 'post').mockRejectedValue(new Error('401'))

    await expect(
      runErrorInterceptor({
        response: { status: 401 },
        config: { url: '/api/links', method: 'get', headers: {} },
      }),
    ).rejects.toBeTruthy()
    expect(lost).toHaveBeenCalledTimes(1)
  })

  // The auth endpoints are the ones that ESTABLISH a session, so routing their
  // 401s through a refresh would be circular.
  it('never refreshes on behalf of /api/auth/*', async () => {
    const post = vi.spyOn(http, 'post')
    await expect(
      runErrorInterceptor({
        response: { status: 401 },
        config: { url: '/api/auth/login', method: 'post', headers: {} },
      }),
    ).rejects.toBeTruthy()
    expect(post).not.toHaveBeenCalled()
  })

  it('leaves non-401 failures alone', async () => {
    const post = vi.spyOn(http, 'post')
    await expect(
      runErrorInterceptor({
        response: { status: 500 },
        config: { url: '/api/links', method: 'get', headers: {} },
      }),
    ).rejects.toBeTruthy()
    expect(post).not.toHaveBeenCalled()
  })

  // A rejected promise must not stay cached, or every later 401 reuses the same
  // failure and the app can never recover — not even after a fresh sign-in.
  it('recovers after a failed refresh', async () => {
    setSessionLostHandler(vi.fn())
    const post = vi.spyOn(http, 'post').mockRejectedValueOnce(new Error('nope'))
    await expect(
      runErrorInterceptor({
        response: { status: 401 },
        config: { url: '/api/links', method: 'get', headers: {} },
      }),
    ).rejects.toBeTruthy()

    post.mockResolvedValue({ data: {} } as never)
    vi.spyOn(http, 'request').mockResolvedValue({ status: 200 } as never)

    const resp = await runErrorInterceptor({
      response: { status: 401 },
      config: { url: '/api/links', method: 'get', headers: {} },
    })
    expect(resp).toMatchObject({ status: 200 })
  })
})
