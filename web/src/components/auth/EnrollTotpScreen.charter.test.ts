import { describe, expect, it } from 'vitest'
import {
  enrollMaySkip,
  enrollPhase,
  enrollSubmitErrorKey,
  initialEnrollMethod,
} from './EnrollTotpScreen.state'

/**
 * CC-DAE-006 — EnrollTotpScreen is a state machine
 * choose → enroll(totp|email) → confirm → codes.
 *
 * INV-038: admin 2FA is a divert onto this screen, not a refusal — there is
 * no skip. INV-174: the screen swap itself must not blank the glass; this
 * charter only owns the in-screen phases, not the AuthGate transition.
 */
describe('EnrollTotpScreenStateCharter', () => {
  const totp = {
    secret: 'JBSWY3DPEHPK3PXP',
    otpauth: 'otpauth://totp/x',
    issuer: 'Foldex',
    account: 'a@b.test',
    qr_url: '/api/auth/2fa/totp/qr.png',
  }
  const mailed = { account: 'a•••@b.test', expires_in: 300, digits: 6 }
  const pending = { status: 'authenticated' as const }

  it('chooser: an SMTP instance asks which method before minting anything', () => {
    expect(initialEnrollMethod(true)).toBeNull()
    expect(
      enrollPhase({
        method: null,
        totp: null,
        mailed: null,
        codes: null,
        pendingSession: null,
      }),
    ).toBe('choose')
  })

  it('auto-totp: no e-mail delivery skips the chooser and starts the authenticator', () => {
    expect(initialEnrollMethod(false)).toBe('totp')
    expect(
      enrollPhase({
        method: 'totp',
        totp: null,
        mailed: null,
        codes: null,
        pendingSession: null,
      }),
    ).toBe('enroll')
  })

  it('confirm: a ready enrollment is the code-entry form, totp or e-mail', () => {
    expect(
      enrollPhase({
        method: 'totp',
        totp,
        mailed: null,
        codes: null,
        pendingSession: null,
      }),
    ).toBe('confirm')
    expect(
      enrollPhase({
        method: 'email',
        totp: null,
        mailed,
        codes: null,
        pendingSession: null,
      }),
    ).toBe('confirm')
  })

  it('codes: recovery codes are shown before the session is adopted', () => {
    expect(
      enrollPhase({
        method: 'totp',
        totp,
        mailed: null,
        codes: ['AAAA-BBBB'],
        pendingSession: pending,
      }),
    ).toBe('codes')
  })

  it('no-skip: the policy diverts, it does not refuse, and it does not offer an exit into a session', () => {
    expect(enrollMaySkip()).toBe(false)
  })

  it.each([
    [{ response: { data: { error: { code: 'invalid_code' } } } }, 'auth_errors.invalid_code'],
    [{ response: { data: { error: { code: 'challenge_invalid' } } } }, 'auth_otp.expired'],
    [{ response: { status: 0 } }, 'auth_errors.network'],
    [{ response: { status: 500 } }, 'auth_errors.generic'],
  ] as const)('maps %j to %s', (err, key) => {
    expect(enrollSubmitErrorKey(err)).toBe(key)
  })
})
