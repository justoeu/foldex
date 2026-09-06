import { describe, expect, it } from 'vitest'
import {
  LOW_RECOVERY_CODES,
  methodActionDisabled,
  methodKind,
  proofMissing,
  twoFactorMethods,
  type MethodKind,
  type MethodSnapshot,
} from './TwoFactorSection.methods'

/**
 * CC-DAE-005 — MethodList is a table of {id, enabled, canDisable, available}
 * rendered as one MethodRow each. The kind is derived from those flags, never
 * from a second copy of the admin policy (INV-138).
 */
describe('TwoFactorMethodListCharter', () => {
  const cases: Array<{
    name: string
    method: MethodSnapshot
    kind: MethodKind
  }> = [
    {
      name: 'totp × enable',
      method: { id: 'totp', enabled: false, canDisable: false, available: true },
      kind: 'enable',
    },
    {
      name: 'totp × disable',
      method: { id: 'totp', enabled: true, canDisable: true, available: true },
      kind: 'disable',
    },
    {
      name: 'totp × lock',
      method: { id: 'totp', enabled: true, canDisable: false, available: true },
      kind: 'lock',
    },
    {
      name: 'email × enable',
      method: { id: 'email', enabled: false, canDisable: false, available: true },
      kind: 'enable',
    },
    {
      name: 'email × disable',
      method: { id: 'email', enabled: true, canDisable: true, available: true },
      kind: 'disable',
    },
    {
      name: 'email × lock',
      method: { id: 'email', enabled: true, canDisable: false, available: true },
      kind: 'lock',
    },
    {
      name: 'email × unavailable',
      method: { id: 'email', enabled: false, canDisable: false, available: false },
      kind: 'unavailable',
    },
    {
      name: 'recovery × regenerate',
      method: { id: 'recovery', enabled: true, canDisable: true, available: true },
      kind: 'regenerate',
    },
    {
      name: 'recovery × hidden while 2FA is off',
      method: { id: 'recovery', enabled: false, canDisable: false, available: false },
      kind: 'hidden',
    },
  ]

  it.each(cases)('$name', ({ method, kind }) => {
    expect(methodKind(method)).toBe(kind)
  })

  it('builds totp, email and recovery from the server flags, never from a local policy', () => {
    const rows = twoFactorMethods({
      totpEnabled: true,
      canDisableTotp: false,
      emailEnabled: false,
      canDisableEmail: false,
      emailAvailable: false,
      twoFactorEnabled: true,
    })
    expect(rows.map((row) => ({ id: row.id, kind: methodKind(row) }))).toEqual([
      { id: 'totp', kind: 'lock' },
      { id: 'email', kind: 'unavailable' },
      { id: 'recovery', kind: 'regenerate' },
    ])
  })

  it('keeps proofMissing as the shared gate for disable and regenerate', () => {
    expect(proofMissing('', '123456')).toBe(true)
    expect(proofMissing('hunter2hunter2', '')).toBe(true)
    expect(proofMissing('hunter2hunter2', '12345')).toBe(true)
    expect(proofMissing('hunter2hunter2', '123456')).toBe(false)

    expect(methodActionDisabled('enable', '', '123456', false)).toBe(true)
    expect(methodActionDisabled('enable', 'pw', '', false)).toBe(false)
    expect(methodActionDisabled('disable', 'pw', '12345', false)).toBe(true)
    expect(methodActionDisabled('disable', 'pw', '123456', false)).toBe(false)
    expect(methodActionDisabled('regenerate', 'pw', '123456', false)).toBe(false)
    expect(methodActionDisabled('regenerate', 'pw', '123456', true)).toBe(true)
  })

  it('treats three remaining recovery codes as not-low — the warning is a strict <', () => {
    expect(LOW_RECOVERY_CODES).toBe(3)
  })
})
