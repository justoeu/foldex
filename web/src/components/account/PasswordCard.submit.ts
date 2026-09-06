import { passwordGateLen } from '../../hooks/useInstancePolicy'
import { OTP_LENGTH } from '../auth/OtpInput'

export type PasswordMode = 'change' | 'create'

export function passwordMode(hasPassword: boolean): PasswordMode {
  return hasPassword ? 'change' : 'create'
}

/**
 * INV-147: a signed-in change goes through POST /api/auth/password/change;
 * creating a password on a Google-only account uses /password/set.
 */
export function passwordEndpoint(hasPassword: boolean): '/api/auth/password/change' | '/api/auth/password/set' {
  return hasPassword ? '/api/auth/password/change' : '/api/auth/password/set'
}

export function passwordsMismatch(next: string, confirm: string): boolean {
  return next !== confirm
}

/**
 * Client submit gate. Mismatch is NOT in here: the button stays enabled so
 * the form can say why, rather than looking broken. INV-169: the length
 * bound is min(floor, 72). INV-147: `needsStepUp` is ignored when there is
 * already a password — the current password is the whole step-up.
 */
export function canSubmit({
  hasPassword,
  needsStepUp,
  current,
  next,
  confirm,
  code,
  minLen,
}: {
  hasPassword: boolean
  needsStepUp: boolean
  current: string
  next: string
  confirm: string
  code: string
  minLen: number
}): boolean {
  if (next.length < passwordGateLen(minLen)) return false
  if (!confirm) return false
  if (hasPassword && !current) return false
  if (!hasPassword && needsStepUp && code.length < OTP_LENGTH) return false
  return true
}
