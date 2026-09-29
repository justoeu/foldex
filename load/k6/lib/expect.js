import { check } from 'k6'
import { Gauge, Rate, Trend } from 'k6/metrics'

// unexpected counts responses outside the statuses that flow considers
// healthy. A 429 from the write quota is expected on the stress profile
// and must be passed in `ok` so it does not fail the run.
export const unexpected = new Rate('unexpected_status')
export const timeouts = new Rate('timeouts')
export const transportErrors = new Rate('transport_errors')
export const serverErrors = new Rate('server_errors')
export const quotaLimited = new Rate('quota_limited')
export const rampMinute = new Gauge('ramp_minute')
const latency = new Trend('endpoint_ms', true)

export function expectStatus(res, ok, name) {
  const status = res.status || 0
  const err = String(res.error || '')
  const timedOut = status === 0 && /timeout/i.test(err)
  timeouts.add(timedOut)
  transportErrors.add(status === 0 && !timedOut)
  serverErrors.add(status >= 500 && status <= 599)
  quotaLimited.add(status === 429)
  latency.add(res.timings.duration, { endpoint: name })
  const good = ok.indexOf(status) !== -1
  unexpected.add(!good)
  check(res, { [name]: () => good })
  return good
}
