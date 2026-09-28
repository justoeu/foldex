// Manual Foldex load. Not part of CI.
//
//   ./load/k6/run.sh smoke
//   K6_EMAIL=... K6_PASSWORD=... ./load/k6/run.sh library-read
//
// See load/k6/README.md. Do not point K6_BASE_URL at a shared instance
// unless that instance's owner asked for the traffic.

import http from 'k6/http'
import { sleep } from 'k6'
import { baseURL, duration, email, flow, password, profile, vus } from './lib/config.js'
import { expectStatus } from './lib/expect.js'
import { authParams, login } from './lib/session.js'

const READS = new Set([
  'session', 'library-read', 'stats', 'activity', 'settings', 'admin', 'mixed', 'export',
])
const WRITES = new Set(['library-write'])

const EXEC = {
  smoke: 'smoke',
  session: 'session',
  'library-read': 'libraryRead',
  'library-write': 'libraryWrite',
  stats: 'stats',
  activity: 'activity',
  settings: 'settings',
  admin: 'admin',
  redirect: 'redirect',
  export: 'exportdump',
  mixed: 'mixed',
}

export const options = {
  tags: {
    testid: __ENV.FOLDEX_K6_RUN || 'manual',
    flow,
  },
  scenarios: {
    selected: {
      executor: 'constant-vus',
      vus,
      duration,
      exec: EXEC[flow] || 'smoke',
      gracefulStop: '10s',
    },
  },
  thresholds: thresholdsFor(profile, flow),
  // A dropped VU must not hide a 500 behind a short summary.
  summaryTrendStats: ['avg', 'med', 'p(95)', 'p(99)', 'max'],
}

function thresholdsFor(which, selected) {
  if (which === 'stress') {
    // Stress is allowed to trip the write quota. It is not allowed to 500.
    return { unexpected_status: ['rate<0.01'] }
  }
  const latency = selected === 'library-write' ? 'p(95)<1500' : 'p(95)<800'
  return {
    unexpected_status: ['rate<0.01'],
    http_req_duration: [latency],
  }
}

export function setup() {
  const session = READS.has(flow) || WRITES.has(flow) ? login(email, password) : null
  if (session) {
    // One CSRF miss, not a loop: this proves the guard still answers 403
    // and does not 500. It is not a load.
    const missed = http.post(
      `${baseURL}/api/links`,
      JSON.stringify({ url: 'https://example.com/k6-no-csrf', title: 'k6' }),
      { headers: { 'Content-Type': 'application/json', Cookie: session.cookie, Accept: 'application/json' }, tags: { name: 'POST /api/links (no csrf)' }, redirects: 0 },
    )
    expectStatus(missed, [401, 403], 'POST /api/links without CSRF')
  }
  return { session }
}

export function smoke() {
  const health = http.get(`${baseURL}/healthz`, { tags: { name: 'GET /healthz' }, redirects: 0 })
  expectStatus(health, [200], 'GET /healthz')

  // INV-042: /api/auth/me is 200 for an anonymous caller too.
  const me = http.get(`${baseURL}/api/auth/me`, { tags: { name: 'GET /api/auth/me' }, redirects: 0 })
  expectStatus(me, [200], 'GET /api/auth/me anonymous')

  const missing = http.get(`${baseURL}/go/k6-no-such-slug`, { tags: { name: 'GET /go/{slug}' }, redirects: 0 })
  expectStatus(missing, [404], 'GET /go/{missing}')

  const denied = http.get(`${baseURL}/api/entries?limit=1`, { tags: { name: 'GET /api/entries' }, redirects: 0 })
  expectStatus(denied, [401], 'GET /api/entries anonymous')
}

export function session(data) {
  const me = http.get(`${baseURL}/api/auth/me`, authParams(data.session, 'GET /api/auth/me'))
  expectStatus(me, [200], 'GET /api/auth/me')
  sleep(0.2)
}

export function libraryRead(data) {
  const pages = [
    ['GET /api/entries', '/api/entries?limit=50'],
    ['GET /api/entries?sort=alpha', '/api/entries?limit=50&sort=alpha'],
    ['GET /api/entries?sort=clicks', '/api/entries?limit=50&sort=clicks'],
    ['GET /api/entries?q=', '/api/entries?limit=20&q=k6'],
    ['GET /api/entries/counts', '/api/entries/counts'],
    ['GET /api/links', '/api/links?limit=50'],
    ['GET /api/links/recent-changes', '/api/links/recent-changes?limit=20'],
    ['GET /api/notes', '/api/notes?limit=50'],
    ['GET /api/folders', '/api/folders?fields=minimal'],
    ['GET /api/tags', '/api/tags'],
  ]
  for (let i = 0; i < pages.length; i++) {
    const res = http.get(`${baseURL}${pages[i][1]}`, authParams(data.session, pages[i][0]))
    expectStatus(res, [200], pages[i][0])
  }
  const listed = http.get(`${baseURL}/api/links?limit=1`, authParams(data.session, 'GET /api/links?limit=1'))
  if (expectStatus(listed, [200], 'GET /api/links?limit=1') && listed.body) {
    const url = firstURL(listed.body)
    if (url) {
      const byURL = http.get(
        `${baseURL}/api/links/by-url?url=${encodeURIComponent(url)}`,
        authParams(data.session, 'GET /api/links/by-url'),
      )
      expectStatus(byURL, [200], 'GET /api/links/by-url')
    }
  }
}

