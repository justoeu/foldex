import { describe, it, expect } from 'vitest'
import {
  ABUSE_BANDS, blockReasonKey, boundOf, observedFor,
  restoreBandDefaults, sortAnomalies, spanMinutes,
} from './abuseFormat'
import type { AbuseBound, AbuseObserved, AbusePolicy, Anomaly } from '../../api/admin'

const BOUNDS: AbuseBound[] = [
  { field: 'login_failures_per_account', min: 3, max: 50, default: 5 },
  { field: 'api_writes_per_minute', min: 30, max: 6000, default: 120 },
]

const POLICY: AbusePolicy = {
  login_distinct_accounts_per_ip: 10,
  login_failures_per_account: 41,
  login_window_minutes: 15,
  api_writes_per_minute: 4000,
  api_expensive_per_hour: 20,
  public_click_coalesce_seconds: 0,
  anomaly_spray_accounts: 10,
  anomaly_hammer_failures: 20,
  anomaly_window_minutes: 15,
}

const OBSERVED: AbuseObserved = {
  window_days: 30,
  max_distinct_accounts_per_ip: 2,
  max_failures_per_account: 0,
  peak_writes_per_minute: 340,
}

function anomaly(over: Partial<Anomaly> = {}): Anomaly {
  return {
    kind: 'spray', ip: '10.0.0.1', ip_trusted: false,
    distinct_accounts: 14, failures: 22, throttles: 0,
    first_seen: '2026-08-28T10:00:00Z', last_seen: '2026-08-28T10:09:00Z',
    blocked: false, severity: 'warning', ...over,
  }
}

describe('boundOf', () => {
  it('finds a knob by NAME, never by position', () => {
    expect(boundOf(BOUNDS, 'api_writes_per_minute')).toEqual(BOUNDS[1])
  })

  // A knob the payload said nothing about still renders — an input with no
  // advertised range beats a field that silently disappears.
  it('answers null for a knob the payload did not describe', () => {
    expect(boundOf(BOUNDS, 'anomaly_window_minutes')).toBeNull()
  })
})

describe('observedFor', () => {
  it('maps a knob to the measurement that informs it', () => {
    expect(observedFor('api_writes_per_minute', OBSERVED)).toBe(340)
  })

  // Zero is "nothing seen", and the caller renders it as such. Returning null
  // for it would make it indistinguishable from a knob with no counterpart.
  it('keeps a zero distinct from a knob nothing measures', () => {
    expect(observedFor('login_failures_per_account', OBSERVED)).toBe(0)
    expect(observedFor('login_window_minutes', OBSERVED)).toBeNull()
    expect(observedFor('anomaly_spray_accounts', OBSERVED)).toBeNull()
  })

  it('treats a missing measurement block as no data, not as absence', () => {
    expect(observedFor('api_writes_per_minute', undefined)).toBe(0)
    expect(observedFor('login_distinct_accounts_per_ip', undefined)).toBe(0)
    expect(observedFor('login_failures_per_account', undefined)).toBe(0)
  })

  it('maps the breadth knob to the breadth measurement', () => {
    expect(observedFor('login_distinct_accounts_per_ip', OBSERVED)).toBe(2)
  })
})

describe('restoreBandDefaults', () => {
  it('restores only the band asked for', () => {
    const login = ABUSE_BANDS.find((b) => b.id === 'login')!
    const out = restoreBandDefaults(POLICY, BOUNDS, login)
    expect(out.login_failures_per_account).toBe(5)
    expect(out.api_writes_per_minute).toBe(4000)
  })

  it('leaves a knob the payload advertised no default for exactly as it was', () => {
    const detection = ABUSE_BANDS.find((b) => b.id === 'detection')!
    expect(restoreBandDefaults(POLICY, BOUNDS, detection)).toEqual(POLICY)
  })
})

describe('ABUSE_BANDS', () => {
  it('covers all nine knobs exactly once', () => {
    const fields = ABUSE_BANDS.flatMap((b) => b.fields)
    expect(new Set(fields).size).toBe(9)
    expect(fields).toHaveLength(9)
  })

  // The distinction the screen exists to make: three of these report, six act.
  it('marks the detection band as the one that enforces nothing', () => {
    expect(ABUSE_BANDS.filter((b) => !b.enforces).map((b) => b.id)).toEqual(['detection'])
  })
})

describe('sortAnomalies', () => {
  it('puts critical first and the most recent first within a severity', () => {
    const out = sortAnomalies([
      anomaly({ ip: 'a', severity: 'warning' }),
      anomaly({ ip: 'b', severity: 'critical', last_seen: '2026-08-28T09:00:00Z' }),
      anomaly({ ip: 'c', severity: 'critical', last_seen: '2026-08-28T11:00:00Z' }),
    ])
    expect(out.map((a) => a.ip)).toEqual(['c', 'b', 'a'])
  })

  it('does not mutate the array it was handed', () => {
    const input = [anomaly({ ip: 'a', severity: 'warning' }), anomaly({ ip: 'b', severity: 'critical' })]
    sortAnomalies(input)
    expect(input.map((a) => a.ip)).toEqual(['a', 'b'])
  })
})

describe('spanMinutes', () => {
  it('measures the span the evidence line reports', () => {
    expect(spanMinutes(anomaly())).toBe(9)
  })

  // A clock skew that puts last_seen before first_seen must not print "-3 min".
  it('never reports a negative span', () => {
    expect(spanMinutes(anomaly({ last_seen: '2026-08-28T09:00:00Z' }))).toBe(0)
  })

  it('reports zero for an unparseable timestamp instead of NaN', () => {
    expect(spanMinutes(anomaly({ last_seen: 'not-a-date' }))).toBe(0)
  })
})

describe('blockReasonKey', () => {
  it('derives a distinct reason per signal', () => {
    const keys = (['spray', 'hammer', 'throttle'] as const).map(blockReasonKey)
    expect(new Set(keys).size).toBe(3)
    expect(keys[0]).toMatch(/spray$/)
  })
})
