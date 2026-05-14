import axios from 'axios'

// In production (nginx) we hit relative /api. In dev (vite), the proxy in
// vite.config.ts forwards /api -> backend.
export const http = axios.create({
  baseURL: '/',
  headers: { 'Content-Type': 'application/json' },
  // 30s ceiling so a wedged backend doesn't leave the UI spinning forever.
  // Backup export/restore can stream multi-second payloads — those call paths
  // override the timeout explicitly when needed (api/backup.ts).
  timeout: 30_000,
})

// SHARED_SECRET wiring. When the backend is started with SHARED_SECRET set,
// every /api/* request needs X-Foldex-Secret. The user pastes the secret into
// the prompt (or stores it manually in localStorage under `foldex.secret`).
// Empty string = no header sent (backend's default off mode).
const SECRET_KEY = 'foldex.secret'

export function getStoredSecret(): string {
  if (typeof localStorage === 'undefined') return ''
  return localStorage.getItem(SECRET_KEY) ?? ''
}

export function setStoredSecret(value: string): void {
  if (typeof localStorage === 'undefined') return
  if (value) localStorage.setItem(SECRET_KEY, value)
  else localStorage.removeItem(SECRET_KEY)
}

http.interceptors.request.use((config) => {
  const secret = getStoredSecret()
  if (secret) {
    config.headers = config.headers ?? {}
    ;(config.headers as Record<string, string>)['X-Foldex-Secret'] = secret
  }
  return config
})

// On 401: drop the stale secret and prompt for a new one once. Prevents an
// infinite loop where every queued request triggers its own prompt.
let promptInFlight = false
http.interceptors.response.use(
  (resp) => resp,
  async (error) => {
    const status = error?.response?.status
    if (status === 401 && typeof window !== 'undefined' && !promptInFlight) {
      promptInFlight = true
      try {
        setStoredSecret('')
        const fresh = window.prompt('Foldex: enter SHARED_SECRET to authenticate /api requests')
        if (fresh) {
          setStoredSecret(fresh)
        }
      } finally {
        promptInFlight = false
      }
    }
    return Promise.reject(error)
  },
)
