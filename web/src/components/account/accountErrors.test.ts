import { describe, it, expect } from 'vitest'
import { accountErrorMessage } from './accountErrors'

/**
 * The mapper exists so credential copy cannot drift between the password form,
 * the Google link and the unlink. A code it does not know ships the generic
 * message; a code it DOES know must never fall through to it, which is the
 * failure this table catches — the branches were at 22% before it.
 */
const t = (key: string, opts?: Record<string, unknown>) =>
  opts && 'count' in opts ? `${key}:${String(opts.count)}` : key

function err(code: string) {
  return { response: { status: 400, data: { error: { code } } } }
}

describe('accountErrorMessage', () => {
  it.each([
    ['invalid_credentials', 'account.wrong_password'],
    ['password_required', 'account.password_required_first'],
    ['password_exists', 'account.password_already_set'],
    ['invalid_code', 'auth_errors.invalid_code'],
    ['password_too_long', 'auth_errors.password_too_long'],
    ['not_linked', 'account.google_not_connected'],
    ['too_many_attempts', 'auth_errors.too_many_attempts'],
  ])('maps %s to its own message', (code, key) => {
    expect(accountErrorMessage(err(code), t)).toBe(key)
  })

  // Carries the floor rather than implying one.
  it('states the minimum length for a short password', () => {
    expect(accountErrorMessage(err('password_too_short'), t)).toMatch(
      /^auth_errors\.password_too_short:\d+$/,
    )
  })

  it.each([['server_error'], ['']])('falls back to the generic message for %s', (code) => {
    expect(accountErrorMessage(err(code), t)).toBe('auth_errors.generic')
  })

  it('falls back for a non-API error', () => {
    expect(accountErrorMessage(new Error('offline'), t)).toBe('auth_errors.generic')
  })
})
