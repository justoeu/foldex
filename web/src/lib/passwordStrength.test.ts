import { describe, it, expect } from 'vitest'
import { passwordChecks, passwordScore } from './passwordStrength'

describe('passwordChecks', () => {
  it('detects character classes and length tiers', () => {
    expect(passwordChecks('abc')).toMatchObject({ length: false, lower: true, upper: false, digit: false, symbol: false })
    expect(passwordChecks('Abcdefg1!')).toMatchObject({ length: true, lower: true, upper: true, digit: true, symbol: true })
    expect(passwordChecks('abcdefghijkl').longer).toBe(true)
  })
})

describe('passwordScore', () => {
  it('is 0 for empty', () => {
    expect(passwordScore('')).toBe(0)
  })
  it('is weak for short/simple', () => {
    expect(passwordScore('abc')).toBe(1)
    expect(passwordScore('abcdefgh')).toBe(1) // length only, single class → weak
    expect(passwordScore('abcdefgh1')).toBe(2) // length + digit → fair
  })
  it('climbs with length + variety', () => {
    expect(passwordScore('Abcdefg1')).toBe(3) // length + case + digit
    expect(passwordScore('Abcdefghijk1!')).toBe(4) // length + longer + case + digit + symbol
  })
  it('is monotonic-ish: strong password scores 4', () => {
    expect(passwordScore('S0me-Very-Long-Pass!')).toBe(4)
  })
})
