// Manual Foldex load. Not part of CI.
//
//   ./load/k6/run.sh smoke
//   K6_EMAIL=... K6_PASSWORD=... ./load/k6/run.sh library-read
//
// See load/k6/README.md. Do not point K6_BASE_URL at a shared instance
// unless that instance's owner asked for the traffic.

import http from 'k6/http'
import { sleep } from 'k6'
import { baseURL, duration, email, flow, password, profile, requestTimeout, vus } from './lib/config.js'
import { expectStatus, rampMinute } from './lib/expect.js'
import { authParams, login } from './lib/session.js'

const READS = new Set([
  'session', 'library-read', 'stats', 'activity', 'settings', 'admin', 'mixed', 'export', 'capacity',
  'orders-read', 'orders-mixed', 'surge-read', 'surge-mixed',
])
const WRITES = new Set([
  'library-write', 'orders-write', 'orders-mixed', 'surge-write', 'surge-mixed',
])

// One order is one HTTP call. orders-* climbs to 1500 creates per minute,
// 3000 lists per minute, and 1000 creates beside 2000 lists. surge-* keeps
// that shape and raises the hold to 10000, 30000, and 8000 beside 20000.
// The account write quota has to sit above the write ramp for that window.
const ORDERS_WRITE_PER_MIN = 1500
const ORDERS_READ_PER_MIN = 3000
const ORDERS_MIXED_WRITE_PER_MIN = 1000
const ORDERS_MIXED_READ_PER_MIN = 2000
const SURGE_WRITE_PER_MIN = 10000
const SURGE_READ_PER_MIN = 30000
const SURGE_MIXED_WRITE_PER_MIN = 8000
const SURGE_MIXED_READ_PER_MIN = 20000

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

export const options = selectOptions()

function selectOptions() {
  if (flow === 'capacity') return capacityOptions()
  if (flow === 'orders-write') return ordersOptions('orders-write', orderArrival('orderWrite', ORDERS_WRITE_PER_MIN, 80, 400, 'write'))
  if (flow === 'orders-read') return ordersOptions('orders-read', orderArrival('orderRead', ORDERS_READ_PER_MIN, 120, 800, 'read'))
  if (flow === 'orders-mixed') {
    return ordersOptions('orders-mixed', null, {
      writers: orderArrival('orderWrite', ORDERS_MIXED_WRITE_PER_MIN, 60, 300, 'write'),
      readers: orderArrival('orderRead', ORDERS_MIXED_READ_PER_MIN, 80, 500, 'read'),
    })
  }
  // Preallocated VUs cover the healthy case. The cap is large enough that a
  // handler stuck for the client timeout shows up as timeouts, not only as
  // iterations k6 never started.
  if (flow === 'surge-write') return ordersOptions('surge-write', orderArrival('orderWrite', SURGE_WRITE_PER_MIN, 200, 2000, 'write'))
  if (flow === 'surge-read') return ordersOptions('surge-read', orderArrival('orderRead', SURGE_READ_PER_MIN, 500, 4000, 'read'))
  if (flow === 'surge-mixed') {
    return ordersOptions('surge-mixed', null, {
      writers: orderArrival('orderWrite', SURGE_MIXED_WRITE_PER_MIN, 150, 1500, 'write'),
      readers: orderArrival('orderRead', SURGE_MIXED_READ_PER_MIN, 400, 3000, 'read'),
    })
  }
  return singleOptions()
}

function orderArrival(exec, finalRate, preAllocatedVUs, maxVUs, lane) {
  const fractions = [0.2, 0.4, 0.6, 0.8, 1]
  const stages = fractions.map((fraction) => ({
    duration: '1m',
    target: Math.round(finalRate * fraction),
  }))
  stages.push({ duration: '2m', target: finalRate })
  return {
    executor: 'ramping-arrival-rate',
    exec,
    startRate: Math.max(1, Math.round(finalRate * 0.1)),
    timeUnit: '1m',
    preAllocatedVUs,
    maxVUs,
    stages,
    gracefulStop: '30s',
    tags: { lane },
  }
}

function ordersOptions(name, single, scenarios) {
  return {
    tags: {
      testid: __ENV.FOLDEX_K6_RUN || 'manual',
      flow: name,
    },
    scenarios: scenarios || { orders: single },
    // Finish the six minutes even when a threshold trips. A quota refusal
    // means the temporary write budget was not in force. It fails the run.
    thresholds: {
      unexpected_status: ['rate<0.01'],
      timeouts: ['rate<0.01'],
      server_errors: ['rate<0.01'],
    },
    summaryTrendStats: ['avg', 'med', 'p(95)', 'p(99)', 'max'],
  }
}

function singleOptions() {
  return {
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
    summaryTrendStats: ['avg', 'med', 'p(95)', 'p(99)', 'max'],
  }
}

