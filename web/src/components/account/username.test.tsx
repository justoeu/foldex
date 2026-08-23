import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { AccountPage } from '../../pages/AccountPage'
import { renderWithProviders, testAdminSession, testAdminUser } from '../../test/renderWithProviders'
import { freshState, installAxiosMock, type MockState } from '../../test/server'
import { http } from '../../api/client'
import type { AuthUser, SessionState } from '../../auth/types'

let state: MockState

beforeEach(() => {
  state = freshState()
  installAxiosMock(state)
})
afterEach(() => vi.restoreAllMocks())

function sessionWith(over: Partial<AuthUser>): SessionState {
  return {
    status: 'authenticated',
    user: { ...testAdminUser, ...over },
    csrfToken: 'test-csrf-token',
    features: { google_oauth: false, two_factor: true, email_delivery: true },
  }
}

/** The username lives in Access now, not on the profile form — it is an
 *  identifier, typed into the login screen where the e-mail goes. */
function render(over: Partial<AuthUser> = {}) {
  return renderWithProviders(<AccountPage initialTab="access" />, { session: sessionWith(over) })
}

/** Scoped to its own row, so a query cannot match the e-mail row's field or
 *  the profile panel's display name one rail item over. */
function row() {
  return within(screen.getByRole('group', { name: /username/i }))
}

function mockPatch(over: Record<string, unknown> = {}) {
  const session = testAdminSession as { user: object; features: object }
  return vi.spyOn(http, 'patch').mockResolvedValue({
    data: {
      status: 'authenticated',
      user: { ...session.user, ...over },
      csrfToken: 'test-csrf-token',
      features: session.features,
    },
  } as never)
}

describe('the account username', () => {
  it('sits with the sign-in methods, not on the profile form', async () => {
    render()
    expect(screen.getByRole('group', { name: /username/i })).toBeInTheDocument()

    // ...and is gone from the profile panel, so one identifier has exactly one
    // place that edits it.
    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: /profile/i }))
    await waitFor(() => expect(screen.getByLabelText(/display name/i)).toBeInTheDocument())
    expect(screen.queryByLabelText(/username/i)).not.toBeInTheDocument()
  })

  // The hint says what happens INSTEAD, not merely that the field is empty:
  // "Not set" alone leaves the reader wondering whether they can still sign in.
  // Matched on the full sentence because the state badge beside it also reads
  // "Not set".
  it('says how you sign in while none is set', () => {
    render()
    expect(row().getByText(/not set — you sign in with your e-mail/i)).toBeInTheDocument()
  })

  // The whole point of the dedicated endpoint: every field on PATCH /profile is
  // tri-state, so replaying a cached display name would revert a rename made in
  // another tab.
  it('saves the username ALONE, sending no name and no locale', async () => {
    const patch = mockPatch({ username: 'valmir' })
    render()
    const user = userEvent.setup()

    await user.click(row().getByRole('button', { name: /set a username/i }))
    await user.type(row().getByLabelText(/username/i), 'valmir')
    await user.click(row().getByRole('button', { name: /^save$/i }))

    await waitFor(() =>
      expect(patch).toHaveBeenCalledWith('/api/auth/profile', { username: 'valmir' }))
  })

  it('offers removing it, which is what makes it optional in both directions', async () => {
    const patch = mockPatch({ username: '' })
    render({ username: 'existing' })
    const user = userEvent.setup()

    expect(row().getByText('existing')).toBeInTheDocument()
    await user.click(row().getByRole('button', { name: /edit/i }))
    await user.click(row().getByRole('button', { name: /remove/i }))

    await waitFor(() =>
      expect(patch).toHaveBeenCalledWith('/api/auth/profile', { username: '' }))
  })

  it('says the shape rule when the server refuses the value', async () => {
    vi.spyOn(http, 'patch').mockRejectedValue({
      response: { status: 400, data: { error: { code: 'invalid_username' } } },
    })
    render()
    const user = userEvent.setup()

    await user.click(row().getByRole('button', { name: /set a username/i }))
    await user.type(row().getByLabelText(/username/i), 'no')
    await user.click(row().getByRole('button', { name: /^save$/i }))

    expect(await row().findByText(/3 to 32 characters/i)).toBeInTheDocument()
  })

  // 409 and 400 are different problems with different fixes: one needs another
  // name, the other needs a different shape of name.
  it('says when the username is taken', async () => {
    vi.spyOn(http, 'patch').mockRejectedValue({
      response: { status: 409, data: { error: { code: 'username_taken' } } },
    })
    render()
    const user = userEvent.setup()

    await user.click(row().getByRole('button', { name: /set a username/i }))
    await user.type(row().getByLabelText(/username/i), 'taken')
    await user.click(row().getByRole('button', { name: /^save$/i }))

    expect(await row().findByText(/already in use/i)).toBeInTheDocument()
  })
})

describe('signing in', () => {
  // `type="email"` would make the browser refuse to submit anything without an
  // `@`, blocking a valid username with a validation bubble the server never
  // gets to answer.
  it('accepts a username in the identifier field', async () => {
    const { LoginScreen } = await import('../auth/LoginScreen')
    renderWithProviders(<LoginScreen onForgotPassword={() => {}} />, { session: null })

    const field = screen.getByRole('textbox', { name: /e-mail or username/i })
    expect(field).toHaveAttribute('type', 'text')
    expect(field).toHaveAttribute('autocomplete', 'username')
  })
})
