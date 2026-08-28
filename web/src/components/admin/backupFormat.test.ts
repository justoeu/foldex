import { describe, it, expect } from 'vitest'
import { formatBytes, formatMinutes, drillTables, drillTableCount, statusTone } from './backupFormat'

describe('formatBytes', () => {
  it('keeps raw bytes below a kilobyte', () => {
    expect(formatBytes(0)).toBe('0 B')
    expect(formatBytes(1023)).toBe('1023 B')
  })

  // One decimal under ten, none above: "1.3 MB" is useful, "1.3 KB" beside
  // "947 KB" is not — the precision follows the magnitude.
  it('scales through the units and drops the decimal past ten', () => {
    expect(formatBytes(1024)).toBe('1.0 KB')
    expect(formatBytes(134_000)).toBe('131 KB')
    expect(formatBytes(5 * 1024 * 1024)).toBe('5.0 MB')
    expect(formatBytes(42 * 1024 * 1024)).toBe('42 MB')
    expect(formatBytes(3 * 1024 ** 4)).toBe('3.0 TB')
  })

  it('stops at terabytes instead of inventing a larger unit', () => {
    expect(formatBytes(9999 * 1024 ** 4)).toBe('9999 TB')
  })
})

describe('formatMinutes', () => {
  it('reads as minutes below an hour and as hours above it', () => {
    expect(formatMinutes(30)).toBe('30m')
    expect(formatMinutes(60)).toBe('1h')
    expect(formatMinutes(720)).toBe('12h')
  })
})

/*
 * The drill's counts come from a jsonb column, so the shape is whatever the
 * agent wrote. Anything that is not a number is dropped rather than rendered:
 * a chip reading "link: undefined" would claim a comparison that never ran.
 */
describe('drillTables', () => {
  it('keeps only numeric entries', () => {
    expect(drillTables({ tables: { link: 42, note: 'seven', tag: 11 } })).toEqual([
      ['link', 42],
      ['tag', 11],
    ])
  })

  it('returns nothing for missing, null or array-shaped meta', () => {
    expect(drillTables({})).toEqual([])
    expect(drillTables({ tables: null })).toEqual([])
    expect(drillTables({ tables: ['link', 42] })).toEqual([])
    expect(drillTableCount({ tables: { link: 1, note: 2 } })).toBe(2)
    expect(drillTableCount({})).toBe(0)
  })
})

describe('statusTone', () => {
  it('gives every non-terminal status the warning tone', () => {
    expect(statusTone('succeeded')).toBe(' fx-chip-ok')
    expect(statusTone('failed')).toBe(' fx-chip-danger')
    expect(statusTone('running')).toBe(' fx-chip-warn')
    expect(statusTone('requested')).toBe(' fx-chip-warn')
  })
})
