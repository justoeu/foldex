import { describe, it, expect } from 'vitest'
import {
  ALPHABET,
  generatePassword,
  GENERATED_LENGTH,
  GENERATED_MAX_LENGTH,
} from './generatePassword'

describe('generatePassword', () => {
  it('honours the instance floor and never falls below its own', () => {
    expect(generatePassword(8)).toHaveLength(GENERATED_LENGTH)
    expect(generatePassword(32)).toHaveLength(32)
    // The alphabet is ASCII, so characters and the bytes bcrypt truncates in
    // are the same unit — generating past 72 would be decorative tail.
    expect(generatePassword(200)).toHaveLength(GENERATED_MAX_LENGTH)
  })

  it('survives a caller with no floor at all', () => {
    expect(generatePassword()).toHaveLength(GENERATED_LENGTH)
    expect(generatePassword(NaN)).toHaveLength(GENERATED_LENGTH)
    expect(generatePassword(-5)).toHaveLength(GENERATED_LENGTH)
  })

  it('omits the characters that do not survive being read aloud', () => {
    // Constructed rather than sampled: 400 draws at length 72 is ~29k
    // characters, so any of these appearing at all is a defect, not luck.
    const bulk = Array.from({ length: 400 }, () => generatePassword(72)).join('')
    for (const ch of '0O1lI') expect(bulk).not.toContain(ch)
  })

  it('draws from the whole alphabet, so no character is unreachable', () => {
    const seen = new Set(Array.from({ length: 400 }, () => generatePassword(72)).join(''))
    expect(seen.size).toBe(ALPHABET.length)
  })

  it('does not repeat itself', () => {
    const draws = new Set(Array.from({ length: 200 }, () => generatePassword()))
    expect(draws.size).toBe(200)
  })

  it('rejects biased bytes instead of folding them in with a modulo', () => {
    // 256 is not a multiple of 57, so bytes >= 228 must be DISCARDED; a plain
    // `%` would fold them onto the first 28 letters and make those ~1.3x
    // likelier. The stub alternates one rejected byte (230) with one accepted
    // byte (5), continuing its cursor across refills so a short final buffer
    // still terminates.
    //
    // Rejection sampling therefore yields index 5 every time. A modulo
    // implementation would alternate index 5 with 230 % 57 = 2, so this
    // assertion is exactly the one it fails.
    const real = crypto.getRandomValues.bind(crypto)
    // 228 is the bound ITSELF. With 230 alone, changing `>=` to `>` accepts
    // 228, folds it onto index 0, and the whole suite stays green.
    const pattern = [228, 230, 5]
    let cursor = 0
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    ;(crypto as any).getRandomValues = (a: ArrayBufferView) => {
      const view = a as Uint8Array
      for (let i = 0; i < view.length; i++) view[i] = pattern[cursor++ % pattern.length]
      return a
    }
    try {
      expect(generatePassword(20)).toBe('f'.repeat(20))
    } finally {
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      ;(crypto as any).getRandomValues = real
    }
  })
})
