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
 * The four states GET /api/auth/me can report.
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
  | { status: 'authenticated'; user: AuthUser; csrfToken: string; features: AuthFeatures }

export const defaultFeatures: AuthFeatures = {
  google_oauth: false,
  two_factor: false,
  email_delivery: false,
}
