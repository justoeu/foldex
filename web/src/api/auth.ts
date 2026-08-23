import { http } from './client'
import type { AuthFeatures, AuthUser, Role } from '../auth/types'

export type AnonymousMeResponse = {
  status: 'anonymous'
  features: AuthFeatures
}

export type SetupRequiredMeResponse = {
  status: 'setup_required'
  features: AuthFeatures
}

export type AuthenticatedMeResponse = {
  status: 'authenticated'
  user: AuthUser
  csrf_token: string
  features: AuthFeatures
}

type TwoFactorRequiredMeResponseBase = {
  status: 'two_factor_required'
  /** Server-masked address; the full account address is never exposed here. */
  email: string
  methods: string[]
  expires_in: number
  max_attempts: number
  features: AuthFeatures
}

export type TwoFactorRequiredMeResponse =
  | (TwoFactorRequiredMeResponseBase & { purpose: 'totp' })
  | (TwoFactorRequiredMeResponseBase & { purpose: 'enroll_2fa'; reason: string })

export type ConvertPasswordAccountMeResponse = {
  status: 'convert_password_account'
  purpose: 'convert_google'
  email: string
  methods: string[]
  expires_in: number
  max_attempts: number
  features: AuthFeatures
}

export type MeResponse =
  | AnonymousMeResponse
  | SetupRequiredMeResponse
  | AuthenticatedMeResponse
  | TwoFactorRequiredMeResponse
  | ConvertPasswordAccountMeResponse

export async function fetchMe(): Promise<MeResponse> {
  const { data } = await http.get<MeResponse>('/api/auth/me')
  return data
}

/**
 * Signs in with an e-mail OR a username.
 *
 * The field is `identifier` because the server resolves one string against both
 * columns in a single statement — branching on whether it "looks like an
 * e-mail" would take two different amounts of time, which is the enumeration
 * oracle the whole login path is built to close.
 */
export async function login(identifier: string, password: string): Promise<MeResponse> {
  const { data } = await http.post<MeResponse>('/api/auth/login', { identifier, password })
  return data
}

export async function bootstrap(email: string, name: string, password: string): Promise<MeResponse> {
  const { data } = await http.post<MeResponse>('/api/auth/bootstrap', { email, name, password })
  return data
}

export async function logout(): Promise<void> {
  await http.post('/api/auth/logout')
}

/**
 * Renames the signed-in account. The only self-service profile field — e-mail
 * is identity and role/status are administration. Answers with the same
 * payload shape /me uses, so callers can adopt the refreshed user directly.
 */
/** Updates the two fields an account controls about itself.
 *
 *  `locale` is tri-state, matching the server: omitted keeps the stored
 *  preference, `''` clears it back to following the browser, and a code sets
 *  it. Without the empty case a user who once picked a language could never
 *  go back. */
export async function updateProfile(
  name: string,
  locale?: string,
  username?: string,
): Promise<MeResponse> {
  // Every field is tri-state server-side: absent keeps, "" clears, a value
  // sets. Sending one that did not change would replay a cached value over
  // whatever another tab wrote since this screen loaded.
  const body: { name: string; locale?: string; username?: string } = { name }
  if (locale !== undefined) body.locale = locale
  if (username !== undefined) body.username = username
  const { data } = await http.patch<MeResponse>('/api/auth/profile', body)
  return data
}

export type PendingEmailChange = { new_email: string; expires_at: string }

/** Starts a move to a new address. Nothing changes until the link mailed to it
 *  is opened — see the backend's RequestEmailChange for why. */
export async function requestEmailChange(
  newEmail: string,
  password: string,
): Promise<PendingEmailChange> {
  const { data } = await http.post<PendingEmailChange>('/api/auth/email/change', {
    new_email: newEmail,
    password,
  })
  return data
}

export async function fetchEmailChange(): Promise<PendingEmailChange | null> {
  const { data } = await http.get<{ pending: PendingEmailChange | null }>('/api/auth/email/change')
  return data.pending
}

export async function cancelEmailChange(): Promise<void> {
  await http.delete('/api/auth/email/change')
}

/** Consumes the confirmation token. Unauthenticated: the link is opened from
 *  the mailbox being moved to, often on a device that never signed in. */
