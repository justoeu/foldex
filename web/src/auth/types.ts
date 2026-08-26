/**
 * The minimum password length, mirroring the backend's auth.MinPasswordLen.
 *
 * Shared rather than repeated per screen: the value is duplicated across a
 * process boundary already, and a third copy inside the frontend is the one
 * that silently disagrees with the hint shown next to the field.
 */
export const MIN_PASSWORD_LEN = 8

/**
 * The four roles of ADR-33, ordered from most to least privileged.
 *
 * `owner` is not in ASSIGNABLE_ROLES: ownership moves only through the transfer
 * endpoint, which demotes the outgoing owner in the same statement. Offering it
 * in a role dropdown would produce a request the server always refuses.
 */
export type Role = 'owner' | 'admin' | 'editor' | 'viewer'

export const ALL_ROLES: readonly Role[] = ['owner', 'admin', 'editor', 'viewer']
export const ASSIGNABLE_ROLES: readonly Role[] = ['admin', 'editor', 'viewer']

/**
 * Every permission the server's matrix can grant, in the server's display
 * order. Mirrored rather than fetched-only so the matrix renders its columns
 * before the request resolves; the server's list is still what fills the cells.
 */
export type Permission =
  | 'content.read'
  | 'content.write'
  | 'backup.export'
  | 'backup.restore'
  | 'import.run'
  | 'users.read'
  | 'users.write'
  | 'roles.assign'
  | 'invites.read'
  | 'invites.write'
  | 'audit.read'
  | 'policy.read'
  | 'policy.write'
  | 'instance.transfer'
  | 'instance.backup'
  | 'instance.backup_schedule'

/** Mirrors authctx.Role.IsAdmin — who may reach /api/admin at all. */
/**
 * The client mirror of the server's `HasSecondFactor()` — TOTP **or** e-mail.
 *
 * One function because CLAUDE.md §4 makes this a single-answer question and the
 * UI had already drifted: the settings tile read `totp_enabled` alone while the
 * account hero read the OR, so an account whose only factor is e-mail saw
 * "two-factor off" on the tile and "two-factor on" one click later. Reading the
 * authenticator alone is the same mistake that once hid the step-up code field
 * from exactly those accounts.
 */
export function hasSecondFactor(user: Pick<AuthUser, 'totp_enabled' | 'email_2fa_enabled'>): boolean {
  return user.totp_enabled === true || user.email_2fa_enabled === true
}

/**
 * Whether a mailed step-up code is obtainable.
 *
 * Separate from `hasSecondFactor`: an account whose only factor is an
 * authenticator has no mailbox path, and one with only e-mail has no six digits
 * to read from an app. The alternative for the latter is a recovery code, which
 * is a lockout credential — too expensive for a settings change.
 */
export function canMailStepUpCode(user: Pick<AuthUser, 'email_2fa_enabled'>): boolean {
  return user.email_2fa_enabled === true
}

export function isAdminRole(role: Role): boolean {
  return role === 'owner' || role === 'admin'
}

export type AuthUser = {
  id: number
  email: string
  /** The optional second way in. Empty means the account has none and signs in
   *  by address only. Never shown to anyone but its owner. */
  username?: string
  name: string
  role: Role
  status: 'pending' | 'active' | 'disabled'
  has_password: boolean
  totp_enabled: boolean
  /** True when e-mail is enrolled as a second factor (ADR-37). Since that
   *  release a mailed code is a factor the account HOLDS, not one the server
   *  offers whenever SMTP happens to be configured. */
  email_2fa_enabled?: boolean
  /** Preferred language for e-mail. Empty/absent means "follow the browser":
   *  the server then falls back to the Accept-Language of whoever triggered
   *  the message, and only then to English. */
  locale?: string
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
 * would see. `purpose` discriminates presenting a code from mandatory setup;
 * `convert_google` has its own password-entry session status.
 */
type TwoFactorPendingBase = {
  email: string
  methods: string[]
  maxAttempts: number
}

export type TwoFactorPending =
  | (TwoFactorPendingBase & { purpose: 'totp' })
  | (TwoFactorPendingBase & { purpose: 'enroll_2fa' })

export type TwoFactorPurpose = TwoFactorPending['purpose']

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
  /**
   * Came back from Google with an address that matches an existing PASSWORD
   * account. Not a login: the account's current password still has to be
   * proven before the Google identity is attached and the password retired.
   *
   * A separate status rather than a flavour of `two_factor_required` because
   * the screen is different — one password field, not a six-digit code — and
   * because conflating them would put a user on a code screen they can never
   * satisfy.
   */
  | { status: 'convert_password_account'; email: string; features: AuthFeatures }
  | { status: 'authenticated'; user: AuthUser; csrfToken: string; features: AuthFeatures }

export const defaultFeatures: AuthFeatures = {
  google_oauth: false,
  two_factor: false,
  email_delivery: false,
}
