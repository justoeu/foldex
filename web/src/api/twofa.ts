import { http } from './client'
import type { AuthenticatedMeResponse } from './auth'

/**
 * The second-factor surface.
 *
 * Every call here authenticates with the PRE-AUTH cookie (`fx_pa`) rather than
 * a session, except the three that manage an existing enrollment from settings.
 * The cookie is httpOnly and scoped to /api/auth, so nothing in this module
 * passes a token explicitly — the browser attaches it.
 */

export type TwoFactorStatus = {
  /** The AGGREGATE: the account has a second factor, by whichever method. */
  enabled: boolean
  totp_enabled: boolean
  email_enabled: boolean
  recovery_codes_remaining: number
  /** True when the admin policy forbids turning 2FA off altogether. */
  required: boolean
  /**
   * Whether each factor may be removed RIGHT NOW. The server answers this
   * because it owns the rule — an admin under a mandatory-2FA policy may drop
   * one factor while the other remains, and deriving that here would put a
   * second copy of the policy in the browser, free to disagree.
   */
  can_disable_totp: boolean
  can_disable_email: boolean
  /** False when the instance has no SMTP: a mailed factor could not arrive. */
  email_available: boolean
}

/** The two ways an account can hold a second factor. */
export type FactorMethod = 'totp' | 'email'

/**
 * Submits a code — TOTP, recovery code or mailed OTP.
 *
 * One endpoint for all three because the user is looking at one field and does
 * not think of them as different systems. On success the response is a normal
 * authenticated payload, so the caller adopts it exactly like a login.
 */
export async function verifyTwoFactor(code: string): Promise<AuthenticatedMeResponse> {
  const { data } = await http.post<AuthenticatedMeResponse>('/api/auth/2fa/verify', { code })
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
export async function confirmTotp(
  code: string,
): Promise<AuthenticatedMeResponse & { recovery_codes: string[] }> {
  const { data } = await http.post<AuthenticatedMeResponse & { recovery_codes: string[] }>(
    '/api/auth/2fa/totp/confirm', { code })
  return data
}

export type EmailFactorEnrollment = {
  account: string
  expires_in: number
  digits: number
}

/**
 * Begins e-mail-factor enrollment and mails the confirming code.
 *
 * Same password rule as startTotp: required from a session, omitted in the
 * mandatory-admin-enrollment flow where the password was proven moments earlier
 * to obtain the pre-auth challenge.
 */
export async function startEmailFactor(password?: string): Promise<EmailFactorEnrollment> {
  const { data } = await http.post<EmailFactorEnrollment>('/api/auth/2fa/email/start',
    password ? { password } : {})
  return data
}

/**
 * Confirms the e-mail factor and returns the recovery codes.
 *
 * The codes matter more here than they do for TOTP. An account whose only
 * factor is e-mail, arriving through a password-reset link, is deliberately
 * refused the e-mail method — otherwise one mailbox would satisfy both steps —
 * so these are its only way back in.
 */
export async function confirmEmailFactor(
  code: string,
): Promise<AuthenticatedMeResponse & { recovery_codes: string[] }> {
  const { data } = await http.post<AuthenticatedMeResponse & { recovery_codes: string[] }>(
    '/api/auth/2fa/email/confirm', { code })
  return data
}

/** Removes the e-mail factor. Needs both the password and a current proof. */
export async function disableEmailFactor(password: string, code: string): Promise<void> {
  await http.post('/api/auth/2fa/email/disable', { password, code })
}

/**
 * Mails a step-up code from the account's enrolled e-mail factor.
 *
 * Without it, an account whose only factor is e-mail would have to spend one of
 * its finite recovery codes to change any security setting — a lockout budget
 * consumed by ordinary use.
 */
export async function sendStepUpCode(): Promise<void> {
  await http.post('/api/auth/2fa/email/send')
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
