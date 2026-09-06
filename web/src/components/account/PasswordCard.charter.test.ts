import { describe, expect, it } from 'vitest'
import { GENERATED_MAX_LENGTH } from '../../lib/generatePassword'
import { passwordGateLen } from '../../hooks/useInstancePolicy'
import { canSubmit, passwordEndpoint, passwordMode, passwordsMismatch } from './PasswordCard.submit'

/**
 * CC-DAE-010 — PasswordRow mounts change or create from user.has_password.
 *
 * INV-147: a signed-in change goes through POST /api/auth/password/change;
 * the current password is the whole step-up. INV-169: the client gate is
 * min(floor, 72), bcrypt's write bound.
 */
describe('PasswordRowModeCharter', () => {
  const atFloor = 'abcdefghijkl' // 12
  const underFloor = 'abcdefghij' // 10

  const cases: Array<{
    name: string
    input: Parameters<typeof canSubmit>[0]
    can: boolean
    mode: 'change' | 'create'
    endpoint: '/api/auth/password/change' | '/api/auth/password/set'
    mismatch: boolean
  }> = [
    {
      name: 'change × no 2FA × at floor × match',
      input: {
        hasPassword: true,
        needsStepUp: false,
        current: 'old-password-here',
        next: atFloor,
        confirm: atFloor,
        code: '',
        minLen: 12,
      },
      can: true,
      mode: 'change',
      endpoint: '/api/auth/password/change',
      mismatch: false,
    },
    {
      name: 'change × 2FA × at floor — current password is the step-up, no code',
      input: {
        hasPassword: true,
        needsStepUp: true,
        current: 'old-password-here',
        next: atFloor,
        confirm: atFloor,
        code: '',
        minLen: 12,
      },
      can: true,
      mode: 'change',
      endpoint: '/api/auth/password/change',
      mismatch: false,
    },
    {
      name: 'change × under floor',
      input: {
        hasPassword: true,
        needsStepUp: false,
        current: 'old-password-here',
        next: underFloor,
        confirm: underFloor,
        code: '',
        minLen: 12,
      },
      can: false,
      mode: 'change',
      endpoint: '/api/auth/password/change',
      mismatch: false,
    },
    {
      name: 'change × missing current',
      input: {
        hasPassword: true,
        needsStepUp: false,
        current: '',
        next: atFloor,
        confirm: atFloor,
        code: '',
        minLen: 12,
      },
      can: false,
      mode: 'change',
      endpoint: '/api/auth/password/change',
      mismatch: false,
    },
    {
      name: 'create × no 2FA × at floor × match',
      input: {
        hasPassword: false,
        needsStepUp: false,
        current: '',
        next: atFloor,
        confirm: atFloor,
        code: '',
        minLen: 12,
      },
      can: true,
      mode: 'create',
      endpoint: '/api/auth/password/set',
      mismatch: false,
    },
    {
      name: 'create × 2FA × missing code',
      input: {
        hasPassword: false,
        needsStepUp: true,
        current: '',
        next: atFloor,
        confirm: atFloor,
        code: '',
        minLen: 12,
      },
      can: false,
      mode: 'create',
      endpoint: '/api/auth/password/set',
      mismatch: false,
    },
    {
      name: 'create × 2FA × six-digit code',
      input: {
        hasPassword: false,
        needsStepUp: true,
        current: '',
        next: atFloor,
        confirm: atFloor,
        code: '123456',
        minLen: 12,
      },
      can: true,
      mode: 'create',
      endpoint: '/api/auth/password/set',
      mismatch: false,
    },
    {
      name: 'create × mismatch — button stays enabled, submit refuses',
      input: {
        hasPassword: false,
        needsStepUp: false,
        current: '',
        next: atFloor,
        confirm: 'abcdefghijxx',
        code: '',
        minLen: 12,
      },
      can: true,
      mode: 'create',
      endpoint: '/api/auth/password/set',
      mismatch: true,
    },
  ]

  it.each(cases)('$name', ({ input, can, mode, endpoint, mismatch }) => {
    expect(passwordMode(input.hasPassword)).toBe(mode)
    expect(passwordEndpoint(input.hasPassword)).toBe(endpoint)
    expect(canSubmit(input)).toBe(can)
    expect(passwordsMismatch(input.next, input.confirm)).toBe(mismatch)
  })

  it('INV-169: a floor above bcrypt\'s 72 still gates at 72, not at the floor', () => {
    expect(passwordGateLen(80)).toBe(GENERATED_MAX_LENGTH)
    const base = {
      hasPassword: true,
      needsStepUp: false,
      current: 'old-password-here',
      confirm: 'x'.repeat(71),
      code: '',
      minLen: 80,
    }
    expect(canSubmit({ ...base, next: 'x'.repeat(71), confirm: 'x'.repeat(71) })).toBe(false)
    expect(canSubmit({ ...base, next: 'x'.repeat(72), confirm: 'x'.repeat(72) })).toBe(true)
  })
})
