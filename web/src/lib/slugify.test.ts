import { describe, it, expect } from 'vitest'
import { slugifyClient } from './slugify'

describe('slugifyClient', () => {
  it('lowercases and hyphenates', () => {
    expect(slugifyClient('Hello World')).toBe('hello-world')
  })

  it('strips accents', () => {
    expect(slugifyClient('Café')).toBe('cafe')
  })

  it('returns empty for symbols-only', () => {
    expect(slugifyClient('!!!')).toBe('')
  })

  it('matches slug.Slugify on hyphen-boundary truncation', () => {
    const long =
      'this-is-a-very-long-title-that-definitely-exceeds-the-80-character-slug-limit-and-must-be-truncated-correctly'
    expect(slugifyClient(long)).toBe(
      'this-is-a-very-long-title-that-definitely-exceeds-the-80-character-slug-limit',
    )
    expect(slugifyClient(long).length).toBeLessThanOrEqual(80)
    expect(slugifyClient(long).endsWith('-')).toBe(false)

    const prose =
      'Refactor the issuing flow for cross-border international wire transfers in the post-migration codebase to make sure observability still holds end to end'
    expect(slugifyClient(prose)).toBe(
      'refactor-the-issuing-flow-for-cross-border-international-wire-transfers-in-the',
    )

    const exactCap = 'a'.repeat(80)
    expect(slugifyClient(exactCap)).toBe(exactCap)
  })
})
