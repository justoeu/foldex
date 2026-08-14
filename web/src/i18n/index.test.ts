import { beforeEach, describe, expect, it } from 'vitest'
import { sanitizePersistedLocale } from './index'

beforeEach(() => localStorage.clear())

describe('persisted locale validation', () => {
  it('keeps a supported locale', () => {
    localStorage.setItem('foldex.locale', 'pt')

    sanitizePersistedLocale()

    expect(localStorage.getItem('foldex.locale')).toBe('pt')
  })

  it('removes an unsupported locale so detection can fall back', () => {
    localStorage.setItem('foldex.locale', 'garbage')

    sanitizePersistedLocale()

    expect(localStorage.getItem('foldex.locale')).toBeNull()
  })
})
