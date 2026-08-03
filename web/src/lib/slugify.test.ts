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
})