export function libraryWrite(data) {
  const stamp = `k6-${__VU}-${__ITER}-${Date.now()}`
  const params = (name) => authParams(data.session, name, { 'Content-Type': 'application/json' })
  // Stay under the default 120 writes/minute for ONE account, shared by
  // every VU. Stress opts out and treats 429 as success.
  const tolerate = profile === 'stress' ? [200, 201, 204, 429] : [200, 201, 204]

  const tag = http.post(
    `${baseURL}/api/tags`,
    JSON.stringify({ name: stamp, color: '#6366F1' }),
    params('POST /api/tags'),
  )
  const tagID = expectStatus(tag, tolerate, 'POST /api/tags') ? idOf(tag) : 0

  const folder = http.post(
    `${baseURL}/api/folders`,
    JSON.stringify({ name: stamp, color: '#6366F1' }),
    params('POST /api/folders'),
  )
  const folderID = expectStatus(folder, tolerate, 'POST /api/folders') ? idOf(folder) : 0

  const link = http.post(
    `${baseURL}/api/links`,
    JSON.stringify({ url: `https://example.com/${stamp}`, title: stamp }),
    params('POST /api/links'),
  )
  const linkID = expectStatus(link, tolerate, 'POST /api/links') ? idOf(link) : 0

  if (linkID) {
    const patched = http.patch(
      `${baseURL}/api/links/${linkID}`,
      JSON.stringify({ title: `${stamp}-edited` }),
      params('PATCH /api/links/{id}'),
    )
    expectStatus(patched, tolerate, 'PATCH /api/links/{id}')
    const removed = http.del(`${baseURL}/api/links/${linkID}`, null, params('DELETE /api/links/{id}'))
    expectStatus(removed, tolerate, 'DELETE /api/links/{id}')
  }
  const note = http.post(
    `${baseURL}/api/notes`,
    JSON.stringify({ title: stamp, body_html: '<p>k6</p>' }),
    params('POST /api/notes'),
  )
  const noteID = expectStatus(note, tolerate, 'POST /api/notes') ? idOf(note) : 0
  if (noteID) {
    const removed = http.del(`${baseURL}/api/notes/${noteID}`, null, params('DELETE /api/notes/{id}'))
    expectStatus(removed, tolerate, 'DELETE /api/notes/{id}')
  }
  if (folderID) {
    const removed = http.del(`${baseURL}/api/folders/${folderID}`, null, params('DELETE /api/folders/{id}'))
    expectStatus(removed, tolerate, 'DELETE /api/folders/{id}')
  }
  if (tagID) {
    const removed = http.del(`${baseURL}/api/tags/${tagID}`, null, params('DELETE /api/tags/{id}'))
    expectStatus(removed, tolerate, 'DELETE /api/tags/{id}')
  }

  // 7 mutations. At or under 90/minute for the whole account.
  const pause = (7 * vus * 60) / 90
  sleep(pause)
}

export function stats(data) {
  const paths = [
    '/api/stats/summary',
    '/api/stats/daily?days=14',
    '/api/stats/top?limit=10',
    '/api/stats/tags',
    '/api/stats/dashboard?days=14&limit=10',
  ]
  for (let i = 0; i < paths.length; i++) {
    const res = http.get(`${baseURL}${paths[i]}`, authParams(data.session, `GET ${paths[i].split('?')[0]}`))
    expectStatus(res, [200], `GET ${paths[i].split('?')[0]}`)
  }
}

export function activity(data) {
  for (const limit of [25, 50, 100]) {
    const res = http.get(
      `${baseURL}/api/activity?limit=${limit}`,
      authParams(data.session, 'GET /api/activity'),
    )
    expectStatus(res, [200], 'GET /api/activity')
  }
}

export function settings(data) {
  const master = http.get(
    `${baseURL}/api/settings/master-password`,
    authParams(data.session, 'GET /api/settings/master-password'),
  )
  expectStatus(master, [200], 'GET /api/settings/master-password')
  const me = http.get(`${baseURL}/api/auth/me`, authParams(data.session, 'GET /api/auth/me'))
  expectStatus(me, [200], 'GET /api/auth/me')
}

export function admin(data) {
  // A non-admin gets 404, not 403 (INV-043). Both are healthy answers.
  const paths = ['/api/admin/users', '/api/admin/metrics', '/api/admin/audit?limit=20']
  for (let i = 0; i < paths.length; i++) {
    const name = `GET ${paths[i].split('?')[0]}`
    const res = http.get(`${baseURL}${paths[i]}`, authParams(data.session, name))
    expectStatus(res, [200, 404], name)
  }
}

export function redirect() {
  const res = http.get(`${baseURL}/go/k6-no-such-slug`, { tags: { name: 'GET /go/{slug}' }, redirects: 0 })
  expectStatus(res, [404], 'GET /go/{slug}')
}

// One iteration, invoked with K6_VUS=1 K6_DURATION=1s. Counts against the
// hourly expensive bucket (default 20). Not part of the mixed loop.
export function exportdump(data) {
  const res = http.get(
    `${baseURL}/api/export?format=json`,
    authParams(data.session, 'GET /api/export'),
  )
  expectStatus(res, [200, 429], 'GET /api/export')
  sleep(1)
}

export function mixed(data) {
  const roll = __ITER % 10
  if (roll < 6) libraryRead(data)
  else if (roll < 8) stats(data)
  else if (roll < 9) activity(data)
  else settings(data)
}

function idOf(res) {
  try {
    const body = res.json()
    const id = body && (body.id || (body.tag && body.tag.id) || (body.folder && body.folder.id))
    return Number(id) || 0
  } catch (_) {
    return 0
  }
}

function firstURL(raw) {
  try {
    const body = JSON.parse(raw)
    const rows = Array.isArray(body) ? body : (body.links || body.items || [])
    for (let i = 0; i < rows.length; i++) {
      if (rows[i] && rows[i].url) return rows[i].url
    }
  } catch (_) {
    return ''
  }
  return ''
}
