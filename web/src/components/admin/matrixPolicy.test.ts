import { describe, expect, it } from 'vitest'
import {
  CELL_BLOCK_I18N,
  cellBlockReason,
  permissionsForWrite,
  sameSet,
  snapshot,
  type CellBlock,
} from './matrixPolicy'
import type { Permission, Role } from '../../auth/types'

type Case = {
  name: string
  canEdit: boolean
  roleEditable: boolean
  permissionLocked: boolean
  callerHolds: boolean
  roleHolds: boolean
  reason: CellBlock | null
}

const cases: Case[] = [
  {
    name: 'readonly caller',
    canEdit: false, roleEditable: true, permissionLocked: false,
    callerHolds: true, roleHolds: false,
    reason: 'readonly',
  },
  {
    name: 'owner column is locked',
    canEdit: true, roleEditable: false, permissionLocked: false,
    callerHolds: true, roleHolds: false,
    reason: 'role_locked',
  },
  {
    name: 'locked permission cannot be offered',
    canEdit: true, roleEditable: true, permissionLocked: true,
    callerHolds: true, roleHolds: false,
    reason: 'permission_locked',
  },
  {
    name: 'granting what the caller does not hold is escalation',
    canEdit: true, roleEditable: true, permissionLocked: false,
    callerHolds: false, roleHolds: false,
    reason: 'escalation',
  },
  {
    name: 'revoking what the caller does not hold is allowed',
    canEdit: true, roleEditable: true, permissionLocked: false,
    callerHolds: false, roleHolds: true,
    reason: null,
  },
  {
    name: 'granting what the caller holds is allowed',
    canEdit: true, roleEditable: true, permissionLocked: false,
    callerHolds: true, roleHolds: false,
    reason: null,
  },
  {
    name: 'role lock beats a locked permission',
    canEdit: true, roleEditable: false, permissionLocked: true,
    callerHolds: true, roleHolds: false,
    reason: 'role_locked',
  },
  {
    name: 'locked permission beats escalation',
    canEdit: true, roleEditable: true, permissionLocked: true,
    callerHolds: false, roleHolds: false,
    reason: 'permission_locked',
  },
  {
    name: 'readonly beats every other reason',
    canEdit: false, roleEditable: false, permissionLocked: true,
    callerHolds: false, roleHolds: false,
    reason: 'readonly',
  },
]

describe('RolesMatrixCellPolicyCharter', () => {
  it.each(cases)('$name', (c) => {
    expect(cellBlockReason({
      canEdit: c.canEdit,
      roleEditable: c.roleEditable,
      permissionLocked: c.permissionLocked,
      callerHolds: c.callerHolds,
      roleHolds: c.roleHolds,
    })).toBe(c.reason)
  })

  // INV-168: a cell that cannot be offered says why — the reason is a real i18n
  // key, never an empty tooltip.
  it('maps every block reason to an i18n key', () => {
    const reasons: CellBlock[] = ['readonly', 'role_locked', 'permission_locked', 'escalation']
    for (const r of reasons) {
      expect(CELL_BLOCK_I18N[r]).toMatch(/^admin\.matrix_why_/)
    }
  })

  // INV-167: owner-level powers (locked on the compiled floor) never travel in
  // the PUT. The draft may contain them; the write strips them.
  it('strips locked permissions so the client cannot grant owner-level powers', () => {
    const locked = new Set<Permission>(['instance.transfer', 'policy.write', 'roles.assign'])
    const after = new Set<Permission>(['content.read', 'instance.transfer', 'users.write'])
    expect(permissionsForWrite(after, locked)).toEqual(['content.read', 'users.write'])
  })

  it('snapshots each role as its own set', () => {
    const rows: { role: Role; permissions: Permission[] }[] = [
      { role: 'editor', permissions: ['content.read', 'content.write'] },
      { role: 'viewer', permissions: ['content.read'] },
    ]
    const snap = snapshot(rows)
    expect([...snap.editor]).toEqual(['content.read', 'content.write'])
    expect(sameSet(snap.editor, new Set(['content.write', 'content.read']))).toBe(true)
    expect(sameSet(snap.editor, snap.viewer)).toBe(false)
  })
})
