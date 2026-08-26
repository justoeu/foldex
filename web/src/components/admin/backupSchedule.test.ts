import { describe, it, expect } from 'vitest'
import { reducesProtection } from './backupSchedule'
import type { BackupAgentJobReport } from '../../api/admin'

const report = (schedule: string): BackupAgentJobReport => ({
  capable: true,
  source: 'env',
  schedule,
})

describe('reducesProtection', () => {
  it('always confirms a DELETE — the baseline it returns to is not on screen', () => {
    expect(reducesProtection('dump', { times: ['03:30'] }, null, report('03:30'))).toBe(true)
    expect(reducesProtection('user_zip', null, null, null)).toBe(true)
  })

  it('flags a dump with fewer times than the stored row', () => {
    const stored = { times: ['03:30', '15:30'] }
    expect(reducesProtection('dump', stored, { times: ['03:30'] }, null)).toBe(true)
    expect(reducesProtection('dump', stored, { times: ['03:30', '15:30'] }, null)).toBe(false)
    expect(reducesProtection('dump', stored, { times: ['01:00', '09:00', '17:00'] }, null)).toBe(false)
  })

  it('reads the dump baseline from the agent render when no row exists', () => {
    expect(reducesProtection('dump', null, { times: ['03:30'] }, report('03:30, 15:30'))).toBe(true)
    expect(reducesProtection('dump', null, { times: ['03:30', '15:30'] }, report('03:30, 15:30'))).toBe(false)
  })

  it('claims no reduction when nothing states the current dump agenda', () => {
    expect(reducesProtection('dump', null, { times: ['03:30'] }, null)).toBe(false)
    expect(reducesProtection('dump', null, { times: ['03:30'] }, undefined)).toBe(false)
  })

  it('flags a mirror stretched to a LONGER interval, from row or agent baseline', () => {
    expect(reducesProtection('mirror', { interval_min: 60 }, { interval_min: 120 }, null)).toBe(true)
    expect(reducesProtection('mirror', { interval_min: 60 }, { interval_min: 30 }, null)).toBe(false)
    expect(reducesProtection('mirror', { interval_min: 60 }, { interval_min: 60 }, null)).toBe(false)
    expect(reducesProtection('mirror', null, { interval_min: 720 }, report('every 360m'))).toBe(true)
    expect(reducesProtection('mirror', null, { interval_min: 60 }, report('every 360m'))).toBe(false)
    expect(reducesProtection('mirror', null, { interval_min: 720 }, null)).toBe(false)
  })

  it('flags switching user_zip off, but only when it is on today', () => {
    expect(reducesProtection('user_zip', { enabled: true, time: '02:30' }, { enabled: false }, null)).toBe(true)
    expect(reducesProtection('user_zip', { enabled: false }, { enabled: false }, null)).toBe(false)
    expect(
      reducesProtection('user_zip', { enabled: false }, { enabled: true, time: '02:30' }, null),
    ).toBe(false)
    // No row: the agent's render is the baseline — "disabled" means already off.
    expect(reducesProtection('user_zip', null, { enabled: false }, report('02:30'))).toBe(true)
    expect(reducesProtection('user_zip', null, { enabled: false }, report('disabled'))).toBe(false)
    expect(reducesProtection('user_zip', null, { enabled: false }, null)).toBe(false)
  })

  it('never flags the drill — weekly by design, a row only moves the slot', () => {
    expect(
      reducesProtection('drill', { time: '01:00', weekday: 'sun' }, { time: '23:00', weekday: 'sat' }, report('01:00 sun')),
    ).toBe(false)
  })
})
