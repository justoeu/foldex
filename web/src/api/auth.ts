import { http } from './client'
import type { AuthFeatures, AuthUser, Role } from '../auth/types'

export type MeResponse = {
  status:
    | 'anonymous'
    | 'setup_required'
    | 'authenticated'
    | 'two_factor_required'
    | 'convert_password_account'
  user?: AuthUser
  csrf_token?: string
  features: AuthFeatures
  // Present on `two_factor_required` and `convert_password_account`. The e-mail
  // is masked by the server.
  purpose?: 'totp' | 'enroll_2fa' | 'convert_google'
  email?: string
  methods?: string[]
  max_attempts?: number
}

export async function fetchMe(): Promise<MeResponse> {
  const { data } = await http.get<MeResponse>('/api/auth/me')
  return data
}

export async function fetchBootstrapStatus(): Promise<boolean> {
  const { data } = await http.get<{ needs_bootstrap: boolean }>('/api/auth/bootstrap-status')
  return data.needs_bootstrap
}

export async function login(email: string, password: string): Promise<MeResponse> {
  const { data } = await http.post<MeResponse>('/api/auth/login', { email, password })
  return data
}

export async function bootstrap(email: string, name: string, password: string): Promise<MeResponse> {
  const { data } = await http.post<MeResponse>('/api/auth/bootstrap', { email, name, password })
  return data
}

export async function logout(): Promise<void> {
  await http.post('/api/auth/logout')
}

export async function logoutAll(): Promise<void> {
  await http.post('/api/auth/logout-all')
}

export async function changePassword(currentPassword: string, newPassword: string): Promise<void> {
  await http.post('/api/auth/password/change', {
    current_password: currentPassword,
    new_password: newPassword,
  })
}

/**
 * Requests a password-reset link.
 *
 * Resolves for every input, including an unknown address — the backend answers
 * 202 unconditionally so the endpoint cannot be used to enumerate accounts, and
 * the UI must not undo that by branching on anything it gets back.
 */
export async function forgotPassword(email: string): Promise<void> {
  await http.post('/api/auth/password/forgot', { email })
}

export async function resetPassword(token: string, password: string): Promise<MeResponse> {
  const { data } = await http.post<MeResponse>('/api/auth/password/reset', { token, password })
  return data
}

/**
 * Confirms an address from a mailed link.
 *
 * Unauthenticated: the 256-bit token IS the credential, which is what lets the
 * link work on a device that has never signed in.
 */
export async function verifyEmail(token: string): Promise<void> {
  await http.post('/api/auth/email/verify', { token })
}

/** Mails a fresh confirmation link to the signed-in user's own address. */
export async function resendEmailVerification(): Promise<void> {
  await http.post('/api/auth/email/resend')
}

export type InvitePreview = { email: string; role: Role; expires_at: string }

export async function lookupInvite(token: string): Promise<InvitePreview> {
  const { data } = await http.get<InvitePreview>(`/api/auth/invites/${encodeURIComponent(token)}`)
  return data
}

export async function acceptInvite(token: string, name: string, password: string): Promise<MeResponse> {
  const { data } = await http.post<MeResponse>('/api/auth/invites/accept', { token, name, password })
  return data
}

/**
 * Adds a password to an account that has none — the Google-only escape hatch.
 *
 * `code` is required when the account has an authenticator: there is no current
 * password to prove, so the second factor is the only step-up available.
 */
export async function setPassword(password: string, code?: string): Promise<void> {
  await http.post('/api/auth/password/set', { password, code: code ?? '' })
}

// ─────────────────────────────────────────────────────────────────────
// Google
// ─────────────────────────────────────────────────────────────────────

export type OAuthPurpose = 'login' | 'link' | 'accept_invite'

/**
 * Builds the URL that starts the flow.
 *
 * Split from the navigation below so the part with actual logic — query
 * encoding of an invitation token, which is a credential and may contain
 * characters that would otherwise terminate the query string — is testable
 * without stubbing `window.location`, which jsdom refuses to let anyone
 * redefine.
 */
export function googleStartUrl(purpose: OAuthPurpose, invite?: string): string {
  const params = new URLSearchParams({ purpose })
  if (invite) params.set('invite', invite)
  return `/api/auth/oauth/google/start?${params}`
}

/**
 * Leaves the SPA for Google's consent screen.
 *
 * A full-page navigation, not fetch: the flow ends in a redirect back to this
 * origin carrying cookies the server sets, and an XHR could neither follow the
 * cross-origin hop nor let the user see what they are consenting to.
 */
export function startGoogleOAuth(purpose: OAuthPurpose, invite?: string): void {
  window.location.assign(googleStartUrl(purpose, invite))
}

/** Confirms the current password, attaching Google and retiring the password. */
export async function convertToGoogle(password: string): Promise<MeResponse> {
  const { data } = await http.post<MeResponse>('/api/auth/oauth/google/convert', { password })
  return data
}

export type Identity = {
  provider: string
  email_at_link?: string
  created_at: string
  last_login_at?: string
}

export async function listIdentities(): Promise<Identity[]> {
  const { data } = await http.get<{ identities: Identity[] }>('/api/auth/identities')
  return data.identities
}

/** Detaches Google. Refused when it would leave the account with no way in. */
export async function unlinkGoogle(password: string): Promise<void> {
  await http.delete('/api/auth/oauth/google', { data: { password } })
}

export type SessionRow = {
  id: number
  created_at: string
  last_seen_at: string
  user_agent?: string
  ip?: string
  current: boolean
}

export async function listSessions(): Promise<SessionRow[]> {
  const { data } = await http.get<{ sessions: SessionRow[] }>('/api/auth/sessions')
  return data.sessions
}

export async function revokeSession(id: number): Promise<void> {
  await http.delete(`/api/auth/sessions/${id}`)
}

/**
 * Extracts the machine-readable code from the backend's error envelope.
 *
 * Screens branch on the CODE, never on the human-readable message: the message
 * is translated and reworded freely, while the code is part of the API
 * contract. Matching on message text is how error handling silently breaks the
 * first time someone improves the copy.
 */
export function errorCode(err: unknown): string {
  const e = err as { response?: { data?: { error?: { code?: string } } } }
  return e?.response?.data?.error?.code ?? ''
}

export function errorStatus(err: unknown): number {
  const e = err as { response?: { status?: number } }
  return e?.response?.status ?? 0
}
