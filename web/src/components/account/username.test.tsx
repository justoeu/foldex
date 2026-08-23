import { describe, it, expect, vi, beforeEach } from 'vitest'
import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { AccountPage } from '../../pages/AccountPage'
import { renderWithProviders, testAdminSession, testAdminUser } from '../../test/renderWithProviders'
import { freshState, installAxiosMock, type MockState } from '../../test/server'
import { http } from '../../api/client'

let state: MockState

beforeEach(() => {
  state = freshState()
  installAxiosMock(state)
})

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
  it('saves a chosen username alongside the name it did not change', async () => {
    const patch = mockPatch({ username: 'valmir' })
    renderWithProviders(<AccountPage />)
    const user = userEvent.setup()

    await user.type(screen.getByLabelText(/username/i), 'valmir')
    await user.click(screen.getByRole('button', { name: /save changes/i }))

    await waitFor(() =>
      expect(patch).toHaveBeenCalledWith('/api/auth/profile', {
        name: 'Test Admin',
        username: 'valmir',
      }))
  })

  // Every field on this form is tri-state server-side. Sending one that did not
  // change replays a cached value over whatever another tab wrote since this
  // screen loaded — the exact hazard the locale field already guards against.
  it('sends the username only when it changed', async () => {
    const patch = mockPatch({ name: 'New Name' })
    renderWithProviders(<AccountPage />)
    const user = userEvent.setup()

    const name = screen.getByLabelText(/display name/i)
    await user.clear(name)
    await user.type(name, 'New Name')
    await user.click(screen.getByRole('button', { name: /save changes/i }))

    await waitFor(() =>
      expect(patch).toHaveBeenCalledWith('/api/auth/profile', { name: 'New Name' }))
  })

  it('says the shape rule when the server refuses the value', async () => {
    vi.spyOn(http, 'patch').mockRejectedValue({
      response: { status: 400, data: { error: { code: 'invalid_username' } } },
    })
    renderWithProviders(<AccountPage />)
    const user = userEvent.setup()

    await user.type(screen.getByLabelText(/username/i), 'no')
    await user.click(screen.getByRole('button', { name: /save changes/i }))

    expect(await screen.findByText(/3 to 32 characters/i)).toBeInTheDocument()
  })

  // 409 and 400 are different problems with different fixes: one needs another
  // name, the other needs a different shape of name.
  it('says when the username is taken', async () => {
    vi.spyOn(http, 'patch').mockRejectedValue({
      response: { status: 409, data: { error: { code: 'username_taken' } } },
    })
    renderWithProviders(<AccountPage />)
    const user = userEvent.setup()

    await user.type(screen.getByLabelText(/username/i), 'taken')
    await user.click(screen.getByRole('button', { name: /save changes/i }))

    expect(await screen.findByText(/already in use/i)).toBeInTheDocument()
  })

  it('offers clearing it, which is what makes it optional in both directions', async () => {
    const patch = mockPatch({ username: '' })
    renderWithProviders(<AccountPage />, {
      session: {
        status: 'authenticated',
        user: { ...testAdminUser, username: 'existing' },
        csrfToken: 'test-csrf-token',
        features: { google_oauth: false, two_factor: true, email_delivery: true },
      },
    })
    const user = userEvent.setup()

    const field = screen.getByLabelText(/username/i)
    expect(field).toHaveValue('existing')
    await user.clear(field)
    await user.click(screen.getByRole('button', { name: /save changes/i }))

    await waitFor(() =>
      expect(patch).toHaveBeenCalledWith('/api/auth/profile', {
        name: 'Test Admin',
        username: '',
      }))
  })

  it('says it is optional and what it is for', () => {
    renderWithProviders(<AccountPage />)
    expect(screen.getByText(/optional\. if you set one/i)).toBeInTheDocument()
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
