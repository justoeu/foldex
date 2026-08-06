/**
 * The minimum password length, mirroring the backend's auth.MinPasswordLen.
 *
 * Shared rather than repeated per screen: the value is duplicated across a
 * process boundary already, and a third copy inside the frontend is the one
 * that silently disagrees with the hint shown next to the field.
 */
export const MIN_PASSWORD_LEN = 8

export type Role = 'admin' | 'user'

export type AuthUser = {
  id: number
  email: string
  name: string
  role: Role
  status: 'pending' | 'active' | 'disabled'
  has_password: boolean
  totp_enabled: boolean
  email_verified_at?: string
  last_login_at?: string
  created_at: string
}

export type AuthFeatures = {
  google_oauth: boolean
  two_factor: boolean
  email_delivery: boolean
}

/**
 * A login that stopped at the second factor.
 *
 * `email` is MASKED by the server. The client never receives the full address
 * here, because this payload is also what a successful credential-stuffing hit
 * would see.
 */
export type TwoFactorPending = {
  /** 'totp' = present a code; 'enroll_2fa' = an admin must set one up first. */
  purpose: 'totp' | 'enroll_2fa'
  email: string
  methods: string[]
  maxAttempts: number
}

/**
 * The states GET /api/auth/me and the credential endpoints can report.
 *
 * `setup_required` is distinct from `anonymous` on purpose: the gate has to
 * decide between the login screen and the first-run setup screen before it can
 * render anything, and inferring that from a separate probe would mean two
 * round-trips on every cold boot.
 */
export type SessionState =
  | { status: 'loading' }
  | { status: 'anonymous'; features: AuthFeatures }
  | { status: 'setup_required'; features: AuthFeatures }
  | { status: 'two_factor_required'; pending: TwoFactorPending; features: AuthFeatures }
  | { status: 'authenticated'; user: AuthUser; csrfToken: string; features: AuthFeatures }

export const defaultFeatures: AuthFeatures = {
  google_oauth: false,
  two_factor: false,
  email_delivery: false,
}
