import { describe, it, expect } from 'vitest'
import { formatBytes } from './formatBytes'

describe('formatBytes', () => {
  it('keeps raw bytes below a kilobyte', () => {
    expect(formatBytes(0)).toBe('0 B')
    expect(formatBytes(1023)).toBe('1023 B')
  })

  it('scales through KB, MB and GB, dropping the decimal past ten', () => {
    expect(formatBytes(1024)).toBe('1.0 KB')
    expect(formatBytes(134_000)).toBe('131 KB')
    expect(formatBytes(5 * 1024 * 1024)).toBe('5.0 MB')
    expect(formatBytes(42 * 1024 * 1024)).toBe('42 MB')
    expect(formatBytes(2 * 1024 ** 3)).toBe('2.0 GB')
    expect(formatBytes(1500 * 1024 ** 3)).toBe('1.5 TB')
  })

  it('formats terabytes instead of capping at gigabytes', () => {
    expect(formatBytes(3 * 1024 ** 4)).toBe('3.0 TB')
    expect(formatBytes(9999 * 1024 ** 4)).toBe('9999 TB')
  })
})
