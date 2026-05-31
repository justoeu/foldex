import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { relativeTime } from './time'

// Minimal i18n translator stub — relativeTime just needs the (key, opts)
// shape and reasonable interpolation. We surface the count to verify which
// branch fired.
const t = (key: string, opts?: Record<string, unknown>): string => {
  const count = opts?.count
  if (typeof count === 'number') return `${count}|${key}`
  return key
}

describe('relativeTime', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date('2026-05-30T12:00:00Z'))
  })
  afterEach(() => {
    vi.useRealTimers()
  })

  it('returns "now" for very recent timestamps (< 45s)', () => {
    expect(relativeTime('2026-05-30T11:59:50Z', t)).toBe('common.time_now')
  })

  it('returns minutes for sub-hour distances', () => {
    expect(relativeTime('2026-05-30T11:55:00Z', t)).toBe('5|common.time_min_ago')
  })

  it('returns hours for sub-day distances', () => {
    expect(relativeTime('2026-05-30T08:00:00Z', t)).toBe('4|common.time_hr_ago')
  })

  it('returns days for sub-week distances', () => {
    expect(relativeTime('2026-05-27T12:00:00Z', t)).toBe('3|common.time_day_ago')
  })

  it('returns weeks for sub-5-week distances', () => {
    expect(relativeTime('2026-05-16T12:00:00Z', t)).toBe('2|common.time_wk_ago')
  })

  it('returns an ISO date for older timestamps', () => {
    expect(relativeTime('2026-01-01T12:00:00Z', t)).toBe('2026-01-01')
  })

  it('returns "" for invalid input', () => {
    expect(relativeTime('not a date', t)).toBe('')
  })

  it('clamps future timestamps to "now"', () => {
    // A small clock skew can make a server timestamp land slightly in the
    // future — we collapse to "now" rather than printing "-2m ago".
    expect(relativeTime('2026-05-30T12:05:00Z', t)).toBe('common.time_now')
  })

  it('accepts Date instances directly', () => {
    expect(relativeTime(new Date('2026-05-30T08:00:00Z'), t)).toBe('4|common.time_hr_ago')
  })
})
