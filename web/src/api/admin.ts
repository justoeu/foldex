import { http } from './client'
import type { AvailabilityResponse } from '../hooks/useAvailability'
import type { AuthUser, Permission, Role } from '../auth/types'

/**
 * The /api/admin surface.
 *
 * Two things about it that the UI mirrors but must never be relied on: a
 * non-admin gets 404 (not 403) from every route here, and the guards that keep
 * the instance from reaching zero administrators live on the server, inside a
 * transaction. The client-side disabling below is affordance, not enforcement.
 */

export type Invite = {
  id: number
  email: string
  role: Role
  created_at: string
  expires_at: string
  accepted_at?: string
  /** Present only in the create response — the raw token is unrecoverable after. */
  accept_url?: string
}

export async function listUsers(): Promise<AuthUser[]> {
  const { data } = await http.get<{ users: AuthUser[] }>('/api/admin/users')
  return data.users
}

export async function updateUser(
  id: number,
  patch: { name?: string; role?: Role; status?: 'active' | 'disabled' },
): Promise<AuthUser> {
  const { data } = await http.patch<AuthUser>(`/api/admin/users/${id}`, patch)
  return data
}

/**
 * Creates an account with a password the ADMINISTRATOR chose.
 *
 * A deliberate exception to the rule the rest of the auth surface enforces —
 * CLAUDE.md §4, "an administrator never chooses, installs or receives another
 * user's credential" — taken by the instance owner with the trade stated. The
 * invitation flow and `sendPasswordRecovery` both avoid the window where two
 * people know one password, and remain the recommended path.
 */
export async function createUser(input: {
  email: string
  name: string
  password: string
  role: Role
}): Promise<AuthUser> {
  const { data } = await http.post('/api/admin/users', input)
  return data as AuthUser
}

export async function deleteUser(id: number): Promise<void> {
  await http.delete(`/api/admin/users/${id}`)
}

export async function revokeUserSessions(id: number): Promise<void> {
  await http.post(`/api/admin/users/${id}/sessions/revoke`)
}

/** Sends a recovery link to the target's verified mailbox. No secret returns. */
export async function sendPasswordRecovery(id: number): Promise<void> {
  await http.post(`/api/admin/users/${id}/force-password-reset`)
}

export async function listInvites(): Promise<Invite[]> {
  const { data } = await http.get<{ invites: Invite[] }>('/api/admin/invites')
  return data.invites
}

export async function createInvite(email: string, role: Role): Promise<Invite> {
  const { data } = await http.post<Invite>('/api/admin/invites', { email, role })
  return data
}

export async function revokeInvite(id: number): Promise<void> {
  await http.delete(`/api/admin/invites/${id}`)
}

/** The administration header's four tiles. Every field is derived server-side. */
export type InstanceMetrics = {
  active_users: number
  active_users_added_30d: number
  pending_invites: number
  /** Hours until the soonest invite expires, or null when none is pending. */
  next_invite_expiry_hours: number | null
  roles_in_use: number
  permission_count: number
  two_factor_percent: number
}

export type RoleSummary = {
  role: Role
  permissions: Permission[]
  user_count: number
  /**
   * Whether this role's grants may be configured at all. Read from the server
   * rather than re-derived as `role !== 'owner'`, for §5's usual reason: two
   * copies of one policy drift, and the direction nobody notices is a screen
   * offering a save the server refuses.
   */
  editable: boolean
}

/** What GET /api/admin/roles answers. */
export type RolesResponse = {
  roles: RoleSummary[]
  /** The full ordered vocabulary — the matrix's rows. */
  permissions: Permission[]
  /** Entries no configuration may add or remove, in either direction. */
  locked: Permission[]
  /** The caller's own role, which bounds what they may grant. */
  caller_role: Role
  /** Whether the caller may write the matrix at all. */
  can_edit: boolean
  /** True on an instance serving the compiled matrix with no store behind it. */
  editable_disabled: boolean
}

export type AuditEntry = {
  id: number
  action: string
  actor_email: string | null
  target_email: string | null
  detail: string | null
  created_at: string
}

/** Which factors satisfy AUTH_REQUIRE_2FA_FOR_ADMINS (ADR-37). */
export type AdminFactorMode = 'any' | 'totp_only'

export type InstancePolicy = {
  admin_second_factor: AdminFactorMode
  password_min_length: number
  otp_ttl_minutes: number
  otp_cooldown_seconds: number
  google_allowed_domains: string[]
  google_auto_provision: boolean
  google_default_role: Role
}

export async function fetchMetrics(): Promise<InstanceMetrics> {
  const { data } = await http.get<InstanceMetrics>('/api/admin/metrics')
  return data
}

/**
 * The RBAC matrix as the SERVER enforces it, plus how many accounts hold each
 * role. `permissions` is the full ordered vocabulary — rendering the columns
 * from it rather than from a local list is what stops the screen from
 * describing a grid the server does not implement.
 */
export async function fetchRoles(): Promise<RolesResponse> {
  const { data } = await http.get<RolesResponse>('/api/admin/roles')
  return data
}

