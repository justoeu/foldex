import type { Permission, Role } from '../../auth/types'

/**
 * Why a matrix cell cannot be toggled — or null when it can.
 *
 * Everything about what may be edited comes from the SERVER (`editable`,
 * `locked`, `can_edit`). Re-deriving any of it here would be two copies of one
 * authorization policy. The reasons exist so a disabled cell says why (INV-168)
 * instead of looking broken.
 */
export type CellBlock = 'readonly' | 'role_locked' | 'permission_locked' | 'escalation'

export const CELL_BLOCK_I18N: Record<CellBlock, string> = {
  readonly: 'admin.matrix_why_readonly',
  role_locked: 'admin.matrix_why_role_locked',
  permission_locked: 'admin.matrix_why_permission_locked',
  escalation: 'admin.matrix_why_escalation',
}

export function cellBlockReason(input: {
  canEdit: boolean
  roleEditable: boolean
  permissionLocked: boolean
  callerHolds: boolean
  roleHolds: boolean
}): CellBlock | null {
  if (!input.canEdit) return 'readonly'
  if (!input.roleEditable) return 'role_locked'
  if (input.permissionLocked) return 'permission_locked'
  // Only GRANTING is bounded by what the caller holds; revoking is not, or an
  // admin could never undo a grant an owner made.
  if (!input.callerHolds && !input.roleHolds) return 'escalation'
  return null
}

/** Permissions in the groups the server documents them in. Unknown ones still render, under `other`. */
const GROUPS: { key: string; members: Permission[] }[] = [
  { key: 'content', members: ['content.read', 'content.write'] },
  { key: 'transfer', members: ['backup.export', 'backup.restore', 'import.run'] },
  { key: 'people', members: ['users.read', 'users.write', 'roles.assign', 'invites.read', 'invites.write'] },
  { key: 'instance', members: ['audit.read', 'policy.read', 'policy.write', 'instance.transfer'] },
]

export function orderPermissionGroups(all: Permission[]): { key: string; members: Permission[] }[] {
  const known = new Set(GROUPS.flatMap((g) => g.members))
  const rest = all.filter((p) => !known.has(p))
  const groups = GROUPS.map((g) => ({ key: g.key, members: g.members.filter((p) => all.includes(p)) }))
    .filter((g) => g.members.length > 0)
  return rest.length > 0 ? [...groups, { key: 'other', members: rest }] : groups
}

/** The server's answer as editable state. */
export function snapshot(rows: { role: Role; permissions: Permission[] }[]): Record<Role, Set<Permission>> {
  const out = {} as Record<Role, Set<Permission>>
  for (const r of rows) out[r.role] = new Set(r.permissions)
  return out
}

export function sameSet(a: Set<Permission>, b: Set<Permission>): boolean {
  return a.size === b.size && [...a].every((x) => b.has(x))
}

/**
 * Locked entries are stripped because Resolve puts them back from the compiled
 * matrix (INV-167). Storing them would create a second source of truth, and
 * sending `instance.transfer` would be the client granting an owner-level power.
 */
export function permissionsForWrite(after: Set<Permission>, locked: Set<Permission>): Permission[] {
  return [...after].filter((p) => !locked.has(p))
}

export function matrixDirty(
  rows: { role: Role; permissions: Permission[] }[],
  draft: Record<Role, Set<Permission>> | null,
): boolean {
  if (draft === null) return false
  return rows.some((r) => !sameSet(new Set(r.permissions), draft[r.role]))
}

export function saveErrorI18n(code: string): string {
  switch (code) {
    case 'role_not_editable':
      return 'admin.matrix_why_role_locked'
    case 'permission_locked':
      return 'admin.matrix_why_permission_locked'
    case 'permission_escalation':
      return 'admin.matrix_why_escalation'
    case 'roles_not_configurable':
      return 'admin.matrix_compiled'
    default:
      return 'auth_errors.generic'
  }
}
