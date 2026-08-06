import { http } from './client'
import type { MeResponse } from './auth'

/**
 * The second-factor surface.
 *
 * Every call here authenticates with the PRE-AUTH cookie (`fx_pa`) rather than
 * a session, except the three that manage an existing enrollment from settings.
 * The cookie is httpOnly and scoped to /api/auth, so nothing in this module
 * passes a token explicitly — the browser attaches it.
 */

export type TwoFactorStatus = {
  enabled: boolean
  recovery_codes_remaining: number
  /** True when the admin policy forbids turning it off. */
  required: boolean
}

/**
 * Submits a code — TOTP, recovery code or mailed OTP.
 *
 * One endpoint for all three because the user is looking at one field and does
 * not think of them as different systems. On success the response is a normal
 * authenticated payload, so the caller adopts it exactly like a login.
 */
export async function verifyTwoFactor(code: string): Promise<MeResponse> {
  const { data } = await http.post<MeResponse>('/api/auth/2fa/verify', { code })
  return data
}

/** Asks the server to mail a one-time code. Resolves even when throttled. */
export async function sendEmailOtp(): Promise<void> {
  await http.post('/api/auth/2fa/email')
}

export type TotpEnrollment = {
  secret: string
  otpauth: string
  issuer: string
  account: string
  qr_url: string
}

/**
 * Begins enrollment.
 *
 * `password` is required when the caller already has a session (adding a factor
 * from settings) and omitted in the mandatory-admin-enrollment flow, where the
 * password was proven moments earlier to obtain the pre-auth challenge.
 */
export async function startTotp(password?: string): Promise<TotpEnrollment> {
  const { data } = await http.post<TotpEnrollment>('/api/auth/2fa/totp/start',
    password ? { password } : {})
  return data
}

/**
 * Confirms enrollment and returns the recovery codes.
 *
 * The codes come back exactly once — the server stores only their sha256 and
 * genuinely cannot reproduce them. In the mandatory-enrollment flow the same
 * response also carries the session, since both factors are now proven.
 */
export async function confirmTotp(code: string): Promise<MeResponse & { recovery_codes: string[] }> {
  const { data } = await http.post<MeResponse & { recovery_codes: string[] }>(
    '/api/auth/2fa/totp/confirm', { code })
  return data
}

export async function fetchTwoFactorStatus(): Promise<TwoFactorStatus> {
  const { data } = await http.get<TwoFactorStatus>('/api/auth/2fa')
  return data
}

/** Removes the second factor. Needs both the password and a current code. */
export async function disableTotp(password: string, code: string): Promise<void> {
  await http.post('/api/auth/2fa/totp/disable', { password, code })
}

export async function regenerateRecoveryCodes(
  password: string,
  code: string,
): Promise<string[]> {
  const { data } = await http.post<{ recovery_codes: string[] }>(
    '/api/auth/2fa/recovery-codes/regenerate', { password, code })
  return data.recovery_codes
}
