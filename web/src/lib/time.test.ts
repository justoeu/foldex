import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { relativeTime, nextCheckPreview } from './time'

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

describe('nextCheckPreview', () => {
  const now = new Date('2026-05-30T12:00:00Z')

  it('returns empty string when interval is null/undefined', () => {
    expect(nextCheckPreview(null, '2026-05-30T11:00:00Z', t, now)).toBe('')
    expect(nextCheckPreview(undefined, '2026-05-30T11:00:00Z', t, now)).toBe('')
  })

  it('returns "soon" when last_checked_at is null/undefined (first scan after opt-in)', () => {
    expect(nextCheckPreview('daily', null, t, now)).toBe('common.next_check_soon')
    expect(nextCheckPreview('hourly', undefined, t, now)).toBe('common.next_check_soon')
  })

  it('returns "soon" when last_checked_at is an invalid string', () => {
    expect(nextCheckPreview('daily', 'not a date', t, now)).toBe('common.next_check_soon')
  })

  it('returns "soon" when the interval has already elapsed (link is overdue)', () => {
    // hourly: last check 90 min ago → overdue 30 min
    expect(nextCheckPreview('hourly', '2026-05-30T10:30:00Z', t, now)).toBe('common.next_check_soon')
  })

  it('returns "soon" when the remaining window is ≤ 60s', () => {
    // hourly: last check 59 min 30 s ago → 30 s remaining → still "soon"
    expect(nextCheckPreview('hourly', '2026-05-30T11:00:30Z', t, now)).toBe('common.next_check_soon')
  })

  it('returns minutes for sub-hour remaining windows', () => {
    // hourly: last check 30 min ago → 30 min remaining
    expect(nextCheckPreview('hourly', '2026-05-30T11:30:00Z', t, now)).toBe('30|common.next_check_in_min')
  })

  it('returns hours for sub-day remaining windows', () => {
    // daily: last check 6h ago → 18h remaining
    expect(nextCheckPreview('daily', '2026-05-30T06:00:00Z', t, now)).toBe('18|common.next_check_in_hr')
  })

  it('returns days for multi-day remaining windows', () => {
    // weekly: last check 2 days ago → 5 days remaining
    expect(nextCheckPreview('weekly', '2026-05-28T12:00:00Z', t, now)).toBe('5|common.next_check_in_day')
  })

  it('handles daily fresh check correctly (full 23h59m remaining → 23h)', () => {
    // daily: last check 1 min ago → 23h 59m ≈ 1439 min ≈ 23h
    expect(nextCheckPreview('daily', '2026-05-30T11:59:00Z', t, now)).toBe('23|common.next_check_in_hr')
  })

  // Boundary at exactly 60 s remaining — `<= 60_000` keeps it in "soon".
  it('returns "soon" at the exact 60 s remaining boundary', () => {
    // hourly: last check 59 min ago → 60 s remaining
    expect(nextCheckPreview('hourly', '2026-05-30T11:01:00Z', t, now)).toBe('common.next_check_soon')
  })

  // Boundary at exactly 60 min remaining — must cross into the hour branch.
  it('crosses from minutes into hours at the 60 min boundary', () => {
    // daily: last check 23 h ago → exactly 60 min remaining
    expect(nextCheckPreview('daily', '2026-05-29T13:00:00Z', t, now)).toBe('1|common.next_check_in_hr')
  })

  // Boundary at exactly 24 h remaining — must cross into the day branch.
  it('crosses from hours into days at the 24 h boundary', () => {
    // weekly: last check 6 d ago → exactly 24 h remaining
    expect(nextCheckPreview('weekly', '2026-05-24T12:00:00Z', t, now)).toBe('1|common.next_check_in_day')
  })

  // Default `now` parameter — locks the parameter signature.
  it('falls back to Date.now() when `now` is omitted', () => {
    vi.useFakeTimers()
    try {
      vi.setSystemTime(new Date('2026-05-30T12:00:00Z'))
      expect(nextCheckPreview('hourly', '2026-05-30T11:30:00Z', t)).toBe('30|common.next_check_in_min')
    } finally {
      vi.useRealTimers()
    }
  })

  // Far-future timestamp (clock skew / hostile value) collapses to ISO date.
  it('returns an ISO date stamp for far-future check times (> 1 year out)', () => {
    // weekly with last_checked in year 9999 → ~9000 years remaining
    expect(nextCheckPreview('weekly', '9999-01-01T00:00:00Z', t, now)).toMatch(/^\d{4}-\d{2}-\d{2}$/)
  })
})
