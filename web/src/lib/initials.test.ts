import { describe, it, expect } from 'vitest'
import { initialsOf } from './initials'

describe('initialsOf', () => {
  it('derives initials from first and last words, falling back to e-mail', () => {
    expect(initialsOf('Valmir Justo', 'v@x.test')).toBe('VJ')
    expect(initialsOf('grace', 'g@x.test')).toBe('G')
    expect(initialsOf('', 'grace@x.test')).toBe('G')
    expect(initialsOf('  ', '')).toBe('?')
    expect(initialsOf('ada lovelace king', 'a@x.test')).toBe('AK')
  })
})
