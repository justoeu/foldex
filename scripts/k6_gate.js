import http from 'k6/http'
import { check, fail } from 'k6'
import { Rate, Trend } from 'k6/metrics'

const baseURL = (__ENV.K6_BASE_URL || 'http://127.0.0.1:9089').replace(/\/$/, '')
const email = __ENV.K6_EMAIL || ''
const password = __ENV.K6_PASSWORD || ''
const flow = __ENV.K6_FLOW || ''
const requestTimeout = __ENV.FOLDEX_K6_TIMEOUT || '30s'

const timeouts = new Rate('timeouts')
const shed = new Rate('shed')
const served = new Rate('served')
const quotaLimited = new Rate('quota_limited')
const otherStatus = new Rate('other_status')
const servedMs = new Trend('served_ms', true)
const shedMs = new Trend('shed_ms', true)

function arrival(exec, perMinute, preAllocatedVUs, maxVUs, lane) {
  const stages = [0.2, 0.4, 0.6, 0.8, 1].map((fraction) => ({
    duration: '1m',
    target: Math.round(perMinute * fraction),
  }))
  stages.push({ duration: '2m', target: perMinute })
  return {
    executor: 'ramping-arrival-rate',
    exec,
    startRate: Math.max(1, Math.round(perMinute * 0.1)),
    timeUnit: '1m',
    preAllocatedVUs,
    maxVUs,
    stages,
    gracefulStop: '30s',
    tags: { lane },
  }
}

function scenarioOptions(name, scenarios) {
  return {
    tags: { testid: __ENV.FOLDEX_K6_RUN || 'gate', flow: name },
    scenarios,
    thresholds: {
      timeouts: ['rate<0.02'],
      other_status: ['rate<0.01'],
    },
    summaryTrendStats: ['avg', 'med', 'p(95)', 'p(99)', 'max'],
  }
}

function selectOptions() {
  if (flow === 'gate-write') {
    return scenarioOptions(flow, { writers: arrival('writeOne', 10000, 200, 2000, 'write') })
  }
  if (flow === 'gate-read') {
    return scenarioOptions(flow, { readers: arrival('readOne', 30000, 500, 4000, 'read') })
  }
  if (flow === 'gate-mixed') {
    return scenarioOptions(flow, {
      writers: arrival('writeOne', 8000, 150, 1500, 'write'),
      readers: arrival('readOne', 20000, 400, 3000, 'read'),
    })
  }
  if (flow === 'gate-climb') {
    return scenarioOptions(flow, {
      readers: {
        executor: 'ramping-arrival-rate',
        exec: 'readOne',
        startRate: 200,
        timeUnit: '1s',
        preAllocatedVUs: 400,
        maxVUs: 7000,
        stages: [
          { duration: '20s', target: 500 },
          { duration: '20s', target: 1500 },
          { duration: '20s', target: 3000 },
          { duration: '30s', target: 5000 },
        ],
        gracefulStop: '10s',
        tags: { lane: 'read' },
      },
    })
  }
  fail(`fluxo desconhecido: ${flow}`)
}

export const options = selectOptions()

function cookie(res, name) {
  const jar = res.cookies && res.cookies[name]
  if (!jar || jar.length === 0 || !jar[0].value) return ''
  return jar[0].value
}

export function setup() {
  if (!email || !password) fail('credenciais do usuário de teste ausentes')
  const res = http.post(
    `${baseURL}/api/auth/login`,
    JSON.stringify({ email, password }),
    {
      headers: { 'Content-Type': 'application/json', Accept: 'application/json' },
      tags: { name: 'POST /api/auth/login' },
      redirects: 0,
      timeout: requestTimeout,
    },
  )
  if (res.status !== 200) fail(`login HTTP ${res.status}`)
  let body = {}
  try {
    body = res.json()
  } catch (_) {
    fail('login sem JSON')
  }
  if (!body || body.status !== 'authenticated') fail(`login parou em ${body && body.status}`)
  const access = cookie(res, 'fx_at')
  const csrf = cookie(res, 'fx_csrf')
  if (!access || !csrf) fail('login não gravou fx_at e fx_csrf')
  return { cookie: `fx_at=${access}; fx_csrf=${csrf}`, csrf }
}

function authParams(session, name, extra) {
  const headers = { Accept: 'application/json', Cookie: session.cookie, 'X-Foldex-CSRF': session.csrf }
  if (extra) Object.assign(headers, extra)
  return { headers, tags: { name }, redirects: 0, timeout: requestTimeout }
}

// 2xx is served. 503 is the gate shedding after 1s and is a measured outcome,
// not a transport failure. A timeout means the gate did not answer.
function classify(res, okStatuses) {
  const status = res.status || 0
  const err = String(res.error || '')
  const timedOut = status === 0 && /timeout/i.test(err)
  const isShed = status === 503
  const isQuota = status === 429
  const good = okStatuses.indexOf(status) !== -1
  timeouts.add(timedOut)
  shed.add(isShed)
  quotaLimited.add(isQuota)
  served.add(good)
  otherStatus.add(!timedOut && !isShed && !isQuota && !good)
  if (good) servedMs.add(res.timings.duration)
  if (isShed) shedMs.add(res.timings.duration)
  check(res, { outcome: () => good || isShed })
}

export function writeOne(data) {
  const stamp = `k6-${__VU}-${__ITER}-${Date.now()}`
  const res = http.post(
    `${baseURL}/api/links`,
    JSON.stringify({ url: `http://127.0.0.1/k6/${stamp}`, title: stamp }),
    authParams(data, 'POST /api/links', { 'Content-Type': 'application/json' }),
  )
  classify(res, [201])
}

export function readOne(data) {
  const res = http.get(`${baseURL}/api/links?limit=50`, authParams(data, 'GET /api/links'))
  classify(res, [200])
}
