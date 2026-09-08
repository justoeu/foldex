import { describe, it, expect } from 'vitest'
import { hasSecondFactor } from './types'
import typesSrc from './types.ts?raw'

/**
 * The client mirror of the server's `HasSecondFactor()`. It feeds the settings
 * tile, the account hero and the password form, and those three had already
 * disagreed once — the tile read the authenticator alone, so an e-mail-only
 * account saw "two-factor off" and "two-factor on" one click apart.
 */
describe('hasSecondFactor', () => {
  it.each([
    [{ totp_enabled: false, email_2fa_enabled: false }, false],
    [{ totp_enabled: true, email_2fa_enabled: false }, true],
    [{ totp_enabled: false, email_2fa_enabled: true }, true],
    [{ totp_enabled: true, email_2fa_enabled: true }, true],
  ])('is the OR of the two factors (%o)', (user, expected) => {
    expect(hasSecondFactor(user)).toBe(expected)
  })

  // email_2fa_enabled is optional on the wire (pre-ADR-37 payloads omit it),
  // and absent must read as "no factor", never as one the account cannot use.
  it('treats an absent e-mail factor as no factor', () => {
    expect(hasSecondFactor({ totp_enabled: false })).toBe(false)
    expect(hasSecondFactor({ totp_enabled: true })).toBe(true)
  })
})

describe('canMailStepUpCode', () => {
  it('is gone; callers read email_2fa_enabled directly', () => {
    expect(typesSrc).not.toMatch(/function canMailStepUpCode/)
  })
})
