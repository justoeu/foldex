import { describe, it, expect } from 'vitest'
import { firingsPerWeek, reducesProtection } from './backupSchedule'
import type { BackupScheduleConfig as Cfg } from '../../api/admin'

const daily: Cfg = { mode: 'times', times: ['03:30'], weekdays: ['sun', 'mon', 'tue', 'wed', 'thu', 'fri', 'sat'] }

describe('firingsPerWeek', () => {
  it('multiplies times by weekdays — the only honest measure of "how often"', () => {
    expect(firingsPerWeek(daily)).toBe(7)
    expect(firingsPerWeek({ mode: 'times', times: ['03:30', '15:30'], weekdays: ['mon', 'wed', 'fri'] })).toBe(6)
    expect(firingsPerWeek({ mode: 'times', times: ['01:00'], weekdays: ['sun'] })).toBe(1)
  })

  it('turns an interval into firings over the 10080 minutes of a week', () => {
    expect(firingsPerWeek({ mode: 'interval', interval_min: 1440 })).toBe(7)
    expect(firingsPerWeek({ mode: 'interval', interval_min: 360 })).toBe(28)
    expect(firingsPerWeek({ mode: 'interval', interval_min: 15 })).toBe(672)
  })

  it('counts a switched-off job as zero, whatever else the config holds', () => {
    expect(firingsPerWeek({ mode: 'times', enabled: false })).toBe(0)
    expect(firingsPerWeek({ enabled: false })).toBe(0)
    expect(firingsPerWeek({ mode: 'interval', interval_min: 60, enabled: false })).toBe(0)
  })

  it('claims nothing about a config it cannot read', () => {
    expect(firingsPerWeek({})).toBeNaN()
    expect(firingsPerWeek({ enabled: true })).toBeNaN()
    // A mode whose own field is missing is exactly as unknown as no mode.
    expect(firingsPerWeek({ mode: 'times', times: ['03:30'] })).toBeNaN()
    expect(firingsPerWeek({ mode: 'interval' })).toBeNaN()
  })
})

describe('reducesProtection', () => {
  it('always confirms a DELETE — the baseline it returns to is not on screen', () => {
    expect(reducesProtection(daily, null, daily)).toBe(true)
    expect(reducesProtection(null, null, null)).toBe(true)
  })

  it('flags fewer weekdays on the same times', () => {
    const stored: Cfg = { mode: 'times', times: ['03:30'], weekdays: ['mon', 'wed', 'fri'] }
    expect(reducesProtection(stored, { mode: 'times', times: ['03:30'], weekdays: ['mon'] }, null)).toBe(true)
    expect(reducesProtection(stored, { mode: 'times', times: ['03:30'], weekdays: ['mon', 'wed', 'fri'] }, null)).toBe(false)
    expect(
      reducesProtection(stored, { mode: 'times', times: ['03:30'], weekdays: ['mon', 'tue', 'wed', 'thu', 'fri'] }, null),
    ).toBe(false)
  })

  it('flags fewer times on the same weekdays', () => {
    const stored: Cfg = { mode: 'times', times: ['03:30', '15:30'], weekdays: ['sun'] }
    expect(reducesProtection(stored, { mode: 'times', times: ['03:30'], weekdays: ['sun'] }, null)).toBe(true)
    expect(
      reducesProtection(stored, { mode: 'times', times: ['03:30', '09:00', '15:30'], weekdays: ['sun'] }, null),
    ).toBe(false)
  })

  it('compares ACROSS the two modes — fewer firings is fewer firings', () => {
    // 2 times × 7 days = 14/week; every 12h = 14/week. Equal is not a cut.
    expect(reducesProtection({ mode: 'interval', interval_min: 720 }, { mode: 'times', times: ['03:30', '15:30'], weekdays: daily.weekdays }, null)).toBe(false)
    // …but one weekly slot instead of that interval is.
    expect(reducesProtection({ mode: 'interval', interval_min: 720 }, { mode: 'times', times: ['03:30'], weekdays: ['sun'] }, null)).toBe(true)
  })

  it('flags a stretched interval and never a tightened one', () => {
    const stored: Cfg = { mode: 'interval', interval_min: 60 }
    expect(reducesProtection(stored, { mode: 'interval', interval_min: 120 }, null)).toBe(true)
    expect(reducesProtection(stored, { mode: 'interval', interval_min: 30 }, null)).toBe(false)
    expect(reducesProtection(stored, { mode: 'interval', interval_min: 60 }, null)).toBe(false)
  })

  it('flags switching a job off, but only when it runs today', () => {
    expect(reducesProtection(daily, { mode: 'times', enabled: false }, null)).toBe(true)
    expect(reducesProtection({ mode: 'times', enabled: false }, { mode: 'times', enabled: false }, null)).toBe(false)
    expect(reducesProtection({ mode: 'times', enabled: false }, daily, null)).toBe(false)
  })

  it('falls back to the ENV baseline when no row is stored yet', () => {
    // No stored row: what the job runs today is the agent's env agenda.
    expect(reducesProtection(null, { mode: 'times', times: ['03:30'], weekdays: ['sun'] }, daily)).toBe(true)
    expect(reducesProtection(null, { mode: 'times', times: ['03:30', '15:30'], weekdays: daily.weekdays }, daily)).toBe(false)
    // A stored row WINS over the baseline — it is what runs today.
    expect(
      reducesProtection({ mode: 'times', times: ['03:30'], weekdays: ['sun'] }, daily, { mode: 'times', times: ['03:30'], weekdays: ['sun'] }),
    ).toBe(false)
  })

  it('claims nothing when the current agenda is unknown', () => {
    expect(reducesProtection(null, { mode: 'times', times: ['03:30'], weekdays: ['sun'] }, null)).toBe(false)
    expect(reducesProtection(null, { mode: 'times', times: ['03:30'], weekdays: ['sun'] }, undefined)).toBe(false)
    // A report whose env agenda is off answers `{}` — no mode, nothing to compare.
    expect(reducesProtection(null, { mode: 'times', times: ['03:30'], weekdays: ['sun'] }, {})).toBe(false)
  })
})
