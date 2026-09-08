import { describe, it, expect } from 'vitest'
import { hintEqualsPassword } from './hintEqualsPassword'

describe('hintEqualsPassword', () => {
  it('trims both sides before comparing', () => {
    expect(hintEqualsPassword('secret', '  Secret')).toBe(true)
    expect(hintEqualsPassword('  secret  ', 'SECRET')).toBe(true)
    expect(hintEqualsPassword('hint', 'password')).toBe(false)
  })
})