// Five minutes, one step per minute. The rate is iterations of the read loop
// (about ten GETs each), so the HTTP rate is roughly 10× the target. Writes
// are iterations of the 7-call mutation; the account quota is 120 mutations
// per minute, so 429s are expected once the writer passes ~17 iterations/minute.
// The run is meant to reach that wall and, on the read side, timeouts and 5xx.
function capacityOptions() {
  return {
    tags: {
      testid: __ENV.FOLDEX_K6_RUN || 'manual',
      flow: 'capacity',
    },
    scenarios: {
      readers: {
        executor: 'ramping-arrival-rate',
        exec: 'capacityRead',
        startRate: 10,
        timeUnit: '1s',
        preAllocatedVUs: 200,
        maxVUs: 1500,
        stages: [
          { duration: '1m', target: 20 },
          { duration: '1m', target: 50 },
          { duration: '1m', target: 100 },
          { duration: '1m', target: 200 },
          { duration: '1m', target: 400 },
        ],
        tags: { lane: 'read' },
      },
      writers: {
        executor: 'ramping-arrival-rate',
        exec: 'libraryWrite',
        startRate: 6,
        timeUnit: '1m',
        preAllocatedVUs: 8,
        maxVUs: 40,
        stages: [
          { duration: '1m', target: 12 },
          { duration: '1m', target: 24 },
          { duration: '1m', target: 48 },
          { duration: '1m', target: 96 },
          { duration: '1m', target: 192 },
        ],
        tags: { lane: 'write' },
      },
    },
    // The point of this flow is to climb until errors show. Thresholds stay
    // on the summary; they do not stop the five minutes early.
    thresholds: {
      timeouts: ['rate<0.5'],
      server_errors: ['rate<0.5'],
    },
    summaryTrendStats: ['avg', 'med', 'p(95)', 'p(99)', 'max'],
  }
}

function thresholdsFor(which, selected) {
  if (which === 'stress') {
    // Stress is allowed to trip the write quota. It is not allowed to 500 or hang.
    return {
      unexpected_status: ['rate<0.01'],
      timeouts: ['rate<0.01'],
      server_errors: ['rate<0.01'],
    }
  }
  const latency = selected === 'library-write' ? 'p(95)<1500' : 'p(95)<800'
  return {
    unexpected_status: ['rate<0.01'],
    timeouts: ['rate<0.01'],
    server_errors: ['rate<0.01'],
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
      { headers: { 'Content-Type': 'application/json', Cookie: session.cookie, Accept: 'application/json' }, tags: { name: 'POST /api/links (no csrf)' }, redirects: 0, timeout: requestTimeout },
    )
    expectStatus(missed, [401, 403], 'POST /api/links without CSRF')
  }
  return { session, startedAt: Date.now() }
}

export function smoke() {
  const health = http.get(`${baseURL}/healthz`, { tags: { name: 'GET /healthz' }, redirects: 0, timeout: requestTimeout })
  expectStatus(health, [200], 'GET /healthz')

  // INV-042: /api/auth/me is 200 for an anonymous caller too.
  const me = http.get(`${baseURL}/api/auth/me`, { tags: { name: 'GET /api/auth/me' }, redirects: 0, timeout: requestTimeout })
  expectStatus(me, [200], 'GET /api/auth/me anonymous')

  const missing = http.get(`${baseURL}/go/k6-no-such-slug`, { tags: { name: 'GET /go/{slug}' }, redirects: 0, timeout: requestTimeout })
  expectStatus(missing, [404], 'GET /go/{missing}')

  const denied = http.get(`${baseURL}/api/entries?limit=1`, { tags: { name: 'GET /api/entries' }, redirects: 0, timeout: requestTimeout })
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
      // capacity deletes the same rows the reader just listed, so 404 is the race, not a 500.
      const byURLOk = flow === 'capacity' ? [200, 404] : [200]
      expectStatus(byURL, byURLOk, 'GET /api/links/by-url')
    }
  }
}

export function libraryWrite(data) {
  const stamp = `k6-${__VU}-${__ITER}-${Date.now()}`
  const params = (name) => authParams(data.session, name, { 'Content-Type': 'application/json' })
  // Stay under the default 120 writes/minute for ONE account, shared by
  // every VU. Stress opts out and treats 429 as success.
  // capacity climbs past the 120 writes/minute quota on purpose. 429 is the
  // quota wall (quota_limited), not a 5xx and not a timeout.
  const tolerate = profile === 'stress' || flow === 'capacity' ? [200, 201, 204, 429] : [200, 201, 204]

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

  // 7 mutations. At or under 90/minute for the whole account. The capacity
  // flow paces writes with an arrival rate instead, so a sleep here would
  // stack on top of that rate and starve the writer.
  if (flow === 'capacity') {
    markRamp(data)
  } else {
    const pause = (7 * vus * 60) / 90
    sleep(pause)
  }
}

// One create. The host is loopback on purpose: the preview worker still
// runs, and the SSRF guard refuses the dial inside the process. A public
// host would make this chart measure that host's latency.
export function orderWrite(data) {
  markRamp(data)
  const stamp = `k6-${__VU}-${__ITER}-${Date.now()}`
  const res = http.post(
    `${baseURL}/api/links`,
    JSON.stringify({ url: `http://127.0.0.1/k6/${stamp}`, title: stamp }),
    authParams(data.session, 'POST /api/links', { 'Content-Type': 'application/json' }),
  )
  expectStatus(res, [201], 'POST /api/links')
}

export function orderRead(data) {
  markRamp(data)
  const res = http.get(
    `${baseURL}/api/links?limit=50`,
    authParams(data.session, 'GET /api/links'),
  )
  expectStatus(res, [200], 'GET /api/links')
}

export function capacityRead(data) {
  markRamp(data)
  const roll = __ITER % 5
  if (roll === 4) stats(data)
  else if (roll === 3) activity(data)
  else libraryRead(data)
}

function markRamp(data) {
  const started = data && data.startedAt
  if (!started) return
  const minute = Math.floor((Date.now() - started) / 60000) + 1
  rampMinute.add(Math.max(1, Math.min(5, minute)))
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
  const res = http.get(`${baseURL}/go/k6-no-such-slug`, { tags: { name: 'GET /go/{slug}' }, redirects: 0, timeout: requestTimeout })
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