export async function confirmEmailChange(token: string): Promise<void> {
  await http.post('/api/auth/email-change/confirm', { token })
}

/** Sets ONLY the account language, sending no name.
 *
 *  Both fields are tri-state on the server, and this exists so a language
 *  change stops replaying a name the caller merely had in cache. Sending it
 *  reverted a rename made in another tab — a hazard that stopped being
 *  theoretical once the SPA began adopting a locale on mount rather than on a
 *  deliberate click. */
export async function updateLocale(locale: string): Promise<MeResponse> {
  const { data } = await http.patch<MeResponse>('/api/auth/profile', { locale })
  return data
}

/**
 * Requests a password-reset link.
 *
 * Resolves for every input, including an unknown address — the backend answers
 * 202 unconditionally so the endpoint cannot be used to enumerate accounts, and
 * the UI must not undo that by branching on anything it gets back.
 */
export async function forgotPassword(email: string, locale?: string): Promise<void> {
  // `locale` is the language this screen is SHOWING, not a preference to store.
  // The server ranks it below the recipient's own stored preference and above
  // Accept-Language — which is a separate browser setting almost nobody
  // configures, and which is why a Portuguese screen used to mail an English
  // reset link.
  await http.post('/api/auth/password/forgot', locale ? { email, locale } : { email })
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

export type InvitePreview = { email: string; role: Role; expires_at: string }

export async function lookupInvite(token: string, signal?: AbortSignal): Promise<InvitePreview> {
  const { data } = await http.post<InvitePreview>('/api/auth/invites/lookup', { token }, { signal })
  return data
}

export async function acceptInvite(
  token: string,
  name: string,
  password: string,
  signal?: AbortSignal,
): Promise<MeResponse> {
  const { data } = await http.post<MeResponse>(
    '/api/auth/invites/accept',
    { token, name, password },
    { signal },
  )
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

/**
 * Changes the password of an account that already has one.
 *
 * Distinct from `setPassword`, and the difference is the proof: here the
 * CURRENT password is the step-up, so no second factor is asked for. The server
 * revokes every other session on success — this session survives, which is why
 * the screen does not have to sign anyone out.
 *
 * The endpoint has existed since ADR-30 and nothing called it: changing a
 * password meant signing out and using the reset-by-e-mail flow.
 */
export async function changePassword(
  currentPassword: string,
  newPassword: string,
): Promise<void> {
  await http.post('/api/auth/password/change', {
    current_password: currentPassword,
    new_password: newPassword,
  })
}

// ─────────────────────────────────────────────────────────────────────
// Google
// ─────────────────────────────────────────────────────────────────────

export type OAuthPurpose = 'login' | 'accept_invite'

/**
 * Builds the URL that starts the flow.
 *
 * Invitation OAuth uses a body POST instead; this function deliberately returns
 * only the endpoint path for that purpose and never serializes its token.
 */
export function googleStartUrl(purpose: OAuthPurpose, invite?: string): string {
  if (purpose === 'accept_invite') return '/api/auth/oauth/google/invite/start'
  const params = new URLSearchParams({ purpose })
  return `/api/auth/oauth/google/start?${params}`
}

/**
 * Leaves the SPA for Google's consent screen.
 *
 * A full-page navigation, not fetch: the flow ends in a redirect back to this
 * origin carrying cookies the server sets, and an XHR could neither follow the
 * cross-origin hop nor let the user see what they are consenting to.
 */
export async function startGoogleOAuth(purpose: OAuthPurpose, invite?: string): Promise<void> {
  if (purpose === 'accept_invite') {
    const { data } = await http.post<{ redirect_url: string }>(googleStartUrl(purpose), {
      invite: invite ?? '',
    })
    navigateToOAuth(data.redirect_url)
    return
  }
  navigateToOAuth(googleStartUrl(purpose))
}

export function navigateToOAuth(url: string): void {
  window.location.assign(url)
}

/** Proves the current credentials and returns Google's server-built URL. */
export async function beginGoogleLink(currentPassword: string, code = ''): Promise<string> {
  const { data } = await http.post<{ redirect_url: string }>('/api/auth/oauth/google/start', {
    current_password: currentPassword,
    code,
  })
  return data.redirect_url
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
