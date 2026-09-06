import { OTP_LENGTH } from './auth/OtpInput'
import type { FactorMethod } from '../api/twofa'

/** Below this, the recovery band turns amber and says so. */
export const LOW_RECOVERY_CODES = 3

export type MethodId = FactorMethod | 'recovery'

export type MethodKind = 'enable' | 'disable' | 'lock' | 'unavailable' | 'regenerate' | 'hidden'

/**
 * One second-factor row, table-driven from the server's flags.
 *
 * `canDisable` is the server's `can_disable_*` — INV-138 forbids re-deriving
 * the admin policy here. `available` is false only for e-mail on an instance
 * whose mail driver cannot deliver.
 */
export type MethodSnapshot = {
  id: MethodId
  enabled: boolean
  canDisable: boolean
  available: boolean
}

export function methodKind(method: MethodSnapshot): MethodKind {
  if (method.id === 'recovery') return method.enabled ? 'regenerate' : 'hidden'
  if (!method.enabled) return method.available ? 'enable' : 'unavailable'
  return method.canDisable ? 'disable' : 'lock'
}

export function twoFactorMethods(flags: {
  totpEnabled: boolean
  canDisableTotp: boolean
  emailEnabled: boolean
  canDisableEmail: boolean
  emailAvailable: boolean
  twoFactorEnabled: boolean
}): MethodSnapshot[] {
  return [
    { id: 'totp', enabled: flags.totpEnabled, canDisable: flags.canDisableTotp, available: true },
    {
      id: 'email',
      enabled: flags.emailEnabled,
      canDisable: flags.canDisableEmail,
      available: flags.emailAvailable,
    },
    {
      id: 'recovery',
      enabled: flags.twoFactorEnabled,
      canDisable: true,
      available: flags.twoFactorEnabled,
    },
  ]
}

/**
 * The step-up the destructive actions require: the same two proofs that
 * turned the factor on. Shared because the method rows and the recovery band
 * gate on the same rule — copied into both, they drift.
 */
export function proofMissing(password: string, code: string): boolean {
  return !password || code.length < OTP_LENGTH
}

export function methodActionDisabled(
  kind: MethodKind,
  password: string,
  code: string,
  busy: boolean,
): boolean {
  if (busy) return true
  if (kind === 'enable') return !password
  if (kind === 'disable' || kind === 'regenerate') return proofMissing(password, code)
  return true
}
