import { isAdminRole, type AuthUser } from '../../auth/types'

export type AccountMutations = {
  disable: boolean
  delete: boolean
  role: boolean
  transfer: boolean
}

type Account = Pick<AuthUser, 'id' | 'role' | 'status'>
type Actor = Pick<AuthUser, 'id' | 'role'> | null

/**
 * Which ordinary edits the users table may offer on one row.
 *
 * The server still refuses the same cases inside a transaction (INV-044, INV-046).
 * This is the affordance so a control never looks open and then 409s: you cannot
 * demote, disable or delete yourself, the last active administrator, or the
 * owner. Transfer is a different door — owner-only, never onto yourself, never
 * onto a disabled account — because the seat moves only that way.
 */
export function canMutateAccount(u: Account, me: Actor, activeAdmins: number): AccountMutations {
  const isSelf = me?.id === u.id
  const isLastAdmin = isAdminRole(u.role) && u.status === 'active' && activeAdmins <= 1
  const isOwner = u.role === 'owner'
  const locked = isSelf || isLastAdmin || isOwner
  return {
    disable: !locked,
    delete: !locked,
    role: !locked,
    transfer: me?.role === 'owner' && !isSelf && u.status === 'active',
  }
}

/** Mirrors guardLastAdminTx: owner counts, or an instance whose only admin is the owner looks empty. */
export function countActiveAdmins(users: Pick<AuthUser, 'role' | 'status'>[]): number {
  return users.filter((u) => isAdminRole(u.role) && u.status === 'active').length
}
