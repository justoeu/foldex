import { describe, expect, it } from 'vitest'
import { canMutateAccount } from './canMutateAccount'
import type { Role } from '../../auth/types'

/**
 * The row-action lock matrix the users page used to re-derive inline.
 *
 * The server still enforces INV-044 (never zero admins) and INV-046 (one
 * owner, moved only by transfer). This charter is the UI mirror: a button the
 * matrix marks closed must never look open.
 */
type Subject = {
  id: number
  role: Role
  status: 'active' | 'disabled'
}

const owner: Subject = { id: 1, role: 'owner', status: 'active' }
const admin: Subject = { id: 2, role: 'admin', status: 'active' }
const editor: Subject = { id: 3, role: 'editor', status: 'active' }
const disabledEditor: Subject = { id: 4, role: 'editor', status: 'disabled' }
const disabledAdmin: Subject = { id: 5, role: 'admin', status: 'disabled' }

type Case = {
  name: string
  u: Subject
  me: Subject
  activeAdmins: number
  disable: boolean
  delete: boolean
  role: boolean
  transfer: boolean
}

const cases: Case[] = [
  {
    name: 'self admin (another admin is active)',
    u: admin, me: admin, activeAdmins: 2,
    disable: false, delete: false, role: false, transfer: false,
  },
  {
    name: 'self last admin',
    u: admin, me: admin, activeAdmins: 1,
    disable: false, delete: false, role: false, transfer: false,
  },
  {
    name: 'self owner',
    u: owner, me: owner, activeAdmins: 2,
    disable: false, delete: false, role: false, transfer: false,
  },
  {
    name: 'self last-admin owner',
    u: owner, me: owner, activeAdmins: 1,
    disable: false, delete: false, role: false, transfer: false,
  },
  {
    name: 'other editor, caller is admin',
    u: editor, me: admin, activeAdmins: 2,
    disable: true, delete: true, role: true, transfer: false,
  },
  {
    name: 'other editor, caller is owner',
    u: editor, me: owner, activeAdmins: 2,
    disable: true, delete: true, role: true, transfer: true,
  },
  {
    name: 'other disabled editor, caller is owner',
    u: disabledEditor, me: owner, activeAdmins: 1,
    disable: true, delete: true, role: true, transfer: false,
  },
  {
    name: 'other last admin (sole active admin, not self)',
    u: admin, me: { id: 9, role: 'admin', status: 'disabled' }, activeAdmins: 1,
    disable: false, delete: false, role: false, transfer: false,
  },
  {
    name: 'other owner, caller is admin',
    u: owner, me: admin, activeAdmins: 2,
    disable: false, delete: false, role: false, transfer: false,
  },
  {
    name: 'other active admin, caller is owner',
    u: admin, me: owner, activeAdmins: 2,
    disable: true, delete: true, role: true, transfer: true,
  },
  {
    name: 'other disabled admin, caller is owner',
    u: disabledAdmin, me: owner, activeAdmins: 1,
    disable: true, delete: true, role: true, transfer: false,
  },
]

describe('AdminUsersRowLockCharter', () => {
  it.each(cases)('$name', ({ u, me, activeAdmins, disable, delete: del, role, transfer }) => {
    expect(canMutateAccount(u, me, activeAdmins)).toEqual({
      disable,
      delete: del,
      role,
      transfer,
    })
  })

  // ASSIGNABLE_ROLES never includes owner: the seat moves only by transfer.
  it('never treats an owner row as role-editable', () => {
    expect(canMutateAccount(owner, admin, 2).role).toBe(false)
    expect(canMutateAccount(owner, owner, 1).role).toBe(false)
  })
})
