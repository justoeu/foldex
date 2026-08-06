import { http } from './client'
import type { AuthUser, Role } from '../auth/types'

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

/**
 * Gives an account a fresh password and returns it ONCE.
 *
 * This is the lockout exit for a user who converted to Google-only and then
 * lost the Google account: nothing else can put a password back, because
 * /password/forgot deliberately refuses to mail a reset link to an account with
 * no password. The administrator is the out-of-band channel.
 */
export async function forcePasswordReset(id: number): Promise<string> {
  const { data } = await http.post<{ temporary_password: string }>(
    `/api/admin/users/${id}/force-password-reset`,
  )
  return data.temporary_password
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