/**
 * Replaces one role's configurable grants (ADR-42).
 *
 * The full set is sent, not a delta: absent means revoked, which is the only
 * encoding where two administrators editing at once cannot silently merge
 * their intents into a role neither of them chose.
 */
export async function setRolePermissions(
  role: Role,
  permissions: Permission[],
): Promise<{ roles: RoleSummary[] }> {
  const { data } = await http.put<{ roles: RoleSummary[] }>(
    `/api/admin/roles/${encodeURIComponent(role)}/permissions`,
    { permissions },
  )
  return data
}

/** Keyset pagination: `before` is the last id already shown, not an offset. */
/**
 * Query key for one page of the audit trail. Shared so the administration
 * overview's attention feed and the audit section's unfiltered first page
 * resolve to the SAME cache entry — the feed ends in a button that opens
 * exactly that view, and two spellings of the same page would refetch it one
 * click later. `before` is the keyset cursor; 0 stands for "the head".
 */
export function auditQueryKey(action = '', before = 0) {
  return ['admin', 'audit', action, before] as const
}

export async function fetchAudit(params: { action?: string; before?: number } = {}): Promise<AuditEntry[]> {
  const { data } = await http.get<{ entries: AuditEntry[] }>('/api/admin/audit', { params })
  return data.entries
}

export async function fetchPolicy(): Promise<InstancePolicy> {
  const { data } = await http.get<InstancePolicy>('/api/admin/policy')
  return data
}

/** Owner-only. An admin gets 403 `forbidden_role`. */
export async function savePolicy(p: InstancePolicy): Promise<InstancePolicy> {
  const { data } = await http.put<InstancePolicy>('/api/admin/policy', p)
  return data
}

/**
 * Hands the instance to another active account and demotes the caller to admin.
 *
 * The outgoing owner is always the CALLER — the server takes no `from`. Both
 * accounts lose every session, so the caller is signed out by this call.
 */
export async function transferOwnership(id: number): Promise<AuthUser> {
  const { data } = await http.post<AuthUser>(`/api/admin/users/${id}/transfer-ownership`)
  return data
}

/** Asks whether an address is free, for the administrator creating an account.
 *
 *  This lives under /api/admin and must never be mirrored anywhere an ordinary
 *  session can reach: past the admin gate the caller can already list every
 *  account with its address, which is the whole reason the probe is allowed to
 *  answer about e-mail at all. `reason: "pending"` rides an AVAILABLE answer —
 *  somebody is moving to this address but has not confirmed, and the create
 *  would still succeed. */
// ── Operational backup status (ADR-43) ─────────────────────────────────

export type BackupJob = 'dump' | 'drill' | 'mirror' | 'user_zip'

export type BackupRunStatus = 'requested' | 'running' | 'succeeded' | 'failed'

/**
 * One backup_run row. `last_error` is a normalized reason token
 * (`upload_failed`, `drill_counts_mismatch`, …), never raw tool output — the
 * UI renders it as code and must not translate it: it is the exact string an
 * operator greps the agent's logs and the runbook for.
 */
export type BackupRun = {
  id: number
  job: BackupJob
  status: BackupRunStatus
  scheduled_for: string
  started_at: string
  finished_at: string | null
  artifact_key: string | null
  artifact_bytes: number | null
  artifact_sha256: string | null
  objects_scanned: number | null
  objects_copied: number | null
  bytes_copied: number | null
  drill_of_run_id: number | null
  last_error: string | null
  meta: Record<string, unknown>
}

export type BackupJobStatus = {
  job: BackupJob
  /** The drill's entry carries drill_of_run_id and the restored counts in meta
   *  — that row IS the "last drill" highlight. */
  last_success: BackupRun | null
  /** Excludes operational outcomes (shutdown, lock_busy, …) — the same number
   *  the agent's alert threshold compares against. */
  consecutive_failures: number
}

export type BackupStatusResponse = {
  jobs: BackupJobStatus[]
  runs: BackupRun[]
}

/** Query key for one history page. `before` is the keyset cursor; 0 = head. */
export function backupStatusQueryKey(before = 0) {
  return ['admin', 'backup', before] as const
}

export async function fetchBackupStatus(
  params: { before?: number } = {},
): Promise<BackupStatusResponse> {
  const { data } = await http.get<BackupStatusResponse>('/api/admin/backup/runs', { params })
  return data
}

/**
 * Enqueues a manual run. Only ever a 'requested' row the agent claims on its
 * next poll — the web process never executes a backup and never holds the S3
 * credentials. 409 `backup_run_pending` while one is already queued/running.
 */
export async function requestBackupRun(
  job: BackupJob,
): Promise<{ id: number; job: BackupJob; status: 'requested' }> {
  const { data } = await http.post<{ id: number; job: BackupJob; status: 'requested' }>(
    '/api/admin/backup/run',
    { job },
  )
  return data
}

export async function emailAvailable(
  email: string,
  signal: AbortSignal,
): Promise<AvailabilityResponse> {
  const { data } = await http.get<AvailabilityResponse>('/api/admin/users/email-available', {
    params: { email },
    signal,
  })
  return data
}
