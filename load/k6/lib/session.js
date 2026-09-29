import http from 'k6/http'
import { fail } from 'k6'
import { baseURL, requestTimeout } from './config.js'

const jsonHeaders = { 'Content-Type': 'application/json', Accept: 'application/json' }

function cookie(res, name) {
  const jar = res.cookies && res.cookies[name]
  if (!jar || jar.length === 0 || !jar[0].value) return ''
  return jar[0].value
}

// login performs ONE password login and returns the cookies the later
// requests must send. It refuses a second-factor challenge: continuing
// would spend a mailed or TOTP code, which a load run must not do.
export function login(email, password) {
  const res = http.post(
    `${baseURL}/api/auth/login`,
    JSON.stringify({ email, password }),
    { headers: jsonHeaders, tags: { name: 'POST /api/auth/login' }, redirects: 0, timeout: requestTimeout },
  )
  if (res.status === 429) {
    fail('login is rate-limited (429). Wait for Retry-After before running again.')
  }
  if (res.status !== 200) {
    fail(`login failed: HTTP ${res.status}. Use an account without a second factor.`)
  }
  let body = {}
  try {
    body = res.json()
  } catch (_) {
    fail('login returned a non-JSON body')
  }
  if (body && body.status && body.status !== 'authenticated') {
    fail(`login stopped at ${body.status}. The load user must not have 2FA and must not be an admin who still needs to enroll.`)
  }
  const access = cookie(res, 'fx_at')
  const csrf = cookie(res, 'fx_csrf')
  if (!access || !csrf) {
    fail('login did not set fx_at and fx_csrf. Check AUTH_PUBLIC_URL: a Secure cookie is dropped on plain http.')
  }
  return {
    cookie: `fx_at=${access}; fx_csrf=${csrf}`,
    csrf,
  }
}

export function authParams(session, name, extra) {
  const headers = {
    Accept: 'application/json',
    Cookie: session.cookie,
    'X-Foldex-CSRF': session.csrf,
  }
  if (extra) Object.assign(headers, extra)
  return { headers, tags: { name }, redirects: 0, timeout: requestTimeout }
}
