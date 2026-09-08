import { apiErrorCode, apiErrorStatus } from '../../lib/apiError'
import type { FactorMethod } from '../../api/twofa'

export type EnrollPhase = 'choose' | 'enroll' | 'confirm' | 'codes'

/**
 * Null means "ask". An instance with no SMTP offers no choice at all, so it
 * starts the authenticator rather than showing a one-button chooser — the
 * screen is mandatory and mid-login, and a question with one possible answer
 * is pure friction there.
 */
export function initialEnrollMethod(emailAvailable: boolean): FactorMethod | null {
  return emailAvailable ? null : 'totp'
}

export function enrollPhase(state: {
  method: FactorMethod | null
  totp: unknown | null
  mailed: unknown | null
  codes: string[] | null
  pendingSession: unknown | null
}): EnrollPhase {
  if (state.codes && state.pendingSession) return 'codes'
  if (!state.method) return 'choose'
  const ready = state.method === 'totp' ? state.totp !== null : state.mailed !== null
  return ready ? 'confirm' : 'enroll'
}

export function enrollSubmitErrorKey(err: unknown): string {
  const code = apiErrorCode(err)
  if (code === 'invalid_code') return 'auth_errors.invalid_code'
  if (code === 'challenge_invalid') return 'auth_otp.expired'
  if ((apiErrorStatus(err) ?? 0) === 0) return 'auth_errors.network'
  return 'auth_errors.generic'
}

/**
 * INV-038: AUTH_REQUIRE_2FA_FOR_ADMINS diverts onto this screen, it does not
 * refuse the login, and it has no privileged-session exception. There is
 * therefore no skip — the only way out is signing out.
 */
export function enrollMaySkip(): false {
  return false
}
