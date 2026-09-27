import { check } from 'k6'
import { Rate, Trend } from 'k6/metrics'

// unexpected counts responses outside the statuses that flow considers
// healthy. A 429 from the write quota is expected on the stress profile
// and must be passed in `ok` so it does not fail the run.
export const unexpected = new Rate('unexpected_status')
const latency = new Trend('endpoint_ms', true)

export function expectStatus(res, ok, name) {
  latency.add(res.timings.duration, { endpoint: name })
  const good = ok.indexOf(res.status) !== -1
  unexpected.add(!good)
  check(res, { [name]: () => good })
  return good
}
