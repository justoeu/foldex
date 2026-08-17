import { http } from './client'
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
}

export type AuditEntry = {
  id: number
  action: string
  actor_email: string | null
  target_email: string | null
  detail: string | null
  created_at: string
}

export type InstancePolicy = {
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
export async function fetchRoles(): Promise<{ roles: RoleSummary[]; permissions: Permission[] }> {
  const { data } = await http.get<{ roles: RoleSummary[]; permissions: Permission[] }>('/api/admin/roles')
  return data
}

/** Keyset pagination: `before` is the last id already shown, not an offset. */
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
