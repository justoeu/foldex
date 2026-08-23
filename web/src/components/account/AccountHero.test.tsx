import { describe, it, expect } from 'vitest'
import { screen, within } from '@testing-library/react'
import { AccountHero } from './AccountHero'
import { renderWithProviders, testAdminUser } from '../../test/renderWithProviders'
import type { AuthUser } from '../../auth/types'

function hero(over: Partial<AuthUser> = {}) {
  renderWithProviders(<AccountHero user={{ ...testAdminUser, ...over } as AuthUser} />)
  // eslint-disable-next-line testing-library/no-node-access
  return within(screen.getByRole('heading', { level: 2 }).closest('header') as HTMLElement)
}

/**
 * The chips are the reason the hero exists: the old account screen showed a
 * card with one line of status, so the state of the account was the one thing
 * the account screen did not tell you.
 */
describe('AccountHero', () => {
  it('reports a password and no second factor', () => {
    const h = hero({ has_password: true, totp_enabled: false, email_2fa_enabled: false })
    expect(h.getByText(/password set/i)).toBeInTheDocument()
    expect(h.getByText(/two-factor off/i)).toBeInTheDocument()
  })

  it('reports a missing password', () => {
    expect(hero({ has_password: false }).getByText(/no password/i)).toBeInTheDocument()
  })

  // The OR, not the authenticator alone — an e-mail-only factor is a factor.
  it('reports two-factor on for an e-mail-only factor', () => {
    const h = hero({ totp_enabled: false, email_2fa_enabled: true })
    expect(h.getByText(/two-factor on/i)).toBeInTheDocument()
  })

  it('names the role', () => {
    expect(hero({ role: 'owner' }).getByText(/^owner$/i)).toBeInTheDocument()
  })

  // An account can legitimately have no display name.
  it('falls back to the e-mail when there is no name', () => {
    renderWithProviders(<AccountHero user={{ ...testAdminUser, name: '' } as AuthUser} />)
    expect(screen.getByRole('heading', { name: testAdminUser.email })).toBeInTheDocument()
  })
})
