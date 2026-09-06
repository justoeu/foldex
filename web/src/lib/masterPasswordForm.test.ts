import { describe, it, expect } from 'vitest'
import { validateMasterForm, validateMasterRemove } from './masterPasswordForm'

describe('MasterPasswordFormCharter', () => {
  it('first-set accepts a matching password and optional hint', () => {
    const result = validateMasterForm({
      next: 'super-secret-master',
      confirm: 'super-secret-master',
      hint: 'my old street',
      configured: false,
      current: '',
    })
    expect(result).toEqual({
      ok: true,
      payload: { password: 'super-secret-master', hint: 'my old street' },
    })
  })

  it('change includes the current password and omits an empty hint', () => {
    const result = validateMasterForm({
      next: 'brand-new-master',
      confirm: 'brand-new-master',
      hint: '  ',
      configured: true,
      current: 'original-master',
    })
    expect(result).toEqual({
      ok: true,
      payload: { password: 'brand-new-master', currentPassword: 'original-master' },
    })
  })

  it('remove requires the current password', () => {
    expect(validateMasterRemove('')).toEqual({ ok: false, errorKey: 'settings.master_wrong_current' })
    expect(validateMasterRemove('original-master')).toEqual({
      ok: true,
      payload: { currentPassword: 'original-master' },
    })
  })

  it('INV-067: hint must never equal the password', () => {
    const result = validateMasterForm({
      next: 'super-secret-master',
      confirm: 'super-secret-master',
      hint: 'SUPER-SECRET-MASTER',
      configured: false,
      current: '',
    })
    expect(result).toEqual({ ok: false, errorKey: 'settings.master_hint_equals' })
  })

  it('wrong-current still ships currentPassword so the API can reject it', () => {
    const result = validateMasterForm({
      next: 'brand-new-master',
      confirm: 'brand-new-master',
      hint: '',
      configured: true,
      current: 'not-the-one',
    })
    expect(result.ok).toBe(true)
    if (result.ok) expect(result.payload.currentPassword).toBe('not-the-one')
  })

  it('rejects a too-short password and a mismatch', () => {
    expect(validateMasterForm({
      next: 'short',
      confirm: 'short',
      hint: '',
      configured: false,
      current: '',
    })).toEqual({ ok: false, errorKey: 'settings.master_too_short' })

    expect(validateMasterForm({
      next: 'super-secret-master',
      confirm: 'different-value',
      hint: '',
      configured: false,
      current: '',
    })).toEqual({ ok: false, errorKey: 'settings.master_mismatch' })
  })

  it('configured change without current fails closed', () => {
    expect(validateMasterForm({
      next: 'brand-new-master',
      confirm: 'brand-new-master',
      hint: '',
      configured: true,
      current: '',
    })).toEqual({ ok: false, errorKey: 'settings.master_wrong_current' })
  })
})
