import { describe, it, expect } from 'vitest'
import { isGradient, makeGradient, primaryColor, parseGradient, hexToHsl, hslToHex } from './tagColor'

describe('isGradient', () => {
  it('returns true for linear-gradient strings', () => {
    expect(isGradient('linear-gradient(135deg, #f00, #0f0)')).toBe(true)
  })
  it('returns true for radial-gradient strings', () => {
    expect(isGradient('radial-gradient(circle, #f00, #0f0)')).toBe(true)
  })
  it('returns true for conic-gradient and case-insensitive prefix', () => {
    expect(isGradient('CONIC-gradient(from 0deg, red, blue)')).toBe(true)
  })
  it('returns false for solid hex', () => {
    expect(isGradient('#6366F1')).toBe(false)
  })
  it('returns false for rgb()', () => {
    expect(isGradient('rgb(99, 102, 241)')).toBe(false)
  })
  it('returns false for empty', () => {
    expect(isGradient('')).toBe(false)
  })
})

describe('makeGradient', () => {
  it('builds a 135deg linear-gradient with two stops', () => {
    expect(makeGradient('#f00', '#0f0')).toBe('linear-gradient(135deg, #f00, #0f0)')
  })
  it('accepts named colors', () => {
    expect(makeGradient('red', 'blue')).toBe('linear-gradient(135deg, red, blue)')
  })
})

describe('primaryColor', () => {
  it('returns input unchanged for solid colors', () => {
    expect(primaryColor('#6366F1')).toBe('#6366F1')
  })
  it('extracts the first hex stop from a gradient', () => {
    expect(primaryColor('linear-gradient(135deg, #6366F1, #EC4899)')).toBe('#6366F1')
  })
  it('extracts rgba() stops', () => {
    expect(primaryColor('linear-gradient(45deg, rgba(255,0,0,.5), rgba(0,255,0,.5))')).toBe('rgba(255,0,0,.5)')
  })
  it('falls back to the default when no color is parseable', () => {
    expect(primaryColor('linear-gradient(135deg)')).toBe('#6366F1')
  })
})

describe('parseGradient', () => {
  it('returns (color, color) for a solid input', () => {
    expect(parseGradient('#6366F1')).toEqual({ from: '#6366F1', to: '#6366F1' })
  })
  it('returns both stops from a gradient', () => {
    expect(parseGradient('linear-gradient(135deg, #6366F1, #EC4899)')).toEqual({
      from: '#6366F1',
      to: '#EC4899',
    })
  })
  it('falls back when the gradient is malformed', () => {
    expect(parseGradient('linear-gradient(135deg)')).toEqual({
      from: '#6366F1',
      to: '#EC4899',
    })
  })
  it('mirrors the only stop when a gradient has just one color', () => {
    expect(parseGradient('linear-gradient(135deg, #abc)')).toEqual({ from: '#abc', to: '#abc' })
  })
})

describe('hexToHsl', () => {
  it('decodes pure red', () => {
    const hsl = hexToHsl('#ff0000')
    expect(hsl.h).toBe(0)
    expect(Math.round(hsl.s)).toBe(100)
    expect(Math.round(hsl.l)).toBe(50)
  })
  it('decodes pure green at hue 120', () => {
    const hsl = hexToHsl('#00ff00')
    expect(Math.round(hsl.h)).toBe(120)
  })
  it('decodes pure blue at hue 240', () => {
    const hsl = hexToHsl('#0000ff')
    expect(Math.round(hsl.h)).toBe(240)
  })
  it('accepts shorthand #rgb', () => {
    const hsl = hexToHsl('#f00')
    expect(hsl.h).toBe(0)
    expect(Math.round(hsl.s)).toBe(100)
  })
  it('falls back to neutral on malformed input', () => {
    expect(hexToHsl('not-a-color')).toEqual({ h: 0, s: 0, l: 50 })
    expect(hexToHsl('#zz')).toEqual({ h: 0, s: 0, l: 50 })
  })
})

describe('hslToHex', () => {
  it('round-trips primary colors', () => {
    expect(hslToHex(0, 100, 50)).toBe('#ff0000')
    expect(hslToHex(120, 100, 50)).toBe('#00ff00')
    expect(hslToHex(240, 100, 50)).toBe('#0000ff')
  })
  it('wraps hue past 360', () => {
    expect(hslToHex(360, 100, 50)).toBe('#ff0000')
    expect(hslToHex(-120, 100, 50)).toBe('#0000ff')
  })
  it('clamps saturation and lightness', () => {
    expect(hslToHex(200, 999, 999)).toBe('#ffffff')
    expect(hslToHex(200, -50, -50)).toBe('#000000')
  })
})

describe('hexToHsl + hslToHex roundtrip', () => {
  it('preserves a known indigo within 1 step', () => {
    const { h, s, l } = hexToHsl('#6366F1')
    const back = hexToHsl(hslToHex(h, s, l))
    // round-trip through HSL drops a couple of LSBs; tolerate ≤1° hue drift
    expect(Math.abs(back.h - h)).toBeLessThanOrEqual(1)
    expect(Math.abs(back.s - s)).toBeLessThanOrEqual(2)
    expect(Math.abs(back.l - l)).toBeLessThanOrEqual(2)
  })
})
