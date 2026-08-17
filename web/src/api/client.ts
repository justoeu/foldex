import axios, { AxiosError, InternalAxiosRequestConfig } from 'axios'

// In production (nginx) we hit relative /api. In dev (vite), the proxy in
// vite.config.ts forwards /api -> backend.
export const http = axios.create({
  baseURL: '/',
  headers: { 'Content-Type': 'application/json' },
  // Cookies ARE the session (ADR-30). Without this, axios drops them on any
  // cross-origin call — which is exactly the dev setup, where the SPA is on
  // :9088 and the API on :9089.
  withCredentials: true,
  // 30s ceiling so a wedged backend doesn't leave the UI spinning forever.
  // Backup export/restore can stream multi-second payloads — those call paths
  // override the timeout explicitly when needed (api/backup.ts).
  timeout: 30_000,
})

// Releases before the SHARED_SECRET perimeter was removed stored its value
// here; nothing reads it anymore, so clear the orphaned secret material once
// per boot instead of letting it linger in localStorage forever.
if (typeof localStorage !== 'undefined') localStorage.removeItem('foldex.secret')

export const CSRF_COOKIE = 'fx_csrf'
export const CSRF_HEADER = 'X-Foldex-CSRF'

/**
 * Reads the CSRF token from its cookie.
 *
 * fx_csrf is the one auth cookie deliberately left readable by JavaScript,
 * because the double-submit scheme requires the SPA to echo it in a header.
 * That is not a weakness: its security comes from a cross-origin attacker
 * being unable to READ it, not from our own script being unable to.
 */
export function readCsrfToken(): string {
  if (typeof document === 'undefined') return ''
  const match = document.cookie.match(new RegExp(`(?:^|;\\s*)${CSRF_COOKIE}=([^;]*)`))
  return match ? decodeURIComponent(match[1]) : ''
}

const UNSAFE_METHODS = new Set(['post', 'put', 'patch', 'delete'])

http.interceptors.request.use((config) => {
  const authConfig = config as RetryConfig
  authConfig._authEpoch ??= authEpoch
  const headers = (config.headers ?? {}) as Record<string, string>

  if (UNSAFE_METHODS.has((config.method ?? 'get').toLowerCase())) {
    const csrf = readCsrfToken()
    // Never overwrite a header a caller set explicitly — tests and the
    // bootstrap flow both need to drive this directly.
    if (csrf && !headers[CSRF_HEADER]) headers[CSRF_HEADER] = csrf
  }
  config.headers = headers as never
  return config
})

// ─────────────────────────────────────────────────────────────────────
// Single-flight refresh
// ─────────────────────────────────────────────────────────────────────

/**
 * The in-flight refresh for one auth generation, shared by every request that
 * 401s in that generation.
 *
 * The App mounts four authenticated queries at once (entries, folders ×2,
 * tags). When the access cookie expires they all 401 within milliseconds of
 * each other. Without this promise each would fire its own
 * POST /api/auth/refresh with the SAME refresh cookie — and the backend's
 * reuse detector exists precisely to treat a re-presented refresh token as an
 * attack. The server's 10-second grace window forgives that race, but relying
 * on it from the client would be building on someone else's safety net;
 * sharing one call means the race never happens.
 */
let authEpoch = 0

type RefreshFlight = {
  epoch: number
  promise: Promise<void>
}

let refreshFlight: RefreshFlight | null = null

/** Starts a new auth generation, invalidating work begun for the previous one. */
export function advanceAuthEpoch(): void {
  authEpoch++
}

/** Called by AuthProvider when a refresh definitively fails. */
let onSessionLost: (() => void) | null = null
export function setSessionLostHandler(fn: (() => void) | null): void {
  onSessionLost = fn
}

/** Test seam: resets the refresh flight and generation between cases. */
export function resetRefreshState(): void {
  refreshFlight = null
  authEpoch = 0
}

function refreshOnce(epoch: number): Promise<void> {
  if (!refreshFlight || refreshFlight.epoch !== epoch) {
    const flight: RefreshFlight = { epoch, promise: Promise.resolve() }
    flight.promise = http
      .post('/api/auth/refresh', null, { _skipAuthRetry: true } as never)
      .then(() => undefined)
      .finally(() => {
        // Cleared in `finally`, not in `then`: leaving a rejected promise
        // cached would make every later 401 reuse the same failure, and the app
        // could never recover — not even after a fresh sign-in.
        if (refreshFlight === flight) refreshFlight = null
      })
    refreshFlight = flight
  }
  return refreshFlight.promise
}

export async function authenticatedFetch(input: string, init: RequestInit = {}): Promise<Response> {
  const requestEpoch = authEpoch
  const request = () => {
    const headers = new Headers(init.headers)
    if (UNSAFE_METHODS.has((init.method ?? 'get').toLowerCase())) {
      const csrf = readCsrfToken()
      if (csrf && !headers.has(CSRF_HEADER)) headers.set(CSRF_HEADER, csrf)
    }
    return fetch(input, { ...init, credentials: init.credentials ?? 'include', headers })
  }

  const response = await request()
  if (response.status !== 401 || input.includes('/api/auth/') || requestEpoch !== authEpoch) return response
  await response.body?.cancel().catch(() => undefined)
  try {
    await refreshOnce(requestEpoch)
  } catch (error) {
    if (requestEpoch === authEpoch) onSessionLost?.()
    throw error
  }
  if (requestEpoch !== authEpoch) throw new Error('authentication changed during request')
  return request()
}

type RetryConfig = InternalAxiosRequestConfig & {
  _retried?: boolean
  _skipAuthRetry?: boolean
  _authEpoch?: number
}

http.interceptors.response.use(
  (resp) => resp,
  async (error: AxiosError) => {
    const status = error.response?.status
    const config = error.config as RetryConfig | undefined
    if (config) config._authEpoch ??= authEpoch
    if (status !== 401 || !config || config._retried || config._skipAuthRetry) {
      return Promise.reject(error)
    }

    // The auth endpoints are the ones that ESTABLISH a session, so retrying
    // them through a refresh would be circular — and /api/auth/me is
    // contractually always 200 anyway.
    if ((config.url ?? '').includes('/api/auth/')) return Promise.reject(error)

    const requestEpoch = config._authEpoch
    if (requestEpoch !== authEpoch) return Promise.reject(error)

    try {
      await refreshOnce(requestEpoch)
    } catch {
      if (requestEpoch === authEpoch) onSessionLost?.()
      return Promise.reject(error)
    }
    if (requestEpoch !== authEpoch) return Promise.reject(error)
    config._retried = true
    return http.request(config)
  },
)
